package authz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The structural guard that makes SystemPrincipal safe to exist.
//
// A runtime check cannot prove that an HTTP handler does not mint one of these.
// This can: it reads every package's source and fails the build if
// SystemPrincipal is called anywhere but the short list of files that register
// scheduled work. Same technique as egress.TestNoPackageDialsDirectly and
// library.TestNothingWritesOutsideAVault, for the same reason.
func TestOnlySchedulingCodeCanMintASystemPrincipal(t *testing.T) {
	// The allowlist. Adding to it should feel like a decision, because it is.
	allowed := map[string]bool{
		// Where scheduled work is registered, so the grant is visible beside
		// the task that receives it.
		filepath.Join("..", "..", "cmd", "cmediastack", "main.go"): true,
		// The declaration itself.
		filepath.Join("..", "..", "internal", "authz", "system.go"): true,
	}

	var offenders []string
	// A file this guard cannot read fails it: skipping would be exactly how an
	// offender went unseen.
	err := filepath.Walk(filepath.Join("..", ".."), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if !strings.Contains(stripComments(string(body)), "SystemPrincipal(") {
			return nil
		}
		if !allowed[p] {
			offenders = append(offenders, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("%s mints a system principal. Background authority must be "+
			"granted where scheduled work is registered, not deep in a package "+
			"where nobody reviewing a handler would see it", o)
	}
}

// Each task's grant is exactly what it needs, asserted exhaustively rather than
// trusted — a permission added to systemGrants by accident must fail here.
func TestEachBackgroundTaskHoldsExactlyItsOwnGrant(t *testing.T) {
	want := map[SystemTask][]Permission{
		TaskImport:      {PermBrowse, PermManageRootFolders},
		TaskLibraryScan: {PermBrowse},
		TaskTrashPurge:  {PermBrowse, PermDeleteMediaFiles},
		TaskIdentify:    {PermBrowse},
		// Browse only. A retention task holding PermDeleteMediaFiles would be
		// one typo away from being a library-deletion task.
		TaskPlaybackPurge: {PermBrowse},
		// Browse only. Holding PermEditLibraryItems would let a background
		// refresh re-monitor a season somebody deliberately stopped following.
		TaskEpisodeRefresh: {PermBrowse},
		TaskAcquire:        {PermBrowse, PermInteractiveSearch, PermManageQueue},
		// The audit log, read, and nothing else: not even the settings it
		// sends by.
		TaskNotify: {PermViewAuditLog},
		// Browse only: a rating is a fact about a title. Holding
		// PermEditLibraryItems would let it overwrite a person's.
		TaskRatings: {PermBrowse},
		// Browse only, like the episode refresh.
		TaskMusicRefresh: {PermBrowse},
		TaskSubtitles:    {PermBrowse},
	}

	for task, allowed := range want {
		ctx := SystemPrincipal(context.Background(), task)
		allow := make(map[Permission]bool, len(allowed))
		for _, p := range allowed {
			allow[p] = true
			if err := RequirePermission(ctx, p); err != nil {
				t.Errorf("%s cannot %s: %v", task, p, err)
			}
		}
		for _, perm := range AllPermissions {
			if allow[perm] {
				continue
			}
			if err := RequirePermission(ctx, perm); err == nil {
				t.Errorf("%s holds %s, which it should not", task, perm)
			}
		}
	}

	// Every declared task is covered by this test. A new one added without a
	// line here would otherwise go unasserted.
	if len(want) != len(systemGrants) {
		t.Errorf("systemGrants has %d tasks, this test checks %d", len(systemGrants), len(want))
	}
}

// The importer must not be able to unlink. Its whole reversible-upgrade design
// rests on that: an upgrade MOVES the file it replaces into trash rather than
// deleting it, and with destroy authority it could simply unlink instead.
func TestTheImporterCannotDestroyMediaBytes(t *testing.T) {
	ctx := SystemPrincipal(context.Background(), TaskImport)
	if err := RequireEffect(ctx, EffectDestroyMediaBytes, "/media/x.mkv"); err == nil {
		t.Error("the importer can destroy media bytes")
	}
	// But it can move paths, which is how a superseded file reaches trash.
	if err := RequireEffect(ctx, EffectMutateLibraryPaths, "/media/x.mkv"); err != nil {
		t.Errorf("the importer cannot move library paths: %v", err)
	}
}

// A scan changes NOTHING on disk — not even a rename — so it has no business
// holding an effect that could.
func TestTheScannerCanChangeNothingAtAll(t *testing.T) {
	ctx := SystemPrincipal(context.Background(), TaskLibraryScan)
	for _, eff := range []Effect{
		EffectDestroyMediaBytes, EffectMutateLibraryPaths,
		EffectEgressConfig, EffectGrantAccess, EffectReadSecret,
	} {
		if err := RequireEffect(ctx, eff, "/media/x.mkv"); err == nil {
			t.Errorf("the scanner holds %s", eff)
		}
	}
}

// The purge unlinks, and that is ALL it does: it does not move things around a
// library, so it does not hold the effect that would let it.
func TestThePurgeCanUnlinkAndNothingElse(t *testing.T) {
	ctx := SystemPrincipal(context.Background(), TaskTrashPurge)
	if err := RequireEffect(ctx, EffectDestroyMediaBytes, "/media/.trash/x.mkv"); err != nil {
		t.Errorf("the purge cannot unlink: %v", err)
	}
	for _, eff := range []Effect{
		EffectMutateLibraryPaths, EffectEgressConfig, EffectGrantAccess, EffectReadSecret,
	} {
		if err := RequireEffect(ctx, eff, "x"); err == nil {
			t.Errorf("the purge holds %s", eff)
		}
	}
}

// A typo in a task name must produce work that cannot act, never work that
// quietly inherits somebody else's authority.
func TestAnUnknownTaskGetsNothing(t *testing.T) {
	ctx := SystemPrincipal(context.Background(), SystemTask("system:typo"))
	for _, perm := range AllPermissions {
		if err := RequirePermission(ctx, perm); err == nil {
			t.Errorf("an unknown task holds %s", perm)
		}
	}
	if IsSystem(FromContext(ctx)) {
		t.Error("an unknown task is treated as recognised system work")
	}
}

// Background work must not be able to change who can do what.
func TestNoBackgroundTaskCanGrantAccessOrChangeEgress(t *testing.T) {
	for task := range systemGrants {
		ctx := SystemPrincipal(context.Background(), task)
		for _, eff := range []Effect{EffectGrantAccess, EffectEgressConfig, EffectReadSecret} {
			if err := RequireEffect(ctx, eff, "x"); err == nil {
				t.Errorf("%s holds %s", task, eff)
			}
		}
	}
}

func TestASystemPrincipalIsRecognisable(t *testing.T) {
	ctx := SystemPrincipal(context.Background(), TaskImport)
	if !IsSystem(FromContext(ctx)) {
		t.Error("a system principal is not recognised as one")
	}
	// A person is not, however they are named — the user id is what settles it.
	if IsSystem(&Principal{Username: string(TaskImport), UserID: 7}) {
		t.Error("a real account named like a system task was treated as system work")
	}
	if IsSystem(nil) {
		t.Error("nil is not system work")
	}
}

// stripComments removes block comments and whole-line // comments so a mention
// in prose does not fail the build.
func stripComments(src string) string {
	for {
		i := strings.Index(src, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(src[i:], "*/")
		if j < 0 {
			break
		}
		src = src[:i] + src[i+j+2:]
	}
	var out strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// The identification pass reads a library and writes proposals. It must not be
// able to rewrite what an operator browses to — that is the outcome ADR-0019 is
// built to prevent, and withholding the permission is stronger than intending
// not to use it.
func TestTheIdentificationPassCannotEditLibraryItems(t *testing.T) {
	ctx := SystemPrincipal(context.Background(), TaskIdentify)

	for _, perm := range []Permission{
		PermEditLibraryItems,  // rewriting a title
		PermManageRootFolders, // and therefore moving files
		PermDeleteMediaFiles,
		PermManageQueue, // causing an acquisition
	} {
		if err := RequirePermission(ctx, perm); err == nil {
			t.Errorf("the identification pass holds %s", perm)
		}
	}
	if err := RequirePermission(ctx, PermBrowse); err != nil {
		t.Errorf("the identification pass cannot read the library: %v", err)
	}
}

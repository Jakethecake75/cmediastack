package library

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// vaultFile is the one file permitted to name the raw filesystem calls. Every
// other file in this package, and every package that imports it to touch media
// files, must go through a Vault.
const vaultFile = "vault.go"

// hostFile is the other exception: operations on paths that no root contains,
// because the path IS the root and is not yet configured. See hostfs.go for why
// that is safe — the invariant there is provenance, not containment.
const hostFile = "hostfs.go"

// sourceFile contains paths the import reads FROM. Like vault.go it is the
// containment layer rather than a user of it, so it is allowed to name the
// calls it wraps.
const sourceFile = "source.go"

// A containment layer only works if nothing goes around it.
//
// This is the same technique as egress.TestNoPackageDialsDirectly, for the same
// reason: the failure that matters is not a check returning the wrong answer,
// it is a code path that never consults the check at all. One os.Rename in an
// importer, written by somebody thinking about episode numbering, is invisible
// in review because it looks exactly like ordinary Go — and it takes a
// stranger-supplied name straight to the filesystem.
//
// A runtime check cannot prove a code path does not exist. This can.
func TestNothingWritesOutsideAVault(t *testing.T) {
	// Packages that touch media files. Listed by name so adding one is a
	// deliberate act rather than something that happens by creating a folder.
	guarded := []string{".", "../importer", "../artwork"}

	banned := map[string]string{
		"os.Create":    "use Vault.Create; a raw path is not contained",
		"os.OpenFile":  "use Vault.Create or Vault.Open",
		"os.Rename":    "use Vault.Rename, which checks EffectMutateLibraryPaths",
		"os.Remove":    "use Vault.Remove, which checks EffectDestroyMediaBytes",
		"os.RemoveAll": "use Vault.RemoveAll, which checks EffectDestroyMediaBytes",
		"os.Mkdir":     "use Vault.MkdirAll",
		"os.MkdirAll":  "use Vault.MkdirAll",
		"os.WriteFile": "use Vault.Create",
		"os.Symlink":   "this software never creates symlinks in a library",
		"os.Truncate":  "truncating is destroying bytes; it needs the effect check",
		"os.Chmod":     "go through the vault",
		"os.Link":      "use Vault.Link, which verifies the result back through the root",
		// filepath.Join is the one that looks harmless and is not: it CLEANS,
		// so Join(root, "../../etc") is /etc, and every prefix check written
		// afterwards is a bug waiting for a symlink.
		"filepath.Join": "os.Root resolves paths; filepath.Join cleans and escapes",
	}

	for _, dir := range guarded {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // the package does not exist yet; it will
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if dir == "." && (name == vaultFile || name == hostFile || name == sourceFile) {
				continue // the files allowed to name them
			}
			// Platform shims for statfs. They read one number about a
			// directory and cannot write anything.
			if dir == "." && strings.HasPrefix(name, "freespace_") {
				continue
			}
			path := filepath.Join(dir, name)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := stripComments(string(body))
			for needle, why := range banned {
				if strings.Contains(src, needle) {
					t.Errorf("%s uses %s: %s", path, needle, why)
				}
			}
		}
	}
}

// Even the permitted file must not reach for the operations that have no safe
// form here.
func TestEvenTheVaultDoesNotCreateSymlinks(t *testing.T) {
	body, err := os.ReadFile(vaultFile)
	if err != nil {
		t.Fatal(err)
	}
	src := stripComments(string(body))
	for _, needle := range []string{"os.Symlink", "os.Chown", "root.Symlink"} {
		if strings.Contains(src, needle) {
			t.Errorf("%s uses %s, which this software never needs", vaultFile, needle)
		}
	}
}

// Every destructive method must consult authz. A new one added without the
// check is the exact mistake this whole design exists to prevent, and it would
// be invisible in review.
func TestEveryDestructiveMethodChecksAnEffect(t *testing.T) {
	body, err := os.ReadFile(vaultFile)
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	// Every method that unlinks, moves or overwrites. A new one added without a
	// line here would be unasserted, so the list itself is checked below.
	destructive := []string{"Remove", "RemoveAll", "Rename", "Supersede", "PurgeTrash"}
	for _, method := range destructive {
		marker := "func (v *Vault) " + method + "("
		i := strings.Index(src, marker)
		if i < 0 {
			t.Fatalf("Vault.%s no longer exists; this test needs updating", method)
		}
		// The body runs to the next top-level func.
		rest := src[i+len(marker):]
		if j := strings.Index(rest, "\nfunc "); j >= 0 {
			rest = rest[:j]
		}
		if !strings.Contains(rest, "authz.RequireEffect") {
			t.Errorf("Vault.%s does not call authz.RequireEffect", method)
		}
	}

	// A method that WRITES through the root but is not in the list above is one
	// nobody has decided the authority for. Caught here rather than in review.
	//
	// removeOwn is the documented exception: it deletes a file this package
	// created moments ago that nothing else has ever referred to — a write
	// probe, a half-written copy — so there is no media byte to destroy.
	writers := []string{"root.Remove(", "root.RemoveAll(", "root.Rename("}
	allowed := map[string]bool{"removeOwn": true}
	for _, method := range destructive {
		allowed[method] = true
	}
	for _, fn := range topLevelVaultMethods(src, "func (v *Vault) ") {
		if allowed[fn.name] {
			continue
		}
		for _, w := range writers {
			if strings.Contains(fn.body, w) {
				t.Errorf("Vault.%s calls %s but is not in the destructive list, "+
					"so nothing has decided what authority it needs", fn.name, w)
			}
		}
	}

	// Cache lives in the same file and writes through a root too, and the check
	// above cannot see it: it matches on the Vault receiver, so a writer added
	// with a different receiver in the same file would be silently unasserted.
	// That is the exact shape of gap this test exists to close, so the rule is
	// extended rather than the type exempted.
	//
	// A Cache writer needs NO effect, deliberately — see the type's doc comment:
	// it holds only bytes this software fetched and can fetch again, so there is
	// no media byte to destroy and nothing to make reversible. The allowlist is
	// therefore the whole set of Cache methods that write, named one by one, so
	// that adding one is a decision somebody made rather than a receiver name
	// that happened not to match.
	cacheWriters := map[string]string{
		"Create":  "creates a file this software owns",
		"Replace": "writes to a .part name and renames it over the target",
		"Remove":  "deletes a re-downloadable file; refuses the marker",
	}
	for _, fn := range topLevelVaultMethods(src, "func (c *Cache) ") {
		writes := false
		for _, w := range append(writers, "root.Create(", "root.MkdirAll(") {
			if strings.Contains(fn.body, w) {
				writes = true
			}
		}
		if writes && cacheWriters[fn.name] == "" {
			t.Errorf("Cache.%s writes through the root but is not one of the "+
				"methods declared to do so. A Cache write needs no permission, "+
				"which is exactly why a new one must be a deliberate addition "+
				"rather than something nobody noticed", fn.name)
		}
	}
}

type vaultMethod struct{ name, body string }

// topLevelVaultMethods splits the vault's source into the methods with the
// given receiver marker.
func topLevelVaultMethods(src, marker string) []vaultMethod {
	var out []vaultMethod
	for i := 0; ; {
		j := strings.Index(src[i:], marker)
		if j < 0 {
			break
		}
		start := i + j + len(marker)
		name := src[start:]
		if k := strings.IndexAny(name, "("); k >= 0 {
			name = name[:k]
		}
		body := src[start:]
		if k := strings.Index(body, "\nfunc "); k >= 0 {
			body = body[:k]
		}
		out = append(out, vaultMethod{name: name, body: body})
		i = start
	}
	return out
}

// stripComments removes block comments and whole-line // comments, so a needle
// mentioned in prose does not fail the build. Deliberately not a Go parser: it
// only has to be conservative in the direction that matters, which is never
// hiding real code.
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

// The deployment must mount the library writable.
//
// # Why this is a test and not a comment
//
// docker-compose.yml mounted it `:ro`, with a comment saying "Read-only until
// Phase 3 needs to write imports; when that changes, narrow it". Phase 3
// landed. Nobody changed it. The comment had correctly predicted the bug and
// that prevented nothing.
//
// It is not a degraded mode. A root folder cannot even be ADDED to a read-only
// mount — RootStore.Create validates one by writing a probe file — so the
// shipped deployment failed at the first thing an operator does, with an
// instance that looked configured and could hold no library at all.
//
// The library is not an archive this application only reads. It imports into it,
// renames on import, and moves a deletion into a trash directory INSIDE the
// root rather than unlinking it (ADR-0016) — which is the only reason a delete
// is reversible.
func TestTheDeploymentMountsTheLibraryWritable(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}

	// Only the volume lines, and only the one naming the media mount: /config
	// and any future read-only mount are not this rule's business.
	checked := 0
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") || !strings.Contains(trimmed, ":/media") {
			continue
		}
		checked++
		if strings.HasSuffix(trimmed, ":ro") {
			t.Errorf("the library is mounted read-only:\n\t%s\n"+
				"A root folder cannot be added to a read-only mount: Create "+
				"validates one by writing a probe file. Imports, renames and "+
				"the trash that makes a delete reversible are all writes too",
				trimmed)
		}
	}
	if checked == 0 {
		t.Error("no /media mount found in docker-compose.yml; either the " +
			"library mount was renamed and this test now checks nothing, or " +
			"the deployment no longer mounts a library at all")
	}
}

// The container healthcheck must probe the running instance, not lint its
// configuration.
//
// It ran `--check`, which validates configuration and passes while the process
// is wedged, its listener is dead or its database has gone away — so the
// container reported healthy forever and `restart: unless-stopped` never
// restarted it. It also named a config file the compose deployment does not
// create, so it passed unconditionally: verified by SIGSTOPping the process,
// which `--check` called healthy and `--healthcheck` fails.
func TestTheContainerHealthcheckProbesTheInstance(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)

	i := strings.Index(src, "healthcheck:")
	if i < 0 {
		t.Fatal("docker-compose.yml has no healthcheck")
	}
	// The test: line within that block.
	block := src[i:]
	j := strings.Index(block, "test:")
	if j < 0 {
		t.Fatal("the healthcheck has no test")
	}
	line := block[j:]
	if k := strings.IndexByte(line, '\n'); k > 0 {
		line = line[:k]
	}

	if !strings.Contains(line, "--healthcheck") {
		t.Errorf("the healthcheck does not probe the instance:\n\t%s", strings.TrimSpace(line))
	}
	if strings.Contains(line, "--check") && !strings.Contains(line, "--healthcheck") {
		t.Error("the healthcheck runs --check, which validates configuration. " +
			"A wedged process has valid configuration, so this reports healthy " +
			"forever and the restart policy never fires")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

// No episode row is ever created from a file.
//
// # The rule, and why a comment is not enough to keep it
//
// An episode exists because a PROVIDER said it exists. A season assembled from
// the files an instance holds is 100% complete by construction, and a "missing
// episodes" list built the same way is always empty — software that confidently
// tells an operator they have everything, when what it means is "here is a list
// of the things I have", is worse than software that says nothing (ADR-0022).
//
// The way that rule breaks is not malice and does not look wrong in review.
// Somebody writing the scan, or the importer, reaches for "while I am here, I
// know this file is S02E04, I may as well make sure the episode row exists" —
// one INSERT, entirely reasonable-looking, and the wanted list is quietly
// wrong forever afterwards. A runtime check cannot prove that code path does
// not exist. This can.
func TestNoEpisodeIsEverCreatedFromAFile(t *testing.T) {
	root := moduleRoot(t)

	// The one file permitted to write these tables. The refresher in internal/tv
	// assembles what it writes but contains no SQL of its own, so it is not
	// here: an allowlist entry that is not needed is where a real violation
	// goes to hide.
	permitted := map[string]string{
		"internal/library/episodes.go": "the store; Upsert is the only writer and " +
			"its input type carries provider fields and nothing else",
	}

	writes := regexp.MustCompile(`(?i)(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+"?(season|episode)"?\b`)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "migrations":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, ok := permitted[rel]; ok {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range writes.FindAllString(string(b), -1) {
			t.Errorf("%s writes the episode tables: %q\n"+
				"An episode exists because a provider said so. A row created "+
				"anywhere else — from a filename, a release name, or a file on "+
				"disk — makes every season complete and every wanted list empty "+
				"(ADR-0022). Go through library.EpisodeStore.Upsert", rel, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// And the permitted files really do exist, so a rename does not turn this
	// into a test that walks a tree and finds nothing by construction.
	for rel := range permitted {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s is in the allowlist and does not exist: %v", rel, err)
		}
	}
}

// The storage layer imports nothing above it.
//
// This package is vaults, root folders and the stores that sit on them. It
// imported authz and platform/db and nothing else — until the episode
// refresher was written here and pulled in internal/metadata, inverting the
// layering: storage depending on the provider package. Nothing failed at the
// time. It surfaced a day later as an import cycle in metadata's live tests,
// which happen to use library.OpenCache.
//
// A cycle is the lucky outcome. The unlucky one is that it compiles, and every
// later change to the provider package drags the storage layer along with it.
// The refresher now lives in internal/tv, above both, and this keeps it there.
func TestTheStorageLayerImportsNothingAboveIt(t *testing.T) {
	allowed := map[string]bool{
		"github.com/jakethecake75/cmediastack/internal/authz":       true,
		"github.com/jakethecake75/cmediastack/internal/platform/db": true,
	}
	root := moduleRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "internal", "library"))
	if err != nil {
		t.Fatal(err)
	}
	importLine := regexp.MustCompile(`"(github.com/jakethecake75/cmediastack/[^"]+)"`)
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		b, err := os.ReadFile(filepath.Join(root, "internal", "library", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range importLine.FindAllStringSubmatch(string(b), -1) {
			if !allowed[m[1]] {
				t.Errorf("internal/library/%s imports %s.\n"+
					"The storage layer depends on authz and the database and "+
					"nothing above them. Orchestration that needs both storage "+
					"and something higher belongs in a package above library — "+
					"see internal/tv for the case that prompted this", name, m[1])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files found in internal/library; this test is reading the wrong directory")
	}
}

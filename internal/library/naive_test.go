package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// naiveOpen is the implementation this package exists to avoid: sanitise, join,
// then check the prefix. It is what most software does, and it is kept here
// deliberately so the difference is measured rather than claimed.
func naiveOpen(root, name string) (*os.File, error) {
	p := filepath.Join(root, name)
	if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(root)) {
		return nil, os.ErrPermission
	}
	return os.Open(p)
}

// A differential test: for every path the naive implementation lets through,
// the Vault must refuse it.
//
// The point is not that Join+prefix is sloppy — it is that it is the OBVIOUS
// implementation, it looks correct, and it reads /etc/passwd through a symlink
// anybody with write access to the library can plant. If this test ever reports
// that the naive version contained everything, the harness has stopped
// exercising the case and the comparison is worthless, so that fails too.
func TestTheVaultRefusesWhatTheObviousImplementationAllows(t *testing.T) {
	r := newRig(t)

	attempts := []string{
		"../outside/secret.txt",
		"escape/secret.txt",
		"etc/passwd",
		"./escape/./secret.txt",
	}

	leaked := 0
	for _, name := range attempts {
		f, naiveErr := naiveOpen(r.root, name)
		if naiveErr == nil {
			_ = f.Close()
			leaked++
			t.Logf("the obvious implementation opened %q", name)
		}
		// Whatever the naive one did, ours refuses.
		if _, err := r.vault.Open(name); !errors.Is(err, ErrEscapes) {
			t.Errorf("the vault did not refuse %q: %v", name, err)
		}
	}

	if leaked == 0 {
		t.Fatal("the obvious implementation contained every path, so this " +
			"comparison proves nothing — the symlink fixtures are not working")
	}
	t.Logf("the obvious Join+prefix implementation leaked %d of %d; the vault leaked 0",
		leaked, len(attempts))
}

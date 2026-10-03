package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The property that makes SafePath safe: a sanitised component can never
// contain a separator, so no input can produce more components than the caller
// passed. Everything else in this file is detail.
func TestASanitisedComponentIsNeverMoreThanOneComponent(t *testing.T) {
	for _, in := range []string{
		"../../etc/cron.d/x",
		"a/b/c",
		`a\b\c`,
		"/absolute",
		"..",
		"....//....//etc",
		"dir/",
		"/",
		`C:\Windows\System32`,
		"a/../../../b",
	} {
		out, err := SafeComponent(in)
		if err != nil {
			continue // refusing outright is also fine
		}
		if strings.ContainsAny(out, `/\`) {
			t.Errorf("SafeComponent(%q) = %q, which is more than one component", in, out)
		}
		if out == "." || out == ".." || out == "" {
			t.Errorf("SafeComponent(%q) = %q", in, out)
		}
	}
}

// And the same property through a whole path: components in, components out.
func TestSafePathCannotGrowMoreComponentsThanItWasGiven(t *testing.T) {
	for _, in := range [][]string{
		{"Movies", "../../etc", "film.mkv"},
		{"a/b", "c/d"},
		{"..", "..", ".."},
	} {
		out, err := SafePath(in...)
		if err != nil {
			continue
		}
		if got := len(strings.Split(out, "/")); got > len(in) {
			t.Errorf("SafePath(%q) = %q: %d components from %d inputs", in, out, got, len(in))
		}
	}
}

func TestControlCharactersAndInvalidUTF8AreRemoved(t *testing.T) {
	for _, in := range []string{
		"film\x00.mkv",
		"film\r\n.mkv",
		"film\x1b[31m.mkv",
		"film\xff\xfe.mkv",
		"film\u200e.mkv", // left-to-right mark, a Unicode control
	} {
		out, err := SafeComponent(in)
		if err != nil {
			continue
		}
		if !utf8.ValidString(out) {
			t.Errorf("SafeComponent(%q) = %q, which is not valid UTF-8", in, out)
		}
		for _, r := range out {
			if r < 0x20 || r == 0x7f {
				t.Errorf("SafeComponent(%q) = %q, which still has a control character", in, out)
			}
		}
	}
}

// Windows strips trailing dots and spaces silently, so "Film." and "Film"
// become the same file there and one overwrites the other.
func TestTrailingDotsAndSpacesAreTrimmed(t *testing.T) {
	for in, want := range map[string]string{
		"Film.":   "Film",
		"Film ":   "Film",
		"Film. .": "Film",
		" Film":   "Film",
	} {
		got, err := SafeComponent(in)
		if err != nil {
			t.Fatalf("SafeComponent(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("SafeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

// A file called CON cannot exist on Windows even today. CMediaStack runs on
// Linux, but an operator's library outlives the software that filled it.
func TestWindowsReservedNamesAreEscaped(t *testing.T) {
	for _, in := range []string{"CON", "con", "NUL.mkv", "com1", "LPT9.txt", "aux"} {
		out, err := SafeComponent(in)
		if err != nil {
			t.Fatalf("SafeComponent(%q): %v", in, err)
		}
		stem := out
		if i := strings.IndexByte(stem, '.'); i > 0 {
			stem = stem[:i]
		}
		if _, bad := windowsReserved[strings.ToUpper(stem)]; bad {
			t.Errorf("SafeComponent(%q) = %q, still a reserved device name", in, out)
		}
	}
	// A name that merely CONTAINS one is left alone.
	if out, _ := SafeComponent("Contact (2007)"); out != "Contact (2007)" {
		t.Errorf("a name containing a reserved word was mangled: %q", out)
	}
}

// A title in its own script is the correct name for it. Transliterating to
// ASCII is the kind of helpfulness that produces a library nobody can search.
func TestUnicodeTitlesSurvive(t *testing.T) {
	for _, in := range []string{
		"千と千尋の神隠し (2001)",
		"Amélie (2001)",
		"Сталкер (1979)",
		"Låt den rätte komma in (2008)",
	} {
		out, err := SafeComponent(in)
		if err != nil {
			t.Fatalf("SafeComponent(%q): %v", in, err)
		}
		if out != in {
			t.Errorf("SafeComponent(%q) = %q — a legitimate title was altered", in, out)
		}
	}
}

// 255 BYTES, not characters: a Japanese title reaches the limit in 85
// characters, and a name cut mid-rune is invalid UTF-8 on disk.
func TestLongNamesAreCutOnARuneBoundary(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("a", 1000),
		strings.Repeat("あ", 400),
		strings.Repeat("🎬", 200),
	} {
		out, err := SafeComponent(in)
		if err != nil {
			t.Fatalf("SafeComponent(len %d): %v", len(in), err)
		}
		if len(out) > MaxComponentBytes {
			t.Errorf("result is %d bytes, over the %d limit", len(out), MaxComponentBytes)
		}
		if !utf8.ValidString(out) {
			t.Errorf("a long name was cut mid-rune: %q", out)
		}
	}
}

// A media file that loses its extension stops being playable by anything that
// dispatches on it. Losing the tail of a long title is cosmetic; losing ".mkv"
// is functional.
func TestAVeryLongTitleKeepsItsExtension(t *testing.T) {
	got, err := PreserveExtension(strings.Repeat("あ", 400), ".mkv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, ".mkv") {
		t.Errorf("the extension was lost: %q", got)
	}
	if len(got) > MaxComponentBytes {
		t.Errorf("result is %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("cut mid-rune: %q", got)
	}
}

func TestNamesThatLeaveNothingAreRefused(t *testing.T) {
	for _, in := range []string{"", "   ", ".", "..", "...", "/", "///", "\x00", "\xff"} {
		if out, err := SafeComponent(in); !errors.Is(err, ErrUnsafeName) {
			t.Errorf("SafeComponent(%q) = %q, %v — want ErrUnsafeName", in, out, err)
		}
	}
	if _, err := SafePath("", "  ", ""); !errors.Is(err, ErrUnsafeName) {
		t.Errorf("SafePath of nothing was accepted")
	}
}

// The end-to-end property: whatever SafePath produces, a Vault can resolve it,
// and it lands inside the root. Names are the untrusted half; containment is
// the kernel's. This checks the two compose.
func TestASanitisedNameAlwaysLandsInsideTheRoot(t *testing.T) {
	r := newRig(t)

	for _, hostile := range []string{
		"../../etc/cron.d/x",
		"/etc/passwd",
		"..",
		`..\..\windows\system32`,
		"escape",
		"film\x00.mkv",
		strings.Repeat("a", 500),
	} {
		rel, err := SafePath("Movies", hostile, "file.mkv")
		if err != nil {
			continue
		}
		f, err := r.vault.Create(rel)
		if err != nil {
			t.Errorf("a sanitised name was refused by the vault: %q -> %q: %v", hostile, rel, err)
			continue
		}
		_ = f.Close()

		// It exists, and it exists INSIDE the root.
		if _, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%q -> %q did not land inside the root: %v", hostile, rel, err)
		}
	}
	// Nothing appeared outside.
	entries, err := os.ReadDir(r.outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the outside directory gained entries: %d", len(entries))
	}
}

// A name that survives sanitisation must never be one that can escape, however
// strange the input. The corpus above is what I thought to try; this is for
// what I did not.
func FuzzSafeComponent(f *testing.F) {
	for _, s := range []string{
		"..", "../..", "/etc/passwd", `C:\x`, "CON", "film.mkv", "  . . ",
		"\x00", "\xff\xfe", "千と千尋", strings.Repeat("a", 300), "a/b",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		out, err := SafeComponent(s)
		if err != nil {
			return
		}
		if out == "" || out == "." || out == ".." {
			t.Fatalf("SafeComponent(%q) = %q", s, out)
		}
		if strings.ContainsAny(out, `/\`) {
			t.Fatalf("SafeComponent(%q) = %q contains a separator", s, out)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("SafeComponent(%q) = %q is not valid UTF-8", s, out)
		}
		if len(out) > MaxComponentBytes {
			t.Fatalf("SafeComponent(%q) is %d bytes", s, len(out))
		}
		for _, r := range out {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("SafeComponent(%q) = %q has a control character", s, out)
			}
		}
		// Growth is bounded at one byte: the ONLY thing that lengthens a name
		// is the underscore prefixed to a Windows reserved device name, and
		// truncation runs after it so the byte cap still holds. An unbounded
		// substitution — U+FFFD for invalid UTF-8, say — would turn every
		// length check into a surprise, which is a bug the release parser's
		// fuzzer found once already.
		if len(out) > len(s)+1 {
			t.Fatalf("SafeComponent(%q) grew from %d to %d bytes: %q",
				s, len(s), len(out), out)
		}
	})
}

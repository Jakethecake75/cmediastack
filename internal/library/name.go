package library

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Name sanitisation. A different job from containment (see the package comment):
// containment stops a path leaving its root, this makes a single component
// usable as a filename. A perfectly contained path can still be a filename an
// operator cannot delete from a shell, and a perfectly tidy name can still be
// "..".

const (
	// MaxComponentBytes bounds one path component.
	//
	// 255 is the limit on ext4, XFS, APFS, NTFS and every other filesystem this
	// is likely to meet, and it is a limit on BYTES rather than characters —
	// which matters, because a title in Japanese reaches it in 85 characters.
	MaxComponentBytes = 255
	// replacement stands in for a character that cannot appear in a filename.
	// A single character, so a name's length is predictable, and a visible one,
	// so an operator can see where the substitution happened.
	replacement = '_'
)

// windowsReserved are device names MS-DOS reserved and Windows still honours.
//
// A file called "CON" cannot be created on Windows even today, and a library
// containing one cannot be copied to a Windows machine, restored from a backup
// on one, or served over SMB to one. CMediaStack runs on Linux, but an
// operator's library outlives the software that filled it.
var windowsReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// SafeComponent turns an arbitrary string into one usable path component.
//
// It never returns a name containing a separator, so the result cannot become
// more than one component — which is what keeps a "title" from quietly becoming
// a directory tree. It never returns "", "." or "..".
//
// What is removed and why:
//
//   - Path separators, both kinds. A Windows-style backslash is a separator on
//     the machine an operator might later copy this library to.
//   - Control characters and invalid UTF-8. A filename containing \r or an
//     escape sequence is one that cannot be safely printed to a terminal, which
//     is how an operator would try to deal with it.
//   - The characters Windows and macOS refuse: < > : " | ? *
//   - Trailing dots and spaces, which Windows silently strips — so "Film." and
//     "Film" become the same file there, and one overwrites the other.
//
// What is deliberately KEPT: Unicode. A film's title in its own script is the
// correct name for it, and transliterating to ASCII is the kind of helpfulness
// that produces a library nobody can search.
func SafeComponent(s string) (string, error) {
	// Invalid UTF-8 is dropped rather than replaced with U+FFFD: substituting
	// makes the output longer than the input, which turns a length check into a
	// surprise. (This exact mistake was found by the release parser's fuzzer.)
	s = strings.ToValidUTF8(s, "")

	var b strings.Builder
	b.Grow(len(s))
	// kept counts characters that survived unchanged. A name made ENTIRELY of
	// replacements carries no information: "/" and "\x00" would both become
	// "_", colliding with each other and telling an operator looking at the
	// directory nothing at all. Refusing lets the caller fall back to something
	// meaningful — an info hash, a release title — instead of writing a file
	// called "___".
	kept := 0
	for _, r := range s {
		switch {
		case r == '/' || r == '\\':
			b.WriteRune(replacement)
		case r < 0x20 || r == 0x7f:
			// Control characters, including NUL, which would truncate the name
			// at the syscall boundary rather than being rejected.
			b.WriteRune(replacement)
		case unicode.IsControl(r):
			b.WriteRune(replacement)
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune(replacement)
		default:
			b.WriteRune(r)
			if !unicode.IsSpace(r) && r != '.' {
				kept++
			}
		}
	}

	if kept == 0 {
		return "", fmt.Errorf("%w: %q leaves nothing usable", ErrUnsafeName, s)
	}

	out := b.String()

	// Windows strips trailing dots and spaces silently, so "Film." and "Film"
	// collide there and one overwrites the other.
	out = strings.TrimRight(out, ". ")
	out = strings.TrimSpace(out)

	// "." and ".." are directory entries, not names. A leading dot is fine and
	// is kept: it only makes the entry hidden, which is not a safety problem.
	if out == "" || out == "." || out == ".." {
		return "", fmt.Errorf("%w: %q leaves nothing usable", ErrUnsafeName, s)
	}

	// A reserved device name, with or without an extension: Windows refuses
	// "NUL" and "NUL.mkv" alike.
	stem := out
	if i := strings.IndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	if _, bad := windowsReserved[strings.ToUpper(stem)]; bad {
		out = "_" + out
	}

	out = truncateBytes(out, MaxComponentBytes)
	// Truncation can re-create a trailing dot or space, or empty the string.
	out = strings.TrimRight(out, ". ")
	if out == "" {
		return "", fmt.Errorf("%w: %q leaves nothing usable", ErrUnsafeName, s)
	}
	return out, nil
}

// SafePath joins components into a relative path, sanitising each.
//
// Every component is sanitised INDIVIDUALLY, which is what makes this safe: a
// component cannot contain a separator once SafeComponent has run, so no input
// can produce more components than the caller passed. Building the whole path
// and sanitising afterwards is the version of this that has a traversal bug in
// it.
//
// The result is always relative, so it is a name a Vault can resolve. It is not
// a claim that the path is contained — that is the kernel's job, at use.
func SafePath(components ...string) (string, error) {
	parts := make([]string, 0, len(components))
	for _, c := range components {
		if strings.TrimSpace(c) == "" {
			continue
		}
		safe, err := SafeComponent(c)
		if err != nil {
			return "", err
		}
		parts = append(parts, safe)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("%w: no usable components", ErrUnsafeName)
	}
	return strings.Join(parts, "/"), nil
}

// truncateBytes cuts a string to at most n bytes without splitting a rune.
//
// Splitting one would produce invalid UTF-8 in a filename, which is exactly the
// state ToValidUTF8 was called to avoid at the top of SafeComponent.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// PreserveExtension truncates a filename to fit while keeping its extension.
//
// A media file whose extension is cut off stops being playable by anything that
// dispatches on it, and stops being recognisable to this software's own
// scanner. A very long title losing its tail is a cosmetic problem; losing
// ".mkv" is a functional one.
func PreserveExtension(stem, ext string) (string, error) {
	safeStem, err := SafeComponent(stem)
	if err != nil {
		return "", err
	}
	if ext == "" {
		return safeStem, nil
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	safeExt, err := SafeComponent(ext)
	if err != nil {
		return "", err
	}
	// SafeComponent trims trailing dots, so a bare "." returns an error above;
	// a leading dot survives, which is what an extension needs.
	if !strings.HasPrefix(safeExt, ".") {
		safeExt = "." + safeExt
	}
	if len(safeExt) >= MaxComponentBytes {
		return "", fmt.Errorf("%w: the extension %q is longer than a filename may be",
			ErrUnsafeName, ext)
	}

	room := MaxComponentBytes - len(safeExt)
	safeStem = strings.TrimRight(truncateBytes(safeStem, room), ". ")
	if safeStem == "" {
		return "", fmt.Errorf("%w: %q leaves no usable name", ErrUnsafeName, stem)
	}
	return safeStem + safeExt, nil
}

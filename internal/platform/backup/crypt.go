package backup

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"filippo.io/age"
)

const (
	// WorkFactor is the scrypt work factor backups are written with: 2^15,
	// about 32 MiB and a few tens of milliseconds. The work factor exists to
	// slow down guessing a person's passphrase; this one is 256 random bits, so
	// age's default of 2^18 — a quarter of a gigabyte per backup — would cost a
	// small machine for nothing.
	WorkFactor = 15
	// MaxWorkFactor is the most a file may demand before it is refused
	// unopened: 2^18. A backup of ours never asks for more than WorkFactor,
	// and a file asking for 2^22 or more is a way to make -verify-backup spend
	// gigabytes.
	MaxWorkFactor = 18
)

// ageMagic is how every age file begins. Checked before age is asked to parse
// anything, so "not an encrypted backup at all" is told apart from "damaged".
var ageMagic = []byte("age-encryption.org/v1\n")

// Why a backup could not be opened. Each wants something different from the
// operator, so they are told apart.
var (
	// ErrNotABackup is a file that is not age-encrypted at all.
	ErrNotABackup = errors.New("backup: this is not an encrypted backup")
	// ErrWrongKey is an age file this instance's master key does not open:
	// taken by another instance, or under a key since replaced.
	ErrWrongKey = errors.New("backup: this backup was not taken with this master key")
	// ErrDamaged is a backup whose encrypted content does not authenticate,
	// or that ends early: it was changed or cut short after it was written.
	ErrDamaged = errors.New("backup: the backup is damaged")
)

// sealed is what encrypting a snapshot produced.
type sealed struct {
	plain  []byte // SHA-256 of the snapshot
	cipher []byte // SHA-256 of the encrypted file
	size   int64  // of the encrypted file
}

// seal encrypts src into dst, a new file, hashing both sides as it goes.
func seal(src, dst, passphrase string) (sealed, error) {
	in, err := os.Open(src) // #nosec G304 -- the snapshot this package just wrote
	if err != nil {
		return sealed{}, err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a name this package built
	if err != nil {
		return sealed{}, err
	}
	res, err := sealInto(in, out, passphrase)
	if err != nil {
		_ = out.Close()
		return sealed{}, err
	}
	// A backup that is only in the page cache is not a backup yet.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return sealed{}, err
	}
	if err := out.Close(); err != nil {
		return sealed{}, err
	}
	return res, nil
}

func sealInto(in io.Reader, out io.Writer, passphrase string) (sealed, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return sealed{}, err
	}
	r.SetWorkFactor(WorkFactor)

	plain, cipher := sha256.New(), sha256.New()
	counted := &countingWriter{w: io.MultiWriter(out, cipher)}
	w, err := age.Encrypt(counted, r)
	if err != nil {
		return sealed{}, err
	}
	if _, err := io.Copy(w, io.TeeReader(in, plain)); err != nil {
		return sealed{}, err
	}
	// Close writes the final chunk; without it the file is truncated by
	// construction.
	if err := w.Close(); err != nil {
		return sealed{}, err
	}
	return sealed{plain: plain.Sum(nil), cipher: cipher.Sum(nil), size: counted.n}, nil
}

// confirm reads a just-written backup back from disk: its bytes must be the
// bytes written, and they must decrypt to the snapshot that passed its checks.
func confirm(path, passphrase string, want sealed) error {
	f, err := os.Open(path) // #nosec G304 -- the partial file this package just wrote
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	cipher := sha256.New()
	counted := &countingReader{r: io.TeeReader(f, cipher)}
	plainText, err := decrypt(counted, passphrase)
	if err != nil {
		return err
	}
	plain := sha256.New()
	if _, err := io.Copy(plain, plainText); err != nil {
		return fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	// Whatever the decrypter did not read is still part of the file.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return err
	}

	switch {
	case counted.n != want.size || !bytes.Equal(cipher.Sum(nil), want.cipher):
		return fmt.Errorf("the file on disk is not the file that was written (%d bytes read, %d written)",
			counted.n, want.size)
	case !bytes.Equal(plain.Sum(nil), want.plain):
		return errors.New("it decrypts, but not to the snapshot that was checked")
	}
	return nil
}

// decrypt opens an age stream with the backup passphrase.
func decrypt(src io.Reader, passphrase string) (io.Reader, error) {
	head := make([]byte, len(ageMagic))
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if !bytes.Equal(head[:n], ageMagic) {
		return nil, ErrNotABackup
	}

	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	id.SetMaxWorkFactor(MaxWorkFactor)

	out, err := age.Decrypt(io.MultiReader(bytes.NewReader(head), src), id)
	var noMatch *age.NoIdentityMatchError
	switch {
	case errors.As(err, &noMatch):
		return nil, ErrWrongKey
	case err != nil:
		// An age header that does not parse, or asks for more work than any
		// backup of ours: either way not a backup this can open.
		return nil, fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	return out, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// createEmpty makes a new, empty, private file.
func createEmpty(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a name this package built
	if err != nil {
		return err
	}
	return f.Close()
}

func removeQuietly(paths ...string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// removeMatching removes the regular files in dir whose names match re, and
// nothing else.
func removeMatching(dir string, re *regexp.Regexp) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type().IsRegular() && re.MatchString(e.Name()) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// syncDir makes a rename durable. Best effort: some network filesystems
// refuse to sync a directory, and the rename has happened either way.
func syncDir(dir string) {
	d, err := os.Open(dir) // #nosec G304 -- the configured backup directory
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

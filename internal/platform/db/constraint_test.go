package db

import (
	"errors"
	"path/filepath"
	"testing"
)

// The driver reports the extended result code, so a uniqueness violation can be
// told apart from every other refusal — a foreign key above all.
func TestAUniqueViolationIsToldApartFromOtherFailures(t *testing.T) {
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "c.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := t.Context()
	for _, q := range []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY, name TEXT UNIQUE)`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id))`,
		`INSERT INTO parent (id, name) VALUES (1, 'a')`,
	} {
		if _, err := d.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	_, unique := d.ExecContext(ctx, `INSERT INTO parent (id, name) VALUES (2, 'a')`)
	_, primary := d.ExecContext(ctx, `INSERT INTO parent (id, name) VALUES (1, 'b')`)
	_, foreign := d.ExecContext(ctx, `INSERT INTO child (parent_id) VALUES (99)`)
	_, notNull := d.ExecContext(ctx, `INSERT INTO child (parent_id) VALUES (NULL)`)

	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"unique": {unique, true}, "primary key": {primary, true},
		"foreign key": {foreign, false}, "not null": {notNull, false},
		"no error": {nil, false}, "another error": {errors.New("disk full"), false},
	} {
		if name != "no error" && name != "another error" && c.err == nil {
			t.Fatalf("%s: the insert was not refused", name)
		}
		if got := IsUniqueViolation(c.err); got != c.want {
			t.Errorf("%s (%v): IsUniqueViolation = %v, want %v", name, c.err, got, c.want)
		}
	}
}

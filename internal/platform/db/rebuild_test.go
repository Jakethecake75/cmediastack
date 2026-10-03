package db

import (
	"context"
	"strings"
	"testing"
)

// ADR-0044, decision 1: a table other tables reference can be rebuilt — its
// CHECK widened — without a single referencing row lost; and a rebuild that
// would leave a reference dangling is refused and rolled back.
func TestAMigrationMayRebuildAReferencedTable(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('a')))`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY,
		     parent_id INTEGER NOT NULL REFERENCES parent(id) ON DELETE CASCADE)`,
		`INSERT INTO parent (id, kind) VALUES (1, 'a'), (2, 'a')`,
		`INSERT INTO child (id, parent_id) VALUES (10, 1), (11, 1), (12, 2)`,
	} {
		if _, err := d.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	count := func(table string) int {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	rebuild := Migration{Version: 9001, Name: "widen", Checksum: "x", SQL: ForeignKeysOffDirective + `
CREATE TABLE parent_new (id INTEGER PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('a', 'b')));
INSERT INTO parent_new (id, kind) SELECT id, kind FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
`}
	if err := d.apply(ctx, rebuild); err != nil {
		t.Fatalf("the rebuild: %v", err)
	}
	if n := count("child"); n != 3 {
		t.Errorf("%d child rows after the rebuild, want 3 — dropping the parent cascaded", n)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO parent (id, kind) VALUES (3, 'b')`); err != nil {
		t.Errorf("the widened CHECK does not admit the new kind: %v", err)
	}
	// Foreign keys are back on for every connection afterwards.
	if _, err := d.ExecContext(ctx, `INSERT INTO child (id, parent_id) VALUES (13, 999)`); err == nil {
		t.Error("foreign keys were left off")
	}

	dangle := Migration{Version: 9002, Name: "lose", Checksum: "y", SQL: ForeignKeysOffDirective + `
DELETE FROM parent WHERE id = 1;
`}
	err := d.apply(ctx, dangle)
	if err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Fatalf("a migration leaving dangling references: %v", err)
	}
	if n := count("parent"); n != 3 {
		t.Errorf("the refused migration was not rolled back: %d parents", n)
	}
	var recorded int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migration WHERE version = 9002`).Scan(&recorded); err != nil || recorded != 0 {
		t.Errorf("the refused migration was recorded: %d %v", recorded, err)
	}
}

package docs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/api"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// APISurface renders docs/API-SURFACE.md from the real routing table.
//
// The table is built by the real RegisterRoutes against a Handlers with no
// dependencies wired. That is safe and deliberate: registration records access
// classes and permissions, and never calls a handler.
func APISurface() (string, error) {
	rt := api.NewRouter(nil, nil)
	api.RegisterRoutes(rt, api.New(api.Deps{}))
	return api.SurfaceMarkdown(rt.Routes()), nil
}

// tableComment matches the comment block immediately above a CREATE TABLE.
//
// The comments in the migrations are the only place the REASONS live — why a
// column is NOT NULL, why a reference cascades, why a table exists at all. A
// data model document without them is a schema dump, which the reader could
// have got from the database.
var tableComment = regexp.MustCompile(`(?ms)((?:^\s*--[^\n]*\n)+)\s*CREATE TABLE (?:IF NOT EXISTS )?([A-Za-z_][A-Za-z0-9_]*)`)

// DataModel renders docs/DATA-MODEL.md from the schema the migrations produce.
//
// It applies them to a throwaway database and reads the result back, rather
// than parsing the SQL: the authority on what the schema IS is the database
// that ran it, and a parser would be a second implementation to keep correct.
func DataModel() (string, error) {
	dir, err := os.MkdirTemp("", "cms-datamodel-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	database, err := db.Open(db.Options{Path: filepath.Join(dir, "schema.db")})
	if err != nil {
		return "", err
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	applied, err := database.Migrate(ctx)
	if err != nil {
		return "", err
	}

	// The reasoning, recovered from the migration each table was created in.
	migrations, err := db.LoadMigrations()
	if err != nil {
		return "", err
	}
	notes := map[string]string{}
	origin := map[string]int{}
	for _, m := range migrations {
		for _, match := range tableComment.FindAllStringSubmatch(m.SQL, -1) {
			table := match[2]
			if _, seen := notes[table]; seen {
				continue
			}
			notes[table] = tidyComment(match[1])
			origin[table] = m.Version
		}
	}

	var b strings.Builder
	b.WriteString("# The data model\n\n")
	b.WriteString("**Generated from the schema the migrations produce — do not edit.**\n")
	b.WriteString("Run `go generate ./internal/docs/` after adding a migration;\n")
	b.WriteString("`docs.TestTheGeneratedDocumentsAreCurrent` fails while this file is stale.\n\n")
	fmt.Fprintf(&b, "%d migrations applied (%v). SQLite with `foreign_keys=ON` and WAL "+
		"(ADR-0004) — the declared references below are enforced, not decorative.\n\n",
		len(applied), applied)

	tables, err := names(database, "table")
	if err != nil {
		return "", err
	}
	for _, t := range tables {
		fmt.Fprintf(&b, "## `%s`\n\n", t)
		if v, ok := origin[t]; ok {
			fmt.Fprintf(&b, "*Migration %04d.*\n\n", v)
		}
		if n := notes[t]; n != "" {
			b.WriteString(n + "\n\n")
		}

		cols, err := columns(database, t)
		if err != nil {
			return "", err
		}
		b.WriteString("| Column | Type | Null | Default | Key |\n|---|---|---|---|---|\n")
		fks, err := foreignKeys(database, t)
		if err != nil {
			return "", err
		}
		for _, c := range cols {
			key := ""
			if c.pk > 0 {
				key = "PK"
			}
			if ref, ok := fks[c.name]; ok {
				if key != "" {
					key += ", "
				}
				key += "→ `" + ref + "`"
			}
			null := "yes"
			if c.notNull {
				null = "**no**"
			}
			def := c.dflt
			if def != "" {
				def = "`" + def + "`"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n",
				c.name, strings.ToLower(orDash(c.typ)), null, orDash(def), orDash(key))
		}
		b.WriteString("\n")

		idx, err := indexesFor(database, t)
		if err != nil {
			return "", err
		}
		if len(idx) > 0 {
			b.WriteString("Indexes: ")
			for i, s := range idx {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString("`" + s + "`")
			}
			b.WriteString("\n\n")
		}
	}
	return b.String(), nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// tidyComment turns a SQL comment block into a paragraph.
func tidyComment(block string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(block), "\n") {
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "--")))
	}
	// Blank comment lines are paragraph breaks; everything else joins.
	var paras []string
	var cur []string
	for _, l := range lines {
		if l == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, " "))
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, " "))
	}
	return strings.Join(paras, "\n\n")
}

func names(d *db.DB, kind string) ([]string, error) {
	rows, err := d.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = ? AND name NOT LIKE 'sqlite_%'
		 ORDER BY name`, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type column struct {
	name    string
	typ     string
	notNull bool
	dflt    string
	pk      int
}

func columns(d *db.DB, table string) ([]column, error) {
	// The table name comes from sqlite_master, not from a caller, so it cannot
	// be anything this database did not already create.
	rows, err := d.QueryContext(context.Background(),
		fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []column
	for rows.Next() {
		var cid int
		var c column
		var dflt sql.NullString
		if err := rows.Scan(&cid, &c.name, &c.typ, &c.notNull, &dflt, &c.pk); err != nil {
			return nil, err
		}
		c.dflt = dflt.String
		out = append(out, c)
	}
	return out, rows.Err()
}

func foreignKeys(d *db.DB, table string) (map[string]string, error) {
	rows, err := d.QueryContext(context.Background(),
		fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, seq int
		var refTable, from, to, onUpdate, onDelete, match string
		var toNull sql.NullString
		if err := rows.Scan(&id, &seq, &refTable, &from, &toNull,
			&onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		to = toNull.String
		ref := refTable
		if to != "" {
			ref += "." + to
		}
		if onDelete != "" && onDelete != "NO ACTION" {
			ref += " (" + strings.ToLower(onDelete) + ")"
		}
		out[from] = ref
	}
	return out, rows.Err()
}

func indexesFor(d *db.DB, table string) ([]string, error) {
	rows, err := d.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?
		   AND name NOT LIKE 'sqlite_%' ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// Package docs holds no code. It exists for the tests below, which check the
// documentation against the tree rather than against itself.
//
// The project's documents make a particular kind of claim constantly: "this is
// established by TestX". That is the most useful sentence in any of them and
// the easiest one to leave behind — a test gets renamed, the claim keeps its
// old name, and the document now cites something that does not exist while
// reading exactly as authoritative as before. Nothing in a normal build
// notices, because a comment is not code.
//
// These have caught it twice. Once after a rename left PROGRESS.md and
// ADR-0016 citing TestNoBackgroundTaskCanDestroyMediaBytes and
// TestSystemAuthorityIsMinimal, both gone. Once on internal/search/search.go,
// which cited TestTheRealClientCanGrab to support a claim about the production
// type satisfying an interface — a test that had never existed, covering a
// silent failure mode that was therefore real. Writing it took ten minutes; the
// bug it prevents returns ErrGrabUnavailable for every grab in production while
// every test in the package passes.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/api"
)

// repoRoot walks up from the test's directory to the module root.
func repoRoot(t *testing.T) string {
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

var (
	declared = regexp.MustCompile(`(?m)^func ((?:Test|Fuzz|Benchmark)[A-Za-z0-9_]*)\(`)
	// An optional lowercase package qualifier, because citations cross packages
	// ("api.TestTheLastAdministratorCannotBeRemoved") as often as not.
	citation = regexp.MustCompile(`\b(?:[a-z][a-z0-9_]*\.)?((?:Test|Fuzz|Benchmark)[A-Za-z0-9_]{3,})\b`)
)

// selfPath is this file, relative to the module root. See the skip in
// TestEveryCitedTestExists.
const selfPath = "internal/docs/docs_test.go"

// englishWords are not citations. "Testing" is the only one that has come up,
// but the rule is worth naming rather than leaving as a mystery exclusion.
var englishWords = map[string]bool{"Testing": true, "Tests": true, "Tested": true}

// walk visits every file under root whose name passes keep, skipping the
// directories a Go tree has no documentation in.
func walk(t *testing.T, root string, keep func(string) bool, visit func(path, body string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !keep(d.Name()) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		visit(rel, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// declaredTests collects every test function name in the tree.
func declaredTests(t *testing.T, root string) map[string]bool {
	out := map[string]bool{}
	walk(t, root,
		func(name string) bool { return strings.HasSuffix(name, "_test.go") },
		func(_, body string) {
			for _, m := range declared.FindAllStringSubmatch(body, -1) {
				out[m[1]] = true
			}
		})
	return out
}

// TestEveryCitedTestExists is the check itself.
//
// It covers Markdown and Go COMMENTS. Go comments matter as much as the
// documents — a header comment explaining why a design is safe, citing the test
// that proves it, is the densest documentation in the project and drifts the
// same way. Code outside comments is skipped: a test calling a helper is not
// citing it.
func TestEveryCitedTestExists(t *testing.T) {
	root := repoRoot(t)
	real := declaredTests(t, root)
	if len(real) < 100 {
		t.Fatalf("only found %d test functions; the walk is probably broken, "+
			"and a broken walk makes this test pass by finding no citations either", len(real))
	}

	type where struct{ file, name string }
	var missing []where
	seen := map[where]bool{}
	cited := 0

	walk(t, root,
		func(name string) bool {
			return strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".go")
		},
		func(path, body string) {
			if path == selfPath {
				// This file names dead tests on purpose, as the examples in its
				// package comment. Explaining what the check caught requires
				// saying what it was called, and the alternative — describing
				// the catches without naming them — makes the one document that
				// justifies this rule the vaguest in the tree.
				return
			}
			if strings.HasSuffix(path, ".go") {
				body = commentsOnly(body)
			}
			for _, m := range citation.FindAllStringSubmatch(body, -1) {
				name := m[1]
				if englishWords[name] {
					continue
				}
				cited++
				if real[name] {
					continue
				}
				w := where{path, name}
				if !seen[w] {
					seen[w] = true
					missing = append(missing, w)
				}
			}
		})

	for _, m := range missing {
		t.Errorf("%s cites %s, which does not exist in the tree — "+
			"either the test was renamed and the claim was not, or the claim was "+
			"never true", m.file, m.name)
	}
	t.Logf("%d test functions declared, %d citations checked", len(real), cited)
}

// TestNoCitationIsSplitAcrossLines refuses a wrapped test name.
//
// A name broken over a line ending cannot be grepped, which defeats the point
// of citing it, and it silently defeats the check above: only the first half is
// matched, and a half-name is never found, so the citation reads as broken when
// it is merely wrapped. Two comments in this tree were wrapped that way.
func TestNoCitationIsSplitAcrossLines(t *testing.T) {
	root := repoRoot(t)
	// A comment line ending in a bare Test-prefixed identifier, where the next
	// line starts with an identifier fragment, is a wrapped name.
	wrapped := regexp.MustCompile(`(?m)(?:^|\s)((?:Test|Fuzz)[A-Za-z0-9_]*)\s*\n\s*(?://\s*)?([A-Z][A-Za-z0-9_]*)`)
	real := declaredTests(t, root)

	walk(t, root,
		func(name string) bool {
			return strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".go")
		},
		func(path, body string) {
			if strings.HasSuffix(path, ".go") {
				body = commentsOnly(body)
			}
			for _, m := range wrapped.FindAllStringSubmatch(body, -1) {
				head, tail := m[1], m[2]
				// Only a complaint when the halves joined name a real test and
				// the head alone does not: that is the wrap, rather than a
				// sentence that happens to end in a test name.
				if !real[head] && real[head+tail] {
					t.Errorf("%s splits %s across a line break, which makes it "+
						"un-greppable; keep a cited name on one line", path, head+tail)
				}
			}
		})
}

// commentsOnly reduces Go source to its comments, so that a call to a helper is
// not mistaken for a citation of it.
func commentsOnly(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBlock:
			b.WriteString(line)
			b.WriteByte('\n')
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
		case strings.HasPrefix(trimmed, "//"):
			b.WriteString(line)
			b.WriteByte('\n')
		case strings.HasPrefix(trimmed, "/*"):
			b.WriteString(line)
			b.WriteByte('\n')
			inBlock = !strings.Contains(trimmed[2:], "*/")
		}
	}
	return b.String()
}

// link matches a Markdown link to a path inside this repository. Anything with
// a scheme (http:, mailto:) is somebody else's to keep working; a relative path
// is this repository's own claim about itself.
var link = regexp.MustCompile(`\]\(([^)#\s]+)(?:#[^)\s]*)?\)`)

// TestEveryLinkedFileExists is the same failure as TestEveryCitedTestExists,
// one level up: a document asserting that a decision is written down somewhere.
//
// It was written after finding NINE dangling ADR links — 0001 through 0008 plus
// 0003a — in PROGRESS.md's "Decisions locked" table and in README.md. Those
// decisions were real and were made; the documents recording them were never
// written, and the table linked to them anyway. A reader following "see
// ADR-0005 for the transcode policy" got nothing, and the table read as though
// the reasoning existed in full.
//
// That is worse than an omission, because it is an omission that looks like a
// citation. The whole point of a decisions table is to be the index a later
// session resumes from, and an index whose entries do not resolve is an index
// that lies.
func TestEveryLinkedFileExists(t *testing.T) {
	root := repoRoot(t)

	type miss struct{ doc, target string }
	var missing []miss

	walk(t, root, func(name string) bool {
		return strings.HasSuffix(name, ".md")
	}, func(path, body string) {
		for _, m := range link.FindAllStringSubmatch(body, -1) {
			target := m[1]
			// Absolute URLs and protocol-relative ones belong to somebody else.
			if strings.Contains(target, ":") || strings.HasPrefix(target, "//") {
				continue
			}
			// Resolve relative to the linking document, as a reader would.
			var full string
			if strings.HasPrefix(target, "/") {
				full = filepath.Join(root, target)
			} else {
				full = filepath.Join(root, filepath.Dir(path), target)
			}
			if _, err := os.Stat(full); err != nil {
				missing = append(missing, miss{path, target})
			}
		}
	})

	for _, m := range missing {
		t.Errorf("%s links to %q, which does not exist", m.doc, m.target)
	}
	if len(missing) > 0 {
		t.Errorf("%d dangling link(s): either write the file or stop claiming it "+
			"is there — a decisions table whose entries do not resolve is worse "+
			"than one that admits the reasoning is unwritten", len(missing))
	}
}

// TestTheGeneratedDocumentsAreCurrent fails while docs/API-SURFACE.md or
// docs/DATA-MODEL.md differ from what this tree would produce now.
//
// Both describe something the compiler already knows — the routing table and
// the schema — so keeping them by hand would guarantee they went stale, and a
// stale API-SURFACE.md is a document somebody consults to ask "is anything
// anonymous that should not be?" and gets a confident wrong answer from.
//
// The fix is never to edit the file: run `go generate ./internal/docs/`.
func TestTheGeneratedDocumentsAreCurrent(t *testing.T) {
	root := repoRoot(t)

	surface, err := APISurface()
	if err != nil {
		t.Fatal(err)
	}
	model, err := DataModel()
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"docs/API-SURFACE.md": surface,
		"docs/DATA-MODEL.md":  model,
	} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Errorf("%s: %v — run `go generate ./internal/docs/`", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s is stale (%d bytes on disk, %d generated). "+
				"Do not edit it: run `go generate ./internal/docs/`.",
				name, len(got), len(want))
		}
	}
}

// PROGRESS.md says of itself: "Hand this file to a new session to resume. It is
// the authoritative statement of what exists." Its headline numbers are
// therefore load-bearing, and they are exactly the numbers nobody remembers to
// update.
//
// They had drifted three phases: 16 packages when there were 27, 346 tests when
// there were 777, and "42 of 81 routes still return 501" when 23 of 110 did.
// Every one of those is derivable from the tree, so none of them needs a person
// to maintain it — it needs a test.
//
// Only the mechanically checkable claims are checked. Prose goes stale too and
// nothing here can catch that; what this catches is the class of error where the
// document a new session is handed gives it a confident wrong number.
func TestTheProgressHeadlineNumbersAreTrue(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "PROGRESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The header block only. A number quoted inside an increment's write-up is a
	// record of what was true THEN, and rewriting history to keep a test happy
	// would be the wrong repair.
	header := string(body)
	if i := strings.Index(header, "\n## Phase 0"); i > 0 {
		header = header[:i]
	}

	rt := api.NewRouter(nil, nil)
	api.RegisterRoutes(rt, api.New(api.Deps{}))
	routes := rt.Routes()
	var anon, stub int
	for _, r := range routes {
		if r.Access == api.AccessAnonymous {
			anon++
		}
		if r.Stub {
			stub++
		}
	}

	for _, c := range []struct {
		what   string
		re     *regexp.Regexp
		want   int
		source string
	}{
		{"Go packages", regexp.MustCompile(`\|\s*Go packages\s*\|\s*(\d+)\s*\|`),
			countPackages(t, root), "directories holding non-test Go files"},
		{"tests", regexp.MustCompile(`\|\s*Tests\s*\|\s*(\d+)`),
			countTestFuncs(t, root), "`func Test` declarations"},
		{"routes registered", regexp.MustCompile(`\|\s*Routes registered\s*\|\s*(\d+)`),
			len(routes), "the routing table"},
		{"anonymous routes", regexp.MustCompile(`Routes registered\s*\|\s*\d+\s*\((\d+) anonymous`),
			anon, "the routing table"},
		{"routes still returning 501", regexp.MustCompile(`(\d+)\s+of\s+\d+\s+routes still return 501`),
			stub, "the routing table"},
	} {
		m := c.re.FindStringSubmatch(header)
		if m == nil {
			t.Errorf("PROGRESS.md's header no longer states the %s. It is the file "+
				"a new session is handed, and the count belongs in it", c.what)
			continue
		}
		got, err := strconv.Atoi(m[1])
		if err != nil {
			t.Errorf("%s: %q is not a number", c.what, m[1])
			continue
		}
		if got != c.want {
			t.Errorf("PROGRESS.md says %d %s; there are %d (counted from %s). "+
				"A wrong number in the authoritative file is worse than no number",
				got, c.what, c.want, c.source)
		}
	}
}

// countPackages counts directories holding non-test Go files, which is what
// `go list ./...` reports.
func countPackages(t *testing.T, root string) int {
	t.Helper()
	dirs := map[string]bool{}
	walk(t, root,
		func(name string) bool {
			return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
		},
		func(path, _ string) { dirs[filepath.Dir(path)] = true })
	return len(dirs)
}

func countTestFuncs(t *testing.T, root string) int {
	t.Helper()
	n := 0
	walk(t, root,
		func(name string) bool { return strings.HasSuffix(name, "_test.go") },
		func(_, body string) {
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "func Test") {
					n++
				}
			}
		})
	return n
}

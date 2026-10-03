package books

import (
	"path"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// A book's file (ADR-0049, decisions 1 and 2).

// formats are the book formats, ranked: EPUB reflows and is open, AZW3 and
// MOBI reflow on fewer readers, a PDF is somebody else's page.
var formats = map[string]struct {
	name string
	rank int
}{
	".epub": {"EPUB", 4},
	".azw3": {"AZW3", 3},
	".mobi": {"MOBI", 2},
	".pdf":  {"PDF", 1},
}

// Format is a file's book format and its rank; "" and 0 for anything else.
func Format(name string) (string, int) {
	f, ok := formats[strings.ToLower(path.Ext(name))]
	if !ok {
		return "", 0
	}
	return f.name, f.rank
}

// IsBook reports whether a file is a book.
func IsBook(name string) bool {
	_, rank := Format(name)
	return rank > 0
}

// rankOf is a recorded quality's rank.
func rankOf(quality string) int {
	for _, f := range formats {
		if f.name == quality {
			return f.rank
		}
	}
	return 0
}

// Better reports whether a file of format newQuality should replace one of
// oldQuality: only a higher rank does.
func Better(newQuality, oldQuality string) bool { return rankOf(newQuality) > rankOf(oldQuality) }

// FilePath is where a book's file goes: "<folder>/<Title> - <Author>.<ext>",
// every part made safe as a film's is.
func FilePath(folder, title, author, ext string) (string, error) {
	name := title
	if a := strings.TrimSpace(author); a != "" {
		name = title + " - " + a
	}
	base, err := library.SafeComponent(strings.TrimSpace(name))
	if err != nil {
		return "", err
	}
	if err := importer.CheckFolderName(folder); err != nil {
		return "", err
	}
	return folder + "/" + base + strings.ToLower(ext), nil
}

// best is the highest-ranked book file of a list, and whether there was one.
// Ties go to the first in order, so the choice does not depend on the order a
// directory was listed in.
func best(files []string) (string, bool) {
	var pick string
	top := 0
	for _, f := range files {
		if _, rank := Format(f); rank > top || (rank == top && rank > 0 && f < pick) {
			pick, top = f, rank
		}
	}
	return pick, top > 0
}

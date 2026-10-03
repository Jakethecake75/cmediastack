package music

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Music files (ADR-0045): which files are music, how good, where each is
// filed, and which track a file is.

var audioQuality = map[string]string{
	".flac": "FLAC", ".wav": "WAV", ".mp3": "MP3", ".m4a": "AAC", ".aac": "AAC",
	".ogg": "Vorbis", ".opus": "Opus",
}

// IsAudio reports whether a file is music.
func IsAudio(name string) bool {
	_, ok := audioQuality[strings.ToLower(path.Ext(name))]
	return ok
}

// AudioQuality names a file's quality by its container.
func AudioQuality(name string) string { return audioQuality[strings.ToLower(path.Ext(name))] }

// Lossless reports whether a quality is lossless.
func Lossless(quality string) bool { return quality == "FLAC" || quality == "WAV" }

// Better reports whether a new file should replace the one a track holds:
// only lossless over lossy (ADR-0045, decision 5).
func Better(newQuality, oldQuality string) bool { return Lossless(newQuality) && !Lossless(oldQuality) }

// AlbumFolder is where an album's files go inside its artist's folder.
func AlbumFolder(title, released string) (string, error) {
	year := 0
	if len(released) >= 4 {
		year, _ = strconv.Atoi(released[:4])
	}
	return importer.FolderFor(title, year)
}

// TrackPath is where one track is filed, relative to the root folder.
func TrackPath(artistFolder string, a Album, t Track, ext string, multiDisc bool) (string, error) {
	album, err := AlbumFolder(a.Title, a.Released)
	if err != nil {
		return "", err
	}
	name, err := importer.FolderFor(t.Title, 0)
	if err != nil {
		return "", err
	}
	number := fmt.Sprintf("%02d", t.Number)
	if multiDisc {
		number = fmt.Sprintf("%d-%02d", t.Disc, t.Number)
	}
	return path.Join(artistFolder, album, number+" - "+name+strings.ToLower(ext)), nil
}

var (
	// "1-03 Title", "1.03", "d1t03".
	discTrack = regexp.MustCompile(`^\s*(?:d|cd|disc\s*)?(\d{1,2})[-._](\d{1,3})(?:\D|$)`)
	// "03 - Title", "03. Title", "03 Title", "103 Title".
	leading = regexp.MustCompile(`^\s*(?:t(?:rack)?\s*)?(\d{1,3})(?:\D|$)`)
	// " - 03 - " inside a longer name: "Portishead - Dummy - 03 - Strangers".
	inner = regexp.MustCompile(`(?:^|[\s._-])(\d{2})(?:[\s._-]+)`)
	// "CD2", "Disc 2" as a folder.
	discFolder = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*(\d{1,2})$`)
)

// MatchTrack says which of an album's tracks a file is: by the number in its
// name, then by its title (ADR-0045, decision 3). ok is false when nothing, or
// more than one track, fits.
func MatchTrack(rel string, tracks []Track) (Track, bool) {
	if len(tracks) == 0 {
		return Track{}, false
	}
	multi := false
	for _, t := range tracks {
		if t.Disc > 1 {
			multi = true
		}
	}
	base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	lower := strings.ToLower(base)
	find := func(disc, number int) (Track, bool) {
		var hit []Track
		for _, t := range tracks {
			if t.Number == number && (disc == 0 || t.Disc == disc) {
				hit = append(hit, t)
			}
		}
		if len(hit) == 1 {
			return hit[0], true
		}
		return Track{}, false
	}

	disc := 0
	if dir := path.Base(path.Dir(rel)); dir != "." {
		if m := discFolder.FindStringSubmatch(dir); m != nil {
			disc, _ = strconv.Atoi(m[1])
		}
	}
	if !multi && disc == 0 {
		disc = 1
	}
	if multi && disc == 0 {
		if m := discTrack.FindStringSubmatch(lower); m != nil {
			d, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[2])
			if t, ok := find(d, n); ok {
				return t, true
			}
		}
	}
	if m := leading.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		if multi && disc == 0 && n >= 100 {
			if t, ok := find(n/100, n%100); ok {
				return t, true
			}
		}
		if t, ok := find(disc, n); ok {
			return t, true
		}
	}
	if m := inner.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		if t, ok := find(disc, n); ok {
			return t, true
		}
	}

	// By title: exactly one track's folded title among the name's words.
	name := " " + search.NormalizeTitle(base) + " "
	var hit []Track
	for _, t := range tracks {
		title := search.NormalizeTitle(t.Title)
		if title != "" && strings.Contains(name, " "+title+" ") && (disc == 0 || !multi || t.Disc == disc) {
			hit = append(hit, t)
		}
	}
	if len(hit) == 1 {
		return hit[0], true
	}
	return Track{}, false
}

var (
	bracketed  = regexp.MustCompile(`\[[^\]]*\]`)
	yearParens = regexp.MustCompile(`\(\s*(?:19|20)\d{2}[^)]*\)`)
)

// AlbumOfFolder says which album a folder is: the one whose folded title the
// folder's name starts with, once a bracketed year and anything in square
// brackets are set aside (ADR-0045, decision 4). A leading "<artist> - ", as
// downloads name their folders, is set aside too. The longest title wins, so
// "Portishead" and "Portishead Live" are told apart.
func AlbumOfFolder(folder, artist string, albums []Album) (Album, bool) {
	name := yearParens.ReplaceAllString(bracketed.ReplaceAllString(folder, " "), " ")
	folded := search.NormalizeTitle(name)
	if a := search.NormalizeTitle(artist); a != "" && strings.HasPrefix(folded, a+" ") {
		if rest := strings.TrimPrefix(folded, a+" "); rest != "" {
			if _, ok := albumByTitle(rest, albums); ok {
				folded = rest
			}
		}
	}
	return albumByTitle(folded, albums)
}

func albumByTitle(folded string, albums []Album) (Album, bool) {
	var best Album
	found := false
	for _, a := range albums {
		title := search.NormalizeTitle(a.Title)
		if title == "" {
			continue
		}
		if folded == title || strings.HasPrefix(folded, title+" ") {
			if !found || len(title) > len(search.NormalizeTitle(best.Title)) {
				best, found = a, true
			}
		}
	}
	return best, found
}

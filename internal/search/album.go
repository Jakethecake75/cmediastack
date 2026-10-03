package search

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// Searching for one album (ADR-0046).
//
// The shape of the film search: every candidate is judged against the album,
// and only one that IS the album carries a Target, which its ticket seals. What
// differs is how a name is read. A music release has no one convention, so it
// is not parsed into parts: it is folded, and it is the album when it starts
// with the artist and then the album's title, and what follows is a year, a
// tag or nothing.

// Why a candidate is not the album.
const (
	ReasonNotThisAlbum  = "not_this_album"
	ReasonUnknownFormat = "unknown_format"
)

// AudioCategories is what an album search asks the indexers for: Torznab's
// Audio, which includes its subcategories (ADR-0046, decision 5).
var AudioCategories = []int{3000}

// AlbumWant is one album a person asked to search for.
type AlbumWant struct {
	// ItemID is the artist; AlbumID the album.
	ItemID  int64
	AlbumID int64
	// Artists are the names the artist goes by, its name in the library first.
	Artists []string
	Title   string
	// Year is the album's first release; zero when MusicBrainz has none.
	Year int
	// Namesake is true when the artist has another album of the same folded
	// title, so a release must name a year to be told apart (decision 3).
	Namesake bool
}

// tags are the words that may follow an album's title in a release name: a
// format, a source, an edition. Anything else is a different title — "Dummy
// Live" is not "Dummy" (ADR-0046, decision 2).
var tags = map[string]bool{
	"flac": true, "alac": true, "lossless": true, "mp3": true, "aac": true, "m4a": true,
	"ogg": true, "opus": true, "wav": true, "320": true, "320kbps": true, "v0": true,
	"v2": true, "256": true, "256kbps": true, "192": true, "24bit": true, "16bit": true,
	"24": true, "16": true, "hi": true, "hires": true, "cd": true, "cdda": true,
	"web": true, "vinyl": true, "lp": true, "sacd": true, "dvd": true, "bd": true,
	"deluxe": true, "remaster": true, "remastered": true, "expanded": true,
	"edition": true, "anniversary": true, "reissue": true, "bonus": true,
	"retail": true, "promo": true, "ep": true, "album": true,
}

var yearWord = regexp.MustCompile(`^(19|20)\d\d$`)

// MatchAlbum says whether a release name is the wanted album, and why not when
// it is not. A nil rejection is a match.
func MatchAlbum(name string, w AlbumWant) *release.Rejection {
	folded := NormalizeTitle(name)
	if folded == "" {
		return &release.Rejection{Reason: release.ReasonUnparsed,
			Detail: "the release name does not say which album it is"}
	}
	album := NormalizeTitle(w.Title)
	var rest string
	matched := false
	for _, a := range w.Artists {
		artist := NormalizeTitle(a)
		if artist == "" || album == "" {
			continue
		}
		prefix := artist + " " + album
		if folded == prefix {
			rest, matched = "", true
			break
		}
		if strings.HasPrefix(folded, prefix+" ") {
			rest, matched = strings.TrimPrefix(folded, prefix+" "), true
			break
		}
	}
	if !matched {
		return &release.Rejection{Reason: ReasonNotThisAlbum,
			Detail: "a different album, or a different artist's: " + strings.TrimSpace(name)}
	}
	words := strings.Fields(rest)
	if len(words) > 0 && !yearWord.MatchString(words[0]) && !tags[words[0]] {
		return &release.Rejection{Reason: ReasonNotThisAlbum,
			Detail: fmt.Sprintf("a different album: “%s %s” is not %s", w.Title, words[0], w.Title)}
	}

	var years []int
	for _, word := range words {
		if yearWord.MatchString(word) {
			y, _ := strconv.Atoi(word)
			years = append(years, y)
		}
	}
	switch {
	case len(years) == 0 && w.Namesake:
		return &release.Rejection{Reason: ReasonNoYear,
			Detail: fmt.Sprintf("names no year, and the artist has more than one album called %s", w.Title)}
	case len(years) > 0 && w.Year > 0:
		for _, y := range years {
			if y-w.Year <= yearTolerance && w.Year-y <= yearTolerance {
				return nil
			}
		}
		return &release.Rejection{Reason: ReasonNotThisAlbum,
			Detail: fmt.Sprintf("a different album: %s from %d, not %d", w.Title, years[0], w.Year)}
	case len(years) > 0 && w.Namesake:
		// The album's own year is not known, so nothing can say which of
		// its namesakes a dated release is.
		return &release.Rejection{Reason: ReasonNoYear,
			Detail: fmt.Sprintf("the year of %s is not known, and the artist has more than one album of that name", w.Title)}
	}
	return nil
}

// AudioQuality is the quality a music release name says, and its rank on the
// ladder (ADR-0046, decision 4). Zero is Unknown.
func AudioQuality(name string) (string, int) {
	words := strings.Fields(NormalizeTitle(name))
	has := make(map[string]bool, len(words))
	for i, w := range words {
		has[w] = true
		if i > 0 {
			has[words[i-1]+" "+w] = true
		}
	}
	lossless := has["flac"] || has["alac"] || has["lossless"]
	hiRes := has["24bit"] || has["24 bit"] || has["24 96"] || has["24 192"] || has["24 88"] ||
		has["24 48"] || has["24 44"] || has["hi res"] || has["hires"]
	switch {
	case lossless && hiRes:
		return "FLAC 24-bit", 6
	case lossless:
		return "FLAC", 5
	case has["320"] || has["320kbps"]:
		return "MP3-320", 4
	case has["v0"]:
		return "MP3-V0", 3
	case has["mp3"]:
		return "MP3", 2
	case has["aac"] || has["m4a"]:
		return "AAC", 2
	case has["ogg"] || has["vorbis"]:
		return "Vorbis", 2
	case has["opus"]:
		return "Opus", 2
	}
	return release.QualityUnknown.Name, 0
}

// AlbumTerm is what the indexers are asked for an album by default: the
// artist and the album, folded.
func AlbumTerm(artist, title string) string {
	return strings.TrimSpace(NormalizeTitle(artist) + " " + NormalizeTitle(title))
}

// AlbumSearch is a search for one album.
type AlbumSearch struct {
	Want AlbumWant
	// Term is what the indexers are asked. Empty means AlbumTerm. Whatever it
	// is, MatchAlbum decides what is grabbable.
	Term       string
	IndexerIDs []int64
}

// SearchAlbum searches for one album and judges every candidate against it.
// No video quality profile applies: the ladder does (decision 4).
func (s *Service) SearchAlbum(ctx context.Context, as AlbumSearch) (Response, error) {
	w := as.Want
	if w.ItemID <= 0 || w.AlbumID <= 0 || len(w.Artists) == 0 || strings.TrimSpace(w.Title) == "" {
		return Response{}, fmt.Errorf("search: %+v does not name an album", w)
	}
	term := strings.TrimSpace(as.Term)
	if term == "" {
		term = AlbumTerm(w.Artists[0], w.Title)
	}
	resp, err := s.Search(ctx, Request{
		Term: term, Season: -1, Categories: AudioCategories, IndexerIDs: as.IndexerIDs,
	})
	if err != nil {
		return resp, err
	}
	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		name, rank := AudioQuality(c.Title)
		c.Quality, c.Score, c.Target = release.Quality{Name: name}, rank, nil
		if rej := MatchAlbum(c.Title, w); rej != nil {
			c.Accepted, c.Rejection = false, rej
			continue
		}
		if rank == 0 {
			c.Accepted, c.Rejection = false, &release.Rejection{Reason: ReasonUnknownFormat,
				Detail: "the name does not say whether it is FLAC, MP3 or anything else"}
			continue
		}
		c.Accepted, c.Rejection = true, nil
		c.Target = &Target{ItemID: w.ItemID, Album: w.AlbumID}
	}
	sort.SliceStable(resp.Candidates, func(i, j int) bool {
		a, b := resp.Candidates[i], resp.Candidates[j]
		if a.Accepted != b.Accepted {
			return a.Accepted
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Seeders > b.Seeders
	})
	return resp, nil
}

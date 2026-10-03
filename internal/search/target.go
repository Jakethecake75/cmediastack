package search

import "fmt"

// Target is what a candidate was matched to: one episode of one series
// (ADR-0023), one film (ADR-0026), or one whole season of a series (ADR-0033). It rides in the grab ticket, sealed, and
// from there onto the download, so the import files the release under the item
// the SERVER matched it to rather than one worked out from the release name.
//
// The JSON names are short because the whole ticket is sealed and carried in a
// URL-safe token. "fm" is omitted for an episode, so a ticket sealed before
// films existed opens as the episode it always was.
type Target struct {
	ItemID  int64 `json:"m"`
	Season  int   `json:"s"`
	Episode int   `json:"e"`
	// Film marks a target that is a whole film: its item, and no season or
	// episode.
	Film bool `json:"fm,omitempty"`
	// Pack marks a target that is a whole season: its series and season, and
	// no episode. Omitted otherwise, for the same reason as "fm".
	Pack bool `json:"pk,omitempty"`
	// LastSeason is a pack's last season when it holds several, from Season
	// to LastSeason (ADR-0057); zero for one season.
	LastSeason int `json:"ls,omitempty"`
	// Album is one album of the artist that is the item (ADR-0046): no season,
	// no episode, neither a film nor a pack.
	Album int64 `json:"al,omitempty"`
	// Book is a book: the item, and nothing else (ADR-0049).
	Book bool `json:"bk,omitempty"`
}

// Valid reports whether the target names something, in one of its three forms
// and not a mixture: a film with an episode number, or a season with one, is
// neither, and sealing it would hand the import a question it cannot answer.
func (t Target) Valid() bool {
	switch {
	case t.ItemID <= 0 || (t.Film && t.Pack) || t.Album < 0:
		return false
	case t.LastSeason != 0 && (!t.Pack || t.LastSeason <= t.Season):
		return false
	case t.Book:
		return !t.Film && !t.Pack && t.Album == 0 && t.Season == 0 && t.Episode == 0
	case t.Album > 0:
		return !t.Film && !t.Pack && t.Season == 0 && t.Episode == 0
	case t.Film:
		return t.Season == 0 && t.Episode == 0
	case t.Pack:
		return t.Season >= 0 && t.Episode == 0
	}
	return t.Season >= 0 && t.Episode > 0
}

// Code renders an episode as S02E03 and a season as S02. A film has no code,
// and renders empty.
func (t Target) Code() string {
	if t.Film || t.Album > 0 || t.Book {
		return ""
	}
	if t.Pack && t.LastSeason > 0 {
		return fmt.Sprintf("S%02d-S%02d", t.Season, t.LastSeason)
	}
	if t.Pack {
		return fmt.Sprintf("S%02d", t.Season)
	}
	return fmt.Sprintf("S%02dE%02d", t.Season, t.Episode)
}

package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
)

// What a transfer was grabbed for, said by name (ADR-0026, decision 6).

// queueTarget is the "for" of a grab's answer and of a queue row: the episode
// or film a targeted search matched the release to, which is also where the
// import will file it.
type queueTarget struct {
	ItemID int64 `json:"item_id"`
	// Kind is "episode", "film", "season", "album" or "book".
	Kind    string `json:"kind"`
	Season  *int   `json:"season,omitempty"`
	Episode *int   `json:"episode,omitempty"`
	// Code is S02E03 for an episode and S02 for a season. A film has none.
	Code string `json:"code,omitempty"`
	// AlbumID is the album of an album target (ADR-0046).
	AlbumID int64 `json:"album_id,omitempty"`
	// Label is what a person reads: "Severance S02E03", "Dune (2021)". Looked
	// up from the item when shown, not carried in the ticket, which stays as
	// small as it was.
	Label string `json:"label"`
}

// itemNames labels the items transfers were grabbed for, reading each item
// once however many transfers name it.
type itemNames struct {
	media MediaService
	// music names an album target's album. Nil names only its artist.
	music MusicService
	seen  map[int64]string
	known map[int64]bool
	// queue reads every title, for the queue (ADR-0037); otherwise only what
	// the caller may see is named.
	queue bool
}

// itemNames names titles the caller may see — for requests.
func (h *Handlers) itemNames() *itemNames {
	return &itemNames{media: h.media, music: h.music, seen: map[int64]string{}, known: map[int64]bool{}}
}

// queueNames names what a download is for, whoever may see it: the queue is an
// operations surface, and a release name already says it (ADR-0037).
func (h *Handlers) queueNames() *itemNames {
	n := h.itemNames()
	n.queue = true
	return n
}

// forDownload renders a queue row's target, or nil for none.
func (n *itemNames) forDownload(ctx context.Context, t *download.Target) *queueTarget {
	if t == nil {
		return nil
	}
	if t.Album > 0 {
		artist, _ := n.name(ctx, t.ItemID, "artist")
		label := "an album of " + artist
		if n.music != nil {
			// Scoped like any album read: one the reader may not see is
			// named only by its artist, which the release name says anyway.
			if a, err := n.music.Album(ctx, t.Album); err == nil {
				label = albumName(a)
			}
		}
		return &queueTarget{ItemID: t.ItemID, Kind: "album", AlbumID: t.Album, Label: label}
	}
	if t.Book {
		name, _ := n.name(ctx, t.ItemID, "book")
		return &queueTarget{ItemID: t.ItemID, Kind: "book", Label: name}
	}
	if t.Film {
		name, _ := n.name(ctx, t.ItemID, "film")
		return &queueTarget{ItemID: t.ItemID, Kind: "film", Label: name}
	}
	season, episode := t.Season, t.Episode
	if t.Pack {
		code := fmt.Sprintf("S%02d", season)
		whole := " (the whole season)"
		if t.LastSeason > 0 {
			code = fmt.Sprintf("S%02d-S%02d", season, t.LastSeason)
			whole = fmt.Sprintf(" (seasons %d to %d)", season, t.LastSeason)
		}
		name, known := n.name(ctx, t.ItemID, "series")
		label := name + " " + code + whole
		if !known {
			label = code + " of " + name
		}
		return &queueTarget{ItemID: t.ItemID, Kind: "season", Season: &season, Code: code, Label: label}
	}
	code := fmt.Sprintf("S%02dE%02d", season, episode)
	name, known := n.name(ctx, t.ItemID, "series")
	label := name + " " + code // "Severance (2022) S02E03"
	if !known {
		label = code + " of " + name // "S02E03 of a series no longer in the library"
	}
	return &queueTarget{
		ItemID: t.ItemID, Kind: "episode", Season: &season, Episode: &episode, Code: code,
		Label: label,
	}
}

// name is an item's title and year, and whether the item was read; when it
// could not be, a phrase that says so. It labels; it never decides: the import
// reads the item for itself.
func (n *itemNames) name(ctx context.Context, id int64, what string) (string, bool) {
	if name, ok := n.seen[id]; ok {
		return name, n.known[id]
	}
	name, known := fmt.Sprintf("%s %d", what, id), false
	if n.media != nil {
		read := n.media.GetItem
		if n.queue {
			read = n.media.ItemForQueue
		}
		it, err := read(ctx, id)
		switch {
		case err == nil:
			name, known = it.Title, true
			if it.Year > 0 {
				name = fmt.Sprintf("%s (%d)", it.Title, it.Year)
			}
		case errors.Is(err, importer.ErrItemNotFound):
			// Said, because it is what the import will say: a target that was
			// deleted is not re-created. To somebody who cannot see all of the
			// library, deleted and hidden must read alike (ADR-0037).
			name = fmt.Sprintf("a %s no longer in the library", what)
			if s := authz.ScopeFromContext(ctx); !n.queue && (!s.AllLibraries || s.RatingCeiling > 0) {
				name = fmt.Sprintf("a %s not in the library you can see", what)
			}
		}
	}
	n.seen[id], n.known[id] = name, known
	return name, known
}

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// DownloadEngine is the subset of the download engine the API needs.
//
// Add and AddTorrentBytes are reachable only from the grab path, and what they
// receive there is a payload THE SERVER FETCHED from a re-resolved indexer —
// never a URI out of a request body. That distinction is the whole security
// property: an endpoint that added a caller-supplied magnet would make the
// engine an open relay, and every upstream check would become advisory.
//
// Note what is NOT here: "remove and delete the files". Remove stops a
// transfer; unlinking bytes is EffectDestroyMediaBytes, a separate permission,
// and merging the two would let PermManageQueue destroy data it was never
// granted.
type DownloadEngine interface {
	List() []download.Transfer
	Remove(hash string) error
	Notes() []string
	Start(hash string) error
	// Connections is the peer connections attempted and failed (ADR-0066).
	Connections() download.ConnStats

	// The grab path uses these two. Both take the metadata the engine cannot
	// know — the release name the indexer published, which indexer it was, and
	// who asked — because a queue that cannot answer "who caused this and what
	// is it" leaves the operator answerable for something they cannot explain.
	AddMagnet(ctx context.Context, magnet string, meta download.Meta) (download.Transfer, error)
	AddTorrent(data []byte, meta download.Meta) (download.Transfer, error)

	// Records is the persisted queue. It carries what the engine cannot know:
	// the release name the indexer published, who grabbed it, which indexer it
	// came from, and when it was FIRST grabbed — which is not when this process
	// happened to add it, and is the one an operator means by "when".
	Records(ctx context.Context) ([]download.Record, error)
}

// queueItem is the wire shape of one transfer.
//
// The info hash is the identity: it is what the engine keys on, what the data
// directory is named after, and — unlike the torrent's own name — it is fixed
// length, hex, and chosen by nobody.
type queueItem struct {
	InfoHash  string  `json:"info_hash"`
	Name      string  `json:"name"`
	Bytes     int64   `json:"bytes"`
	Completed int64   `json:"completed"`
	Percent   float64 `json:"percent"`
	Peers     int     `json:"peers"`
	Seeders   int     `json:"seeders"`
	Done      bool    `json:"done"`
	// How it is doing (ADR-0068).
	Connected  int   `json:"connected"`
	Connecting int   `json:"connecting"`
	Waiting    int   `json:"waiting"`
	Received   int64 `json:"received"`
	Rate       int64 `json:"rate"`
	// HaveMetadata is false while a magnet is still resolving. Until it flips,
	// Bytes and Name are unknown rather than zero and empty, and a UI that does
	// not distinguish the two shows a 0%-of-0-bytes download that looks broken.
	HaveMetadata bool `json:"have_metadata"`

	// Below here comes from the persisted row rather than from the engine. A
	// transfer that is running but has no row is reported with these empty and
	// status "unrecorded", which is a real and alarming state — it means the
	// row could not be written and this transfer will vanish on restart — so it
	// is shown rather than smoothed over.
	Title   string `json:"title"`
	Indexer string `json:"indexer,omitempty"`
	AddedBy string `json:"added_by,omitempty"`
	// Automatic marks a download no person pressed Grab for: automatic
	// acquisition's (ADR-0030).
	Automatic   bool       `json:"automatic,omitempty"`
	Status      string     `json:"status"`
	AddedAt     *time.Time `json:"added_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// For is the episode or film the transfer was grabbed for, when it came
	// from an episode or film search (ADR-0023, ADR-0026) — which is also
	// where the import will file it.
	For *queueTarget `json:"for,omitempty"`
	// Stalled says the download stopped moving (ADR-0034).
	Stalled *queueStall `json:"stalled,omitempty"`
}

// queueStall is a download found making no progress: since when it has not
// moved, when that was found, and whether it was given up — automatic
// acquisition's are, a person's are left to them.
type queueStall struct {
	Since   time.Time `json:"since"`
	FoundAt time.Time `json:"found_at"`
	GivenUp bool      `json:"given_up"`
}

func stallOf(rec download.Record) *queueStall {
	if rec.StalledAt.IsZero() {
		return nil
	}
	since := rec.ProgressedAt
	if since.IsZero() {
		since = rec.AddedAt
	}
	// Automatic acquisition's are stopped when they are found; a person's
	// never are (ADR-0034).
	return &queueStall{Since: since, FoundAt: rec.StalledAt, GivenUp: automaticRecord(rec)}
}

// Queue lists every transfer the engine knows about.
//
// It reports the engine's startup notes alongside the items, because those
// notes are where "DHT is off, so magnet links may never resolve" is said, and
// an operator staring at a stuck magnet needs that sentence on the same screen
// as the stuck magnet.
func (h *Handlers) Queue(w http.ResponseWriter, r *http.Request) {
	if h.downloads == nil {
		writeProblem(w, http.StatusNotImplemented,
			"the download engine is not running (download.enabled is false in the configuration)")
		return
	}

	// The two halves of the truth: the engine knows progress, the store knows
	// identity and history. Neither alone is a queue an operator can act on.
	records, err := h.downloads.Records(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "the download queue could not be read")
		return
	}
	byHash := make(map[string]download.Record, len(records))
	for _, rec := range records {
		byHash[rec.InfoHash] = rec
	}

	transfers := h.downloads.List()
	live := make(map[string]struct{}, len(transfers))
	names := h.queueNames()
	items := make([]queueItem, 0, len(transfers)+len(records))

	for _, t := range transfers {
		live[t.InfoHash] = struct{}{}
		item := queueItem{
			InfoHash: t.InfoHash, Name: t.Name,
			Bytes: t.Bytes, Completed: t.Completed, Percent: t.Percent(),
			Peers: t.Peers, Seeders: t.Seeders, Done: t.Done,
			Connected: t.Connected, Connecting: t.Connecting, Waiting: t.Waiting,
			Received: t.Received, Rate: t.Rate,
			HaveMetadata: t.MetadataGot,
			Title:        t.Name,
			Status:       "unrecorded",
		}
		if rec, ok := byHash[t.InfoHash]; ok {
			item.Title = rec.Title
			item.Indexer = rec.IndexerName
			item.AddedBy = rec.AddedLabel
			item.Automatic = automaticRecord(rec)
			item.Status = rec.Status
			added := rec.AddedAt
			item.AddedAt = &added
			item.CompletedAt = rec.CompletedAt
			item.For = names.forDownload(r.Context(), rec.Target)
			item.Stalled = stallOf(rec)
		}
		items = append(items, item)
	}

	// Rows the engine is not running. A completed or stopped transfer belongs
	// in the queue view: "what has this instance acquired" is a question the
	// operator is answerable for, and a list that shows only what is in flight
	// cannot answer it.
	for _, rec := range records {
		if _, running := live[rec.InfoHash]; running {
			continue
		}
		added := rec.AddedAt
		items = append(items, queueItem{
			InfoHash: rec.InfoHash, Title: rec.Title, Name: rec.Title,
			Indexer: rec.IndexerName, AddedBy: rec.AddedLabel, Automatic: automaticRecord(rec),
			Status: rec.Status, AddedAt: &added, CompletedAt: rec.CompletedAt,
			Done:    rec.Status == download.StatusComplete,
			For:     names.forDownload(r.Context(), rec.Target),
			Stalled: stallOf(rec),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"notes":       h.downloads.Notes(),
		"connections": h.downloads.Connections(),
	})
}

// QueueRemove stops one transfer and leaves its bytes alone.
func (h *Handlers) QueueRemove(w http.ResponseWriter, r *http.Request) {
	if h.downloads == nil {
		writeProblem(w, http.StatusNotImplemented,
			"the download engine is not running (download.enabled is false in the configuration)")
		return
	}
	p := authz.FromContext(r.Context())

	// Validated here rather than left to the engine: an info hash is hex of a
	// known length, so anything else is a client error worth naming, and
	// checking the shape before it reaches a lookup keeps a path-shaped string
	// from ever being used as an identifier.
	hash := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !download.IsInfoHash(hash) {
		writeProblem(w, http.StatusBadRequest, "not an info hash")
		return
	}

	err := h.downloads.Remove(hash)

	outcome := audit.OutcomeSuccess
	if err != nil {
		outcome = audit.OutcomeFailure
	}
	if h.audit != nil && p != nil {
		_ = h.audit.Write(r.Context(), audit.Event{
			ActorUserID: &p.UserID,
			ActorLabel:  p.Username,
			Action:      audit.ActionQueueRemoved,
			Outcome:     outcome,
			TargetKind:  "download",
			TargetID:    hash,
			SourceIP:    ClientIP(r.Context()),
			UserAgent:   r.UserAgent(),
			Detail:      "the transfer was stopped; files on disk were not touched",
		})
	}

	switch {
	case errors.Is(err, download.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "no such transfer")
	case errors.Is(err, download.ErrEngineClosed):
		writeProblem(w, http.StatusServiceUnavailable, "the download engine is shutting down")
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "could not stop the transfer")
	default:
		note := "The transfer was stopped. Any bytes already written are still on disk: " +
			"deleting them is a separate permission."
		if h.acquisition != nil {
			// The queue is automatic acquisition's blocklist (ADR-0030,
			// decision 8). Said here, because the obvious reading of "remove"
			// is "I do not want this", and the item is still wanted.
			note += " Automatic acquisition will never grab this release again, but whatever " +
				"it was for is still wanted and will be looked for again: unmonitor it if you " +
				"do not want it at all."
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"removed": hash,
			"note":    note,
		})
	}
}

// automaticRecord reports whether a queue row was added by automatic
// acquisition rather than by a person: no person's id, and its label.
func automaticRecord(rec download.Record) bool { return acquire.Automatic(rec) }

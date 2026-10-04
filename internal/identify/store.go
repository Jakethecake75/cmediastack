package identify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Errors this package distinguishes.
var (
	// ErrNotFound means there is no identification row for that item.
	ErrNotFound = errors.New("identify: no identification for that item")
	// ErrDecidedByAPerson means an automatic pass tried to change something a
	// person decided. The single most important refusal in this package.
	ErrDecidedByAPerson = errors.New("identify: a person already decided this one")
	// ErrNoSuchCandidate means the chosen id was not among the ones offered.
	ErrNoSuchCandidate = errors.New("identify: that was not one of the candidates")
)

// State is where an item's identification has got to.
type State string

const (
	// StateUnidentified means nothing has been tried.
	StateUnidentified State = "unidentified"
	// StateProposed means candidates are waiting for a person.
	StateProposed State = "proposed"
	// StateConfirmed means the item carries a provider id.
	StateConfirmed State = "confirmed"
	// StateNone means a search ran and found nothing worth showing.
	//
	// Distinct from unidentified on purpose: collapsing them would make every
	// pass re-search the items it already knows it cannot help with, which for
	// a large library is a lot of requests to a third party to learn nothing.
	StateNone State = "none"
)

// Identification is what is known about one item.
type Identification struct {
	ItemID int64
	State  State

	Provider   string
	ProviderID int64

	// ParsedTitle and ParsedYear are what the release-name parser produced,
	// preserved so a confirmation can be undone.
	ParsedTitle string
	ParsedYear  int

	// DecidedBy is nil when the software decided. This is the field an
	// automatic pass consults before touching anything.
	DecidedBy  *int64
	DecidedAt  *time.Time
	Verdict    Verdict
	VerdictWhy string

	SearchedAt *time.Time
	UpdatedAt  time.Time

	Candidates []Candidate
}

// ByAPerson reports whether a human made this decision.
func (i *Identification) ByAPerson() bool { return i.DecidedBy != nil }

// Candidate is one stored option.
type Candidate struct {
	Rank          int
	Provider      string
	ProviderID    int64
	Title         string
	OriginalTitle string
	Year          int
	Overview      string
	PosterPath    string
	Score         float64
	Why           string
}

// Match rebuilds the provider match, for re-scoring or for artwork.
func (c Candidate) Match(kind metadata.Kind) metadata.Match {
	return metadata.Match{
		ProviderID: c.ProviderID, Kind: kind, Title: c.Title,
		OriginalTitle: c.OriginalTitle, Year: c.Year,
		Overview: c.Overview, PosterPath: c.PosterPath,
	}
}

// Store persists identifications.
//
// No permission checks: this is the data layer, and who may confirm an
// identification is a question that needs the actor, which lives one layer up.
type Store struct {
	db  *db.DB
	now func() time.Time
}

// NewStore builds the repository.
func NewStore(database *db.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, now: now}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func nullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// SaveProposal records the outcome of a search, with its candidates.
//
// # The refusal that matters
//
// It will NOT overwrite a row a person decided. An automatic pass over a
// library must be safe to re-run — that is the whole point of it being
// automatic — and a re-run that quietly undid yesterday's corrections would
// make the feature worse than useless: the operator would fix the same items
// forever and never work out why.
//
// So a human decision is a wall. Changing one is ReIdentify, which is an
// explicit act with a person behind it.
func (s *Store) SaveProposal(ctx context.Context, itemID int64, parsed Item, res Result) error {
	now := ts(s.now())

	return s.db.InTx(ctx, func(tx db.Execer) error {
		var decidedBy sql.NullInt64
		var state string
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(state,''), decided_by FROM media_identification WHERE item_id = ?`,
			itemID).Scan(&state, &decidedBy)
		switch {
		case err == nil && decidedBy.Valid:
			return fmt.Errorf("%w: item %d was decided by user %d",
				ErrDecidedByAPerson, itemID, decidedBy.Int64)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}

		newState := StateProposed
		if res.Verdict == VerdictNone {
			newState = StateNone
		}

		// An ACCEPTED verdict is still recorded as decided_by NULL — the
		// software decided. That is not a technicality: it means a later pass
		// may revisit its own automatic decision when the provider's data
		// improves, while a person's decision stands.
		var providerID sql.NullInt64
		provider := ""
		if res.Verdict == VerdictAccept {
			newState = StateConfirmed
			if b := res.Best(); b != nil {
				providerID = sql.NullInt64{Int64: b.Match.ProviderID, Valid: true}
				provider = "tmdb"
			}
		}

		var decidedAt any
		if newState == StateConfirmed {
			decidedAt = now
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO media_identification
			    (item_id, state, provider, provider_id, parsed_title, parsed_year,
			     decided_by, decided_at, verdict, verdict_why, searched_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?)
			ON CONFLICT (item_id) DO UPDATE SET
			    state = excluded.state, provider = excluded.provider,
			    provider_id = excluded.provider_id,
			    parsed_title = excluded.parsed_title, parsed_year = excluded.parsed_year,
			    decided_at = excluded.decided_at,
			    verdict = excluded.verdict, verdict_why = excluded.verdict_why,
			    searched_at = excluded.searched_at, updated_at = excluded.updated_at`,
			itemID, string(newState), provider, providerID,
			parsed.Title, nullYear(parsed.Year), decidedAt,
			string(res.Verdict), res.Why, now, now); err != nil {
			return fmt.Errorf("identify: saving a proposal: %w", err)
		}

		// Candidates are replaced wholesale. A merge would leave candidates
		// from an older search mixed with a newer one, and a person choosing
		// between them could pick something this software no longer considers
		// plausible.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM media_identification_candidate WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for i, c := range res.Ranked {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO media_identification_candidate
				    (item_id, provider, provider_id, rank, title, original_title,
				     year, overview, poster_path, score, why)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				itemID, "tmdb", c.Match.ProviderID, i,
				c.Match.Title, c.Match.OriginalTitle, nullYear(c.Match.Year),
				c.Match.Overview, c.Match.PosterPath, c.Score,
				strings.Join(c.Why, "; ")); err != nil {
				return fmt.Errorf("identify: saving a candidate: %w", err)
			}
		}
		return nil
	})
}

func nullYear(y int) any {
	if y <= 0 {
		return nil
	}
	return y
}

// Confirm attaches a provider id, on a person's authority.
//
// The id must be one of the candidates that were STORED — see the migration's
// comment. A person confirms what they were shown; accepting an arbitrary id
// here would make the confirmation screen advisory.
//
// Setting the library item's own title is the caller's job, not this one's:
// this records the decision, and rewriting what an operator browses to is a
// separate act that deserves to be visible at the call site.
func (s *Store) Confirm(ctx context.Context, itemID, providerID, byUser int64) (Candidate, error) {
	var chosen Candidate
	now := ts(s.now())

	err := s.db.InTx(ctx, func(tx db.Execer) error {
		row := tx.QueryRowContext(ctx, `
			SELECT rank, provider, provider_id, title, original_title,
			       COALESCE(year, 0), overview, poster_path, score, why
			FROM media_identification_candidate
			WHERE item_id = ? AND provider_id = ?`, itemID, providerID)
		if err := row.Scan(&chosen.Rank, &chosen.Provider, &chosen.ProviderID,
			&chosen.Title, &chosen.OriginalTitle, &chosen.Year, &chosen.Overview,
			&chosen.PosterPath, &chosen.Score, &chosen.Why); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %d was not offered for item %d",
					ErrNoSuchCandidate, providerID, itemID)
			}
			return err
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE media_identification
			SET state = ?, provider = ?, provider_id = ?, decided_by = ?,
			    decided_at = ?, updated_at = ?
			WHERE item_id = ?`,
			string(StateConfirmed), chosen.Provider, chosen.ProviderID,
			byUser, now, now, itemID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: item %d", ErrNotFound, itemID)
		}
		return nil
	})
	return chosen, err
}

// RecordChosen records an identification a person made by choosing a title from
// the provider directly — by adding it to the library (ADR-0025) — rather than
// by confirming a proposal.
//
// It leaves behind exactly what Confirm would have: state confirmed, the
// person's id, and the chosen title stored as the candidate they chose. Each of
// those is load-bearing — the pass never re-proposes a person's decision, the
// review screen can show and reopen it, and the poster route fetches artwork
// only for titles recorded as a candidate (RecordedPosterPath).
//
// It runs in the caller's transaction, because an added item and its
// identification exist together or not at all. It never overwrites: the item
// is new, and a row already there would be a bug worth failing the add over.
func (s *Store) RecordChosen(ctx context.Context, tx db.Execer, itemID int64, c Candidate, byUser int64) error {
	if itemID <= 0 || c.ProviderID <= 0 || strings.TrimSpace(c.Provider) == "" || byUser <= 0 {
		return fmt.Errorf("identify: a chosen title needs an item, a provider id and the person who chose it")
	}
	now := ts(s.now())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO media_identification
		    (item_id, state, provider, provider_id, parsed_title, parsed_year,
		     decided_by, decided_at, verdict, verdict_why, searched_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		itemID, string(StateConfirmed), c.Provider, c.ProviderID,
		// There was no parse. The title the item started with IS the
		// provider's, and that is what undoing this would put back.
		c.Title, nullYear(c.Year), byUser, now,
		string(VerdictChosen), "chosen from the provider when it was added to the library",
		now, now); err != nil {
		return fmt.Errorf("identify: recording the chosen title: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO media_identification_candidate
		    (item_id, provider, provider_id, rank, title, original_title,
		     year, overview, poster_path, score, why)
		VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, 0, ?)`,
		itemID, c.Provider, c.ProviderID, c.Title, c.OriginalTitle,
		nullYear(c.Year), c.Overview, c.PosterPath,
		"chosen by a person when the title was added"); err != nil {
		return fmt.Errorf("identify: recording the chosen title: %w", err)
	}
	return nil
}

// Reject records that none of the candidates is right.
//
// A decision, not an absence: it is stored with the person's id, so an
// automatic pass leaves it alone. An operator who has looked at an item and
// concluded the provider does not have it should not be asked again every time
// the pass runs.
func (s *Store) Reject(ctx context.Context, itemID, byUser int64, why string) error {
	now := ts(s.now())
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_identification
		SET state = ?, provider = '', provider_id = NULL, decided_by = ?,
		    decided_at = ?, verdict_why = ?, updated_at = ?
		WHERE item_id = ?`,
		string(StateNone), byUser, now, strings.TrimSpace(why), now, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: item %d", ErrNotFound, itemID)
	}
	return nil
}

// Reopen clears a decision so the item can be identified again.
//
// The explicit act that a human decision requires. It exists so that
// SaveProposal never needs an override flag: a caller that wants to redo a
// person's decision has to say so, by name, with a person behind it.
func (s *Store) Reopen(ctx context.Context, itemID int64) error {
	now := ts(s.now())
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_identification
		SET state = ?, provider = '', provider_id = NULL, decided_by = NULL,
		    decided_at = NULL, verdict = '', verdict_why = '', updated_at = ?
		WHERE item_id = ?`, string(StateUnidentified), now, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: item %d", ErrNotFound, itemID)
	}
	return nil
}

const selectIdentification = `
	SELECT item_id, state, provider, COALESCE(provider_id, 0),
	       parsed_title, COALESCE(parsed_year, 0),
	       decided_by, decided_at, verdict, verdict_why, searched_at, updated_at
	FROM media_identification`

func scanIdentification(row interface{ Scan(...any) error }) (*Identification, error) {
	var i Identification
	var state, verdict, updated string
	var decidedAt, searchedAt sql.NullString
	if err := row.Scan(&i.ItemID, &state, &i.Provider, &i.ProviderID,
		&i.ParsedTitle, &i.ParsedYear, &i.DecidedBy, &decidedAt,
		&verdict, &i.VerdictWhy, &searchedAt, &updated); err != nil {
		return nil, err
	}
	i.State, i.Verdict = State(state), Verdict(verdict)
	i.DecidedAt, i.SearchedAt = nullTime(decidedAt), nullTime(searchedAt)
	i.UpdatedAt = parseTS(updated)
	return &i, nil
}

// Get loads one identification with its candidates.
//
// Only for a title the caller may see (ADR-0037): the review screen is a read
// of the library like any other.
func (s *Store) Get(ctx context.Context, itemID int64) (*Identification, error) {
	visible, vargs := library.Visible(ctx, "")
	i, err := scanIdentification(s.db.QueryRowContext(ctx,
		selectIdentification+` WHERE item_id = ?
		  AND item_id IN (SELECT id FROM media_item WHERE `+visible+`)`,
		append([]any{itemID}, vargs...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: item %d", ErrNotFound, itemID)
	}
	if err != nil {
		return nil, err
	}
	if i.Candidates, err = s.candidates(ctx, itemID); err != nil {
		return nil, err
	}
	return i, nil
}

func (s *Store) candidates(ctx context.Context, itemID int64) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rank, provider, provider_id, title, original_title,
		       COALESCE(year, 0), overview, poster_path, score, why
		FROM media_identification_candidate
		WHERE item_id = ? ORDER BY rank`, itemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Candidate, 0, 5)
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Rank, &c.Provider, &c.ProviderID, &c.Title,
			&c.OriginalTitle, &c.Year, &c.Overview, &c.PosterPath,
			&c.Score, &c.Why); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListByState returns identifications in a given state, oldest first.
//
// Oldest first because this feeds a review queue, and a person working through
// one should not have yesterday's items pushed down by today's.
func (s *Store) ListByState(ctx context.Context, state State, limit int) ([]*Identification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	visible, vargs := library.Visible(ctx, "")
	rows, err := s.db.QueryContext(ctx,
		selectIdentification+` WHERE state = ?
		  AND item_id IN (SELECT id FROM media_item WHERE `+visible+`)
		ORDER BY updated_at LIMIT ?`,
		append(append([]any{string(state)}, vargs...), limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*Identification
	for rows.Next() {
		i, err := scanIdentification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, i := range out {
		if i.Candidates, err = s.candidates(ctx, i.ItemID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UnidentifiedItemIDs returns library items with nothing recorded yet.
//
// A LEFT JOIN rather than a state query, because an item created before this
// table existed — or created by an import a moment ago — has no row at all, and
// that is the commonest case on a first run over an existing library.
func (s *Store) UnidentifiedItemIDs(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Unscoped: the identification pass looks at the whole library.
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id FROM media_item i
		LEFT JOIN media_identification m ON m.item_id = i.id
		WHERE m.item_id IS NULL OR m.state = ?
		ORDER BY i.added_at LIMIT ?`, string(StateUnidentified), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ErrNotRecorded means this software has no record of that provider id, so
// there is nothing it is willing to go and fetch.
var ErrNotRecorded = errors.New("identify: no candidate with that provider id")

// RecordedPosterPath returns the remote poster path stored for a provider id.
//
// The lookup exists so that fetching a poster on demand can be driven by what
// this software ALREADY RECORDED rather than by anything in a request. A caller
// supplies a provider and an id; if no candidate row holds them, the answer is
// ErrNotRecorded and no outbound request is made. So the set of URLs this
// instance can be induced to fetch is exactly the set of posters belonging to
// candidates a search already returned — not an arbitrary path.
func (s *Store) RecordedPosterPath(ctx context.Context, provider string, providerID int64) (string, error) {
	var path string
	err := s.db.QueryRowContext(ctx, `
		SELECT poster_path FROM media_identification_candidate
		WHERE provider = ? AND provider_id = ? AND poster_path <> ''
		UNION ALL
		SELECT poster_path FROM offered_poster
		WHERE provider = ? AND provider_id = ?
		LIMIT 1`, provider, providerID, provider, providerID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotRecorded
	}
	if err != nil {
		return "", fmt.Errorf("identify: reading a candidate's poster: %w", err)
	}
	return path, nil
}

// OfferPosters records the posters a title search showed an account
// (ADR-0070), by provider id, so the poster route may fetch and serve them.
func (s *Store) OfferPosters(ctx context.Context, userID int64, provider string, posters map[int64]string) error {
	now := ts(s.now())
	for id, path := range posters {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO offered_poster (user_id, provider, provider_id, poster_path, offered_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (user_id, provider, provider_id)
			DO UPDATE SET poster_path = excluded.poster_path, offered_at = excluded.offered_at`,
			userID, provider, id, path, now); err != nil {
			return fmt.Errorf("identify: recording an offered poster: %w", err)
		}
	}
	return nil
}

// PosterOffered reports whether a search showed this account that poster.
func (s *Store) PosterOffered(ctx context.Context, userID int64, provider string, providerID int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM offered_poster
		WHERE user_id = ? AND provider = ? AND provider_id = ?`,
		userID, provider, providerID).Scan(&n); err != nil {
		return false, fmt.Errorf("identify: reading an offered poster: %w", err)
	}
	return n > 0, nil
}

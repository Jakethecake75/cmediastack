package request

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Errors the request surface distinguishes.
var (
	ErrNotFound = errors.New("request: no such request")
	// ErrTooManyOpen means this account has reached its cap of outstanding
	// requests. See Store.Create.
	ErrTooManyOpen = errors.New("request: too many outstanding requests")
	// ErrNotAskable means the title normalises to nothing, so it could not be
	// told apart from any other such request.
	ErrNotAskable = errors.New("request: that title is not something we can match on")
	// ErrAlreadyDecided means the request has already been approved or denied.
	ErrAlreadyDecided = errors.New("request: that request has already been decided")
	// ErrInvalid wraps every rejection of what the CALLER sent, so a handler can
	// answer 400 for those and keep 500 for the things that are genuinely this
	// software's fault. Without the distinction a missing field and a broken
	// database look identical to the person, and identical in the logs.
	ErrInvalid = errors.New("request: invalid")
	// ErrNotApproved means a request that is not approved was to be linked to
	// a library item (ADR-0028): a pending one has not been agreed to, and a
	// denied or fulfilled one is finished.
	ErrNotApproved = errors.New("request: only an approved request can be linked to a library item")
	// ErrNoSuchItem means the library item to link does not exist.
	ErrNoSuchItem = errors.New("request: no such library item")
	// ErrWrongKind means the item is not the kind of thing that was asked for:
	// a film request satisfied by a series, or the other way round.
	ErrWrongKind = errors.New("request: that library item is not the kind of thing that was asked for")
)

// State is where a request has got to.
type State string

const (
	StatePending   State = "pending"
	StateApproved  State = "approved"
	StateDenied    State = "denied"
	StateFulfilled State = "fulfilled"
)

// Action is what a request asks to be done (ADR-0075).
type Action string

const (
	ActionAdd    Action = "add"
	ActionRemove Action = "remove"
)

// Kind is what sort of thing was asked for.
type Kind string

const (
	KindMovie  Kind = "movie"
	KindSeries Kind = "series"
)

// Request is one thing somebody asked for.
type Request struct {
	ID    int64
	Kind  Kind
	Title string
	Year  int
	Note  string
	State State

	// TMDBID is the provider's title the requester chose, zero for a request
	// made in words (ADR-0075).
	TMDBID int64
	// Scope is the seasons and episodes asked for; empty is the whole title.
	Scope  string
	Action Action

	RequestedBy   *int64
	RequesterName string
	RequestedAt   time.Time

	DecidedBy      *int64
	DeciderName    string
	DecidedAt      *time.Time
	DecisionReason string

	InfoHash  string
	GrabbedAt *time.Time

	MediaItemID *int64
	FulfilledAt *time.Time

	// Followers is everyone waiting on this, the original requester included.
	Followers []Follower
}

// Follower is somebody waiting on a request.
type Follower struct {
	UserID   int64
	Username string
	Since    time.Time
}

// Open reports whether the request is still something that might be acquired.
func (r *Request) Open() bool {
	return r.State == StatePending || r.State == StateApproved
}

// Store is the request repository.
//
// It holds no permission checks. Authorization for this surface is about WHO
// may see and decide WHOSE request, which needs the actor — so it lives one
// layer up, in Service, where the actor is. A store method that silently
// filtered by principal would make the scoping invisible at the call site,
// which is how a listing ends up returning everybody's requests to everybody.
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

func nullTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// NewRequest is a submission.
type NewRequest struct {
	Kind  Kind
	Title string
	Year  int
	Note  string
	By    int64
	// TMDBID, Scope and Action as on Request (ADR-0075). Scope is canonical,
	// from FormatScope. A removal names the item it would remove.
	TMDBID      int64
	Scope       string
	Action      Action
	MediaItemID int64
}

// MaxOpenPerUser caps how many outstanding requests one account may hold.
//
// Not a rate limit — a rate limit bounds how fast somebody fills a queue, and
// this bounds how much of it one person can occupy at once, which is the thing
// that makes the queue useless to everybody else. Deliberately generous: the
// instance has about five users (§13), and a cap that a legitimate user hits
// teaches them the feature is broken.
const MaxOpenPerUser = 20

// Create records a request, or joins an existing open one for the same thing.
//
// The second return value reports which happened, because the caller must tell
// the person: "added" and "you have joined an existing request, currently
// approved and downloading" are very different answers, and collapsing them
// into a silent 201 is how somebody re-requests the same film four times.
func (s *Store) Create(ctx context.Context, nr NewRequest) (*Request, bool, error) {
	key := MatchKey(nr.Title, nr.Year)
	if key == "" {
		return nil, false, fmt.Errorf("%w: %q", ErrNotAskable, nr.Title)
	}
	// Part of a series, or a removal, is a different request from the whole
	// title fetched (ADR-0075).
	if nr.Scope != "" {
		key += "#" + nr.Scope
	}
	if nr.Action == "" {
		nr.Action = ActionAdd
	}
	if nr.Action == ActionRemove {
		key = "remove:" + key
	}

	var id int64
	var joined bool

	err := s.db.InTx(ctx, func(tx db.Execer) error {
		now := ts(s.now())

		// An OPEN request for the same thing already? Join it. The partial
		// unique index makes this the only outcome the database will allow
		// anyway; doing it explicitly means the person gets an explanation
		// rather than a constraint violation.
		var existing int64
		switch err := tx.QueryRowContext(ctx,
			`SELECT id FROM media_request WHERE match_key = ? AND state IN ('pending','approved')`,
			key).Scan(&existing); {
		case err == nil:
			id, joined = existing, true
		case !errors.Is(err, sql.ErrNoRows):
			return err
		default:
			// Counted inside the transaction, so two concurrent submissions
			// cannot both see room for the last one.
			var open int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM media_request
				WHERE requested_by = ? AND state IN ('pending','approved')`,
				nr.By).Scan(&open); err != nil {
				return err
			}
			if open >= MaxOpenPerUser {
				return fmt.Errorf("%w: %d already open", ErrTooManyOpen, open)
			}

			res, err := tx.ExecContext(ctx, `
				INSERT INTO media_request
				    (kind, title, year, note, match_key, state, requested_by,
				     requested_at, updated_at, tmdb_id, scope, action, media_item_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				string(nr.Kind), strings.TrimSpace(nr.Title), nullYear(nr.Year),
				strings.TrimSpace(nr.Note), key, string(StatePending), nr.By, now, now,
				nullID(nr.TMDBID), nr.Scope, string(nr.Action), nullID(nr.MediaItemID))
			if err != nil {
				return fmt.Errorf("request: create: %w", err)
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		}

		// The requester follows their own request, so "who is waiting" is one
		// query rather than a union with the requester column.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO media_request_follower (request_id, user_id, followed_at)
			VALUES (?, ?, ?)
			ON CONFLICT (request_id, user_id) DO NOTHING`, id, nr.By, now); err != nil {
			return fmt.Errorf("request: follow: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}

	r, err := s.ByID(ctx, id)
	return r, !joined, err
}

func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func nullYear(y int) any {
	if y <= 0 {
		return nil
	}
	return y
}

const selectRequest = `
	SELECT r.id, r.kind, r.title, COALESCE(r.year, 0), r.note, r.state,
	       r.requested_by, COALESCE(ru.username, ''), r.requested_at,
	       r.decided_by, COALESCE(du.username, ''), r.decided_at,
	       r.decision_reason, COALESCE(r.info_hash, ''), r.grabbed_at,
	       r.media_item_id, r.fulfilled_at,
	       COALESCE(r.tmdb_id, 0), r.scope, r.action
	FROM media_request r
	LEFT JOIN app_user ru ON ru.id = r.requested_by
	LEFT JOIN app_user du ON du.id = r.decided_by
`

func scanRequest(row interface{ Scan(...any) error }) (*Request, error) {
	var r Request
	var kind, state, requestedAt, action string
	var decidedAt, grabbedAt, fulfilledAt sql.NullString
	if err := row.Scan(&r.ID, &kind, &r.Title, &r.Year, &r.Note, &state,
		&r.RequestedBy, &r.RequesterName, &requestedAt,
		&r.DecidedBy, &r.DeciderName, &decidedAt,
		&r.DecisionReason, &r.InfoHash, &grabbedAt,
		&r.MediaItemID, &fulfilledAt,
		&r.TMDBID, &r.Scope, &action); err != nil {
		return nil, err
	}
	r.Kind, r.State, r.Action = Kind(kind), State(state), Action(action)
	r.RequestedAt = parseTS(requestedAt)
	r.DecidedAt, r.GrabbedAt, r.FulfilledAt = nullTS(decidedAt), nullTS(grabbedAt), nullTS(fulfilledAt)
	return &r, nil
}

// ByID loads one request and its followers.
func (s *Store) ByID(ctx context.Context, id int64) (*Request, error) {
	r, err := scanRequest(s.db.QueryRowContext(ctx, selectRequest+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if r.Followers, err = s.followers(ctx, id); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) followers(ctx context.Context, id int64) ([]Follower, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.user_id, COALESCE(u.username, ''), f.followed_at
		FROM media_request_follower f
		LEFT JOIN app_user u ON u.id = f.user_id
		WHERE f.request_id = ? ORDER BY f.followed_at`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Follower, 0, 2)
	for rows.Next() {
		var f Follower
		var since string
		if err := rows.Scan(&f.UserID, &f.Username, &since); err != nil {
			return nil, err
		}
		f.Since = parseTS(since)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Filter narrows a listing.
type Filter struct {
	// OnlyUser restricts to requests this account opened or follows. Set by the
	// service for an actor who may not see everybody's.
	OnlyUser *int64
	// States, empty for all.
	States []State
	Limit  int
}

// List reads requests, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]*Request, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}

	q := selectRequest + ` WHERE 1=1`
	var args []any

	if f.OnlyUser != nil {
		// Opened by them OR followed by them: somebody who joined an existing
		// request must still see it, or joining looks like nothing happened.
		q += ` AND (r.requested_by = ? OR EXISTS (
		           SELECT 1 FROM media_request_follower f
		           WHERE f.request_id = r.id AND f.user_id = ?))`
		args = append(args, *f.OnlyUser, *f.OnlyUser)
	}
	if len(f.States) > 0 {
		q += ` AND r.state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.States)), ",") + `)`
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	q += ` ORDER BY r.requested_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Request, 0)
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		if r.Followers, err = s.followers(ctx, r.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Decide approves or denies a pending request.
//
// Only a PENDING request can be decided, and the check is in the UPDATE's WHERE
// clause rather than in a read-then-write: two approvers clicking at once must
// not both succeed and leave two audit lines claiming to be the decision.
func (s *Store) Decide(ctx context.Context, id int64, state State, deciderID int64, reason string) error {
	now := ts(s.now())
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_request
		SET state = ?, decided_by = ?, decided_at = ?, decision_reason = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		string(state), deciderID, now, strings.TrimSpace(reason), now, id, string(StatePending))
	if err != nil {
		return fmt.Errorf("request: decide: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either it does not exist or it was not pending. Distinguished for the
		// caller, because "already approved" and "no such request" need
		// different answers.
		if _, err := s.ByID(ctx, id); err != nil {
			return err
		}
		return ErrAlreadyDecided
	}
	return nil
}

// LinkGrab records that a download was started for this request.
//
// Only an APPROVED request can be linked. A grab against a pending one would
// mean the instance acquired something nobody had approved, which is the exact
// thing the approval step exists to prevent.
func (s *Store) LinkGrab(ctx context.Context, id int64, infoHash string) error {
	now := ts(s.now())
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_request
		SET info_hash = ?, grabbed_at = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		strings.ToLower(infoHash), now, now, id, string(StateApproved))
	if err != nil {
		return fmt.Errorf("request: link grab: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if _, err := s.ByID(ctx, id); err != nil {
			return err
		}
		return fmt.Errorf("%w: only an approved request can be grabbed for", ErrAlreadyDecided)
	}
	return nil
}

// Fulfil closes out every approved request this import satisfies: the ones
// waiting on this info hash, and the ones linked to the item it was imported
// into (ADR-0028).
//
// Called by the importer when a download becomes a library item. It takes what
// the importer knows — a hash and an item — rather than a request id, because
// the importer knows what it imported and not what anybody asked for, and
// looking the requests up here keeps the importer from having to know this
// table exists at all.
//
// A request found by its hash records the item the download became, whatever it
// was linked to: that is where what was grabbed for it actually went.
func (s *Store) Fulfil(ctx context.Context, infoHash string, mediaItemID int64) (int64, error) {
	now := ts(s.now())
	hash := strings.ToLower(strings.TrimSpace(infoHash))
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_request
		SET state = ?, media_item_id = ?, fulfilled_at = ?, updated_at = ?
		WHERE state = ? AND action = 'add'
		  AND ((? <> '' AND info_hash = ?) OR media_item_id = ?)`,
		string(StateFulfilled), mediaItemID, now, now,
		string(StateApproved), hash, hash, mediaItemID)
	if err != nil {
		return 0, fmt.Errorf("request: fulfil: %w", err)
	}
	return res.RowsAffected()
}

// MarkFulfilled closes an approved request whose work is done: a removal,
// once its files are in the trash (ADR-0075).
func (s *Store) MarkFulfilled(ctx context.Context, id int64) error {
	now := ts(s.now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE media_request SET state = ?, fulfilled_at = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		string(StateFulfilled), now, now, id, string(StateApproved))
	if err != nil {
		return fmt.Errorf("request: fulfil: %w", err)
	}
	return nil
}

// LinkedItem is what Store.Link did.
type LinkedItem struct {
	// Previous is the item the request was linked to before, if it was.
	Previous *int64
	// Label is the item's title and year, as the audit line and the answer
	// name it.
	Label string
	// Fulfilled is set when the item already had a file, so the link closed
	// the request there and then.
	Fulfilled bool
}

// Link records which library item satisfies an approved request (ADR-0028),
// and fulfils the request at once when that item already has a file.
//
// One transaction: the request's state and kind, the item's kind, the link and
// the fulfilment are read and written together, so a request cannot be linked
// to an item of the other kind, or after it stopped being approved.
//
// The item is read here, from its own table, rather than described by the
// caller: a store that trusted a kind it was handed would link whatever a
// buggy caller said.
func (s *Store) Link(ctx context.Context, id, itemID int64) (LinkedItem, error) {
	// Unscoped: LinkRequest refuses a title the approver cannot see before it gets here (ADR-0037).
	var out LinkedItem
	err := s.db.InTx(ctx, func(tx db.Execer) error {
		var kind, state string
		var current sql.NullInt64
		switch err := tx.QueryRowContext(ctx,
			`SELECT kind, state, media_item_id FROM media_request WHERE id = ?`, id).
			Scan(&kind, &state, &current); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		}
		if State(state) != StateApproved {
			return fmt.Errorf("%w; this one is %s", ErrNotApproved, state)
		}

		var itemKind, title string
		var year sql.NullInt64
		switch err := tx.QueryRowContext(ctx,
			`SELECT kind, title, year FROM media_item WHERE id = ?`, itemID).
			Scan(&itemKind, &title, &year); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: %d", ErrNoSuchItem, itemID)
		case err != nil:
			return err
		}
		if itemKind != kind {
			return fmt.Errorf("%w: this is a %s request, and item %d is a %s",
				ErrWrongKind, noun(kind), itemID, noun(itemKind))
		}
		out.Label = title
		if year.Valid && year.Int64 > 0 {
			out.Label = fmt.Sprintf("%s (%d)", title, year.Int64)
		}
		if current.Valid {
			prev := current.Int64
			out.Previous = &prev
		}

		now := ts(s.now())
		if _, err := tx.ExecContext(ctx, `
			UPDATE media_request SET media_item_id = ?, updated_at = ?
			WHERE id = ? AND state = ?`,
			itemID, now, id, string(StateApproved)); err != nil {
			return fmt.Errorf("request: link: %w", err)
		}
		// Already on disk: the thing asked for is in the library, which is all
		// "fulfilled" claims.
		res, err := tx.ExecContext(ctx, `
			UPDATE media_request SET state = ?, fulfilled_at = ?, updated_at = ?
			WHERE id = ? AND state = ? AND action = 'add'
			  -- Part of a series is not there because another part is
			  -- (ADR-0075): it is fulfilled when a file arrives.
			  AND scope = ''
			  AND EXISTS (SELECT 1 FROM media_file WHERE item_id = ?)`,
			string(StateFulfilled), now, now, id, string(StateApproved), itemID)
		if err != nil {
			return fmt.Errorf("request: link: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		out.Fulfilled = n > 0
		return nil
	})
	if err != nil {
		return LinkedItem{}, err
	}
	return out, nil
}

// FulfilOnDisk closes every approved request linked to an item that now has a
// file, and returns their ids (ADR-0028, decision 3).
//
// An import into a linked item closes its requests itself, through Fulfil. This
// is for the file an import did not bring: one put in the item's folder by hand
// and recorded by a scan. Without it such a request would say "nothing on disk
// yet" beside a film that plays, and go on counting against its requester's
// open requests.
func (s *Store) FulfilOnDisk(ctx context.Context) ([]int64, error) {
	var ids []int64
	err := s.db.InTx(ctx, func(tx db.Execer) error {
		// Read, and the rows closed, before anything is written on the same
		// transaction.
		var err error
		if ids, err = linkedOnDisk(ctx, tx); err != nil {
			return err
		}
		now := ts(s.now())
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `
				UPDATE media_request SET state = ?, fulfilled_at = ?, updated_at = ?
				WHERE id = ? AND state = ?`,
				string(StateFulfilled), now, now, id, string(StateApproved)); err != nil {
				return fmt.Errorf("request: fulfil: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// linkedOnDisk lists the approved requests linked to an item that has a file.
func linkedOnDisk(ctx context.Context, tx db.Execer) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM media_request
		WHERE state = ? AND action = 'add' AND media_item_id IS NOT NULL
		  AND EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = media_request.media_item_id)
		ORDER BY id`, string(StateApproved))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// noun is what a kind is called in a sentence.
func noun(kind string) string {
	if kind == string(KindMovie) {
		return "film"
	}
	return kind
}

// CountOpen reports how many requests are waiting on a decision.
func (s *Store) CountOpen(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_request WHERE state = ?`, string(StatePending)).Scan(&n)
	return n, err
}

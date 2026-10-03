package request

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// An approved request is satisfied by a library item (ADR-0028): an approver
// says which, and the request is fulfilled when that item has a file — however
// the file got there.

var linkNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

type linkRig struct {
	t        *testing.T
	db       *db.DB
	svc      *Service
	audit    *audit.Logger
	approver context.Context
	user     context.Context
	film     int64
	series   int64
	films    int64
}

func newLinkRig(t *testing.T) *linkRig {
	t.Helper()
	database, err := db.Open(db.Options{Path: t.TempDir() + "/r.db", BusyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := linkNow.Format(time.RFC3339Nano)
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := database.ExecContext(t.Context(), q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	role := exec(`INSERT INTO role (name, rank, builtin, created_at, updated_at) VALUES ('Admin', 100, 1, ?, ?)`, now, now)
	for _, name := range []string{"jacob", "sam"} {
		exec(`INSERT INTO app_user (username, email, password_hash, state, role_id, rating_ceiling, created_at, updated_at)
		      VALUES (?, ?, 'x', 'active', ?, 0, ?, ?)`, name, name+"@example.com", role, now, now)
	}
	films := exec(`INSERT INTO root_folder (path, kind, label, created_at, updated_at) VALUES ('/media/movies', 'movies', 'Films', ?, ?)`, now, now)
	tv := exec(`INSERT INTO root_folder (path, kind, label, created_at, updated_at) VALUES ('/media/tv', 'series', 'TV', ?, ?)`, now, now)

	r := &linkRig{t: t, db: database, films: films}
	r.film = r.item("movie", "Dune", 2021, films)
	r.series = r.item("series", "Severance", 2022, tv)

	r.audit = audit.New(database, func() time.Time { return linkNow })
	r.svc = NewService(NewStore(database, func() time.Time { return linkNow }), r.audit, func() time.Time { return linkNow })

	perms := func(ps ...authz.Permission) authz.Role {
		return authz.Role{ID: 1, Name: "role", Rank: 50, Permissions: authz.NewPermissionSet(ps...)}
	}
	r.approver = authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "jacob",
		State: authz.StateActive, MFASatisfied: true,
		Role: perms(authz.PermSubmitRequest, authz.PermApproveRequests, authz.PermBrowse)})
	r.user = authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 2, Username: "sam",
		State: authz.StateActive, MFASatisfied: true,
		Role: perms(authz.PermSubmitRequest, authz.PermBrowse)})
	return r
}

func (r *linkRig) item(kind, title string, year int, root int64) int64 {
	r.t.Helper()
	now := linkNow.Format(time.RFC3339Nano)
	res, err := r.db.ExecContext(r.t.Context(), `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder, added_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, kind, title, year, strings.ToLower(title), root,
		fmt.Sprintf("%s (%d)", title, year), now, now)
	if err != nil {
		r.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// file records a file of an item, as an import or a scan would.
func (r *linkRig) file(item int64, name string) {
	r.t.Helper()
	var root int64
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT root_folder_id FROM media_item WHERE id = ?`, item).Scan(&root); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.db.ExecContext(r.t.Context(), `
		INSERT INTO media_file (item_id, root_folder_id, relative_path, size_bytes, quality, imported_at)
		VALUES (?, ?, ?, 1, 'Bluray-1080p', ?)`, item, root, name, linkNow.Format(time.RFC3339Nano)); err != nil {
		r.t.Fatal(err)
	}
}

// approved submits a request as sam and approves it as jacob.
func (r *linkRig) approved(kind Kind, title string, year int) int64 {
	r.t.Helper()
	id := r.pending(kind, title, year)
	if _, err := r.svc.Approve(r.approver, id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *linkRig) pending(kind Kind, title string, year int) int64 {
	r.t.Helper()
	res, err := r.svc.Submit(r.user, NewRequest{Kind: kind, Title: title, Year: year})
	if err != nil {
		r.t.Fatal(err)
	}
	return res.Request.ID
}

func (r *linkRig) request(id int64) *Request {
	r.t.Helper()
	got, err := r.svc.Store().ByID(r.t.Context(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

// events returns the audit lines for one action, as "actor: detail".
func (r *linkRig) events(action audit.Action) []string {
	r.t.Helper()
	rows, err := r.db.QueryContext(r.t.Context(),
		`SELECT actor_label, detail FROM audit_event WHERE action = ? ORDER BY id`, string(action))
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var actor, detail string
		if err := rows.Scan(&actor, &detail); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, actor+": "+detail)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func itemOf(r *Request) int64 {
	if r.MediaItemID == nil {
		return 0
	}
	return *r.MediaItemID
}

func TestAnApprovedRequestIsLinkedToTheItemThatSatisfiesIt(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)

	got, err := r.svc.Link(r.approver, id, r.film)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fulfilled || got.Previous != nil || got.Label != "Dune (2021)" {
		t.Errorf("linked = %+v, want an unfulfilled first link to Dune (2021)", got)
	}
	// Linked, and still approved: nothing of it is on disk.
	if rq := r.request(id); rq.State != StateApproved || itemOf(rq) != r.film || rq.FulfilledAt != nil {
		t.Errorf("request = state %s, item %d, fulfilled %v; want approved, linked to %d, not fulfilled",
			rq.State, itemOf(rq), rq.FulfilledAt, r.film)
	}
	want := []string{`jacob: movie "Dune" is satisfied by library item 1, Dune (2021)`}
	if ev := r.events(audit.ActionRequestLinked); len(ev) != 1 || ev[0] != want[0] {
		t.Errorf("audit = %q, want %q", ev, want)
	}
	if ev := r.events(audit.ActionRequestFulfilled); len(ev) != 0 {
		t.Errorf("a link with nothing on disk recorded a fulfilment: %q", ev)
	}
}

// A pending request has not been agreed to, and a denied or fulfilled one is
// finished: none of them can be linked, and refusing changes nothing.
func TestOnlyAnApprovedRequestCanBeLinked(t *testing.T) {
	r := newLinkRig(t)
	pending := r.pending(KindMovie, "Dune", 2021)
	denied := r.pending(KindMovie, "Arrival", 2016)
	if _, err := r.svc.Deny(r.approver, denied, "not on this instance"); err != nil {
		t.Fatal(err)
	}
	fulfilled := r.approved(KindMovie, "Amelie", 2001)
	if _, err := r.svc.Link(r.approver, fulfilled, r.film); err != nil {
		t.Fatal(err)
	}
	r.file(r.film, "Dune (2021)/Dune (2021).mkv")
	if n, err := r.svc.FulfilFromImport(t.Context(), "", r.film); err != nil || n != 1 {
		t.Fatalf("fulfil = %d, %v", n, err)
	}

	for name, id := range map[string]int64{"pending": pending, "denied": denied, "fulfilled": fulfilled} {
		before := itemOf(r.request(id))
		_, err := r.svc.Link(r.approver, id, r.film)
		if !errors.Is(err, ErrNotApproved) || !strings.Contains(err.Error(), "this one is "+name) {
			t.Errorf("%s: err = %v, want ErrNotApproved naming the state", name, err)
		}
		if after := itemOf(r.request(id)); after != before {
			t.Errorf("%s: a refused link moved the item from %d to %d", name, before, after)
		}
	}
	if _, err := r.svc.Link(r.approver, 9999, r.film); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown request: %v, want ErrNotFound", err)
	}
}

// A film request is satisfied by a film and a series request by a series; an
// item that does not exist satisfies nothing.
func TestARequestIsLinkedOnlyToAnItemOfItsKind(t *testing.T) {
	r := newLinkRig(t)
	film := r.approved(KindMovie, "Dune", 2021)
	series := r.approved(KindSeries, "Severance", 2022)

	if _, err := r.svc.Link(r.approver, film, r.series); !errors.Is(err, ErrWrongKind) ||
		!strings.Contains(err.Error(), "this is a film request, and item 2 is a series") {
		t.Errorf("a film request linked to a series: %v", err)
	}
	if _, err := r.svc.Link(r.approver, series, r.film); !errors.Is(err, ErrWrongKind) ||
		!strings.Contains(err.Error(), "this is a series request, and item 1 is a film") {
		t.Errorf("a series request linked to a film: %v", err)
	}
	if _, err := r.svc.Link(r.approver, film, 9999); !errors.Is(err, ErrNoSuchItem) {
		t.Errorf("an unknown item: %v, want ErrNoSuchItem", err)
	}
	if _, err := r.svc.Link(r.approver, film, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("item 0: %v, want ErrInvalid", err)
	}
	for _, id := range []int64{film, series} {
		if rq := r.request(id); rq.MediaItemID != nil {
			t.Errorf("request %d was linked to %d by a refused call", id, *rq.MediaItemID)
		}
	}
	if _, err := r.svc.Link(r.approver, series, r.series); err != nil {
		t.Errorf("a series request linked to the series: %v", err)
	}
}

// Saying which film a request meant is part of deciding it: the requester may
// not, however much they would like to.
func TestLinkingARequestNeedsThePermissionToApprove(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)
	if _, err := r.svc.Link(r.user, id, r.film); !authz.IsDenied(err) {
		t.Fatalf("the requester linked their own request: %v", err)
	}
	if rq := r.request(id); rq.MediaItemID != nil {
		t.Errorf("a denied link changed the request: %d", *rq.MediaItemID)
	}
}

// A wrong identification must be correctable while the request is open, and
// the audit line says what it replaced.
func TestALinkCanBeChangedWhileTheRequestIsOpen(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 0)
	other := r.item("movie", "Dune", 1984, r.films)

	if _, err := r.svc.Link(r.approver, id, other); err != nil {
		t.Fatal(err)
	}
	got, err := r.svc.Link(r.approver, id, r.film)
	if err != nil {
		t.Fatal(err)
	}
	if got.Previous == nil || *got.Previous != other {
		t.Errorf("previous = %v, want %d", got.Previous, other)
	}
	if rq := r.request(id); itemOf(rq) != r.film {
		t.Errorf("linked to %d, want %d", itemOf(rq), r.film)
	}
	ev := r.events(audit.ActionRequestLinked)
	if len(ev) != 2 || !strings.HasSuffix(ev[1], "Dune (2021) (it was linked to item 3)") {
		t.Errorf("audit = %q", ev)
	}
}

// Whichever grab brought the file — one that named no request, from the film's
// own page — an import into the linked item fulfils the request.
func TestAnImportIntoTheLinkedItemFulfilsTheRequest(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)
	unlinked := r.approved(KindMovie, "Arrival", 2016)
	if _, err := r.svc.Link(r.approver, id, r.film); err != nil {
		t.Fatal(err)
	}
	other := r.item("movie", "Amelie", 2001, r.films)

	if n, err := r.svc.FulfilFromImport(t.Context(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", other); err != nil || n != 0 {
		t.Fatalf("an import into another item fulfilled %d request(s): %v", n, err)
	}
	if n, err := r.svc.FulfilFromImport(t.Context(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", r.film); err != nil || n != 1 {
		t.Fatalf("the import into the linked item fulfilled %d request(s), %v; want 1", n, err)
	}
	if rq := r.request(id); rq.State != StateFulfilled || itemOf(rq) != r.film || rq.FulfilledAt == nil {
		t.Errorf("request = %s, item %d; want fulfilled by %d", rq.State, itemOf(rq), r.film)
	}
	if rq := r.request(unlinked); rq.State != StateApproved {
		t.Errorf("a request linked to nothing was fulfilled: %s", rq.State)
	}
}

// ADR-0017's path still works: a grab that named the request fulfils it by its
// hash, and records the item the download became — even when the request was
// linked to another, because that is where what was grabbed for it went. An
// empty hash matches no request, whatever its hash column holds.
func TestAGrabThatNamedTheRequestStillFulfilsItByItsHash(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)
	blank := r.approved(KindMovie, "Arrival", 2016)
	if _, err := r.svc.Link(r.approver, id, r.film); err != nil {
		t.Fatal(err)
	}
	const hash = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	if err := r.svc.Store().LinkGrab(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Store().LinkGrab(t.Context(), blank, ""); err != nil {
		t.Fatal(err)
	}
	other := r.item("movie", "Amelie", 2001, r.films)

	if n, err := r.svc.FulfilFromImport(t.Context(), "", other); err != nil || n != 0 {
		t.Errorf("an import with no hash fulfilled %d request(s): %v", n, err)
	}
	if n, err := r.svc.FulfilFromImport(t.Context(), strings.ToLower(hash), other); err != nil || n != 1 {
		t.Fatalf("fulfil by hash = %d, %v", n, err)
	}
	if rq := r.request(id); rq.State != StateFulfilled || itemOf(rq) != other {
		t.Errorf("request = %s, item %d; want fulfilled by %d", rq.State, itemOf(rq), other)
	}
	if rq := r.request(blank); rq.State != StateApproved {
		t.Errorf("a request with a blank hash was fulfilled: %s", rq.State)
	}
}

// Linking to an item that already has a file fulfils the request there and
// then — the thing asked for is in the library — and the approver is on record
// for both.
func TestLinkingAnItemAlreadyOnDiskFulfilsTheRequestAtOnce(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)
	r.file(r.film, "Dune (2021)/Dune (2021).mkv")

	got, err := r.svc.Link(r.approver, id, r.film)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fulfilled || got.Request.State != StateFulfilled {
		t.Errorf("linked = fulfilled %v, state %s; want fulfilled", got.Fulfilled, got.Request.State)
	}
	if rq := r.request(id); rq.State != StateFulfilled || itemOf(rq) != r.film || rq.FulfilledAt == nil {
		t.Errorf("request = %s, item %d", rq.State, itemOf(rq))
	}
	want := "jacob: fulfilled on linking: Dune (2021) already has a file"
	if ev := r.events(audit.ActionRequestFulfilled); len(ev) != 1 || ev[0] != want {
		t.Errorf("audit = %q, want %q", ev, want)
	}
}

// A file put in the linked item's folder by hand, and recorded by a scan,
// fulfils the request too. Nothing else is touched, and nothing twice.
func TestAFileAScanRecordedFulfilsTheLinkedRequest(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)
	unlinked := r.approved(KindMovie, "Arrival", 2016)
	series := r.approved(KindSeries, "Severance", 2022)
	for _, l := range [][2]int64{{id, r.film}, {series, r.series}} {
		if _, err := r.svc.Link(r.approver, l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := r.svc.FulfilOnDisk(t.Context()); err != nil || n != 0 {
		t.Fatalf("with nothing on disk, %d request(s) fulfilled: %v", n, err)
	}
	r.file(r.film, "Dune (2021)/Dune.2021.mkv")
	if n, err := r.svc.FulfilOnDisk(t.Context()); err != nil || n != 1 {
		t.Fatalf("fulfilled %d, %v; want 1", n, err)
	}
	if rq := r.request(id); rq.State != StateFulfilled || itemOf(rq) != r.film {
		t.Errorf("request = %s, item %d", rq.State, itemOf(rq))
	}
	for _, other := range []int64{unlinked, series} {
		if rq := r.request(other); rq.State != StateApproved {
			t.Errorf("request %d = %s, want approved", other, rq.State)
		}
	}
	if n, _ := r.svc.FulfilOnDisk(t.Context()); n != 0 {
		t.Errorf("a second pass fulfilled %d again", n)
	}
	want := "system:scan: fulfilled: a scan recorded a file of the library item it is linked to"
	if ev := r.events(audit.ActionRequestFulfilled); len(ev) != 1 || ev[0] != want {
		t.Errorf("audit = %q, want %q", ev, want)
	}
}

// Deleting the item a request is linked to leaves the request approved and
// linked to nothing — which is what it then is (migration 0017).
func TestDeletingTheLinkedItemUnlinksTheRequest(t *testing.T) {
	r := newLinkRig(t)
	id := r.approved(KindMovie, "Dune", 2021)
	if _, err := r.svc.Link(r.approver, id, r.film); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(t.Context(), `DELETE FROM media_item WHERE id = ?`, r.film); err != nil {
		t.Fatal(err)
	}
	if rq := r.request(id); rq.State != StateApproved || rq.MediaItemID != nil {
		t.Errorf("request = %s, item %v; want approved and unlinked", rq.State, rq.MediaItemID)
	}
}

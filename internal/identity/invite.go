package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Invite errors.
var (
	ErrInviteInvalid  = errors.New("identity: invite code is invalid, expired or already used")
	ErrInviteRequired = errors.New("identity: an invite code is required")
)

// Invite is a pre-authorised account grant.
//
// §7.2 calls invites the recommended default path, because the trust decision
// is made when the code is issued rather than when it is redeemed. That is why
// an invite carries the role, the library grants and the rating ceiling: those
// are the decision, and they are fixed at issuance.
type Invite struct {
	ID            int64
	RoleID        int64
	RoleName      string
	AllLibraries  bool
	LibraryIDs    []int64
	RatingCeiling int
	AutoApprove   bool
	IssuedBy      int64
	IssuerRank    int
	Note          string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	RedeemedAt    *time.Time
	RedeemedBy    *int64
	RevokedAt     *time.Time
}

// Active reports whether the invite can still be redeemed.
func (i *Invite) Active(now time.Time) bool {
	return i.RedeemedAt == nil && i.RevokedAt == nil && now.Before(i.ExpiresAt)
}

func hashInviteCode(code string) string {
	sum := sha256.Sum256([]byte(normalizeInviteCode(code)))
	return hex.EncodeToString(sum[:])
}

func normalizeInviteCode(code string) string {
	return strings.TrimSpace(code)
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// CreateInvite stores an invite and returns the one-time code.
//
// The code is 256 bits from crypto/rand and is returned exactly once. Only its
// SHA-256 is stored, so a database copy does not yield usable codes; a fast
// hash is correct here because there is nothing to brute force.
func (s *Store) CreateInvite(ctx context.Context, inv Invite) (code string, id int64, err error) {
	code, err = randomToken(32)
	if err != nil {
		return "", 0, err
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO invite (code_hash, role_id, all_libraries, library_ids_json, rating_ceiling,
		                    auto_approve, issued_by_user_id, issuer_rank, note, created_at,
		                    expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashInviteCode(code), inv.RoleID, boolInt(inv.AllLibraries), marshalIDs(inv.LibraryIDs),
		inv.RatingCeiling,
		inv.AutoApprove, inv.IssuedBy, inv.IssuerRank, nullStr(inv.Note),
		ts(inv.CreatedAt), ts(inv.ExpiresAt))
	if err != nil {
		return "", 0, fmt.Errorf("identity: create invite: %w", err)
	}
	id, err = res.LastInsertId()
	return code, id, err
}

const inviteSelect = `
SELECT i.id, i.role_id, r.name, i.all_libraries, i.library_ids_json, i.rating_ceiling,
       i.auto_approve,
       i.issued_by_user_id, i.issuer_rank, COALESCE(i.note,''), i.created_at, i.expires_at,
       i.redeemed_at, i.redeemed_by_user_id, i.revoked_at
FROM invite i JOIN role r ON r.id = i.role_id `

func scanInvite(row interface{ Scan(...any) error }) (*Invite, error) {
	var inv Invite
	var libsJSON, created, expires string
	var redeemedAt, revokedAt sql.NullString
	var redeemedBy sql.NullInt64
	var all int

	err := row.Scan(&inv.ID, &inv.RoleID, &inv.RoleName, &all, &libsJSON, &inv.RatingCeiling,
		&inv.AutoApprove, &inv.IssuedBy, &inv.IssuerRank, &inv.Note, &created, &expires,
		&redeemedAt, &redeemedBy, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(libsJSON), &inv.LibraryIDs)
	inv.AllLibraries = all == 1
	inv.CreatedAt, inv.ExpiresAt = parseTS(created), parseTS(expires)
	inv.RedeemedAt = parseTSPtr(redeemedAt)
	inv.RevokedAt = parseTSPtr(revokedAt)
	if redeemedBy.Valid {
		v := redeemedBy.Int64
		inv.RedeemedBy = &v
	}
	return &inv, nil
}

// InviteByCode looks an invite up by its code. It returns ErrInviteInvalid for
// unknown, redeemed, revoked and expired codes alike, so a probe cannot tell
// which of those it hit.
func (s *Store) InviteByCode(ctx context.Context, code string) (*Invite, error) {
	if normalizeInviteCode(code) == "" {
		return nil, ErrInviteInvalid
	}
	inv, err := scanInvite(s.db.QueryRowContext(ctx, inviteSelect+`WHERE i.code_hash = ?`, hashInviteCode(code)))
	if err != nil {
		return nil, ErrInviteInvalid
	}
	if !inv.Active(s.now()) {
		return nil, ErrInviteInvalid
	}
	return inv, nil
}

// RedeemInvite marks an invite used, atomically.
//
// The UPDATE is conditional on the invite still being unredeemed, so two
// simultaneous redemptions cannot both succeed: single-use is enforced by the
// database, not by a check-then-act in application code.
func (s *Store) RedeemInvite(ctx context.Context, inviteID, userID int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE invite SET redeemed_at = ?, redeemed_by_user_id = ?
		WHERE id = ? AND redeemed_at IS NULL AND revoked_at IS NULL`,
		ts(s.now()), userID, inviteID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInviteInvalid
	}
	return nil
}

// ListInvites returns invites for the management UI. Codes are never returned:
// only their hashes are stored, so they cannot be.
func (s *Store) ListInvites(ctx context.Context) ([]Invite, error) {
	if err := authz.RequirePermission(ctx, authz.PermIssueInvites); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, inviteSelect+`ORDER BY i.created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// RevokeInvite cancels an unredeemed invite.
func (s *Store) RevokeInvite(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE invite SET revoked_at = ? WHERE id = ? AND redeemed_at IS NULL AND revoked_at IS NULL`,
		ts(s.now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeInvitesIssuedBy cancels every outstanding invite a user issued.
//
// This closes a real gap: an invite carries a pre-approved role, and the trust
// decision behind it belongs to the issuer. If that issuer is suspended, their
// outstanding invites are pending grants made on authority they no longer have,
// so they die with their issuer's access.
func (s *Store) RevokeInvitesIssuedBy(ctx context.Context, userID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE invite SET revoked_at = ?
		WHERE issued_by_user_id = ? AND redeemed_at IS NULL AND revoked_at IS NULL`,
		ts(s.now()), userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeExpiredInvites removes dead invites.
func (s *Store) PurgeExpiredInvites(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM invite WHERE redeemed_at IS NULL AND expires_at < ?`, ts(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func marshalIDs(ids []int64) string {
	if ids == nil {
		ids = []int64{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// IssueInviteInput describes an invite to create.
type IssueInviteInput struct {
	RoleID int64
	// AllLibraries and LibraryIDs are what the account will see (ADR-0037):
	// every library, or these root folders.
	AllLibraries  bool
	LibraryIDs    []int64
	RatingCeiling int
	AutoApprove   bool
	TTL           time.Duration
	Note          string
	SourceIP      string
	UserAgent     string
}

// IssueInvite creates a single-use invite and returns its code once.
//
// The same escalation guard that governs approval governs issuance: an invite
// is an approval made in advance, so a Manager cannot issue an Admin invite for
// exactly the reason they cannot approve one.
func (svc *Service) IssueInvite(ctx context.Context, in IssueInviteInput) (string, *Invite, error) {
	actor := authz.FromContext(ctx)

	if err := authz.RequirePermission(ctx, authz.PermIssueInvites); err != nil {
		return "", nil, err
	}

	role, err := svc.store.RoleByID(ctx, in.RoleID)
	if err != nil {
		return "", nil, err
	}
	if err := authz.CanAssignRole(ctx, role); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "invite.issue", d, in.SourceIP, in.UserAgent)
		}
		return "", nil, err
	}
	// An invite is an approval made in advance, so its grant is held to the
	// same rule: no wider than the issuer's own (ADR-0037).
	if err := authz.CanGrant(ctx, authz.Grant{AllLibraries: in.AllLibraries,
		RootFolderIDs: in.LibraryIDs, RatingCeiling: in.RatingCeiling}); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "invite.issue", d, in.SourceIP, in.UserAgent)
		}
		return "", nil, err
	}
	if in.AllLibraries {
		in.LibraryIDs = nil
	}
	if err := svc.store.RootFoldersExist(ctx, in.LibraryIDs); err != nil {
		return "", nil, err
	}

	if in.TTL <= 0 {
		in.TTL = 7 * 24 * time.Hour
	}
	now := svc.now()

	inv := Invite{
		RoleID:        role.ID,
		RoleName:      role.Name,
		AllLibraries:  in.AllLibraries,
		LibraryIDs:    in.LibraryIDs,
		RatingCeiling: in.RatingCeiling,
		AutoApprove:   in.AutoApprove,
		IssuedBy:      actor.UserID,
		IssuerRank:    actor.Role.Rank,
		Note:          in.Note,
		CreatedAt:     now,
		ExpiresAt:     now.Add(in.TTL),
	}

	code, id, err := svc.store.CreateInvite(ctx, inv)
	if err != nil {
		return "", nil, err
	}
	inv.ID = id

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID, ActorLabel: actor.Username,
		Action: audit.ActionInviteIssued, TargetKind: "invite",
		TargetID: fmt.Sprintf("%d", id),
		SourceIP: in.SourceIP, UserAgent: in.UserAgent,
		After: map[string]any{
			"role": role.Name, "auto_approve": in.AutoApprove,
			"all_libraries": in.AllLibraries,
			"library_ids":   in.LibraryIDs, "rating_ceiling": in.RatingCeiling,
			"expires_at": inv.ExpiresAt,
		},
	})
	return code, &inv, nil
}

// RevokeInvite cancels an invite.
func (svc *Service) RevokeInvite(ctx context.Context, id int64, sourceIP, userAgent string) error {
	actor := authz.FromContext(ctx)
	if err := authz.RequirePermission(ctx, authz.PermIssueInvites); err != nil {
		return err
	}
	if err := svc.store.RevokeInvite(ctx, id); err != nil {
		return err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID, ActorLabel: actor.Username,
		Action: audit.ActionInviteRevoked, TargetKind: "invite",
		TargetID: fmt.Sprintf("%d", id), SourceIP: sourceIP, UserAgent: userAgent,
	})
	return nil
}

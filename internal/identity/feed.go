package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Feed tokens (ADR-0041): the credential in a calendar or feed address. One per
// account, which can read the two feed routes and nothing else.

// FeedTokenPrefix marks a feed token, so it is recognisable in a leaked
// address and in a log line that escaped redaction.
const FeedTokenPrefix = "cms_feed_" // #nosec G101 -- a prefix that marks a feed token, not a credential

// ErrFeedTokenInvalid is a feed token that is wrong, revoked, or belongs to an
// account that cannot act — all answered alike.
var ErrFeedTokenInvalid = errors.New("identity: feed token is not valid")

// FeedStatus is whether an account has a feed token.
type FeedStatus struct {
	Exists     bool
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// FeedStatus reports the caller's feed token, never the token itself.
func (svc *Service) FeedStatus(ctx context.Context) (FeedStatus, error) {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return FeedStatus{}, ErrLoginFailed
	}
	var created string
	var used sql.NullString
	err := svc.store.db.QueryRowContext(ctx,
		`SELECT created_at, last_used_at FROM feed_token WHERE user_id = ?`, p.UserID).Scan(&created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return FeedStatus{}, nil
	}
	if err != nil {
		return FeedStatus{}, err
	}
	st := FeedStatus{Exists: true, CreatedAt: parseTS(created)}
	if used.Valid {
		t := parseTS(used.String)
		st.LastUsedAt = &t
	}
	return st, nil
}

// MintFeedToken issues the caller's feed token, replacing any other, and
// returns it once.
func (svc *Service) MintFeedToken(ctx context.Context, sourceIP, userAgent string) (string, error) {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() || p.IsToken() {
		return "", ErrLoginFailed
	}
	secret, err := randomToken(32)
	if err != nil {
		return "", err
	}
	token := FeedTokenPrefix + secret
	if _, err := svc.store.db.ExecContext(ctx, `
		INSERT INTO feed_token (user_id, token_hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET token_hash = excluded.token_hash,
		                                   created_at = excluded.created_at, last_used_at = NULL`,
		p.UserID, hashToken(token), ts(svc.now())); err != nil {
		return "", fmt.Errorf("identity: minting a feed token: %w", err)
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &p.UserID, ActorLabel: p.Username, Action: audit.ActionFeedTokenIssued,
		TargetKind: "user", TargetID: fmt.Sprintf("%d", p.UserID),
		SourceIP: sourceIP, UserAgent: userAgent,
		Detail: "a calendar and feed address was issued, replacing any other",
	})
	return token, nil
}

// RevokeFeedToken ends the caller's feed token.
func (svc *Service) RevokeFeedToken(ctx context.Context, sourceIP, userAgent string) error {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() || p.IsToken() {
		return ErrLoginFailed
	}
	res, err := svc.store.db.ExecContext(ctx, `DELETE FROM feed_token WHERE user_id = ?`, p.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &p.UserID, ActorLabel: p.Username, Action: audit.ActionFeedTokenRevoked,
		TargetKind: "user", TargetID: fmt.Sprintf("%d", p.UserID),
		SourceIP: sourceIP, UserAgent: userAgent, Detail: "the calendar and feed address was revoked",
	})
	return nil
}

// FeedPrincipal authenticates a feed token. The principal is its account's,
// holding media.browse and nothing else, and only if the account's role holds
// it; its libraries and ceiling are the account's (ADR-0037).
func (svc *Service) FeedPrincipal(ctx context.Context, token string) (*authz.Principal, error) {
	if !strings.HasPrefix(token, FeedTokenPrefix) {
		return nil, ErrFeedTokenInvalid
	}
	var userID int64
	err := svc.store.db.QueryRowContext(ctx,
		`SELECT user_id FROM feed_token WHERE token_hash = ?`, hashToken(token)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFeedTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	user, err := svc.store.UserByID(ctx, userID)
	if err != nil {
		return nil, ErrFeedTokenInvalid
	}
	if user.State != authz.StateActive || !user.Enrolled() {
		return nil, ErrFeedTokenInvalid
	}
	role, err := svc.store.RoleByID(ctx, user.RoleID)
	if err != nil {
		return nil, err
	}
	grants, err := svc.store.LibraryGrants(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	perms := authz.PermissionSet{}
	if role.Permissions.Has(authz.PermBrowse) {
		perms[authz.PermBrowse] = struct{}{}
	}
	scoped := role
	scoped.Permissions = perms
	_, _ = svc.store.db.ExecContext(ctx,
		`UPDATE feed_token SET last_used_at = ? WHERE user_id = ?`, ts(svc.now()), user.ID)
	return &authz.Principal{
		UserID: user.ID, Username: user.Username, Role: scoped, State: user.State,
		SessionID: "feed", LibraryIDs: grants,
		UnrestrictedLibraries: user.AllLibraries || role.Permissions.Has(authz.PermSystemSettings),
		RatingCeiling:         user.RatingCeiling,
		MFASatisfied:          true,
		Credential:            authz.CredentialToken,
	}, nil
}

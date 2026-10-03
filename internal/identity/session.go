package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// Sessions are opaque, server-side and looked up per request, rather than
// self-contained tokens such as JWTs.
//
// The deciding requirement is §7.2: suspending a user "immediately revokes all
// sessions, API tokens, and in-flight playback sessions". A self-contained
// token cannot be revoked without consulting a revocation list on every
// request, which is the database lookup a JWT was supposed to avoid — so the
// lookup happens, and the complexity of signing keys, rotation and clock skew
// buys nothing here.
//
// Cookie format: "<sessionID>.<secret>". Only sha256(secret) is stored, so a
// database copy does not yield usable cookies.

const (
	// SessionCookieName is the session cookie.
	SessionCookieName = "cms_session"

	// rotateEvery is how often an active session's secret is replaced.
	rotateEvery = 15 * time.Minute

	// rotationGrace is how long the previous secret keeps working after a
	// rotation, so that requests already in flight do not fail.
	//
	// Presenting the previous secret AFTER this window is not a race. It is a
	// replay of a token that was superseded, which means it was captured, and
	// the whole session is revoked.
	rotationGrace = 30 * time.Second
)

// Session errors.
var (
	ErrSessionInvalid = errors.New("identity: session invalid")
	ErrSessionExpired = errors.New("identity: session expired")
	// ErrSessionReuse means a superseded secret was presented outside the
	// rotation grace window. The session has been revoked as a result.
	ErrSessionReuse = errors.New("identity: session token reuse detected")
)

// Session is a stored login.
type Session struct {
	ID           string
	UserID       int64
	MFASatisfied bool
	CreatedAt    time.Time
	LastSeenAt   time.Time
	IdleExpires  time.Time
	AbsExpires   time.Time
	Generation   int
	DeviceLabel  string
	SourceIP     string
	UserAgent    string
}

// SessionConfig holds the timeouts.
type SessionConfig struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("identity: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateSession issues a session for a user.
//
// mfaSatisfied is false for the intermediate session created after a correct
// password but before the second factor. That session can do exactly two
// things: present a second factor, or enroll an authenticator.
func (s *Store) CreateSession(ctx context.Context, userID int64, mfaSatisfied bool, cfg SessionConfig, sourceIP, userAgent string) (cookie string, sess Session, err error) {
	idBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, idBytes); err != nil {
		return "", Session{}, err
	}
	id := hex.EncodeToString(idBytes)

	secret, err := randomToken(32)
	if err != nil {
		return "", Session{}, err
	}

	now := s.now()
	sess = Session{
		ID:           id,
		UserID:       userID,
		MFASatisfied: mfaSatisfied,
		CreatedAt:    now,
		LastSeenAt:   now,
		IdleExpires:  now.Add(cfg.IdleTimeout),
		AbsExpires:   now.Add(cfg.AbsoluteTimeout),
		SourceIP:     sourceIP,
		UserAgent:    userAgent,
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO session (id, user_id, refresh_hash, refresh_generation, mfa_satisfied,
		                     source_ip, user_agent, created_at, last_seen_at,
		                     idle_expires_at, absolute_expires_at, rotated_at)
		VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, hashSecret(secret), mfaSatisfied,
		nullStr(sourceIP), nullStr(userAgent),
		ts(now), ts(now), ts(sess.IdleExpires), ts(sess.AbsExpires), ts(now))
	if err != nil {
		return "", Session{}, fmt.Errorf("identity: create session: %w", err)
	}

	return id + "." + secret, sess, nil
}

// ResolveSession validates a cookie and returns the session and its user.
//
// It also performs rotation and reuse detection, so it is the single place a
// session's validity is decided.
func (s *Store) ResolveSession(ctx context.Context, cookie string, cfg SessionConfig) (*Session, *User, string, error) {
	id, secret, ok := strings.Cut(cookie, ".")
	if !ok || id == "" || secret == "" {
		return nil, nil, "", ErrSessionInvalid
	}

	var (
		userID                             int64
		currentHash                        string
		prevHash                           sql.NullString
		generation                         int
		mfaSatisfied                       bool
		lastSeen, idleExp, absExp, rotated string
		revoked                            sql.NullString
		created                            string
		sourceIP, userAgent                sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id, refresh_hash, prev_refresh_hash, refresh_generation, mfa_satisfied,
		       created_at, last_seen_at, idle_expires_at, absolute_expires_at,
		       COALESCE(rotated_at, created_at), revoked_at, source_ip, user_agent
		FROM session WHERE id = ?`, id).
		Scan(&userID, &currentHash, &prevHash, &generation, &mfaSatisfied,
			&created, &lastSeen, &idleExp, &absExp, &rotated, &revoked, &sourceIP, &userAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", ErrSessionInvalid
	}
	if err != nil {
		return nil, nil, "", err
	}

	if revoked.Valid && revoked.String != "" {
		return nil, nil, "", ErrSessionInvalid
	}

	now := s.now()
	presented := hashSecret(secret)

	matchesCurrent := subtle.ConstantTimeCompare([]byte(presented), []byte(currentHash)) == 1
	matchesPrev := prevHash.Valid && prevHash.String != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(prevHash.String)) == 1

	switch {
	case matchesCurrent:
		// normal
	case matchesPrev:
		if now.Sub(parseTS(rotated)) > rotationGrace {
			// A superseded secret, presented well after it was replaced. The
			// legitimate client has the new one, so this is a captured token.
			// Revoke the whole session rather than just refusing this request.
			_ = s.RevokeSession(ctx, id, "token_reuse_detected")
			return nil, nil, "", ErrSessionReuse
		}
	default:
		return nil, nil, "", ErrSessionInvalid
	}

	if now.After(parseTS(absExp)) || now.After(parseTS(idleExp)) {
		_ = s.RevokeSession(ctx, id, "expired")
		return nil, nil, "", ErrSessionExpired
	}

	user, err := s.UserByID(ctx, userID)
	if err != nil {
		return nil, nil, "", ErrSessionInvalid
	}
	// A suspended or disabled account's sessions stop working immediately,
	// without waiting for anything to expire.
	if user.State == authz.StateSuspended || user.State == authz.StateDisabled {
		_ = s.RevokeSession(ctx, id, "account_"+string(user.State))
		return nil, nil, "", ErrSessionInvalid
	}

	sess := &Session{
		ID: id, UserID: userID, MFASatisfied: mfaSatisfied,
		CreatedAt: parseTS(created), LastSeenAt: parseTS(lastSeen),
		IdleExpires: parseTS(idleExp), AbsExpires: parseTS(absExp),
		Generation: generation,
		SourceIP:   sourceIP.String, UserAgent: userAgent.String,
	}

	// Slide the idle window and rotate if due.
	newCookie := ""
	if matchesCurrent && now.Sub(parseTS(rotated)) >= rotateEvery {
		nextSecret, err := randomToken(32)
		if err != nil {
			return nil, nil, "", err
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE session SET refresh_hash = ?, prev_refresh_hash = ?, refresh_generation = ?,
			                   rotated_at = ?, last_seen_at = ?, idle_expires_at = ?
			WHERE id = ?`,
			hashSecret(nextSecret), currentHash, generation+1,
			ts(now), ts(now), ts(now.Add(cfg.IdleTimeout)), id); err != nil {
			return nil, nil, "", err
		}
		newCookie = id + "." + nextSecret
		sess.Generation = generation + 1
	} else {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE session SET last_seen_at = ?, idle_expires_at = ? WHERE id = ?`,
			ts(now), ts(now.Add(cfg.IdleTimeout)), id); err != nil {
			return nil, nil, "", err
		}
	}
	sess.IdleExpires = now.Add(cfg.IdleTimeout)

	return sess, user, newCookie, nil
}

// MarkSessionMFASatisfied promotes an intermediate session to a full one.
func (s *Store) MarkSessionMFASatisfied(ctx context.Context, sessionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE session SET mfa_satisfied = 1 WHERE id = ? AND revoked_at IS NULL`, sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSessionInvalid
	}
	return nil
}

// RevokeSession ends one session.
func (s *Store) RevokeSession(ctx context.Context, sessionID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE session SET revoked_at = ?, revoked_reason = ? WHERE id = ? AND revoked_at IS NULL`,
		ts(s.now()), reason, sessionID)
	return err
}

// RevokeAllUserSessions ends every session for a user. This is what makes
// suspension immediate.
func (s *Store) RevokeAllUserSessions(ctx context.Context, userID int64, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE session SET revoked_at = ?, revoked_reason = ? WHERE user_id = ? AND revoked_at IS NULL`,
		ts(s.now()), reason, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RevokeOtherUserSessions ends every session for a user except one. Used when
// a password changes: the browser doing the change stays signed in, everything
// else does not.
func (s *Store) RevokeOtherUserSessions(ctx context.Context, userID int64, keepSessionID, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE session SET revoked_at = ?, revoked_reason = ?
		WHERE user_id = ? AND id != ? AND revoked_at IS NULL`,
		ts(s.now()), reason, userID, keepSessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RevokeOwnedSession revokes one session, but only if it belongs to userID.
//
// The ownership predicate is in the WHERE clause rather than in a preceding
// SELECT. That makes it impossible for a caller to skip the check, and it
// closes the object-level hole this endpoint would otherwise be: session IDs
// are opaque, but "opaque" is not an authorization control.
func (s *Store) RevokeOwnedSession(ctx context.Context, userID int64, sessionID, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE session SET revoked_at = ?, revoked_reason = ?
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		ts(s.now()), reason, sessionID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Not found, already revoked, or owned by somebody else — all the same
		// answer, so this cannot be used to probe for other users' sessions.
		return ErrNotFound
	}
	return nil
}

// UserSessions lists a user's live sessions for the per-device list.
func (s *Store) UserSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, mfa_satisfied, created_at, last_seen_at, idle_expires_at, absolute_expires_at,
		       COALESCE(device_label,''), COALESCE(source_ip,''), COALESCE(user_agent,'')
		FROM session WHERE user_id = ? AND revoked_at IS NULL
		ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		s := Session{UserID: userID}
		var created, lastSeen, idleExp, absExp string
		if err := rows.Scan(&s.ID, &s.MFASatisfied, &created, &lastSeen, &idleExp, &absExp,
			&s.DeviceLabel, &s.SourceIP, &s.UserAgent); err != nil {
			return nil, err
		}
		s.CreatedAt, s.LastSeenAt = parseTS(created), parseTS(lastSeen)
		s.IdleExpires, s.AbsExpires = parseTS(idleExp), parseTS(absExp)
		out = append(out, s)
	}
	return out, rows.Err()
}

// PurgeExpiredSessions removes rows that can no longer authenticate.
func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM session
		WHERE absolute_expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)`,
		ts(s.now()), ts(s.now().Add(-7*24*time.Hour)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BuildPrincipal assembles the authorization principal for a session.
//
// This is the only place a *authz.Principal is constructed from stored state.
// Everything the authorization engine decides flows from what this function
// puts in it.
func (s *Store) BuildPrincipal(ctx context.Context, sess *Session, user *User) (*authz.Principal, error) {
	role, err := s.RoleByID(ctx, user.RoleID)
	if err != nil {
		return nil, err
	}
	grants, err := s.LibraryGrants(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	return &authz.Principal{
		UserID:                user.ID,
		Username:              user.Username,
		Role:                  role,
		State:                 user.State,
		SessionID:             sess.ID,
		LibraryIDs:            grants,
		UnrestrictedLibraries: user.AllLibraries || role.Permissions.Has(authz.PermSystemSettings),
		RatingCeiling:         user.RatingCeiling,
		MFASatisfied:          sess.MFASatisfied,
	}, nil
}

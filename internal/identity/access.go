package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// What an account may see of the library (ADR-0037): every library, or the
// root folders it is granted, and a rating ceiling.

var (
	// ErrNoSuchRootFolder means a grant named a root folder that does not exist.
	ErrNoSuchRootFolder = errors.New("identity: there is no such root folder to grant")
	// ErrNoLibraries means an account was to be restricted to no library at
	// all, which would let it sign in and see nothing.
	ErrNoLibraries = errors.New("identity: an account restricted to some libraries must be granted at least one root folder; grant all libraries instead")
)

// NormaliseGrant turns what an approver sent into a grant.
//
// all is nil when the caller did not say. Then an empty list means every
// library — which is what an account approved without a choice has always
// seen — and a list means those root folders. Saying all: false with no list
// is refused rather than creating an account that can see nothing.
func NormaliseGrant(all *bool, rootIDs []int64, ceiling int) (authz.Grant, error) {
	g := authz.Grant{RatingCeiling: ceiling}
	switch {
	case all != nil && *all:
		g.AllLibraries = true
	case all == nil && len(rootIDs) == 0:
		g.AllLibraries = true
	default:
		ids := slices.Clone(rootIDs)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		if len(ids) == 0 {
			return authz.Grant{}, ErrNoLibraries
		}
		g.RootFolderIDs = ids
	}
	if ceiling < 0 || ceiling > authz.MaxRatingRank {
		return authz.Grant{}, fmt.Errorf("identity: a rating ceiling is 0 for none, or 1 to %d",
			authz.MaxRatingRank)
	}
	return g, nil
}

// writeGrants replaces an account's root folder grants, inside the caller's
// transaction. Every root must exist: a grant of a root that does not is
// refused by name rather than by a foreign-key message.
func writeGrants(ctx context.Context, tx db.Execer, userID int64, rootIDs []int64, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM root_folder_grant WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("identity: clearing grants: %w", err)
	}
	for _, id := range rootIDs {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM root_folder WHERE id = ?`, id).
			Scan(&n); err != nil {
			return fmt.Errorf("identity: reading root folders: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %d", ErrNoSuchRootFolder, id)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO root_folder_grant (user_id, root_folder_id, granted_at) VALUES (?, ?, ?)`,
			userID, id, now); err != nil {
			return fmt.Errorf("identity: granting root folder %d: %w", id, err)
		}
	}
	return nil
}

// RootFoldersExist refuses a list naming a root folder that does not exist, so
// an invite cannot be issued that would fail when it is redeemed.
func (s *Store) RootFoldersExist(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM root_folder WHERE id = ?`, id).
			Scan(&n); err != nil {
			return fmt.Errorf("identity: reading root folders: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %d", ErrNoSuchRootFolder, id)
		}
	}
	return nil
}

// Access returns what an account may see.
func (s *Store) Access(ctx context.Context, userID int64) (authz.Grant, error) {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return authz.Grant{}, err
	}
	g := authz.Grant{AllLibraries: u.AllLibraries, RatingCeiling: u.RatingCeiling}
	if !u.AllLibraries {
		if g.RootFolderIDs, err = s.LibraryGrants(ctx, userID); err != nil {
			return authz.Grant{}, err
		}
	}
	return g, nil
}

// setAccess writes an account's grant in one transaction.
func (s *Store) setAccess(ctx context.Context, userID int64, g authz.Grant) error {
	return s.db.InTx(ctx, func(tx db.Execer) error {
		now := ts(s.now())
		res, err := tx.ExecContext(ctx,
			`UPDATE app_user SET all_libraries = ?, rating_ceiling = ?, updated_at = ? WHERE id = ?`,
			boolInt(g.AllLibraries), g.RatingCeiling, now, userID)
		if err != nil {
			return fmt.Errorf("identity: changing access: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		var roots []int64
		if !g.AllLibraries {
			roots = g.RootFolderIDs
		}
		return writeGrants(ctx, tx, userID, roots, now)
	})
}

// SetAccess changes what an existing account may see (ADR-0037, decision 5).
//
// Only an actor who may modify the account — admin.users, strictly above it —
// and only to a grant no wider than the actor's own. The account's principal
// is rebuilt on its next request, so the change applies then, to its sessions
// and its API tokens alike.
func (svc *Service) SetAccess(ctx context.Context, userID int64, g authz.Grant, sourceIP, userAgent string) error {
	actor := authz.FromContext(ctx)
	if err := authz.RequirePermission(ctx, authz.PermManageUsers); err != nil {
		return err
	}
	target, err := svc.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	role, err := svc.store.RoleByID(ctx, target.RoleID)
	if err != nil {
		return err
	}
	if err := authz.CanModifyUser(ctx, authz.TargetUser{UserID: userID, Role: role}); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "user.grants", d, sourceIP, userAgent)
		}
		return err
	}
	if err := authz.CanGrant(ctx, g); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "user.grants", d, sourceIP, userAgent)
		}
		return err
	}
	before, err := svc.store.Access(ctx, userID)
	if err != nil {
		return err
	}
	if err := svc.store.setAccess(ctx, userID, g); err != nil {
		return err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionGrantsChanged,
		TargetKind:  "user",
		TargetID:    fmt.Sprintf("%d", userID),
		SourceIP:    sourceIP,
		UserAgent:   userAgent,
		Before:      grantMap(before),
		After:       grantMap(g),
		Detail:      target.Username + ": " + DescribeGrant(g),
	})
	return nil
}

func grantMap(g authz.Grant) map[string]any {
	ids := g.RootFolderIDs
	if ids == nil {
		ids = []int64{}
	}
	return map[string]any{
		"all_libraries":  g.AllLibraries,
		"library_ids":    ids,
		"rating_ceiling": g.RatingCeiling,
	}
}

// DescribeGrant says a grant in words, for an audit line.
func DescribeGrant(g authz.Grant) string {
	var b strings.Builder
	if g.AllLibraries {
		b.WriteString("every library")
	} else {
		b.WriteString("root folders ")
		for i, id := range g.RootFolderIDs {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%d", id)
		}
	}
	if g.RatingCeiling > 0 {
		fmt.Fprintf(&b, ", rated up to rank %d", g.RatingCeiling)
	} else {
		b.WriteString(", no rating ceiling")
	}
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

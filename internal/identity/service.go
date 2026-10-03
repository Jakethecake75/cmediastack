package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Service errors.
//
// ErrLoginFailed is deliberately the only error a failed sign-in produces. It
// covers unknown account, wrong password, wrong code, pending request,
// suspended account and disabled account, because distinguishing any of them
// is an enumeration oracle.
var (
	ErrLoginFailed        = errors.New("identity: invalid credentials")
	ErrThrottled          = errors.New("identity: too many attempts")
	ErrRegistrationClosed = errors.New("identity: registration is closed")
	ErrQueueFull          = errors.New("identity: too many pending requests")
	ErrAlreadyEnrolled    = errors.New("identity: authenticator already enrolled")
	ErrSetupComplete      = errors.New("identity: setup has already been completed")
)

// Policy is the identity subsystem's configuration, kept independent of the
// config package so that this package can be tested without one.
type Policy struct {
	RegistrationMode  string // open | invite | closed
	PendingTTL        time.Duration
	MaxOutstanding    int
	Password          PasswordPolicy
	Session           SessionConfig
	LoginMaxAttempts  int
	LoginWindow       time.Duration
	TOTPIssuer        string
	RecoveryCodeCount int
	Argon2            Argon2Params
}

// Service holds the account flows.
type Service struct {
	store    *Store
	audit    *audit.Logger
	policy   Policy
	delivery ResetDelivery
	now      func() time.Time
	// breach is the breached-password check, nil when it is off (ADR-0051);
	// log is where a check that could not be made is said.
	breach BreachChecker
	log    *slog.Logger
	// pow is signup's proof-of-work; nil is off (ADR-0051).
	pow *ProofOfWork
}

// NewService builds the service.
func NewService(store *Store, auditLog *audit.Logger, policy Policy, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:  store,
		audit:  auditLog,
		policy: policy,
		// No mail transport in this build. The default refuses rather than
		// silently pretending to send; SetResetDelivery replaces it.
		delivery: NoResetDelivery{},
		now:      now,
	}
}

// SetResetDelivery installs a transport for password-reset tokens.
func (svc *Service) SetResetDelivery(d ResetDelivery) {
	if d != nil {
		svc.delivery = d
	}
}

// Store exposes the repository for wiring.
func (svc *Service) Store() *Store { return svc.store }

// Now is the clock this service decides expiry by.
//
// Exposed so that anything REPORTING an expiry uses the same clock as the code
// that ENFORCES it. The invite list recomputed "expired" with time.Now() while
// redemption used this, so the state an operator was shown and the state that
// decided whether a code worked came from different clocks. In production both
// are the wall clock and nobody could see it; the test suite injects a fixed
// one, and the list's test became a time bomb that went off the week after it
// was written.
func (svc *Service) Now() time.Time { return svc.now() }

// Policy exposes the policy for wiring.
func (svc *Service) Policy() Policy { return svc.policy }

// ---------------------------------------------------------------------------
// First run
// ---------------------------------------------------------------------------

// SetupNeeded reports whether the instance has no accounts yet.
//
// The setup route consults this on every request rather than caching it, so
// the wizard disables itself the instant the first Admin exists. There is no
// default account and no bootstrap credential written anywhere.
func (svc *Service) SetupNeeded(ctx context.Context) (bool, error) {
	n, err := svc.store.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// CreateFirstAdmin creates the initial administrator.
//
// It is guarded by the account count, checked inside the same call that
// creates the user, so a race between two concurrent setup submissions cannot
// produce two administrators: the second fails the unique username constraint
// or the count check.
func (svc *Service) CreateFirstAdmin(ctx context.Context, username, email, password, sourceIP, userAgent string) (int64, error) {
	needed, err := svc.SetupNeeded(ctx)
	if err != nil {
		return 0, err
	}
	if !needed {
		return 0, ErrSetupComplete
	}
	if err := svc.acceptablePassword(ctx, password); err != nil {
		return 0, err
	}

	role, err := svc.store.RoleByName(ctx, authz.RoleAdmin)
	if err != nil {
		return 0, fmt.Errorf("identity: admin role missing, run migrations: %w", err)
	}

	hash, err := HashPassword(password, svc.policy.Argon2)
	if err != nil {
		return 0, err
	}

	id, err := svc.store.CreateUser(ctx, NewUser{
		Username:     strings.TrimSpace(username),
		Email:        strings.TrimSpace(email),
		PasswordHash: hash,
		RoleID:       role.ID,
		AllLibraries: true,
	})
	if err != nil {
		return 0, err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorLabel: "setup-wizard",
		Action:     audit.ActionFirstRunCompleted,
		TargetKind: "user",
		TargetID:   fmt.Sprintf("%d", id),
		SourceIP:   sourceIP,
		UserAgent:  userAgent,
		Detail:     "initial administrator created; the wizard is now permanently disabled",
	})
	return id, nil
}

// ---------------------------------------------------------------------------
// Signup
// ---------------------------------------------------------------------------

// SignupInput is a submitted account request.
type SignupInput struct {
	Username   string
	Email      string
	Password   string
	Note       string
	InviteCode string
	SourceIP   string
	UserAgent  string
	// ProofChallenge and ProofCounter are the solved signup challenge
	// (ADR-0051, decision 2).
	ProofChallenge string
	ProofCounter   string
}

// SignupResult says what happened, so the caller can tell the applicant
// whether to wait for review or go straight to logging in.
type SignupResult struct {
	// Approved is true when an auto-approving invite created the account
	// outright. The account still starts in awaiting_mfa.
	Approved bool
	UserID   int64
}

// Signup records an account request.
//
// It never creates a user and never issues a session. The caller must respond
// identically whether or not the email is already registered: the returned
// error distinguishes only cases the applicant could see anyway (a password
// that fails policy, a closed registration, a full queue).
func (svc *Service) Signup(ctx context.Context, in SignupInput) (SignupResult, error) {
	hasCode := strings.TrimSpace(in.InviteCode) != ""

	switch svc.policy.RegistrationMode {
	case "open":
		// A code is optional here; with one, the applicant skips the queue.
	case "invite":
		if !hasCode {
			// Same shape as a closed registration: without a code there is
			// nothing at this endpoint to discover.
			return SignupResult{}, ErrRegistrationClosed
		}
	default:
		return SignupResult{}, ErrRegistrationClosed
	}

	// Paid for before anything costly: the breach check's request and the
	// password's Argon2id (ADR-0051, decision 2). After the mode check, so a
	// closed registration still answers exactly as a missing route does.
	if err := svc.pow.Verify(in.ProofChallenge, in.ProofCounter); err != nil {
		return SignupResult{}, err
	}
	if err := svc.acceptablePassword(ctx, in.Password); err != nil {
		return SignupResult{}, err
	}

	var invite *Invite
	if hasCode {
		var err error
		invite, err = svc.store.InviteByCode(ctx, in.InviteCode)
		if err != nil {
			// Safe to report: the applicant holds the code, so telling them it
			// does not work reveals nothing they did not already supply.
			_ = svc.audit.Write(ctx, audit.Event{
				ActorLabel: "anonymous", Action: audit.ActionInviteRedeemed,
				Outcome: audit.OutcomeFailure, SourceIP: in.SourceIP, UserAgent: in.UserAgent,
				Detail: "invalid, expired, revoked or already-used invite code",
			})
			return SignupResult{}, ErrInviteInvalid
		}
	}

	// The queue cap does not apply to an auto-approving invite: it exists to
	// bound an anonymous queue, and an invite is not anonymous.
	if invite == nil || !invite.AutoApprove {
		pending, err := svc.store.CountPendingRequests(ctx)
		if err != nil {
			return SignupResult{}, err
		}
		if pending >= svc.policy.MaxOutstanding {
			// The cap protects the argon2id work below from being an anonymous
			// CPU-exhaustion primitive.
			return SignupResult{}, ErrQueueFull
		}
	}

	username := strings.TrimSpace(in.Username)
	email := strings.ToLower(strings.TrimSpace(in.Email))

	hash, err := HashPassword(in.Password, svc.policy.Argon2)
	if err != nil {
		return SignupResult{}, err
	}

	// An auto-approving invite creates the account outright, because the trust
	// decision was made when the code was issued (§7.2).
	if invite != nil && invite.AutoApprove {
		userID, err := svc.store.CreateUser(ctx, NewUser{
			Username: username, Email: email, PasswordHash: hash,
			RoleID: invite.RoleID, RatingCeiling: invite.RatingCeiling,
			AllLibraries: invite.AllLibraries,
			LibraryIDs:   invite.LibraryIDs, ApprovedBy: &invite.IssuedBy,
		})
		if err != nil {
			return SignupResult{}, ErrInviteInvalid
		}
		if err := svc.store.RedeemInvite(ctx, invite.ID, userID); err != nil {
			return SignupResult{}, err
		}
		_ = svc.audit.Write(ctx, audit.Event{
			ActorLabel: "anonymous", Action: audit.ActionInviteRedeemed,
			TargetKind: "user", TargetID: fmt.Sprintf("%d", userID),
			SourceIP: in.SourceIP, UserAgent: in.UserAgent,
			After: map[string]any{
				"role": invite.RoleName, "invite_id": invite.ID,
				"state": string(authz.StateAwaitingMFA),
			},
		})
		return SignupResult{Approved: true, UserID: userID}, nil
	}

	now := svc.now()
	var inviteID *int64
	if invite != nil {
		inviteID = &invite.ID
	}
	_, err = svc.store.CreateAccountRequest(ctx, AccountRequest{
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		Note:         in.Note,
		SourceIP:     in.SourceIP,
		UserAgent:    in.UserAgent,
		InviteID:     inviteID,
		CreatedAt:    now,
		ExpiresAt:    now.Add(svc.policy.PendingTTL),
	})
	if err != nil {
		// Only a uniqueness violation is masked. Any other failure — a locked
		// database, a full disk — is a failure, and answering it with the
		// success message would tell somebody their request was recorded when
		// nothing was.
		if !db.IsUniqueViolation(err) {
			return SignupResult{}, fmt.Errorf("identity: recording the account request: %w", err)
		}
		// A duplicate email or username collides with a unique index. That is
		// NOT surfaced: the caller reports success either way, so the signup
		// form cannot be used to test whether an address is registered.
		_ = svc.audit.Write(ctx, audit.Event{
			ActorLabel: "anonymous",
			Action:     audit.ActionAccountRequested,
			Outcome:    audit.OutcomeFailure,
			SourceIP:   in.SourceIP,
			UserAgent:  in.UserAgent,
			Detail:     "request rejected by a uniqueness constraint",
		})
		return SignupResult{}, nil //nolint:nilerr // masked on purpose, above
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorLabel: "anonymous",
		Action:     audit.ActionAccountRequested,
		TargetKind: "account_request",
		TargetID:   username,
		SourceIP:   in.SourceIP,
		UserAgent:  in.UserAgent,
	})
	return SignupResult{}, nil
}

// ApprovalInput is the decision an approver makes.
//
// Every field is required at approval time (§7.2): the approver sets the role,
// the library grants, the content-rating ceiling and whether MFA is mandatory
// in the same action that creates the account. There is no "approve now,
// configure later" path that leaves an account with defaults nobody chose.
type ApprovalInput struct {
	RequestID int64
	RoleID    int64
	// AllLibraries and LibraryIDs are what the account will see (ADR-0037):
	// every library, or these root folders.
	AllLibraries  bool
	LibraryIDs    []int64
	RatingCeiling int
	SourceIP      string
	UserAgent     string
}

// ApproveRequest promotes a pending request into a user.
//
// Authorization happens twice on purpose: the caller must hold
// PermApproveAccounts to read the request at all, and CanAssignRole enforces
// that the target role is strictly below the approver's own and grants nothing
// the approver does not hold.
func (svc *Service) ApproveRequest(ctx context.Context, in ApprovalInput) (int64, error) {
	actor := authz.FromContext(ctx)

	req, err := svc.store.AccountRequestByID(ctx, in.RequestID)
	if err != nil {
		return 0, err
	}

	role, err := svc.store.RoleByID(ctx, in.RoleID)
	if err != nil {
		return 0, err
	}
	if err := authz.CanAssignRole(ctx, role); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "account.approve", d, in.SourceIP, in.UserAgent)
		}
		return 0, err
	}
	// Approving an account must not be a way for a restricted approver to
	// create a less restricted one (ADR-0037).
	if err := authz.CanGrant(ctx, authz.Grant{AllLibraries: in.AllLibraries,
		RootFolderIDs: in.LibraryIDs, RatingCeiling: in.RatingCeiling}); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "account.approve", d, in.SourceIP, in.UserAgent)
		}
		return 0, err
	}
	if in.AllLibraries {
		in.LibraryIDs = nil
	}

	userID, err := svc.store.CreateUser(ctx, NewUser{
		Username:      req.Username,
		Email:         req.Email,
		PasswordHash:  req.PasswordHash, // carried over; never re-derived or reset
		RoleID:        role.ID,
		RatingCeiling: in.RatingCeiling,
		AllLibraries:  in.AllLibraries,
		LibraryIDs:    in.LibraryIDs,
		ApprovedBy:    &actor.UserID,
	})
	if err != nil {
		return 0, err
	}

	if err := svc.store.DecideRequest(ctx, in.RequestID, "approved", actor.UserID, ""); err != nil {
		return 0, err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionAccountApproved,
		TargetKind:  "user",
		TargetID:    fmt.Sprintf("%d", userID),
		SourceIP:    in.SourceIP,
		UserAgent:   in.UserAgent,
		After: map[string]any{
			"role":           role.Name,
			"all_libraries":  in.AllLibraries,
			"library_ids":    in.LibraryIDs,
			"rating_ceiling": in.RatingCeiling,
			"state":          string(authz.StateAwaitingMFA),
		},
	})
	return userID, nil
}

// DenyRequest refuses a pending request.
func (svc *Service) DenyRequest(ctx context.Context, requestID int64, reason, sourceIP, userAgent string) error {
	actor := authz.FromContext(ctx)
	if err := authz.RequirePermission(ctx, authz.PermApproveAccounts); err != nil {
		return err
	}
	if err := svc.store.DecideRequest(ctx, requestID, "denied", actor.UserID, reason); err != nil {
		return err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionAccountDenied,
		TargetKind:  "account_request",
		TargetID:    fmt.Sprintf("%d", requestID),
		SourceIP:    sourceIP,
		UserAgent:   userAgent,
		Detail:      reason,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

// LoginResult tells the caller what the client must do next.
type LoginResult struct {
	// Cookie is the session cookie value to set.
	Cookie string
	// NeedsEnrollment means the account has no authenticator yet. The session
	// can reach only the enrollment endpoints.
	NeedsEnrollment bool
	// NeedsMFA means a second factor must be presented before the session can
	// do anything else.
	NeedsMFA bool
}

// Login verifies a password and issues an intermediate session.
//
// The session it returns can never act on its own: it is created with
// mfa_satisfied = false, and the middleware refuses everything but enrollment
// or the second-factor step until that changes.
func (svc *Service) Login(ctx context.Context, username, password, sourceIP, userAgent string) (LoginResult, error) {
	userKey := "user:" + strings.ToLower(strings.TrimSpace(username))
	ipKey := "ip:" + sourceIP

	for _, key := range []string{userKey, ipKey} {
		n, err := svc.store.RecentFailures(ctx, "login", key, svc.policy.LoginWindow)
		if err != nil {
			return LoginResult{}, err
		}
		if n >= svc.policy.LoginMaxAttempts {
			_ = svc.audit.Write(ctx, audit.Event{
				ActorLabel: "anonymous", Action: audit.ActionLoginFailed,
				Outcome: audit.OutcomeDenied, SourceIP: sourceIP, UserAgent: userAgent,
				Detail: "throttled: " + key,
			})
			return LoginResult{}, ErrThrottled
		}
	}

	user, err := svc.store.UserByUsername(ctx, username)
	if err != nil {
		// Spend the same CPU an existing account would, so the response time
		// does not reveal whether the username exists.
		SpendVerificationTime(password)
		svc.recordLoginFailure(ctx, userKey, ipKey, sourceIP, userAgent, "unknown account")
		return LoginResult{}, ErrLoginFailed
	}

	if err := VerifyPassword(password, user.PasswordHash); err != nil {
		svc.recordLoginFailure(ctx, userKey, ipKey, sourceIP, userAgent, "bad password")
		return LoginResult{}, ErrLoginFailed
	}

	// A correct password on a suspended or disabled account still fails, and
	// fails identically to a wrong one.
	if user.State == authz.StateSuspended || user.State == authz.StateDisabled {
		svc.recordLoginFailure(ctx, userKey, ipKey, sourceIP, userAgent, "account "+string(user.State))
		return LoginResult{}, ErrLoginFailed
	}

	cookie, _, err := svc.store.CreateSession(ctx, user.ID, false, svc.policy.Session, sourceIP, userAgent)
	if err != nil {
		return LoginResult{}, err
	}

	_ = svc.store.RecordAttempt(ctx, "login", userKey, true)
	_ = svc.store.RecordAttempt(ctx, "login", ipKey, true)
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: audit.ActionLoginSucceeded, SourceIP: sourceIP, UserAgent: userAgent,
		Detail: "password accepted; second factor pending",
	})

	return LoginResult{
		Cookie:          cookie,
		NeedsEnrollment: !user.Enrolled(),
		NeedsMFA:        user.Enrolled(),
	}, nil
}

func (svc *Service) recordLoginFailure(ctx context.Context, userKey, ipKey, sourceIP, userAgent, detail string) {
	_ = svc.store.RecordAttempt(ctx, "login", userKey, false)
	_ = svc.store.RecordAttempt(ctx, "login", ipKey, false)
	_ = svc.audit.Write(ctx, audit.Event{
		ActorLabel: "anonymous", Action: audit.ActionLoginFailed, Outcome: audit.OutcomeFailure,
		SourceIP: sourceIP, UserAgent: userAgent, Detail: detail,
	})
}

// VerifyMFA completes a sign-in with an authenticator code or a recovery code.
func (svc *Service) VerifyMFA(ctx context.Context, sess *Session, user *User, code, sourceIP, userAgent string) error {
	if sess.MFASatisfied {
		return nil
	}
	if !user.Enrolled() {
		return ErrLoginFailed
	}

	key := fmt.Sprintf("mfa:%d", user.ID)
	n, err := svc.store.RecentFailures(ctx, "mfa", key, svc.policy.LoginWindow)
	if err != nil {
		return err
	}
	if n >= svc.policy.LoginMaxAttempts {
		return ErrThrottled
	}

	secret, err := svc.store.TOTPSecret(user)
	if err != nil {
		return ErrLoginFailed
	}

	now := svc.now()
	if err := VerifyTOTP(secret, code, now); err == nil {
		// A code stays valid for its whole step, so verification alone allows
		// replay. Recording the counter is what closes that window.
		if err := svc.store.ConsumeTOTPCounter(ctx, user.ID, ConsumedCounter(now)); err != nil {
			_ = svc.store.RecordAttempt(ctx, "mfa", key, false)
			_ = svc.audit.Write(ctx, audit.Event{
				ActorUserID: &user.ID, ActorLabel: user.Username,
				Action: audit.ActionMFAFailed, Outcome: audit.OutcomeDenied,
				SourceIP: sourceIP, UserAgent: userAgent,
				Detail: "authenticator code replayed within its time step",
			})
			return ErrLoginFailed
		}
		return svc.completeMFA(ctx, sess, user, key, sourceIP, userAgent, audit.ActionMFASucceeded, "")
	}

	// Fall back to a recovery code.
	if err := svc.store.ConsumeRecoveryCode(ctx, user.ID, code); err == nil {
		remaining, _ := svc.store.UnusedRecoveryCodeCount(ctx, user.ID)
		return svc.completeMFA(ctx, sess, user, key, sourceIP, userAgent,
			audit.ActionRecoveryCodeUsed, fmt.Sprintf("%d recovery codes remain", remaining))
	}

	_ = svc.store.RecordAttempt(ctx, "mfa", key, false)
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: audit.ActionMFAFailed, Outcome: audit.OutcomeFailure,
		SourceIP: sourceIP, UserAgent: userAgent,
	})
	return ErrLoginFailed
}

func (svc *Service) completeMFA(ctx context.Context, sess *Session, user *User, key, sourceIP, userAgent string, action audit.Action, detail string) error {
	if err := svc.store.MarkSessionMFASatisfied(ctx, sess.ID); err != nil {
		return err
	}
	_ = svc.store.RecordAttempt(ctx, "mfa", key, true)
	_ = svc.store.TouchLogin(ctx, user.ID)
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: action, SourceIP: sourceIP, UserAgent: userAgent, Detail: detail,
	})
	return nil
}

// Logout revokes the current session.
func (svc *Service) Logout(ctx context.Context, sessionID string, userID int64, username string) error {
	if err := svc.store.RevokeSession(ctx, sessionID, "logout"); err != nil {
		return err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &userID, ActorLabel: username, Action: audit.ActionLogout,
	})
	return nil
}

// ---------------------------------------------------------------------------
// MFA enrollment
// ---------------------------------------------------------------------------

// EnrollmentOffer is what the enrollment page needs.
type EnrollmentOffer struct {
	Secret string
	URI    string
}

// BeginEnrollment generates a candidate authenticator secret.
//
// The secret is NOT stored yet. It is returned to the client and echoed back
// with a code in CompleteEnrollment, so an account cannot end up with a stored
// secret the user's app never received — which, with mandatory MFA, would lock
// them out permanently.
func (svc *Service) BeginEnrollment(user *User) (EnrollmentOffer, error) {
	if user.Enrolled() {
		return EnrollmentOffer{}, ErrAlreadyEnrolled
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return EnrollmentOffer{}, err
	}
	return EnrollmentOffer{
		Secret: secret,
		URI:    ProvisioningURI(svc.policy.TOTPIssuer, user.Username, secret),
	}, nil
}

// CompleteEnrollment verifies the code, stores the secret, activates the
// account and returns single-use recovery codes.
//
// The codes are returned once and never again: only their hashes are stored.
func (svc *Service) CompleteEnrollment(ctx context.Context, user *User, secret, code, sourceIP, userAgent string) ([]string, error) {
	if user.Enrolled() {
		return nil, ErrAlreadyEnrolled
	}
	now := svc.now()
	if err := VerifyTOTP(secret, code, now); err != nil {
		return nil, ErrLoginFailed
	}

	if err := svc.store.SetTOTPSecret(ctx, user.ID, secret); err != nil {
		return nil, err
	}
	if err := svc.store.ConsumeTOTPCounter(ctx, user.ID, ConsumedCounter(now)); err != nil {
		return nil, err
	}

	codes, err := GenerateRecoveryCodes(svc.policy.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := svc.store.StoreRecoveryCodes(ctx, user.ID, codes); err != nil {
		return nil, err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: audit.ActionMFAEnrolled, SourceIP: sourceIP, UserAgent: userAgent,
		Detail: fmt.Sprintf("%d recovery codes issued", len(codes)),
	})
	return codes, nil
}

// ---------------------------------------------------------------------------
// Suspension
// ---------------------------------------------------------------------------

// SuspendUser freezes an account and kills its sessions in the same action.
//
// §7.2 requires suspension to be immediate. Revoking the sessions here is what
// makes it so; waiting for them to expire would leave a suspended user active
// for up to the idle timeout.
func (svc *Service) SuspendUser(ctx context.Context, targetID int64, reason, sourceIP, userAgent string) error {
	actor := authz.FromContext(ctx)

	target, err := svc.store.UserByID(ctx, targetID)
	if err != nil {
		return err
	}
	targetRole, err := svc.store.RoleByID(ctx, target.RoleID)
	if err != nil {
		return err
	}
	if err := authz.CanModifyUser(ctx, authz.TargetUser{UserID: target.ID, Role: targetRole}); err != nil {
		svc.denied(ctx, "user.suspend", err, sourceIP, userAgent)
		return err
	}
	// Suspending the last administrator locks the instance out of its own
	// administration exactly as thoroughly as demoting them does, and this
	// path reaches that outcome faster: it does not need a second role to
	// exist. The guard was on ChangeUserRole alone until a mutation test
	// turned off the rank rules and watched a Manager suspend the only
	// administrator — 200 OK, zero administrators left. See admin.go.
	if err := svc.guardLastAdmin(ctx, target, targetRole); err != nil {
		return err
	}

	if err := svc.store.SetUserState(ctx, targetID, authz.StateSuspended); err != nil {
		return err
	}
	revoked, err := svc.store.RevokeAllUserSessions(ctx, targetID, "user_suspended")
	if err != nil {
		return err
	}
	// §7.2 requires suspension to revoke sessions AND API tokens. A token that
	// outlived its owner's access would be the obvious way back in.
	tokens, err := svc.store.RevokeAllUserTokens(ctx, targetID, "user_suspended")
	if err != nil {
		return err
	}
	// An invite is a pre-approved grant made on the issuer's authority. If that
	// authority is withdrawn, the outstanding grants go with it.
	invites, err := svc.store.RevokeInvitesIssuedBy(ctx, targetID)
	if err != nil {
		return err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID, ActorLabel: actor.Username,
		Action: audit.ActionUserSuspended, TargetKind: "user",
		TargetID: fmt.Sprintf("%d", targetID),
		SourceIP: sourceIP, UserAgent: userAgent, Detail: reason,
		Before: map[string]any{"state": string(target.State)},
		After: map[string]any{
			"state":            string(authz.StateSuspended),
			"sessions_revoked": revoked,
			"tokens_revoked":   tokens,
			"invites_revoked":  invites,
		},
	})
	return nil
}

// ---------------------------------------------------------------------------
// Maintenance
// ---------------------------------------------------------------------------

// RunMaintenance purges expired requests, sessions and throttle records. The
// scheduler calls it; it is exported so an admin can trigger it on demand.
func (svc *Service) RunMaintenance(ctx context.Context) (requests, sessions, attempts int64, err error) {
	if requests, err = svc.store.PurgeExpiredRequests(ctx); err != nil {
		return
	}
	if sessions, err = svc.store.PurgeExpiredSessions(ctx); err != nil {
		return
	}
	if _, err = svc.store.PurgeExpiredInvites(ctx); err != nil {
		return
	}
	if _, err = svc.store.PurgeExpiredResetTokens(ctx); err != nil {
		return
	}
	if _, err = svc.store.PurgeExpiredTokens(ctx); err != nil {
		return
	}
	attempts, err = svc.store.PurgeOldAttempts(ctx, 30*24*time.Hour)
	return
}

// AssignableRoles returns the roles the calling principal may actually grant.
//
// The filter is authz.CanAssignRole itself, run once per role, rather than a
// second implementation of the same rank-and-subset rule. That matters: a UI
// that builds its dropdown from a reimplemented rule eventually offers an
// option the server refuses, or — far worse — stops offering one the server
// would have allowed and the divergence is never noticed. Here the list is
// exactly the set that would pass at approval time, by construction.
func (svc *Service) AssignableRoles(ctx context.Context) ([]authz.Role, error) {
	if err := authz.RequirePermission(ctx, authz.PermApproveAccounts); err != nil {
		return nil, err
	}
	all, err := svc.store.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]authz.Role, 0, len(all))
	for _, r := range all {
		if authz.CanAssignRole(ctx, r) == nil {
			out = append(out, r)
		}
	}

	// Least privileged first, and this is a security property rather than a
	// presentation choice, which is why it lives here and not in the UI. A
	// dropdown takes its default from the first option, so ordering by
	// descending rank would mean that an approver who clicks Approve without
	// reading grants the most powerful role they are capable of granting. The
	// safe option has to be the one you get for not thinking.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out, nil
}

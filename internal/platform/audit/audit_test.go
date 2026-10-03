package audit

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

func newTestLogger(t *testing.T) (*Logger, *db.DB) {
	t.Helper()
	d, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return New(d, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }), d
}

func adminCtx() context.Context {
	var role authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleAdmin {
			role = r
		}
	}
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "admin", Role: role,
		State: authz.StateActive, MFASatisfied: true,
	})
}

// The core structural guarantee: this package must expose no way to change or
// remove an audit record. If someone adds one, this test fails.
func TestLoggerExposesNoMutationMethods(t *testing.T) {
	forbidden := []string{"update", "delete", "remove", "purge", "truncate", "edit", "redact", "clear"}

	typ := reflect.TypeOf(&Logger{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := strings.ToLower(typ.Method(i).Name)
		for _, f := range forbidden {
			if strings.Contains(name, f) {
				t.Errorf("audit.Logger exposes %q; the audit log is append-only",
					typ.Method(i).Name)
			}
		}
	}
}

func TestWriteAndList(t *testing.T) {
	l, _ := newTestLogger(t)
	ctx := adminCtx()
	actor := int64(1)

	events := []Event{
		{ActorUserID: &actor, ActorLabel: "admin", Action: ActionLoginSucceeded, SourceIP: "203.0.113.5"},
		{ActorLabel: "anonymous", Action: ActionLoginFailed, Outcome: OutcomeFailure, SourceIP: "198.51.100.9"},
		{ActorUserID: &actor, ActorLabel: "admin", Action: ActionUserSuspended,
			TargetKind: "user", TargetID: "20",
			Before: map[string]string{"state": "active"},
			After:  map[string]string{"state": "suspended"}},
	}
	for _, e := range events {
		if err := l.Write(context.Background(), e); err != nil {
			t.Fatalf("write %s: %v", e.Action, err)
		}
	}

	got, err := l.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}

	filtered, err := l.List(ctx, Query{Action: ActionLoginFailed})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Outcome != OutcomeFailure {
		t.Errorf("action filter returned %+v", filtered)
	}
}

// Reading the audit log is itself privileged, and that is enforced in the data
// layer rather than only at the route.
func TestListRequiresAuditPermission(t *testing.T) {
	l, _ := newTestLogger(t)

	var userRole authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleUser {
			userRole = r
		}
	}

	cases := map[string]context.Context{
		"anonymous": context.Background(),
		"user": authz.WithPrincipal(context.Background(), &authz.Principal{
			UserID: 20, Role: userRole, State: authz.StateActive, MFASatisfied: true,
		}),
	}
	for name, ctx := range cases {
		if _, err := l.List(ctx, Query{}); err == nil {
			t.Errorf("%s read the audit log (THE ATTACK SUCCEEDED)", name)
		} else if !authz.IsDenied(err) {
			t.Errorf("%s got a non-authorization error: %v", name, err)
		}
	}
}

// An authorization denial must be recorded even though the client saw a 404.
func TestAuthzDeniedIsRecorded(t *testing.T) {
	l, _ := newTestLogger(t)

	l.AuthzDenied(context.Background(), "GET /api/v1/admin/users",
		&authz.Denial{Actor: 20, Reason: authz.ReasonMissingPerm, Detail: `role "User" lacks "admin.users"`},
		"203.0.113.44", "curl/8.5.0")

	got, err := l.List(adminCtx(), Query{Action: ActionAuthzDenied})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d denial records, want 1", len(got))
	}
	e := got[0]
	if e.Outcome != OutcomeDenied {
		t.Errorf("outcome = %q", e.Outcome)
	}
	if e.TargetID != "GET /api/v1/admin/users" {
		t.Errorf("target = %q", e.TargetID)
	}
	if e.SourceIP != "203.0.113.44" {
		t.Errorf("source ip = %q", e.SourceIP)
	}
	if !strings.Contains(e.Detail, authz.ReasonMissingPerm) {
		t.Errorf("detail lost the reason: %q", e.Detail)
	}
	if e.ActorUserID == nil || *e.ActorUserID != 20 {
		t.Errorf("actor = %v", e.ActorUserID)
	}
}

func TestAnonymousDenialIsRecordedWithoutAnActor(t *testing.T) {
	l, _ := newTestLogger(t)

	l.AuthzDenied(context.Background(), "GET /api/v1/media",
		&authz.Denial{Reason: authz.ReasonAnonymous, Detail: "no session presented"},
		"198.51.100.1", "nmap")

	got, _ := l.List(adminCtx(), Query{Action: ActionAuthzDenied})
	if len(got) != 1 {
		t.Fatalf("got %d records", len(got))
	}
	if got[0].ActorUserID != nil {
		t.Errorf("anonymous denial recorded an actor: %v", *got[0].ActorUserID)
	}
	if got[0].ActorLabel != "anonymous" {
		t.Errorf("actor label = %q", got[0].ActorLabel)
	}
}

func TestDefaultsAreFilled(t *testing.T) {
	l, _ := newTestLogger(t)
	if err := l.Write(context.Background(), Event{Action: ActionLogout}); err != nil {
		t.Fatal(err)
	}
	got, _ := l.List(adminCtx(), Query{})
	if len(got) != 1 {
		t.Fatal("no event written")
	}
	if got[0].ActorLabel != "anonymous" {
		t.Errorf("actor label default = %q", got[0].ActorLabel)
	}
	if got[0].Outcome != OutcomeSuccess {
		t.Errorf("outcome default = %q", got[0].Outcome)
	}
	if got[0].OccurredAt.IsZero() {
		t.Error("occurred_at was not filled in")
	}
}

func TestLimitIsBounded(t *testing.T) {
	l, _ := newTestLogger(t)
	for i := 0; i < 20; i++ {
		_ = l.Write(context.Background(), Event{Action: ActionLoginFailed})
	}
	got, err := l.List(adminCtx(), Query{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("limit ignored: got %d", len(got))
	}

	// An absurd limit is clamped rather than honoured.
	got, err = l.List(adminCtx(), Query{Limit: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Errorf("got %d, want all 20", len(got))
	}
}

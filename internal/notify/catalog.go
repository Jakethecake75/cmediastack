package notify

import (
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Category is what the operator turns on and off (ADR-0032, decision 4).
type Category string

// The categories, in the order the screen lists them.
const (
	Security   Category = "security"
	Accounts   Category = "accounts"
	Operations Category = "operations"
	Library    Category = "library"
	Requests   Category = "requests"
)

// CategoryInfo describes a category for the screen.
type CategoryInfo struct {
	ID      Category
	Label   string
	Icon    string
	Default bool
	// Sends says what it sends, in the screen's words.
	Sends string
	// Titles is set on a category that sends what the library holds, or who
	// asked for what, to Discord.
	Titles bool
}

// Categories is every category.
var Categories = []CategoryInfo{
	{ID: Security, Label: "Security", Icon: "🔒", Default: true,
		Sends: "Denials over the audit log's ceiling; a signed-in account refused a route; sign-ins throttled; " +
			"a correct password with a refused authenticator code; a recovery code used; an authenticator " +
			"enrolled; a password changed or a reset link made; an API token issued; recovery from the host; " +
			"roles, suspensions and grants; settings and indexers changed; a database restored."},
	{ID: Accounts, Label: "Accounts", Icon: "👤", Default: true,
		Sends: "An account requested and waiting for approval; an invite redeemed; a request that expired."},
	{ID: Operations, Label: "Operations", Icon: "⚙️", Default: true,
		Sends: "A scheduled task that starts failing, and when it recovers — the tunnel, backups, acquisition; " +
			"the kill switch engaging."},
	{ID: Library, Label: "Library", Icon: "🎬", Default: false, Titles: true,
		Sends: "Grabs — a person's or automatic acquisition's — and failed ones; files arriving; deletions, " +
			"restores and the trash emptied."},
	{ID: Requests, Label: "Requests", Icon: "📨", Default: false, Titles: true,
		Sends: "Requests made, approved, denied and fulfilled."},
}

// categoryInfo finds a category.
func categoryInfo(c Category) (CategoryInfo, bool) {
	for _, ci := range Categories {
		if ci.ID == c {
			return ci, true
		}
	}
	return CategoryInfo{}, false
}

// entry is how one kind of audit line is sent.
type entry struct {
	category Category
	title    string
	// failed and refused are the titles when the line records a failure or a
	// refusal; a line with no title for its outcome is not sent.
	failed, refused string
	// when, if set, picks the lines of this kind that are sent at all.
	when func(audit.Record) bool
	// always sends the line whatever the categories say (ADR-0065).
	always bool
}

// A condition on a line.
var aPerson = func(r audit.Record) bool { return r.ActorUserID != nil }

// catalog is every audit line that is sent, and how. A line not here is
// never sent: successful sign-ins and sign-outs, an anonymous denial (the
// ceiling's count stands for a flood of them), routine backups, a person's
// own session list. Adding a kind of line to what leaves for Discord is a
// change to this table and to ADR-0032's.
var catalog = map[audit.Action]entry{
	// Security. A refusal here is the interesting line: somebody tried.
	audit.ActionAuthzDeniedSuppressed: {category: Security, title: "Denials over the audit log's ceiling",
		refused: "Denials over the audit log's ceiling"},
	audit.ActionAuthzDenied: {category: Security, refused: "An account was refused a route", when: aPerson},
	// Only when throttled: one wrong password is a typo.
	audit.ActionLoginFailed: {category: Security, refused: "Sign-ins throttled"},
	audit.ActionMFAFailed: {category: Security, failed: "Password accepted, authenticator code refused",
		refused: "Password accepted, authenticator code refused"},
	audit.ActionRecoveryCodeUsed:         {category: Security, title: "Signed in with a recovery code"},
	audit.ActionRecoveryCodesRegenerated: {category: Security, title: "Recovery codes replaced"},
	audit.ActionMFAEnrolled:              {category: Security, title: "Authenticator enrolled"},
	audit.ActionPasswordChanged:          {category: Security, title: "Password changed"},
	audit.ActionPasswordResetMinted:      {category: Security, title: "Password-reset link made"},
	audit.ActionAPITokenIssued: {category: Security, title: "API token issued",
		refused: "An API token wider than its owner was refused"},
	audit.ActionAccountRecovered: {category: Security, title: "Recovered from the host (break-glass)"},
	audit.ActionRoleChanged: {category: Security, title: "Role changed",
		refused: "A role change was refused"},
	audit.ActionUserSuspended: {category: Security, title: "Account suspended",
		refused: "A suspension was refused"},
	audit.ActionUserReactivated: {category: Security, title: "Account reactivated",
		refused: "A reactivation was refused"},
	audit.ActionGrantsChanged: {category: Security, title: "Grants changed",
		refused: "A change of grants was refused"},
	audit.ActionRolePermissionsChanged: {category: Security, title: "Role permissions changed",
		refused: "A role edit was refused"},
	audit.ActionSystemSettingChanged: {category: Security, title: "Setting changed",
		failed: "A setting could not be changed", refused: "A setting change was refused"},
	audit.ActionIndexerCreated:    {category: Security, title: "Indexer added"},
	audit.ActionIndexerUpdated:    {category: Security, title: "Indexer changed"},
	audit.ActionIndexerDeleted:    {category: Security, title: "Indexer removed"},
	audit.ActionBackupRestored:    {category: Security, title: "Database restored from a backup"},
	audit.ActionFirstRunCompleted: {category: Security, title: "First-run setup completed"},
	// Where the traffic goes: sent even with Security off, so an operator
	// hears of a change they did not make (ADR-0065).
	audit.ActionEgressProxyChanged: {category: Security, title: "SOCKS5 proxy changed", always: true},
	audit.ActionSystemRestarted:    {category: Security, title: "Restarted from the web"},

	// Accounts.
	audit.ActionAccountRequested: {category: Accounts, title: "Account requested — waiting for approval"},
	audit.ActionInviteRedeemed:   {category: Accounts, title: "Invite redeemed"},
	audit.ActionAccountExpired:   {category: Accounts, title: "Account request expired"},

	// Operations. Task failures and recoveries come from the scheduler, not
	// the log (Service.TaskFinished).
	audit.ActionKillSwitch: {category: Operations, title: "Kill switch engaged — transfers paused",
		failed: "Kill switch engaged — transfers paused"},

	// Library.
	audit.ActionReleaseGrabbed: {category: Library, title: "Grabbed", failed: "Grab failed"},
	audit.ActionMediaImported:  {category: Library, title: "Arrived in the library"},
	audit.ActionQueueRemoved:   {category: Library, title: "Removed from the queue"},
	// Library, not operations: the message names a release (ADR-0034).
	audit.ActionDownloadStalled: {category: Library, title: "A download stopped moving"},
	audit.ActionMediaDeleted:    {category: Library, title: "Deleted (to the trash)"},
	audit.ActionMediaRestored:   {category: Library, title: "Restored from the trash"},
	audit.ActionTrashPurged:     {category: Library, title: "Trash emptied"},

	// Requests.
	audit.ActionRequestSubmitted: {category: Requests, title: "Requested"},
	audit.ActionRequestApproved:  {category: Requests, title: "Request approved"},
	audit.ActionRequestDenied:    {category: Requests, title: "Request denied"},
	audit.ActionRequestFulfilled: {category: Requests, title: "Request fulfilled"},
	// A problem with a title (ADR-0042): somebody's words, sent as untrusted
	// text like every other.
	audit.ActionIssueReported: {category: Requests, title: "Problem reported"},
	audit.ActionIssueResolved: {category: Requests, title: "Problem resolved"},
}

// classify says whether an audit line is sent, under which category and
// title.
func classify(r audit.Record) (Category, string, bool, bool) {
	e, ok := catalog[r.Action]
	if !ok || (e.when != nil && !e.when(r)) {
		return "", "", false, false
	}
	var title string
	switch r.Outcome {
	case audit.OutcomeFailure:
		title = e.failed
	case audit.OutcomeDenied:
		title = e.refused
	default:
		title = e.title
	}
	return e.category, title, title != "", e.always
}

// Titles for the tasks a failure of which means something particular; any
// other task is named.
var taskTitles = map[string][2]string{
	"egress.health":   {"The tunnel is down — transfers are paused", "The tunnel is back"},
	"database.backup": {"Backups are failing", "Backups are working again"},
}

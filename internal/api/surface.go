package api

import (
	"fmt"
	"sort"
	"strings"
)

// The API surface, described by the routing table itself.
//
// # Why this is generated and not written
//
// docs/API-SURFACE.md is a list of every route and the access decision taken
// for it. Kept by hand it would be wrong within a week — a route is added, the
// document is not, and a reader checking "is anything anonymous that should not
// be?" gets an answer from a stale file. That is the same defect as a citation
// to a test that no longer exists, and it is worse here, because this is the
// document somebody consults about access control.
//
// So it is produced from Router.Routes() — the real table, built by the real
// RegisterRoutes — and docs.TestTheGeneratedDocumentsAreCurrent fails when the
// committed file differs. The document cannot drift without the build saying so.

// AccessName is the word used for an access class in documentation and logs.
func AccessName(a Access) string {
	switch a {
	case AccessAnonymous:
		return "anonymous"
	case AccessAuthenticated:
		return "authenticated"
	case AccessEnrollment:
		return "enrollment"
	case AccessPermission:
		return "permission"
	}
	return "unknown"
}

// group is the prefix a route is filed under in the document.
func group(pattern string) string {
	switch {
	case !strings.HasPrefix(pattern, "/api/"):
		return "Pages and assets"
	case strings.HasPrefix(pattern, "/api/v1/auth/"), pattern == "/api/v1/setup":
		return "Authentication and enrollment"
	case strings.HasPrefix(pattern, "/api/v1/admin/"):
		return "Administration"
	}
	parts := strings.Split(strings.TrimPrefix(pattern, "/api/v1/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "Other"
	}
	return strings.ToUpper(parts[0][:1]) + parts[0][1:]
}

// SurfaceMarkdown renders the routing table as docs/API-SURFACE.md.
func SurfaceMarkdown(routes []Route) string {
	var b strings.Builder

	b.WriteString("# The HTTP surface\n\n")
	b.WriteString("**Generated from the routing table — do not edit.**\n")
	b.WriteString("Run `go generate ./internal/docs/` after changing routes;\n")
	b.WriteString("`docs.TestTheGeneratedDocumentsAreCurrent` fails while this file is stale.\n\n")

	var anon, hidden, sessionOnly, browser, stub int
	byGroup := map[string][]Route{}
	for _, r := range routes {
		byGroup[group(r.Pattern)] = append(byGroup[group(r.Pattern)], r)
		if r.Access == AccessAnonymous {
			anon++
		}
		if r.Hidden {
			hidden++
		}
		if r.SessionOnly {
			sessionOnly++
		}
		if r.Browser {
			browser++
		}
		if r.Stub {
			stub++
		}
	}

	fmt.Fprintf(&b, "%d routes. **%d are reachable without a session**; every one of "+
		"those also appears in `api.AnonymousAllowlist`, and `Router.register` panics "+
		"at startup if the two ever disagree.\n\n", len(routes), anon)
	fmt.Fprintf(&b, "- **%d hidden** — 404 rather than 403 when unauthorized, so the "+
		"route's existence is not disclosed (requirements §7.3).\n", hidden)
	fmt.Fprintf(&b, "- **%d session-only** — an API token may not use them whatever "+
		"its scope. Credential management lives here.\n", sessionOnly)
	fmt.Fprintf(&b, "- **%d browser-facing** — may redirect a denied navigation "+
		"instead of answering with JSON (ADR-0012).\n", browser)
	// The unbuilt ones, counted rather than remembered. Their access class is
	// enforced today; only the handler is missing, which is what makes it safe
	// to register them early and what makes the count worth publishing.
	fmt.Fprintf(&b, "- **%d not implemented** — the access class is enforced, "+
		"the handler answers `501`.\n\n", stub)

	b.WriteString("| Column | Meaning |\n|---|---|\n")
	b.WriteString("| `anonymous` | No session required. |\n")
	b.WriteString("| `enrollment` | A session in `awaiting_mfa` may use it. Nothing else. |\n")
	b.WriteString("| `authenticated` | Active, MFA-satisfied. Sessions and API tokens. |\n")
	b.WriteString("| a permission name | That permission, checked before the handler runs. |\n\n")

	names := make([]string, 0, len(byGroup))
	for g := range byGroup {
		names = append(names, g)
	}
	sort.Strings(names)

	for _, g := range names {
		fmt.Fprintf(&b, "## %s\n\n", g)
		b.WriteString("| Method | Path | Requires | Notes |\n|---|---|---|---|\n")
		for _, r := range byGroup[g] {
			requires := AccessName(r.Access)
			if r.Access == AccessPermission {
				requires = "`" + string(r.Permission) + "`"
			}
			var notes []string
			if r.Hidden {
				notes = append(notes, "hidden")
			}
			if r.SessionOnly {
				notes = append(notes, "session only")
			}
			if r.Browser {
				notes = append(notes, "browser")
			}
			if r.Stub {
				notes = append(notes, "**501**")
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n",
				r.Method, r.Pattern, requires, strings.Join(notes, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

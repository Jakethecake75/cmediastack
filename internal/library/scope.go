package library

import (
	"context"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// What a person may see of the library, applied where the rows are read
// (ADR-0037).
//
// A library is a root folder. An account sees every one or the ones it is
// granted, and with a rating ceiling it sees only titles rated at or below it —
// an unrated title is above every ceiling. The filter is SQL, not a loop over
// what came back, so that a count, a LIMIT and a single-row lookup are all
// filtered the same way, and a title out of scope reads as absent.

// Visible returns a boolean SQL expression, and its arguments, that is true for
// a media_item row the context's principal may see. alias is the table's alias
// in the query, or "" when it has none.
//
// An anonymous or non-actable principal gets "0": nothing. Background work and
// an unrestricted account with no ceiling get "1".
func Visible(ctx context.Context, alias string) (string, []any) {
	return ScopeClause(authz.ScopeFromContext(ctx), alias)
}

// ScopeClause is Visible for a scope already in hand.
func ScopeClause(s authz.Scope, alias string) (string, []any) {
	if s.MatchesNothing() {
		return "0", nil
	}
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	var parts []string
	var args []any
	if !s.AllLibraries {
		parts = append(parts, col("root_folder_id")+" IN ("+
			strings.TrimSuffix(strings.Repeat("?,", len(s.LibraryIDs)), ",")+")")
		for _, id := range s.LibraryIDs {
			args = append(args, id)
		}
	}
	if s.RatingCeiling > 0 {
		// Films and series only (ADR-0044): music and books carry no
		// certification, and are governed by the libraries granted.
		parts = append(parts, "("+col("kind")+" NOT IN ('movie', 'series') OR ("+
			col("rating_rank")+" IS NOT NULL AND "+col("rating_rank")+" <= ?))")
		args = append(args, s.RatingCeiling)
	}
	if len(parts) == 0 {
		return "1", nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", args
}

// Certification ranks (ADR-0037, decision 3). US film certifications and US
// television content ratings, on one scale. Anything else — "NR", an empty
// string, another country's — is unrated.
var certificationRanks = map[string]int{
	"G": 1, "TV-Y": 1, "TV-G": 1,
	"PG": 2, "TV-Y7": 2, "TV-Y7-FV": 2, "TV-PG": 2,
	"PG-13": 3, "TV-14": 3,
	"R": 4, "TV-MA": 4,
	"NC-17": 5,
}

// RatingRank is a certification's rank, or 0 when it is not one this instance
// knows. Case and surrounding space are ignored.
func RatingRank(certification string) int {
	return certificationRanks[strings.ToUpper(strings.TrimSpace(certification))]
}

// CanonicalCertification returns a known certification as it is written, or ""
// for one that is not known.
func CanonicalCertification(certification string) string {
	c := strings.ToUpper(strings.TrimSpace(certification))
	if _, ok := certificationRanks[c]; !ok {
		return ""
	}
	return c
}

// CeilingLabels says what each ceiling admits, for a form.
var CeilingLabels = map[int]string{
	0: "No limit",
	1: "G, TV-Y, TV-G",
	2: "up to PG, TV-PG",
	3: "up to PG-13, TV-14",
	4: "up to R, TV-MA",
	5: "up to NC-17",
}

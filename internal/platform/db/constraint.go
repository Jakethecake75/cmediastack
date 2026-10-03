package db

import (
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation reports whether err is a UNIQUE or PRIMARY KEY constraint
// refusing a row — the one database error a caller may answer as an outcome
// rather than a failure: "that already exists".
//
// Anything else — a locked database, a full disk, a foreign key — is a failure,
// and a caller that treated every insert error as "already exists" would report
// success while nothing was recorded.
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	}
	return false
}

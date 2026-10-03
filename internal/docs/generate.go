// Package docs holds no code that ships. This file is the generator behind
// docs/API-SURFACE.md and docs/DATA-MODEL.md.
//
// Both documents describe something the compiler already knows: the routing
// table, and the schema the migrations produce. Written by hand they would be
// wrong within a week, and wrong in the worst way — a reader asking "is
// anything anonymous that should not be?" or "what does this column mean?"
// would get a confident answer from a stale file. That is the same failure as a
// citation to a test that no longer exists, which this package already exists
// to catch.
//
// So they are generated, and TestTheGeneratedDocumentsAreCurrent fails while
// either file differs from what the tree would produce now.
package docs

//go:generate go run ./cmd/gendocs

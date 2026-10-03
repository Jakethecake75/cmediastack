// Command gendocs writes the documents that describe the tree to the tree.
//
// Run it with `go generate ./internal/docs/`.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jakethecake75/cmediastack/internal/docs"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		die(err)
	}
	// Walk up to the module root so the command works from anywhere.
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			die(fmt.Errorf("gendocs: no go.mod above %s", root))
		}
		root = parent
	}

	surface, err := docs.APISurface()
	if err != nil {
		die(err)
	}
	model, err := docs.DataModel()
	if err != nil {
		die(err)
	}
	for name, body := range map[string]string{
		"docs/API-SURFACE.md": surface,
		"docs/DATA-MODEL.md":  model,
	} {
		path := filepath.Join(root, name)
		// #nosec G306 -- a document committed to the repository, readable like every other
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			die(err)
		}
		fmt.Println("wrote", name)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

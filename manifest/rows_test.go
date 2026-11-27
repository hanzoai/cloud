package manifest

import (
	"os"
	"regexp"
	"testing"
)

// An app's row names it with a string literal, never a constant. The image's
// Dockerfile, the Makefile, mk/fleet.mk and cmd/cone each read the roster out of
// apps.go with `{Name: "…"` (a build cannot import the package it is building),
// so a row written `{Name: Auto,` is not in the roster: its plugin is never
// linked, the host finds no binary, and every route under its prefix answers 503
// "no instance running". A constant for a peer to import (Auto, Research) is fine
// beside the row; the row itself is the literal.
func TestEveryRowNamesItselfByLiteral(t *testing.T) {
	src, err := os.ReadFile("apps.go")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`(?m)^\t\{Name:`)
	lit := regexp.MustCompile(`(?m)^\t\{Name: "[^"]+"`)
	rows, lits := len(row.FindAll(src, -1)), len(lit.FindAll(src, -1))
	if rows != lits {
		t.Fatalf("%d rows of apps.go name the app by something other than a string literal (%d rows, %d literal): the build derives its plugin list from the literal", rows-lits, rows, lits)
	}
	if rows != len(Apps) {
		t.Fatalf("apps.go has %d rows by this pattern and Apps has %d: a row is written some other way, and the build will not see it", rows, len(Apps))
	}
}

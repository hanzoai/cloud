package git

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The point of the Repository model is a BOUNDARY, and a boundary that is not
// enforced is a comment. These tests fail if the read plane starts reaching for
// go-git again — which is exactly how the coupling grew the first time, one
// innocent import at a time.

// goGitAllowed lists the files permitted to import go-git, with the reason each
// one earns it. Everything else reads through Repository (repository.go).
var goGitAllowed = map[string]string{
	"gitbackend.go": "the git backend — the whole point is that go-git lives HERE",
	"push.go":       "the write path: builds commit/tree objects directly",
	"storage.go":    "repository initialisation over the S3-backed storer",
}

func TestReadPlaneDoesNotImportGoGit(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			if !strings.Contains(imp.Path.Value, "go-git/go-git") {
				continue
			}
			if _, ok := goGitAllowed[name]; !ok {
				t.Errorf("%s imports go-git (%s).\n"+
					"The read plane must go through the Repository model in repository.go —\n"+
					"resolve a ref to a Revision, then read Tree/Blob/Log/WalkText. If this file\n"+
					"genuinely belongs to the git backend or the write/storage plane, add it to\n"+
					"goGitAllowed with the reason.", name, imp.Path.Value)
			}
		}
	}
}

// A stale entry in the allow-list is its own kind of rot: it silently re-opens
// the door for a file that has already been cleaned up.
func TestGoGitAllowListHasNoStaleEntries(t *testing.T) {
	fset := token.NewFileSet()
	for name := range goGitAllowed {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("allow-list names %s, which does not parse: %v", name, err)
			continue
		}
		found := false
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "go-git/go-git") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is on the go-git allow-list but no longer imports go-git — drop it "+
				"so the exemption cannot be inherited by unrelated code.", name)
		}
	}
}

// ShortRev is what the browse API's shortSha field is built from; it has always
// been seven characters and changing it would change the wire format.
func TestShortRevIsSevenChars(t *testing.T) {
	const full = Revision("beee9aa8909f55bb60d9b55b10d3908802426191")
	if got := ShortRev(full); got != "beee9aa" {
		t.Fatalf("ShortRev = %q, want %q (the browse API's shortSha is 7 chars)", got, "beee9aa")
	}
	// Shorter than seven: returned whole rather than panicking on the slice.
	if got := ShortRev(Revision("abc")); got != "abc" {
		t.Fatalf("ShortRev(short) = %q, want %q", got, "abc")
	}
	if got := ShortRev(""); got != "" {
		t.Fatalf("ShortRev(empty) = %q, want empty", got)
	}
}

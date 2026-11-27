package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	objcache "github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// TestEditKeepsUntouchedTrees is the edit. One changed file rewrites the
// directories on its path and leaves every other tree byte-for-byte, including
// the mode of an executable and a symlink it did not touch. A file may replace
// a directory, and a directory may replace a file.
func TestEditKeepsUntouchedTrees(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "r.git")
	gitRun(t, "", "init", "--bare", "-q", "-b", "main", bare)
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	for _, dir := range []string{"keep", "touch"} {
		if err := os.MkdirAll(filepath.Join(work, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "keep", "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "touch", "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "touch", "run.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(work, "keep", "link")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "update-index", "--chmod=+x", "touch/run.sh")
	gitRun(t, work, "commit", "-q", "-m", "seed")
	gitRun(t, work, "push", "-q", bare, "HEAD:refs/heads/main")

	st := filesystem.NewStorage(osfs.New(bare), objcache.NewObjectLRUDefault())
	ref, err := st.Reference(plumbing.NewBranchReferenceName("main"))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := object.GetCommit(st, ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	before := gitOut(t, "", "--git-dir="+bare, "rev-parse", "HEAD:keep")

	h, err := writeTreeWithFiles(st, tree, []blob{
		{path: "touch/b.txt", content: []byte("edited\n")},
		{path: "fresh/z.txt", content: []byte("z\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, "", "--git-dir="+bare, "rev-parse", h.String()+":keep"); got != before {
		t.Fatalf("untouched tree rewritten: %s want %s", got, before)
	}
	if got := gitOut(t, "", "--git-dir="+bare, "cat-file", "-p", h.String()+":touch/b.txt"); got != "edited" {
		t.Fatalf("edited file: %q", got)
	}
	if got := gitOut(t, "", "--git-dir="+bare, "cat-file", "-p", h.String()+":fresh/z.txt"); got != "z" {
		t.Fatalf("new file: %q", got)
	}
	touch := gitOut(t, "", "--git-dir="+bare, "ls-tree", h.String()+":touch")
	if !strings.Contains(touch, "100755 blob") || !strings.Contains(touch, "run.sh") {
		t.Fatalf("executable was not kept:\n%s", touch)
	}
	keep := gitOut(t, "", "--git-dir="+bare, "ls-tree", h.String()+":keep")
	if !strings.Contains(keep, "120000 blob") || !strings.Contains(keep, "link") {
		t.Fatalf("symlink was not kept:\n%s", keep)
	}

	flat, err := writeTreeWithFiles(st, tree, []blob{{path: "keep", content: []byte("flat\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, "", "--git-dir="+bare, "ls-tree", flat.String(), "keep"); !strings.HasPrefix(got, "100644 ") {
		t.Fatalf("file did not replace directory: %s", got)
	}
	nested, err := writeTreeWithFiles(st, tree, []blob{{path: "touch/b.txt/nested.txt", content: []byte("n\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, "", "--git-dir="+bare, "cat-file", "-t", nested.String()+":touch/b.txt"); got != "tree" {
		t.Fatalf("directory did not replace file: %s", got)
	}
}

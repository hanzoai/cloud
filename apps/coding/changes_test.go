package coding

// changes_test.go pins what a run's change names: where its pull request is when
// the forge does not hold it, and the path of every file whatever the checkout's
// git is configured to print.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/hanzoai/cloud/client"
)

// A PULL REQUEST THE FORGE DOES NOT HOLD IS STILL THE RUN'S: its session names it,
// and the change answers where it is and its number.
func TestChanges_NameThePullRequestTheSessionRecorded(t *testing.T) {
	_, sessions := servesBox(t)
	stored(t)
	x := sessions[runSession]
	x.PR = "https://github.com/hanzoai/site/pull/13"
	sessions[runSession] = x
	got, err := Changes(asCaller("sub-zoe"), runSession)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if got.Pull == nil || got.Pull.URL != x.PR || got.Pull.Number != 13 {
		t.Fatalf("the pull request read as %+v, want %s #13", got.Pull, x.PR)
	}
	if got.Pull.Reviews == nil {
		t.Error("a recorded pull request carries null reviews; the wire promises a list")
	}
}

// A RUN THAT PROPOSED NOTHING HAS NO PULL REQUEST to name.
func TestChanges_NameNoPullRequestTheSessionDoesNot(t *testing.T) {
	servesBox(t)
	stored(t)
	got, err := Changes(asCaller("sub-zoe"), runSession)
	if err != nil || got.Pull != nil {
		t.Fatalf("Changes = %+v, %v; want no pull request", got.Pull, err)
	}
}

// EVERY FILE OF A CHANGE HAS ITS PATH, whatever git is told to print. A checkout
// whose git drops or renames the a/ and b/ of a file header (`diff.noprefix`,
// `diff.mnemonicPrefix`) printed headers diffFiles could not read a path from, and
// every file came back with the right counts and no name.
func TestChangeRead_NamesEveryFileWhateverGitIsConfiguredToPrint(t *testing.T) {
	kit.Tool(t, "git", "bash")
	for _, config := range []string{"[diff]\n\tnoprefix = true\n", "[diff]\n\tmnemonicPrefix = true\n"} {
		dir := t.TempDir()
		home := filepath.Join(dir, "home")
		work := filepath.Join(dir, "work")
		for _, d := range []string{home, work} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		gitconfig := filepath.Join(home, "gitconfig")
		if err := os.WriteFile(gitconfig, []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		env := append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL="+gitconfig, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
		do := func(name string, args ...string) string {
			t.Helper()
			c := exec.Command(name, args...)
			c.Dir, c.Env = work, env
			out, err := c.CombinedOutput()
			if err != nil {
				t.Fatalf("%s %v: %v\n%s", name, args, err, out)
			}
			return string(out)
		}
		write := func(name, body string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		do("git", "init", "-q", "-b", "agent/x")
		write("kept.txt", "one\n")
		write("gone.txt", "bye\n")
		do("git", "add", "-A")
		do("git", "commit", "-q", "-m", "First")
		do("git", "update-ref", baseRef+"agent/x", "HEAD")
		write("kept.txt", "one\ntwo\n")
		write("fresh.txt", "new\n")
		if err := os.Remove(filepath.Join(work, "gone.txt")); err != nil {
			t.Fatal(err)
		}

		out := do("bash", "-c", changeRead, "bash", work)
		var got client.CodingChanges
		readChange(out, &got)
		var paths []string
		for _, f := range got.Files {
			paths = append(paths, f.Path)
		}
		slices.Sort(paths)
		if want := []string{"fresh.txt", "gone.txt", "kept.txt"}; !slices.Equal(paths, want) {
			t.Errorf("with %q the change names %q, want %q", config, paths, want)
		}
	}
}

// A run started on the repository's default branch recorded no base, so its
// change named none; the checkout's origin/HEAD says which branch that was. A
// base the run did record is kept.
func TestChangeRead_NamesTheDefaultBranchARunStartedFrom(t *testing.T) {
	kit.Tool(t, "git", "bash")
	dir := t.TempDir()
	origin, work := filepath.Join(dir, "origin"), filepath.Join(dir, "work")
	env := append(os.Environ(), "HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
	do := func(in string, args ...string) string {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir, c.Env = in, env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	do(origin, "git", "init", "-q", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	do(origin, "git", "add", "-A")
	do(origin, "git", "commit", "-q", "-m", "First")
	do(dir, "git", "clone", "-q", origin, work)
	do(work, "git", "checkout", "-q", "-b", "agent/x")
	do(work, "git", "update-ref", baseRef+"agent/x", "HEAD")

	out := do(dir, "bash", "-c", changeRead, "bash", work)
	var got client.CodingChanges
	readChange(out, &got)
	if got.Base != "trunk" {
		t.Fatalf("base = %q, want the default branch the clone started on", got.Base)
	}
	kept := client.CodingChanges{Base: "release"}
	readChange(out, &kept)
	if kept.Base != "release" {
		t.Fatalf("a recorded base was replaced: %q", kept.Base)
	}
}

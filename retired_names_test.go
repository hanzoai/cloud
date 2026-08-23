package cloud_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A name this codebase has stopped meaning must stop appearing in it.
//
// The one here is `casibase`. It was the upstream this binary's account and
// session layer grew out of, and it survived only in prose — nineteen comments
// across seven files explaining a surface by naming where it came from rather
// than saying what it is. None of it was an identifier; all of it was the kind of
// reference that keeps an origin alive in a reader's head long after the code has
// moved on, and reads to anyone outside as a fork wearing someone else's name.
//
// The comments now describe the thing: the embedded account surface, the
// {status,msg,data} envelope Visor still answers with. That reads better AND is
// more accurate — a reader who has never heard of the upstream loses nothing.
//
// The check is lexical and blunt on purpose. A retired name comes back the way it
// left: one comment at a time, each individually harmless, written by someone who
// read the last one and matched it.
//
// WHAT THIS DOES NOT COVER: the volume subPath of the same name, which is a real
// directory of real bytes on a real disk. A path is data, not vocabulary — it is
// renamed by moving what it holds, not by editing a string, and doing that
// wrongly loses the bytes. See charts/app/values/hanzo/cloud.yaml in universe.
func TestRetiredNamesStayRetired(t *testing.T) {
	retired := []struct{ name, why string }{
		{"casibase", "the embedded account surface is described by what it does, not by the upstream it grew from"},
	}

	// Extensions worth policing: source and the prose that ships beside it. A name
	// in a lockfile or a vendored tree is not this repository saying it.
	//
	// This list started as .go and .md and that was too narrow — the first sweep
	// under it reported the tree clean while the word was still sitting in
	// TypeScript. A guard is only as wide as the thing it walks.
	watched := map[string]bool{
		".go": true, ".md": true, ".ts": true, ".tsx": true,
		".yaml": true, ".yml": true, ".json": true, ".sh": true,
	}

	skip := map[string]bool{
		"vendor": true, "node_modules": true, "dist": true,
		"testdata": true, "explorer": true,
	}

	var findings []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A DOT-DIRECTORY IS NEVER THIS MODULE'S SOURCE — .git, and equally
			// .claude/worktrees and .worktrees, which hold whole checkouts of this
			// same repo at other commits. Naming ".git" alone let those be walked,
			// so a name retired HERE was reported as "back" because an old checkout
			// still spelled it — a finding under a path that exists for nobody else,
			// indistinguishable from a real regression. The sibling guard
			// (typed_request_gate_test.go) already skips them for the same reason;
			// this is that rule, not a second one.
			if path != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !watched[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		// This file names what it bans; it is the one place the word belongs.
		if filepath.Base(path) == "retired_names_test.go" {
			return nil
		}
		// NOTICE is exempt, and NOT as an oversight — deleting from it would be a
		// license violation. The AI, RAG and search surfaces derive from an
		// Apache-2.0 work, and §4(d) requires a derivative to carry the upstream's
		// attribution. The entry goes when the derivation does, not before, and
		// whoever removes one removes the other in the same change.
		if filepath.Base(path) == "NOTICE" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := strings.ToLower(string(b))
		for _, r := range retired {
			if strings.Contains(src, r.name) {
				findings = append(findings, path+": "+r.name+" — "+r.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, f := range findings {
		t.Errorf("a retired name is back: %s", f)
	}
}

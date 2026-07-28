package git

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBrowsePaths proves the read a delivery generator makes: one call turns a
// glob into the inventory at a pinned revision. The tree is the real shape —
// charts/app/values/<namespace>/<name>.yaml — because that is what the fleet
// ApplicationSet globs, and a matcher that crosses a `/` would silently widen
// the fleet rather than fail.
func TestBrowsePaths(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "universe"}); code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", code, b)
	}

	bareAbs := mounted.Load().State.storage.absRepoPath("acme", "", "universe")
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	for _, f := range []string{
		"charts/app/values/hanzo/www.yaml",
		"charts/app/values/hanzo/iam.yaml",
		"charts/app/values/tenant-maxpower/site.yaml",
		"charts/app/values-review/hanzo/chat.yaml", // staged out — must NOT match
		"charts/app/Chart.yaml",                    // wrong depth — must NOT match
		"charts/app/values/hanzo/nested/deep.yaml", // one level too deep for */*
		"README.md",
	} {
		if err := os.MkdirAll(filepath.Join(work, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, work, f, "# "+f+"\n")
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "seed")
	gitRun(t, work, "push", "-q", bareAbs, "main:refs/heads/main")

	get := func(glob string) pathsJSON {
		t.Helper()
		code, body := do(t, app, http.MethodGet, "/v1/git/repos/universe/paths?ref=main&glob="+glob, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("paths %q: %d %s", glob, code, body)
		}
		var out pathsJSON
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("paths json: %v (%s)", err, body)
		}
		return out
	}

	// The fleet glob. `*` must not cross a `/`, so the nested file and the
	// values-review tree are both excluded.
	got := get("charts/app/values/*/*.yaml")
	want := []string{
		"charts/app/values/hanzo/iam.yaml",
		"charts/app/values/hanzo/www.yaml",
		"charts/app/values/tenant-maxpower/site.yaml",
	}
	if strings.Join(got.Paths, ",") != strings.Join(want, ",") {
		t.Fatalf("fleet glob:\n got %v\nwant %v", got.Paths, want)
	}
	if len(got.Rev) != 40 {
		t.Fatalf("rev not pinned to a full revision: %q", got.Rev)
	}

	// `**` spans segments, so the nested file joins.
	if p := get("charts/app/values/**/*.yaml").Paths; len(p) != 4 {
		t.Fatalf("** glob returned %d paths, want 4: %v", len(p), p)
	}

	// `**` as the final segment takes everything beneath.
	if p := get("charts/**").Paths; len(p) != 6 {
		t.Fatalf("trailing ** returned %d paths, want 6: %v", len(p), p)
	}

	// A glob matching nothing is an empty list, not an error — a generator
	// pointed at a path that does not exist yet must see "no services", not a
	// failure it will retry forever.
	if p := get("does/not/exist/*.yaml").Paths; len(p) != 0 {
		t.Fatalf("missing prefix returned %v, want none", p)
	}

	// Directories are never results.
	for _, p := range get("charts/app/*").Paths {
		if !strings.HasSuffix(p, ".yaml") {
			t.Fatalf("glob returned a non-file: %q", p)
		}
	}

	// An absent glob is a bad request, not a full-tree dump.
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/universe/paths?ref=main", "acme", nil); code != http.StatusBadRequest {
		t.Fatalf("empty glob: %d, want 400", code)
	}

	// Isolation matches the rest of git: another org cannot read the repo.
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/universe/paths?ref=main&glob=**", "other", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org read: %d, want 404", code)
	}
}

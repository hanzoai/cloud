package git

// The OTHER ref writers, proved the same way refpolicy_wire_test.go proves the
// first: with the real client, against the real server, doing the thing an
// attacker would actually do.
//
// refpolicy_wire_test.go proved the rule was correct AND reached — at ONE writer.
// That was the false comfort: the rule was reached at the writer the test
// exercised, and nowhere else. A red-team review walked in through the side of the
// building with the same credential. So each test here is named for the writer it
// exercises, and the file fails if a writer is reopened.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// ── writer 2: the client-less push ───────────────────────────────────────────

// THE ATTACK, exactly as Red walked it: no git wire protocol at all, one JSON
// POST. The run opens a clean PR on agent/<session>, a human reads it, and
// before the merge the run posts a FAST-FORWARD CHILD onto the same branch.
//
// This is worse than the force-push the wire writer already refused, because it is
// append-only: nothing anywhere reports that the branch was rewritten. The
// reviewer approved A and merges A+B.
func TestClientlessPushCannotAppendToAnAgentBranch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
			t.Fatalf("create repo: %d %s", code, b)
		}
		// The run's branch, created legitimately through the same writer.
		code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
			"branch": "agent/abc123def456", "message": "the reviewed change",
			"files": []map[string]any{{"path": "a.txt", "content": "reviewed\n"}},
		})
		if code != 200 {
			t.Fatalf("the legitimate first push must land: %d %s", code, b)
		}

		// The switch. Same writer, same credential, one more file.
		code, b = do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
			"branch": "agent/abc123def456", "message": "the payload",
			"files": []map[string]any{{"path": "evil.txt", "content": "payload\n"}},
		})
		if code == 200 {
			t.Fatalf("THE BAIT-AND-SWITCH LANDED through /repos/:name/push: %d %s", code, b)
		}
		if !strings.Contains(string(b), "agent branch") {
			t.Fatalf("refused, but not by the ref policy: %d %s", code, b)
		}
		t.Logf("REFUSED: %s", strings.TrimSpace(string(b)))
	})
}

// A client-less push to an ordinary branch is untouched — the control must not
// break the hanzo.app builder it exists for.
func TestClientlessPushToAnOrdinaryBranchStillWorks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "site"}); code != 201 {
			t.Fatalf("create repo: %d %s", code, b)
		}
		for i, content := range []string{"one", "two"} {
			code, b := do(t, app, http.MethodPost, "/v1/git/repos/site/push", "acme", map[string]any{
				"branch": "main", "message": "build",
				"files": []map[string]any{{"path": "index.html", "content": content}},
			})
			if code != 200 {
				t.Fatalf("push %d must land: %d %s", i, code, b)
			}
		}
		t.Log("two successive pushes to main landed — the builder's path is untouched")
	})
}

// ── writers 3 + 4: the cache fill ───────────────────────────────────────────

// A link's fill force-writes every branch and tag from its host, so a link may
// only ever be made where nothing local is there to overwrite. Pointed at an
// attacker-chosen source over a repository with its own history, it would do in
// one call everything the push writer refuses — rewrite the branch under a PR a
// human is reading, or delete it. The link is refused instead, and the reviewed
// agent branch stays where it was.
func TestALinkCannotReachAHistoryHeldHere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
			t.Fatalf("create repo: %d %s", code, b)
		}
		// The reviewed agent branch, in native.
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
			"branch": "agent/abc123def456", "message": "reviewed",
			"files": []map[string]any{{"path": "a.txt", "content": "reviewed\n"}},
		}); code != 200 {
			t.Fatalf("seed the agent branch: %d %s", code, b)
		}
		before := refTip(t, app, "acme", "code", "agent/abc123def456")
		if before == "" {
			t.Fatal("the agent branch was not seeded")
		}

		// The attacker's source: it carries main and its OWN agent/abc123def456.
		srcRoot := t.TempDir()
		work := t.TempDir()
		gitRun(t, work, "init", "-q", "-b", "main")
		write(t, work, "README.md", "# theirs\n")
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "commit", "-q", "-m", "theirs")
		gitRun(t, work, "branch", "agent/abc123def456")
		gitRun(t, "", "clone", "-q", "--bare", work, filepath.Join(srcRoot, "evil.git"))
		srcURL := serveGitHTTP(t, srcRoot) + "/evil.git"

		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/link", "acme",
			map[string]any{"source": srcURL}); code != http.StatusConflict {
			t.Fatalf("a link over a history held here answered %d %s", code, b)
		}
		if after := refTip(t, app, "acme", "code", "agent/abc123def456"); after != before {
			t.Fatalf("THE LINK REWROTE A REVIEWED AGENT BRANCH: %s -> %s", before, after)
		}
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// refTip reads one branch's tip through the browse API, or "" when absent.
func refTip(t *testing.T, app *zip.App, org, repo, branch string) string {
	t.Helper()
	code, b := do(t, app, http.MethodGet, "/v1/git/repos/"+repo+"/refs", org, nil)
	if code != 200 {
		t.Fatalf("browse refs: %d %s", code, b)
	}
	var out refsJSON
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode refs %s: %v", b, err)
	}
	for _, r := range out.Branches {
		if r.Name == branch {
			return r.SHA
		}
	}
	return ""
}

// ── writer 5: the merge path ─────────────────────────────────────────────────

// TestMergeObeysTheRefPolicy proves merging is not a way around checkRefPolicy:
// an agent branch may be created and never rewritten, and that holds whether the
// rewrite arrives as a push or as a merge.
func TestMergeObeysTheRefPolicy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		a := pushTo(t, app, "acme", "code", "main", "README.md", "hello")
		forkBranch(t, "acme", "code", "agent/run-1", a) // as a run's push would leave it
		forkBranch(t, "acme", "code", "feature", a)
		pushTo(t, app, "acme", "code", "feature", "x.go", "package x")

		// feature IS a fast-forward of agent/run-1 — only the policy stands in the way.
		code, body := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
			"title": "rewrite the run", "head": "feature", "base": "agent/run-1",
		})
		if code != http.StatusCreated {
			t.Fatalf("open: %d %s", code, body)
		}
		code, body = do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "acme", nil)
		if code != http.StatusBadRequest {
			t.Fatalf("merge into an agent branch: %d %s, want 400", code, body)
		}
		if !strings.Contains(string(body), "agent branch") {
			t.Fatalf("refusal does not name the policy: %s", body)
		}
		if now := branchAt(t, "acme", "code", "agent/run-1"); now != a {
			t.Fatalf("agent/run-1 moved to %s past the ref policy, want %s", now, a)
		}
	})
}

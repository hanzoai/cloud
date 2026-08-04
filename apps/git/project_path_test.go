package git

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zap-proto/zip"
)

// A project-scoped repo is reachable only through the three-segment path. The
// scope otherwise rides X-Project-Id, and `git clone` sends no headers, so
// without the path form such a repo has no usable remote at all.

// doScoped is do() with a project sub-scope, which is how a project-scoped repo
// is created in the first place.
func doScoped(t *testing.T, app *zip.App, method, path, org, project string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func scopedHeaderArgs(org, project string) []string {
	args := orgHeaderArgs(org)
	if project != "" {
		args = append(args, "-c", "http.extraHeader=X-Project-Id: "+project)
	}
	return args
}

// gitTry runs git and returns the error instead of failing the test, for the
// cases where the refusal IS the assertion.
func gitTry(t *testing.T, dir string, args ...string) error {
	t.Helper()
	_, err := gitTestCmd(dir, args...).CombinedOutput()
	return err
}

// TestProjectPathClonePushRoundTrip is the before/after: the same repo name in
// two projects, each pushed and cloned through its own three-segment URL, with
// the commits proving they are distinct repositories rather than one.
func TestProjectPathClonePushRoundTrip(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	type scoped struct{ project, content, commit, url string }
	repos := []*scoped{
		{project: "hanzo-apps", content: "# the site\n"},
		{project: "hanzo-docs", content: "# the docs\n"},
	}

	for _, r := range repos {
		if code, b := doScoped(t, app, "POST", "/v1/git/repos", "hanzo", r.project, map[string]any{"name": "ai"}); code != 201 {
			t.Fatalf("create hanzo/%s/ai: %d %s", r.project, code, b)
		}
		r.url = base + "/v1/git/hanzo/" + r.project + "/ai.git"

		work := t.TempDir()
		gitRun(t, work, "init", "-q", "-b", "main")
		if err := os.WriteFile(filepath.Join(work, "README.md"), []byte(r.content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "commit", "-q", "-m", "first")
		r.commit = gitOut(t, work, "rev-parse", "HEAD")
		gitRun(t, work, "remote", "add", "origin", r.url)
		gitRun(t, work, append(scopedHeaderArgs("hanzo", r.project), "push", "origin", "main")...)
	}

	if repos[0].commit == repos[1].commit {
		t.Fatal("fixture is degenerate: the two repos must hold different commits")
	}

	// Each three-segment URL returns its OWN repo. Before this change both names
	// resolved to one storage path, so the second push would have collided with
	// the first instead of standing beside it.
	for _, r := range repos {
		dst := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", append(scopedHeaderArgs("hanzo", r.project), "clone", "-q", r.url, dst)...)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != r.commit {
			t.Fatalf("%s cloned HEAD %s, want %s", r.url, got, r.commit)
		}
		got, err := os.ReadFile(filepath.Join(dst, "README.md"))
		if err != nil {
			t.Fatalf("%s: read cloned file: %v", r.url, err)
		}
		if string(got) != r.content {
			t.Fatalf("%s cloned %q, want %q", r.url, got, r.content)
		}
	}
}

// TestOrgLevelPathIsUnchanged is the compatibility half: the two-segment URL
// every existing repo is cloned from still resolves to the org-level repo, and
// adding the deeper route did not shadow it.
func TestOrgLevelPathIsUnchanged(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	url := base + "/v1/git/acme/code.git"

	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# org level\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "first")
	commit := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, work, "remote", "add", "origin", url)
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "origin", "main")...)

	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", url, dst)...)
	if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("two-segment clone HEAD %s != pushed %s", got, commit)
	}
}

// A repo created at the org level is NOT reachable under some project, and vice
// versa: the middle segment selects a storage path, so a wrong one is a miss
// rather than a fallback to the org-level repo.
func TestProjectSegmentDoesNotFallBackToOrgLevel(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	dst := filepath.Join(t.TempDir(), "clone")
	err := gitTry(t, "", append(scopedHeaderArgs("acme", "nosuch"),
		"clone", "-q", base+"/v1/git/acme/nosuch/code.git", dst)...)
	if err == nil {
		t.Fatal("an org-level repo must not be served under an arbitrary project segment")
	}
}

// The reported clone URL is the one that works: a project-scoped repo advertises
// its three-segment remote, so a caller never has to know the rule.
func TestReportedCloneURLCarriesTheProject(t *testing.T) {
	app := mountApp(t)
	if code, b := doScoped(t, app, "POST", "/v1/git/repos", "hanzo", "hanzo-apps", map[string]any{"name": "ai"}); code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	code, b := doScoped(t, app, "GET", "/v1/git/repos/ai", "hanzo", "hanzo-apps", nil)
	if code != 200 {
		t.Fatalf("detail: %d %s", code, b)
	}
	var v repoView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	if v.Project != "hanzo-apps" {
		t.Fatalf("project = %q, want hanzo-apps", v.Project)
	}
	for _, want := range []string{"/v1/git/hanzo/hanzo-apps/ai.git", ":hanzo/hanzo-apps/ai.git"} {
		if !bytes.Contains([]byte(v.CloneURL+" "+v.SSHURL), []byte(want)) {
			t.Errorf("advertised URLs %q / %q should contain %q", v.CloneURL, v.SSHURL, want)
		}
	}
}

// An org-level repo keeps advertising the two-segment remote, so nothing that
// already works starts pointing somewhere new.
func TestOrgLevelCloneURLIsUnchanged(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	code, b := do(t, app, "GET", "/v1/git/repos/code", "acme", nil)
	if code != 200 {
		t.Fatalf("detail: %d %s", code, b)
	}
	var v repoView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	if v.Project != "" {
		t.Fatalf("project = %q, want empty", v.Project)
	}
	if !bytes.HasSuffix([]byte(v.CloneURL), []byte("/v1/git/acme/code.git")) {
		t.Errorf("clone URL %q must keep the two-segment form", v.CloneURL)
	}
	if !bytes.HasSuffix([]byte(v.SSHURL), []byte(":acme/code.git")) {
		t.Errorf("ssh URL %q must keep the two-segment form", v.SSHURL)
	}
}

// The middle segment becomes a storage path segment, so it is validated like
// every other one rather than trusted.
func TestProjectSegmentIsTraversalSafe(t *testing.T) {
	app := mountApp(t)
	for _, bad := range []string{"..", ".", "-x", "a/b"} {
		req := httptest.NewRequest("GET", "/v1/git/acme/"+bad+"/code.git/info/refs?service=git-upload-pack", nil)
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "u_acme")
		resp, err := app.Test(req, testCfg)
		if err != nil {
			continue // the router rejected the shape outright, which is also a refusal
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("project segment %q answered %d; it must never be served", bad, resp.StatusCode)
		}
	}
}

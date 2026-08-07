package git

// The OTHER seven ref writers, proved the same way refpolicy_wire_test.go proves
// the first: with the real client, against the real server, doing the thing an
// attacker would actually do.
//
// refpolicy_wire_test.go proved the rule was correct AND reached — at ONE door.
// That was the false comfort: the rule was reached at the door the test knocked
// on, and nowhere else. A red-team review walked in through the side of the
// building with the same credential. So each test here is named for the door it
// knocks on, and the file fails if a door is reopened.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http/httptest"

	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── writer 3: the client-less push ───────────────────────────────────────────

// THE ATTACK, exactly as Red walked it: no git wire protocol at all, one JSON
// POST. The run opens a clean PR on agent/<session>, a human reads it, and
// before the merge the run posts a FAST-FORWARD CHILD onto the same branch.
//
// This is worse than the force-push the wire door already refused, because it is
// append-only: nothing anywhere reports that the branch was rewritten. The
// reviewer approved A and merges A+B.
func TestClientlessPushCannotAppendToAnAgentBranch(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	// The run's branch, created legitimately through the same door.
	code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
		"branch": "agent/abc123def456", "message": "the reviewed change",
		"files": []map[string]any{{"path": "a.txt", "content": "reviewed\n"}},
	})
	if code != 200 {
		t.Fatalf("the legitimate first push must land: %d %s", code, b)
	}

	// The switch. Same door, same credential, one more file.
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
}

// A client-less push to an ordinary branch is untouched — the control must not
// break the hanzo.app builder it exists for.
func TestClientlessPushToAnOrdinaryBranchStillWorks(t *testing.T) {
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
}

// ── writer 2: SSH receive-pack ───────────────────────────────────────────────

// The same attack over SSH, with the REAL git CLI against the REAL listener.
// This door ran `git receive-pack <bareDir>` with nothing in front of it, so
// every refusal the HTTP door made was available here by enrolling a key —
// which any org member may do at POST /v1/git/keys.
func TestSSHPushCarriesTheSameRefPolicy(t *testing.T) {
	app := mountApp(t)
	priv, authLine := genClientKey(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/keys", "acme",
		map[string]any{"title": "laptop", "publicKey": authLine}); code != 201 {
		t.Fatalf("register key: %d %s", code, b)
	}
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}

	keyPath := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(keyPath, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	sshURL := fmt.Sprintf("ssh://git@%s/acme/code.git", mounted.Load().State.ssh.addr())
	sshEnv := "GIT_SSH_COMMAND=ssh -i " + keyPath +
		" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR"

	run := func(t *testing.T, dir string, args ...string) (string, error) {
		c := gitTestCmd(dir, args...)
		c.Env = append(c.Env, sshEnv)
		out, err := c.CombinedOutput()
		return string(out), err
	}

	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	write(t, work, "README.md", "# seed\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "first")
	gitRun(t, work, "remote", "add", "origin", sshURL)
	if out, err := run(t, work, "push", "origin", "main"); err != nil {
		t.Fatalf("seed push over SSH: %v\n%s", err, out)
	}

	// A legitimate agent-branch create over SSH must still work.
	gitRun(t, work, "checkout", "-q", "-b", "agent/abc123def456")
	write(t, work, "feature.txt", "agent work\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "agent change")
	if out, err := run(t, work, "push", "origin", "HEAD:refs/heads/agent/abc123def456"); err != nil {
		t.Fatalf("the legitimate agent push over SSH must land: %v\n%s", err, out)
	}
	t.Log("SSH: agent branch created")

	// Now every refusal, over SSH, with the real client.
	write(t, work, "evil.txt", "payload\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "payload")

	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"fast-forward onto the reviewed branch", "agent branch",
			[]string{"push", "origin", "HEAD:refs/heads/agent/abc123def456"}},
		// A genuinely divergent tip, so this is a real rewind-and-replace and not
		// git deciding the ref is already where it was asked to go.
		{"force-push over the reviewed branch", "agent branch",
			[]string{"push", "--force", "origin", "main:refs/heads/agent/abc123def456"}},
		{"delete the reviewed branch", "agent branch",
			[]string{"push", "origin", ":refs/heads/agent/abc123def456"}},
		{"delete the default branch", "default branch",
			[]string{"push", "origin", ":refs/heads/main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, work, tc.args...)
			if err == nil {
				t.Fatalf("SSH push SUCCEEDED and must be refused\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("refused, but git did not print the reason (%q):\n%s", tc.want, out)
			}
			if !strings.Contains(out, "[remote rejected]") {
				t.Fatalf("refused without git's own report-status:\n%s", out)
			}
			t.Logf("SSH REFUSED with a reason the pusher can read:\n%s", strings.TrimSpace(out))
		})
	}

	// And the trunk still takes ordinary work — the control must not break SSH.
	if out, err := run(t, work, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("an ordinary SSH push to main must still land: %v\n%s", err, out)
	}
	t.Log("SSH: ordinary push to main still lands")
}

// ── writer 4 + 5: mirror-in ──────────────────────────────────────────────────

// `+refs/*:refs/*` with --prune is the most powerful writer in the forge: it
// force-overwrites every ref from a source the CALLER names, and DELETES any ref
// that source does not have. Pointed at an attacker-chosen upstream it did, in
// one call, everything the push door refuses — including replacing the branch
// under a PR a human is reading, and deleting it outright.
func TestMirrorCannotReachTheAgentNamespace(t *testing.T) {
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

	// The attacker's source: it carries main and its OWN agent/abc123def456, and
	// lacks nothing else. A mirror from it would (a) overwrite ours with theirs
	// and (b) prune anything they omit.
	srcRoot := t.TempDir()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	write(t, work, "README.md", "# theirs\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "theirs")
	gitRun(t, work, "branch", "agent/abc123def456")
	gitRun(t, work, "branch", "agent/brand-new-one")
	gitRun(t, "", "clone", "-q", "--bare", work, filepath.Join(srcRoot, "evil.git"))
	srcURL := serveGitHTTP(t, srcRoot) + "/evil.git"

	code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/mirror", "acme",
		map[string]any{"source": srcURL})
	if code != 200 {
		t.Fatalf("the mirror itself should still work: %d %s", code, b)
	}

	if after := refTip(t, app, "acme", "code", "agent/abc123def456"); after != before {
		t.Fatalf("THE MIRROR REWROTE A REVIEWED AGENT BRANCH: %s -> %s", before, after)
	}
	if tip := refTip(t, app, "acme", "code", "agent/brand-new-one"); tip != "" {
		t.Fatalf("THE MIRROR CREATED AN AGENT BRANCH FROM AN ATTACKER-CHOSEN SOURCE: %s", tip)
	}
	t.Logf("the mirror landed, and the agent namespace is untouched (tip still %s)", before[:8])
}

// ── writer 7: the inbound fast-forward ───────────────────────────────────────

// A fast-forward is not a safe exception. Appending a commit to a branch a
// reviewer has already read IS a fast-forward, and it is precisely the
// capability the policy calls "changing what a name already points at". This
// door took the ref from its argument and only asked git to refuse a
// NON-fast-forward, so the append went through.
func TestInboundSyncCannotAdvanceAnAgentBranch(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
		"branch": "agent/abc123def456", "message": "reviewed",
		"files": []map[string]any{{"path": "a.txt", "content": "reviewed\n"}},
	}); code != 200 {
		t.Fatalf("seed: %d %s", code, b)
	}

	// A well-formed upstream, so the refusal below is the ref policy's and not the
	// URL guard's. The policy returns before any network call is made.
	res, err := githubImporter{}.InboundSync(context.Background(), cloud.GitInboundReq{
		Org: "acme", Repo: "code", Ref: "refs/heads/agent/abc123def456",
		CloneURL: "https://github.com/acme/code.git", Origin: "github.com",
	})
	if err == nil && res.Applied {
		t.Fatal("THE INBOUND DOOR ADVANCED AN AGENT BRANCH")
	}
	if !res.Conflict || !strings.Contains(res.Detail, "agent branch") {
		t.Fatalf("refused, but not by the ref policy: %+v (err=%v)", res, err)
	}
	t.Logf("REFUSED before the fetch ran: %s", res.Detail)
}

// ── the grant: what it can and cannot do ─────────────────────────────────────

// A grant is not an identity. This is the whole of C2's fix, and it is the test
// that matters most: the credential a compromised run holds must open the one
// ref it was minted for and NOTHING else in the platform.
func TestAGrantIsNotAPrincipal(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	tok, _, err := issued.issue(grant{org: "acme", repo: "code", ref: "refs/heads/agent/abc123def456"}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Every control-plane door, with the grant as the credential. All must refuse:
	// the grant resolves to no principal, so tenantOf has nothing to read.
	for _, d := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/git/repos", nil},
		{http.MethodGet, "/v1/git/usage", nil},
		{http.MethodDelete, "/v1/git/repos/code", nil},
		{http.MethodPost, "/v1/git/repos/code/mirror", map[string]any{"source": "https://example.com/x.git"}},
		{http.MethodPost, "/v1/git/repos/code/push", map[string]any{
			"branch": "main", "files": []map[string]any{{"path": "x", "content": "y"}}}},
		{http.MethodPost, "/v1/git/keys", map[string]any{"title": "t", "publicKey": "ssh-ed25519 AAAA"}},
	} {
		code, b := doAuth(t, app, d.method, d.path, tok, d.body)
		if code != http.StatusForbidden {
			t.Fatalf("A GRANT REACHED %s %s: %d %s", d.method, d.path, code, b)
		}
	}
	t.Log("the grant reaches none of the control plane — it is not a principal anywhere")
}

// A grant addresses the repository it names, and answers the stranger's 404 for
// any other — so it cannot even be used to learn which repositories an org has.
func TestAGrantOpensOnlyItsOwnRepo(t *testing.T) {
	app := mountApp(t)
	for _, name := range []string{"code", "secrets"} {
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": name}); code != 201 {
			t.Fatalf("create %s: %d %s", name, code, b)
		}
	}
	tok, _, err := issued.issue(grant{org: "acme", repo: "code", ref: "refs/heads/agent/abc123def456"}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if code, b := doAuth(t, app, http.MethodGet,
		"/v1/git/acme/code/info/refs?service=git-receive-pack", tok, nil); code != 200 {
		t.Fatalf("the grant must open its own repo: %d %s", code, b)
	}
	if code, b := doAuth(t, app, http.MethodGet,
		"/v1/git/acme/secrets/info/refs?service=git-receive-pack", tok, nil); code != http.StatusNotFound {
		t.Fatalf("A GRANT REACHED ANOTHER REPO: %d %s", code, b)
	}
	t.Log("the grant opens acme/code and answers 404 for acme/secrets")
}

// The whole thing end to end, with the REAL git CLI holding ONLY the grant —
// which is exactly what the sandbox holds. It must be able to do its job, and
// nothing else.
func TestARunPushesWithItsGrantAndNothingMore(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	// Seed a trunk as the org's human would.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
		"branch": "main", "message": "seed",
		"files": []map[string]any{{"path": "README.md", "content": "# seed\n"}},
	}); code != 200 {
		t.Fatalf("seed main: %d %s", code, b)
	}

	branch := "agent/abc123def456"
	tok, _, err := issued.issue(grant{org: "acme", repo: "code", ref: "refs/heads/" + branch}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// The ONLY credential the run carries. No X-Org-Id, no X-User-Id — the
	// sandbox has no identity headers to send and no principal behind it.
	auth := []string{"-c", "http.extraHeader=Authorization: Basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))}
	url := base + "/v1/git/acme/code.git"

	work := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(auth, "clone", "-q", url, work)...)
	t.Log("the grant cloned its repo")

	gitRun(t, work, "checkout", "-q", "-b", branch)
	write(t, work, "feature.txt", "agent work\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "agent change")
	if out, err := gitTestCmd(work, append(auth, "push", "origin", "HEAD:refs/heads/"+branch)...).CombinedOutput(); err != nil {
		t.Fatalf("the run must be able to push its own branch: %v\n%s", err, out)
	}
	t.Log("the grant pushed the one ref it names")

	// Everything else the same credential might try.
	for _, tc := range []struct{ name, refspec, want string }{
		{"the trunk", "HEAD:refs/heads/main", "may only write"},
		{"another run's branch", "HEAD:refs/heads/agent/somebody-else", "may only write"},
		{"a ref outside the machine namespace", "HEAD:refs/heads/release", "may only write"},
		{"deleting the trunk", ":refs/heads/main", "may only write"},
		{"appending to its own branch", "HEAD:refs/heads/" + branch, "agent branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, work, "more.txt", tc.name)
			gitRun(t, work, "add", "-A")
			gitRun(t, work, "commit", "-q", "-m", "more")
			out, err := gitTestCmd(work, append(auth, "push", "origin", tc.refspec)...).CombinedOutput()
			if err == nil {
				t.Fatalf("A GRANT WROTE %s\n%s", tc.name, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("refused, but not by the rule (%q):\n%s", tc.want, out)
			}
			t.Logf("REFUSED: %s", firstRejectLine(string(out)))
		})
	}
}

// firstRejectLine pulls git's own rejection line out of its output, for a log
// line that shows what the pusher actually read.
func firstRejectLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "remote rejected") || strings.Contains(l, "[rejected]") {
			return strings.TrimSpace(l)
		}
	}
	return strings.TrimSpace(out)
}

// An expired grant is not a grant. The TTL is the backstop for a run that dies
// without withdrawing its own credential.
func TestAGrantExpires(t *testing.T) {
	tok, handle, err := issued.issue(grant{org: "acme", repo: "code", ref: "refs/heads/agent/x"}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, ok := issued.lookup(tok); !ok {
		t.Fatal("a fresh grant must resolve")
	}
	// Age THIS grant, rather than sleeping for the TTL. Only this one: the table
	// is process-wide, so a test that expired everything would be reaching into
	// its neighbours.
	key := sha256.Sum256([]byte(tok))
	issued.mu.Lock()
	g := issued.m[key]
	g.expires = g.expires.Add(-2 * grantMaxTTL)
	issued.m[key] = g
	issued.mu.Unlock()
	if _, ok := issued.lookup(tok); ok {
		t.Fatal("AN EXPIRED GRANT STILL RESOLVES")
	}
	// And revoking is idempotent on an already-gone grant.
	issued.revoke("acme", handle)
	t.Log("an expired grant resolves to nothing")
}

// Revocation is what makes a grant's life the RUN's life, and it is org-scoped
// so a handle is not authority over someone else's capability.
func TestAGrantIsRevokedByItsOwnerOnly(t *testing.T) {
	tok, handle, err := issued.issue(grant{org: "acme", repo: "code", ref: "refs/heads/agent/x"}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	issued.revoke("beta", handle) // another tenant, same handle
	if _, ok := issued.lookup(tok); !ok {
		t.Fatal("ANOTHER ORG REVOKED THIS GRANT")
	}
	issued.revoke("acme", handle)
	if _, ok := issued.lookup(tok); ok {
		t.Fatal("the owner's revoke did not take")
	}
	t.Log("only the holding org revokes")
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

// doAuth is `do` with a BEARER credential and NO identity headers — the shape a
// request carries when the caller is not a principal.
func doAuth(t *testing.T, app *zip.App, method, path, token string, body any) (int, []byte) {
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
	req.Header.Set("Authorization", "Basic "+
		base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// TestAGrantAuthenticatesTheWayTheSandboxPresentsIt is the end-to-end proof for
// the shape apps/coding actually builds, and it exists because the shape it used
// to build could not work.
//
// A run's clone and push carried the grant as URL userinfo —
// https://x-access-token:hgg_…@host/… — on the reasoning that a credential in a
// URL leaves nothing behind. Two things were wrong with it, and the second is
// fatal rather than untidy:
//
//   - git writes the URL it was given into .git/config verbatim, so the grant sat
//     readable in the checkout that then executes untrusted model output.
//   - git does not SEND a URL-embedded credential until it is challenged. It makes
//     an anonymous request and waits for 401 WWW-Authenticate. resolvePackRepo
//     answers a caller it cannot place with 403, never 401 — so the grant was
//     never presented at all.
//
// The header form is what the rest of the forge already uses (mirror_out.go), and
// this pins that a URL-SCOPED one — which git attaches to this repository and to
// no other host the run might be steered at — authenticates the same way.
func TestAGrantAuthenticatesTheWayTheSandboxPresentsIt(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/push", "acme", map[string]any{
		"branch": "main", "message": "seed",
		"files": []map[string]any{{"path": "README.md", "content": "# seed\n"}},
	}); code != 200 {
		t.Fatalf("seed main: %d %s", code, b)
	}

	branch := "agent/abc123def456"
	tok, _, err := issued.issue(grant{org: "acme", repo: "code", ref: "refs/heads/" + branch}, 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	url := base + "/v1/git/acme/code.git"
	// EXACTLY what apps/coding builds (sandboxrunner.go gitAs): one -c, scoped to
	// this url, applied to this invocation.
	auth := []string{"-c", "http." + url + ".extraHeader=Authorization: Basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))}

	work := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(auth, "clone", "-q", "--depth", "1", "-b", "main", url, work)...)
	t.Log("the grant cloned its repo with a url-scoped header")

	// AND IT LEFT NOTHING BEHIND. The checkout the model edits holds no credential:
	// a top-level -c is not written into the new repository's config.
	cfg, err := os.ReadFile(filepath.Join(work, ".git", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, leak := range []string{tok, "extraHeader", "Authorization"} {
		if strings.Contains(string(cfg), leak) {
			t.Fatalf("the clone persisted %q into .git/config:\n%s", leak, cfg)
		}
	}
	t.Log("nothing was written to .git/config")

	gitRun(t, work, "switch", "-q", "-c", branch)
	write(t, work, "feature.txt", "agent work\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "agent change")
	if out, err := gitTestCmd(work, append(auth, "push", url, "HEAD:refs/heads/"+branch)...).CombinedOutput(); err != nil {
		t.Fatalf("the run must be able to push its own branch: %v\n%s", err, out)
	}
	t.Log("the grant pushed the one ref it names")

	// The trunk is still refused with the credential presented THIS way, so the
	// change of form did not change what the form is allowed to do.
	out, err := gitTestCmd(work, append(auth, "push", url, "HEAD:refs/heads/main")...).CombinedOutput()
	if err == nil {
		t.Fatalf("A GRANT WROTE THE TRUNK\n%s", out)
	}
	if !strings.Contains(string(out), "may only write") {
		t.Fatalf("refused, but not by the ref policy:\n%s", out)
	}
	t.Logf("REFUSED: %s", firstRejectLine(string(out)))
}

// ── writer 9: the merge door ─────────────────────────────────────────────────

// TestMergeObeysTheRefPolicy proves merging is not a door around checkRefPolicy:
// an agent branch may be created and never rewritten, and that holds whether the
// rewrite arrives as a push or as a merge.
func TestMergeObeysTheRefPolicy(t *testing.T) {
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
}

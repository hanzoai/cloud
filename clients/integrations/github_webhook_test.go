package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// github_webhook_test.go proves the inbound half: signature verification
// (fail-closed), the ping/non-push/non-branch/delete no-ops, org resolution SOLELY
// from the (signed) installation id, cross-org isolation, and that a spoofed
// X-Org-Id can never override the installation-derived org. It also proves the
// import handler queues a bounded background import intersected with the GRANTED set.

// recordingImporter is a cloud.GitImporter test double that records the calls the
// integrations plane makes across the seam.
type recordingImporter struct {
	mu       sync.Mutex
	inbound  []cloud.GitInboundReq
	imported []cloud.GitImportReq
	res      cloud.GitSyncResult
}

func (f *recordingImporter) ImportRepo(_ context.Context, req cloud.GitImportReq) error {
	f.mu.Lock()
	f.imported = append(f.imported, req)
	f.mu.Unlock()
	return nil
}
func (f *recordingImporter) InboundSync(_ context.Context, req cloud.GitInboundReq) (cloud.GitSyncResult, error) {
	f.mu.Lock()
	f.inbound = append(f.inbound, req)
	f.mu.Unlock()
	return f.res, nil
}
func (f *recordingImporter) RepoStatus(_ context.Context, _, _ string, _ []string) (map[string]cloud.GitRepoStatus, error) {
	return nil, nil
}
func (f *recordingImporter) inboundN() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inbound)
}
func (f *recordingImporter) lastInbound() cloud.GitInboundReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inbound[len(f.inbound)-1]
}
func (f *recordingImporter) importedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.imported))
	for _, r := range f.imported {
		out = append(out, r.Repo)
	}
	return out
}

func useImporter(t *testing.T) *recordingImporter {
	t.Helper()
	f := &recordingImporter{res: cloud.GitSyncResult{Applied: true}}
	cloud.RegisterGitImporter(f)
	t.Cleanup(func() { cloud.RegisterGitImporter(nil) })
	return f
}

// syncCapture records the events the webhook hands cloud.Sync: the webhook now
// enqueues every verified push to the universal engine (the one place a sync
// happens), so the test asserts the ENQUEUED event rather than a direct git call.
type syncCapture struct {
	mu     sync.Mutex
	events []cloud.SyncEvent
}

func (e *syncCapture) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}
func (e *syncCapture) last() cloud.SyncEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events[len(e.events)-1]
}

func useSyncEngine(t *testing.T) *syncCapture {
	t.Helper()
	e := &syncCapture{}
	cloud.RegisterSync(func(_ context.Context, ev cloud.SyncEvent) (cloud.SyncResult, error) {
		e.mu.Lock()
		e.events = append(e.events, ev)
		e.mu.Unlock()
		return cloud.SyncResult{Ran: 1}, nil
	})
	t.Cleanup(func() { cloud.RegisterSync(nil) })
	return e
}

// pushPayload builds a GitHub push event body.
func pushPayload(t *testing.T, inst int64, repo, ref string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"ref": ref, "before": "0000", "after": "1111",
		"repository": map[string]any{
			"name": repo, "full_name": "acme-gh/" + repo,
			"clone_url": "https://github.com/acme-gh/" + repo + ".git",
		},
		"installation": map[string]any{"id": inst},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// webhookPost posts a webhook. sig is sent verbatim as X-Hub-Signature-256 when
// non-empty; org, when non-empty, sets a (would-be-spoofed) X-Org-Id header.
func webhookPost(t *testing.T, app *zip.App, event, sig, org string, payload []byte) httpResult {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/connector/github/webhook", bytes.NewReader(payload))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-GitHub-Event", event)
	if sig != "" {
		rq.Header.Set("X-Hub-Signature-256", sig)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: b}
}

func ghSign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestVerifyGitHubSignature unit-proves the fail-closed HMAC gate.
func TestVerifyGitHubSignature(t *testing.T) {
	body := []byte(`{"a":1}`)
	good := ghSign("s3cr3t", body)
	if !verifyGitHubSignature("s3cr3t", good, body) {
		t.Fatal("valid signature must verify")
	}
	// Fail closed on every degenerate input.
	for name, tc := range map[string]struct{ secret, sig string }{
		"empty secret":  {"", good},
		"empty sig":     {"s3cr3t", ""},
		"wrong secret":  {"other", good},
		"no prefix":     {"s3cr3t", hex.EncodeToString([]byte("x"))},
		"tampered body": {"s3cr3t", ghSign("s3cr3t", []byte(`{"a":2}`))},
	} {
		if verifyGitHubSignature(tc.secret, tc.sig, body) {
			t.Fatalf("%s must fail closed", name)
		}
	}
}

// TestWebhookOrgIsolationAndFailClosed is the core inbound proof: a bad/absent
// signature never reaches the git plane, the org is derived ONLY from the signed
// installation id (a spoofed X-Org-Id is ignored), each installation maps to its
// OWN org, and ping/non-branch/delete/unknown-install are benign no-ops.
func TestWebhookOrgIsolationAndFailClosed(t *testing.T) {
	eng := useSyncEngine(t)
	srv := mockGitHub(t, nil)
	withGithubApp(t, srv)
	const secret = "wh_secret_xyz"
	t.Setenv(githubWebhookSecretEnv, secret)
	app := newApp(t, newKMS(t))

	ctx := context.Background()
	// Two orgs, two installations.
	_ = mounted.State.store.Upsert(ctx, Connection{Org: "acme", Provider: "github", ExternalID: "111"})
	_ = mounted.State.store.Upsert(ctx, Connection{Org: "beta", Provider: "github", ExternalID: "222"})

	// 1) Valid push for installation 111 → org acme, enqueued to the sync engine.
	p := pushPayload(t, 111, "widgets", "refs/heads/main")
	if r := webhookPost(t, app, "push", ghSign(secret, p), "", p); r.Code != http.StatusOK {
		t.Fatalf("valid push want 200, got %d (%s)", r.Code, r.Body)
	}
	if eng.count() != 1 {
		t.Fatalf("valid push must enqueue one sync event, got %d", eng.count())
	}
	in := eng.last()
	if in.Kind != "git" || in.Provider != "github" || in.Org != "acme" || in.Repo != "widgets" || in.Branch != "main" {
		t.Fatalf("wrong sync event: %+v", in)
	}
	if in.Locator != "https://github.com/acme-gh/widgets.git" {
		t.Fatalf("event must carry the source clone URL, got %q", in.Locator)
	}

	// 2) A SPOOFED X-Org-Id must NOT override the installation-derived org.
	p = pushPayload(t, 111, "widgets", "refs/heads/main")
	webhookPost(t, app, "push", ghSign(secret, p), "beta", p) // attacker claims beta
	if got := eng.last().Org; got != "acme" {
		t.Fatalf("spoofed X-Org-Id must be ignored; org resolved to %q (want acme)", got)
	}

	// 3) Installation 222 → org beta (each install isolated to its own org).
	p = pushPayload(t, 222, "secret-svc", "refs/heads/main")
	webhookPost(t, app, "push", ghSign(secret, p), "", p)
	if got := eng.last(); got.Org != "beta" || got.Repo != "secret-svc" {
		t.Fatalf("installation 222 must resolve to beta, got %+v", got)
	}

	// 4) BAD signature → 401, engine never touched.
	before := eng.count()
	p = pushPayload(t, 111, "widgets", "refs/heads/main")
	if r := webhookPost(t, app, "push", "sha256=deadbeef", "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature want 401, got %d", r.Code)
	}
	// 5) ABSENT signature → 401.
	if r := webhookPost(t, app, "push", "", "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("absent signature want 401, got %d", r.Code)
	}
	if eng.count() != before {
		t.Fatalf("an unsigned/bad-signed push must NEVER reach the sync engine")
	}

	// 6) Unknown installation → 200 ignored, not enqueued.
	before = eng.count()
	p = pushPayload(t, 999, "widgets", "refs/heads/main")
	if r := webhookPost(t, app, "push", ghSign(secret, p), "", p); r.Code != http.StatusOK {
		t.Fatalf("unknown install want 200, got %d", r.Code)
	}
	if eng.count() != before {
		t.Fatal("unknown installation must not enqueue a sync event")
	}

	// 7) ping → 200. 8) tag (non-branch) → 200 ignored. 9) branch delete → 200 ignored.
	if r := webhookPost(t, app, "ping", ghSign(secret, []byte(`{"zen":"x"}`)), "", []byte(`{"zen":"x"}`)); r.Code != http.StatusOK {
		t.Fatalf("ping want 200, got %d", r.Code)
	}
	before = eng.count()
	tag := pushPayload(t, 111, "widgets", "refs/tags/v1.0.0")
	webhookPost(t, app, "push", ghSign(secret, tag), "", tag)
	del, _ := json.Marshal(map[string]any{
		"ref": "refs/heads/gone", "deleted": true,
		"repository":   map[string]any{"name": "widgets"},
		"installation": map[string]any{"id": 111},
	})
	webhookPost(t, app, "push", ghSign(secret, del), "", del)
	if eng.count() != before {
		t.Fatal("tag push and branch delete must NOT enqueue a sync event")
	}
}

// TestImportHandlerGrantScopedBackground proves the import handler: it queues a
// background import of the requested repos INTERSECTED with the granted set (a
// repo the App was not granted is refused — an org can't import arbitrary repos).
func TestImportHandlerGrantScopedBackground(t *testing.T) {
	fake := useImporter(t)
	repos := []map[string]any{
		{"name": "alpha", "full_name": "acme-gh/alpha", "clone_url": "https://github.com/acme-gh/alpha.git"},
		{"name": "beta", "full_name": "acme-gh/beta", "clone_url": "https://github.com/acme-gh/beta.git"},
	}
	srv := mockGitHub(t, repos)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", ExternalID: "333"})

	// Import ONLY alpha.
	r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/import", "acme", map[string]any{"repos": []string{"alpha"}})
	if r.Code != http.StatusAccepted {
		t.Fatalf("import want 202, got %d (%s)", r.Code, r.Body)
	}
	// The background worker imports alpha (and only alpha).
	waitFor(t, 5*time.Second, func() bool {
		for _, n := range fake.importedNames() {
			if n == "alpha" {
				return true
			}
		}
		return false
	})
	for _, n := range fake.importedNames() {
		if n == "beta" {
			t.Fatal("beta was not requested — must not be imported")
		}
	}

	// A repo NOT granted to the installation is refused (400) — no arbitrary import.
	if r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/import", "acme", map[string]any{"repos": []string{"not-granted"}}); r.Code != http.StatusBadRequest {
		t.Fatalf("importing an ungranted repo want 400, got %d (%s)", r.Code, r.Body)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

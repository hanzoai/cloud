package git

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

const testWebhookSecret = "test-webhook-secret"

// pushCapture records every push event the registered builder receives.
type pushCapture struct {
	sync.Mutex
	events []cloud.GitPushEvent
}

// captureBuilder installs a push-builder that records every event, and cleans up.
func captureBuilder(t *testing.T) *pushCapture {
	t.Helper()
	got := &pushCapture{}
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) error {
		got.Lock()
		got.events = append(got.events, ev)
		got.Unlock()
		return nil
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })
	return got
}

func (p *pushCapture) seen() []cloud.GitPushEvent {
	p.Lock()
	defer p.Unlock()
	return append([]cloud.GitPushEvent(nil), p.events...)
}

// signHook returns the hex HMAC-SHA256 of body under secret — the exact encoding
// the forge sends in X-Git-Signature.
func signHook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// forgePayload is one real push delivery, shaped exactly as git.hanzo.ai sends it
// (see the live hook_task rows: repository.owner.login, pusher.login, clone_url).
func forgePayload(owner, name, ref, after, pusher string) []byte {
	b, _ := json.Marshal(map[string]any{
		"ref":   ref,
		"after": after,
		"repository": map[string]any{
			"name":      name,
			"full_name": owner + "/" + name,
			"clone_url": "https://git.hanzo.ai/" + owner + "/" + name + ".git",
			"owner":     map[string]any{"login": owner},
		},
		"pusher": map[string]any{"login": pusher},
	})
	return b
}

// postHook posts a raw body to /v1/git/webhook with the given event + signature
// headers. The body is sent verbatim (the signed message), never re-marshalled.
func postHook(t *testing.T, app *zip.App, event, sig string, body []byte) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/git/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set(eventHeader, event)
	}
	if sig != "" {
		req.Header.Set(sigHeader, sig)
	}
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test POST /v1/git/webhook: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestWebhookFiresBuild proves a signed forge push reaches the SAME
// cloud.OnGitPush seam the embedded server and the GitHub App fire, carrying the
// org, repo, FULL ref, tip commit, and the forge's own clone URL — the value
// clients/platform matches an Application's RepoURL against.
func TestWebhookFiresBuild(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	const after = "c3501b317c97458289cf102055c4bdf732031063"
	body := forgePayload("hanzoai", "cloud", "refs/heads/main", after, "z")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, body), body); code != http.StatusNoContent {
		t.Fatalf("signed push want 204, got %d", code)
	}

	evs := got.seen()
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 push event, got %d: %+v", len(evs), evs)
	}
	ev := evs[0]
	if ev.Org != "hanzoai" || ev.Repo != "cloud" || ev.Ref != "refs/heads/main" || ev.Commit != after {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.CloneURL != "https://git.hanzo.ai/hanzoai/cloud.git" {
		t.Fatalf("clone URL = %q, want the forge's own clone_url", ev.CloneURL)
	}
}

// TestWebhookTagPushFiresBuild proves a tag reaches the builder too. Releases are
// cut by tag, so a refs/heads-only filter would silently stop publishing.
func TestWebhookTagPushFiresBuild(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	body := forgePayload("luxfi", "market", "refs/tags/v1.1.3", "1111111111111111111111111111111111111111", "z")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, body), body); code != http.StatusNoContent {
		t.Fatalf("tag push want 204, got %d", code)
	}
	if evs := got.seen(); len(evs) != 1 || evs[0].Ref != "refs/tags/v1.1.3" {
		t.Fatalf("tag push must reach the builder with its full ref, got %+v", evs)
	}
}

// TestWebhookRejectsBadSignature proves the endpoint is not an open build trigger:
// a wrong, malformed, or absent signature is 401 and fires nothing.
func TestWebhookRejectsBadSignature(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	body := forgePayload("hanzoai", "cloud", "refs/heads/main", "1111111111111111111111111111111111111111", "z")
	for _, tc := range []struct{ name, sig string }{
		{"wrong secret", signHook("wrong-secret", body)},
		{"not hex", "not-hex"},
		{"absent", ""},
		{"truncated", signHook(testWebhookSecret, body)[:32]},
	} {
		if code := postHook(t, app, "push", tc.sig, body); code != http.StatusUnauthorized {
			t.Fatalf("%s signature want 401, got %d", tc.name, code)
		}
	}
	// A signature over a DIFFERENT body must not authenticate this one.
	other := forgePayload("hanzoai", "universe", "refs/heads/main", "2222222222222222222222222222222222222222", "z")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, other), body); code != http.StatusUnauthorized {
		t.Fatalf("signature over another body want 401, got %d", code)
	}
	if evs := got.seen(); len(evs) != 0 {
		t.Fatalf("rejected webhooks must fire no build, got %+v", evs)
	}
}

// TestWebhookMissingSecretFailsClosed proves that with no GIT_WEBHOOK_SECRET set
// the door is shut, not open: even a body signed under the empty secret is 401.
// A misconfigured deployment refuses builds rather than triggering arbitrary ones.
func TestWebhookMissingSecretFailsClosed(t *testing.T) {
	t.Setenv(webhookSecretEnv, "")
	got := captureBuilder(t)
	app := mountApp(t)

	body := forgePayload("hanzoai", "cloud", "refs/heads/main", "1111111111111111111111111111111111111111", "z")
	if code := postHook(t, app, "push", signHook("", body), body); code != http.StatusUnauthorized {
		t.Fatalf("unset-secret push want 401, got %d", code)
	}
	if evs := got.seen(); len(evs) != 0 {
		t.Fatalf("unset secret must fire no build, got %+v", evs)
	}
}

// TestWebhookNoOps proves every delivery we do not act on is an acknowledged 204
// (the forge must not retry-storm) that fires no build: a non-push event, a
// non-ref, a ref delete, and a bot-authored push.
func TestWebhookNoOps(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	const after = "1111111111111111111111111111111111111111"
	for _, tc := range []struct {
		name, event string
		body        []byte
	}{
		{"non-push event", "issues", forgePayload("hanzoai", "cloud", "refs/heads/main", after, "z")},
		{"absent event header", "", forgePayload("hanzoai", "cloud", "refs/heads/main", after, "z")},
		{"not a ref", "push", forgePayload("hanzoai", "cloud", "main", after, "z")},
		{"ref delete", "push", forgePayload("hanzoai", "cloud", "refs/heads/main", zeroSHA, "z")},
		{"forge actions bot", "push", forgePayload("hanzoai", "cloud", "refs/heads/main", after, "hanzo-actions")},
		{"github app bot", "push", forgePayload("hanzoai", "cloud", "refs/heads/main", after, "hanzo-sync[bot]")},
	} {
		if code := postHook(t, app, tc.event, signHook(testWebhookSecret, tc.body), tc.body); code != http.StatusNoContent {
			t.Fatalf("%s want 204, got %d", tc.name, code)
		}
	}
	if evs := got.seen(); len(evs) != 0 {
		t.Fatalf("no-op deliveries must fire no build, got %+v", evs)
	}
}

// TestWebhookMalformedBody proves an authenticated but unparseable body is a 400
// the forge can see, not a silent 204 that hides a payload-shape change.
func TestWebhookMalformedBody(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	junk := []byte("{not json")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, junk), junk); code != http.StatusBadRequest {
		t.Fatalf("malformed body want 400, got %d", code)
	}
	// A well-formed push with no repository identity is equally a 400.
	noRepo, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": "1111111111111111111111111111111111111111"})
	if code := postHook(t, app, "push", signHook(testWebhookSecret, noRepo), noRepo); code != http.StatusBadRequest {
		t.Fatalf("push with no repository want 400, got %d", code)
	}
	if evs := got.seen(); len(evs) != 0 {
		t.Fatalf("malformed deliveries must fire no build, got %+v", evs)
	}
}

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

// signHook returns Gitea's hex HMAC-SHA256 of body under secret.
func signHook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// postHook posts a raw body to /v1/git/webhook with the given event + signature
// headers, returning the status code. The body is sent verbatim (the signed
// message), never re-marshalled by the harness.
func postHook(t *testing.T, app *zip.App, event, sig string, body []byte) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/git/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set(giteaEventHeader, event)
	}
	if sig != "" {
		req.Header.Set(giteaSigHeader, sig)
	}
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test POST /v1/git/webhook: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
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

func pushPayload(owner, name, ref, before, after, pusher string) []byte {
	var p giteaPush
	p.Ref = ref
	p.Before = before
	p.After = after
	p.Repository.Name = name
	p.Repository.Owner.Username = owner
	p.Pusher.Login = pusher
	b, _ := json.Marshal(p)
	return b
}

// TestWebhookFiresBuild proves a signed push webhook drives the SAME
// cloud.OnGitPush core a native push does, with the parsed owner/repo/branch/tip.
func TestWebhookFiresBuild(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	after := "1111111111111111111111111111111111111111"
	body := pushPayload("acme", "code", "refs/heads/main",
		"0000000000000000000000000000000000000000", after, "hanzo-dev")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, body), body); code != http.StatusNoContent {
		t.Fatalf("signed push want 204, got %d", code)
	}

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 1 {
		t.Fatalf("want exactly 1 push event, got %d: %+v", len(got.events), got.events)
	}
	ev := got.events[0]
	if ev.Org != "acme" || ev.Repo != "code" || ev.Branch != "main" || ev.Commit != after {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.CloneURL != "https://api.hanzo.test/v1/git/acme/code.git" {
		t.Fatalf("unexpected clone URL: %q", ev.CloneURL)
	}
}

// TestWebhookRejectsBadSignature proves a wrong signature is 401 and fires no build.
func TestWebhookRejectsBadSignature(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	body := pushPayload("acme", "code", "refs/heads/main", zeroSHA,
		"1111111111111111111111111111111111111111", "hanzo-dev")
	if code := postHook(t, app, "push", signHook("wrong-secret", body), body); code != http.StatusUnauthorized {
		t.Fatalf("wrong-signature push want 401, got %d", code)
	}
	if code := postHook(t, app, "push", "not-hex", body); code != http.StatusUnauthorized {
		t.Fatalf("malformed-signature push want 401, got %d", code)
	}
	if code := postHook(t, app, "push", "", body); code != http.StatusUnauthorized {
		t.Fatalf("missing-signature push want 401, got %d", code)
	}

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 0 {
		t.Fatalf("rejected webhooks must fire no build, got %+v", got.events)
	}
}

// TestWebhookMissingSecretFailsClosed proves that with no GIT_WEBHOOK_SECRET set,
// even a body signed under the empty secret is refused — the seam is fail-closed.
func TestWebhookMissingSecretFailsClosed(t *testing.T) {
	t.Setenv(webhookSecretEnv, "")
	got := captureBuilder(t)
	app := mountApp(t)

	body := pushPayload("acme", "code", "refs/heads/main", zeroSHA,
		"1111111111111111111111111111111111111111", "hanzo-dev")
	if code := postHook(t, app, "push", signHook("", body), body); code != http.StatusUnauthorized {
		t.Fatalf("unset-secret push want 401, got %d", code)
	}

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 0 {
		t.Fatalf("unset secret must fire no build, got %+v", got.events)
	}
}

// TestWebhookLoopGuardSkipsSyncActor proves the loop guard: a push BY the
// configured sync service account (GIT_SYNC_ACTOR) is the echo of an inbound relay
// and is acknowledged 204 with NO build, while a push by any other actor still
// drives the build — so a relay can never ping-pong back to the upstream it came
// from.
func TestWebhookLoopGuardSkipsSyncActor(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	t.Setenv(syncActorEnv, "hanzo-sync")
	got := captureBuilder(t)
	app := mountApp(t)

	after := "1111111111111111111111111111111111111111"
	// A push BY the sync actor is our own relay's echo → skipped, no build.
	bot := pushPayload("acme", "code", "refs/heads/main", zeroSHA, after, "hanzo-sync")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, bot), bot); code != http.StatusNoContent {
		t.Fatalf("sync-actor push want 204, got %d", code)
	}
	// A push by anyone else still drives the build.
	human := pushPayload("acme", "code", "refs/heads/main", zeroSHA, after, "alice")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, human), human); code != http.StatusNoContent {
		t.Fatalf("human push want 204, got %d", code)
	}

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 1 {
		t.Fatalf("only the non-sync-actor push must build, got %d: %+v", len(got.events), got.events)
	}
	if ev := got.events[0]; ev.Org != "acme" || ev.Branch != "main" {
		t.Fatalf("unexpected build event: %+v", got.events[0])
	}
}

// TestWebhookEventFilter proves a non-push Gitea event is an acknowledged 204
// no-op that fires no build (even with a valid signature).
func TestWebhookEventFilter(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	body := pushPayload("acme", "code", "refs/heads/main", zeroSHA,
		"1111111111111111111111111111111111111111", "hanzo-dev")
	if code := postHook(t, app, "issues", signHook(testWebhookSecret, body), body); code != http.StatusNoContent {
		t.Fatalf("non-push event want 204, got %d", code)
	}
	if code := postHook(t, app, "", signHook(testWebhookSecret, body), body); code != http.StatusNoContent {
		t.Fatalf("absent event header want 204, got %d", code)
	}

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 0 {
		t.Fatalf("non-push events must fire no build, got %+v", got.events)
	}
}

// TestWebhookZeroShaNoOp proves a branch-delete push (after=000..0) and a
// non-branch ref are acknowledged 204 no-ops that fire no build.
func TestWebhookZeroShaNoOp(t *testing.T) {
	t.Setenv(webhookSecretEnv, testWebhookSecret)
	got := captureBuilder(t)
	app := mountApp(t)

	del := pushPayload("acme", "code", "refs/heads/main",
		"1111111111111111111111111111111111111111", zeroSHA, "hanzo-dev")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, del), del); code != http.StatusNoContent {
		t.Fatalf("branch-delete push want 204, got %d", code)
	}

	tag := pushPayload("acme", "code", "refs/tags/v1.0.0", zeroSHA,
		"1111111111111111111111111111111111111111", "hanzo-dev")
	if code := postHook(t, app, "push", signHook(testWebhookSecret, tag), tag); code != http.StatusNoContent {
		t.Fatalf("tag push want 204, got %d", code)
	}

	got.Lock()
	defer got.Unlock()
	if len(got.events) != 0 {
		t.Fatalf("no-op pushes must fire no build, got %+v", got.events)
	}
}

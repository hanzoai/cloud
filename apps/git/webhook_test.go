package git

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// webhook_test.go pins a TOMBSTONE, and the reason it is worth testing at all is
// that its predecessor passed its own tests while building nothing.
//
// The old suite asserted that a signed push answered 204 and reached
// cloud.OnGitPush. Both were true IN THE TEST, because the test registered a
// builder in-process. Production never does: the only registrant lives in
// apps/platform and cloud runs each app as its own OS process, so the client was
// nil and 204 meant "received", never "built". A green suite over a dead endpoint
// for as long as it existed.
//
// So these tests assert the two things that keep the endpoint honest: no input gets
// anything but 410, and the refusal names where the delivery belongs.

// postHook posts a raw body to /v1/git/webhook with arbitrary headers and returns
// the status and body. Headers are a parameter because the point of most of these
// cases is that they do not matter any more.
func postHook(t *testing.T, app *zip.App, headers map[string]string, body []byte) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/git/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test POST /v1/git/webhook: %v", err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(out)
}

// forgePayload is one real push delivery, shaped as git.hanzo.ai sends it. Kept
// so the refusal is proven against the exact body that used to be accepted, not
// against a stub the handler could never have mistaken for real.
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

// TestWebhookIsGoneForEveryDelivery: the endpoint has ONE answer. A valid push, an
// unsigned one, an empty body and a non-push event all get 410 — there is no
// input left that changes the outcome, which is what "retired" has to mean. If
// any case here starts answering 2xx, a dead trigger has been wired back in.
func TestWebhookIsGoneForEveryDelivery(t *testing.T) {
	app := mountApp(t)
	const after = "c3501b317c97458289cf102055c4bdf732031063"
	push := forgePayload("hanzoai", "cloud", "refs/heads/main", after, "z")

	cases := []struct {
		name    string
		headers map[string]string
		body    []byte
	}{
		{"a real push delivery", map[string]string{"X-Git-Event": "push"}, push},
		{"the forge's Gitea-compatible header", map[string]string{"X-Gitea-Event": "push"}, push},
		{"a tag push", map[string]string{"X-Git-Event": "push"},
			forgePayload("hanzoai", "cloud", "refs/tags/v1.2.3", after, "z")},
		{"no event header at all", nil, push},
		{"a non-push event", map[string]string{"X-Git-Event": "issues"}, push},
		{"an empty body", map[string]string{"X-Git-Event": "push"}, nil},
		{"a malformed body", map[string]string{"X-Git-Event": "push"}, []byte("{not json")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postHook(t, app, tc.headers, tc.body)
			if code != http.StatusGone {
				t.Fatalf("want 410 Gone, got %d (%s)", code, body)
			}
		})
	}
}

// TestWebhookRefusalNamesTheEndpointThatBuilds is the reason this route is kept at
// all rather than deleted. A deleted route 404s, and a 404 from this estate is
// the signal that has twice been read as "the API is switched off" — Hanzo Git
// serves /v1, so /api/v1 404s and looks identical to a disabled server. The
// refusal therefore has to carry its own fix in the body an operator reads.
func TestWebhookRefusalNamesTheEndpointThatBuilds(t *testing.T) {
	app := mountApp(t)
	body := forgePayload("hanzoai", "cloud", "refs/heads/main", "c3501b3", "z")
	code, out := postHook(t, app, map[string]string{"X-Git-Event": "push"}, body)
	if code != http.StatusGone {
		t.Fatalf("want 410, got %d", code)
	}
	if !strings.Contains(out, buildEndpoint) {
		t.Fatalf("the refusal must name %s so the answer carries its own fix; got: %s", buildEndpoint, out)
	}
}

// TestWebhookDispatchesNothing: the endpoint does not reach the push-to-deploy
// client, signed or not. This is the assertion the old suite inverted — it PROVED
// the call happened, in a process where it never could — so it is stated here in
// the direction that actually protects the estate.
func TestWebhookDispatchesNothing(t *testing.T) {
	var mu sync.Mutex
	var fired []cloud.GitPushEvent
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) (int, error) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
		return 0, nil
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	app := mountApp(t)
	body := forgePayload("hanzoai", "cloud", "refs/heads/main", "c3501b3", "z")
	if code, out := postHook(t, app, map[string]string{"X-Git-Event": "push"}, body); code != http.StatusGone {
		t.Fatalf("want 410, got %d (%s)", code, out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("the retired endpoint dispatched %d push event(s) — it must trigger nothing: %+v", len(fired), fired)
	}
}

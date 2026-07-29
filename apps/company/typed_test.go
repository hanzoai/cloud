package company

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// Typing moved two facts out of the handlers and into the mount, where nothing
// in the app's own flow tests would notice them going wrong. These pin them.
//
//   - The 1 MiB JSON body cap used to be a line in every handler's decode(). It
//     is now the ONE group middleware limitBody, which is registered AFTER the
//     deck upload precisely so the deck — document BYTES, bounded by the edge —
//     keeps the ceiling it always had. Registration order is the whole mechanism,
//     and registration order is exactly what a refactor silently reverses.
//
//   - begin answers 201 on a first registration and 200 on the idempotent
//     repeat. zip.WithStatus declares ONE status and cannot express that, so the
//     201 rides cloud.Created through the Bridge — which means it is a fact about
//     the middleware being installed, not about the handler.

// raw issues a request with an arbitrary body and content type — what the deck
// upload takes, and what do() (which marshals JSON) cannot express.
func raw(t *testing.T, app *zip.App, method, path, org, contentType string, body []byte) int {
	t.Helper()
	rq := httptest.NewRequest(method, path, bytes.NewReader(body))
	rq.Header.Set("Content-Type", contentType)
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u_"+org)
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestBodyCapCoversJSONNotTheDeck proves the cap sits exactly where it always
// sat: over the JSON actions, and never over the deck upload.
func TestBodyCapCoversJSONNotTheDeck(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "cap"

	// Fast-forward to company via the skip path, so the deck route is reachable.
	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"alreadyIncorporated": true})
	do(t, app, http.MethodPost, "/v1/company/skip", org, nil)
	do(t, app, http.MethodPost, "/v1/company/import/documents", org, map[string]any{"folderId": "F"})
	do(t, app, http.MethodPost, "/v1/company/import/captable", org, map[string]any{"spreadsheetId": "S"})
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "company"})

	over := bytes.Repeat([]byte("x"), maxBody+1)

	// A JSON action over the cap: 413, the same answer decode() gave.
	if code := raw(t, app, http.MethodPost, "/v1/company/founders", org, "application/json", over); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON body want 413, got %d — limitBody must precede every JSON leaf", code)
	}
	// The surface ROOT too. It is declared on the App with its whole path (a group
	// leaf of "" would register "/v1/company/"), so this is the assertion that the
	// group's middleware — limitBody here, and Bridge with it — still reaches it.
	if code := raw(t, app, http.MethodPost, "/v1/company", org, "application/json", over); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body at the surface root want 413, got %d — the group middleware does not reach /v1/company", code)
	}

	// The deck, over the same cap: ingested, because a deck is bytes and its only
	// ceiling has always been the edge's BodyLimit.
	if code := raw(t, app, http.MethodPost, "/v1/company/fundraise/deck", org, "application/pdf", over); code != http.StatusCreated {
		t.Fatalf("oversized deck want 201, got %d — the deck must be registered BEFORE limitBody", code)
	}
}

// TestBeginIsConditionallyCreated proves the 201/200 split survives typing: the
// status rides cloud.Created through the Bridge the group installs, so this fails
// if that middleware is dropped or registered after the leaves.
func TestBeginIsConditionallyCreated(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "twice"

	if code, _ := do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "c-corp", "jurisdiction": "DE", "name": "Twice Inc.",
	}); code != http.StatusCreated {
		t.Fatalf("first begin want 201, got %d", code)
	}
	if code, m := do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "llc", "jurisdiction": "WY", "name": "Ignored",
	}); code != http.StatusOK {
		t.Fatalf("repeat begin want 200 (idempotent), got %d", code)
	} else if f, _ := m["formation"].(map[string]any); f["name"] != "Twice Inc." {
		t.Fatalf("repeat begin must return the EXISTING formation, got name %v", f["name"])
	}
}

package websearch

// What a page the caller never visited can make them pay for.
//
// /v1/websearch/search is named in cloud's paidReads: one debit per answer a bought
// engine served. A read that costs money is not covered by the rule that a read
// needs no anti-forgery token — a cross-site page can send the browser here, the
// cookie it already holds authenticates it, and the debit lands on the caller.
// Nothing leaks (the answer is unreadable cross-origin); what moves is money.
//
// PAIRED: the refusal sits beside the two callers that must get through, the
// service key the chat server presents and a caller's own bearer, or it would pass
// just as well against an endpoint that refuses everyone.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

func visit(t *testing.T, app *zip.App, extra map[string]string) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, "/v1/websearch/search?q=example", nil)
	rq.Header.Set("Cookie", "hanzo_iam_token=whatever")
	rq.Header.Set("X-Org-Id", "acme")
	rq.Header.Set("X-User-Id", "u-acme")
	for k, v := range extra {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("GET /v1/websearch/search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestACookieAloneCannotSpendOnSearch(t *testing.T) {
	mockBing(t, bingFixture)
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	if st, body := visit(t, app, nil); st != http.StatusForbidden {
		t.Fatalf("a search on a cookie alone = %d %s, want 403 — that read spends the caller's balance", st, body)
	}

	// The chat server presents the shared key and carries no browser cookie of its
	// own; a caller's bearer is a credential a cross-site page cannot set. Both are
	// untouched, so the control costs a service and an API client nothing.
	if st, body := visit(t, app, map[string]string{"Authorization": "Bearer t"}); st == http.StatusForbidden {
		t.Errorf("a search with a bearer = 403 %s — the control must cost an API client nothing", body)
	}
	rq := httptest.NewRequest(http.MethodGet, "/v1/websearch/search?q=example", nil)
	rq.Header.Set("X-API-Key", "k")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("service search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the service key search = %d, want 200 — a caller that sends no cookie cannot be forged into", resp.StatusCode)
	}
}

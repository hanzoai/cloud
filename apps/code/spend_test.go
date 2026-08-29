package code

// What a page the caller never visited can make them pay for.
//
// Two operations here are named in cloud's paidReads: /search embeds the query and
// /ask synthesizes the answer, both against the caller's balance. A read that costs
// money is not covered by the rule that a read needs no anti-forgery token — a
// cross-site page can send the browser to one of these addresses, the cookie it
// already holds authenticates it, and the debit lands on the caller. Nothing leaks
// (the answer is unreadable cross-origin); what moves is money.
//
// Every refusal below is PAIRED with a call that must get through, so none of them
// could pass against a surface that refuses everything.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/zap-proto/zip"
)

// visit drives a GET the way a cross-site page can: the cookie the browser already
// holds, plus whatever the caller was actually able to present.
func visit(t *testing.T, app *zip.App, path string, extra map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Cookie", "hanzo_iam_token=whatever")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	code, body := runReq(t, app, req)
	return code, string(body)
}

func TestACookieAloneCannotSpendOnCode(t *testing.T) {
	app, _ := newTestApp(t)
	if err := account.Use(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	for _, path := range []string{"/v1/code/search?q=open", "/v1/code/ask?q=open"} {
		if st, body := visit(t, app, path, nil); st != http.StatusForbidden {
			t.Errorf("%s on a cookie alone = %d %s, want 403 — that read spends the caller's balance", path, st, body)
		}
	}

	// A free read on the SAME surface is untouched. The control asks the money
	// rule, not the prefix, so a read that costs nothing keeps costing nothing.
	if st, body := visit(t, app, "/v1/code/tree?repo=cloud", nil); st == http.StatusForbidden {
		t.Errorf("/v1/code/tree on a cookie alone = 403 %s — a read that spends nothing is not gated", body)
	}

	// A caller that PRESENTED a credential cannot be forged into and pays no price.
	if st, body := visit(t, app, "/v1/code/search?q=open", map[string]string{"Authorization": "Bearer t"}); st == http.StatusForbidden {
		t.Errorf("/v1/code/search with a bearer = 403 %s — the control must cost an API client nothing", body)
	}

	// And the token the refusal names works: obtained from a same-origin response a
	// cross-site page cannot read, echoed in a header a simple request cannot set.
	st, body := visit(t, app, "/v1/account/csrf", nil)
	if st != http.StatusOK {
		t.Fatalf("mint token = %d %s", st, body)
	}
	tok := between(body, `"csrfToken":"`, `"`)
	if tok == "" {
		t.Fatalf("no token in %s", body)
	}
	if st, body = visit(t, app, "/v1/code/search?q=open", map[string]string{"X-CSRF-Token": tok}); st == http.StatusForbidden {
		t.Errorf("/v1/code/search with the minted token = 403 %s", body)
	}
}

func between(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

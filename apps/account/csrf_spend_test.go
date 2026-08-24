package account

// RequireCSRFOnSpend is the composition point between two rules that must not
// drift: WHICH requests spend (cloud's money rule) and WHAT a spending request
// reached by an ambient cookie has to show. It restates neither, so what is worth
// pinning is that it asks both and applies them in the right order.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// reachedIt records whether the request got past the control.
func reachedIt(t *testing.T, path string, hdr map[string]string) (int, bool) {
	t.Helper()
	var ran bool
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(RequireCSRFOnSpend())
	app.Get(path, func(c *zip.Ctx) error {
		ran = true
		return c.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, ran
}

// TestOnlyASpendingReadIsAsked. Four rows, one axis each, so a failure names its
// own cause: a paid read on a cookie alone is refused; the same read presenting a
// credential is not; a free read is never asked; and a request with no cookie at
// all — the gateway and service shapes — is not asked either.
func TestOnlyASpendingReadIsAsked(t *testing.T) {
	const paid, free = "/v1/code/ask", "/v1/code/tree"
	cookie := map[string]string{"Cookie": "hanzo_iam_token=x"}

	if st, ran := reachedIt(t, paid, cookie); st != http.StatusForbidden || ran {
		t.Errorf("a paid read on a cookie alone = %d ran=%v, want 403 and not run", st, ran)
	}
	if st, ran := reachedIt(t, paid, map[string]string{"Cookie": "hanzo_iam_token=x", "Authorization": "Bearer t"}); !ran {
		t.Errorf("a paid read with a bearer = %d ran=%v, want it served — a credential cannot be forged", st, ran)
	}
	if st, ran := reachedIt(t, free, cookie); !ran {
		t.Errorf("a free read on a cookie = %d ran=%v, want it served — the control asks the money rule, not the prefix", st, ran)
	}
	if st, ran := reachedIt(t, paid, nil); !ran {
		t.Errorf("a paid read with no cookie = %d ran=%v, want it served — there is no ambient credential to abuse", st, ran)
	}
}

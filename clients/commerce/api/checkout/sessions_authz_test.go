package checkout

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// mountCheckout registers the real checkout route stack (including the
// publishedRequired auth middleware on /sessions) on a fresh gin engine, exactly
// as the binary does at /v1.
func mountCheckout(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	Route(eng.Group("/v1"))
	return eng
}

// TestSessions_AnonymousMintBlocked is the core P0 regression: POST
// /v1/checkout/sessions with NO valid principal must be rejected (401/403)
// BEFORE any org resolution or Square call — no anonymous path can mint a
// payment link. We assert this for a naked request AND for every client-supplied
// org-selector the old handler honored (body org, ?org, X-Org-Id, X-Hanzo-Org):
// every one must fail closed with no checkout link in the response.
func TestSessions_AnonymousMintBlocked(t *testing.T) {
	eng := mountCheckout(t)

	const body = `{"org":"hanzo","tenant":"hanzo","currency":"USD",` +
		`"successUrl":"https://hanzo.ai/ok",` +
		`"items":[{"name":"x","amount":100,"quantity":1}]}`

	cases := []struct {
		name    string
		url     string
		headers map[string]string
	}{
		{"no auth, body org", "/v1/checkout/sessions", nil},
		{"query ?org override", "/v1/checkout/sessions?org=hanzo", nil},
		{"spoofed X-Org-Id", "/v1/checkout/sessions", map[string]string{"X-Org-Id": "hanzo"}},
		{"spoofed X-Hanzo-Org", "/v1/checkout/sessions", map[string]string{"X-Hanzo-Org": "hanzo"}},
		{"spoofed X-IAM-Org", "/v1/checkout/sessions", map[string]string{"X-IAM-Org": "hanzo"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			eng.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("anon %s: status = %d, want 401/403 (anonymous mint must be closed)", tc.name, w.Code)
			}
			// Fail closed: no minted link, ever.
			if b := w.Body.String(); strings.Contains(b, "checkoutUrl") ||
				strings.Contains(b, "square.link") || strings.Contains(b, "squareup") {
				t.Fatalf("anon %s leaked a checkout link (status %d): %s", tc.name, w.Code, b)
			}
		})
	}
}

// TestCheckoutSessionRequest_NoClientOrgField pins the "one org source" contract:
// the request body must NOT carry an org/tenant selector — the org is derived
// solely from the authenticated principal. A future re-add of the field would
// re-open the cross-tenant mint hole, so guard it at compile-adjacent time.
func TestCheckoutSessionRequest_NoClientOrgField(t *testing.T) {
	tp := reflect.TypeOf(checkoutSessionRequest{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(strings.ToLower(tp.Field(i).Tag.Get("json")), ",")[0]
		if name == "org" || name == "tenant" {
			t.Fatalf("checkoutSessionRequest.%s exposes a client-supplied %q field; "+
				"org MUST come only from the authenticated principal", tp.Field(i).Name, name)
		}
	}
}

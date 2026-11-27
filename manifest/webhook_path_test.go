package manifest

import (
	"net/http"
	"testing"
)

func TestProviderWebhookPath_RewritesPayPalInboundOnly(t *testing.T) {
	cases := []struct {
		method string
		in     string
		want   string
	}{
		{http.MethodPost, "/v1/webhook/paypal", "/v1/commerce/webhooks/paypal"},
		{http.MethodPost, "/v1/webhook/paypal/", "/v1/commerce/webhooks/paypal"},
		// Plural alias after Normalize -> ProviderWebhookPath:
		{http.MethodPost, Normalize("/v1/webhooks/paypal"), "/v1/commerce/webhooks/paypal"},
		// Outbound webhook CRUD routes stay untouched:
		{http.MethodGet, "/v1/webhook", "/v1/webhook"},
		{http.MethodPost, "/v1/webhook", "/v1/webhook"},
		{http.MethodGet, "/v1/webhook/paypal", "/v1/webhook/paypal"},
		{http.MethodPatch, "/v1/webhook/paypal", "/v1/webhook/paypal"},
		{http.MethodDelete, "/v1/webhook/paypal", "/v1/webhook/paypal"},
		{http.MethodPost, "/v1/webhook/wh_123/test", "/v1/webhook/wh_123/test"},
		{http.MethodPost, "/v1/webhook/wh_123/rotate", "/v1/webhook/wh_123/rotate"},
		{http.MethodPost, "/v1/commerce/webhooks/paypal", "/v1/commerce/webhooks/paypal"},
	}
	for _, tc := range cases {
		if got := ProviderWebhookPath(tc.method, tc.in); got != tc.want {
			t.Errorf("ProviderWebhookPath(%s, %q) = %q, want %q", tc.method, tc.in, got, tc.want)
		}
	}
}

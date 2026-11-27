package manifest

import (
	"net/http"

	"github.com/zap-proto/zip"
)

// ProviderWebhookPath rewrites a third-party payment provider's registered
// inbound webhook address (POST /v1/webhook/paypal) onto commerce's canonical
// webhook receiver (/v1/commerce/webhooks/paypal).
//
// PayPal registers https://api.hanzo.ai/v1/webhook/paypal for All Events. On
// the host, /v1/webhook belongs to the outbound webhook CRUD app (which serves
// POST /v1/webhook, POST /v1/webhook/:id/test and POST /v1/webhook/:id/rotate —
// never POST /v1/webhook/:id), whereas inbound payment verification and ledger
// settlement live in commerce at POST /v1/commerce/webhooks/:provider.
// Rewriting at the edge ahead of the mounts routes PayPal's sessionless
// deliveries to commerce without publishing a duplicate OpenAPI address or
// shadowing outbound webhook CRUD.
func ProviderWebhookPath(method, path string) string {
	if method == http.MethodPost && (path == "/v1/webhook/paypal" || path == "/v1/webhook/paypal/") {
		return "/v1/commerce/webhooks/paypal"
	}
	return path
}

// ProviderWebhookMiddleware rewrites PayPal's registered POST /v1/webhook/paypal
// onto commerce's sessionless /v1/commerce/webhooks/paypal before identity and
// spend gates run.
func ProviderWebhookMiddleware() zip.Handler {
	return func(c *zip.Ctx) error {
		if p := c.Path(); p != "" {
			if dst := ProviderWebhookPath(c.Method(), p); dst != p {
				c.Fiber().Path(dst)
			}
		}
		return c.Next()
	}
}

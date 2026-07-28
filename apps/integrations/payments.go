package integrations

// payments.go registers the payment/commerce connectors an online store needs:
// customer-held API keys, verified live against a cheap authenticated endpoint
// via keyVerify. All user-scoped (the key belongs to the customer, not a Hanzo
// OAuth app) — parity with the anthropic/openai key connectors.

import (
	"fmt"
	"strings"
)

func init() {
	register(&Provider{
		ID: "stripe", Name: "Stripe",
		Description: "Payments, billing, and payouts.",
		Category:    "Payments", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "stripe",
			origin:   constOrigin("STRIPE_API_BASE", "https://api.stripe.com"),
			path:     "/v1/balance", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "paypal", Name: "PayPal",
		Description: "Checkout and payouts. Connect with client-id:secret.",
		Category:    "Payments", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "paypal",
			origin:   constOrigin("PAYPAL_API_BASE", "https://api-m.paypal.com"),
			path:     "/v1/oauth2/token",
			method:   "POST", body: "grant_type=client_credentials",
			bodyType: "application/x-www-form-urlencoded",
			place:    basicRaw, minLen: 3,
		}),
	})

	register(&Provider{
		ID: "square", Name: "Square",
		Description: "In-person and online payments.",
		Category:    "Payments", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "square",
			origin:   constOrigin("SQUARE_API_BASE", "https://connect.squareup.com"),
			path:     "/v2/merchants", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "shopify", Name: "Shopify",
		Description: "Storefront and orders. Connect with your store domain + Admin API token.",
		Category:    "Commerce", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "shopify",
			origin:   shopifyOrigin,
			path:     "/admin/api/2024-10/shop.json",
			place:    headerAt, name: "X-Shopify-Access-Token",
			minLen: 16, echoAccount: true,
		}),
	})
}

// shopifyOrigin builds the store's Admin API origin from the caller's store
// domain (VerifyInput.AccountID). Env-overridable for the httptest seam.
func shopifyOrigin(in VerifyInput) (string, error) {
	if v := envBase("SHOPIFY_API_BASE"); v != "" {
		return v, nil
	}
	host, err := shopHost(in.AccountID)
	if err != nil {
		return "", err
	}
	return "https://" + host, nil
}

// shopHost normalizes a Shopify store identifier to its <name>.myshopify.com
// host. It accepts the bare handle ("acme"), the full myshopify domain, or a URL,
// and rejects anything that is not a plain host label (no path, no injection).
func shopHost(account string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(account))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "", fmt.Errorf("shopify needs the store's myshopify.com domain")
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i] // drop any path
	}
	if !strings.Contains(s, ".") {
		s += ".myshopify.com"
	}
	if !strings.HasSuffix(s, ".myshopify.com") || !validHostLabel(s) {
		return "", fmt.Errorf("shopify domain must be <store>.myshopify.com")
	}
	return s, nil
}

// validHostLabel accepts only DNS-safe host characters, so a normalized host can
// never smuggle a credential, path, or query into the verify URL.
func validHostLabel(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

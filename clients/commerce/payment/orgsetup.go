// Package payment provides per-org payment processor configuration.
//
// The payment/processor registry holds global singleton processors registered at
// init() time. These singletons are NOT safe for multi-tenant use because
// credentials would be shared across orgs.
//
// ProcessorsForOrg creates a FRESH registry with per-org processor instances,
// each configured with credentials from the KMS-hydrated Organization model.
// Call kms.Hydrate(cc, org) before calling ProcessorsForOrg.
package payment

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/adyen"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/braintree"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/lemonsqueezy"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/paypal"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/recurly"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/stripe"
	square "github.com/hanzoai/cloud/clients/commerce/thirdparty/square"
)

// defaultSquareWebhookURL is the canonical commerce API ingress URL that
// Square callbacks are delivered to. Square signs each delivery over this URL
// plus the raw body, so it must match the URL registered in the Square
// dashboard byte-for-byte. pay.hanzo.ai is the customer-facing payment host
// (the commerce API ingress); commerce.hanzo.ai serves the marketing SPA, not
// the API. Overridable per-org (org.Square.WebhookURL) or per-deployment
// (SQUARE_WEBHOOK_URL) for non-production hosts.
const defaultSquareWebhookURL = "https://pay.hanzo.ai/v1/billing/webhooks/square"

// ProcessorsForOrg returns a processor registry configured with the given
// organization's payment credentials. Each processor is a fresh instance —
// no shared state with other orgs or the global singleton registry.
//
// Processors without credentials in the org are still registered but marked
// as unavailable (IsAvailable returns false).
func ProcessorsForOrg(org *organization.Organization) *processor.Registry {
	reg := processor.NewRegistry(processor.DefaultConfig())

	// Stripe
	sk := org.StripeToken()
	if sk != "" {
		reg.Register(stripe.NewProvider(stripe.Config{
			SecretKey:      sk,
			PublishableKey: org.Stripe.Live.PublishableKey,
			WebhookSecret:  "", // webhook secret is global, not per-org
		}))
	}

	// Square. The deployment's SQUARE_ENVIRONMENT is the single authority for
	// sandbox-vs-production (see org.TestMode): it selects BOTH which
	// KMS-hydrated credential set to use AND the Environment passed to the
	// provider (which drives the Square API base URL). Falls back to process env
	// vars when KMS is disabled or the org has no stored Square credentials.
	{
		useSandbox := org.TestMode()
		env := org.SquareEnvironment()

		sqCfg := org.SquareConfig(useSandbox)
		token := sqCfg.AccessToken
		locationID := sqCfg.LocationId
		webhookKey := org.Square.WebhookSignatureKey
		// Notification URL that Square signs deliveries against. Prefer the
		// org's configured value, then the deployment env, then the canonical
		// commerce ingress URL. Must match the URL registered in the Square
		// dashboard byte-for-byte or HMAC verification fails.
		webhookURL := org.Square.WebhookURL
		if webhookURL == "" {
			webhookURL = strings.TrimSpace(os.Getenv("SQUARE_WEBHOOK_URL"))
		}
		if webhookURL == "" {
			webhookURL = defaultSquareWebhookURL
		}

		// Env-var credential fallback (KMS disabled or no stored Square creds).
		// The SAME useSandbox authority picks the sandbox vs production env vars,
		// so the credential set always matches `env`.
		if token == "" {
			token = strings.TrimSpace(os.Getenv("SQUARE_ACCESS_TOKEN"))
			locationID = strings.TrimSpace(os.Getenv("SQUARE_LOCATION_ID"))
			if useSandbox {
				if t := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_ACCESS_TOKEN")); t != "" {
					token = t
				}
				if l := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_LOCATION_ID")); l != "" {
					locationID = l
				}
			}
			if webhookKey == "" {
				webhookKey = strings.TrimSpace(os.Getenv("SQUARE_WEBHOOK_SIGNATURE_KEY"))
			}
		}

		if token != "" && locationID != "" {
			reg.Register(square.NewProcessor(square.Config{
				AccessToken:   token,
				LocationID:    locationID,
				WebhookSecret: webhookKey,
				WebhookURL:    webhookURL,
				Environment:   env,
			}))
		}
	}

	// Adyen
	if org.Adyen.APIKey != "" {
		reg.Register(adyen.NewProvider(adyen.Config{
			APIKey:          org.Adyen.APIKey,
			MerchantAccount: org.Adyen.MerchantAccount,
			HMACKey:         org.Adyen.HMACKey,
			Environment:     adyen.Environment(org.Adyen.Environment),
			LiveURLPrefix:   org.Adyen.LiveURLPrefix,
		}))
	}

	// Braintree
	if org.Braintree.PublicKey != "" {
		reg.Register(braintree.NewProvider(braintree.Config{
			MerchantID:  org.Braintree.MerchantID,
			PublicKey:   org.Braintree.PublicKey,
			PrivateKey:  org.Braintree.PrivateKey,
			Environment: org.Braintree.Environment,
		}))
	}

	// PayPal — use v2 REST API client credentials
	// The legacy org.Paypal fields use Adaptive Payments (deprecated).
	// Map securityUserId → clientID, securityPassword → clientSecret.
	ppCreds := org.Paypal.Live
	ppSandbox := false
	if !org.Live {
		ppCreds = org.Paypal.Test
		ppSandbox = true
	}
	if ppCreds.SecurityUserId != "" {
		reg.Register(paypal.NewProvider(paypal.Config{
			ClientID:     ppCreds.SecurityUserId,
			ClientSecret: ppCreds.SecurityPassword,
			Sandbox:      ppSandbox,
		}))
	}

	// Recurly
	if org.Recurly.APIKey != "" {
		reg.Register(recurly.NewProvider(recurly.Config{
			APIKey:    org.Recurly.APIKey,
			Subdomain: org.Recurly.Subdomain,
		}))
	}

	// LemonSqueezy
	if org.LemonSqueezy.APIKey != "" {
		reg.Register(lemonsqueezy.NewProvider(lemonsqueezy.Config{
			APIKey:           org.LemonSqueezy.APIKey,
			StoreID:          org.LemonSqueezy.StoreID,
			WebhookSecret:    org.LemonSqueezy.WebhookSecret,
			DefaultVariantID: org.LemonSqueezy.DefaultVariantID,
		}))
	}

	return reg
}

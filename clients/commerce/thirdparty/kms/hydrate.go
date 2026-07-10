package kms

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

// secretMapping maps a KMS secret name to a function that sets the value on the org.
type secretMapping struct {
	path string
	name string
	set  func(org *organization.Organization, val string)
}

// mappings returns all provider credential mappings for a given org name.
func mappings(orgName string) []secretMapping {
	stripe := "/tenants/" + orgName + "/stripe"
	square := "/tenants/" + orgName + "/square"
	authnet := "/tenants/" + orgName + "/authorizenet"
	paypal := "/tenants/" + orgName + "/paypal"
	adyen := "/tenants/" + orgName + "/adyen"
	braintree := "/tenants/" + orgName + "/braintree"
	recurly := "/tenants/" + orgName + "/recurly"
	lemon := "/tenants/" + orgName + "/lemonsqueezy"
	wire := "/tenants/" + orgName + "/wire"
	merc := "/tenants/" + orgName + "/mercury"

	return []secretMapping{
		// Stripe — Live
		{stripe, "STRIPE_LIVE_ACCESS_TOKEN", func(o *organization.Organization, v string) { o.Stripe.Live.AccessToken = v }},
		{stripe, "STRIPE_LIVE_PUBLISHABLE_KEY", func(o *organization.Organization, v string) { o.Stripe.Live.PublishableKey = v }},
		{stripe, "STRIPE_LIVE_REFRESH_TOKEN", func(o *organization.Organization, v string) { o.Stripe.Live.RefreshToken = v }},
		{stripe, "STRIPE_LIVE_USER_ID", func(o *organization.Organization, v string) { o.Stripe.Live.UserId = v }},
		// Stripe — Test
		{stripe, "STRIPE_TEST_ACCESS_TOKEN", func(o *organization.Organization, v string) { o.Stripe.Test.AccessToken = v }},
		{stripe, "STRIPE_TEST_PUBLISHABLE_KEY", func(o *organization.Organization, v string) { o.Stripe.Test.PublishableKey = v }},
		{stripe, "STRIPE_TEST_REFRESH_TOKEN", func(o *organization.Organization, v string) { o.Stripe.Test.RefreshToken = v }},
		{stripe, "STRIPE_TEST_USER_ID", func(o *organization.Organization, v string) { o.Stripe.Test.UserId = v }},

		// Square — Production
		{square, "SQUARE_PRODUCTION_ACCESS_TOKEN", func(o *organization.Organization, v string) { o.Square.Production.AccessToken = v }},
		{square, "SQUARE_PRODUCTION_LOCATION_ID", func(o *organization.Organization, v string) { o.Square.Production.LocationId = v }},
		{square, "SQUARE_PRODUCTION_APPLICATION_ID", func(o *organization.Organization, v string) { o.Square.Production.ApplicationId = v }},
		// Square — Sandbox
		{square, "SQUARE_SANDBOX_ACCESS_TOKEN", func(o *organization.Organization, v string) { o.Square.Sandbox.AccessToken = v }},
		{square, "SQUARE_SANDBOX_LOCATION_ID", func(o *organization.Organization, v string) { o.Square.Sandbox.LocationId = v }},
		{square, "SQUARE_SANDBOX_APPLICATION_ID", func(o *organization.Organization, v string) { o.Square.Sandbox.ApplicationId = v }},
		// Square — Webhook
		{square, "SQUARE_WEBHOOK_SIGNATURE_KEY", func(o *organization.Organization, v string) { o.Square.WebhookSignatureKey = v }},
		{square, "SQUARE_WEBHOOK_URL", func(o *organization.Organization, v string) { o.Square.WebhookURL = v }},

		// AuthorizeNet — Live
		{authnet, "AUTHORIZENET_LIVE_LOGIN_ID", func(o *organization.Organization, v string) { o.AuthorizeNet.Live.LoginId = v }},
		{authnet, "AUTHORIZENET_LIVE_TRANSACTION_KEY", func(o *organization.Organization, v string) { o.AuthorizeNet.Live.TransactionKey = v }},
		// AuthorizeNet — Sandbox
		{authnet, "AUTHORIZENET_SANDBOX_LOGIN_ID", func(o *organization.Organization, v string) { o.AuthorizeNet.Sandbox.LoginId = v }},
		{authnet, "AUTHORIZENET_SANDBOX_TRANSACTION_KEY", func(o *organization.Organization, v string) { o.AuthorizeNet.Sandbox.TransactionKey = v }},

		// PayPal — Live
		{paypal, "PAYPAL_LIVE_EMAIL", func(o *organization.Organization, v string) { o.Paypal.Live.Email = v }},
		{paypal, "PAYPAL_LIVE_SECURITY_USER_ID", func(o *organization.Organization, v string) { o.Paypal.Live.SecurityUserId = v }},
		{paypal, "PAYPAL_LIVE_SECURITY_PASSWORD", func(o *organization.Organization, v string) { o.Paypal.Live.SecurityPassword = v }},
		{paypal, "PAYPAL_LIVE_SECURITY_SIGNATURE", func(o *organization.Organization, v string) { o.Paypal.Live.SecuritySignature = v }},
		{paypal, "PAYPAL_LIVE_APPLICATION_ID", func(o *organization.Organization, v string) { o.Paypal.Live.ApplicationId = v }},
		// PayPal — Test
		{paypal, "PAYPAL_TEST_EMAIL", func(o *organization.Organization, v string) { o.Paypal.Test.Email = v }},
		{paypal, "PAYPAL_TEST_SECURITY_USER_ID", func(o *organization.Organization, v string) { o.Paypal.Test.SecurityUserId = v }},
		{paypal, "PAYPAL_TEST_SECURITY_PASSWORD", func(o *organization.Organization, v string) { o.Paypal.Test.SecurityPassword = v }},
		{paypal, "PAYPAL_TEST_SECURITY_SIGNATURE", func(o *organization.Organization, v string) { o.Paypal.Test.SecuritySignature = v }},
		{paypal, "PAYPAL_TEST_APPLICATION_ID", func(o *organization.Organization, v string) { o.Paypal.Test.ApplicationId = v }},

		// Adyen
		{adyen, "ADYEN_API_KEY", func(o *organization.Organization, v string) { o.Adyen.APIKey = v }},
		{adyen, "ADYEN_MERCHANT_ACCOUNT", func(o *organization.Organization, v string) { o.Adyen.MerchantAccount = v }},
		{adyen, "ADYEN_HMAC_KEY", func(o *organization.Organization, v string) { o.Adyen.HMACKey = v }},
		{adyen, "ADYEN_ENVIRONMENT", func(o *organization.Organization, v string) { o.Adyen.Environment = v }},
		{adyen, "ADYEN_LIVE_URL_PREFIX", func(o *organization.Organization, v string) { o.Adyen.LiveURLPrefix = v }},

		// Braintree
		{braintree, "BRAINTREE_MERCHANT_ID", func(o *organization.Organization, v string) { o.Braintree.MerchantID = v }},
		{braintree, "BRAINTREE_PUBLIC_KEY", func(o *organization.Organization, v string) { o.Braintree.PublicKey = v }},
		{braintree, "BRAINTREE_PRIVATE_KEY", func(o *organization.Organization, v string) { o.Braintree.PrivateKey = v }},
		{braintree, "BRAINTREE_ENVIRONMENT", func(o *organization.Organization, v string) { o.Braintree.Environment = v }},

		// Recurly
		{recurly, "RECURLY_API_KEY", func(o *organization.Organization, v string) { o.Recurly.APIKey = v }},
		{recurly, "RECURLY_SUBDOMAIN", func(o *organization.Organization, v string) { o.Recurly.Subdomain = v }},

		// LemonSqueezy
		{lemon, "LEMONSQUEEZY_API_KEY", func(o *organization.Organization, v string) { o.LemonSqueezy.APIKey = v }},
		{lemon, "LEMONSQUEEZY_STORE_ID", func(o *organization.Organization, v string) { o.LemonSqueezy.StoreID = v }},
		{lemon, "LEMONSQUEEZY_WEBHOOK_SECRET", func(o *organization.Organization, v string) { o.LemonSqueezy.WebhookSecret = v }},
		{lemon, "LEMONSQUEEZY_DEFAULT_VARIANT_ID", func(o *organization.Organization, v string) { o.LemonSqueezy.DefaultVariantID = v }},

		// Wire Transfer
		{wire, "WIRE_BANK_NAME", func(o *organization.Organization, v string) { o.Wire.BankName = v }},
		{wire, "WIRE_ACCOUNT_HOLDER", func(o *organization.Organization, v string) { o.Wire.AccountHolder = v }},
		{wire, "WIRE_ROUTING_NUMBER", func(o *organization.Organization, v string) { o.Wire.RoutingNumber = v }},
		{wire, "WIRE_ACCOUNT_NUMBER", func(o *organization.Organization, v string) { o.Wire.AccountNumber = v }},
		{wire, "WIRE_SWIFT", func(o *organization.Organization, v string) { o.Wire.SWIFT = v }},
		{wire, "WIRE_IBAN", func(o *organization.Organization, v string) { o.Wire.IBAN = v }},
		{wire, "WIRE_BANK_ADDRESS", func(o *organization.Organization, v string) { o.Wire.BankAddress = v }},
		{wire, "WIRE_REFERENCE", func(o *organization.Organization, v string) { o.Wire.Reference = v }},
		{wire, "WIRE_INSTRUCTIONS", func(o *organization.Organization, v string) { o.Wire.Instructions = v }},

		// Mercury
		{merc, "MERCURY_API_TOKEN", func(o *organization.Organization, v string) { o.Mercury.APIToken = v }},
		{merc, "MERCURY_WEBHOOK_SECRET", func(o *organization.Organization, v string) { o.Mercury.WebhookSecret = v }},
		{merc, "MERCURY_ACCOUNT_ID", func(o *organization.Organization, v string) { o.Mercury.AccountID = v }},
	}
}

// Hydrate fetches all provider credentials from KMS and populates the org's
// integration fields. Missing secrets are silently skipped (not every org uses
// every provider). Only KMS communication failures are returned as errors.
func Hydrate(cc *CachedClient, org *organization.Organization) error {
	for _, m := range mappings(org.Name) {
		val, err := cc.GetSecret(m.path, m.name)
		if err != nil {
			// "not found" errors are expected — skip silently
			if strings.Contains(err.Error(), "status 404") || strings.Contains(err.Error(), "status 400") {
				continue
			}
			// Real communication failure
			return err
		}
		if val != "" {
			m.set(org, val)
		}
	}
	return nil
}

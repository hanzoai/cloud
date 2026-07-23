package integrations

// whatsapp.go registers WhatsApp Business (Cloud API) as an ORG token connector for
// the Guide's messaging Account-Linking step. The customer supplies a permanent
// System User access token plus the phone number id (the accountId on /connect),
// VERIFIED live by reading that phone number's metadata via the Graph API before the
// token is sealed into the org's KMS namespace under api_key; the messaging path
// rides it via integrations.TokenFor(org, "whatsapp", "api_key") + the phone number
// id recorded as the connection ExternalID.
//
// It is the declarative keyVerify shape (keyverify.go): the token rides the
// Authorization header (never the URL), a bad/insufficient token stores nothing, and
// no error carries the token value. The {account} path token binds the verify to the
// caller-supplied phone number id (echoed as ExternalID), exactly like the Twilio
// account SID. Verify origin is env-overridable (WHATSAPP_API_BASE) for tests.
// Linking is an org-admin action.

func init() {
	register(&Provider{
		ID:          "whatsapp",
		Name:        "WhatsApp Business",
		Description: "Send WhatsApp messages via the Cloud API. Connect with a System User token + phone number id.",
		Category:    "Messaging",
		Kind:        apiKeyKind,
		AdminOnly:   true,
		Secrets:     []string{apiKeySecret},
		Configured:  func() bool { return true },
		Creds:       func() OAuthConfig { return OAuthConfig{} },
		Verify: keyVerify(keySpec{
			provider: "whatsapp",
			origin:   constOrigin("WHATSAPP_API_BASE", "https://graph.facebook.com"),
			path:     "/v21.0/{account}?fields=verified_name,display_phone_number",
			place:    bearer, minLen: 20, echoAccount: true,
		}),
	})
}

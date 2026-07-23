package integrations

// warpcast.go registers Farcaster (Warpcast) as an ORG apikey connector for the
// Guide's social Account-Linking step. The customer supplies a Farcaster API key
// (Neynar — the standard Farcaster read/write API), VERIFIED live against a cheap
// authenticated endpoint before it is sealed into the org's KMS namespace under
// api_key; the social publish/read paths ride it via
// integrations.TokenFor(org, "warpcast", "api_key").
//
// It is the declarative keyVerify shape (keyverify.go): fail-closed and token-free by
// construction — a bad key stores nothing and no error carries the key value. The
// verify origin is env-overridable (NEYNAR_API_BASE) for tests and self-hosted
// gateways. Linking is an org-admin action.

func init() {
	register(&Provider{
		ID:          "warpcast",
		Name:        "Warpcast",
		Description: "Publish and read Farcaster casts via a Neynar API key.",
		Category:    "Social",
		Kind:        apiKeyKind,
		AdminOnly:   true,
		Secrets:     []string{apiKeySecret},
		// Org apikey providers declare Configured/Creds (Mount asserts it); the apikey
		// path is always available (customer-held key), and there is no OAuth leg.
		Configured: func() bool { return true },
		Creds:      func() OAuthConfig { return OAuthConfig{} },
		Verify: keyVerify(keySpec{
			provider: "warpcast",
			origin:   constOrigin("NEYNAR_API_BASE", "https://api.neynar.com"),
			path:     "/v2/farcaster/user/bulk?fids=3",
			place:    headerAt, name: "x-api-key", minLen: 8,
		}),
	})
}

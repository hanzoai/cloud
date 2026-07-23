package integrations

// analytics.go registers the product-analytics & experimentation connectors that
// give the agentic loop its "deep insights" — who converted, which experiment won,
// what a cohort did. Customer-held keys, verified live via keyVerify against each
// provider's authenticated read, on the per-user /v1/connectors plane.
//
// Least-privilege note: these are READ credentials (dashboards/queries), not
// ingestion write keys. A publish-only CDP write key (e.g. a Segment source write
// key) has no synchronous authenticated read to verify against and belongs to the
// destinations/automations plane, not the credential-verify plane — see the report.

func init() {
	// Optimizely — experimentation. A Personal Access Token is a bearer credential,
	// verified against /v2/projects. Ties to the /v1/experiments surface.
	register(&Provider{
		ID: "optimizely", Name: "Optimizely",
		Description: "Experimentation and feature flags. Connect with a Personal Access Token.",
		Category:    "Experimentation", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "optimizely",
			origin:   constOrigin("OPTIMIZELY_API_BASE", "https://api.optimizely.com"),
			path:     "/v2/projects", place: bearer, minLen: 8,
		}),
	})

	// Amplitude — product analytics. The Dashboard/Analytics REST API authenticates
	// with HTTP Basic "<api_key>:<secret_key>" (basicRaw over the joined pair),
	// verified by listing annotations.
	register(&Provider{
		ID: "amplitude", Name: "Amplitude",
		Description: "Product analytics. Connect with your project's `API_KEY:SECRET_KEY`.",
		Category:    "Analytics", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "amplitude",
			origin:   constOrigin("AMPLITUDE_API_BASE", "https://amplitude.com"),
			path:     "/api/2/annotations", place: basicRaw, minLen: 8,
		}),
	})

	// Mixpanel — product analytics. A Service Account authenticates with HTTP Basic
	// "<username>:<secret>" (basicRaw) and reads are project-scoped, so the caller
	// supplies the project id as the accountId; verify reads that project's Lexicon
	// schemas. The project id is echoed as the connection's external id.
	register(&Provider{
		ID: "mixpanel", Name: "Mixpanel",
		Description: "Product analytics. Connect a Service Account `username:secret` + your project id.",
		Category:    "Analytics", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "mixpanel",
			origin:   constOrigin("MIXPANEL_API_BASE", "https://mixpanel.com"),
			path:     "/api/app/projects/{account}/schemas",
			place:    basicRaw, minLen: 8, echoAccount: true,
		}),
	})

	// PostHog — product analytics & session insights. A Personal API Key is a bearer
	// credential, verified against /api/users/@me/. Default host is PostHog Cloud US;
	// EU or self-hosted deployments repoint POSTHOG_API_BASE (the non-redirecting
	// regional host — the auth client never follows a 3xx).
	register(&Provider{
		ID: "posthog", Name: "PostHog",
		Description: "Product analytics and session replay. Connect with a Personal API Key.",
		Category:    "Analytics", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "posthog",
			origin:   constOrigin("POSTHOG_API_BASE", "https://us.posthog.com"),
			path:     "/api/users/@me/", place: bearer, minLen: 8,
		}),
	})
}

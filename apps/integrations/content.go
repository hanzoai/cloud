package integrations

// content.go registers the content & hosting connectors an agentic marketing site
// publishes through: a CMS (Contentful) and a static host (Netlify). Customer-held
// tokens on the per-user /v1/connectors plane, verified live via keyVerify.
//
// Netlify offers full 3-legged OAuth too; the Personal Access Token (a bearer
// credential) is the token-auth path that fits the one key mechanism, exactly as
// Zendesk's API token does — the OAuth app leg is a follow-on, not a blocker.

func init() {
	// Contentful — headless CMS. A Content Management API (CMA) token is a bearer
	// credential, verified by listing the spaces it can reach.
	register(&Provider{
		ID: "contentful", Name: "Contentful",
		Description: "Headless CMS. Connect with a Content Management API token.",
		Category:    "Content", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "contentful",
			origin:   constOrigin("CONTENTFUL_API_BASE", "https://api.contentful.com"),
			path:     "/spaces", place: bearer, minLen: 8,
		}),
	})

	// Netlify — static hosting & deploys. A Personal Access Token is a bearer
	// credential, verified against the authenticated user.
	register(&Provider{
		ID: "netlify", Name: "Netlify",
		Description: "Static hosting and deploys. Connect with a Personal Access Token.",
		Category:    "Hosting", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "netlify",
			origin:   constOrigin("NETLIFY_API_BASE", "https://api.netlify.com"),
			path:     "/api/v1/user", place: bearer, minLen: 8,
		}),
	})
}

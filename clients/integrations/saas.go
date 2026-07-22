package integrations

// saas.go registers the CRM, docs, issue-tracking, and structured-data
// connectors both verticals run their operations on. Customer-held tokens,
// verified live via keyVerify against each provider's identity/account read.

func init() {
	register(&Provider{
		ID: "hubspot", Name: "HubSpot",
		Description: "CRM, marketing, and sales. Connect with a private-app token.",
		Category:    "Sales", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "hubspot",
			origin:   constOrigin("HUBSPOT_API_BASE", "https://api.hubapi.com"),
			path:     "/account-info/v3/details", place: bearer, minLen: 16,
		}),
	})

	register(&Provider{
		ID: "notion", Name: "Notion",
		Description: "Docs, wikis, and databases.",
		Category:    "Productivity", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "notion",
			origin:   constOrigin("NOTION_API_BASE", "https://api.notion.com"),
			path:     "/v1/users/me", place: bearer, minLen: 16,
			extra: map[string]string{"Notion-Version": "2022-06-28"},
		}),
	})

	register(&Provider{
		ID: "linear", Name: "Linear",
		Description: "Issue tracking and project planning.",
		Category:    "Developer", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "linear",
			origin:   constOrigin("LINEAR_API_BASE", "https://api.linear.app"),
			path:     "/graphql",
			method:   "POST", body: `{"query":"{ viewer { id } }"}`,
			bodyType: "application/json",
			place:    headerAt, name: "Authorization", minLen: 16,
		}),
	})

	register(&Provider{
		ID: "airtable", Name: "Airtable",
		Description: "Spreadsheet-database for structured data.",
		Category:    "Data", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "airtable",
			origin:   constOrigin("AIRTABLE_API_BASE", "https://api.airtable.com"),
			path:     "/v0/meta/whoami", place: bearer, minLen: 16,
		}),
	})
}

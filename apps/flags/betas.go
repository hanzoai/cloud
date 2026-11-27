// Copyright © 2026 Hanzo AI. MIT License.

package flags

// betas.go — every app or model no one has yet, on hanzo.ai or platform.hanzo.ai,
// each behind a principal switch of its own.
//
// beta.<id> is whether a caller sees that one app in the Hanzo app: it appears
// in the rail's More and opens where it lives. It hides nothing anyone has. It
// is decided per caller like `beta` (access.go): a SuperAdmin names the person
// (grant.go) or the org, nothing stored is off, and no tenant can write it. A
// caller reads every one at GET /v1/flag/platform.
//
// The ids are the app's own (@hanzo/rooms lib/betas.ts, which carries each app's
// name, line and group for the Betas list). A person asks for one from that list
// or from hanzo.ai/beta, the ask lands on their CRM record (apps/marketing), and
// the grant is made from there.

// BetaApp is one app or model in beta: its id and the name the cockpit shows.
type BetaApp struct {
	ID   string
	Name string
}

// Betas is every app and model in beta, in the Betas list's order.
var Betas = []BetaApp{
	{"crm", "CRM"},
	{"erp", "ERP"},
	{"commerce", "Commerce"},
	{"books", "Books"},
	{"finance", "Finance"},
	{"treasury", "Treasury & Ledger"},
	{"dataroom", "Data Room"},
	{"sign", "Sign"},
	{"company", "Company"},
	{"cms", "CMS"},
	{"helpdesk", "Help Center"},
	{"marketing", "Marketing"},
	{"ads", "Ads"},
	{"social", "Social"},
	{"tel", "Telecom"},
	{"risk", "Risk"},
	{"idv", "Identity Verification"},
	{"base", "Base"},
	{"analytics", "Analytics"},
	{"insights", "Insights"},
	{"search", "Search"},
	{"world", "World"},
	{"crawl", "Crawl"},
	{"notebook", "Notebook"},
	{"workspace", "Google Workspace"},
	{"science", "Science"},
	{"operative", "Operative"},
	{"tasks", "Tasks"},
	{"functions", "Functions"},
	{"visor", "Visor"},
	{"edge", "Edge"},
	{"registry", "Registry"},
	{"ci", "CI"},
	{"kms", "KMS"},
	{"guard", "Guard"},
	{"sentinel", "Sentinel"},
	{"authz", "Authz"},
	{"hsm", "HSM"},
	{"cyber", "Cyber"},
	{"koma", "Koma"},
	{"iro", "Iro"},
	{"mori", "Mori"},
	{"oto", "Oto"},
	{"koe", "Koe"},
	{"uta", "Uta"},
	{"zen7", "Zen 7"},
	// The two beta sites. IAM gates their OAuth code mint on these switches, by
	// the VALIDATED redirect host (one client serves many hosts) — see
	// iam/internal/oidc/beta.go and iam/BETA_GATE.md. Staff get every beta.* via
	// access.go staffed(); nothing here duplicates that.
	{"bot", "Bot"},
	{"team", "Team"},
}

// BetaKey is the principal switch of one app in beta.
func BetaKey(id string) string { return Beta + "." + id }

func init() {
	for _, b := range Betas {
		Register(Def{
			Key:       BetaKey(b.ID),
			Category:  "Beta",
			Label:     b.Name,
			Desc:      "Who sees " + b.Name + " in the Hanzo app before it ships: it appears in their rail's More and opens where it lives. On for staff (SuperAdmins), for the people a SuperAdmin grants it to (\"user\", their IAM id) and the orgs its rules name, and, once promoted to a public beta (an \"optin\" rule), for everyone who opted in to public betas and whose org did not block it; off for everyone else, and with nothing stored.",
			Type:      TypeBool,
			Default:   "false",
			Principal: true,
		})
	}
}

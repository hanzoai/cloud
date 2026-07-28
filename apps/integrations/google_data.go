package integrations

// google_data.go adds the two Google DATA-platform connectors — BigQuery and Google
// Cloud — as siblings of the base google.go provider and the google_marketing.go ad
// connectors. They REUSE google.go's OAuth plumbing wholesale (googleCreds,
// googleConfigured, googleExchange, googleRevoke, googleAccount, googleAuthorizeWith)
// and the same registered Google Cloud app creds (GOOGLE_CLIENT_ID/SECRET); the ONLY
// thing that differs is the requested SCOPE set. Distinct provider ids give each its
// own KMS custody namespace, so an org can hold independent consents per Google
// product (Drive/Sheets via base google, Ads/Analytics, BigQuery/Cloud) and each
// reader pulls its own token via integrations.TokenFor(org, "google_bigquery"|
// "google_cloud", "access_token").
//
// Both are READ scopes (bigquery.readonly, cloud-platform.read-only) — least
// privilege, the meta_ads discipline: no write/admin grant until a write path is
// wired. Google DRIVE is already covered by the base "google" connector's
// drive.readonly scope and is NOT duplicated here. Linking is an org-admin action.

const (
	googleBigQueryProvider = "google_bigquery"
	googleCloudProvider    = "google_cloud"
)

// Scope sets. openid+email give googleAccount the connecting user's email for the
// account label; the product scope is the (read-only) capability.
var (
	googleBigQueryScopes = []string{
		"openid", "email",
		"https://www.googleapis.com/auth/bigquery.readonly",
	}
	googleCloudScopes = []string{
		"openid", "email",
		"https://www.googleapis.com/auth/cloud-platform.read-only",
	}
)

func init() {
	register(&Provider{
		ID:           googleBigQueryProvider,
		Name:         "Google BigQuery",
		Description:  "Connect Google BigQuery to read datasets, tables, and query results.",
		Category:     "Data",
		AdminOnly:    true,
		Scopes:       googleBigQueryScopes,
		RedirectPath: callbackPath(googleBigQueryProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   googleConfigured, // reuse: GOOGLE_CLIENT_ID present
		Creds:        googleCreds,      // reuse: GOOGLE_CLIENT_ID/SECRET
		Authorize:    googleAuthorizeWith(googleBigQueryScopes),
		Exchange:     googleExchange, // reuse: scope-agnostic code→tokens+account
		Revoke:       googleRevoke,   // reuse
	})

	register(&Provider{
		ID:           googleCloudProvider,
		Name:         "Google Cloud",
		Description:  "Connect Google Cloud to read project resources and metadata.",
		Category:     "Data",
		AdminOnly:    true,
		Scopes:       googleCloudScopes,
		RedirectPath: callbackPath(googleCloudProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   googleConfigured,
		Creds:        googleCreds,
		Authorize:    googleAuthorizeWith(googleCloudScopes),
		Exchange:     googleExchange,
		Revoke:       googleRevoke,
	})
}

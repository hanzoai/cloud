package integrations

// crm.go registers the CRM & customer-support connectors an agentic marketing
// loop reads to know its customers. Customer-held API tokens, verified live via
// keyVerify against each provider's identity/account read — the exact declarative
// shape saas.go uses for HubSpot, on the per-user /v1/connectors plane. The
// OAuth-only CRM (Salesforce) lives on the org plane in salesforce.go; these are
// the token-auth providers that fit the one key mechanism.

import (
	"fmt"
	"strings"
)

func init() {
	// Zendesk — support tickets & help center. API-token auth is HTTP Basic with the
	// username "<email>/token" and the API token as the password, against the
	// account's own <subdomain>.zendesk.com host. The caller supplies the subdomain as
	// the accountId and the pre-joined "<email>/token:<api_token>" as the credential
	// (the basicRaw shape PayPal/ShipStation use), so verify reads /users/me.json.
	register(&Provider{
		ID: "zendesk", Name: "Zendesk",
		Description: "Support tickets and help center. Connect with your Zendesk subdomain + `email/token:APITOKEN`.",
		Category:    "Support", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "zendesk",
			origin:   subdomainOrigin("ZENDESK_API_BASE", ".zendesk.com", "zendesk"),
			path:     "/api/v2/users/me.json",
			place:    basicRaw, minLen: 8, echoAccount: true,
		}),
	})

	// Pipedrive — sales CRM. The API token rides the x-api-token header (Pipedrive's
	// current header auth), verified against /v1/users/me.
	register(&Provider{
		ID: "pipedrive", Name: "Pipedrive",
		Description: "Sales CRM: deals, contacts, and pipelines. Connect with an API token.",
		Category:    "Sales", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "pipedrive",
			origin:   constOrigin("PIPEDRIVE_API_BASE", "https://api.pipedrive.com"),
			path:     "/v1/users/me", place: headerAt, name: "x-api-token", minLen: 8,
		}),
	})

	// Intercom — support & lifecycle messaging. A workspace Access Token is a bearer
	// credential (the token-auth path; full 3-legged OAuth is a follow-on), verified
	// against /me (the authenticated app/admin identity).
	register(&Provider{
		ID: "intercom", Name: "Intercom",
		Description: "Customer messaging and support. Connect with an Access Token.",
		Category:    "Support", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "intercom",
			origin:   constOrigin("INTERCOM_API_BASE", "https://api.intercom.io"),
			path:     "/me", place: bearer, minLen: 8,
			extra: map[string]string{"Accept": "application/json"},
		}),
	})

	// Re:amaze — helpdesk & live chat. HTTP Basic with the login email as username and
	// the API token as password (basicRaw over "<email>:<api_token>"), against the
	// brand's own <brand>.reamaze.io host. Verify lists the account's channels.
	register(&Provider{
		ID: "reamaze", Name: "Re:amaze",
		Description: "Helpdesk and live chat. Connect with your Re:amaze brand + `email:APITOKEN`.",
		Category:    "Support", Scope: userScope,
		Secrets: []string{apiKeySecret},
		Verify: keyVerify(keySpec{
			provider: "reamaze",
			origin:   subdomainOrigin("REAMAZE_API_BASE", ".reamaze.io", "reamaze"),
			path:     "/api/v1/channels",
			place:    basicRaw, minLen: 8, echoAccount: true,
		}),
	})
}

// subdomainOrigin builds a provider's per-account origin from the caller's
// subdomain (VerifyInput.AccountID) on a FIXED suffix (".zendesk.com",
// ".reamaze.io") — the sibling of payments.go's shopifyOrigin, generalized for the
// providers whose API host is <account>.<vendor>. Env-overridable for the httptest
// seam. It is the ONE subdomain-host normalizer for the key plane's multi-tenant
// SaaS hosts; shopify keeps its bespoke builder (its normalization messages are
// load-bearing).
func subdomainOrigin(envKey, suffix, provider string) func(VerifyInput) (string, error) {
	return func(in VerifyInput) (string, error) {
		if v := envBase(envKey); v != "" {
			return v, nil
		}
		host, err := subdomainHost(in.AccountID, suffix, provider)
		if err != nil {
			return "", err
		}
		return "https://" + host, nil
	}
}

// subdomainHost normalizes a customer-supplied account identifier to a
// "<label><suffix>" host: it accepts the bare label ("acme"), the full host
// ("acme.zendesk.com"), or a URL, and rejects anything that is not a plain DNS host
// on the fixed suffix — so a hostile value can never smuggle a path, port,
// credential, or a different host into the verify origin. validHostLabel (payments.go)
// admits only DNS-safe characters, closing "acme.zendesk.com@evil.com" and
// "acme.zendesk.com/../x"; the HasSuffix gate closes "acme.zendesk.com.evil.com".
func subdomainHost(account, suffix, provider string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(account))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i] // drop any path
	}
	bare := strings.TrimPrefix(suffix, ".")
	if s == "" {
		return "", fmt.Errorf("%s needs the account's %s subdomain", provider, bare)
	}
	if !strings.Contains(s, ".") {
		s += suffix
	}
	if !strings.HasSuffix(s, suffix) || !validHostLabel(s) {
		return "", fmt.Errorf("%s domain must be a <name>%s host", provider, suffix)
	}
	return s, nil
}

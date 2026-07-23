package integrations

// salesforce.go registers Salesforce as an ORG OAuth connector — the highest-value
// CRM link for an agentic marketing loop. It is the standard authorization-code flow
// (Authorize → Exchange), the same shape as google.go, with ONE provider-specific
// wrinkle: Salesforce returns a per-org instance_url in the token response, and every
// REST call must go to THAT host. So the connection custodies three values in the
// org's KMS namespace — access_token, refresh_token, and instance_url — and the CRM
// reader rides them via integrations.TokenFor(org, "salesforce", …). instance_url is
// sealed alongside the tokens (the connection's private store) so the reader learns
// the API host without a second lookup.
//
// Creds are ENV-injected (KMS-synced, never in code): SALESFORCE_CLIENT_ID +
// SALESFORCE_CLIENT_SECRET. Scope is least-privilege for a working CRM connection:
// "api" (REST access — Salesforce offers NO read-only API scope, exactly like
// Microsoft's single msads.manage), "refresh_token" (offline), "openid" (identity
// label only). No "full", no "web". Sandbox orgs repoint SALESFORCE_LOGIN_BASE to
// test.salesforce.com. Linking a client's Salesforce org is an org-admin action.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// salesforceInstanceSuffixes are the domains a real Salesforce instance_url ends
// in. The custody guard pins the host to one of these so a spoofed token endpoint
// cannot inject an SSRF target (IMDS, RFC1918, an attacker host) the reader would
// later call as its API base. Parity with shopHost/subdomainHost, which pin too.
var salesforceInstanceSuffixes = []string{".salesforce.com", ".force.com", ".cloudforce.com", ".database.com"}

const (
	salesforceProvider        = "salesforce"
	salesforceClientIDEnv     = "SALESFORCE_CLIENT_ID"
	salesforceClientSecretEnv = "SALESFORCE_CLIENT_SECRET"
	// salesforceInstanceURL is the KMS secret name the per-org API host seals under.
	// It is NON-secret metadata, but the connection's KMS namespace is its private
	// store, and the reader needs the host to build any REST call.
	salesforceInstanceURL = "instance_url"
	// salesforceLoginBaseEnv repoints the login host for sandbox orgs
	// (test.salesforce.com) or the httptest seam. My-domain orgs also work through
	// login.salesforce.com for the authorization leg.
	salesforceLoginBaseEnv = "SALESFORCE_LOGIN_BASE"
)

// salesforceScopes: REST + offline + identity. "api" is Salesforce's minimum REST
// scope (no read-only variant exists); "refresh_token" grants offline renewal;
// "openid" yields an id_token for the account label (no extra call, no data grant).
var salesforceScopes = []string{"api", "refresh_token", "openid"}

// Endpoint paths are appended to the resolved login base. Package var for the base so
// a test / sandbox repoints it; never mutated in production.
var salesforceLoginBase = "https://login.salesforce.com"

func salesforceBase() string {
	if v := envBase(salesforceLoginBaseEnv); v != "" {
		return v
	}
	return salesforceLoginBase
}

func init() {
	register(&Provider{
		ID:           salesforceProvider,
		Name:         "Salesforce",
		Description:  "Connect Salesforce CRM to read and act on accounts, contacts, and opportunities.",
		Category:     "CRM",
		AdminOnly:    true,
		Scopes:       salesforceScopes,
		RedirectPath: callbackPath(salesforceProvider),
		Secrets:      []string{accessSecret, refreshSecret, salesforceInstanceURL},
		Configured:   salesforceConfigured,
		Creds:        salesforceCreds,
		Authorize:    salesforceAuthorize,
		Exchange:     salesforceExchange,
		Revoke:       salesforceRevoke,
	})
}

func salesforceCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(salesforceClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(salesforceClientSecretEnv)),
	}
}

func salesforceConfigured() bool { return salesforceCreds().ClientID != "" }

func salesforceAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {creds.ClientID},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(salesforceScopes, " ")},
		"state":         {state},
	}
	return salesforceBase() + "/services/oauth2/authorize?" + q.Encode(), nil
}

type salesforceTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	InstanceURL  string `json:"instance_url"`
	IDToken      string `json:"id_token"`
	ID           string `json:"id"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// salesforceExchange trades the code for tokens + the org's instance_url and seals
// all three. Requires the client secret. A missing or non-https instance_url FAILS
// the exchange: a Salesforce connection with no API host is broken, and refusing a
// non-https host blocks a spoofed token endpoint from injecting an SSRF target the
// reader would later call.
func salesforceExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("salesforce integration is not fully configured on this deployment: SALESFORCE_CLIENT_SECRET is not set")
	}
	var r salesforceTokenResponse
	if err := oauthPostForm(ctx, salesforceProvider, salesforceBase()+"/services/oauth2/token", nil, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("salesforce token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("salesforce token exchange returned no access_token")
	}
	instance, err := salesforceInstance(r.InstanceURL)
	if err != nil {
		return nil, err
	}
	tokens := map[string]string{accessSecret: r.AccessToken, salesforceInstanceURL: instance}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := salesforceAccount(r.IDToken, r.ID)
	return &ExchangeResult{
		Tokens:       tokens,
		ExternalID:   externalID,
		AccountLabel: label,
		Scopes:       strings.Fields(r.Scope),
	}, nil
}

// salesforceInstance validates the token response's instance_url is an https URL with
// a plain DNS host, returning the normalized "https://<host>" origin. It rejects any
// non-https scheme, an embedded credential/port-trick, or a missing host — the reader
// custodies this as its API base, so a malformed value must never reach it.
func salesforceInstance(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("salesforce returned no usable instance_url")
	}
	host := strings.ToLower(u.Hostname()) // port-stripped; the port is dropped below
	if !validHostLabel(host) {
		return "", fmt.Errorf("salesforce returned an unusable instance host")
	}
	// A test seam (SALESFORCE_LOGIN_BASE set) points the whole flow at httptest, whose
	// instance_url is a loopback host:port — allow it there only. In prod, pin the host
	// to a Salesforce suffix, reject a bare IP literal (IMDS/RFC1918/loopback), and drop
	// any port, so a spoofed token endpoint cannot make the reader dial an attacker host.
	if strings.TrimSpace(os.Getenv(salesforceLoginBaseEnv)) == "" {
		if net.ParseIP(host) != nil {
			return "", fmt.Errorf("salesforce instance host must not be an IP literal")
		}
		pinned := false
		for _, s := range salesforceInstanceSuffixes {
			if strings.HasSuffix(host, s) {
				pinned = true
				break
			}
		}
		if !pinned {
			return "", fmt.Errorf("salesforce instance host is not a Salesforce domain")
		}
		return "https://" + host, nil // port dropped
	}
	return "https://" + u.Host, nil
}

// salesforceAccount derives the account label from the id_token claims (openid), with
// the identity-service URL (id) as the external id fallback. The id_token is
// decorative metadata, never an authentication input. Best-effort: absent claims
// yield empty labels (the tokens are already valid).
func salesforceAccount(idToken, idURL string) (externalID, label string) {
	if claims := jwtClaims(idToken); claims != nil {
		externalID = claimString(claims, "user_id", "sub")
		label = claimString(claims, "email", "preferred_username", "name")
	}
	if externalID == "" {
		externalID = strings.TrimSpace(idURL)
	}
	return externalID, label
}

// salesforceRevoke best-effort invalidates a token at Salesforce on disconnect.
func salesforceRevoke(ctx context.Context, _ OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return oauthPostForm(ctx, salesforceProvider, salesforceBase()+"/services/oauth2/revoke", nil, url.Values{"token": {token}}, nil)
}

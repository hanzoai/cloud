package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Teams is provider #5: a Microsoft Teams integration backed by an Azure AD app +
// an Azure Bot (Bot Framework). ONE Hanzo Teams app is installed into an org's
// tenant; connecting runs the AAD authorize CODE flow whose id_token `tid` claim is
// the tenant id, which we map tenant.id → org. Inbound is the Bot Framework
// messaging webhook (JWT-verified against Bot Framework's JWKS, teams_events.go).
//
// Connect FITS the generic OAuth mold (Authorize/Exchange): the admin signs in at
// login.microsoftonline.com, we exchange the code and read the tenant id from the
// id_token. The card lights up on MICROSOFT_APP_ID + MICROSOFT_APP_PASSWORD; the
// events webhook uses the same app id as the inbound JWT audience.
func init() {
	register(&Provider{
		ID:           "teams",
		Name:         "Microsoft Teams",
		Description:  "Install the Hanzo app in Teams and mention @Hanzo.",
		Category:     "Communication",
		Scopes:       nil,
		RedirectPath: callbackPath("teams"),
		Secrets:      nil, // no per-tenant secret; the ONE app id/password (env) drives inbound audience + reply token
		Configured:   teamsConfigured,
		Creds:        teamsCreds,
		Authorize:    teamsAuthorize,
		Exchange:     teamsExchange,
		Revoke:       nil, // uninstall happens in Teams; nothing to revoke here
	})
}

const (
	teamsAppIDEnv       = "MICROSOFT_APP_ID"
	teamsAppPasswordEnv = "MICROSOFT_APP_PASSWORD"
	teamsTenantEnv      = "MICROSOFT_APP_TENANT_ID"
)

// teamsAADBase is Microsoft's identity host; a var so tests can repoint it.
var teamsAADBase = "https://login.microsoftonline.com"

func teamsAppID() string       { return strings.TrimSpace(os.Getenv(teamsAppIDEnv)) }
func teamsAppPassword() string { return strings.TrimSpace(os.Getenv(teamsAppPasswordEnv)) }

// teamsAuthority is the AAD authority segment: the pinned single tenant if set,
// else "organizations" (any work/school tenant — the multi-tenant bot case).
func teamsAuthority() string {
	if t := strings.TrimSpace(os.Getenv(teamsTenantEnv)); t != "" {
		return t
	}
	return "organizations"
}

func teamsCreds() OAuthConfig {
	return OAuthConfig{ClientID: teamsAppID(), ClientSecret: teamsAppPassword()}
}

func teamsConfigured() bool { return teamsAppID() != "" && teamsAppPassword() != "" }

// teamsAuthorize builds the AAD v2 authorize (code) URL. The signed-in admin's
// id_token carries the tenant id we bind to the org.
func teamsAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"response_mode": {"query"},
		"scope":         {"openid profile"},
		"state":         {state},
	}
	return teamsAADBase + "/" + teamsAuthority() + "/oauth2/v2.0/authorize?" + q.Encode(), nil
}

// teamsExchange trades the code for an id_token and reads the tenant id (`tid`)
// from it — the tenant we map to the org. The id_token arrives directly from the
// token endpoint over TLS using our client secret, so reading its claims (without a
// second signature check) is sound for extracting the non-secret tenant id.
func teamsExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	form := url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"scope":         {"openid profile"},
	}
	endpoint := teamsAADBase + "/" + teamsAuthority() + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("teams token http %d", resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("teams token decode: %w", err)
	}
	if tok.Error != "" || tok.IDToken == "" {
		return nil, fmt.Errorf("teams token: %s", nonEmpty(tok.Error, "no id_token"))
	}
	tid, name := teamsClaimsFromIDToken(tok.IDToken)
	if tid == "" {
		return nil, fmt.Errorf("teams token: id_token has no tenant id")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{},
		ExternalID:   tid,
		AccountLabel: nonEmpty(name, tid),
	}, nil
}

// teamsClaimsFromIDToken decodes (without re-verifying) the id_token payload for the
// tenant id (`tid`) and a display name. Safe because the token came directly from
// the AAD token endpoint over TLS with our client secret.
func teamsClaimsFromIDToken(idToken string) (tid, name string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var c struct {
		TID  string `json:"tid"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", ""
	}
	return c.TID, c.Name
}

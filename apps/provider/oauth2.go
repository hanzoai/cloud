package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/appcred"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/iam/pkg/pkce"
)

// The authorization-code flow, written ONCE.
//
// Every provider in this catalog speaks RFC 6749 §4.1: send the browser to a
// consent URL, receive a `code`, POST it to a token endpoint, read an access
// token out of the JSON, then call one "who is this" endpoint to label the
// connection. The providers differ only in STRINGS — which URLs, which scope
// separator, where the account name sits in the identity JSON — and in a couple
// of genuine protocol quirks.
//
// Written per-provider, that shape is ~150 lines each, and thirteen copies of a
// token exchange is thirteen chances to get the one that matters wrong: a
// provider whose Exchange forgets to check the HTTP status stores the STRING
// "error" as an access token and reports the account connected. So a provider
// here is a DECLARATION — `grant` below — and `oauthProvider` turns it into the
// Provider the registry wants. The flow has one implementation and one place a
// bug can live.
//
// What a declaration cannot express, it does not have to: `authExtra` adds
// query parameters to the consent URL, and `identify` is a function, so a
// provider whose identity call is genuinely unusual writes that part and
// inherits the rest.

// identity is what an identity call yields: the non-secret pair that labels a
// connection in the UI. Both are clamped by the caller before they are stored.
type identity struct {
	ExternalID string
	Label      string
}

// grant declares one OAuth2 provider. Everything here is data except `identify`,
// which is a function because "who is this" is the one leg providers genuinely
// disagree about — some answer with a flat object, some nest it, some need a
// second call to name the account.
type grant struct {
	id       string
	name     string
	desc     string
	category string

	// authURL / tokenURL are the provider's two endpoints. revokeURL is optional;
	// a provider with no revoke endpoint leaves it empty and disconnect simply
	// forgets the token locally (which is still a real disconnect — the secret is
	// deleted from KMS).
	authURL   string
	tokenURL  string
	revokeURL string

	// scopes are requested at consent and shown on the card. scopeSep is the
	// separator this provider wants between them: a space per RFC 6749, but
	// several large providers use commas and reject spaces.
	scopes   []string
	scopeSep string

	// env names the ENV vars carrying the deployment's APP credentials. A network
	// on the shared ladder (internal/appcred) reads that instead, canonical name
	// first and the older names as a fallback. An organization's own sealed app
	// is applied by credsFor before either, at the connect and the callback.
	clientIDEnv     string
	clientSecretEnv string

	// clientParam is the query and form name of the client id. TikTok Login Kit
	// says client_key. Empty means client_id.
	clientParam string

	// perTarget seals one token per platform account instead of one token per
	// network. The connection key is the platform id.
	perTarget bool

	// expand turns one consent into the accounts worth keeping. Facebook keeps
	// each Page's own token. LinkedIn keeps the member and each company, the
	// company keyed so it cannot replace the member.
	expand func(context.Context, OAuthConfig, string, identity) ([]socialGrant, error)

	// authExtra adds provider-specific query parameters to the consent URL —
	// Google's `access_type=offline`, Reddit's `duration=permanent`, and so on.
	//
	// PKCE is not among them. A declaration carries no per-request state, so the
	// verifier it could send is a compile-time constant every deployment shares
	// and publishes in each authorize URL, which is the property PKCE exists to
	// deny. A provider that requires PKCE writes its own flow, where a verifier
	// can be derived per app (x.go) or per flow.
	authExtra map[string]string

	// headers ride every request this provider's token endpoint receives. Reddit
	// answers a default User-Agent with 429, and says what it wants in its rules.
	headers map[string]string

	// refreshes keeps the refresh token and the access token's expiry the
	// endpoint answers, and trades the one for a new pair when the other is
	// near. A provider that issues neither leaves it false and keeps the token
	// it was given. refresh replaces the standard trade for the few that do it
	// their own way: Threads exchanges the long-lived token itself.
	refreshes bool
	refresh   func(ctx context.Context, c OAuthConfig, token string) (*ExchangeResult, error)

	// basicAuth sends the client credentials as an HTTP Basic header instead of
	// form fields. RFC 6749 §2.3.1 says a server MUST support Basic and MAY
	// support the form; several providers only implement the MUST.
	basicAuth bool

	// identify labels the connection. nil means the provider cannot say who it
	// connected, and the connection is labelled by the provider name alone.
	identify func(ctx context.Context, token string) (identity, error)

	// tokenSecret is the KMS secret name the access token is sealed under. It is
	// "access_token" for almost everyone; Slack's bot token is not the user's
	// token, so it says so.
	tokenSecret string

	// pkce sends an RFC 7636 challenge at consent and its verifier at the
	// exchange, for a provider that requires one; pkcePlain sends the verifier
	// itself as the challenge, for the few that do not take S256. The verifier
	// is derived per app from the app's own credential (pkceVerifier), which
	// both legs receive — see x.go for why that holds for a confidential client.
	pkce      bool
	pkcePlain bool

	// finish reads a token response that is NOT shaped like RFC 6749 §5.1.
	// Slack is the real case: it answers 200 with `ok:false` on failure and
	// carries the account in `team{id,name}` rather than at a separate identity
	// endpoint. Giving it a hook keeps ONE request, one status check and one
	// body cap — the parts that go wrong — and lets the one provider that
	// disagrees about JSON field names say only that.
	//
	// It runs after the status check and before the token is required, so it may
	// both reject and supply. A provider that leaves it nil gets the standard
	// reading.
	finish func(body []byte, res *ExchangeResult) error
}

// oauthProvider turns a declaration into a registry Provider. This is the only
// place the authorization-code flow is implemented.
func oauthProvider(sp grant) *Provider {
	if sp.scopeSep == "" {
		sp.scopeSep = " "
	}
	if sp.tokenSecret == "" {
		sp.tokenSecret = "access_token"
	}

	creds := func() OAuthConfig {
		if id, secret, _ := appcred.Resolve(sp.id); id != "" || secret != "" || len(appcred.Pairs(sp.id)) > 0 {
			return OAuthConfig{ClientID: id, ClientSecret: secret}
		}
		return OAuthConfig{
			ClientID:     env.Or(sp.clientIDEnv, ""),
			ClientSecret: env.Or(sp.clientSecretEnv, ""),
		}
	}
	idName := sp.clientParam
	if idName == "" {
		idName = "client_id"
	}

	secrets := []string{sp.tokenSecret}
	if sp.refreshes {
		secrets = append(secrets, refreshSecret)
	}

	return &Provider{
		ID:           sp.id,
		Name:         sp.name,
		Description:  sp.desc,
		Category:     sp.category,
		Scopes:       sp.scopes,
		RedirectPath: callbackPath(sp.id),
		Secrets:      secrets,
		MultiAccount: sp.perTarget,
		PerTarget:    sp.perTarget,
		Expand:       sp.expand,

		// A card lights up on the PUBLIC client id alone: that is all the consent
		// URL needs, so an org can reach the provider's "Allow" screen as soon as
		// the deployment sets it. The secret is required at the exchange, which
		// validates it against the provider — a missing one yields an honest
		// failure redirect, never a silent dead end.
		Configured: func() bool { return creds().ClientID != "" },
		Creds:      creds,

		Authorize: func(c OAuthConfig, redirectURI, state string) (string, error) {
			q := url.Values{
				idName:          {c.ClientID},
				"redirect_uri":  {redirectURI},
				"state":         {state},
				"response_type": {"code"},
			}
			if len(sp.scopes) > 0 {
				q.Set("scope", strings.Join(sp.scopes, sp.scopeSep))
			}
			for k, v := range sp.authExtra {
				q.Set(k, v)
			}
			if sp.pkce {
				v := pkceVerifier(sp.id, c)
				if sp.pkcePlain {
					q.Set("code_challenge", v)
					q.Set("code_challenge_method", "plain")
				} else {
					q.Set("code_challenge", pkce.Challenge(v))
					q.Set("code_challenge_method", "S256")
				}
			}
			// An endpoint may already carry a query (Slack's names its user
			// scopes there), so the two are merged rather than concatenated.
			u, err := url.Parse(sp.authURL)
			if err != nil {
				return "", err
			}
			merged := u.Query()
			for k, vs := range q {
				merged[k] = vs
			}
			u.RawQuery = merged.Encode()
			return u.String(), nil
		},

		Exchange: func(ctx context.Context, c OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
			form := url.Values{
				"grant_type":   {"authorization_code"},
				"code":         {code},
				"redirect_uri": {redirectURI},
			}
			if !sp.basicAuth {
				form.Set(idName, c.ClientID)
				form.Set("client_secret", c.ClientSecret)
			}
			if sp.pkce {
				form.Set("code_verifier", pkceVerifier(sp.id, c))
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.tokenURL,
				strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			for k, v := range sp.headers {
				req.Header.Set(k, v)
			}
			if sp.basicAuth {
				req.SetBasicAuth(c.ClientID, c.ClientSecret)
			}

			resp, err := oauthHTTP.Do(req)
			if err != nil {
				return nil, fmt.Errorf("%s: token exchange: %w", sp.id, err)
			}
			defer resp.Body.Close()
			// maxBody bounds a hostile or broken provider response. A token
			// payload is a few hundred bytes.
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return nil, fmt.Errorf("%s: token exchange: read: %w", sp.id, err)
			}
			// THE STATUS IS CHECKED BEFORE THE BODY IS BELIEVED. An error
			// response is still JSON, and a decoder that ignores the status
			// happily yields a zero-value token — which then gets sealed into
			// KMS and reported to the reader as a live connection.
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				return nil, fmt.Errorf("%s: token exchange: provider said %d", sp.id, resp.StatusCode)
			}

			if sp.finish != nil {
				res := &ExchangeResult{Tokens: map[string]string{}}
				if err := sp.finish(body, res); err != nil {
					return nil, fmt.Errorf("%s: token exchange: %w", sp.id, err)
				}
				if strings.TrimSpace(res.Tokens[sp.tokenSecret]) == "" {
					return nil, fmt.Errorf("%s: token exchange: no access token", sp.id)
				}
				if len(res.Scopes) == 0 {
					res.Scopes = sp.scopes
				}
				if res.AccountLabel == "" {
					res.AccountLabel = sp.name
				}
				return res, nil
			}

			var tok struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				ExpiresIn    int64  `json:"expires_in"`
				Scope        string `json:"scope"`
				// Some providers answer 200 with an error IN the body rather than
				// a 4xx status; both are refusals and both must fail closed.
				Error            string `json:"error"`
				ErrorDescription string `json:"error_description"`
			}
			if err := json.Unmarshal(body, &tok); err != nil {
				return nil, fmt.Errorf("%s: token exchange: decode: %w", sp.id, err)
			}
			if tok.Error != "" {
				return nil, fmt.Errorf("%s: token exchange refused: %s", sp.id, tok.Error)
			}
			if strings.TrimSpace(tok.AccessToken) == "" {
				return nil, fmt.Errorf("%s: token exchange: no access token", sp.id)
			}

			res := &ExchangeResult{
				Tokens: map[string]string{sp.tokenSecret: tok.AccessToken},
				Scopes: splitScopes(tok.Scope, sp.scopeSep),
			}
			if len(res.Scopes) == 0 {
				res.Scopes = sp.scopes
			}
			if sp.refreshes {
				if tok.RefreshToken != "" {
					res.Tokens[refreshSecret] = tok.RefreshToken
				}
				if tok.ExpiresIn > 0 {
					res.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
				}
			}

			// The identity call is best-effort BY DESIGN. The token is already
			// good at this point; failing the whole connect because a display
			// name could not be read would throw away a working credential over
			// a label. An unlabelled connection says so in the UI.
			if sp.identify != nil {
				if acct, err := sp.identify(ctx, tok.AccessToken); err == nil {
					res.ExternalID = acct.ExternalID
					res.AccountLabel = acct.Label
				}
			}
			if res.AccountLabel == "" {
				res.AccountLabel = sp.name
			}
			return res, nil
		},

		Revoke:  revokeFunc(sp),
		Refresh: refreshFunc(sp, creds),
	}
}

// refreshFunc builds the trade of a refresh token for a new access token, or
// nil when the provider issues none. It reads the app the person's org sealed
// before the deployment's, as the connect did.
func refreshFunc(sp grant, creds func() OAuthConfig) func(context.Context, string) (*ExchangeResult, error) {
	if !sp.refreshes {
		return nil
	}
	return func(ctx context.Context, token string) (*ExchangeResult, error) {
		c := creds()
		if org, ok := cloud.Tenant(ctx); ok && mounted != nil {
			if own, hit := orgApp(mounted.State.kms, org, sp.id); hit && own.ClientID != "" {
				c = own
			}
		}
		if sp.refresh != nil {
			return sp.refresh(ctx, c, token)
		}
		return refreshGrant(ctx, sp, c, token)
	}
}

// refreshGrant is RFC 6749 section 6: the refresh token, the client, and the
// new pair. A provider that does not rotate its refresh token answers none, and
// the one in hand stays.
func refreshGrant(ctx context.Context, sp grant, c OAuthConfig, refresh string) (*ExchangeResult, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	headers := map[string]string{}
	for k, v := range sp.headers {
		headers[k] = v
	}
	if sp.basicAuth {
		headers["Authorization"] = basicAuth(c.ClientID, c.ClientSecret)
	} else {
		name := sp.clientParam
		if name == "" {
			name = "client_id"
		}
		form.Set(name, c.ClientID)
		form.Set("client_secret", c.ClientSecret)
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	if err := oauthPostForm(ctx, sp.id, sp.tokenURL, headers, form, &r); err != nil {
		return nil, err
	}
	if r.Error != "" || strings.TrimSpace(r.AccessToken) == "" {
		return nil, fmt.Errorf("%s token refresh refused: %s", sp.id, r.Error)
	}
	res := &ExchangeResult{
		Tokens: map[string]string{sp.tokenSecret: r.AccessToken, refreshSecret: refresh},
		Scopes: splitScopes(r.Scope, sp.scopeSep),
	}
	if r.RefreshToken != "" {
		res.Tokens[refreshSecret] = r.RefreshToken
	}
	if r.ExpiresIn > 0 {
		res.ExpiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second).Unix()
	}
	return res, nil
}

// pkceVerifier is the RFC 7636 verifier for one provider's app: derived from
// the app's credential and the provider id, so both legs compute it and no two
// providers sharing an app share one. The same construction as xVerifier, and
// safe for the same reason: the token exchange also carries the client secret.
func pkceVerifier(id string, c OAuthConfig) string {
	sum := sha256.Sum256([]byte(c.ClientSecret + "|" + c.ClientID + "|" + id + "|hanzo-pkce-v1"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// revokeFunc builds the disconnect-time revoke, or nil when the provider has no
// revoke endpoint. nil is honest: the registry treats it as "nothing to call",
// and the secret is deleted from KMS either way.
func revokeFunc(sp grant) func(context.Context, OAuthConfig, string) error {
	if sp.revokeURL == "" {
		return nil
	}
	return func(ctx context.Context, c OAuthConfig, token string) error {
		form := url.Values{"token": {token}}
		if !sp.basicAuth {
			name := sp.clientParam
			if name == "" {
				name = "client_id"
			}
			form.Set(name, c.ClientID)
			form.Set("client_secret", c.ClientSecret)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.revokeURL,
			strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range sp.headers {
			req.Header.Set(k, v)
		}
		if sp.basicAuth {
			req.SetBasicAuth(c.ClientID, c.ClientSecret)
		}
		resp, err := oauthHTTP.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
}

// splitScopes reads a granted-scope string. Providers answer with spaces or
// commas regardless of what they accepted, so both are split on.
func splitScopes(s, _ string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	out := make([]string, 0, len(f))
	for _, v := range f {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// getJSON does an authenticated GET and decodes it — the shape every identity
// call below has. Written once for the same reason the exchange is.
func getJSON(ctx context.Context, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("identity: provider said %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

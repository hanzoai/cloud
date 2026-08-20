package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
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
// here is a DECLARATION — `spec` below — and `oauthProvider` turns it into the
// Provider the registry wants. The flow has one implementation and one place a
// bug can live.
//
// What a declaration cannot express, it does not have to: `authExtra` adds
// query parameters to the consent URL, and `identify` is a function, so a
// provider whose identity call is genuinely unusual writes that part and
// inherits the rest.

// oauthHTTP bounds every provider call. A real timeout, so a slow or hostile
// provider cannot pin a request goroutine open forever.
var oauthHTTP = &http.Client{Timeout: 15 * time.Second}

// account is what an identity call yields: the non-secret pair that labels a
// connection in the UI. Both are clamped by the caller before they are stored.
type account struct {
	ExternalID string
	Label      string
}

// spec declares one OAuth2 provider. Everything here is data except `identify`,
// which is a function because "who is this" is the one leg providers genuinely
// disagree about — some answer with a flat object, some nest it, some need a
// second call to name the account.
type spec struct {
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

	// env names the ENV vars carrying the deployment's APP credentials. They are
	// operator-injected from KMS; this package never reads KMS for them.
	clientIDEnv     string
	clientSecretEnv string

	// authExtra adds provider-specific query parameters to the consent URL —
	// Google's `access_type=offline`, X's PKCE challenge, and so on.
	authExtra map[string]string

	// basicAuth sends the client credentials as an HTTP Basic header instead of
	// form fields. RFC 6749 §2.3.1 says a server MUST support Basic and MAY
	// support the form; several providers only implement the MUST.
	basicAuth bool

	// identify labels the connection. nil means the provider cannot say who it
	// connected, and the connection is labelled by the provider name alone.
	identify func(ctx context.Context, token string) (account, error)

	// tokenSecret is the KMS secret name the access token is sealed under. It is
	// "access_token" for almost everyone; Slack's bot token is not the user's
	// token, so it says so.
	tokenSecret string

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
func oauthProvider(sp spec) *Provider {
	if sp.scopeSep == "" {
		sp.scopeSep = " "
	}
	if sp.tokenSecret == "" {
		sp.tokenSecret = "access_token"
	}

	creds := func() OAuthConfig {
		return OAuthConfig{
			ClientID:     strings.TrimSpace(os.Getenv(sp.clientIDEnv)),
			ClientSecret: strings.TrimSpace(os.Getenv(sp.clientSecretEnv)),
		}
	}

	return &Provider{
		ID:           sp.id,
		Name:         sp.name,
		Description:  sp.desc,
		Category:     sp.category,
		Scopes:       sp.scopes,
		RedirectPath: callbackPath(sp.id),
		Secrets:      []string{sp.tokenSecret},

		// A card lights up on the PUBLIC client id alone: that is all the consent
		// URL needs, so an org can reach the provider's "Allow" screen as soon as
		// the deployment sets it. The secret is required at the exchange, which
		// validates it against the provider — a missing one yields an honest
		// failure redirect, never a silent dead end.
		Configured: func() bool { return creds().ClientID != "" },
		Creds:      creds,

		Authorize: func(c OAuthConfig, redirectURI, state string) (string, error) {
			q := url.Values{
				"client_id":     {c.ClientID},
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
			return sp.authURL + "?" + q.Encode(), nil
		},

		Exchange: func(ctx context.Context, c OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
			form := url.Values{
				"grant_type":   {"authorization_code"},
				"code":         {code},
				"redirect_uri": {redirectURI},
			}
			if !sp.basicAuth {
				form.Set("client_id", c.ClientID)
				form.Set("client_secret", c.ClientSecret)
			}
			// PKCE: a provider that asked for a challenge must be sent the
			// verifier. The challenge is `plain` (see pkceVerifier), so the two
			// are the same string — which is why this needs no per-request state.
			if v := sp.authExtra["code_challenge"]; v != "" {
				form.Set("code_verifier", v)
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.tokenURL,
				strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
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
				AccessToken string `json:"access_token"`
				Scope       string `json:"scope"`
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

		Revoke: revokeFunc(sp),
	}
}

// revokeFunc builds the disconnect-time revoke, or nil when the provider has no
// revoke endpoint. nil is honest: the registry treats it as "nothing to call",
// and the secret is deleted from KMS either way.
func revokeFunc(sp spec) func(context.Context, OAuthConfig, string) error {
	if sp.revokeURL == "" {
		return nil
	}
	return func(ctx context.Context, c OAuthConfig, token string) error {
		form := url.Values{"token": {token}}
		if !sp.basicAuth {
			form.Set("client_id", c.ClientID)
			form.Set("client_secret", c.ClientSecret)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.revokeURL,
			strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

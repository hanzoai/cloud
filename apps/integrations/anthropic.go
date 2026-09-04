package integrations

// anthropic.go registers the Anthropic USER connector (/v1/integration/connectors plane).
// THREE credential flavours, ONE custody slot:
//
//	API key       (sk-ant-api…)  Verify → static, ExpiresAt 0
//	setup token   (sk-ant-oat01-) Verify → static, ExpiresAt 0
//	Claude Pro/Max subscription   Adopt  → rotating, ExpiresAt set
//
// Upstream acceptance of a setup token is offline-syntactic; the live GET
// /v1/models call here is a deliberate this-port addition required by the
// intake contract — ALWAYS verify-before-store.
//
// All three land in Secrets[0] ("api_key") because the sk-ant-oat01- prefix
// already decides the auth header at use time, and a subscription access token
// carries that same prefix — so the ONE prefix rule covers the OAuth flavour
// with no second branch. The subscription flavour additionally custodies a
// refresh token, and its non-zero ExpiresAt is what arms fresh()'s rotation;
// static flavours keep ExpiresAt 0 and degenerate to a plain custody read.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	anthropicProvider = "anthropic"
	// anthropicKeySecret is the ONE slot both setup tokens (sk-ant-oat01-) and
	// API keys custody under; the prefix decides the auth header at use time.
	anthropicKeySecret   = "api_key"
	anthropicSetupPrefix = "sk-ant-oat01-"
	anthropicSetupMinLen = 80
	anthropicVersion     = "2023-06-01"
	anthropicOAuthBeta   = "oauth-2025-04-20"
	// anthropicOAuthClientID is Claude's PUBLIC subscription client id (Claude
	// Pro/Max sign-in). Public by construction — the flow is PKCE, so there is
	// no client secret and no operator app creds to configure.
	anthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// anthropicAccessTTL is the assumed lifetime of an ADOPTED subscription
	// access token. The bundle carries no expiry and the token is opaque (not a
	// JWT), so intake has no expiry to read — this is the one number here that is
	// assumed rather than observed, and it is deliberately short. It is used ONCE:
	// the first refresh returns a real expires_in and every later expiry is that
	// observed value, so the assumption self-corrects after one rotation.
	anthropicAccessTTL = time.Hour
)

// anthropicOAuthBase reads ANTHROPIC_OAUTH_BASE at call time (httptest client),
// defaulting to the production token origin. NOT anthropicBase(): the token
// endpoint lives on the console origin, not the API origin.
func anthropicOAuthBase() string {
	if v := environ.Or("ANTHROPIC_OAUTH_BASE", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://platform.claude.com"
}

// anthropicBase reads ANTHROPIC_API_BASE at call time (httptest client),
// defaulting to the production API origin.
func anthropicBase() string {
	if v := environ.Or("ANTHROPIC_API_BASE", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.anthropic.com"
}

func init() {
	register(&Provider{
		ID:          anthropicProvider,
		Name:        "Anthropic",
		Description: "Claude via setup token or API key.",
		Category:    "AI",
		Scope:       userScope,
		Secrets:     []string{anthropicKeySecret, refreshSecret},
		Verify:      anthropicVerify,
		Adopt:       anthropicAdopt,
		Refresh:     anthropicRefresh,
	})
}

// anthropicAdopt admits a Claude Pro/Max subscription OAuth bundle obtained by
// a local PKCE login (the claude.ai/oauth/authorize loopback the CLI runs).
//
// Unlike openaiAdopt this does NOT refresh-on-intake. Anthropic rotates the
// refresh token on every refresh and invalidates the prior one, so refreshing
// here would silently kill the operator's still-live local Claude Code session
// the moment they connected. Instead the bundle's own access token is proven
// LIVE against GET /api/oauth/usage — a read, not a rotation — which also
// proves the token carries the user:profile scope the usage plane needs.
//
// Consequence, stated rather than hidden: cloud and the local CLI now share one
// refresh-token lineage, and whichever rotates first wins. That is inherent to
// adopting an existing bundle; an operator who wants an independent lineage
// signs in again so cloud gets its own.
func anthropicAdopt(ctx context.Context, b Bundle) (*ExchangeResult, error) {
	access := strings.TrimSpace(b.Access)
	refresh := strings.TrimSpace(b.Refresh)
	if access == "" || refresh == "" {
		return nil, fmt.Errorf("an anthropic bundle must include both an access and a refresh token")
	}
	expiresAt, err := anthropicProbe(ctx, access)
	if err != nil {
		return nil, err
	}
	return &ExchangeResult{
		Tokens: map[string]string{anthropicKeySecret: access, refreshSecret: refresh},
		// /api/oauth/usage discloses quota, never identity; the caller hint is the
		// only source and "" is acceptable on the user plane.
		ExternalID: strings.TrimSpace(b.Account),
		ExpiresAt:  expiresAt,
	}, nil
}

// anthropicProbe proves an access token is live and subscription-scoped, and
// returns the expiry to arm rotation with. The usage endpoint is the narrowest
// call that proves BOTH facts: /v1/models answers for any credential flavour,
// so it cannot distinguish a subscription token from an API key.
//
// The response carries no expiry, so the returned ExpiresAt is a conservative
// anthropicAccessTTL from now. Being early is harmless — fresh() rotates within
// refreshSkew of it and a rotation is idempotent; being late would serve a dead
// token. Never 0: that would mean "static, never rotate" and strand the
// connector on the first expiry.
func anthropicProbe(ctx context.Context, access string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicBase()+"/api/oauth/usage", nil)
	if err != nil {
		return 0, fmt.Errorf("anthropic probe: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("anthropic-beta", anthropicOAuthBeta)
	req.Header.Set("Accept", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		// Deliberately unwrapped: a transport error can echo the URL but never a
		// header; the message stays token-free regardless.
		return 0, fmt.Errorf("anthropic probe request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, fmt.Errorf("anthropic rejected the subscription token (%d)", resp.StatusCode)
	case resp.StatusCode == http.StatusForbidden:
		// 403 here is specifically a scope failure: the token authenticates but
		// lacks user:profile. A setup token does this — it belongs on the Verify
		// path, not the subscription path.
		return 0, fmt.Errorf("this token lacks the user:profile scope a subscription connector needs")
	default:
		return 0, fmt.Errorf("anthropic probe failed (%d)", resp.StatusCode)
	}
	return time.Now().Add(anthropicAccessTTL).Unix(), nil
}

// anthropicRefresh trades a refresh token for rotated material (Provider.Refresh).
func anthropicRefresh(ctx context.Context, refresh string) (*ExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {anthropicOAuthClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicOAuthBase()+"/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("anthropic token refresh: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic token refresh request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, anthropicOAuthError(resp.StatusCode, resp.Body)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<18)).Decode(&out); err != nil {
		return nil, fmt.Errorf("anthropic token refresh: malformed response")
	}
	access := strings.TrimSpace(out.AccessToken)
	next := strings.TrimSpace(out.RefreshToken)
	// Anthropic ALWAYS rotates. A response missing the rotated refresh token is a
	// FAILURE — never silently keep the old one, which upstream has just
	// invalidated (custody must own the canonical token).
	if access == "" || next == "" {
		return nil, fmt.Errorf("anthropic token refresh: incomplete token response")
	}
	expiresAt := time.Now().Add(anthropicAccessTTL).Unix()
	if out.ExpiresIn > 0 {
		expiresAt = time.Now().Unix() + out.ExpiresIn
	}
	return &ExchangeResult{
		Tokens:    map[string]string{anthropicKeySecret: access, refreshSecret: next},
		ExpiresAt: expiresAt,
	}, nil
}

// anthropicOAuthError extracts a token-free protocol error from a non-2xx token
// response. Bounded read; never echoes request material.
func anthropicOAuthError(status int, body io.Reader) error {
	var e struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(body, 8<<10)).Decode(&e)
	msg := strings.TrimSpace(e.Error)
	if d := strings.TrimSpace(e.Desc); d != "" {
		if msg != "" {
			msg += ": "
		}
		msg += d
	}
	if msg == "" {
		return fmt.Errorf("anthropic token refresh failed (%d)", status)
	}
	return fmt.Errorf("anthropic token refresh failed (%d): %s", status, msg)
}

// anthropicVerify validates a setup token or API key LIVE against /v1/models
// and returns it for custody. FAIL CLOSED throughout: any non-200 stores
// nothing, and no error ever contains the credential value.
func anthropicVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	// Strip ALL whitespace (setup tokens are often pasted with line wraps) —
	// upstream normalizeAnthropicSetupTokenInput parity.
	tok := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, in.Token)
	if tok == "" {
		return nil, fmt.Errorf("empty credential")
	}
	setup := strings.HasPrefix(tok, anthropicSetupPrefix)
	if setup && len(tok) < anthropicSetupMinLen {
		// OFFLINE reject: a syntactically impossible setup token never reaches the
		// network (tests assert zero requests).
		return nil, fmt.Errorf("setup token too short")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicBase()+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic verify: build request: %w", err)
	}
	if setup {
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("anthropic-beta", anthropicOAuthBeta)
	} else {
		req.Header.Set("x-api-key", tok)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		// Deliberately unwrapped: a transport error can echo the URL but never a
		// header; the message stays token-free regardless.
		return nil, fmt.Errorf("anthropic verify request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain (bounded) so the connection can be reused; the content is unused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("anthropic rejected the credential (%d)", resp.StatusCode)
	default:
		return nil, fmt.Errorf("anthropic verify failed (%d)", resp.StatusCode)
	}
	return &ExchangeResult{
		Tokens: map[string]string{anthropicKeySecret: tok},
		// /v1/models discloses no account identity; the caller hint is the only
		// source and "" is acceptable on the user plane (no ResolveOrgByExternalID
		// routing here).
		ExternalID: strings.TrimSpace(in.AccountID),
	}, nil
}

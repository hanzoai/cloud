package integrations

// openai.go registers the OpenAI ChatGPT/Codex USER connector (/v1/connectors
// plane): device-code sign-in, oauth-bundle adoption (CLI local PKCE), and
// refresh-token rotation. Protocol facts come from the Codex device flow:
// usercode POST /api/accounts/deviceauth/usercode; poll POST
// /api/accounts/deviceauth/token (pending is HTTP 403/404 — this protocol has
// no RFC-8628 error strings and no slow_down); success returns
// authorization_code + a SERVER-SIDE code_verifier; exchange/refresh both POST
// /oauth/token. The refresh endpoint always rotates: access_token,
// refresh_token, and expires_in are all mandatory in its response. The public
// client id is baked in — no operator app creds exist for this flow.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	openaiProvider  = "openai"
	openaiClientID  = "app_EMoamEEZ73f0CkXaXp7hrann" // Codex public client id
	openaiDeviceTTL = 15 * time.Minute               // overall device-flow deadline
)

// openaiBase reads OPENAI_AUTH_BASE at call time (httptest seam), defaulting
// to the production auth origin.
func openaiBase() string {
	if v := strings.TrimSpace(os.Getenv("OPENAI_AUTH_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://auth.openai.com"
}

func init() {
	register(&Provider{
		ID:          openaiProvider,
		Name:        "OpenAI",
		Description: "ChatGPT/Codex account via device code or CLI sign-in.",
		Category:    "AI",
		Scope:       userScope,
		Secrets:     []string{accessSecret, refreshSecret},
		Device:      &Device{Start: openaiStart, Poll: openaiPoll},
		Adopt:       openaiAdopt,
		Refresh:     openaiRefresh,
	})
}

// openaiStart begins a device authorization: POST /api/accounts/deviceauth/usercode.
func openaiStart(ctx context.Context) (*DeviceStart, error) {
	body, err := json.Marshal(map[string]string{"client_id": openaiClientID})
	if err != nil {
		return nil, fmt.Errorf("openai device start: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiBase()+"/api/accounts/deviceauth/usercode", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai device start: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		// Deliberately unwrapped: a transport error can echo the URL but never a
		// credential; the message stays token-free regardless.
		return nil, fmt.Errorf("openai device start request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("openai device sign-in is not enabled")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, openaiError("device start", resp.StatusCode, resp.Body)
	}
	var out struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		UserCodeAlt  string `json:"usercode"` // wire fallback key for user_code
		Interval     int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<18)).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai device start: malformed response")
	}
	userCode := strings.TrimSpace(out.UserCode)
	if userCode == "" {
		userCode = strings.TrimSpace(out.UserCodeAlt)
	}
	deviceAuthID := strings.TrimSpace(out.DeviceAuthID)
	if deviceAuthID == "" || userCode == "" {
		return nil, fmt.Errorf("openai device start: missing device handle")
	}
	return &DeviceStart{
		Code:     deviceAuthID,
		UserCode: userCode,
		// Constructed: the usercode response carries no verification URL.
		VerifyURL: openaiBase() + "/codex/device",
		Interval:  out.Interval, // raw wire value — begin() is the sole normalizer
		ExpiresAt: time.Now().Add(openaiDeviceTTL).Unix(),
	}, nil
}

// openaiPoll checks a pending device authorization: POST
// /api/accounts/deviceauth/token. On success it immediately exchanges the
// returned authorization_code — that exchange IS the live proof the Device
// contract requires for a pollDone Result.
func openaiPoll(ctx context.Context, g Grant) (*DevicePoll, error) {
	body, err := json.Marshal(map[string]string{"device_auth_id": g.Code, "user_code": g.UserCode})
	if err != nil {
		return nil, fmt.Errorf("openai device poll: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiBase()+"/api/accounts/deviceauth/token", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai device poll: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai device poll request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// Pending is expressed by STATUS CODE in this protocol (403/404) — there are
	// no RFC-8628 error strings and no slow_down.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return &DevicePoll{Status: pollPending}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, openaiError("device poll", resp.StatusCode, resp.Body)
	}
	var out struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"` // server-side PKCE: the verifier comes back FROM the server
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<18)).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai device poll: malformed response")
	}
	code := strings.TrimSpace(out.AuthorizationCode)
	verifier := strings.TrimSpace(out.CodeVerifier)
	if code == "" || verifier == "" {
		return nil, fmt.Errorf("openai device poll: incomplete authorization")
	}
	res, err := openaiExchange(ctx, code, verifier)
	if err != nil {
		return nil, err
	}
	return &DevicePoll{Status: pollDone, Result: res}, nil
}

// openaiExchange trades the device authorization_code (+ server-issued PKCE
// verifier) for the token pair.
func openaiExchange(ctx context.Context, code, verifier string) (*ExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {openaiBase() + "/deviceauth/callback"},
		"client_id":     {openaiClientID},
		"code_verifier": {verifier},
	}
	access, refresh, expiresIn, err := openaiToken(ctx, "token exchange", form)
	if err != nil {
		return nil, err
	}
	if access == "" || refresh == "" {
		return nil, fmt.Errorf("openai token exchange: incomplete token response")
	}
	return openaiResult(access, refresh, openaiExpiry(access, expiresIn)), nil
}

// openaiRefresh trades a refresh token for rotated material (Provider.Refresh).
func openaiRefresh(ctx context.Context, refresh string) (*ExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {openaiClientID},
		// NO scope, NO redirect_uri — the refresh grant carries neither.
	}
	access, next, expiresIn, err := openaiToken(ctx, "token refresh", form)
	if err != nil {
		return nil, err
	}
	// The server ALWAYS rotates: access_token, refresh_token, and expires_in are
	// all mandatory. A response missing the rotated refresh token is a FAILURE —
	// never silently keep the old one (custody must own the canonical token).
	if access == "" || next == "" || expiresIn <= 0 {
		return nil, fmt.Errorf("openai token refresh: incomplete token response")
	}
	return openaiResult(access, next, openaiExpiry(access, expiresIn)), nil
}

// openaiAdopt admits an externally obtained OAuth bundle (CLI local PKCE) by
// performing ONE refresh: that call is both the live proof and a rotation, so
// cloud custody owns the canonical refresh token from the first moment
// (token-sink doctrine; refresh tokens are never returned to clients — /token
// serves access material only).
func openaiAdopt(ctx context.Context, b Bundle) (*ExchangeResult, error) {
	refresh := strings.TrimSpace(b.Refresh)
	if refresh == "" {
		return nil, fmt.Errorf("an openai bundle must include a refresh token")
	}
	res, err := openaiRefresh(ctx, refresh)
	if err != nil {
		return nil, err
	}
	// b.Access is intentionally unused — identity is re-extracted from the
	// refreshed access JWT; b.Account is only the ExternalID fallback when the
	// JWT lacks the account claim.
	if res.ExternalID == "" {
		res.ExternalID = strings.TrimSpace(b.Account)
	}
	return res, nil
}

// openaiToken is the ONE /oauth/token call shape (exchange + refresh share it).
func openaiToken(ctx context.Context, op string, form url.Values) (access, refresh string, expiresIn int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiBase()+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, fmt.Errorf("openai %s: build request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("openai %s request failed", op)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", 0, openaiError(op, resp.StatusCode, resp.Body)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<18)).Decode(&out); err != nil {
		return "", "", 0, fmt.Errorf("openai %s: malformed response", op)
	}
	return strings.TrimSpace(out.AccessToken), strings.TrimSpace(out.RefreshToken), out.ExpiresIn, nil
}

// openaiExpiry is the ONE expiry precedence: expires_in > 0 → now+expires_in;
// else the access JWT's exp claim; else 0 (unknown lifetime — never "now",
// which would force a refresh on every /token read).
func openaiExpiry(access string, expiresIn int64) int64 {
	if expiresIn > 0 {
		return time.Now().Unix() + expiresIn
	}
	_, _, exp := openaiIdentity(access)
	return exp
}

// openaiResult builds the ExchangeResult every openai intake path returns.
func openaiResult(access, refresh string, expiresAt int64) *ExchangeResult {
	account, email, _ := openaiIdentity(access)
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: access, refreshSecret: refresh},
		ExternalID:   account,
		AccountLabel: email,
		ExpiresAt:    expiresAt,
	}
}

// openaiIdentity best-effort decodes the access JWT payload for account id,
// email, and exp. No signature check — this is identity METADATA, not authz.
// Malformed input yields zero values, never an error, and never a log line
// (the token must not leak through diagnostics).
func openaiIdentity(access string) (account, email string, exp int64) {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return "", "", 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", 0
	}
	var claims struct {
		Exp  float64 `json:"exp"` // numeric claim: float-tolerant, truncated to unix seconds
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		Profile struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", 0
	}
	return strings.TrimSpace(claims.Auth.ChatGPTAccountID), strings.TrimSpace(claims.Profile.Email), int64(claims.Exp)
}

// openaiError extracts a token-free protocol error ({error, error_description})
// from a non-2xx response body. Bounded read; never echoes request material.
func openaiError(op string, status int, body io.Reader) error {
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
		return fmt.Errorf("openai %s failed (%d)", op, status)
	}
	return fmt.Errorf("openai %s failed (%d): %s", op, status, msg)
}

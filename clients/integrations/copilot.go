package integrations

// copilot.go registers the GitHub Copilot USER connector (/v1/connectors
// plane) under the ecosystem-canonical id "github-copilot" (distinct from the
// org "github" provider). Device flow per RFC 8628 against github.com with the
// full error-string protocol (authorization_pending / slow_down /
// expired_token / access_denied); every pollDone and token intake is
// live-proven by minting a Copilot token via GET /copilot_internal/v2/token.
// The LONG-LIVED GitHub OAuth token is what enters custody; the minted
// short-lived Copilot token is a runtime derivative and is never stored. No
// refresh token exists in this flow, so ExpiresAt stays 0.

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

const (
	copilotProvider = "github-copilot"
	copilotClientID = "Iv1.b507a08c87ecfe98"
	copilotScope    = "read:user"
	copilotSlowStep = 5 // slow_down: +5 s when the response carries no interval
)

// copilotBase reads COPILOT_GITHUB_BASE at call time (httptest seam),
// defaulting to the login host.
func copilotBase() string {
	if v := strings.TrimSpace(os.Getenv("COPILOT_GITHUB_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://github.com"
}

// copilotAPI reads COPILOT_API_BASE at call time (httptest seam), defaulting
// to the API host the Copilot verify lives on.
func copilotAPI() string {
	if v := strings.TrimSpace(os.Getenv("COPILOT_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

func init() {
	register(&Provider{
		ID:          copilotProvider,
		Name:        "GitHub Copilot",
		Description: "Copilot via GitHub device sign-in.",
		Category:    "AI",
		Scope:       userScope,
		Secrets:     []string{accessSecret},
		Device:      &Device{Start: copilotStart, Poll: copilotPoll},
		// Verify doubles as token intake: paste an existing gh token, and
		// verify-before-store still holds (copilotToken must prove Copilot access).
		Verify: copilotVerify,
	})
}

// copilotStart begins a device authorization: POST /login/device/code.
func copilotStart(ctx context.Context) (*DeviceStart, error) {
	form := url.Values{"client_id": {copilotClientID}, "scope": {copilotScope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, copilotBase()+"/login/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github device start: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	if err != nil {
		// Deliberately unwrapped: a transport error can echo the URL but never a
		// credential; the message stays token-free regardless.
		return nil, fmt.Errorf("github device start request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("github device start failed (%d)", resp.StatusCode)
	}
	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		ExpiresIn  int64  `json:"expires_in"`
		Interval   int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return nil, fmt.Errorf("github device start: malformed response")
	}
	deviceCode := strings.TrimSpace(out.DeviceCode)
	userCode := strings.TrimSpace(out.UserCode)
	if deviceCode == "" || userCode == "" || len(userCode) > 64 {
		return nil, fmt.Errorf("github device start: malformed device handle")
	}
	return &DeviceStart{
		Code:     deviceCode,
		UserCode: userCode,
		// ALWAYS the constructed canonical URL: upstream validates the response's
		// verification_uri then REPLACES it with this canonical value anyway —
		// constructing is the same outcome with one path (and a trivial httptest seam).
		VerifyURL: copilotBase() + "/login/device",
		Interval:  out.Interval, // raw wire value — begin() is the sole normalizer
		ExpiresAt: time.Now().Unix() + out.ExpiresIn,
	}, nil
}

// copilotPoll checks a pending device authorization: POST
// /login/oauth/access_token. A returned GitHub token is only pollDone after
// copilotToken proves Copilot access — that verify IS the live proof the
// Device contract requires.
func copilotPoll(ctx context.Context, g Grant) (*DevicePoll, error) {
	form := url.Values{
		"client_id":   {copilotClientID},
		"device_code": {g.Code},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, copilotBase()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github device poll: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github device poll request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Interval    int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return nil, fmt.Errorf("github device poll: malformed response")
	}
	// RFC-8628 error-string protocol (this flow, unlike OpenAI's, has all four).
	switch out.Error {
	case "authorization_pending":
		return &DevicePoll{Status: pollPending}, nil
	case "slow_down":
		iv := out.Interval
		if iv <= 0 {
			iv = g.Interval + copilotSlowStep
		}
		return &DevicePoll{Status: pollSlow, Interval: iv}, nil
	case "expired_token":
		return &DevicePoll{Status: pollExpired}, nil
	case "access_denied":
		return &DevicePoll{Status: pollDenied}, nil
	case "":
	default:
		return nil, fmt.Errorf("github device poll failed: %s", out.Error)
	}
	tok := strings.TrimSpace(out.AccessToken)
	if tok == "" {
		return nil, fmt.Errorf("github device poll: empty token response")
	}
	ok, err := copilotToken(ctx, tok)
	if err != nil {
		return nil, err // RETRYABLE (transport/5xx): grant intact, client retries
	}
	if !ok {
		// GitHub auth succeeded but the account has no Copilot access/seat — terminal.
		return &DevicePoll{Status: pollDenied}, nil
	}
	return &DevicePoll{
		Status: pollDone,
		Result: &ExchangeResult{Tokens: map[string]string{accessSecret: tok}, ExpiresAt: 0},
	}, nil
}

// copilotVerify is the token intake seam: an existing GitHub token is admitted
// only after copilotToken proves Copilot access (verify-before-store).
func copilotVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	tok := strings.TrimSpace(in.Token)
	if tok == "" {
		return nil, fmt.Errorf("empty token")
	}
	ok, err := copilotToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("github token has no copilot access")
	}
	return &ExchangeResult{Tokens: map[string]string{accessSecret: tok}, ExpiresAt: 0}, nil
}

// copilotToken mints a Copilot session token from a GitHub token: GET
// /copilot_internal/v2/token. ok=false is a DEFINITIVE refusal (401/403/404 or
// an empty mint: authed but no Copilot access); err is retryable (transport /
// other status). The minted short-lived Copilot token is NOT custodied — it is
// a runtime derivative; only the long-lived GitHub token enters custody.
func copilotToken(ctx context.Context, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotAPI()+"/copilot_internal/v2/token", nil)
	if err != nil {
		return false, fmt.Errorf("copilot verify: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Editor-Version", "vscode/1.107.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.35.0")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
	resp, err := authClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("copilot verify request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("copilot verify failed (%d)", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return false, fmt.Errorf("copilot verify: malformed response")
	}
	return strings.TrimSpace(out.Token) != "", nil
}

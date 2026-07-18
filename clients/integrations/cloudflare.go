package integrations

// cloudflare.go registers Cloudflare as an APIKEY connector on the integrations
// plane. The customer supplies a Cloudflare scoped API token — created
// least-privilege (DNS edit, zone read, Pages edit) — which is VERIFIED live via
// GET /user/tokens/verify (must be status:active) BEFORE it is sealed into the
// org's KMS namespace (/orgs/{org}/integrations/cloudflare/api_token). The store
// row holds only non-secret metadata (account id/label, the requested scopes);
// the token is never in the row, the response, or a log line.
//
// Cloudflare has no server-side OAuth-app requirement for this path (the customer
// already holds the token), so the connector is always "configured". Cloudflare
// DID ship self-managed OAuth clients on 2026-06-03; a `kind:oauth` Cloudflare
// provider can be added later as a sibling file (a registered Hanzo OAuth client +
// domain verification) without touching this apikey path — one framework, N
// providers. See the connector HIP / hanzo dns wiring.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// cloudflareProvider is the :provider slug.
	cloudflareProvider = "cloudflare"
	// cloudflareTokenSecret is the KMS secret name the token is sealed under, at
	// /orgs/{org}/integrations/cloudflare/{name}.
	cloudflareTokenSecret = "api_token"
)

// cloudflareScopes is the least-privilege permission set a Hanzo Cloudflare token
// is expected to carry. Cloudflare's verify response does NOT disclose a token's
// scopes (a least-privilege token cannot read its own token object), so this is
// recorded as the REQUESTED/expected set — connection metadata, not a read-back.
var cloudflareScopes = []string{"Zone:DNS:Edit", "Zone:Read", "Account:Cloudflare Pages:Edit"}

// cfHTTPClient is the shared client for Cloudflare verify/discovery calls. A tight
// timeout so a slow/hung Cloudflare never wedges a connect request.
var cfHTTPClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Provider{
		ID:          cloudflareProvider,
		Name:        "Cloudflare",
		Description: "DNS, zones, and Pages via a least-privilege scoped API token.",
		Category:    "Infrastructure",
		Kind:        apiKeyKind,
		AdminOnly:   true,
		Scopes:      cloudflareScopes,
		Secrets:     []string{cloudflareTokenSecret},
		// apikey: no OAuth callback, no server-side app creds. Always available;
		// the only secret is the customer's token, custodied in KMS.
		Configured: func() bool { return true },
		Creds:      func() OAuthConfig { return OAuthConfig{} },
		Verify:     cloudflareVerify,
	})
}

// cfAPIBase is Cloudflare's API v4 origin. Overridable via CLOUDFLARE_API_BASE for
// tests (an httptest server) and CF-compatible endpoints; read at call time so a
// test can set it per-process. The default is the real Cloudflare API.
func cfAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

// cfEnvelope is the shared Cloudflare API v4 response envelope shape.
type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// cfVerifyResult is GET /user/tokens/verify's result. It carries the token id +
// status only — NOT the token name or scopes.
type cfVerifyResult struct {
	cfEnvelope
	Result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"result"`
}

// cfAccountsResult is GET /accounts's result (best-effort account discovery).
type cfAccountsResult struct {
	cfEnvelope
	Result []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"result"`
}

// cloudflareVerify validates a Cloudflare scoped API token and returns the token
// to seal + non-secret account metadata. It FAILS CLOSED: a transport error, a
// non-2xx, an unsuccessful envelope, or a non-"active" status yields an error, so
// connect stores nothing. The returned error NEVER contains the token value.
// accountId/label are best-effort (a least-privilege token may be unable to list
// accounts); the caller-supplied AccountID hint wins when present.
func cloudflareVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	token := strings.TrimSpace(in.Token)
	if token == "" {
		return nil, fmt.Errorf("empty token")
	}
	status, tokenID, err := cfTokenStatus(ctx, token)
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, fmt.Errorf("cloudflare token is not active (status %q)", status)
	}
	accountID, accountLabel := cfDiscoverAccount(ctx, token)
	if hint := strings.TrimSpace(in.AccountID); hint != "" {
		accountID = hint
	}
	externalID := accountID
	if externalID == "" {
		// No account disclosed and no hint: use the token id as the stable external
		// key so the row/ResolveOrgByExternalID has a non-empty, non-colliding value.
		externalID = tokenID
	}
	return &ExchangeResult{
		Tokens:       map[string]string{cloudflareTokenSecret: token},
		ExternalID:   externalID,
		AccountLabel: accountLabel,
		Scopes:       cloudflareScopes,
	}, nil
}

// cfTokenStatus calls GET /user/tokens/verify and returns the lower-cased status +
// token id. The token rides ONLY the Authorization header — never a query or log.
func cfTokenStatus(ctx context.Context, token string) (status, tokenID string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfAPIBase()+"/user/tokens/verify", nil)
	if err != nil {
		return "", "", fmt.Errorf("cloudflare verify: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		// Do NOT wrap err: a transport error can echo the request URL but never the
		// header, and we keep the message token-free regardless.
		return "", "", fmt.Errorf("cloudflare verify request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", fmt.Errorf("cloudflare rejected the token (%d)", resp.StatusCode)
	}
	var vr cfVerifyResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&vr); err != nil {
		return "", "", fmt.Errorf("cloudflare verify: malformed response")
	}
	if !vr.Success {
		return "", "", fmt.Errorf("cloudflare verify unsuccessful")
	}
	return strings.ToLower(strings.TrimSpace(vr.Result.Status)), strings.TrimSpace(vr.Result.ID), nil
}

// cfDiscoverAccount best-effort resolves the first account the token can see. A
// least-privilege token may be forbidden here; that is NOT fatal (returns "","").
func cfDiscoverAccount(ctx context.Context, token string) (id, name string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfAPIBase()+"/accounts?per_page=1", nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var ar cfAccountsResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&ar); err != nil || !ar.Success || len(ar.Result) == 0 {
		return "", ""
	}
	return strings.TrimSpace(ar.Result[0].ID), strings.TrimSpace(ar.Result[0].Name)
}

package integrations

// oauth_http.go is the ONE bounded, token-free HTTP transport the marketing/ad
// OAuth providers (meta/microsoft/tiktok/reddit/linkedin) share for their token
// exchange and account-label calls — the sibling of keyverify.go's shared verify
// transport. It decomplects the HTTP dance (bounded body, JSON decode, honest
// error) from each provider's DATA (endpoints, scopes, param shapes), so a new ad
// connector is a thin Authorize/Exchange over these helpers, never another
// hand-rolled client.
//
// Every helper is token-free BY CONSTRUCTION: credentials ride the POST form body,
// a JSON body, or an Authorization header — NEVER the URL query — so a wrapped
// transport error (*url.Error, which echoes the URL) can never leak a secret, and
// the response-body excerpt in an error is the provider's own OAuth error
// (invalid_grant, …), never our token. The provider slug is the only identifying
// text in an error.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// oauthHTTP is the shared client for every marketing/ad OAuth call. A tight
// timeout so a slow/hung provider never wedges a connect or callback request.
var oauthHTTP = &http.Client{Timeout: 20 * time.Second}

// maxOAuthBody bounds every provider response read, so a hostile/oversized body
// can never exhaust memory during a token exchange or account fetch.
const maxOAuthBody = 1 << 20 // 1 MiB

// oauthPostForm POSTs an application/x-www-form-urlencoded body and decodes the
// JSON response into out. headers carries per-provider extras (Authorization:
// Basic for reddit, User-Agent, Accept). The credential rides the form body, never
// the URL — wrapping the transport error is safe.
func oauthPostForm(ctx context.Context, provider, endpoint string, headers map[string]string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%s request: build", provider)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return oauthDo(provider, req, headers, out)
}

// oauthPostJSON POSTs a JSON body and decodes the JSON response into out (TikTok's
// token exchange, whose credential and auth_code ride the JSON body).
func oauthPostJSON(ctx context.Context, provider, endpoint string, headers map[string]string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s request: encode", provider)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("%s request: build", provider)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return oauthDo(provider, req, headers, out)
}

// oauthGetJSON GETs with headers (Authorization: Bearer for account fetches) and
// decodes the JSON response into out. The access token rides the Authorization
// header, never the URL. Best-effort callers ignore the error and keep an empty
// label; the connection is not failed by a userinfo miss.
func oauthGetJSON(ctx context.Context, provider, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%s request: build", provider)
	}
	req.Header.Set("Accept", "application/json")
	return oauthDo(provider, req, headers, out)
}

// oauthDelete issues a DELETE with headers (best-effort provider-side revoke). The
// credential rides the Authorization header. A non-2xx is an honest token-free
// error; callers treat any error as non-fatal.
func oauthDelete(ctx context.Context, provider, endpoint string, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%s request: build", provider)
	}
	return oauthDo(provider, req, headers, nil)
}

// oauthDo executes req, enforces a bounded read, maps a non-2xx to an honest
// token-free error (with a truncated provider-error excerpt), and decodes a
// non-empty 2xx body into out. An empty 2xx body is a success with nothing to
// decode (some revoke endpoints).
func oauthDo(provider string, req *http.Request, headers map[string]string, out any) error {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s call failed", provider)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if err != nil {
		return fmt.Errorf("%s read failed", provider)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s http %d: %s", provider, resp.StatusCode, truncateBody(body))
	}
	if len(body) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s decode failed", provider)
	}
	return nil
}

// basicAuth builds an HTTP Basic Authorization header value from a confidential
// client's id + secret (reddit's token exchange authenticates the client this way).
func basicAuth(id, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
}

// jwtClaims best-effort decodes the claims of an UNVERIFIED JWT (an OIDC id_token)
// purely to derive a human account label — it is NOT used for authentication (the
// access token is the credential; the id_token is decorative metadata). A malformed
// token yields a nil map, never an error or a panic. The claims are sanitized by
// sanitizeResult before they are stored or logged.
func jwtClaims(idToken string) map[string]any {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some issuers pad; tolerate standard base64url with padding too.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	if len(payload) > maxOAuthBody {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

// claimString reads a string claim across the keys an issuer might use, returning
// the first non-empty match ("" if none). Used to pull an email/subject label from
// id_token claims without hard-coding one issuer's naming.
func claimString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := claims[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

package integrations

// verifypost.go is the shared transport for the small class of connectors whose
// accept/reject decision rides the RESPONSE BODY of a 200, not the HTTP status:
// Authorize.Net (authenticateTest → messages.resultCode) and reCAPTCHA (siteverify
// → error-codes) both answer 200 for a rejected credential. keyVerify (the dominant
// status-only mechanism) cannot express that, so these providers keep a bespoke
// Verify — parity with the pre-keyVerify anthropicVerify — but they SHARE this one
// transport rather than each hand-rolling an HTTP client.
//
// It reuses authClient (device.go): a 30s budget and, critically, NO redirect
// following — a provider answering bad auth with 302→200 fails closed here exactly
// as it does for keyVerify. The credential rides the request BODY (form/JSON), never
// the URL, so a wrapped transport error can never echo it; every error is a
// token-free literal naming only the provider.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// verifyPost POSTs body (contentType) to endpoint and returns the bounded 2xx
// response body for the caller to inspect. A non-2xx (including a surfaced 3xx) is a
// token-free fail-closed error; a leading UTF-8 BOM is stripped (Authorize.Net emits
// one) so the caller's JSON decode always sees clean bytes.
func verifyPost(ctx context.Context, provider, endpoint, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s verify: build request", provider)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s verify request failed", provider)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("%s verify: read failed", provider)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s verify failed (%d)", provider, resp.StatusCode)
	}
	// Strip a leading UTF-8 BOM (Authorize.Net's JSON response carries one).
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}), nil
}

// splitPair splits a "<a>:<b>" credential into its two halves, trimming each. Both
// halves must be non-empty (a two-part credential — Authorize.Net's API-login +
// transaction-key — supplied as one string). The error never echoes the value.
func splitPair(cred, provider, aName, bName string) (a, b string, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(cred), ":")
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if !ok || a == "" || b == "" {
		return "", "", fmt.Errorf("%s credential must be %s:%s", provider, aName, bName)
	}
	return a, b, nil
}

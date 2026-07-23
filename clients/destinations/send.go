package destinations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// send.go is the ONE bounded, credential-safe HTTP transport every adapter shares to
// deliver a translated batch — the sibling of the analytics ingest transport. It
// decomplects the HTTP dance (bounded body, JSON decode, honest error) from each
// adapter's DATA (endpoint, payload, header auth). Credentials ride the request body
// or an Authorization/Access-Token header, never the URL — the sole exception being
// GA4's Measurement Protocol, whose api_secret Google's protocol mandates as a query
// param; a transport error is therefore returned platform-tagged WITHOUT the wrapped
// *url.Error (which echoes the URL), so a secret can never leak into a log line.

// sendHTTP is the shared client. A tight timeout so a slow/hung platform never wedges
// the fan-out; each Send is additionally deadline-bounded by the caller's context.
var sendHTTP = &http.Client{Timeout: 15 * time.Second}

// maxSendRespBody bounds every platform response read so a hostile/oversized body
// can never exhaust memory.
const maxSendRespBody = 1 << 20 // 1 MiB

// postJSON POSTs a JSON body to endpoint with headers and decodes a non-empty 2xx
// JSON body into out (nil out ⇒ the body is discarded). A non-2xx is an honest,
// credential-free error carrying a bounded provider-error excerpt (the platform's own
// error text, never our secret). platform is the only identifying text in a transport
// error. The credential rides headers or the body, never the endpoint (except GA4 MP,
// whose caller keeps the endpoint out of every error string).
func postJSON(ctx context.Context, platform, endpoint string, headers map[string]string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encode request", platform)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("%s: build request", platform)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := sendHTTP.Do(req)
	if err != nil {
		// NEVER wrap err: *url.Error echoes the endpoint (which may carry a
		// query-string credential for GA4 MP). Platform-tagged, secret-free.
		return fmt.Errorf("%s: request failed", platform)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSendRespBody))
	if err != nil {
		return fmt.Errorf("%s: read response", platform)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: http %d: %s", platform, resp.StatusCode, truncate(raw, 512))
	}
	if len(raw) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode response", platform)
	}
	return nil
}

// truncate bounds a provider-error excerpt to n bytes (repairing a byte-boundary cut
// to valid UTF-8) so an oversized upstream error never bloats a log line.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:n]), "")
}

// ── PII normalization + hashing (advanced matching) ──────────────────────────

// hashEmail normalizes an email (trim + lower-case) then SHA-256-hex-encodes it —
// the shared normalization every platform's advanced matching mandates. "" in ⇒ ""
// out (an absent match key is omitted, never hashed to a constant).
func hashEmail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return sha256hex(s)
}

// hashPhone normalizes a phone (strip everything but digits) then SHA-256-hex-encodes
// it. Callers should include the country code; we do not synthesize one.
func hashPhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return sha256hex(b.String())
}

// sha256hex lower-cases nothing further (caller normalized) and hex-encodes the
// SHA-256 of s. "" in ⇒ "" out.
func sha256hex(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

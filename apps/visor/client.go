// client.go is the ONE HTTP path from this subsystem to Visor (the cloud OS at
// visor.hanzo.svc:19000 that owns compute — machines and DOKS node pools). Every
// handler in visor.go routes through this client, so the wire contract (base URL,
// auth, the casibase {status,msg,data} envelope, error mapping) lives once here
// and can never drift between six hand-rolled fetches.
//
// AUTH (one rule): a request carries a Visor identity that is EITHER the service
// credential (VISOR_CLIENT_ID + VISOR_CLIENT_SECRET, KMS-sourced, sent as Basic
// auth so Visor's ApiFilter authorizes cloud as the `app/<visorApp>` subject) OR
// the caller's forwarded Authorization bearer when no service credential is
// configured. The tenant is ALWAYS pinned by ?owner=<org> (the validated
// principal's org, never a client field) plus the forwarded identity headers, so
// Visor scopes to exactly the caller's tenant on both paths.
//
// ENVELOPE: Visor (casibase) returns HTTP 200 with {status:"ok"|"error", msg,
// data}. A logical failure is status:"error" at HTTP 200, NOT a 4xx/5xx — so a
// bare status-code check would read an error as success. call() inspects the
// status field and surfaces msg as an honest error; it never fabricates data.
package visor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// identityHeaders are forwarded to Visor so it sees the SAME gateway-minted
// tenant context cloud validated (already sanitized + re-injected by
// middleware_identity upstream). This INCLUDES the org sub-scopes X-Project-Id /
// X-App-Id: SanitizeIdentity has already REFUSED any cross-org project claim and
// dropped scope on the anonymous path, so what reaches here is trustworthy —
// forwarding it lets Visor attribute compute (compute_usage.app/project) to the
// caller's OWN project, never another tenant's. Authorization is forwarded only
// when no service credential is configured (see authorize).
var identityHeaders = []string{"X-Org-Id", "X-User-Id", "X-User-Email", "X-Project-Id", "X-App-Id"}

// defaultBase is the in-cluster Visor Service (Beego, httpport 19000). Overridable
// by VISOR_URL for other environments and for tests (an httptest.Server URL).
const defaultBase = "http://visor.hanzo.svc:19000"

// maxBody bounds an upstream response read — Visor compute payloads are small
// inventories, not blobs; the cap stops a pathological upstream from ballooning
// memory.
const maxBody = 8 << 20

func visorBase() string {
	if v := strings.TrimSpace(os.Getenv("VISOR_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultBase
}

func serviceClientID() string     { return strings.TrimSpace(os.Getenv("VISOR_CLIENT_ID")) }
func serviceClientSecret() string { return strings.TrimSpace(os.Getenv("VISOR_CLIENT_SECRET")) }

// client is the tenant-scoped Visor HTTP client. target has no trailing slash.
type client struct {
	target string
	cc     *http.Client
}

func newClient() *client {
	return &client{target: visorBase(), cc: &http.Client{Timeout: 30 * time.Second}}
}

// envelope is Visor's casibase response wrapper. Data is deferred so call() can
// decode it into the caller's concrete type only after confirming status:"ok".
type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// call issues one request to Visor and unwraps the envelope into out (a pointer),
// which may be nil when the caller only needs success/failure. query is appended
// as-is; body (non-nil) is JSON-encoded. The caller's zip.Ctx supplies the
// forwarded identity + the request context for cancellation.
//
// Error mapping is honest and customer-appropriate: an unreachable Visor → 502,
// a non-2xx HTTP status → that status, and a status:"error" envelope → 502 with
// Visor's msg (a logical upstream failure, never masked as success).
func (cl *client) call(c *zip.Ctx, method, path, query string, body any, out any) error {
	u := cl.target + path
	if query != "" {
		u += "?" + query
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "visor: encode request: %v", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(c.Context(), method, u, rdr)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "visor: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	authorize(req, c)
	for _, h := range identityHeaders {
		if v := c.Header(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	resp, err := cl.cc.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "visor: unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zip.Errorf(resp.StatusCode, "visor: upstream %d: %s", resp.StatusCode, snippet(raw))
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return zip.Errorf(http.StatusBadGateway, "visor: decode envelope: %v", err)
	}
	if env.Status != "" && env.Status != "ok" {
		return zip.Errorf(http.StatusBadGateway, "visor: %s", firstNonEmpty(env.Msg, "upstream error"))
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return zip.Errorf(http.StatusBadGateway, "visor: decode data: %v", err)
		}
	}
	return nil
}

// authorize attaches the Visor identity to req per the one-rule model: the
// configured service credential (Basic auth) wins; otherwise the caller's bearer
// is forwarded. The credential is never logged.
func authorize(req *http.Request, c *zip.Ctx) {
	if id, secret := serviceClientID(), serviceClientSecret(); id != "" && secret != "" {
		req.SetBasicAuth(id, secret)
		return
	}
	if a := c.Header("Authorization"); a != "" {
		req.Header.Set("Authorization", a)
	}
}

// q builds a URL query string from ordered key/value pairs, skipping empty
// values, so no handler hand-concatenates a query.
func q(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	return v.Encode()
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

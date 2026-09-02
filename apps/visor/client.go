// client.go is the ONE HTTP path from this subsystem to Visor (the cloud OS at
// visor.hanzo.svc:19000 that owns compute — machines and DOKS node pools). Every
// handler in visor.go routes through this client, so the wire contract (base URL,
// auth, the {status,msg,data} envelope, error mapping) lives once here
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
// TWO WIRES, and a call site says which, because they cannot be told apart by
// looking:
//
//	cl.call  LEGACY. Visor returns HTTP 200 with {status:"ok"|"error",
//	         msg, data}; a logical failure is status:"error" at HTTP 200, NOT a
//	         4xx/5xx, so a bare status-code check would read an error as success.
//	cl.op    TYPED (zip.Get/Put/Delete[In,Out]). The answer IS the value, the
//	         status IS the outcome: 204 for a void result, 404 for a miss, and
//	         no envelope at all.
//
// Converted so far: a machine's AGENT (bots.go) and the DOKS node list (k8s.go).
// Visor is converting noun by noun and drops the envelope as each lands (its
// LLM.md, "Typed ops"), so `call` shrinks toward zero and goes with the last
// noun. Converting a visor route is a WIRE BREAK and lands with its caller here
// in the same change.

package visor

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/shorten"
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
	if v := environ.Or("VISOR_URL", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultBase
}

func serviceClientID() string     { return environ.Or("VISOR_CLIENT_ID", "") }
func serviceClientSecret() string { return environ.Or("VISOR_CLIENT_SECRET", "") }

// client is the tenant-scoped Visor HTTP client. target has no trailing slash.
type client struct {
	target string
	cc     *http.Client
}

func newClient() *client {
	return &client{target: visorBase(), cc: &http.Client{Timeout: 30 * time.Second}}
}

// envelope is Visor's response wrapper. Data is deferred so call() can
// decode it into the caller's concrete type only after confirming status:"ok".
type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// do issues ONE request to Visor and returns the response body. Every call goes
// through it: the URL, the forwarded identity, the credential, the body limit and
// the transport error mapping are decided here and nowhere else.
//
// What it deliberately does NOT decide is how to read the answer, because Visor
// does not have one answer shape — see call and op.
//
// Error mapping is honest and customer-appropriate: an unreachable Visor → 502,
// and a non-2xx HTTP status → that status with a snippet of what came back.
func (cl *client) do(c *zip.Ctx, method, path, query string, body any) ([]byte, error) {
	u := cl.target + path
	if query != "" {
		u += "?" + query
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "visor: encode request: %v", err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(c.Context(), method, u, rdr)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "visor: build request: %v", err)
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
		return nil, zip.Errorf(http.StatusBadGateway, "visor: unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zip.Errorf(resp.StatusCode, "visor: upstream %d: %s", resp.StatusCode, snippet(raw))
	}
	return raw, nil
}

// call reads an ENVELOPED answer — Visor's untyped controller routes, which put
// the payload under {status,msg,data} and report a logical failure as
// status:"error" inside an HTTP 200. out (a pointer) may be nil when the caller
// only needs success/failure.
//
// This is the OLD half of Visor's surface and it shrinks: a route converted to a
// typed zip op answers its Out directly and moves to op below. When the last one
// has moved, this and the envelope type go with it.
func (cl *client) call(c *zip.Ctx, method, path, query string, body any, out any) error {
	raw, err := cl.do(c, method, path, query, body)
	if err != nil {
		return err
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return zip.Errorf(http.StatusBadGateway, "visor: decode envelope: %v", err)
	}
	if env.Status != "" && env.Status != "ok" {
		return zip.Errorf(http.StatusBadGateway, "visor: %s", cmp.Or(strings.TrimSpace(env.Msg), "upstream error"))
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return zip.Errorf(http.StatusBadGateway, "visor: decode data: %v", err)
		}
	}
	return nil
}

// op reads a TYPED op's answer — Visor's zip.Get/Post[In,Out] routes, whose body
// IS the declared Out with nothing wrapped around it. A logical failure arrives
// as an HTTP status, so do has already turned it into an error by the time this
// decodes anything.
//
// The two readings cannot be merged, and merging them is the trap: an envelope
// decoded as an Out (or the reverse) leaves every field at its zero value and
// returns no error at all, so a version skew reads as an empty answer rather than
// a broken one. A caller therefore checks that the field it asked for ARRIVED —
// see listK8sNodes — instead of trusting a decode that cannot fail.
//
// It is not always that quiet, and the loud form is worth knowing: an
// AgentBinding carries its OWN `status` field, so reading one with call above
// takes "Pending" for an envelope status, decides the upstream failed, and
// answers 502. A real answer becomes an outage.
func (cl *client) op(c *zip.Ctx, method, path, query string, body any, out any) error {
	raw, err := cl.do(c, method, path, query, body)
	if err != nil {
		return err
	}
	// A VOID op answers 204 with no body, which is not a decode failure — it is
	// the whole answer. Only a caller that asked for nothing can be given
	// nothing, so this is checked with out rather than instead of it.
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return zip.Errorf(http.StatusBadGateway, "visor: decode %s: %v", path, err)
	}
	return nil
}

// notFound reports whether err is an upstream 404. For the agent ops that is a
// FACT about the machine — it runs no bot — and not a fault, so a caller that
// has something honest to say about "no binding" says it rather than passing a
// 404 through with visor's prose attached.
func notFound(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
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
	return shorten.To(strings.TrimSpace(string(b)), 200)
}

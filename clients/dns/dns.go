// Package dns forwards the console's DNS dashboard traffic (/v1/dns/*) to the
// Hanzo DNS control plane (dns/plugin/hanzodns), which owns the authoritative
// zone/record store. cloud serves console.hanzo.ai (the DnsModule) but holds no
// DNS state of its own, so without this thin head console.hanzo.ai/v1/dns/* 404s
// and the dashboard shows empty zones.
//
// SHAPE. One prefix (/v1/dns/*), every verb, full path passthrough, forwarded to a
// service whose base URL comes from env (HANZO_DNS_URL) -- the SAME shape and env
// convention the domain product uses to reach the same plane. It builds a FRESH
// upstream request and sets only the headers it means to send, so no inbound header
// (a stray cookie, a forged X-*, an injected Authorization copy) is blindly relayed.
//
// ISOLATION -- BEARER RELAY, NO STANDING CRED. The DNS plane is OIDC-gated and keys
// every zone per-org: it re-validates the caller's OWN bearer and derives the org
// from the `owner` claim. This head relays that identity UNCHANGED -- the caller's
// validated bearer as Authorization (cloud.CallerBearer), plus the server-validated
// org as X-Org-Id -- and substitutes NO service credential (which would collapse
// tenants). So a caller in org A can reach only org A's zones, exactly as if it had
// called the DNS plane directly. Fail-closed: a request with no validated principal
// is refused 403 before any byte leaves cloud.
package dns

import (
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// defaultDNSURL is the in-cluster DNS control-plane API -- the operator's
// DNSConnector default endpoint (the API listens on :8443). Used when HANZO_DNS_URL
// is unset so a standard cluster deployment forwards without extra config.
const defaultDNSURL = "http://coredns-hanzodns.dns-system.svc:8443"

// maxDNSBody bounds the upstream response read: zone/record listings are small
// JSON, so this caps a hostile or runaway upstream body.
const maxDNSBody = 4 << 20 // 4 MiB

// forwardTimeout bounds a single upstream call so a hung DNS plane cannot wedge a
// console request. The caller's context deadline (if tighter) still wins.
const forwardTimeout = 15 * time.Second

type edge struct {
	base string
	http *http.Client
}

// Mount wires the DNS dashboard forward head at /v1/dns/* (all verbs, full path
// passthrough). Registered as a subsystem in apps.Wire(); on by default.
func Mount(app *zip.App, deps cloud.Deps) error {
	e := &edge{
		base: strings.TrimRight(strings.TrimSpace(dnsURL()), "/"),
		http: &http.Client{
			Timeout: forwardTimeout,
			// Do NOT follow upstream 3xx. Relay the redirect response verbatim
			// (status + Location) so responses pass through as claimed and a
			// redirect can never silently re-target the request onto another host
			// or path under this head's own (fresh-request, bearer-relay) identity.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	app.Group("/v1/dns").All("/*", e.forward)
	if deps.Logger != nil {
		deps.Logger.Info("dns forward head mounted", "upstream", e.base)
	}
	return nil
}

// dnsURL is the DNS control-plane base URL, from env with an in-cluster default.
func dnsURL() string {
	if v := strings.TrimSpace(os.Getenv("HANZO_DNS_URL")); v != "" {
		return v
	}
	return defaultDNSURL
}

// forward relays one /v1/dns/* request to the DNS control plane under the CALLER'S
// OWN validated identity and returns the plane's response verbatim.
func (e *edge) forward(c *zip.Ctx) error {
	// Fail closed: only a validated principal with a real org proceeds. A forged or
	// anonymous X-Org-Id yields ("", false) and is refused HERE -- it never reaches
	// the DNS plane. This is the cloud-side tenant gate; the DNS plane enforces its
	// own org-scoping on top.
	org, ok := principal.Org(c)
	if !ok {
		return c.JSON(http.StatusForbidden, fail("forbidden", "a validated principal is required"))
	}
	if e.base == "" {
		return c.JSON(http.StatusServiceUnavailable, fail("unconfigured", "dns control plane is not configured"))
	}

	// Path + query pass through verbatim, but ONLY within /v1/dns. uri.Path() is
	// normalized and percent-decoded, so any dot-segment or encoded-dot traversal
	// (which Fiber's raw wildcard still matches) has ALREADY been resolved here --
	// e.g. /v1/dns/../../admin normalizes to /admin. Guard the normalized path so
	// the forward is LOCKED under /v1/dns; the caller can never walk the request
	// onto another path of the DNS host. Fail closed: anything outside is refused
	// before a byte leaves cloud. The upstream host still comes ONLY from env
	// (never caller-influenced), so a path can never re-target another host: no SSRF.
	uri := c.Fiber().Request().URI()
	p := string(uri.Path())
	if p != "/v1/dns" && !strings.HasPrefix(p, "/v1/dns/") {
		return c.JSON(http.StatusBadRequest, fail("bad_request", "path must be under /v1/dns"))
	}
	target := e.base + p
	if qs := uri.QueryString(); len(qs) > 0 {
		target += "?" + string(qs)
	}

	var body io.Reader
	if b := c.Body(); len(b) > 0 {
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(c.Context(), c.Method(), target, body)
	if err != nil {
		return c.JSON(http.StatusBadGateway, fail("bad_gateway", "dns request failed"))
	}

	// BEARER RELAY -- the caller's OWN validated bearer, unchanged. The DNS plane
	// re-validates it and derives the org from the `owner` claim; cloud substitutes
	// NO service credential. X-Org-Id carries the server-validated org for a
	// trusted-proxy DNS mode. Both are the caller's own identity, so org A can never
	// reach org B. Because this is a fresh request, no other inbound header crosses.
	if bearer := cloud.CallerBearer(c); bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("X-Org-Id", org)
	if ct := c.Header("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")

	res, err := e.http.Do(req)
	if err != nil {
		return c.JSON(http.StatusBadGateway, fail("bad_gateway", "dns control plane unavailable"))
	}
	defer func() { _ = res.Body.Close() }()

	out, _ := io.ReadAll(io.LimitReader(res.Body, maxDNSBody))
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.SetHeader("Content-Type", ct)
	return c.Bytes(res.StatusCode, out)
}

// fail is the standard cloud-generated error body, matching the DNS plane's
// {code,message} shape so a client parses one error schema across the hop.
func fail(code, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

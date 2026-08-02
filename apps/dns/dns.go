// Package dns is your DNS records: the zones and records behind every name you
// point at Hanzo.
//
// It forwards the console's DNS dashboard traffic (/v1/dns/*) to the Hanzo DNS
// control plane (dns/plugin/hanzodns), which owns the authoritative
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
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// dnsRoute is the ONE address this subsystem answers on, written as the WHOLE
// fiber pattern Mount's group and leaf compose — which is the form the prose
// registry keys on, and the form a description that names the group leaf alone
// would silently miss.
const dnsRoute = "/v1/dns/*"

// dnsRelay is the half of every verb's prose that is identical for every verb,
// because the HANDLER is identical for every verb: one registration serves them
// all. Stated once so five descriptions cannot drift into five accounts of one
// forward.
const dnsRelay = " The plane owns the authoritative zone and record store behind " +
	"every name pointed at Hanzo; this head keeps none of it. The sub-path after " +
	"/v1/dns and the query string ARE the plane's own API address, relayed verbatim, " +
	"and the plane's answer comes back unchanged — its status code, its Content-Type, " +
	"and its Location on a redirect this head never follows.\n\n" +
	"It travels under the CALLER'S OWN identity and substitutes no service " +
	"credential, which would collapse tenants: the caller's validated session bearer " +
	"goes upstream as Authorization and the server-validated org as X-Org-Id, so a " +
	"caller in one org reaches only that org's zones, exactly as if it had called the " +
	"plane directly. The upstream host comes only from deployment config, never from " +
	"the request, so no path can re-target another host.\n\n" +
	"Fails closed before a byte leaves cloud: no validated principal is 403; an API " +
	"key is 401, because a pk-/sk- key is not a JWT the OIDC-gated plane can " +
	"validate and there is no substitute credential to send in its place; a path that " +
	"normalizes outside /v1/dns, or still carries a percent-escape or a `..` after one " +
	"decode, is 400; an unconfigured plane is 503 and an unreachable one 502."

// The prose for this subsystem's ONE route, stated once per verb the document
// renders. Mount explains why the route cannot be a typed op; the consequence was
// that the WHOLE subsystem published five operationIds and nothing else — five SDK
// methods and five CLI commands that could not say what reaches the DNS plane, or
// under whose identity. Declared through the same registry openapi.Register uses,
// so it renders only while the router actually serves the route.
func init() {
	for _, d := range []struct{ method, summary, lead string }{
		{http.MethodGet, "Read your org's DNS zones and records",
			"Reads DNS state — a zone, a record, a listing — from the Hanzo DNS control plane."},
		{http.MethodPost, "Create a DNS zone or record",
			"Creates DNS state — a zone, a record — on the Hanzo DNS control plane."},
		{http.MethodPut, "Replace a DNS zone or record",
			"Replaces a DNS zone or record on the Hanzo DNS control plane."},
		{http.MethodPatch, "Amend a DNS zone or record",
			"Amends a DNS zone or record on the Hanzo DNS control plane."},
		{http.MethodDelete, "Delete a DNS zone or record",
			"Removes a DNS zone or record from the Hanzo DNS control plane."},
	} {
		openapi.Describe(dnsRoute, d.method, d.summary, d.lead+dnsRelay)
	}
	// The methods left over. This address is bound with All(), so it publishes every
	// method this generator knows and the ones above are only the ones that DO
	// something. DescribeRest covers the remainder from the generator's own set, so a
	// method added there is covered the day it appears rather than published bare —
	// which is what a hand-copied list here had already produced for OPTIONS and TRACE.
	openapi.DescribeRest(dnsRoute,
		"Not served by the DNS surface",
		"Published because this address accepts every method, but the DNS surface routes "+
			"nothing here: no zone or record is read or changed."+dnsRelay)

}

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
func Mount(app cloud.Router, deps cloud.Deps) error {
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
	// UNTYPED BY DESIGN — and it is the only route here, so this whole subsystem
	// publishes no prose, no MCP tool and no CLI command. Three wire facts make it
	// untypable as it stands, each on its own sufficient:
	//
	//   - it is ONE registration for EVERY method (All), including OPTIONS and
	//     TRACE. zip's typed registrars are per-method and it has no All[In, Out];
	//     seven ops would each have to name a body the relay does not parse.
	//   - the path is a GREEDY wildcard. fiber calls the segment `*1` and the
	//     document calls it `{wildcard1}`, so a typed In's bound field and the
	//     published parameter cannot agree — and the value is a whole sub-path,
	//     not a scalar the binder can set.
	//   - the response is the DNS plane's own, verbatim: its status code
	//     (c.Bytes(res.StatusCode, out), below), its Content-Type, and its
	//     Location on a 3xx. A typed op answers the status its op DECLARED and
	//     serialises its Out as JSON, so every one of those three moves.
	//
	// Typing this means giving the DNS plane a typed control surface in the plane
	// itself, not wrapping it here. See LLM.md, "the typed migration".
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
	// normalized and decoded by ONE layer, so a single-encoded dot-segment traversal
	// (which Fiber's raw wildcard still matches) has ALREADY been resolved here --
	// e.g. /v1/dns/../../admin normalizes to /admin -- and fails the prefix check.
	// Guard the normalized path so the forward is LOCKED under /v1/dns; the caller
	// can never walk the request onto another path of the DNS host.
	uri := c.Fiber().Request().URI()
	p := string(uri.Path())
	if p != "/v1/dns" && !strings.HasPrefix(p, "/v1/dns/") {
		return c.JSON(http.StatusBadRequest, fail("bad_request", "path must be under /v1/dns"))
	}
	// Second layer: a DOUBLE-encoded traversal survives one decode as a literal `%2e`
	// (e.g. /v1/dns/%252e%252e/admin -> /v1/dns/%2e%2e/admin) that KEEPS the prefix,
	// so the upstream would decode the second layer and resolve outside /v1/dns. A
	// residual `%` (still-encoded byte) or `..` (residual traversal) in the
	// once-decoded path is the tell -- neither occurs in a legitimate DNS-API path
	// (zone labels are DNS names / punycode xn--). Fail closed before a byte leaves
	// cloud. The upstream host still comes ONLY from env (never caller-influenced),
	// so a path can never re-target another host: no SSRF.
	if strings.ContainsRune(p, '%') || strings.Contains(p, "..") {
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
	//
	// REFUSE HERE when there is no relayable bearer, rather than forwarding without
	// one. CallerBearer returns "" for an API KEY (middleware_identity: `tok == "" ||
	// isAPIKey(tok)`), because a pk-/sk- key is not a JWT and the OIDC-gated DNS
	// plane cannot validate it. This head was written for the console, which carries
	// a session bearer, so that case went unhandled: the request was forwarded with
	// NO Authorization at all and the caller got the DNS plane's own
	// {"code":"unauthorized","message":"missing Authorization header"} -- an upstream
	// error that reads like the DNS plane is broken, when the real answer is that
	// this credential type cannot reach it. Measured against the live plane
	// (ghcr.io/hanzoai/dns:0.11.1) with an API key: exactly that 401.
	//
	// Failing closed here also keeps the tenant story honest. X-Org-Id is set below
	// from the server-validated org, so a headerless forward would arrive carrying an
	// org claim and no proof of identity -- safe only for as long as the DNS plane
	// keeps rejecting it. Cloud should not depend on an upstream to refuse what it
	// can refuse itself.
	bearer := cloud.CallerBearer(c)
	if bearer == "" {
		return c.JSON(http.StatusUnauthorized, fail("unauthorized",
			"the DNS plane requires a session bearer; an API key cannot be relayed to it"))
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
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
	// Relay Location so an upstream 3xx (never followed -- see CheckRedirect) passes
	// back verbatim: status + Location, for the caller to act on.
	if loc := res.Header.Get("Location"); loc != "" {
		c.SetHeader("Location", loc)
	}
	return c.Bytes(res.StatusCode, out)
}

// fail is the standard cloud-generated error body, matching the DNS plane's
// {code,message} shape so a client parses one error schema across the hop.
func fail(code, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

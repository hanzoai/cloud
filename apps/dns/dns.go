// Package dns is your DNS records: the zones and records behind every name you
// point at Hanzo.
//
// It forwards the console's DNS dashboard traffic (/v1/dns/*) to the Hanzo DNS
// control plane (dns/plugin/hanzodns), which owns the authoritative
// zone/record store. cloud serves console.hanzo.ai (the DnsModule) but holds no
// DNS state of its own, so without this thin head console.hanzo.ai/v1/dns/* 404s
// and the dashboard shows empty zones.
//
// SHAPE. One prefix (/v1/dns/*), every verb, full path passthrough, answered by the
// DNS control plane this deployment selected. It builds a FRESH upstream request and
// sets only the headers it means to send, so no inbound header (a stray cookie, a
// forged X-*, an injected Authorization copy) is blindly relayed.
//
// WHICH PLANE IS CONFIGURATION, NOT CODE. The plane sits behind Provider
// (provider.go): the head validates the caller, scopes the path, reads the operation
// off the address, and hands over a Call. HANZO_DNS_PROVIDER names the adapter and
// defaults to Hanzo's own plane (hanzo.go, which reads HANZO_DNS_URL). A second plane
// — Cloudflare, Route 53, anything an org already runs — is a NEW FILE carrying a
// type, its four methods and a register() in its init(): no edit here, none to the
// registry, none to the route.
//
// UNTYPED, AND THE COUNT IS FIVE. The whole surface is one All() registration on
// a greedy wildcard, so it publishes five operations (one per method the document
// generator knows) and not one of them can be a typed op — three wire facts, each
// sufficient, each re-verified against the pinned zip and gated in
// typed_wire_test.go rather than believed. Prose is therefore the only thing this
// head can state about itself, and it states it per method. The module that would
// end that is named at the registration in Mount.
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
	"errors"
	"net/http"
	"strings"

	luxlog "github.com/luxfi/log"

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
	// method the generator knows, and the five above are the ones that DO something.
	// The generator's set is exactly those five today (openapi.Methods), so this
	// currently covers nothing — and that is the point of asking rather than listing:
	// a hand-copied list here had already published bare operations for OPTIONS and
	// TRACE after the generator stopped emitting them, and would publish them bare
	// again the day it starts.
	openapi.DescribeRest(dnsRoute,
		"Not served by the DNS surface",
		"Published because this address accepts every method, but the DNS surface routes "+
			"nothing here: no zone or record is read or changed."+dnsRelay)

}

// edge is the head: a validated caller, a scoped path, and the one provider this
// deployment answers from. It holds no endpoint and no credential — those are the
// provider's, and only the provider's.
type edge struct{ p Provider }

// Mount wires the DNS dashboard forward head at /v1/dns/* (all verbs, full path
// passthrough). Registered as a subsystem in apps.Wire(); on by default.
func Mount(app cloud.Router, deps cloud.Deps) error {
	p, err := selected()
	if err != nil {
		return err
	}
	e := &edge{p: p}
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
	// zip v1.31.0 closed two of the three gaps this used to name — a SET of declared
	// statuses (WithStatus + StatusCoder) and declared response headers
	// (WithResponseHeader + HeaderCoder) — and neither reaches: a relay passes ANY
	// upstream status rather than one of a declared set, and fiber stamps
	// application/json over whatever a header coder set (res.go:501). The wildcard
	// fact got STRONGER on re-reading: zip's Template leaves `*` verbatim while
	// cloud's router reading names it {wildcard1}, so a typed op here does not
	// mis-name a parameter, it makes the fold refuse to produce a document at all.
	// typed_wire_test.go runs both.
	//
	// THE MODULE OWED. This head cannot be fixed from here, and neither can the
	// smaller half — declaring the bodies — because it authors none: the request
	// bytes are the caller's, relayed unread, and the response bytes are the
	// plane's, relayed unparsed. What ends it is hanzoai/dns handing its host what
	// hanzoai/ai hands one: a route table (`path -> methods` plus
	// `"METHOD /path" -> sentence`) or a *zip.App. openapi.Table and openapi.Front
	// already consume both, and a relay REPLACES the route with what the route
	// reaches — so the day that table exists, /v1/dns publishes the plane's real
	// addresses instead of five wildcard operations, with no edit to this file
	// beyond the declaration. Until then five operations is the honest count and
	// prose is the honest declaration. See LLM.md, "the typed migration", and
	// openapi/relay.go.
	app.Group("/v1/dns").All("/*", e.forward)
	if luxlog.Default() != nil {
		luxlog.Default().Info("dns forward head mounted", "provider", p.ID())
	}
	return nil
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

	// The address is read ONCE, here, into the operation it names plus the zone and
	// record it names it on — so an adapter reads values and never a request.
	call := classify(c.Method(), p)
	call.Org, call.Bearer = org, bearer
	call.Query = string(uri.QueryString())
	call.Body = c.Body()
	call.ContentType = c.Header("Content-Type")

	a, err := answer(c.Context(), e.p, call)
	switch {
	case errors.Is(err, ErrUnconfigured):
		return c.JSON(http.StatusServiceUnavailable, fail("unconfigured", "dns control plane is not configured"))
	case errors.Is(err, errNoAddress):
		return c.JSON(http.StatusNotFound, fail("not_found", "the configured dns plane has no such address"))
	case err != nil:
		// The provider's own error stays with the provider: a caller learns that the
		// plane did not answer, never why, and never an upstream URL.
		return c.JSON(http.StatusBadGateway, fail("bad_gateway", "dns control plane unavailable"))
	}

	ct := a.ContentType
	if ct == "" {
		ct = "application/json"
	}
	c.SetHeader("Content-Type", ct)
	// Relay Location so a plane's 3xx (which the adapter never follows) passes back
	// verbatim: status + Location, for the caller to act on.
	if a.Location != "" {
		c.SetHeader("Location", a.Location)
	}
	return c.Bytes(a.Status, a.Body)
}

// fail is the standard cloud-generated error body, matching the DNS plane's
// {code,message} shape so a client parses one error schema across the hop.
func fail(code, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

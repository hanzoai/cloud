// Package cloudflare is the per-org Cloudflare asset plane for the unified Hanzo
// Cloud binary — the first-class /v1/cloudflare/* surface (sibling of /v1/dns and
// /v1/domain) that manages an org's Cloudflare Zones/Analytics, Pages, Workers,
// Workers AI, R2, KV, and D1 through the SAME per-org, KMS-sealed API token the org
// connected via clients/integrations. Connecting the provider stays on the
// integrations plane (/v1/integrations/cloudflare/{connect,callback}); MANAGING the
// resources is this first-class plane — "how you connected" and "what you manage"
// are separated, one concern each. Every call drives Cloudflare with the org's own
// scoped token, so the platform never reaches Cloudflare with a global env token —
// one token, one custody boundary, one org.
//
// Workers AI is the one exception to pure passthrough: an /ai/run is INFERENCE, so
// it meters through the SAME unified usage/billing spine (cloud.AIMeterProvider) and
// emits to the SAME gen_ai o11y span plane as every other model call — at the thin
// BYO fee, since the org's own token already paid Cloudflare for the compute. There
// is no Cloudflare-specific usage or o11y path.
//
// TENANT ISOLATION (the crown jewel). Every handler resolves the caller's org from
// the VALIDATED principal (principal.Org → the X-Org-Id the identity boundary minted
// from a verified credential, HIP-0026 / SanitizeIdentity), NEVER from a body or
// query field. The org is then the ONLY input to token custody: the per-org token is
// read in-process through the ONE seam integrations.TokenFor, which keys KMS on that
// org (/orgs/{org}/integrations/cloudflare/api_token). So a request can ONLY ever
// address its own org's Cloudflare account:
//   - no validated principal ⟹ principal.Org fails ⟹ 403 (a forged X-Org-Id with no
//     bearer is refused by the identity boundary, then again here);
//   - a non-SuperAdmin bearer has X-Org-Id pinned to its own owner (SanitizeIdentity),
//     so it cannot name another org;
//   - cross-org token reach is structurally impossible — the token path is derived
//     from the validated org, not from any caller-controlled field.
//
// The token rides ONLY the Authorization header on the outbound Cloudflare request;
// it is never logged, echoed in an error, or stored by this subsystem.
//
// FAIL-CLOSED. An org that has not connected Cloudflare, an unmounted integrations
// plane, or a KMS that is not Ready each yield an error and a 503 — never another
// org's data and never a silent success.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	// providerCloudflare is the integrations provider slug the token is custodied
	// under, and secretAPIToken the secret name — the SAME coordinate the connector
	// (clients/integrations/cloudflare.go) seals BOTH the apikey and OAuth paths to,
	// and hanzodns reads for DNS. One coordinate, auth-method-agnostic.
	providerCloudflare = "cloudflare"
	secretAPIToken     = "api_token"
)

// tokenFor is the ONE door to per-org Cloudflare token custody. It defaults to
// integrations.TokenFor (KMS-sealed, fail-closed, org-validated). It is a package
// var ONLY so a test can inject per-org tokens and prove every fetch scopes to the
// caller's org; production never reassigns it.
var tokenFor = integrations.TokenFor

// connectionFor reads an org's NON-secret connection metadata (the account id captured
// at connect time, ExternalID) from the integrations plane. Also a package var ONLY
// for test injection; production never reassigns it.
var connectionFor = integrations.ConnectionFor

// cfAPIBase is Cloudflare's API v4 origin. Overridable via CLOUDFLARE_API_BASE for
// tests (an httptest server) and CF-compatible endpoints; read at call time. The
// default is the real Cloudflare API. (Same knob hanzodns uses, so a test harness
// points both planes at one stub.)
func cfAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

// cfHTTPClient is the ONE client for every Cloudflare call (connection-pooled). Its
// Timeout is the outer ceiling; each call tightens it with a per-request deadline via
// send, so a slow/hung Cloudflare never wedges a goroutine.
var cfHTTPClient = &http.Client{Timeout: timeoutAI}

const (
	// timeout bounds a metadata/relay call; timeoutAI a Workers AI run (inference is
	// slower). Passed explicitly to send — the caller states its own bound, no magic.
	timeout   = 30 * time.Second
	timeoutAI = 120 * time.Second
	// maxBody bounds an enveloped JSON response (list/get/query results); maxRawBody a
	// raw KV value (CF caps a value at 25 MiB).
	maxBody    = 4 << 20
	maxRawBody = 25 << 20
)

var (
	// nameRE bounds a Cloudflare NAME path segment (Pages project, Worker script,
	// custom-domain name/id, bucket, namespace, database). It is validated before it
	// is folded into an upstream URL so a hostile value can never smuggle path
	// structure (a `/` or `..`) into the Cloudflare request.
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	// idRE bounds a Cloudflare 32-hex ID path segment (account id, zone id, route id).
	idRE = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
)

// state is this subsystem's own data. Token custody lives in integrations and the
// account id is resolved per request, so the only field is aiBill: the meter that
// prices Workers AI inference.
type state struct {
	// aiBill meters Workers AI runs through the SAME usage/billing spine as every
	// LLM call (provider cloud.AIMeterProvider = "ai"), so a BYO Workers AI run
	// debits the same product axis and shares the same per-scope caps — never a
	// Cloudflare-specific usage path. It is the ONLY resource this subsystem owns.
	aiBill *cloud.ResourceMeter
}

// Mount wires /v1/cloudflare/* onto app. The subsystem holds no store and runs no
// goroutine: it reads the per-org token in-process per request and proxies to
// Cloudflare. The build closure captures deps to construct the "ai"-provider meter
// (Base.Bill is provider "cloudflare"; Workers AI must bill under "ai").
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "cloudflare",
		func(cloud.Base) (state, error) {
			return state{aiBill: cloud.NewResourceMeter(deps, cloud.AIMeterProvider)}, nil
		},
		routes)
}

// routes registers the first-class /v1/cloudflare surface. Every route runs through
// authClient (validated-org gate + fail-closed per-org token) FIRST — reads require
// a validated org, mutations additionally require org admin (authWrite) — so no
// route is a softer target than another.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/cloudflare")

	// Zones + Analytics (read) — enumerate the org's zones and read a zone's traffic
	// analytics; the zone ids feed Workers routes and analytics. Zone/record
	// MANAGEMENT stays with the Hanzo DNS plane (/v1/dns); this only surfaces CF zones.
	g.Get("/zones", cloud.Handle(s, zonesList))
	g.Get("/zones/:zone", cloud.Handle(s, zoneGet))
	g.Get("/zones/:zone/analytics", cloud.Handle(s, zoneAnalytics))

	// Pages — account-scoped.
	g.Get("/pages/projects", cloud.Handle(s, pagesList))
	g.Post("/pages/projects", cloud.Handle(s, pagesCreate))
	g.Get("/pages/projects/:project", cloud.Handle(s, pagesGet))
	g.Delete("/pages/projects/:project", cloud.Handle(s, pagesDelete))
	g.Post("/pages/projects/:project/deployments", cloud.Handle(s, pagesDeploy))
	g.Post("/pages/projects/:project/domains", cloud.Handle(s, pagesDomainAdd))
	g.Delete("/pages/projects/:project/domains/:domain", cloud.Handle(s, pagesDomainDelete))

	// Workers — scripts + workers.dev subdomain are account-scoped; routes are
	// zone-scoped.
	g.Get("/workers/scripts", cloud.Handle(s, workersScriptList))
	g.Put("/workers/scripts/:script", cloud.Handle(s, workersScriptPut))
	g.Delete("/workers/scripts/:script", cloud.Handle(s, workersScriptDelete))
	g.Post("/workers/scripts/:script/subdomain", cloud.Handle(s, workersScriptSubdomainSet))
	g.Get("/workers/subdomain", cloud.Handle(s, workersSubdomainGet))
	g.Get("/workers/zones/:zone/routes", cloud.Handle(s, workersRouteList))
	g.Post("/workers/zones/:zone/routes", cloud.Handle(s, workersRouteCreate))
	g.Delete("/workers/zones/:zone/routes/:route", cloud.Handle(s, workersRouteDelete))

	// Workers AI (inference) — run a CF-hosted model with the org's own token. The
	// model rides a wildcard: CF model ids look like @cf/meta/llama-3-8b-instruct.
	// Metered through the unified AI spine + emitted to the one gen_ai span plane.
	g.Post("/ai/run/*", cloud.Handle(s, aiRun))

	// R2 — account-scoped buckets.
	g.Get("/r2/buckets", cloud.Handle(s, r2BucketList))
	g.Post("/r2/buckets", cloud.Handle(s, r2BucketCreate))
	g.Delete("/r2/buckets/:bucket", cloud.Handle(s, r2BucketDelete))

	// KV — namespaces and a namespace's key values.
	g.Get("/kv/namespaces", cloud.Handle(s, kvNamespaceList))
	g.Post("/kv/namespaces", cloud.Handle(s, kvNamespaceCreate))
	g.Delete("/kv/namespaces/:namespace", cloud.Handle(s, kvNamespaceDelete))
	g.Get("/kv/namespaces/:namespace/values/:key", cloud.Handle(s, kvValueGet))
	g.Put("/kv/namespaces/:namespace/values/:key", cloud.Handle(s, kvValuePut))
	g.Delete("/kv/namespaces/:namespace/values/:key", cloud.Handle(s, kvValueDelete))

	// D1 — databases and a query against one.
	g.Get("/d1/databases", cloud.Handle(s, d1DatabaseList))
	g.Post("/d1/databases", cloud.Handle(s, d1DatabaseCreate))
	g.Delete("/d1/databases/:database", cloud.Handle(s, d1DatabaseDelete))
	g.Post("/d1/databases/:database/query", cloud.Handle(s, d1Query))
}

// ── client (the cfDo shape, reused verbatim from hanzodns) ──────────────────────

// client drives the Cloudflare API v4 with a per-org scoped token. The token rides
// only the Authorization header — never a query parameter, error, or log line.
type client struct {
	token string
	base  string
}

// cfEnvelope is the shared Cloudflare API v4 response envelope.
type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// cfError carries the upstream Cloudflare HTTP status so a proxied not-found/bad
// request is reported with a recognizable code rather than a blanket 502. Its
// message is Cloudflare's own — token-free by construction.
type cfError struct {
	upstream int
	code     int
	msg      string
}

func (e *cfError) Error() string { return e.msg }

func (e cfEnvelope) err(status int) error {
	if e.Success {
		return nil
	}
	if len(e.Errors) > 0 {
		return &cfError{upstream: status, code: e.Errors[0].Code,
			msg: fmt.Sprintf("cloudflare API error (%d): [%d] %s", status, e.Errors[0].Code, e.Errors[0].Message)}
	}
	return &cfError{upstream: status, msg: fmt.Sprintf("cloudflare API error (status %d)", status)}
}

// send is the ONE authed Cloudflare request: issue method+path with the per-org
// Bearer (the token rides ONLY this header — never a query, log, or error), bound the
// call by to, read at most limit bytes, and return the raw status + content type +
// body. Every response shape — envelope unwrap (exec), verbatim relay (pass), raw
// value (getRaw), AI run (runAI) — composes over this one primitive, so the
// request/auth/read dance lives in exactly one place. A transport failure is
// token-free by construction.
func (cl *client) send(ctx context.Context, method, path, contentType string, body io.Reader, to time.Duration, limit int64) (int, string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, cl.base+path, body)
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		// A transport error can echo the request URL but never the header; keep the
		// message token-free regardless.
		return 0, "", nil, fmt.Errorf("cloudflare request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return resp.StatusCode, resp.Header.Get("Content-Type"), data, nil
}

// envErr maps a Cloudflare response to a token-free error from its {success,errors}
// envelope, carrying the upstream status for cfErr's remap. It returns nil ONLY for a
// successful envelope — the shared fail-closed check for every enveloped response.
func envErr(status int, data []byte) error {
	var env cfEnvelope
	if len(data) > 0 {
		_ = json.Unmarshal(data, &env)
	}
	return env.err(status)
}

// cfDo performs a Cloudflare API v4 call, JSON-encoding body and decoding the
// {success,errors,result} envelope's result into out. Thin over exec.
func (cl *client) cfDo(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	var contentType string
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
		contentType = "application/json"
	}
	return cl.exec(ctx, method, path, contentType, reader, out)
}

// cfUpload performs a Cloudflare call with a pre-built body + content type (the
// multipart Worker-script upload, a raw KV value write), sharing exec's envelope.
func (cl *client) cfUpload(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	return cl.exec(ctx, method, path, contentType, bytes.NewReader(body), out)
}

// exec is send + the {success,errors,result} envelope shape: fail closed on an
// unsuccessful envelope, then decode result into out. A 204 is success with no result.
func (cl *client) exec(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	status, _, data, err := cl.send(ctx, method, path, contentType, body, timeout, maxBody)
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		return nil
	}
	if err := envErr(status, data); err != nil {
		return err
	}
	if out != nil {
		var wrap struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(data, &wrap); err != nil {
			return fmt.Errorf("cloudflare: malformed response")
		}
		if len(wrap.Result) > 0 {
			if err := json.Unmarshal(wrap.Result, out); err != nil {
				return fmt.Errorf("cloudflare: malformed result")
			}
		}
	}
	return nil
}

// pass runs a Cloudflare call and relays its result to the caller VERBATIM (as raw
// JSON), so the upstream shape reaches the platform without field loss — the ONE
// response path for every wired handler. An empty result (e.g. a 204 delete) becomes
// {"success":true}.
func (cl *client) pass(c *zip.Ctx, method, path string, body any) error {
	var out json.RawMessage
	if err := cl.cfDo(c.Context(), method, path, body, &out); err != nil {
		return cfErr(err)
	}
	return writeResult(c, out)
}

func writeResult(c *zip.Ctx, out json.RawMessage) error {
	if len(out) == 0 {
		return c.JSON(http.StatusOK, map[string]any{"success": true})
	}
	c.SetHeader("Content-Type", "application/json; charset=utf-8")
	return c.Bytes(http.StatusOK, out)
}

// ── request gate + token custody ────────────────────────────────────────────────

// actingOrgHeader is stamped on every SERVED /v1/cloudflare response with the org
// whose Cloudflare token was actually used. A per-org caller (the platform BFF) MUST
// assert it equals the org it requested: if a misdeployed, non-org-switch-capable
// service credential made the identity boundary PIN X-Org-Id to the token's OWN
// owner, this header exposes the mismatch so the caller fails LOUD instead of
// silently reading/writing another tenant's Cloudflare account.
const actingOrgHeader = "X-Hanzo-Org"

// authClient is the READ front door: it resolves the caller's validated org (403 if
// unvalidated — a forged X-Org-Id with no bearer never gets past this) and builds a
// Cloudflare client bound to THAT org's KMS-sealed token (503 if the org has not
// connected Cloudflare or KMS is down). On success it stamps actingOrgHeader with the
// served org. The token detail is logged token-free and never surfaced to the client.
func authClient(s *cloud.Service[state], c *zip.Ctx) (*client, string, error) {
	org, ok := principal.Org(c)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	tok, err := tokenFor(c.Context(), org, providerCloudflare, secretAPIToken)
	if err != nil || len(bytes.TrimSpace(tok)) == 0 {
		// err is custody-authored and token-free (not-connected / invalid-org /
		// KMS-down). Log the reason, tell the client only that CF is unavailable.
		s.Log.Warn("cloudflare token unavailable", "org", org, "err", err)
		return nil, org, zip.Errorf(http.StatusServiceUnavailable, "cloudflare is not connected for this org")
	}
	// Stamp the org actually served so a per-org caller can prove no tenant comingling.
	c.SetHeader(actingOrgHeader, org)
	return &client{token: string(bytes.TrimSpace(tok)), base: cfAPIBase()}, org, nil
}

// authWrite is the MUTATION front door (POST/PUT/DELETE): it additionally requires the
// caller be an admin of its OWN org (principal.IsOrgAdmin — NOT SuperAdmin), parity
// with the AdminOnly connector that seals the token. Wielding the token's dangerous
// verbs (a Worker script PUT is arbitrary code on the org's Cloudflare account/domains;
// a Pages project DELETE is production destruction) must match connecting it. The admin
// check is FIRST, so a non-admin is refused before any KMS token read. Reads stay
// validated-org-only via authClient — org members may look, only org admins may change.
func authWrite(s *cloud.Service[state], c *zip.Ctx) (*client, string, error) {
	if !principal.IsOrgAdmin(c) {
		return nil, "", zip.ErrForbidden("this action requires org admin")
	}
	return authClient(s, c)
}

// acctClient resolves BOTH the caller-org client and its account id — the
// account-scoped READ preamble every Pages/Workers/Workers-AI/R2/KV/D1 handler shares,
// so the auth+account dance is written once. Zone-scoped handlers (workers routes,
// zones, analytics) need no account and use authClient directly.
func acctClient(s *cloud.Service[state], c *zip.Ctx) (*client, string, error) {
	cl, org, err := authClient(s, c)
	if err != nil {
		return nil, "", err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
	return cl, acct, err
}

// acctWrite is acctClient for a mutation: org-admin FIRST (authWrite), then account.
func acctWrite(s *cloud.Service[state], c *zip.Ctx) (*client, string, error) {
	cl, org, err := authWrite(s, c)
	if err != nil {
		return nil, "", err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
	return cl, acct, err
}

// resolveAccount resolves the Cloudflare account id for account-scoped endpoints
// (Pages / Workers). Order: (1) an explicit, validated ?account= override wins (for an
// org whose token spans multiple accounts); (2) the account captured at connect time
// (the connection's ExternalID) — no per-call round-trip and deterministic for a
// multi-account token; (3) only if none is stored, discover it live from the token's
// own /accounts. Every candidate is validated 32-hex so it can never inject path
// structure. Fails closed (400) when nothing yields a usable account.
func (cl *client) resolveAccount(ctx context.Context, org string, c *zip.Ctx) (string, error) {
	if a := strings.TrimSpace(c.Query("account")); a != "" {
		if !idRE.MatchString(a) {
			return "", zip.ErrBadRequest("account must be a 32-character hex id")
		}
		return url.PathEscape(a), nil
	}
	if conn, ok := connectionFor(org, providerCloudflare); ok {
		if id := strings.TrimSpace(conn.ExternalID); idRE.MatchString(id) {
			return url.PathEscape(id), nil
		}
	}
	var accts []struct {
		ID string `json:"id"`
	}
	if err := cl.cfDo(ctx, http.MethodGet, "/accounts?per_page=1", nil, &accts); err != nil {
		return "", cfErr(err)
	}
	for _, a := range accts {
		if idRE.MatchString(a.ID) {
			return url.PathEscape(a.ID), nil
		}
	}
	return "", zip.ErrBadRequest("no cloudflare account is resolvable for this token; pass ?account=<id>")
}

// pathSeg reads a route param, rejects anything not matching re (so it can never
// smuggle path structure into the upstream Cloudflare URL), and returns the
// url.PathEscape'd value ready to concatenate into a CF path.
func pathSeg(c *zip.Ctx, name string, re *regexp.Regexp) (string, error) {
	v := strings.TrimSpace(c.Param(name))
	if !re.MatchString(v) {
		return "", zip.ErrBadRequest(name + " is invalid")
	}
	return url.PathEscape(v), nil
}

// cfErr maps a Cloudflare call failure to a client-facing HTTP error, propagating a
// recognizable upstream status (404/400/403/409) so a proxied not-found is not
// mis-reported as a 502, and defaulting to 502 Bad Gateway otherwise. The message is
// Cloudflare's own (token-free by construction), never this process's token.
func cfErr(err error) error {
	var ce *cfError
	if errors.As(err, &ce) {
		switch ce.upstream {
		case http.StatusNotFound, http.StatusBadRequest, http.StatusForbidden, http.StatusConflict:
			return zip.Errorf(ce.upstream, "%s", ce.msg)
		}
	}
	return zip.Errorf(http.StatusBadGateway, "%s", err.Error())
}

// ── raw relay + query passthrough ───────────────────────────────────────────────

// getRaw performs a GET whose 2xx body is NOT the {success,errors,result} envelope
// but a raw stored value (a KV value), relaying it verbatim with its content type. A
// non-2xx IS an envelope, so it fails closed through envErr — token-free. Thin over
// send.
func (cl *client) getRaw(ctx context.Context, path string) ([]byte, string, error) {
	status, ct, data, err := cl.send(ctx, http.MethodGet, path, "", nil, timeout, maxRawBody)
	if err != nil {
		return nil, "", err
	}
	if status/100 != 2 {
		if e := envErr(status, data); e != nil {
			return nil, "", e
		}
		return nil, "", &cfError{upstream: status, msg: fmt.Sprintf("cloudflare error (status %d)", status)}
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return data, ct, nil
}

// query builds a "?..."-encoded upstream query string from an ALLOWLISTED set of
// inbound query keys, so a read handler can forward pagination/window params
// (page, per_page, since, until, …) without opening arbitrary passthrough. Values
// are url.Values-escaped and the upstream HOST + PATH are fixed by the caller, so a
// hostile value stays a query value — it can inject neither path structure nor a
// different host (no SSRF). Overlong values (>256 bytes) are dropped.
func query(c *zip.Ctx, keys ...string) string {
	q := url.Values{}
	for _, k := range keys {
		if v := strings.TrimSpace(c.Query(k)); v != "" && len(v) <= 256 {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

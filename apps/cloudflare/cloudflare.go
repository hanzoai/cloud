// Package cloudflare is your Cloudflare account, managed from Hanzo: zones,
// Pages, Workers, Workers AI, R2, KV and D1.
//
// It is the per-org Cloudflare asset plane for the unified Hanzo Cloud binary —
// the first-class /v1/cloudflare/* surface (sibling of /v1/dns and /v1/domain)
// that drives an org's Zones/Analytics, Pages, Workers, Workers AI, R2, KV, and
// D1 through the SAME per-org, KMS-sealed API token the org
// connected via apps/integrations. Connecting the provider stays on the
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
// read in-process through the ONE client integrations.TokenFor, which keys KMS on that
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
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

const (
	// providerCloudflare is the integrations provider slug the token is custodied
	// under, and secretAPIToken the secret name — the SAME coordinate the connector
	// (apps/integrations/cloudflare.go) seals BOTH the apikey and OAuth paths to,
	// and hanzodns reads for DNS. One coordinate, auth-method-agnostic.
	providerCloudflare = "cloudflare"
	secretAPIToken     = "api_token"
)

// tokenFor is the ONE entry point to per-org Cloudflare token custody. It defaults to
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

// ops binds the service to every op on this plane. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and each op is a method value (o.zonesList), which is also
// the only bound form cmd/zipdoc can lift prose from. The handful of routes that
// cannot be typed (see routes) are methods on the same receiver, so there is ONE
// way a handler here reaches the service.
type ops struct{ s *cloud.Service[state] }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the first-class /v1/cloudflare surface. Every route runs through
// authClient (validated-org gate + fail-closed per-org token) FIRST — reads require
// a validated org, mutations additionally require org admin (authWrite) — so no
// route is a softer target than another.
//
// Ops are declared on the GROUP, so each op's path is the group's prefix composed
// with its leaf — the same composition the router does, and the identity every
// projection (document, MCP tool, CLI command, SDK method) keys on.
//
// FOUR routes are deliberately NOT typed ops, because a typed op decodes its input
// from JSON and writes its output as JSON, and these four carry bytes that are
// neither. Each is named where it is registered; the reason is on the handler.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/cloudflare")
	o := ops{s: s}

	// A typed op receives only a context, so what it may act on has to be parked
	// there — never taken from an In field, which is caller-supplied and would be a
	// cross-tenant read the caller asserted for itself. cloud.Bridge does the
	// parking, and it is installed HERE, on this plane's own group and ahead of its
	// leaves, rather than left to the composer's app-wide one: a package's own tests
	// mount without a composer, so relying on that alone breaks in tests and nowhere
	// else.
	//
	// The org is the lesser half of what it parks — principal.OrgFrom falls back to
	// the caller zip already carries, so a READ resolves either way. The half only
	// the Bridge can supply is the REQUEST itself (cloud.Request), which authWrite
	// reads for the org-admin header and resolveAccount for `?account=`. Without it
	// every mutation on this plane refuses a genuine org admin.
	g.Use(cloud.Bridge())

	// Zones + Analytics (read) — enumerate the org's zones and read a zone's traffic
	// analytics; the zone ids feed Workers routes and analytics. Zone/record
	// MANAGEMENT stays with the Hanzo DNS plane (/v1/dns); this only surfaces CF zones.
	zip.Get(g, "/zones", o.zonesList)
	zip.Get(g, "/zones/:zone", o.zoneGet)
	zip.Get(g, "/zones/:zone/analytics", o.zoneAnalytics)
	zip.Post(g, "/zones/:zone/purge", o.zonePurge)

	// Pages — account-scoped.
	zip.Get(g, "/pages/projects", o.pagesList)
	zip.Post(g, "/pages/projects", o.pagesCreate)
	zip.Get(g, "/pages/projects/:project", o.pagesGet)
	zip.Delete(g, "/pages/projects/:project", o.pagesDelete)
	// UNTYPED: a malformed deploy body is IGNORED here (the deploy falls back to the
	// project's production branch); a typed In answers 400 instead, which is a
	// different contract. See pagesDeploy.
	g.Post("/pages/projects/:project/deployments", o.pagesDeploy)
	zip.Post(g, "/pages/projects/:project/domains", o.pagesDomainAdd)
	zip.Delete(g, "/pages/projects/:project/domains/:domain", o.pagesDomainDelete)

	// Workers — scripts + workers.dev subdomain are account-scoped; routes are
	// zone-scoped.
	zip.Get(g, "/workers/scripts", o.workersScriptList)
	zip.Put(g, "/workers/scripts/:script", o.workersScriptPut)
	zip.Delete(g, "/workers/scripts/:script", o.workersScriptDelete)
	zip.Post(g, "/workers/scripts/:script/subdomain", o.workersScriptSubdomainSet)
	zip.Get(g, "/workers/subdomain", o.workersSubdomainGet)
	zip.Get(g, "/workers/zones/:zone/routes", o.workersRouteList)
	zip.Post(g, "/workers/zones/:zone/routes", o.workersRouteCreate)
	zip.Delete(g, "/workers/zones/:zone/routes/:route", o.workersRouteDelete)

	// Workers AI (inference) — run a CF-hosted model with the org's own token. The
	// model rides a wildcard: CF model ids look like @cf/meta/llama-3-8b-instruct.
	// Metered through the unified AI spine + emitted to the one gen_ai span plane.
	// UNTYPED: the request body is forwarded to the model verbatim and the response
	// may be image or audio bytes under Cloudflare's own content type. See aiRun.
	g.Post("/ai/run/*", o.aiRun)

	// R2 — account-scoped buckets.
	zip.Get(g, "/r2/buckets", o.r2BucketList)
	zip.Post(g, "/r2/buckets", o.r2BucketCreate)
	zip.Delete(g, "/r2/buckets/:bucket", o.r2BucketDelete)

	// KV — namespaces and a namespace's key values.
	zip.Get(g, "/kv/namespaces", o.kvNamespaceList)
	zip.Post(g, "/kv/namespaces", o.kvNamespaceCreate)
	zip.Delete(g, "/kv/namespaces/:namespace", o.kvNamespaceDelete)
	// UNTYPED (both): a KV value is opaque bytes under the caller's own content type
	// — the GET relays it raw, the PUT forwards the request body raw. See kvValueGet
	// and kvValuePut.
	g.Get("/kv/namespaces/:namespace/values/:key", o.kvValueGet)
	g.Put("/kv/namespaces/:namespace/values/:key", o.kvValuePut)
	zip.Delete(g, "/kv/namespaces/:namespace/values/:key", o.kvValueDelete)

	// D1 — databases and a query against one.
	zip.Get(g, "/d1/databases", o.d1DatabaseList)
	zip.Post(g, "/d1/databases", o.d1DatabaseCreate)
	zip.Delete(g, "/d1/databases/:database", o.d1DatabaseDelete)
	zip.Post(g, "/d1/databases/:database/query", o.d1Query)
}

// The four routes above cannot be typed ops — each carries a wire fact the
// declaration cannot express, and relay_wire_test.go names all four with the reason.
// But "cannot be a typed op" is not "must be undocumented". ONE of them still takes
// ordinary JSON, and openapi.Register is the client for exactly that case: it declares
// the body off the very struct the handler binds, so the published contract follows
// the code, and it is pure DESCRIPTION — no route, status, field or byte moves.
// Without it that route reaches every generated SDK with no request shape at all,
// indistinguishable from a route that takes no body.
//
// The other three have nothing to declare because they are not JSON on the wire: the
// two KV value routes carry opaque bytes under the caller's own content type, and an
// /ai/run body is whatever the chosen model takes.
//
// The response side is cfResult, whose custom marshaler makes it honestly
// UNCONSTRAINED (openapi/register.go) — the payload is Cloudflare's, and this plane
// deliberately does not model Cloudflare's shapes.
//
// init, not routes: Register panics on a duplicate declaration, and routes runs once
// per Mount.
// The same argument applies to the PROSE, and with none of the "nothing to
// declare" exceptions: a body a route does not take is a fact only one of them
// has, but "what this does, who may call it, what it costs" is a fact all four
// have. Left bare they reached every SDK and the MCP tool list as an operationId
// and a tag — four Cloudflare calls a caller could not tell apart, on a plane whose
// whole point is that the org's OWN token is what moves. openapi.Describe is that
// prose's client, keyed exactly like Register and just as unable to invent a route.
//
// Each description is stated beside the Register it belongs to, so the body and
// the prose for one operation are read and edited as one thing.
func init() {
	openapi.Register("/v1/cloudflare/pages/projects/:project/deployments", "POST", PagesDeploy{}, cfResult{})
	openapi.Describe("/v1/cloudflare/pages/projects/:project/deployments", "POST",
		"Trigger a new Pages deployment for a project",
		"Starts a build and deployment of one Cloudflare Pages project on the org's "+
			"OWN Cloudflare account, and relays Cloudflare's deployment record back. "+
			"`branch` picks what to build; OMITTING it builds the project's production "+
			"branch.\n\n"+
			"A body it cannot parse is IGNORED rather than refused — the deployment falls "+
			"back to the production branch — which is the one rule to get right here and "+
			"the reason this is not a typed op: a typed request would answer 400 where "+
			"this deploys. Requires ORG ADMIN (403 otherwise), and 503 if the org has "+
			"never connected a Cloudflare token.")

	// The three with no body to declare still have prose to state, and these are the
	// ones a caller most needs it for: two carry OPAQUE BYTES rather than JSON, and
	// the third is the only PRICED route on an otherwise free passthrough plane.
	openapi.Describe("/v1/cloudflare/kv/namespaces/:namespace/values/:key", http.MethodGet,
		"Read a Workers KV value as its stored bytes",
		"Answers one KV key's value from the org's OWN Cloudflare account as RAW BYTES "+
			"under the content type it was written with — not wrapped in a JSON envelope, "+
			"which is why this is not a typed op. Any org member may read. A key that "+
			"does not exist is Cloudflare's own 404; an invalid namespace, or a key that "+
			"is empty, over 512 bytes, not valid UTF-8, or carries a control character, "+
			"is 400; 503 if the org has never connected a Cloudflare token.")
	openapi.Describe("/v1/cloudflare/kv/namespaces/:namespace/values/:key", http.MethodPut,
		"Write a Workers KV value from the request body",
		"Stores one KV key on the org's OWN Cloudflare account. The REQUEST BODY IS THE "+
			"VALUE, forwarded verbatim under the caller's own Content-Type (`text/plain` "+
			"when none is sent), so a value is never re-encoded on the way in — which is "+
			"why this is not a typed op. `expiration` and `expiration_ttl` may ride the "+
			"query string and are passed through to Cloudflare. Requires ORG ADMIN (403 "+
			"otherwise); the same namespace and key validation as the read answers 400; "+
			"503 if the org has never connected a Cloudflare token.")
	openapi.Describe("/v1/cloudflare/ai/run/*", http.MethodPost,
		"Run a Cloudflare Workers AI model and get its output back",
		"Runs a Workers AI model — the model id is the rest of the path, e.g. "+
			"`@cf/meta/llama-3.1-8b-instruct` — on the org's OWN Cloudflare account and "+
			"relays the model's output. The request body is whatever the chosen model "+
			"takes (a prompt, chat messages, a base64 audio clip) and is forwarded "+
			"unchanged; the response is the model's own, which for an image or audio "+
			"model is BYTES under Cloudflare's content type rather than JSON. Both halves "+
			"are why this is not a typed op.\n\n"+
			"It is the ONE PRICED route on this plane, because a run is inference rather "+
			"than passthrough. The org's own token already paid Cloudflare for the "+
			"compute, so Hanzo debits only the thin BYO routing fee — never the full "+
			"inference cost — and meters it on the SAME `ai` product axis and per-project "+
			"caps as every other model call, so Workers AI spend sums with LLM spend. "+
			"The fee has a floor, so every run leaves a usage row even for a modality "+
			"that reports no tokens, and it emits one gen_ai span with "+
			"`gen_ai.system = cloudflare`.\n\n"+
			"Gated by BALANCE, not by the admin bit that guards the destructive verbs "+
			"here: a validated org is enough, and a frozen, broke or over-cap org is "+
			"refused with the fleet-wide 402/503 billing contract BEFORE any byte reaches "+
			"Cloudflare — no run, and no account discovery either. An empty or oversized "+
			"body is 400, as is a model id that is not a plain Cloudflare model path; 503 "+
			"if the org has never connected a Cloudflare token.")
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

// emptyResult is what a call whose envelope carried no `result` answers — a CF
// delete that reports only success. ONE literal, read by both response paths
// (cfResult below and writeResult), so they cannot drift.
const emptyResult = `{"success":true}`

// cfResult is Cloudflare's own response payload, relayed to the caller VERBATIM so
// the upstream shape reaches the platform without field loss. It is opaque BY
// CONSTRUCTION: this plane proxies Cloudflare and deliberately does not model
// Cloudflare's response shapes, so its properties are Cloudflare's, not ours — see
// the Cloudflare API v4 reference for the endpoint behind each op. An envelope that
// carried no result relays {"success":true}.
type cfResult struct{ raw json.RawMessage }

// MarshalJSON emits the upstream payload as-is, which is what makes cfResult a
// relay rather than a model.
func (r cfResult) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return []byte(emptyResult), nil
	}
	return r.raw, nil
}

// relay is the ONE response path for a typed op: run the Cloudflare call and hand
// back its result for verbatim relay, mapping a failure to a recognizable status.
func (cl *client) relay(ctx context.Context, method, path string, body any) (*cfResult, error) {
	var out json.RawMessage
	if err := cl.cfDo(ctx, method, path, body, &out); err != nil {
		return nil, cfErr(err)
	}
	return &cfResult{raw: out}, nil
}

// pass is relay for a route that cannot be a typed op (d1Query): it writes the
// upstream result to the response itself, byte for byte.
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

// authClient is the READ preamble: it resolves the caller's validated org (403 if
// unvalidated — a forged X-Org-Id with no bearer never gets past this) and builds a
// Cloudflare client bound to THAT org's KMS-sealed token (503 if the org has not
// connected Cloudflare or KMS is down). On success it stamps actingOrgHeader with the
// served org. The token detail is logged token-free and never surfaced to the client.
//
// The org comes from the context — principal.OrgFrom, which IS principal.Org's
// answer, parked there by cloud.Bridge — never from an In field: an In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the caller
// asserted for itself.
func (o ops) authClient(ctx context.Context) (*client, string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	tok, err := tokenFor(ctx, org, providerCloudflare, secretAPIToken)
	if err != nil || len(bytes.TrimSpace(tok)) == 0 {
		// err is custody-authored and token-free (not-connected / invalid-org /
		// KMS-down). Log the reason, tell the client only that CF is unavailable.
		o.s.Log.Warn("cloudflare token unavailable", "org", org, "err", err)
		return nil, org, zip.Errorf(http.StatusServiceUnavailable, "cloudflare is not connected for this org")
	}
	// Stamp the org actually served so a per-org caller can prove no tenant comingling.
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader(actingOrgHeader, org)
	}
	return &client{token: string(bytes.TrimSpace(tok)), base: cfAPIBase()}, org, nil
}

// authWrite is the MUTATION preamble (POST/PUT/DELETE): it additionally requires the
// caller be an admin of its OWN org (principal.IsOrgAdmin — NOT SuperAdmin), parity
// with the AdminOnly connector that seals the token. Wielding the token's dangerous
// verbs (a Worker script PUT is arbitrary code on the org's Cloudflare account/domains;
// a Pages project DELETE is production destruction) must match connecting it. The admin
// check is FIRST, so a non-admin is refused before any KMS token read. Reads stay
// validated-org-only via authClient — org members may look, only org admins may change.
//
// Org-admin-ness lives in a header (X-User-IsOrgAdmin) that principal.OrgFrom does
// not carry, so this one predicate needs the REQUEST. It fails closed off the HTTP
// path — no request, no attested caller, no mutation.
func (o ops) authWrite(ctx context.Context) (*client, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.IsOrgAdmin(c) {
		return nil, "", zip.ErrForbidden("this action requires org admin")
	}
	return o.authClient(ctx)
}

// acctClient resolves BOTH the caller-org client and its account id — the
// account-scoped READ preamble every Pages/Workers/Workers-AI/R2/KV/D1 handler shares,
// so the auth+account dance is written once. Zone-scoped handlers (workers routes,
// zones, analytics) need no account and use authClient directly.
func (o ops) acctClient(ctx context.Context) (*client, string, error) {
	cl, org, err := o.authClient(ctx)
	if err != nil {
		return nil, "", err
	}
	acct, err := cl.resolveAccount(ctx, org)
	return cl, acct, err
}

// acctWrite is acctClient for a mutation: org-admin FIRST (authWrite), then account.
func (o ops) acctWrite(ctx context.Context) (*client, string, error) {
	cl, org, err := o.authWrite(ctx)
	if err != nil {
		return nil, "", err
	}
	acct, err := cl.resolveAccount(ctx, org)
	return cl, acct, err
}

// resolveAccount resolves the Cloudflare account id for account-scoped endpoints
// (Pages / Workers). Order: (1) an explicit, validated ?account= override wins (for an
// org whose token spans multiple accounts); (2) the account captured at connect time
// (the connection's ExternalID) — no per-call round-trip and deterministic for a
// multi-account token; (3) only if none is stored, discover it live from the token's
// own /accounts. Every candidate is validated 32-hex so it can never inject path
// structure. Fails closed (400) when nothing yields a usable account.
//
// The ?account= override is read off the REQUEST rather than modeled as an In
// field, because zip binds an In field from the body as well as the URL: modeling
// it would let a POST body name the account, which is not what this route accepts
// today.
func (cl *client) resolveAccount(ctx context.Context, org string) (string, error) {
	if c, ok := cloud.Request(ctx); ok {
		if a := strings.TrimSpace(c.Query("account")); a != "" {
			if !idRE.MatchString(a) {
				return "", zip.ErrBadRequest("account must be a 32-character hex id")
			}
			return url.PathEscape(a), nil
		}
	}
	if conn, ok := connectionFor(org, providerCloudflare, ""); ok {
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

// seg validates one caller-supplied path value against re (so it can never smuggle
// path structure into the upstream Cloudflare URL) and returns it url.PathEscape'd,
// ready to concatenate into a CF path. It is the ONE gate for every name/id segment
// this plane forwards, whether the value arrived on a typed In or off the request.
func seg(name, v string, re *regexp.Regexp) (string, error) {
	v = strings.TrimSpace(v)
	if !re.MatchString(v) {
		return "", zip.ErrBadRequest(name + " is invalid")
	}
	return url.PathEscape(v), nil
}

// pathSeg is seg over a route param, for the routes that are not typed ops.
func pathSeg(c *zip.Ctx, name string, re *regexp.Regexp) (string, error) {
	return seg(name, c.Param(name), re)
}

// cfErr maps a Cloudflare call failure to a client-facing HTTP error, propagating a
// recognizable upstream status (404/400/403/409) so a proxied not-found is not
// mis-reported as a 502, and defaulting to 502 Bad Gateway otherwise. The message is
// Cloudflare's own (token-free by construction), never this process's token.
func cfErr(err error) error {
	if ce, ok := errors.AsType[*cfError](err); ok {
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

// forward builds a "?..."-encoded upstream query string from an ALLOWLISTED set of
// caller values, so a read op can forward pagination/window params (page, per_page,
// since, until, …) without opening arbitrary passthrough. Values are
// url.Values-escaped and the upstream HOST + PATH are fixed by the caller, so a
// hostile value stays a query value — it can inject neither path structure nor a
// different host (no SSRF). Empty and overlong (>256 bytes) values are dropped.
func forward(vals map[string]string) string {
	q := url.Values{}
	for k, v := range vals {
		if v = strings.TrimSpace(v); v != "" && len(v) <= 256 {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// query is forward over inbound request query keys, for the routes that are not
// typed ops.
func query(c *zip.Ctx, keys ...string) string {
	vals := make(map[string]string, len(keys))
	for _, k := range keys {
		vals[k] = c.Query(k)
	}
	return forward(vals)
}

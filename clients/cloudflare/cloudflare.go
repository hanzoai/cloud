// Package cloudflare is the per-org Cloudflare asset plane for the unified Hanzo
// Cloud binary — the /v1/integrations/cloudflare/* surface that manages an org's Cloudflare
// Pages, Workers, and (Phase 2) R2/KV/D1 through the SAME per-org, KMS-sealed API
// token the org connected via clients/integrations. It is a sibling of hanzodns
// (which owns /v1/dns as a separate CoreDNS process): both drive Cloudflare with an
// org's own scoped token, so the platform never reaches Cloudflare with a global
// env token again — one token, one custody boundary, one org.
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

// cfHTTPClient is the shared client for every Cloudflare call. A bounded timeout so a
// slow/hung Cloudflare never wedges a request goroutine.
var cfHTTPClient = &http.Client{Timeout: 30 * time.Second}

var (
	// nameRE bounds a Cloudflare NAME path segment (Pages project, Worker script,
	// custom-domain name/id, bucket, namespace, database). It is validated before it
	// is folded into an upstream URL so a hostile value can never smuggle path
	// structure (a `/` or `..`) into the Cloudflare request.
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	// idRE bounds a Cloudflare 32-hex ID path segment (account id, zone id, route id).
	idRE = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
)

// state is this subsystem's own data — none. Token custody lives in integrations;
// the account id is resolved per request. So the value is the shared Base only.
type state struct{}

// Mount wires /v1/integrations/cloudflare/* onto app. The subsystem is stateless (no store, no
// goroutine): it reads the per-org token in-process per request and proxies to
// Cloudflare, so there is nothing to build or tear down.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "cloudflare",
		func(cloud.Base) (state, error) { return state{}, nil },
		routes)
}

// routes registers the /v1/integrations/cloudflare surface. Every route runs through authClient
// (validated-org gate + fail-closed per-org token) FIRST, so no route is a softer
// target than another. Pages + Workers are WIRED; R2/KV/D1 are typed Phase-2 stubs
// that answer an honest 501 (never a fake success).
func routes(app *zip.App, s *cloud.Service[state]) {
	// Pages (wired) — account-scoped.
	app.Get("/v1/integrations/cloudflare/pages/projects", cloud.Handle(s, pagesList))
	app.Post("/v1/integrations/cloudflare/pages/projects", cloud.Handle(s, pagesCreate))
	app.Get("/v1/integrations/cloudflare/pages/projects/:project", cloud.Handle(s, pagesGet))
	app.Delete("/v1/integrations/cloudflare/pages/projects/:project", cloud.Handle(s, pagesDelete))
	app.Post("/v1/integrations/cloudflare/pages/projects/:project/deployments", cloud.Handle(s, pagesDeploy))
	app.Post("/v1/integrations/cloudflare/pages/projects/:project/domains", cloud.Handle(s, pagesDomainAdd))
	app.Delete("/v1/integrations/cloudflare/pages/projects/:project/domains/:domain", cloud.Handle(s, pagesDomainDelete))

	// Workers (wired) — scripts + workers.dev subdomain are account-scoped; routes
	// are zone-scoped.
	app.Get("/v1/integrations/cloudflare/workers/scripts", cloud.Handle(s, workersScriptList))
	app.Put("/v1/integrations/cloudflare/workers/scripts/:script", cloud.Handle(s, workersScriptPut))
	app.Delete("/v1/integrations/cloudflare/workers/scripts/:script", cloud.Handle(s, workersScriptDelete))
	app.Post("/v1/integrations/cloudflare/workers/scripts/:script/subdomain", cloud.Handle(s, workersScriptSubdomainSet))
	app.Get("/v1/integrations/cloudflare/workers/subdomain", cloud.Handle(s, workersSubdomainGet))
	app.Get("/v1/integrations/cloudflare/workers/zones/:zone/routes", cloud.Handle(s, workersRouteList))
	app.Post("/v1/integrations/cloudflare/workers/zones/:zone/routes", cloud.Handle(s, workersRouteCreate))
	app.Delete("/v1/integrations/cloudflare/workers/zones/:zone/routes/:route", cloud.Handle(s, workersRouteDelete))

	// R2 / KV / D1 (Phase-2 stubs) — routes + typed provider methods exist; bodies
	// ship in Phase 2. Each answers an honest 501, never a misleading 200.
	app.Get("/v1/integrations/cloudflare/r2/buckets", cloud.Handle(s, r2BucketList))
	app.Post("/v1/integrations/cloudflare/r2/buckets", cloud.Handle(s, r2BucketCreate))
	app.Delete("/v1/integrations/cloudflare/r2/buckets/:bucket", cloud.Handle(s, r2BucketDelete))
	app.Get("/v1/integrations/cloudflare/kv/namespaces", cloud.Handle(s, kvNamespaceList))
	app.Post("/v1/integrations/cloudflare/kv/namespaces", cloud.Handle(s, kvNamespaceCreate))
	app.Delete("/v1/integrations/cloudflare/kv/namespaces/:namespace", cloud.Handle(s, kvNamespaceDelete))
	app.Get("/v1/integrations/cloudflare/d1/databases", cloud.Handle(s, d1DatabaseList))
	app.Post("/v1/integrations/cloudflare/d1/databases", cloud.Handle(s, d1DatabaseCreate))
	app.Delete("/v1/integrations/cloudflare/d1/databases/:database", cloud.Handle(s, d1DatabaseDelete))
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

// cfDo performs a Cloudflare API v4 call, JSON-encoding body, unwrapping the
// {success, errors, result} envelope, and decoding result into out. It fails closed:
// a transport error, an unsuccessful envelope, or a non-2xx status yields an error,
// and the error NEVER contains the token. (The hanzodns cfDo shape, verbatim.)
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
	return cl.do(ctx, method, path, contentType, reader, out)
}

// cfUpload performs a Cloudflare call with a pre-built body + content type (the
// multipart Worker-script upload), sharing cfDo's fail-closed envelope handling.
func (cl *client) cfUpload(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	return cl.do(ctx, method, path, contentType, bytes.NewReader(body), out)
}

// do is the shared request core for cfDo and cfUpload: Bearer-only auth, bounded
// response read, envelope unwrap, fail-closed. Token appears ONLY in the header.
func (cl *client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, cl.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cl.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		// Never wrap err: a transport error can echo the request URL but never the
		// header. Keep the message token-free regardless.
		return fmt.Errorf("cloudflare request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var env cfEnvelope
	if len(data) > 0 {
		_ = json.Unmarshal(data, &env)
	}
	if err := env.err(resp.StatusCode); err != nil {
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

// actingOrgHeader is stamped on every SERVED /v1/integrations/cloudflare response with the org
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

// ── Phase-2 stub plumbing ───────────────────────────────────────────────────────

// errPhase2 marks a provider capability whose route + typed method exist but whose
// body ships in Phase 2. stubResult maps it to an honest 501 — never a fake 200, so
// a caller is never misled into thinking a no-op succeeded.
var errPhase2 = errors.New("cloudflare: not yet implemented (phase 2)")

// stubResult maps a Phase-2 provider seam's result to the wire: errPhase2 → 501, any
// real error surfaces as-is, and even a nil still yields 501 (a stub can never report
// success). This guarantees a stub route NEVER returns a misleading 200.
func stubResult(c *zip.Ctx, capability string, err error) error {
	if err != nil && !errors.Is(err, errPhase2) {
		return cfErr(err)
	}
	return zip.Errorf(http.StatusNotImplemented, "cloudflare %s is not yet wired (phase 2)", capability)
}

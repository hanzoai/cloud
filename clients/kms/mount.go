// mount.go exposes the embedded luxfi/kms secrets-manager as /v1/kms/* on the
// unified Hanzo Cloud binary (HIP-0106) — the REST face of this package.
//
// It re-declares luxfi/kms's REST surface (cmd/kms is package main with no
// mountable handler) on cloud's Fiber app, backed by the SAME embedded
// SecretStore the in-process cloud.KMSClient uses (Client, this package, handed
// through deps.KMS), and gated by cloud's ONE auth boundary (SanitizeIdentity →
// c.Org()/c.IsAdmin()) — never a parallel JWT stack.
//
//	GET    /v1/kms/health                 — real probe (503 in health-only mode); public
//	GET    /v1/kms/config                 — SPA runtime config;                    public
//	GET    /v1/kms/orgs/:org/secrets      — list a path's secret metadata;         JWT, org-scoped
//	GET    /v1/kms/orgs/:org/secrets/+    — read one secret value;                 JWT, org-scoped
//	POST   /v1/kms/orgs/:org/secrets      — upsert a secret (sealed);              JWT, org-scoped
//	DELETE /v1/kms/orgs/:org/secrets/+    — delete a secret;                       JWT, org-scoped
//
// ORG SCOPING — {org} must equal the caller's validated org (c.Org()); a global
// admin (c.IsAdmin()) may act on any org. The org is folded into the store PATH
// as /orgs/{org}{subpath}, so one org can never address another org's records.
// This mirrors clients/paas and clients/admin.
package kms

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// state is kms's own data; shared deps live in the embedded cloud.Base. It holds
// the CONCRETE embedded *Client the routes serve from — distinct from Base.KMS,
// the KMSClient interface — plus the IAM token-broker URL.
//
// A nil kms means KMS is not co-resident in this process (secrets served
// out-of-process or disabled); the subsystem then mounts only the honest
// fail-closed health/config so the binary never pretends to host secrets it cannot.
//
// iamTokenURL is the IAM client_credentials endpoint the /v1/kms/auth/login broker
// exchanges a caller's clientId/clientSecret at. Empty ⇒ login fails closed (503):
// cloud is NOT a token issuer, so with no IAM to broker to there is no way to mint
// a validatable bearer.
type state struct {
	kms         *Client
	iamTokenURL string
}

// Mount wires /v1/kms/* onto app. The concrete-client cast (deps.KMS → *Client),
// deps.IAMIssuer and the conditional (health-only) route set make this a direct
// construction (cloud.NewBase), not cloud.Mount.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("kms.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("kms.Mount: nil deps.Logger")
	}

	// deps.KMS is the in-process Client (built by the factory this package
	// registers, filled by build.go's BuildDeps) when kms is co-resident. Anything
	// else (RPC/disabled stub) means secrets are served elsewhere, so the REST
	// surface mounts health/config only.
	kc, _ := deps.KMS.(*Client)
	// tokenURL is IAM's client_credentials endpoint the login broker exchanges a
	// per-tenant machine credential at. It MUST be reachable FROM INSIDE THE CLUSTER:
	// the broker runs in-cluster and the public issuer host (e.g. https://hanzo.id) is
	// fronted by Cloudflare, which 403s a server-side (non-browser) loopback POST — so
	// brokering against the PUBLIC issuer URL fails 401 and the whole per-tenant KMS
	// secret sync silently stays pending (root-caused 2026-07-04: in-cluster POST to
	// https://hanzo.id/v1/iam/oauth/token → 403, while http://iam.hanzo.svc/... → 200).
	// Prefer, in order: an explicit override (CLOUD_KMS_IAM_TOKEN_URL), the in-cluster
	// IAM service base (IAM_URL — already wired to http://iam.hanzo.svc for JWKS), then
	// the public issuer as a last resort (single-process / no split-horizon deploys).
	tokenURL := strings.TrimSpace(os.Getenv("CLOUD_KMS_IAM_TOKEN_URL"))
	if tokenURL == "" {
		if base := strings.TrimRight(strings.TrimSpace(os.Getenv("IAM_URL")), "/"); base != "" {
			tokenURL = base + "/v1/iam/oauth/token"
		}
	}
	if tokenURL == "" {
		if iss := strings.TrimRight(strings.TrimSpace(deps.IAMIssuer), "/"); iss != "" {
			tokenURL = iss + "/v1/iam/oauth/token"
		}
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "kms"), State: state{kms: kc, iamTokenURL: tokenURL}}

	// Plain /v1/kms surface. The /v1/kms/auth login broker below keeps its OWN
	// group because it carries a per-source-IP rate-limit middleware this group
	// must not apply.
	g := app.Group("/v1/kms")
	g.Get("/health", cloud.Handle(s, health))
	g.Get("/config", configHandler(deps))
	// The login broker is PUBLIC (it IS the credential exchange) and independent of
	// the local store: the kms-operator POSTs its per-tenant clientId/clientSecret
	// here to mint the owner-scoped IAM bearer it then carries on the org-scoped
	// secret reads. Mounted even in health-only mode so the auth handshake is
	// consistent; it fails closed (503) when no IAM issuer is configured.
	//
	// Because it is public, unauthenticated, and fans out to IAM, it is the ONE route
	// rate-limited PER SOURCE IP (credential-stuffing-through-cloud) — via a scoped
	// group so the limiter touches only this path — while the outbound loginHTTPClient
	// additionally caps concurrent IAM connections (login.go). Keyed on the source IP,
	// not the org: this route runs before any validated principal exists, so there is
	// no owner to key on (and X-Org-Id has already been stripped by SanitizeIdentity).
	// The key is the REAL TCP peer (cloud sets no trusted-proxy header, so a forged
	// X-Forwarded-For cannot inflate the key space), and cloud-api is fronted by the
	// gateway/ingress — so the limiter's per-key bucket map is bounded by that small
	// peer set, not by arbitrary internet IPs. loginMaxConnsPerHost is the hard bound
	// on the actual IAM fan-out regardless.
	app.Group("/v1/kms/auth", middleware.RateLimit(middleware.RateLimitConfig{
		Limit:  loginRateLimit,
		Window: loginRateWindow,
		KeyFn:  func(c *zip.Ctx) string { return c.Fiber().IP() },
	})).Post("/login", cloud.Handle(s, login))

	if kc == nil {
		s.Log.Warn("kms REST mounted health-only: no in-process KMS client (secrets served out-of-process or disabled)")
		return nil
	}

	// Value routes use the REQUIRED-greedy `+` (one-or-more), not the
	// optional-greedy `*`: fiber's `*` also matches an empty tail, so
	// `/secrets/*` swallows the bare `GET .../secrets` list path and answers it
	// from getSecret (400 "secret name is required"), leaving listSecrets
	// unreachable. `+` requires a non-empty name, so the bare path falls through
	// to the exact list route.
	g.Get("/orgs/:org/secrets", guard(s, cloud.Handle(s, listSecrets)))
	g.Get("/orgs/:org/secrets/+", guard(s, cloud.Handle(s, getSecret)))
	g.Post("/orgs/:org/secrets", guard(s, cloud.Handle(s, putSecret)))
	g.Delete("/orgs/:org/secrets/+", guard(s, cloud.Handle(s, deleteSecret)))

	s.Log.Info(
		"kms subsystem mounted",
		"prefix", "/v1/kms",
		"ready", kc.Ready(),
		"signing", kc.SigningConfigured(),
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// init wires this package into cloud twice, both under the clean id "kms":
//
//   - cloud.Register mounts the /v1/kms/* subsystem at order 10 — KMS's reserved
//     slot: it mounts before every dependent subsystem so deps.KMS is a live
//     in-process client by the time authz/commerce/ai mount. It serves its OWN
//     fail-closed GET /v1/kms/health (Mount), so it registers with
//     cloud.HealthOwner: Serve's generic liveness loop skips a HealthOwner, so the
//     always-ok route never shadows the real probe with a fake 200.
//   - cloud.RegisterKMSClientFactory hands build.go's BuildDeps the embedded-client
//     constructor so deps.KMS is filled BEFORE MountAll WITHOUT cloud importing this
//     package — the inversion that lets the KMS library (Client, New) and its REST
//     surface share one package with no cloud⇄kms import cycle.
//
// Enable with --enable=kms, or leave --enable empty for the default all-on bundle.
func init() {
	cloud.RegisterKMSClientFactory(newEmbeddedClient)
}

// newEmbeddedClient builds the in-process embedded KMS client from cloud Config.
// Registered as cloud's KMS client factory (init) so BuildDeps can populate
// deps.KMS before MountAll. A store-open failure returns the error; build.go then
// fails closed to the disabled stub rather than crashing the binary.
func newEmbeddedClient(cfg *cloud.Config, log luxlog.Logger) (cloud.KMSClient, error) {
	c, err := New(Config{
		DataDir:      cfg.DataDir,
		MasterKeyB64: cfg.KMSMasterKeyRef,
		MPCAddr:      cfg.KMSMPCAddr,
		MPCVaultID:   cfg.KMSMPCVaultID,
		// Reader HA role opens the per-org KMS files READ-ONLY (mutations fail
		// closed); per-org SQLite is WAL-shareable, so no exclusive lock is taken.
		// Writer (default) opens writable exactly as before.
		ReadOnly: cfg.Role.IsReader(),
	}, log)
	if err != nil {
		return nil, err
	}
	log.Info("deps.KMS → in-process (embedded luxfi/kms)", "ready", c.Ready(), "signing", c.SigningConfigured())
	return c, nil
}

// guard wraps a secrets handler with the org-scope gate. Fail-closed: a request
// whose validated org is neither {org} nor a SuperAdmin is refused 403 before
// the store is touched; an unconfigured master key yields 503.
//
// The org match is EXACT (==), not case-folded: this mirrors the platform's own
// tenant boundary (SanitizeIdentity gates admin on `owner == adminOrg`, and
// X-Org-Id is the raw owner claim), and it keeps the authz check and the store
// path in lockstep — orgPath folds :org into /orgs/{org} verbatim, so a
// case-insensitive authz check would let org "Acme" reach org "acme"'s namespace.
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return func(ctx *zip.Ctx) error {
		org := reqOrg(ctx)
		if !validOrg(org) {
			return zip.ErrBadRequest("org must be a DNS-1123 label")
		}
		if !principal.Validated(ctx) {
			// No validated principal. The identity middleware RESTORES a client
			// X-Org-Id on the bearer-less path, so ctx.Org() could equal a forged
			// :org and defeat the equality check below — an off-gateway caller
			// could read another org's secrets with no credential. Refuse here.
			return zip.ErrForbidden("no validated principal")
		}
		if !ctx.IsAdmin() && ctx.Org() != org {
			return zip.ErrForbidden("caller may only access its own org's secrets")
		}
		if !s.State.kms.Ready() {
			return zip.Errorf(http.StatusServiceUnavailable, "%s", ErrMasterKeyMissing.Error())
		}
		return h(ctx)
	}
}

// ── health + config ────────────────────────────────────────────────────────────

// health is a REAL probe: 200 only when the store is open AND a master key is
// configured; 503 + the honest reason in health-only mode. Not JWT-gated —
// liveness must be probe-able by the platform without a token.
func health(s *cloud.Service[state], ctx *zip.Ctx) error {
	res := map[string]any{"service": "kms", "status": "ok"}
	if s.State.kms == nil {
		res["status"], res["ready"] = "degraded", false
		res["error"] = "no in-process KMS client (secrets served out-of-process or disabled)"
		return ctx.JSON(http.StatusServiceUnavailable, res)
	}
	res["signing"] = s.State.kms.SigningConfigured()
	if !s.State.kms.Ready() {
		res["status"], res["ready"] = "degraded", false
		res["error"] = ErrMasterKeyMissing.Error()
		return ctx.JSON(http.StatusServiceUnavailable, res)
	}
	res["ready"] = true
	return ctx.JSON(http.StatusOK, res)
}

// configHandler serves the KMS console SPA's runtime config (the OIDC issuer the
// console logs in against + the KMS API base). Kept under the /v1/kms namespace
// (not /v1/admin) so a gateway that admin-gates the /v1/admin/* prefix cannot
// block the console's legitimate public config fetch. No secrets, so it is public.
func configHandler(deps cloud.Deps) zip.Handler {
	issuer := strings.TrimRight(strings.TrimSpace(deps.IAMIssuer), "/")
	return func(ctx *zip.Ctx) error {
		return ctx.JSON(http.StatusOK, map[string]any{
			"brand":     deps.Brand,
			"issuer":    issuer,
			"apiBase":   "/v1/kms",
			"loginPath": "/v1/kms/auth/login",
		})
	}
}

// ── secrets CRUD (org-scoped, sealed) ──────────────────────────────────────────

// secretPutRequest is the POST body: the secret to upsert. env defaults to
// "default"; path is optional (relative to the org root); name is required.
type secretPutRequest struct {
	Path  string `json:"path"`  // optional subpath under the org, e.g. "/ci"
	Name  string `json:"name"`  // required
	Env   string `json:"env"`   // optional, default "default"
	Value string `json:"value"` // required; sealed before storage
}

// listSecrets returns the metadata (no ciphertext) of the org's secrets at a
// path/env. ?path= narrows to a subpath; ?env= selects the environment.
func listSecrets(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	sub := ctx.Query("path")
	if !ValidSubpath(sub) {
		return zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	metas, err := s.State.kms.List(orgPath(org, sub), env)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"secrets": metas, "total": len(metas)})
}

// getSecret reads one secret value. The trailing wildcard is the sub-path + name
// under the org; ?env= selects the environment. Returns the opened plaintext.
func getSecret(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	path, name, ok := targetOf(org, reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	val, err := s.State.kms.Get(path, name, env)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return zip.ErrNotFound("secret not found")
		}
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"name": name, "env": env, "value": string(val)})
}

// putSecret seals + upserts a secret. Body: {path?, name, env?, value}. The value
// is sealed under a fresh per-secret DEK (master-key-wrapped) before storage —
// plaintext never touches disk.
func putSecret(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	var req secretPutRequest
	if err := json.Unmarshal(ctx.Body(), &req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	name := strings.TrimSpace(req.Name)
	if !validName(name) {
		return zip.ErrBadRequest("'name' is required and must not contain '/', control characters, or exceed 253 bytes")
	}
	if req.Value == "" {
		return zip.ErrBadRequest("'value' is required")
	}
	// env is a first-class component of the storage key
	// (kms/secrets/{path}/{env}/{name}); it can never be aliased. A silent
	// "default" would commit this write to a bucket that project/env/path
	// readers (the kms-operator, cluster syncs) never resolve — the exact split
	// that let an IAM z-password land in env=default while prod kept serving the
	// stale value. Writes fail loud; reads/deletes keep the envOr compat default
	// (a read/delete can't plant a value another reader later trusts, and legacy
	// readers that omit env must keep working).
	env := strings.TrimSpace(req.Env)
	if env == "" {
		return zip.ErrBadRequest(`'env' is required — there is no default. A silent default would split this write from the project/env/path record that readers resolve.`)
	}
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	if !ValidSubpath(req.Path) {
		return zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	path := orgPath(org, req.Path)
	if err := s.State.kms.Put(path, name, env, []byte(req.Value)); err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"stored": true, "name": name, "env": env})
}

// deleteSecret removes one secret. The trailing wildcard is the sub-path + name.
func deleteSecret(s *cloud.Service[state], ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	path, name, ok := targetOf(org, reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	if err := s.State.kms.Delete(path, name, env); err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return zip.ErrNotFound("secret not found")
		}
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"deleted": true, "name": name, "env": env})
}

// ── path helpers ───────────────────────────────────────────────────────────────

func reqOrg(ctx *zip.Ctx) string { return strings.TrimSpace(ctx.Param("org")) }

// reqWildcard returns the trailing "+" segment of a /secrets/+ route, trimmed of
// surrounding slashes. This is the secret's sub-path + name under the org.
func reqWildcard(ctx *zip.Ctx) string {
	return strings.Trim(strings.TrimSpace(ctx.Param("+")), "/")
}

// validOrg accepts a DNS-1123-ish label. It is the tenant-isolation boundary
// folded into the store path, so it is validated strictly at the edge.
func validOrg(org string) bool {
	if org == "" || len(org) > 63 {
		return false
	}
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// The key-shape validators live in ONE place — this package — so the HTTP
// boundary and the in-process store methods enforce identically (DRY). The REST
// handlers reuse ValidSegment / ValidSubpath here to return a specific 400 early,
// before the request reaches the store.
func validName(s string) bool { return ValidSegment(s, MaxNameLen) }
func validEnv(s string) bool  { return ValidSegment(s, MaxEnvLen) }

// orgPath folds an org + an optional relative subpath into the store path,
// namespacing every org under /orgs/{org}. "" subpath → /orgs/{org}.
func orgPath(org, sub string) string {
	base := "/orgs/" + org
	sub = strings.Trim(strings.TrimSpace(sub), "/")
	if sub == "" {
		return base
	}
	return base + "/" + sub
}

// targetOf splits a /secrets/+ wildcard (sub-path + name) into the validated
// store (path, name): the last segment is the name, the rest is the sub-path
// folded under the org. "DB" → (/orgs/{org}, DB); "ci/DB" → (/orgs/{org}/ci, DB).
// Returns ok=false when the name or sub-path fails the boundary validators, so
// the caller can reject the request rather than key a malformed record.
func targetOf(org, sub string) (path, name string, ok bool) {
	var subpath string
	if slash := strings.LastIndex(sub, "/"); slash >= 0 {
		subpath, name = sub[:slash], sub[slash+1:]
	} else {
		name = sub
	}
	if !validName(name) || !ValidSubpath(subpath) {
		return "", "", false
	}
	return orgPath(org, subpath), name, true
}

// envOr returns env or the "default" environment when empty. defaultEnv is
// defined in kms.go (the library face) — the ONE place the fallback lives.
func envOr(env string) string {
	if e := strings.TrimSpace(env); e != "" {
		return e
	}
	return defaultEnv
}

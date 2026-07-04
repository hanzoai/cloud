// Package kms is the Fiber-facing subsystem that exposes the embedded luxfi/kms
// secrets-manager as /v1/kms/* on the unified Hanzo Cloud binary (HIP-0106).
//
// It re-declares luxfi/kms's REST surface (cmd/kms is package main with no
// mountable handler) on cloud's Fiber app, backed by the SAME embedded
// SecretStore the in-process cloud.KMSClient uses (clients/kms.Client,
// built in build.go and handed through deps.KMS), and gated by cloud's ONE auth
// boundary (SanitizeIdentity → c.Org()/c.IsAdmin()) — never a parallel JWT stack.
//
//	GET    /v1/kms/health                 — real probe (503 in health-only mode); public
//	GET    /v1/kms/config                 — SPA runtime config;                    public
//	GET    /v1/kms/orgs/:org/secrets      — list a path's secret metadata;         JWT, org-scoped
//	GET    /v1/kms/orgs/:org/secrets/*    — read one secret value;                 JWT, org-scoped
//	POST   /v1/kms/orgs/:org/secrets      — upsert a secret (sealed);              JWT, org-scoped
//	DELETE /v1/kms/orgs/:org/secrets/*    — delete a secret;                       JWT, org-scoped
//
// ORG SCOPING — {org} must equal the caller's validated org (c.Org()); a global
// admin (c.IsAdmin()) may act on any org. The org is folded into the store PATH
// as /orgs/{org}{subpath}, so one org can never address another org's records.
// This mirrors clients/paassvc and clients/admin.
package kmssvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// svc holds the embedded KMS client the routes serve from. A nil client means
// KMS is not co-resident in this process (secrets served out-of-process or
// disabled); the subsystem then mounts only the honest fail-closed health/config
// so the binary never pretends to host secrets it cannot.
//
// iamTokenURL is the IAM client_credentials endpoint the /v1/kms/auth/login broker
// exchanges a caller's clientId/clientSecret at. Empty ⇒ login fails closed (503):
// cloud is NOT a token issuer, so with no IAM to broker to there is no way to mint
// a validatable bearer.
type svc struct {
	kms         *kms.Client
	iamTokenURL string
}

// Mount wires /v1/kms/* onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("kms.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("kms.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "kms")

	// deps.KMS is the in-process kms.Client (build.go pickKMSClient) when kms
	// is co-resident. Anything else (RPC/disabled stub) means secrets are served
	// elsewhere, so the REST surface mounts health/config only.
	kc, _ := deps.KMS.(*kms.Client)
	// tokenURL mirrors build.go's AI M2M identity resolution: {issuer}/v1/iam/oauth/
	// token is IAM's client_credentials endpoint. The login broker exchanges a
	// per-tenant machine-identity credential there for an owner-scoped IAM JWT.
	tokenURL := ""
	if iss := strings.TrimRight(strings.TrimSpace(deps.IAMIssuer), "/"); iss != "" {
		tokenURL = iss + "/v1/iam/oauth/token"
	}
	s := &svc{kms: kc, iamTokenURL: tokenURL}

	app.Get("/v1/kms/health", s.health)
	app.Get("/v1/kms/config", configHandler(deps))
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
	})).Post("/login", s.login)

	if kc == nil {
		log.Warn("kms REST mounted health-only: no in-process KMS client (secrets served out-of-process or disabled)")
		return nil
	}

	app.Get("/v1/kms/orgs/:org/secrets", s.guard(s.listSecrets))
	app.Get("/v1/kms/orgs/:org/secrets/*", s.guard(s.getSecret))
	app.Post("/v1/kms/orgs/:org/secrets", s.guard(s.putSecret))
	app.Delete("/v1/kms/orgs/:org/secrets/*", s.guard(s.deleteSecret))

	log.Info("kms subsystem mounted",
		"prefix", "/v1/kms",
		"ready", kc.Ready(),
		"signing", kc.SigningConfigured(),
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// init registers the subsystem. Registered as "kmssvc" (not "kms") for the same
// reason clients/paassvc uses "paassvc": serve.go auto-mounts a generic
// GET /v1/<name>/health BEFORE MountAll and zip is first-match-wins, so a name of
// "kms" would shadow the real fail-closed /v1/kms/health with a fake 200. The
// name "kmssvc" parks the generic liveness at the unrouted /v1/kmssvc/health and
// lets the real probe own /v1/kms/health. The /v1/kms API prefix is independent
// of the internal subsystem name (paassvc → /v1/paas). Enable with
// --enable=kmssvc, or leave --enable empty for the default all-on bundle.
//
// Order 10 is KMS's reserved slot: it mounts before every dependent subsystem so
// deps.KMS is a live in-process client by the time authz/commerce/ai mount.
func init() {
	cloud.Register("kmssvc", 10, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("kms.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// guard wraps a secrets handler with the org-scope gate. Fail-closed: a request
// whose validated org is neither {org} nor a global admin is refused 403 before
// the store is touched; an unconfigured master key yields 503.
//
// The org match is EXACT (==), not case-folded: this mirrors the platform's own
// tenant boundary (SanitizeIdentity gates admin on `owner == adminOrg`, and
// X-Org-Id is the raw owner claim), and it keeps the authz check and the store
// path in lockstep — orgPath folds :org into /orgs/{org} verbatim, so a
// case-insensitive authz check would let org "Acme" reach org "acme"'s namespace.
func (s *svc) guard(h zip.Handler) zip.Handler {
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
		if !s.kms.Ready() {
			return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
		}
		return h(ctx)
	}
}

// ── health + config ────────────────────────────────────────────────────────────

// health is a REAL probe: 200 only when the store is open AND a master key is
// configured; 503 + the honest reason in health-only mode. Not JWT-gated —
// liveness must be probe-able by the platform without a token.
func (s *svc) health(ctx *zip.Ctx) error {
	res := map[string]any{"service": "kms", "status": "ok"}
	if s.kms == nil {
		res["status"], res["ready"] = "degraded", false
		res["error"] = "no in-process KMS client (secrets served out-of-process or disabled)"
		return ctx.JSON(http.StatusServiceUnavailable, res)
	}
	res["signing"] = s.kms.SigningConfigured()
	if !s.kms.Ready() {
		res["status"], res["ready"] = "degraded", false
		res["error"] = kms.ErrMasterKeyMissing.Error()
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
func (s *svc) listSecrets(ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	sub := ctx.Query("path")
	if !kms.ValidSubpath(sub) {
		return zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	metas, err := s.kms.List(orgPath(org, sub), env)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"secrets": metas, "total": len(metas)})
}

// getSecret reads one secret value. The trailing wildcard is the sub-path + name
// under the org; ?env= selects the environment. Returns the opened plaintext.
func (s *svc) getSecret(ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	path, name, ok := targetOf(org, reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	val, err := s.kms.Get(path, name, env)
	if err != nil {
		if errors.Is(err, kms.ErrSecretNotFound) {
			return zip.ErrNotFound("secret not found")
		}
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"name": name, "env": env, "value": string(val)})
}

// putSecret seals + upserts a secret. Body: {path?, name, env?, value}. The value
// is sealed under a fresh per-secret DEK (master-key-wrapped) before storage —
// plaintext never touches disk.
func (s *svc) putSecret(ctx *zip.Ctx) error {
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
	env := envOr(req.Env)
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	if !kms.ValidSubpath(req.Path) {
		return zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	path := orgPath(org, req.Path)
	if err := s.kms.Put(path, name, env, []byte(req.Value)); err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"stored": true, "name": name, "env": env})
}

// deleteSecret removes one secret. The trailing wildcard is the sub-path + name.
func (s *svc) deleteSecret(ctx *zip.Ctx) error {
	org := reqOrg(ctx)
	env := envOr(ctx.Query("env"))
	if !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	path, name, ok := targetOf(org, reqWildcard(ctx))
	if !ok {
		return zip.ErrBadRequest("secret name is required and must be a clean '/'-separated path")
	}
	if err := s.kms.Delete(path, name, env); err != nil {
		if errors.Is(err, kms.ErrSecretNotFound) {
			return zip.ErrNotFound("secret not found")
		}
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"deleted": true, "name": name, "env": env})
}

// ── path helpers ───────────────────────────────────────────────────────────────

func reqOrg(ctx *zip.Ctx) string { return strings.TrimSpace(ctx.Param("org")) }

// reqWildcard returns the trailing "*" segment of a /secrets/* route, trimmed of
// surrounding slashes. This is the secret's sub-path + name under the org.
func reqWildcard(ctx *zip.Ctx) string {
	return strings.Trim(strings.TrimSpace(ctx.Param("*")), "/")
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

// The key-shape validators live in ONE place — clients/kms — so the HTTP
// boundary and the in-process store methods enforce identically (DRY). The
// subsystem reuses kms.ValidSegment / ValidSubpath here to return a specific
// 400 early, before the request reaches the store.
func validName(s string) bool { return kms.ValidSegment(s, kms.MaxNameLen) }
func validEnv(s string) bool  { return kms.ValidSegment(s, kms.MaxEnvLen) }

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

// targetOf splits a /secrets/* wildcard (sub-path + name) into the validated
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
	if !validName(name) || !kms.ValidSubpath(subpath) {
		return "", "", false
	}
	return orgPath(org, subpath), name, true
}

// envOr returns env or the "default" environment when empty.
func envOr(env string) string {
	if e := strings.TrimSpace(env); e != "" {
		return e
	}
	return defaultEnv
}

// defaultEnv is the secret environment used when a request omits ?env=, matching
// luxfi/kms's REST default.
const defaultEnv = "default"

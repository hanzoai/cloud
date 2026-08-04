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
//	GET    /v1/kms/secrets                — list a path's secret metadata;         JWT
//	GET    /v1/kms/secrets/+              — read one secret value;                 JWT
//	POST   /v1/kms/secrets                — upsert a secret (sealed);              JWT
//	DELETE /v1/kms/secrets/+              — delete a secret;                       JWT
//
// ORG SCOPING — the org is the CALLER'S, read from the validated principal, and
// never named in the URL. It used to be a path segment that had to equal
// c.Org(), which made the tenant caller-selectable and left the guard
// reconciling two sources for one fact; every other subsystem (platform, paas)
// derives it from the identity, and so does this one now.
//
// The org is still folded into the store PATH as /orgs/{org}{subpath} — that is
// the isolation partition, the same role the `org` column plays in every table,
// and it is what keeps one org from addressing another's records.

package kms

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/org"
	"github.com/hanzoai/cloud/openapi"
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

// The prose for this subsystem's seven operations. None of them is a typed op —
// each answers a map assembled in the handler, and two answer the same body under
// two statuses — so there is no doc comment for zipdoc to lift, and the prose is
// declared beside the route table instead.
//
// Written under one rule, because this is the credential broker: a description
// says what an operation DOES and where its answer is scoped, and never implies
// that secret material turns up anywhere but the one response body that exists to
// carry it. The list op returns metadata, the read op returns a value, and neither
// a log line nor an error body ever carries either — that asymmetry is the thing a
// reader most needs stated, so it is stated.
func init() {
	openapi.Describe("/v1/kms/secrets", http.MethodGet,
		"List the secrets your org holds, without their values",
		"Returns the METADATA of the caller's own secrets: each one's name, path, "+
			"environment and sealing scheme. No value and no ciphertext is included — this "+
			"operation exists to enumerate what is held, and reading a value is a separate, "+
			"per-secret call.\n\n"+
			"Scoped to the caller's own org and nothing else, structurally: there is no org "+
			"in the path, the store root is derived from the validated org claim, and a "+
			"caller therefore has no way to name another tenant's namespace. `path` narrows "+
			"to a subpath and `env` selects the environment; both are also accepted under "+
			"the operator's spellings, `secretPath` and `environment`.\n\n"+
			"Admission is fail-closed and in order: a validated member, an org that is a "+
			"DNS-1123 label, and a store holding a master key — 403, 400 and 503 "+
			"respectively, all decided before any record is touched.")
	openapi.Describe("/v1/kms/secrets", http.MethodPost,
		"Store or replace one secret in your org",
		"Upserts one secret under the caller's own org. The value is sealed before it is "+
			"written — a fresh per-secret data key, itself wrapped by the master key — so "+
			"plaintext never reaches disk. The receipt confirms the name and environment "+
			"that were written and does not echo the value.\n\n"+
			"`env` is REQUIRED on a write and has no default, which is the rule most easily "+
			"got wrong here: reads and deletes still fall back to the default environment "+
			"for older callers, but a write must not, because the environment is part of the "+
			"storage key. A silently defaulted write lands in a bucket the readers that "+
			"resolve project, environment and path never look in, and the stale value keeps "+
			"being served — so the write fails loudly instead.\n\n"+
			"`name` is required, `path` is an optional subpath beneath the org root, and the "+
			"org is taken from the validated claim rather than the body. Same fail-closed "+
			"admission as the rest of the secret surface: validated member, well-formed org, "+
			"master key present.")
	openapi.Describe("/v1/kms/secrets/+", http.MethodGet,
		"Read one secret's value",
		"Opens one sealed secret belonging to the caller's own org and returns its value in "+
			"the response body, with the name and environment it was resolved under. This is "+
			"the broker's purpose, and the response body is the ONLY place the value appears "+
			"— it is not logged, and it is never carried in an error.\n\n"+
			"The trailing path is the secret's subpath and name beneath the caller's org "+
			"root; `env` selects the environment and falls back to the default when omitted. "+
			"A secret that is not there is a plain 404 that names nothing about the store.\n\n"+
			"Scoped to the caller's own org and nothing else: there is no org in the path, so "+
			"another tenant's secret is not merely refused, it is unnameable. Admission is "+
			"fail-closed — validated member, well-formed org, master key present — and an "+
			"unconfigured master key is 503 rather than an empty read.")
	openapi.Describe("/v1/kms/secrets/+", http.MethodDelete,
		"Delete one secret from your org",
		"Removes one secret from the caller's own org and confirms the name and environment "+
			"that were removed. Deleting a secret that is not there is a 404, not a silent "+
			"success, so a caller can tell a real deletion from a typo.\n\n"+
			"The trailing path is the secret's subpath and name beneath the caller's org "+
			"root, and `env` selects the environment, defaulting when omitted. Scoped to the "+
			"caller's own org — the store root comes from the validated claim, never from the "+
			"request — under the same fail-closed admission as the reads.")
	openapi.Describe("/v1/kms/auth/login", http.MethodPost,
		"Exchange a machine credential for an IAM bearer token",
		"Takes a tenant's machine credential — a client id and client secret — and returns "+
			"an owner-scoped IAM access token with its lifetime, which is the bearer the "+
			"caller then carries on the org-scoped secret operations.\n\n"+
			"It is deliberately public and unauthenticated, because it IS the credential "+
			"exchange and runs before any principal exists. That makes it the one route in "+
			"this subsystem rate-limited PER SOURCE IP, keyed on the real TCP peer rather "+
			"than on any caller-supplied header.\n\n"+
			"The submitted secret is never logged and never echoed, and failures collapse to "+
			"one clean status with no upstream detail: 401 when the credential does not "+
			"authenticate, 502 when the identity provider is unreachable, 503 when no issuer "+
			"is configured. That is on purpose — a richer error would be a validity oracle "+
			"for guessed credentials.")
	openapi.Describe("/v1/kms/health", http.MethodGet,
		"Whether this broker can actually serve secrets",
		"A real readiness probe, not a liveness stub: 200 only when the store is open AND a "+
			"master key is configured, with `signing` reporting whether signing keys are set "+
			"up too. Anything less answers 503 with `ready:false` and the reason — no "+
			"in-process store, or no master key — which are exactly the two states in which "+
			"the secret operations refuse.\n\n"+
			"Not token-gated, because the platform must be able to probe it without a "+
			"credential. It reports the broker's configuration state only; no secret, no key "+
			"material and no tenant name appears in it.")
	openapi.Describe("/v1/kms/config", http.MethodGet,
		"Runtime configuration for the KMS console",
		"Returns what the console needs before anyone has signed in: the brand, the OIDC "+
			"issuer it authenticates against, the API base for this subsystem and the path "+
			"of the login exchange.\n\n"+
			"Public on purpose, and it holds nothing sensitive — it is deliberately kept "+
			"under this subsystem's own namespace rather than under an admin prefix, so a "+
			"gateway that admin-gates the admin routes cannot break the console's legitimate "+
			"pre-login fetch.")
}

// Mount wires /v1/kms/* onto app. The concrete-client cast (deps.KMS → *Client),
// deps.IAMIssuer and the conditional (health-only) route set make this a direct
// construction (cloud.NewBase), not cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	// The sealed store lives here, so the reads and writes are published here.
	exposeSecrets(deps.KMS)

	if app == nil {
		return fmt.Errorf("kms.Mount: nil app")
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
		if base := cloud.IAMBaseURL(deps.IAMIssuer); base != "" {
			tokenURL = base + "/v1/iam/oauth/token"
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
	// The tenant surface: the caller's OWN secrets. No org in the path.
	g.Get("/secrets", guard(s, cloud.Handle(s, listSecrets)))
	g.Get("/secrets/+", guard(s, cloud.Handle(s, getSecret)))
	g.Post("/secrets", guard(s, cloud.Handle(s, putSecret)))
	g.Delete("/secrets/+", guard(s, cloud.Handle(s, deleteSecret)))

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
func newEmbeddedClient(cfg *cloud.Config, dur *org.Durability, log luxlog.Logger) (cloud.KMSClient, error) {
	c, err := New(Config{
		DataDir:      cfg.DataDir,
		Durable:      dur,
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

// guard wraps a secrets handler with the platform gate (cloud.Member — a
// validated principal, HIP-0519's one predicate set) and then the two facts that
// are this subsystem's OWN business: the org key must be a storage-safe label,
// and the store must hold a master key. Fail-closed in that order, before any
// record is touched: 403 for an unvalidated caller, 400 for a malformed org, 503
// for an unconfigured key.
//
// The org match is EXACT (==), not case-folded: this mirrors the platform's own
// tenant boundary (SanitizeIdentity gates admin on `owner == adminOrg`, and
// X-Org-Id is the raw owner claim), and it keeps the authz check and the store
// path in lockstep — orgPath folds :org into /orgs/{org} verbatim, so a
// case-insensitive authz check would let org "Acme" reach org "acme"'s namespace.
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return cloud.Guard(cloud.Member, func(ctx *zip.Ctx) error {
		org := reqOrg(ctx)
		if !validOrg(org) {
			return zip.ErrBadRequest("org must be a DNS-1123 label")
		}
		if !s.State.kms.Ready() {
			return zip.Errorf(http.StatusServiceUnavailable, "%s", ErrMasterKeyMissing.Error())
		}
		return h(ctx)
	})
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
	// The KMS operator (the fleet's only machine consumer) spells these
	// `environment` and `secretPath`; this plane's own clients use `env` and
	// `path`. Accept both so one endpoint serves both callers — the operator can
	// be repointed here without a lockstep operator release.
	// An OMITTED env means every environment, and an omitted path means the whole
	// org — this is the enumeration surface, so it must be able to answer "what
	// is in here". envOr's silent default belongs on the single-secret routes,
	// where a coordinate has to be complete; applied here it reported a populated
	// store as empty, because the fleet writes `prod` and the default was
	// `default`. A caller that wants one env still says so.
	env := strings.TrimSpace(firstQuery(ctx, "env", "environment"))
	if env != "" && !validEnv(env) {
		return zip.ErrBadRequest("'env' must not contain '/', control characters, or exceed 63 bytes")
	}
	sub := firstQuery(ctx, "path", "secretPath")
	if !ValidSubpath(sub) {
		return zip.ErrBadRequest("'path' must be '/'-separated non-empty segments without '.', '..', or control characters")
	}
	// Find, not List: the path is a subtree root here, so listing an org returns
	// the org. List is exact-coordinate and stays that way for the credential
	// broker, which must not have its scope widened by a listing change.
	metas, err := s.State.kms.Find(orgPath(org, sub), env)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	// Superset response: `secrets`/`total` for this plane's clients, `names` for
	// the operator, which reads that key. Emitting both means the standalone KMS
	// can be retired without a flag day — a consumer of either shape keeps working.
	names := make([]string, 0, len(metas))
	for _, m := range metas {
		names = append(names, m.Name)
	}
	return ctx.JSON(http.StatusOK, map[string]any{"secrets": metas, "total": len(metas), "names": names})
}

// firstQuery returns the first non-empty value among alias query keys, so one
// handler serves callers that spell the same parameter differently.
func firstQuery(ctx *zip.Ctx, keys ...string) string {
	return firstNonEmpty(ctx.Query, keys...)
}

// firstNonEmpty is firstQuery's lookup rule, taken as a plain function so the
// precedence (primary before alias, empty treated as absent) is testable without
// standing up a request context.
func firstNonEmpty(get func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := get(k); v != "" {
			return v
		}
	}
	return ""
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

// reqOrg is the org whose records a request addresses: always the CALLER'S own,
// from the validated principal. There is no per-request choice of tenant and no
// admin override — a token names one org, and that is the one whose secrets it
// reaches. Cross-org access exists only IN-PROCESS (cloud's own kms.Client, which
// holds the master key), never over HTTP.
func reqOrg(ctx *zip.Ctx) string { return strings.TrimSpace(ctx.Org()) }

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

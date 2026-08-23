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
//	GET    /v1/kms/secrets                — list a path's secret metadata;      member
//	GET    /v1/kms/secrets/+              — read one secret value;              member
//	POST   /v1/kms/secrets                — upsert a secret (sealed);       org admin
//	DELETE /v1/kms/secrets/+              — delete a secret;                org admin
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
	// brand and issuer are what the console needs before anyone signs in. They
	// live here rather than in a closure because a TypedHandler takes no deps
	// parameter and a closure is not a bound method value cmd/zipdoc can lift
	// prose from.
	brand  string
	issuer string
}

// The prose for the TWO operations that are not typed ops, and the bodies they
// carry. Every other route here declares its In and its Out (typed.go), so
// zipdoc lifts its prose from the handler's own doc comment; a description
// stated here as well would be the same fact in two places, and the fold
// replaces a structural operation with the typed one, so the second copy would
// simply never render.
//
// Written under one rule, because this is the credential broker: a description
// says what an operation DOES and where its answer is scoped, and never implies
// that secret material turns up anywhere but the one response body that exists
// to carry it. The list operation returns metadata, the read returns a value,
// and neither a log line nor an error body carries either — that asymmetry is
// the thing a reader most needs stated, so it is stated.
func init() {
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
			"request.\n\n"+
			"Requires ADMIN authority over the org, like the write: destroying a secret is an "+
			"administrative act, and a credential distributed to read one must not be able to "+
			"remove it.")

	// The BODIES those two answer with, declared through the reflection client.
	// Without this each renders as an operationId and a tag and NOTHING else —
	// indistinguishable from a route that returns nothing at all — so every SDK
	// generated off the document offered a secret read with no return type. Both
	// read their input entirely from the URL, so the request half is nil: that is
	// the honest declaration, not an omission.
	openapi.Register("/v1/kms/secrets/+", http.MethodGet, nil, kmsSecret{})
	openapi.Register("/v1/kms/secrets/+", http.MethodDelete, nil, kmsRemoved{})
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
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "kms"), State: state{
		kms:         kc,
		iamTokenURL: tokenURL,
		brand:       deps.Brand,
		issuer:      strings.TrimRight(strings.TrimSpace(deps.IAMIssuer), "/"),
	}}
	o := ops{s: s}

	// Plain /v1/kms surface. The /v1/kms/auth login broker below keeps its OWN
	// group because it carries a per-source-IP rate-limit middleware this group
	// must not apply.
	g := app.Group("/v1/kms")
	// Bridge FIRST: a typed op receives only a context, so the validated org and
	// the request its authority is read off reach it by being parked there.
	// fiber runs middleware in registration order, so this must precede every
	// leaf below — including the ones Serve would otherwise have covered, which
	// no test of this package alone ever runs.
	g.Use(cloud.Bridge())
	// Declared on the GROUP, so each op's path is the prefix composed with its
	// leaf — the same composition the router does, and the identity every
	// projection keys on. For a typed op that is not a change of address either:
	// zip registers every op on the concrete *zip.App with the prefix already
	// composed in, so `zip.Get(g, "/health")` is the route `/v1/kms/health` on
	// the same router the absolute spelling would have reached.
	zip.Get(g, "/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))
	zip.Get(g, "/config", o.config)
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
	//
	// The BODY CAP lives at this door beside the limiter, and for the same
	// reason: both are what a public, pre-identity door accepts, decided before
	// any principal exists and before anything parses a byte. A typed op is
	// handed a decoded value, so by the time it could refuse a 64 MiB body the
	// body has been read — the cap was never the operation's business.
	auth := app.Group("/v1/kms/auth", middleware.RateLimit(middleware.RateLimitConfig{
		Limit:  loginRateLimit,
		Window: loginRateWindow,
		KeyFn:  func(c *zip.Ctx) string { return c.Fiber().IP() },
	}), capBody(maxLoginBodyBytes))
	zip.Post(auth, "/login", o.login)

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
	// READS admit a member; WRITES require admin authority over the org. That split is
	// the estate's rule, not this subsystem's invention — authz states it at the
	// definition of Verb ("cloud gates writes on org-admin authority while admitting
	// members to read") and Role.Admits encodes it (member reads; admin and owner read
	// and write). KMS asks the same question through the same two doors.
	//
	// It lands hardest, and most usefully, on MACHINES. A client_credentials identity
	// carries no membership, so SanitizeIdentity grants it neither admin scope; it can
	// still read the secrets it was issued for and can no longer overwrite or delete
	// one. That is the right authority for a credential whose whole job is to deliver
	// material into a workload, and it is the authority every reader in the fleet
	// actually exercises — a sync reads.
	//
	// The collection is TYPED and the two value routes are not, and the split is
	// the fiber `+` rather than anything about secrets: the document spells a
	// wildcard `{wildcard1}` and zip's own registry spells it `+`, so a typed op
	// there makes the two readings disagree about its path and the fold REFUSES
	// to produce a document at all. typed_wire_test.go proves that by trying it.
	zip.Get(g, "/secrets", o.listSecrets)
	zip.Post(g, "/secrets", o.putSecret)
	g.Get("/secrets/+", guard(s, cloud.Member, cloud.Handle(s, getSecret)))
	g.Delete("/secrets/+", guard(s, cloud.Admin, cloud.Handle(s, deleteSecret)))

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

// guard is the RAW handlers' reading of the one admission door (admits, typed.go),
// so the two value routes and the typed collection ops cannot answer differently
// about who may pass or in what order.
//
// The scope is the ROUTE'S, passed in rather than fixed here, because reading a
// secret and replacing one are different acts and the gate is where that
// difference belongs. Taking it as a parameter also puts the answer at the route
// table, where a reader sees which door each operation is behind without
// following a call.
func guard(s *cloud.Service[state], scope cloud.Scope, h zip.Handler) zip.Handler {
	return func(ctx *zip.Ctx) error {
		if err := admits(s, scope, cloud.AuthorityOf(ctx), reqOrg(ctx)); err != nil {
			return err
		}
		return h(ctx)
	}
}

// ── health + config ────────────────────────────────────────────────────────────

// ── secrets CRUD (org-scoped, sealed) ──────────────────────────────────────────

// secretPutRequest is the POST body: the secret to upsert. env defaults to
// "default"; path is optional (relative to the org root); name is required.
type secretPutRequest struct {
	Path  string `json:"path"`  // optional subpath under the org, e.g. "/ci"
	Name  string `json:"name"`  // required
	Env   string `json:"env"`   // optional, default "default"
	Value string `json:"value"` // required; sealed before storage
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
	return ctx.JSON(http.StatusOK, kmsSecret{Env: env, Name: name, Value: string(val)})
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
	return ctx.JSON(http.StatusOK, kmsRemoved{Deleted: true, Env: env, Name: name})
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

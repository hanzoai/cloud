// mount.go exposes the embedded luxfi/kms secrets-manager as /v1/kms/* on the
// unified Hanzo Cloud binary (HIP-0106) — the REST face of this package.
//
// It re-declares luxfi/kms's REST surface (cmd/kms is package main with no
// mountable handler) on cloud's Fiber app, backed by the SAME embedded
// SecretStore the in-process cloud.Store uses (Client, this package, handed
// through deps.KMS), and admitted by cloud's ONE auth boundary — SanitizeIdentity
// mints the validated principal, cloud.Bridge parks it, and each op reads it
// through principal.OrgFrom and cloud.AuthorityOf — never a parallel JWT stack.
//
//	GET    /v1/kms/health                 — real probe (503 in health-only mode); public
//	GET    /v1/kms/config                 — SPA runtime config;                    public
//	GET    /v1/kms/secrets                — list a path's secret metadata; grant | admin | super
//	GET    /v1/kms/secrets/+              — read one secret value;       grant | super
//	POST   /v1/kms/secrets                — upsert a secret (sealed);    grant | super
//	DELETE /v1/kms/secrets/+              — delete a secret;             grant | super
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
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/secret"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
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
	// grants is the live grant declaration (policy.go) and what the mount last
	// said. Its policy is nil until a document loads, which measures: the rule
	// each caller had before still decides.
	grants *grants
	// audit is the deployment's tamper-evident trail, which admin holds; every
	// value operation is appended to it (record). An access it does not take is
	// refused.
	audit *audit.Recorder
	// trailed is whether the trail took the last record (health reports it).
	trailed *trailed
}

// Mount wires /v1/kms/* onto app. The concrete-client cast (deps.KMS → *Client),
// deps.IAMIssuer and the conditional (health-only) route set make this a direct
// construction (cloud.NewBase), not cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("kms: nil app")
	}
	// The sealed store lives here, so the reads and writes are published here.
	exposeSecrets(deps.KMS)

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
	tokenURL := env.Or("CLOUD_KMS_IAM_TOKEN_URL", "")
	if tokenURL == "" {
		if base := iam.BaseURL(deps.IAMIssuer); base != "" {
			tokenURL = base + "/v1/iam/oauth/token"
		}
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "kms"), State: state{
		kms:         kc,
		iamTokenURL: tokenURL,
		brand:       cloud.Brand(),
		issuer:      strings.TrimRight(strings.TrimSpace(deps.IAMIssuer), "/"),
		audit:       deps.Audit,
		trailed:     &trailed{},
	}}
	o := ops{s: s}

	// Plain /v1/kms surface. The /v1/kms/auth login broker below keeps its OWN
	// group because it carries a per-source-IP rate-limit middleware this group
	// must not apply.
	g := app.Group(prefix)
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
	// composed in, so `g.Get("/health")` is the route `/v1/kms/health` on
	// the same router the absolute spelling would have reached.
	g.Get("/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))
	g.Get("/config", o.config)
	// The login broker is PUBLIC (it IS the credential exchange) and independent of
	// the local store: the kms-operator POSTs its per-tenant clientId/clientSecret
	// here to mint the owner-scoped IAM bearer it then carries on the org-scoped
	// secret reads. Mounted even in health-only mode so the auth handshake is
	// consistent; it fails closed (503) when no IAM issuer is configured.
	//
	// Because it is public, unauthenticated, and fans out to IAM, it is the ONE route
	// rate-limited PER CALLER ADDRESS (credential-stuffing-through-cloud) — via a
	// scoped group so the limiter touches only this path — while the outbound
	// loginHTTPClient additionally caps concurrent IAM connections (login.go). Keyed on
	// the address, not the org: this route runs before any validated principal exists,
	// so there is no owner to key on (and X-Org-Id has already been stripped by
	// SanitizeIdentity).
	//
	// The key is geo.IP, never c.Fiber().IP(): in this subsystem that is the
	// host's socket, so every caller on earth shared ONE bucket — a global 60/min that
	// any one stranger could spend for the kms-operator. In-cluster callers have no
	// address and share the "" bucket. The keyspace is caller-influenced, so the count
	// is a cloud.Window, which charges an IPv6 caller's /64 and its /48 and evicts
	// expired windows; zip's middleware.RateLimit never does. loginMaxConnsPerHost is
	// the hard bound on the actual IAM fan-out regardless.
	//
	// The BODY CAP lives at this endpoint beside the limiter, and for the same
	// reason: both are what a public, pre-identity endpoint accepts, decided before
	// any principal exists and before anything parses a byte. A typed op is
	// handed a decoded value, so by the time it could refuse a 64 MiB body the
	// body has been read — the cap was never the operation's business.
	logins := cloud.NewWindow("kms:login", loginRateLimit, loginRateWindow)
	auth := app.Group(prefix+"/auth", func(c *zip.Ctx) error {
		if !logins.Take(geo.IP(c)) {
			return zip.Errorf(http.StatusTooManyRequests, "rate limit exceeded")
		}
		return c.Continue()
	}, capBody(maxLoginBodyBytes))
	auth.Post("/login", o.login)

	if kc == nil {
		s.Log.Warn("kms REST mounted health-only: no in-process KMS client (secrets served out-of-process or disabled)")
		return nil
	}

	// The grant declaration is loaded BEFORE the value routes exist, so no read is
	// ever decided against a policy that has not been read yet. The last good one
	// is kept in this store, so a restart onto an unusable mount decides as this
	// process did.
	s.State.grants = newGrants(GrantsFile, kc, s.Log)

	// The tenant surface: the caller's OWN secrets. No org in the path.
	//
	// LISTING shows every name to a SuperAdmin (audited) and, outside the orgs
	// the platform runs, to an admin of the org; anyone else sees only the names its own grants
	// open (typed.go lists), and a plain member is refused. A VALUE — read, write or delete — opens only for SuperAdmin holding an
	// access token (audited) or for a holder a declared grant covers (policy.go),
	// decided inside each op (typed.go open) rather than around the route, because
	// an op is reachable without its route. Org admin confers nothing over values:
	// administering an org is self-service, and reading its API keys is not.
	//
	// The collection is registered BEFORE the value routes so the bare listing
	// path keeps its exact match; `+` requires a non-empty tail, so the two
	// cannot overlap in either order (secretLeaf, typed.go).
	g.Get("/secrets", o.listSecrets)
	g.Post("/secrets", o.putSecret)
	g.Get(secretLeaf, o.getSecret)
	g.Delete(secretLeaf, o.deleteSecret)

	s.Log.Info(
		"kms subsystem mounted",
		"prefix", prefix,
		"ready", kc.Ready(),
		"signing", kc.SigningConfigured(),
		"brand", cloud.Brand(),
		"env", cloud.Env(),
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
//     constructor so deps.KMS is filled BEFORE UseAll WITHOUT cloud importing this
//     package — the inversion that lets the KMS library (Client, New) and its REST
//     surface share one package with no cloud⇄kms import cycle.
//
// Enable with --enable=kms, or leave --enable empty for the default all-on bundle.
func init() {
	cloud.RegisterKMSClientFactory(newEmbeddedClient)
}

// newEmbeddedClient builds the in-process embedded KMS client from cloud Config.
// Registered as cloud's KMS client factory (init) so BuildDeps can populate
// deps.KMS before UseAll. A store-open failure returns the error; build.go then
// fails closed to the disabled stub rather than crashing the binary.
func newEmbeddedClient(cfg *cloud.Config, dur *tenure.Durability, log luxlog.Logger) (secret.Store, error) {
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

// ── path helpers ───────────────────────────────────────────────────────────────

// The key-shape validators live in ONE place — this package — so the HTTP
// boundary and the in-process store methods enforce identically (DRY). The REST
// handlers reuse ValidSegment / ValidSubpath here to return a specific 400 early,
// before the request reaches the store.
func validName(s string) bool { return ValidSegment(s, MaxNameLen) }
func validEnv(s string) bool  { return ValidSegment(s, MaxEnvLen) }

// OrgPath renders the path an org's secrets live under — /orgs/<slug>, plus any
// sub-segments — and "" for an org that names nothing. It is the ONE spelling of
// that path; integrations, destinations and the reseal inventory ask here rather
// than each concatenating its own.
//
// The org SEGMENT IS DERIVED, not validated. namespace.Sanitize is this estate's
// fold from an identity's org to a name that can key storage, and its answer is
// always [a-z0-9-]: "../../etc/passwd" keys "etc-passwd-3754d6cb3a38e118", so no
// org can name a path outside its own subtree and the tenant boundary holds by
// construction. A clean lowercase label up to 32 characters is returned unchanged,
// which is every real org, so the path is the one it always was.
//
// The empty answer is the refusal, and it agrees with the identity boundary for
// free: OrgHasUnsafeRune is Sanitize's "" under another name, so an org that gets
// no scope at the edge names no store here. Stated as two predicates instead, the
// two drifted — the edge granted a scope to an org 64 characters long or bearing a
// '.', and four subsystems then answered it 403.
func OrgPath(org string, sub ...string) string {
	slug := namespace.Sanitize(org)
	if slug == "" {
		return ""
	}
	var p strings.Builder
	p.WriteString("/orgs/" + slug)
	for _, s := range sub {
		if s = strings.Trim(strings.TrimSpace(s), "/"); s != "" {
			p.WriteString("/" + s)
		}
	}
	return p.String()
}

// split parses a secret's coordinate (sub-path + name) into its sub-path and
// name: the last segment is the name, the rest is the sub-path beneath the org
// root. "DB" → ("", DB); "ci/DB" → ("ci", DB). ok=false when either fails the
// boundary validators, so the caller rejects the request rather than key a
// malformed record. The org is folded in afterwards by OrgPath, once the grant
// rule has been asked about the same (sub-path, name) it will store under.
//
// It trims the surrounding slashes itself, so one coordinate keys one record
// whether it arrived as a matched path tail or as an argument that spelled the
// same address with a leading or trailing "/".
//
// It does NOT decode: measured, the router hands the capture over already
// decoded, so an encoded traversal is "../" by the time it arrives and
// ValidSubpath refuses a "." or ".." segment on the far side of the split.
// Decoding a second time would only make a secret whose name holds a literal
// percent unreachable.
func split(coord string) (sub, name string, ok bool) {
	coord = strings.Trim(strings.TrimSpace(coord), "/")
	if slash := strings.LastIndex(coord, "/"); slash >= 0 {
		sub, name = coord[:slash], coord[slash+1:]
	} else {
		name = coord
	}
	if !validName(name) || !ValidSubpath(sub) {
		return "", "", false
	}
	return sub, name, true
}

// namesAnOrg reports whether a subpath starts with the `orgs` segment, which is
// the store root's own first segment: written beneath the caller's root it nests
// one org's coordinate inside another's (`/orgs/admin/orgs/hanzo`). Only a write
// asks it. A read, a delete and a listing still reach such a record, because
// that is how one already written is found and removed.
func namesAnOrg(sub string) bool {
	first, _, _ := strings.Cut(strings.Trim(strings.TrimSpace(sub), "/"), "/")
	return first == "orgs"
}

// envOr returns env or the "default" environment when empty. defaultEnv is
// defined in kms.go (the library face) — the ONE place the fallback lives.
func envOr(env string) string {
	if e := strings.TrimSpace(env); e != "" {
		return e
	}
	return defaultEnv
}

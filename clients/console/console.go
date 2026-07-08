// Package console mounts the console's OWN standalone server surface natively in
// the unified cloud binary at /v1/console/* (HIP-0106). It is the Go port of the
// two console Next server routes that hold NO backend-proxy role but DO privileged
// IAM work on the signed-in user's behalf — app/keys/route.ts and
// app/onboard/route.ts — so console can drop those Node server handlers and be
// statically exported (task #41, "True 1-binary FE": the console SPA is go:embed'd
// and every dynamic call terminates at THIS binary's /v1, no separate Node origin).
//
// WHY THESE (and not the pure passthrough proxies). console's PURE BFF reverse-
// proxies — app/cloud, app/ai, app/commerce, whose only server work is minting a
// user Bearer — vanish in the one-binary model: the SPA calls the canonical /v1/*
// on its own origin and the already-mounted subsystems answer. The routes ported
// HERE are the ones that do REAL server work a static SPA cannot: `keys`/`onboard`
// run privileged IAM logic as the confidential `hanzo-console` client; `waitlist`/
// `embed-status`/`topup` do server-side verification; and the /v1/billing/* bridge
// (billing.go) injects the commerce SERVICE token and pins the caller's own billing
// subject SERVER-SIDE (a passthrough would leak cross-tenant ledgers). Each has no
// pure-proxy equivalent, so it must be ported for the static export to be complete.
//
// SURFACE (every route requires a VALIDATED principal — a gateway-minted, IAM-
// verified X-User-Id; a client-forged X-Org-Id on the bearer-less path is refused):
//
//	GET    /v1/console/keys      — whether the caller has an `hk-` key (+ prefix/mtime); no secret.
//	POST   /v1/console/keys      — mint/rotate the key; returns { accessKey } ONCE.
//	DELETE /v1/console/keys      — revoke the key.
//	POST   /v1/console/onboard   — create the caller's org (+ move them in on first run).
//	GET    /v1/console/health    — real IAM-configured probe (fail-closed when unwired).
//	GET    /v1/billing/*         — per-tenant billing read (balance/usage/invoices/…),
//	POST   /v1/billing/*           forwarded to commerce with the service token, SCOPED
//	                              to the validated caller's own subject (billing.go).
//	GET    /v1/commerce/*        — per-tenant STORE (products/orders/customers/…),
//	…      /v1/commerce/*          forwarded to commerce's bare /v1/<kind> with the
//	                              service token, SCOPED to the validated caller's own
//	                              org; full CRUD, store-heads only (commerce.go).
//
// TENANCY. The caller is resolved from the VALIDATED identity headers ONLY
// (principal.Validated / c.Org() / c.User()), the same trust boundary every
// mutating subsystem uses. The IAM id targeted is DERIVED as `<owner>/<name>` from
// those validated claims — never taken from the request body/query — so a caller
// can only ever mint/revoke their OWN key and onboard THEMSELVES; there is no path
// to name a third-party subject. When the confidential client is unwired the surface
// is honestly "not configured" (501), never a fabricated key or org.
package console

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// adminOrg is THE global-admin / IAM-system org that owns every customer org row —
// standardized as "admin" across the whole stack (IAM, commerce, ai, gateway all
// gate cross-tenant on owner=="admin"). A created customer org is owned by it.
const adminOrg = "admin"

// errNotConfigured is returned by the IAM client when the confidential
// `hanzo-console` credential is unset; handlers map it to a 501 (honest "not
// configured on this deployment", mirroring identity.ts's mintConfigured() gate).
var errNotConfigured = errors.New("iam confidential client not configured")

// errNotFound is a not-present sentinel (e.g. the user row IAM cannot return).
var errNotFound = errors.New("not found")

type svc struct {
	iam   *iamClient
	log   luxlog.Logger
	brand string
}

// Mount wires the /v1/console surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("console.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("console.Mount: nil deps.Logger")
	}
	s := &svc{
		iam:   newIAMClient(),
		log:   deps.Logger.New("subsystem", "console"),
		brand: deps.Brand,
	}
	s.routes(app)
	s.log.Info("console standalone surface mounted",
		"prefix", "/v1/console", "iam", s.iam.base, "configured", s.iam.configured(), "brand", deps.Brand)
	return nil
}

// routes is the ONE place the surface is wired — shared by Mount (real IAM) and the
// test (an httptest fake IAM injected via s.iam.base).
func (s *svc) routes(app *zip.App) {
	app.Get("/v1/console/keys", s.getKey)
	app.Post("/v1/console/keys", s.mintKey)
	app.Delete("/v1/console/keys", s.revokeKey)
	app.Post("/v1/console/onboard", s.onboard)
	// The remaining standalone console server routes, ported native (task #41):
	// waitlist join (session-bound email), embed-status (entitlement + reachability
	// probe), and the HUSD wallet top-up (on-chain verify → commerce credit).
	app.Post("/v1/console/waitlist", s.waitlistJoin)
	app.Get("/v1/console/embed-status", s.embedStatus)
	app.Post("/v1/console/topup/wallet", s.walletTopup)
	app.Get("/v1/console/health", s.health)
	// Per-tenant billing DATA bridge (task #41 BFF catch-all sweep) — the canonical
	// /v1/billing/* the statically-exported console calls, forwarded to commerce with
	// the admin service token and SCOPED to the validated caller's own subject
	// (billing.go). GET+POST only, mirroring app/billing/v1/[...path]/route.ts.
	app.Get("/v1/billing/*", s.billingData)
	app.Post("/v1/billing/*", s.billingData)
	// Per-tenant STORE DATA bridge (task #41 BFF catch-all sweep) — the canonical
	// /v1/commerce/* the statically-exported console calls, forwarded to commerce's
	// bare store surface /v1/<kind> with the admin service token and SCOPED to the
	// validated caller's own org (commerce.go). Full CRUD, mirroring the five method
	// exports of app/commerce/[...path]/route.ts.
	app.Get("/v1/commerce/*", s.commerceData)
	app.Post("/v1/commerce/*", s.commerceData)
	app.Put("/v1/commerce/*", s.commerceData)
	app.Patch("/v1/commerce/*", s.commerceData)
	app.Delete("/v1/commerce/*", s.commerceData)
}

// init registers the subsystem under the clean id "console" with cloud.HealthOwner:
// it serves its OWN fail-closed probe at /v1/console/health, and cloud.HealthOwner
// makes serve.go skip the generic GET /v1/<name>/health so the always-ok route
// never shadows it (same flag as kms/paas/s3). Order 122 binds the /v1/console
// family well before the AI /v1/* catch-all (150); it shares no path prefix with
// another subsystem, so the order only needs to precede 150.
func init() {
	cloud.Register("console", 122, cloud.Typed(Mount), cloud.HealthOwner)
}

// ── caller resolution (the tenancy boundary) ─────────────────────────────────

// caller is the signed-in user resolved from the VALIDATED identity headers. id is
// the `<owner>/<name>` composite IAM's privileged ops parse (GetOwnerAndNameFromId
// requires it — a bare token count of 1 throws "wrong token count"); owner is the
// org (X-Org-Id).
type caller struct {
	id    string // <owner>/<name> (or the bare user id when owner-less)
	owner string // validated org (may be "" for a zero-org, first-run user)
	name  string // username within the org
}

// resolveCaller derives the caller from the validated identity, or (zero,false)
// when there is no validated principal. requireOwner=true refuses a user with no
// org yet (used by the key ops, which must act scoped); onboarding passes
// requireOwner=false so a first-run zero-org user can create their first org. The id
// is ALWAYS derived from the validated claims, never a request value.
func resolveCaller(c *zip.Ctx, requireOwner bool) (caller, bool) {
	if !principal.Validated(c) {
		return caller{}, false // no gateway-minted, IAM-verified principal — refuse
	}
	name := strings.TrimSpace(c.User())
	if name == "" {
		return caller{}, false
	}
	owner := strings.TrimSpace(c.Org())
	if requireOwner && owner == "" {
		return caller{}, false
	}
	// IAM parses `<owner>/<name>`; prefer it, fall back to the bare id for an
	// owner-less (first-run) user. Same id semantics as identity.ts.
	id := name
	if owner != "" {
		id = owner + "/" + name
	}
	return caller{id: id, owner: owner, name: name}, true
}

// ── keys (the per-user `hk-` Cloud API key) ──────────────────────────────────

type keyStatus struct {
	HasKey    bool   `json:"hasKey"`
	KeyPrefix string `json:"keyPrefix,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// getKey reports whether the caller has an `hk-` key, its public prefix, and when
// the key row last changed — NO secret material. Reads IAM authoritatively (not the
// session claim, which lags a fresh key). Mirrors GET app/keys/route.ts.
func (s *svc) getKey(c *zip.Ctx) error {
	cr, ok := resolveCaller(c, true)
	if !ok {
		return zip.ErrForbidden("sign in to manage API keys")
	}
	if !s.iam.configured() {
		return notConfigured("API key management")
	}
	uk, err := s.iam.getUserKey(c.Context(), cr.id)
	if err != nil {
		// Fail-soft on a transient IAM read: report "no key" rather than 5xx, so the
		// page shows the honest empty state (never a fabricated key). The mint path
		// still 502s loudly on a real failure — reads degrade, writes do not.
		s.log.Warn("get key: iam read failed (reporting no key)", "err", err)
		return c.JSON(http.StatusOK, keyStatus{HasKey: false})
	}
	if uk.AccessKey == "" {
		return c.JSON(http.StatusOK, keyStatus{HasKey: false})
	}
	prefix := uk.AccessKey
	if len(prefix) > 11 {
		prefix = prefix[:11]
	}
	return c.JSON(http.StatusOK, keyStatus{HasKey: true, KeyPrefix: prefix, CreatedAt: uk.UpdatedTime})
}

// mintKey (re)generates the caller's `hk-` key and returns it ONCE (show-once). A
// real IAM failure surfaces as 502 (never a fabricated key). Mirrors POST app/keys.
func (s *svc) mintKey(c *zip.Ctx) error {
	cr, ok := resolveCaller(c, true)
	if !ok {
		return zip.ErrForbidden("sign in to manage API keys")
	}
	if !s.iam.configured() {
		return notConfigured("API key management")
	}
	key, err := s.iam.mintUserKey(c.Context(), cr.id)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not mint an API key: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]string{"accessKey": key})
}

// revokeKey clears the caller's `hk-` key. Mirrors DELETE app/keys.
func (s *svc) revokeKey(c *zip.Ctx) error {
	cr, ok := resolveCaller(c, true)
	if !ok {
		return zip.ErrForbidden("sign in to manage API keys")
	}
	if !s.iam.configured() {
		return notConfigured("API key management")
	}
	if err := s.iam.revokeUserKey(c.Context(), cr.id); err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not revoke the API key: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// ── onboard (create the caller's org) ────────────────────────────────────────

type onboardReq struct {
	Name     string `json:"name"`
	Personal bool   `json:"personal"`
}

type onboardResp struct {
	Org         string `json:"org"`
	DisplayName string `json:"displayName"`
	Additional  bool   `json:"additional"`
}

// onboard creates the caller's organization. Two flows, keyed on whether the caller
// already has a home org (mirrors app/onboard/route.ts):
//
//   - FIRST-RUN (no owner): create + MOVE the user in as admin, so their next JWT
//     carries the new owner and the cloud scopes everything to it.
//   - ADDITIONAL (owner set): create the org but do NOT move the user — a move
//     changes their IAM owner (stripping a global admin's status + orphaning their
//     current org). They reach the new org via the OrgSwitcher, which re-scopes
//     X-Org-Id without touching IAM membership. A personal-org request from someone
//     who already has an org is meaningless → 409.
func (s *svc) onboard(c *zip.Ctx) error {
	cr, ok := resolveCaller(c, false) // first-run onboarding allows a zero-org user
	if !ok {
		return zip.ErrForbidden("sign in to create an organization")
	}
	if !s.iam.configured() {
		return notConfigured("organization creation")
	}
	var body onboardReq
	if len(c.Body()) > 0 {
		if err := c.Bind(&body); err != nil {
			return err
		}
	}

	additional := cr.owner != ""
	if additional && body.Personal {
		return zip.ErrConflict("you already have an organization; name the new one explicitly")
	}

	baseSlug, displayName, herr := s.resolveOnboardName(body, cr)
	if herr != nil {
		return herr
	}

	// Resolve a unique slug. Personal orgs auto-suffix to stay unique; an explicit
	// name that's taken is an honest conflict the user resolves by renaming.
	slug, herr := s.uniqueSlug(c, baseSlug, body.Personal)
	if herr != nil {
		return herr
	}

	// Create the org (cloning the caller's current org for password/locale
	// compatibility), then — first-run only — move the zero-org user in as admin.
	org := s.buildOrg(c, slug, displayName, body.Personal, cr.owner)
	if err := s.iam.createOrganization(c.Context(), org); err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not create the organization: %v", err)
	}
	if !additional {
		if err := s.iam.moveUserToOrg(c.Context(), cr.id, slug); err != nil {
			return zip.Errorf(http.StatusBadGateway, "org created but could not assign you to it: %v", err)
		}
	}
	return c.JSON(http.StatusOK, onboardResp{Org: slug, DisplayName: displayName, Additional: additional})
}

// resolveOnboardName derives the base slug + display name from the request, or a
// mapped HTTP error. Personal orgs derive from the username; a named org validates
// through the shared policy (onboarding.go).
func (s *svc) resolveOnboardName(body onboardReq, cr caller) (baseSlug, displayName string, err error) {
	if body.Personal {
		baseSlug = personalOrgSlug(cr.name)
		if len(baseSlug) < minOrgSlug || isReservedOrg(baseSlug) {
			baseSlug = "org-" + firstNonEmpty(slugifyOrg(cr.name), "workspace")
		}
		return baseSlug, humanize(cr.name), nil
	}
	v := validateOrgName(body.Name)
	if !v.ok {
		return "", "", zip.ErrBadRequest(v.error)
	}
	return v.slug, strings.TrimSpace(body.Name), nil
}

// uniqueSlug returns a free slug at/after base. A named org that's taken is a 409;
// a personal org auto-suffixes (base, base-2, …) up to a small bound.
func (s *svc) uniqueSlug(c *zip.Ctx, base string, personal bool) (string, error) {
	existing, err := s.iam.getOrganization(c.Context(), base)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "could not check organization availability: %v", err)
	}
	if existing == nil {
		return base, nil
	}
	if !personal {
		return "", zip.Errorf(http.StatusConflict, "“%s” is taken; choose a different name", base)
	}
	free, err := s.freeSlug(c, base)
	if err != nil {
		return "", err
	}
	if free == "" {
		return "", zip.Errorf(http.StatusConflict, "could not find an available name")
	}
	return free, nil
}

// freeSlug finds the first free slug at/after base (base, base-2, … base-20), or ""
// if all are taken. Mirrors identity.ts's freeSlug bound of 20.
func (s *svc) freeSlug(c *zip.Ctx, base string) (string, error) {
	for i := 2; i <= 20; i++ {
		trimmed := base
		if len(trimmed) > maxOrgSlug-3 {
			trimmed = trimmed[:maxOrgSlug-3]
		}
		candidate := strings.Trim(fmt.Sprintf("%s-%d", strings.TrimRight(trimmed, "-"), i), "-")
		if len(candidate) < minOrgSlug || isReservedOrg(candidate) {
			continue
		}
		existing, err := s.iam.getOrganization(c.Context(), candidate)
		if err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "could not check organization availability: %v", err)
		}
		if existing == nil {
			return candidate, nil
		}
	}
	return "", nil
}

// buildOrg assembles the new customer org owned by the `admin` org, cloning
// password/locale settings from the caller's current org (best-effort; a nil source
// just yields a minimal org IAM completes with its defaults) and clearing all
// instance-specific material. Mirrors identity.ts's createOrganization body.
func (s *svc) buildOrg(c *zip.Ctx, slug, displayName string, personal bool, sourceOwner string) iamOrg {
	org := iamOrg{Owner: adminOrg, Name: slug, DisplayName: displayName, IsPersonal: personal}
	if sourceOwner == "" {
		return org
	}
	src, err := s.iam.getOrganization(c.Context(), sourceOwner)
	if err != nil || src == nil {
		return org // clone is best-effort; IAM applies its org defaults otherwise
	}
	org.PasswordType = src.PasswordType
	org.PasswordSalt = src.PasswordSalt
	org.PasswordObfuscatorType = src.PasswordObfuscatorType
	org.PasswordObfuscatorKey = src.PasswordObfuscatorKey
	org.PasswordOptions = src.PasswordOptions
	org.CountryCodes = src.CountryCodes
	org.Languages = src.Languages
	org.DefaultAvatar = src.DefaultAvatar
	return org
}

// ── health ───────────────────────────────────────────────────────────────────

// health is a REAL probe: 200 when the confidential IAM client is wired; 503 with
// the honest reason otherwise (the console still runs — its own in-browser IAM SDK
// handles login — but the key/onboard surface is inert until the client is
// provisioned). Not admin-gated: liveness must be probe-able without a JWT.
func (s *svc) health(c *zip.Ctx) error {
	res := map[string]any{"service": "console", "status": "ok", "iam": s.iam.configured()}
	if !s.iam.configured() {
		res["status"] = "degraded"
		res["error"] = "IAM confidential client (IAM_MINT_CLIENT_ID/SECRET) not configured"
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	return c.JSON(http.StatusOK, res)
}

// ── shared helpers ────────────────────────────────────────────────────────────

// notConfigured is the honest 501 for a surface whose confidential client is
// unwired — the deployment simply lacks the `hanzo-console` credential.
func notConfigured(surface string) error {
	return zip.Errorf(http.StatusNotImplemented, "%s is not configured on this deployment (IAM client unset)", surface)
}

// humanize title-cases the base of a username for a personal org's display name
// (dave.smith@x.com → "Dave Smith"). Mirrors identity/onboard humanize().
func humanize(username string) string {
	base := username
	// Split on '@' anywhere (mirrors identity.ts humanize's `includes('@')`), so a
	// bare "@" collapses to "" → "Personal". (personalOrgSlug intentionally uses
	// `> 0` instead, matching its own TS source.)
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	base = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '.' || r == '_' || r == '-' {
			return ' '
		}
		return r
	}, base))
	if base == "" {
		return "Personal"
	}
	parts := strings.Fields(base)
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func getenv(key, dflt string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return dflt
}

func basicToken(id, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(id + ":" + secret))
}

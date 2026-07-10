// Package admin mounts the god-mode admin surface (/v1/admin/*) the Hanzo
// Admin Console (admin.hanzo.ai, apps/operator) calls, per the api.ts contract.
//
// It is an AGGREGATOR, not a new store: identity (orgs/users/roles/applications/
// audit/me) is read from IAM, the money panels (spend/tokens/credits) from
// commerce, and System Health from o11y — every one a real upstream, none fused
// into this binary (see subsystems.go). The facade fans out over HTTP exactly
// like o11ysvc / productsvc: it holds no business logic, it shapes the reads into
// the /v1 envelope { status, msg, data, data2 } the operator's transport
// decodes (get<T> reads data; getList<T> reads data + data2 total).
//
// SECURITY — TWO tiers off ONE identity predicate, both fail-closed. The cockpit is a
// single pane for a SuperAdmin (owner == AdminOrg — c.IsAdmin(), the SANITIZED
// X-User-IsAdmin, true ONLY for a JWT-validated principal whose org IS the admin org,
// matching the gateway's admin-guard) AND for an org admin (any other validated admin
// caller). The predicate is enforced in ONE place — resolveScope/scopedOrgs (scope.go):
//
//   - PLATFORM routes (roles/applications/audit/products/finance/compute/o11y/revenue +
//     the launch/release/flags/access control plane) are SuperAdmin ONLY (s.guard).
//     No principal → 403; an org admin → 403; a forged X-User-IsAdmin never survives
//     ingress (SanitizeIdentity strips it).
//   - ORG-SCOPED routes (me/overview/orgs/users/usage/analytics/bases) are s.guardScoped:
//     a SuperAdmin sees EVERY tenant; any other validated admin caller is HARD-limited to
//     their OWN org subtree. The cross-tenant boundary — the escalation line — cannot be
//     crossed by a non-super caller for ANY input, because their org is the sanitized,
//     un-forgeable c.Org() and every read folds over scopedOrgs.
//
// admin adds no service credential to the IAM fan-out — it replays the caller's own
// cookie/bearer, so it can never read more than the caller already could, and IAM
// re-checks authority on every call (a non-super caller replaying to a cross-tenant IAM
// read is refused by IAM too — defense in depth).
//
// Panels with no in-binary feed yet (the Usage & Costs timeseries + per-product
// breakdown live in insights/datastore; the product/workload registry + infra
// tiles live in platform.hanzo.ai / the operator inventory) return the real,
// honest empty state — never a fabricated number. The operator UI renders those
// as an em-dash / empty table by design.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/zap-proto/zip"
)

// svc holds the resolved upstream clients + the admin org for this deployment.
type svc struct {
	iam      *iamClient
	commerce *commerceClient
	health   *healthClient
	do       *doClient
	adminOrg string
	// auditStore is cloud's OWN tamper-evident audit store (nil when unconfigured,
	// in which case /v1/admin/audit falls back to the IAM get-records proxy). Serve
	// builds it and hands it over via deps.Audit. See audit.go.
	auditStore *audit.Recorder
}

// Mount registers the /v1/admin/* surface on app. Every handler gates on
// c.IsAdmin() first (global-admin only), then aggregates real upstream data.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("admin.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("admin.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "admin")

	s := &svc{
		iam:        newIAMClient(iamBase(deps)),
		commerce:   newCommerceClient(commerceinproc.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		health:     newHealthClient(o11yHealthURL()),
		do:         newDOClient(doTokenFromEnv()),
		adminOrg:   adminOrgOf(deps),
		auditStore: deps.Audit,
	}

	// Org-scoped panels — guardScoped: a SuperAdmin OR any validated admin caller
	// pinned to an org. The HANDLER scopes the data (scopedOrgs / resolveScope) so a
	// non-super caller is HARD-limited to their own org subtree — cross-tenant reads
	// are impossible. Same panels, both tiers, scoped by the ONE predicate.
	app.Get("/v1/admin/me", s.guardScoped(s.me))
	app.Get("/v1/admin/overview", s.guardScoped(s.overview))
	app.Get("/v1/admin/orgs", s.guardScoped(s.orgs))
	app.Get("/v1/admin/users", s.guardScoped(s.users))
	app.Get("/v1/admin/usage", s.guardScoped(s.usage))
	// Platform reads — SuperAdmin only (cross-tenant by nature): roles/apps catalog,
	// the fleet audit trail, workload registry, SaaS profitability, compute fleet,
	// system health.
	app.Get("/v1/admin/roles", s.guard(s.roles))
	app.Get("/v1/admin/applications", s.guard(s.applications))
	app.Get("/v1/admin/audit", s.guard(s.audit))
	app.Get("/v1/admin/audit/verify", s.guard(s.auditVerify))
	app.Get("/v1/admin/products", s.guard(s.products))
	app.Get("/v1/admin/finance", s.guard(s.finance))
	app.Get("/v1/admin/compute", s.guard(s.compute))
	app.Get("/v1/admin/o11y", s.guard(s.o11y))
	app.Post("/v1/admin/sync", s.guard(s.sync))

	// Customer management — the operator cockpit. List (static) precedes the :org
	// param route; the write actions are POST (distinct method), so none collide.
	app.Get("/v1/admin/customers", s.guard(s.customers))
	app.Get("/v1/admin/customers/:org", s.guard(s.customerDetail))
	app.Post("/v1/admin/customers/:org/credit", s.guard(s.grantCredit))
	app.Get("/v1/admin/grants", s.guard(s.grants))
	app.Post("/v1/admin/grants", s.guard(s.issueGrant))
	app.Post("/v1/admin/customers/:org/suspend", s.guard(s.suspendCustomer))
	app.Post("/v1/admin/customers/:org/reactivate", s.guard(s.reactivateCustomer))

	// Fleet revenue aggregate — SuperAdmin only (cross-tenant profitability).
	app.Get("/v1/admin/revenue", s.guard(s.revenue))
	// Product analytics — org-scoped (SuperAdmin: all-orgs SaaS analytics; org admin:
	// their own org's usage/active/spend).
	app.Get("/v1/admin/analytics", s.guardScoped(s.analytics))

	// Bases — the tenant Base-instance panel, org-scoped (bases.go).
	app.Get("/v1/admin/bases", s.guardScoped(s.bases))

	// ── Platform control plane — SuperAdmin ONLY (launch/release/flags + access). ──
	// Flipping public_signup / waitlist_open / rollout %, and granting waitlist
	// access, are PLATFORM sudo; an org admin never sees or touches them (s.guard,
	// super-only, like every mutating fleet action). flags.go + waitlist.go.
	app.Get("/v1/admin/flags", s.guard(s.flags))
	app.Get("/v1/admin/waitlist", s.guard(s.waitlist))
	app.Post("/v1/admin/waitlist/boost", s.guard(s.waitlistBoost))

	logger.Info("admin surface mounted",
		"prefix", "/v1/admin",
		"iam", s.iam.configured(),
		"commerce", s.commerce.configured(),
		"digitalocean", s.do.configured(),
		"adminOrg", s.adminOrg,
	)
	return nil
}

// guard wraps a handler with the global-admin gate. Fail-closed: any request
// whose validated identity is not a global admin (X-User-IsAdmin != "true",
// which SanitizeIdentity sets only for owner == AdminOrg) is refused 403 before
// the handler — no upstream is touched, no data leaks.
func (s *svc) guard(h func(*zip.Ctx) error) zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("global admin required")
		}
		return h(c)
	}
}

// guardScoped is the gate for the ORG-SCOPED panels (me/overview/orgs/users/usage/
// analytics/bases). It admits a SuperAdmin (c.IsAdmin()) OR any VALIDATED admin caller
// pinned to an org, and the handler then scopes every read to resolveScope(c) — so a
// non-super caller passes the gate but the DATA layer hard-limits them to their own org
// subtree. Cross-tenant reads are impossible for a non-super caller regardless of input.
//
// The non-super admission requires c.User() (X-User-Id) non-empty, which SanitizeIdentity
// sets ONLY for a validated principal — so an anonymous caller who forged X-Org-Id (the
// documented Phase-1 residual restores a client X-Org-Id for the data path) is REFUSED
// here: no validated principal, no X-User-Id, no admission. A validated non-super
// principal's X-Org-Id is PINNED by the boundary to their own owner, never client-chosen.
//
// The org-ADMIN-vs-member distinction (should a non-admin org member reach the cockpit?)
// is enforced at the console BFF getAdminGate, which reads IAM's isAdmin claim; cloud
// cannot see that claim without a trusted org-admin header from SanitizeIdentity (a
// follow-up trust-boundary change). The cross-tenant boundary — the escalation line — is
// fully enforced HERE regardless, because a non-super caller can only ever read their own
// org's data.
func (s *svc) guardScoped(h func(*zip.Ctx) error) zip.Handler {
	return func(c *zip.Ctx) error {
		if c.IsAdmin() {
			return h(c)
		}
		if strings.TrimSpace(c.User()) != "" && strings.TrimSpace(c.Org()) != "" {
			return h(c)
		}
		return zip.ErrForbidden("admin required")
	}
}

// callerCreds captures the caller's replayed authorization context for the IAM
// fan-out: the raw Cookie header (session model) and the Authorization bearer.
func callerCreds(c *zip.Ctx) creds {
	return creds{
		cookie: string(c.Fiber().Request().Header.Peek("Cookie")),
		auth:   c.Header("Authorization"),
	}
}

// ── /v1 envelope writers ────────────────────────────────────────────────

// ok writes a { status:"ok", data } envelope (the get<T> shape).
func ok(c *zip.Ctx, data any) error {
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": data})
}

// okList writes a { status:"ok", data:[...], data2:total } envelope (getList<T>).
func okList(c *zip.Ctx, rows any, total int) error {
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": rows, "data2": total})
}

// okRaw writes a { status:"ok", data:<raw>, data2:total } envelope, forwarding an
// IAM payload verbatim so its exact wire shape (Role, Application, Record, User)
// reaches the operator field-for-field.
func okRaw(c *zip.Ctx, rows json.RawMessage, total int) error {
	if len(rows) == 0 {
		rows = json.RawMessage("[]")
	}
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": rows, "data2": total})
}

// fail writes a { status:"error", msg } envelope. The operator's transport maps
// a non-ok envelope to a surfaced error (never a fabricated value).
func fail(c *zip.Ctx, msg string) error {
	return c.JSON(200, map[string]any{"status": "error", "msg": msg, "data": nil})
}

// ── /v1/admin/me — operator identity (AdminMe) ───────────────────────────────

// me answers with the validated operator identity. The gate already proved this
// is a global admin, so the fields come from the sanitized identity headers —
// authoritative and never client-forgeable.
func (s *svc) me(c *zip.Ctx) error {
	sc := s.resolveScope(c)
	owner := strings.TrimSpace(c.Org())
	if owner == "" && sc.super {
		owner = s.adminOrg
	}
	name := strings.TrimSpace(c.User())
	// IsGlobalAdmin reflects the REAL scope: true only for a SuperAdmin (owner ==
	// admin org). An org admin gets false + their own org, so the cockpit renders the
	// scoped (own-subtree) view and hides the platform-sudo panels.
	return ok(c, adminMe{
		Owner:         owner,
		Name:          name,
		Email:         strings.TrimSpace(c.UserEmail()),
		DisplayName:   name,
		IsSuperAdmin:  sc.super,
		IsGlobalAdmin: sc.super, // DEPRECATED alias of isSuperAdmin; kept populated for back-compat
	})
}

// ── /v1/admin/orgs — tenant directory (OrgRow[]) ─────────────────────────────

func (s *svc) orgs(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	orgs, err := s.scopedOrgs(ctx, c, cr)
	if err != nil {
		return fail(c, err.Error())
	}
	rows := make([]orgRow, 0, len(orgs))
	for _, o := range orgs {
		users := s.orgUserCount(ctx, cr, o.Name)
		spend, credits := s.orgMoney(ctx, o.Name)
		rows = append(rows, orgRow{
			Org:          o.Name,
			Display:      display(o.DisplayName, o.Name),
			Users:        users,
			Products:     0, // workload registry feed pending (platform apps table)
			SpendCents:   spend,
			CreditsCents: credits,
			Tokens:       0, // fleet token counters pending (insights/datastore)
			Created:      o.CreatedTime,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Org < rows[j].Org })
	return okList(c, rows, len(rows))
}

// ── /v1/admin/users — cross-org directory (OperatorUser[]) ───────────────────

func (s *svc) users(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	sc := s.resolveScope(c)
	q := url.Values{}
	if !sc.super {
		// A scoped caller lists ONLY their own org's users — the client ?org= is
		// ignored, the owner hard-pinned to the sanitized org subtree.
		if len(sc.orgs) > 0 {
			q.Set("owner", sc.orgs[0])
		}
	} else if owner := strings.TrimSpace(c.Query("org")); owner != "" {
		q.Set("owner", owner)
	}
	if p := strings.TrimSpace(c.Query("p")); p != "" {
		q.Set("p", p)
	}
	if ps := strings.TrimSpace(c.Query("pageSize")); ps != "" {
		q.Set("pageSize", ps)
	}
	if term := strings.TrimSpace(c.Query("q")); term != "" {
		// IAM's list uses field/value contains-matching for the free-text filter.
		q.Set("field", "name")
		q.Set("value", term)
	}
	res, err := s.iam.getList(ctx, cr, "/v1/iam/get-users", q)
	if err != nil {
		return fail(c, err.Error())
	}
	var raw []iamUser
	if len(res.rows) > 0 {
		if err := json.Unmarshal(res.rows, &raw); err != nil {
			return fail(c, "users decode: "+err.Error())
		}
	}
	rows := make([]operatorUser, 0, len(raw))
	for _, u := range raw {
		rows = append(rows, operatorUser{
			Owner:         u.Owner,
			Name:          u.Name,
			Email:         u.Email,
			DisplayName:   u.DisplayName,
			IsAdmin:       u.IsAdmin,
			IsSuperAdmin:  u.Owner == s.adminOrg,
			IsGlobalAdmin: u.Owner == s.adminOrg, // back-compat alias; same fact
			Tag:           u.Tag,
			Created:       u.CreatedTime,
			LastSignin:    u.LastSigninTime,
			Forbidden:     u.IsForbidden,
		})
	}
	total := res.total
	if total < len(rows) {
		total = len(rows)
	}
	return okList(c, rows, total)
}

// ── /v1/admin/roles and /applications — verbatim IAM passthrough ─────────────

func (s *svc) roles(c *zip.Ctx) error {
	return s.iamPassthrough(c, "/v1/iam/get-roles")
}

func (s *svc) applications(c *zip.Ctx) error {
	return s.iamPassthrough(c, "/v1/iam/get-applications")
}

// iamPassthrough forwards a paginated IAM read verbatim (the operator decodes
// Role / Application as the raw IAM wire shape). `owner` defaults to the admin
// org, which owns the platform applications.
func (s *svc) iamPassthrough(c *zip.Ctx, path string) error {
	q := url.Values{}
	owner := strings.TrimSpace(c.Query("owner"))
	if owner == "" {
		owner = s.adminOrg
	}
	q.Set("owner", owner)
	if p := strings.TrimSpace(c.Query("p")); p != "" {
		q.Set("p", p)
	}
	if ps := strings.TrimSpace(c.Query("pageSize")); ps != "" {
		q.Set("pageSize", ps)
	}
	res, err := s.iam.getList(c.Context(), callerCreds(c), path, q)
	if err != nil {
		return fail(c, err.Error())
	}
	return okRaw(c, res.rows, res.total)
}

// ── /v1/admin/audit — records directory (AuditRow[]) ─────────────────────────
//
// The handler lives in audit.go (it reads cloud's OWN tamper-evident store).
// iamAuditQuery builds the IAM get-records query for the federated fallback
// auditFromIAM uses when no local store is configured.

func iamAuditQuery(c *zip.Ctx) url.Values {
	q := url.Values{}
	if org := strings.TrimSpace(c.Query("org")); org != "" {
		q.Set("organizationName", org)
	}
	q.Set("p", "1")
	ps := strings.TrimSpace(c.Query("pageSize"))
	if ps == "" {
		ps = "100"
	}
	q.Set("pageSize", ps)
	q.Set("sortField", "createdTime")
	q.Set("sortOrder", "descend")
	return q
}

// ── /v1/admin/usage — fleet usage roll-up (UsageData) ────────────────────────

// usage returns the real fleet money totals from commerce. The daily series and
// the per-product breakdown are NOT derivable from the commerce billing API
// (they live in insights/datastore, owned separately); admin returns the
// honest empty series/byProduct rather than fabricating a trend — the operator
// renders that as an empty chart, never a fake line.
func (s *svc) usage(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	sc := s.resolveScope(c)
	org := strings.TrimSpace(c.Query("org"))
	if !sc.super {
		// A scoped caller reads ONLY their own org's usage — the client ?org= is
		// ignored, the org hard-pinned to the sanitized subtree.
		org = ""
		if len(sc.orgs) > 0 {
			org = sc.orgs[0]
		}
	}

	var spend int64
	switch {
	case org != "":
		if r, err := s.commerce.usageRollup(ctx, org, orgSubject(org)); err == nil {
			spend = r.ConsumedCents
		}
	case sc.super:
		// Fleet: sum month-to-date consumption across every org.
		orgs, err := s.listOrgs(ctx, cr)
		if err == nil {
			for _, o := range orgs {
				if r, e := s.commerce.usageRollup(ctx, o.Name, orgSubject(o.Name)); e == nil {
					spend += r.ConsumedCents
				}
			}
		}
	}

	return ok(c, usageData{
		Totals:    usageTotals{SpendCents: spend, Tokens: 0, Requests: 0},
		Series:    []usagePoint{},
		ByProduct: []usageByProduct{},
	})
}

// ── /v1/admin/products — workload registry (ProductRow[]) ────────────────────

// products is the workload/drift registry (declared vs running tag, health).
// That inventory is the platform.hanzo.ai apps table / operator reconcile state,
// NOT an in-binary source. admin exposes the gated endpoint and returns the
// real empty registry until that feed is wired — it never fabricates workload
// rows. The operator renders an empty table, not fake products.
func (s *svc) products(c *zip.Ctx) error {
	return okList(c, []productRow{}, 0)
}

// ── /v1/admin/overview — Platform Overview tiles (OverviewData) ───────────────

func (s *svc) overview(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	now := time.Now().UTC().Format(time.RFC3339)

	var sources []sourceStatus
	orgCount, userCount, spend, credits := 0, 0, int64(0), int64(0)

	orgs, orgErr := s.scopedOrgs(ctx, c, cr)
	sources = append(sources, srcOf("iam", orgErr, len(orgs), now))
	if orgErr == nil {
		orgCount = len(orgs)
		for _, o := range orgs {
			userCount += s.orgUserCount(ctx, cr, o.Name)
			sp, cr2 := s.orgMoney(ctx, o.Name)
			spend += sp
			credits += cr2
		}
	}

	// Commerce freshness: probe one org's rollup so the tile reflects a real read.
	commerceRows := 0
	var commerceErr error
	if s.commerce.configured() {
		probe := s.adminOrg
		if len(orgs) > 0 {
			probe = orgs[0].Name
		}
		if _, err := s.commerce.usageRollup(ctx, probe, orgSubject(probe)); err != nil {
			commerceErr = err
		} else {
			commerceRows = 1
		}
	} else {
		commerceErr = fmt.Errorf("commerce endpoint not configured")
	}
	sources = append(sources, srcOf("commerce", commerceErr, commerceRows, now))

	// o11y System Health.
	o11yRows := 0
	oOK, oErr := s.health.ok(ctx)
	if oOK {
		o11yRows = 1
	}
	sources = append(sources, srcOf("o11y", oErr, o11yRows, now))

	return ok(c, overviewData{
		Orgs:           orgCount,
		Users:          userCount,
		Products:       0, // workload registry feed pending (platform apps table)
		ActiveProducts: 0,
		Drift:          0,
		SpendCents30d:  spend,
		Tokens30d:      0, // fleet token counters pending (insights/datastore)
		CreditsCents:   credits,
		LastSync:       now,
		Sources:        sources,
	})
}

// ── /v1/admin/sync — refresh trigger ─────────────────────────────────────────

// sync answers the operator's "Sync now" button. admin aggregates LIVE on
// every read (there is no cached fleet snapshot in-binary), so there is no batch
// job to kick — the button simply re-reads. We acknowledge honestly with
// { started: true } so the UI re-fetches the (freshly-computed) overview.
func (s *svc) sync(c *zip.Ctx) error {
	return ok(c, map[string]bool{"started": true})
}

// ── aggregation helpers ──────────────────────────────────────────────────────

// listOrgs reads the org directory (owner = admin org) as the typed shape the
// overview/orgs/usage aggregators fold over.
func (s *svc) listOrgs(ctx context.Context, cr creds) ([]iamOrg, error) {
	q := url.Values{}
	q.Set("owner", s.adminOrg)
	res, err := s.iam.getList(ctx, cr, "/v1/iam/get-organizations", q)
	if err != nil {
		return nil, err
	}
	var orgs []iamOrg
	if len(res.rows) > 0 {
		if err := json.Unmarshal(res.rows, &orgs); err != nil {
			return nil, fmt.Errorf("orgs decode: %w", err)
		}
	}
	return orgs, nil
}

// orgUserCount returns the member count for one org from the IAM list total
// (data2). Best-effort: an error yields 0 rather than failing the whole row.
func (s *svc) orgUserCount(ctx context.Context, cr creds, org string) int {
	q := url.Values{}
	q.Set("owner", org)
	q.Set("p", "1")
	q.Set("pageSize", "1")
	res, err := s.iam.getList(ctx, cr, "/v1/iam/get-users", q)
	if err != nil {
		return 0
	}
	return res.total
}

// orgMoney returns (spendCents, creditsCents) for one org from commerce.
// Best-effort: unreachable/unconfigured commerce yields zeros.
func (s *svc) orgMoney(ctx context.Context, org string) (int64, int64) {
	subj := orgSubject(org)
	var spend, credits int64
	if r, err := s.commerce.usageRollup(ctx, org, subj); err == nil {
		spend = r.ConsumedCents
	}
	if c, err := s.commerce.creditsCents(ctx, org, subj); err == nil {
		credits = c
	}
	return spend, credits
}

// orgSubject is the billing subject commerce keys an org's wallet on. Commerce's
// per-org billing store (the 2026-07 durability rework, commerce >=1.46.8)
// namespaces by the TRUSTED X-Org-Id header (set by commerceClient.get from this
// same org) and keys the org wallet under the BARE org slug as the `user` subject —
// NOT "org/user". The prior "org/org" subject (with the wrong X-IAM-Org-Id header)
// resolved to an EMPTY wallet, so every per-org money panel read $0 while real
// balances existed (lux $10,000, maxpower $20,498). Verified live against commerce
// /v1/billing/{balance,usage-rollup}: user=<org> + X-Org-Id=<org> returns the real
// wallet; user="org/org" or a missing/other org header returns $0.
func orgSubject(org string) string { return org }

// srcOf builds a SourceStatus freshness row for the overview.
func srcOf(name string, err error, rows int, at string) sourceStatus {
	s := sourceStatus{Name: name, OK: err == nil, Rows: rows, At: at}
	if err != nil {
		s.Error = err.Error()
	}
	return s
}

func display(displayName, fallback string) string {
	if strings.TrimSpace(displayName) != "" {
		return displayName
	}
	return fallback
}

// ── config resolution ────────────────────────────────────────────────────────

// iamBase resolves the IAM management HTTP base. CLOUD_IAM_HTTP_URL wins (the
// in-cluster Service, e.g. http://iam.hanzo.svc.cluster.local:8000); otherwise
// the public issuer (deps.IAMIssuer, e.g. https://hanzo.id) which also serves
// /v1/iam/*. Empty only when neither is set (endpoint reports not-configured).
func iamBase(deps cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_IAM_HTTP_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(deps.IAMIssuer)
}

// o11yHealthURL resolves the o11y health probe URL for the System Health source.
// CLOUD_O11Y_HEALTH_URL wins; else the in-cluster o11y Service default.
func o11yHealthURL() string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_O11Y_HEALTH_URL")); v != "" {
		return v
	}
	return "http://o11y.hanzo.svc.cluster.local:80/v1/o11y/health"
}

// adminOrgOf resolves the admin org slug (IAM's IsGlobalAdmin owner). IAM_ADMIN_ORG
// mirrors config.go's default; "admin" is the fleet-wide default.
func adminOrgOf(_ cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("IAM_ADMIN_ORG")); v != "" {
		return v
	}
	return "admin"
}

func init() {
	// Order 146: after productsvc (145); the admin surface has no ordering
	// dependency (it fans out over HTTP), placed adjacent to the other console
	// read facades.
	cloud.Register("admin", 146, cloud.Typed(Mount))
}

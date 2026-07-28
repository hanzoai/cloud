// Package admin mounts the god-mode admin surface (/v1/admin/*) the Hanzo Admin Console
// (admin.hanzo.ai, apps/operator) calls, per the api.ts contract.
//
// It is an AGGREGATOR, not a new store: identity (orgs/users/roles/applications/audit/me)
// is read from IAM, the money panels (spend/tokens/credits) from commerce, and System
// Health from o11y — every one a real upstream. The facade fans out over HTTP, shaping
// the reads into the /v1 envelope { status, msg, data, data2 } the operator's transport
// decodes.
//
// The subsystem is decomposed into a shared kernel (clients/admin/core) plus one package
// per handler domain (audit/customer/revenue/finance). This file is the Mount: it builds
// the ONE core.State from Deps, then registers each domain's routes alongside the
// top-level reads (me/overview/orgs/users/usage/roles/applications/products/compute/o11y/
// analytics/bases + the flags/waitlist control plane).
//
// SECURITY — TWO tiers off ONE identity predicate, both fail-closed. PLATFORM routes are
// SuperAdmin ONLY (core.Guard). ORG-SCOPED routes (me/overview/orgs/users/usage/analytics/
// bases) are core.GuardScoped: a SuperAdmin sees EVERY tenant; any other validated admin
// caller is HARD-limited to their OWN org subtree by core.ResolveScope/ScopedOrgs.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/audit"
	"github.com/hanzoai/cloud/clients/admin/commerce"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/customer"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/hanzoai/cloud/clients/admin/finance"
	"github.com/hanzoai/cloud/clients/admin/health"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/hanzoai/cloud/clients/admin/infra"
	"github.com/hanzoai/cloud/clients/admin/invoices"
	"github.com/hanzoai/cloud/clients/admin/metrics"
	"github.com/hanzoai/cloud/clients/admin/revenue"
	"github.com/hanzoai/cloud/clients/admin/subscriptions"
	"github.com/hanzoai/cloud/clients/commerce/transport"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Mount registers the /v1/admin/* surface on app. Every handler gates on c.IsAdmin()
// first (via core.Guard/GuardScoped), then aggregates real upstream data.
//
// The state is built from Deps fields NOT on cloud.Base (deps.Audit, deps.IAMIssuer), so
// it constructs the cloud.Service value directly (cloud.NewBase + &cloud.Service[core.State]{…})
// rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("admin.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("admin.Mount: nil deps.Logger")
	}
	b := cloud.NewBase(deps, "admin")
	s := &cloud.Service[core.State]{
		Base: b,
		State: core.State{
			IAM:        iam.New(iamBase(deps)),
			Commerce:   commerce.New(transport.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
			Health:     health.New(o11yHealthURL()),
			DO:         digitalocean.New(doTokenFromEnv()),
			AdminOrg:   adminOrgOf(deps),
			AuditStore: deps.Audit,
			WLTenants:  wlTenantsFromEnv(),
		},
	}

	routes(app, s, deps.Self)

	b.Log.Info("admin surface mounted",
		"prefix", "/v1/admin",
		"iam", s.State.IAM.Ready(),
		"commerce", s.State.Commerce.Ready(),
		"digitalocean", s.State.DO.Ready(),
		"adminOrg", s.State.AdminOrg,
	)
	return nil
}

// routes registers the /v1/admin/* surface on app, threading the ONE service value
// through the two-tier gate: org-scoped panels behind core.GuardScoped, the platform
// control plane behind core.Guard. Each carved-out domain (audit/customer/revenue/finance)
// owns its own route registration.
//
// self is this replica's id (Deps.Self), threaded through for the ONE read that reports
// on the host itself rather than an upstream (/plugins).
func routes(app cloud.Router, s *cloud.Service[core.State], self string) {
	g := app.Group("/v1/admin")
	// Org-scoped panels — GuardScoped. Cross-tenant reads are impossible for a non-super
	// caller.
	g.Get("/me", core.GuardScoped(s, me))
	g.Get("/overview", core.GuardScoped(s, overview))
	g.Get("/orgs", core.GuardScoped(s, orgs))
	g.Get("/users", core.GuardScoped(s, users))
	g.Get("/usage", core.GuardScoped(s, usage))
	// Platform reads — SuperAdmin only (cross-tenant by nature).
	g.Get("/roles", core.Guard(s, roles))
	g.Get("/applications", core.Guard(s, applications))
	g.Get("/products", core.Guard(s, products))
	g.Get("/compute", core.Guard(s, compute))
	g.Get("/block-storage", core.Guard(s, blockStorage))
	g.Get("/o11y", core.Guard(s, o11y))
	// Per-subsystem lens on the one binary: the mount inventory (what is on/off) fused
	// with the RED signals the request span already carries. See subsystems.go.
	g.Get("/subsystems", core.Guard(s, subsystems))
	// The ONE consolidated financial view — revenue, credits, spend by org, infra cost.
	// See moneyboard.go.
	g.Get("/money", core.Guard(s, moneyBoardHandler))
	g.Get("/aimetrics", core.Guard(s, aimetrics))
	// What this HOST is actually running (plugins.go). The only read here that asks
	// the process rather than an upstream — so it takes the Router, whose Plugins()
	// is the read-only window onto the app. Self names the replica answering.
	g.Get("/plugins", core.Guard(s, pluginsBoard(app, self)))
	g.Post("/sync", core.Guard(s, syncNow))

	// Credit grants — the ONE admin mint surface (SuperAdmin only). Thin, audited
	// relay to commerce's mint-gated POST /v1/billing/credit-grants; commerce is the
	// sole ledger. See creditgrant.go.
	g.Post("/credit-grants", core.Guard(s, createCreditGrant))

	// Product analytics — org-scoped (SuperAdmin: all-orgs; org admin: their own org).
	g.Get("/analytics", core.GuardScoped(s, analytics))
	// Bases — the tenant Base-instance panel, org-scoped (bases.go).
	g.Get("/bases", core.GuardScoped(s, bases))

	// ── Platform control plane — SuperAdmin ONLY (launch/release/flags + access). ──
	g.Get("/flags", core.Guard(s, flagsBoard))
	g.Put("/flags/:key", core.Guard(s, setFlag))
	// Launch-control services board — the waitlist-mode lens on the flag engine (twin
	// of /v1/admin/flags), reading the registry + decide the admission gate owns.
	g.Get("/services", core.Guard(s, services))
	g.Post("/services", core.Guard(s, upsertService))
	g.Post("/services/:service/mode", core.Guard(s, setServiceMode))
	g.Get("/waitlist", core.Guard(s, waitlist))
	g.Post("/waitlist/boost", core.Guard(s, waitlistBoost))

	// Usage-cap + promo control plane (promos platform-only; spend-caps org-scoped).
	limitRoutes(app, s)

	// ── Carved-out domains own their routes (audit/customer/revenue/finance +
	// the billing fleet views metrics/invoices/subscriptions). ──
	audit.Routes(app, s)
	customer.Routes(app, s)
	revenue.Routes(app, s)
	finance.Routes(app, s)
	metrics.Routes(app, s)
	infra.Routes(app, s)
	invoices.Routes(app, s)
	subscriptions.Routes(app, s)
}

// ── /v1/admin/me — operator identity (AdminMe) ───────────────────────────────

// me answers with the validated operator identity. The gate already proved this is an
// admin, so the fields come from the sanitized identity headers — authoritative and never
// client-forgeable.
func me(s *cloud.Service[core.State], c *zip.Ctx) error {
	sc := core.ResolveScope(s, c)
	owner, _ := principal.Org(c)
	if owner == "" && sc.Super {
		owner = s.State.AdminOrg
	}
	name := strings.TrimSpace(c.User())
	return core.OK(c, adminMe{
		Owner:        owner,
		Name:         name,
		Email:        strings.TrimSpace(c.UserEmail()),
		DisplayName:  name,
		IsSuperAdmin: sc.Super,
		// The gate (GuardScoped) already proved this caller is either a SuperAdmin or an
		// admin of an ENABLED WL tenant, so an admitted non-super IS the WL tier — no
		// separate lookup needed. ScopeOrgs is the resolved subtree (empty ⇒ all, for super).
		IsWhiteLabel: !sc.Super,
		ScopeOrgs:    sc.Orgs,
	})
}

// ── /v1/admin/orgs — tenant directory (OrgRow[]) ─────────────────────────────

func orgs(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	orgs, err := core.ScopedOrgs(s, ctx, c, cr)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	rows := make([]orgRow, 0, len(orgs))
	for _, o := range orgs {
		users := orgUserCount(s, ctx, cr, o.Name)
		// orgs is a per-ROW panel (OrgRow[] via OKList; it carries NO sources[] channel):
		// a failed read degrades THAT org's row to an honest zero, never a fleet total that
		// falsely reads healthy. The aggregate-freshness signal lives on /overview.
		spend, credits, _ := core.OrgMoney(s, ctx, o.Name)
		rows = append(rows, orgRow{
			Org:          o.Name,
			Display:      core.Display(o.DisplayName, o.Name),
			Users:        users,
			Products:     0, // workload registry feed pending (platform apps table)
			SpendCents:   spend,
			CreditsCents: credits,
			Tokens:       0, // fleet token counters pending (insights/datastore)
			Created:      o.CreatedTime,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Org < rows[j].Org })
	return core.OKList(c, rows, len(rows))
}

// ── /v1/admin/users — cross-org directory (OperatorUser[]) ───────────────────

func users(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	sc := core.ResolveScope(s, c)
	q := url.Values{}
	if !sc.Super {
		// A scoped caller lists ONLY their own org's users — the client ?org= is ignored,
		// the owner hard-pinned to the sanitized org subtree.
		if len(sc.Orgs) > 0 {
			q.Set("owner", sc.Orgs[0])
		}
	} else if owner := strings.TrimSpace(c.Query("org")); owner != "" {
		q.Set("owner", owner)
	}
	// Default pagination when the client omits it. IAM's user list returns ZERO
	// rows AND total 0 when p/pageSize are unset — which surfaced as the admin
	// directory showing "0 of 222". Default to the first page at the shared admin
	// page size so the directory populates and the REAL total is reported; an
	// explicit client p/pageSize still wins (the UI paginates from there).
	if p := strings.TrimSpace(c.Query("p")); p != "" {
		q.Set("p", p)
	} else {
		q.Set("p", "1")
	}
	if ps := strings.TrimSpace(c.Query("pageSize")); ps != "" {
		q.Set("pageSize", ps)
	} else {
		q.Set("pageSize", "200")
	}
	if term := strings.TrimSpace(c.Query("q")); term != "" {
		// IAM's list uses field/value contains-matching for the free-text filter.
		q.Set("field", "name")
		q.Set("value", term)
	}
	res, err := s.State.IAM.Users(ctx, cr, q)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	var raw []iam.User
	if len(res.Rows) > 0 {
		if err := json.Unmarshal(res.Rows, &raw); err != nil {
			return core.Fail(c, "users decode: "+err.Error())
		}
	}
	rows := make([]operatorUser, 0, len(raw))
	for _, u := range raw {
		rows = append(rows, operatorUser{
			Owner:        u.Owner,
			Name:         u.Name,
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			IsAdmin:      u.IsAdmin,
			IsSuperAdmin: u.Owner == s.State.AdminOrg,
			Tag:          u.Tag,
			Created:      u.CreatedTime,
			LastSignin:   u.LastSigninTime,
			Forbidden:    u.IsForbidden,
		})
	}
	total := res.Total
	if total < len(rows) {
		total = len(rows)
	}
	return core.OKList(c, rows, total)
}

// ── /v1/admin/roles and /applications — verbatim IAM passthrough ─────────────

func roles(s *cloud.Service[core.State], c *zip.Ctx) error {
	return iamPassthrough(s, c, "/v1/iam/get-roles")
}

func applications(s *cloud.Service[core.State], c *zip.Ctx) error {
	return iamPassthrough(s, c, "/v1/iam/get-applications")
}

// iamPassthrough forwards a paginated IAM read verbatim (the operator decodes Role /
// Application as the raw IAM wire shape). `owner` defaults to the admin org, which owns
// the platform applications.
func iamPassthrough(s *cloud.Service[core.State], c *zip.Ctx, path string) error {
	q := url.Values{}
	owner := strings.TrimSpace(c.Query("owner"))
	if owner == "" {
		owner = s.State.AdminOrg
	}
	q.Set("owner", owner)
	if p := strings.TrimSpace(c.Query("p")); p != "" {
		q.Set("p", p)
	}
	if ps := strings.TrimSpace(c.Query("pageSize")); ps != "" {
		q.Set("pageSize", ps)
	}
	res, err := s.State.IAM.List(c.Context(), core.CallerCreds(c), path, q)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	return core.OKRaw(c, res.Rows, res.Total)
}

// ── /v1/admin/usage — fleet usage roll-up (UsageData) ────────────────────────

// usage returns the real fleet money totals from commerce. The daily series and the
// per-product breakdown are NOT derivable from the commerce billing API (they live in
// insights/datastore); admin returns the honest empty series/byProduct rather than
// fabricating a trend.
func usage(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	sc := core.ResolveScope(s, c)
	org := strings.TrimSpace(c.Query("org"))
	if !sc.Super {
		// A scoped caller reads ONLY their own org's usage — the client ?org= is ignored,
		// the org hard-pinned to the sanitized subtree.
		org = ""
		if len(sc.Orgs) > 0 {
			org = sc.Orgs[0]
		}
	}

	var spend int64
	switch {
	case org != "":
		if sp, err := s.State.Commerce.Spend(ctx, org); err == nil {
			spend = int64(sp.Consumed)
		}
	case sc.Super:
		// Fleet: sum month-to-date consumption across every org.
		orgs, err := core.ListOrgs(s, ctx, cr)
		if err == nil {
			for _, o := range orgs {
				if sp, e := s.State.Commerce.Spend(ctx, o.Name); e == nil {
					spend += int64(sp.Consumed)
				}
			}
		}
	}

	return core.OK(c, usageData{
		Totals:    usageTotals{SpendCents: spend, Tokens: 0, Requests: 0},
		Series:    []usagePoint{},
		ByProduct: []usageByProduct{},
	})
}

// ── /v1/admin/products — workload registry (ProductRow[]) ────────────────────
// The handler + the fleet projection live in products.go: it reads the operator App-CR +
// drift observation through the in-process paas.CurrentFleet seam (reuse, never fork).

// ── /v1/admin/overview — Platform Overview tiles (OverviewData) ───────────────

func overview(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	now := time.Now().UTC().Format(time.RFC3339)

	var sources []core.SourceStatus
	orgCount, userCount, spend, credits := 0, 0, int64(0), int64(0)

	orgs, orgErr := core.ScopedOrgs(s, ctx, c, cr)
	sources = append(sources, core.SrcOf("iam", orgErr, len(orgs), now))
	commercePartial := false
	if orgErr == nil {
		orgCount = len(orgs)
		// FAN OUT. Each org costs two independent reads (users, money), so doing this
		// serially made the dashboard's latency O(orgs): at 122 orgs that is ~244
		// blocking round-trips before a single tile renders, and it grows every time a
		// tenant signs up. The reads do not depend on each other, so they run
		// concurrently under a fixed ceiling — bounded so a large fleet cannot stampede
		// the finance ledger or the IAM store.
		const maxParallelOrgReads = 12
		var (
			mu  sync.Mutex
			wg  sync.WaitGroup
			sem = make(chan struct{}, maxParallelOrgReads)
		)
		for _, o := range orgs {
			wg.Add(1)
			go func(org string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				uc := orgUserCount(s, ctx, cr, org)
				sp, cr2, ok := core.OrgMoney(s, ctx, org)
				mu.Lock()
				defer mu.Unlock()
				userCount += uc
				spend += sp
				credits += cr2
				if !ok {
					// This org's money did not read — the fleet spend/credits totals are
					// now an UNDERCOUNT, so the commerce source must report degraded,
					// not healthy.
					commercePartial = true
				}
			}(o.Name)
		}
		wg.Wait()
	}

	// Commerce freshness derives from the SAME per-org reads the totals fold — NOT a
	// single probe org (which could read healthy while commerce was down for every other
	// org, masking an undercount). Not-configured when unwired; degraded/partial (the ONE
	// core.ErrPartialRevenue sentinel revenue/finance use) when ANY per-org read failed.
	var commerceErr error
	commerceRows := 0
	switch {
	case !s.State.Commerce.Ready():
		commerceErr = fmt.Errorf("commerce endpoint not configured")
	case commercePartial:
		commerceErr = core.ErrPartialRevenue
		commerceRows = orgCount
	default:
		commerceRows = orgCount
	}
	sources = append(sources, core.SrcOf("commerce", commerceErr, commerceRows, now))

	// o11y System Health.
	o11yRows := 0
	oOK, oErr := s.State.Health.Up(ctx)
	if oOK {
		o11yRows = 1
	}
	sources = append(sources, core.SrcOf("o11y", oErr, o11yRows, now))

	// Fleet workload registry — the operator App-CR + drift observation via the paas seam
	// (products.go). A nil/unready seam degrades to an honest-empty rollup (zeros, no error);
	// a hard observation error marks the "fleet" source degraded without failing the overview.
	fleetRows, fleetRoll, fleetErr := fleetProducts(ctx)
	sources = append(sources, core.SrcOf("fleet", fleetErr, len(fleetRows), now))

	return core.OK(c, overviewData{
		Orgs:           orgCount,
		Users:          userCount,
		Products:       fleetRoll.Total,
		ActiveProducts: fleetRoll.Active,
		Drift:          fleetRoll.Drift,
		SpendCents30d:  spend,
		Tokens30d:      0, // fleet token counters pending (insights/datastore)
		CreditsCents:   credits,
		LastSync:       now,
		Sources:        sources,
	})
}

// ── /v1/admin/sync — refresh trigger ─────────────────────────────────────────

// syncNow answers the operator's "Sync now" button. admin aggregates LIVE on every read,
// so there is no batch job to kick — the button simply re-reads. We acknowledge honestly
// with { started: true }.
func syncNow(s *cloud.Service[core.State], c *zip.Ctx) error {
	return core.OK(c, map[string]bool{"started": true})
}

// ── aggregation helpers ──────────────────────────────────────────────────────

// orgUserCount returns the member count for one org from the IAM list total (data2).
// Best-effort: an error yields 0 rather than failing the whole row.
func orgUserCount(s *cloud.Service[core.State], ctx context.Context, cr iam.Creds, org string) int {
	q := url.Values{}
	q.Set("owner", org)
	q.Set("p", "1")
	q.Set("pageSize", "1")
	res, err := s.State.IAM.Users(ctx, cr, q)
	if err != nil {
		return 0
	}
	return res.Total
}

// ── config resolution ────────────────────────────────────────────────────────

// iamBase resolves the IAM management HTTP base. CLOUD_IAM_HTTP_URL wins (the in-cluster
// Service); otherwise the public issuer (deps.IAMIssuer) which also serves /v1/iam/*.
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

// adminOrgOf resolves the admin org slug (IAM's IsSuperAdmin owner). IAM_ADMIN_ORG mirrors
// config.go's default; "admin" is the fleet-wide default.
func adminOrgOf(_ cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("IAM_ADMIN_ORG")); v != "" {
		return v
	}
	return "admin"
}

// wlTenantsFromEnv resolves the enabled white-label tenant allowlist from
// ADMIN_WL_TENANT_ORGS (comma-separated org slugs). It is the ONE seed of
// State.WLTenants — the fail-closed second admission tier: EMPTY/unset ⇒ no customer
// org-admin is admitted (SuperAdmins only), so an absent/mis-set env fails CLOSED.
// Each entry is trimmed and matched verbatim against principal.Org (the validated
// owner), never folded; blank entries are dropped. Onboarding a reseller is a
// deliberate, KMS-/git-auditable edit to this env, not a runtime self-service flip.
func wlTenantsFromEnv() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("ADMIN_WL_TENANT_ORGS"))
	if raw == "" {
		return nil
	}
	set := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if org := strings.TrimSpace(part); org != "" {
			set[org] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// doTokenFromEnv reads the DigitalOcean token from the environment. Sourced from a
// KMSSecret on the cloud deployment (DO_API_TOKEN) — never hard-coded.
func doTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("DO_API_TOKEN"))
}

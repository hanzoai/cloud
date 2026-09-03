// Package admin is the operator's view of the fleet: orgs, users, roles, spend and
// system health.
//
// It mounts the god-mode surface (/v1/admin/*) the Hanzo Admin Console
// (admin.hanzo.ai, apps/operator) calls, per the api.ts contract.
//
// It is an AGGREGATOR, not a new store: identity (orgs/users/roles/applications/audit/me)
// is read from IAM, the money panels (spend/tokens/credits) from commerce, and System
// Health from o11y — every one a real upstream. The facade fans out over HTTP, shaping
// the reads into the /v1 envelope { status, msg, data, total } the operator's transport
// decodes.
//
// The subsystem is decomposed into a shared kernel (clients/admin/core) plus one package
// per handler domain (audit/customer/revenue/finance). This file is the Use:  it builds
// the ONE core.State from Deps, then registers each domain's routes alongside the
// top-level reads (me/overview/orgs/users/usage/roles/applications/products/compute/o11y/
// analytics/bases + the flags/waitlist control plane).
//
// SECURITY — TWO tiers off ONE identity predicate, both fail-closed. PLATFORM ops are
// SuperAdmin ONLY (core.Admit). ORG-SCOPED ops (me/overview/orgs/users/usage/analytics/
// bases) call core.AdmitScoped: a SuperAdmin sees EVERY tenant; any other validated admin
// caller is HARD-limited to their OWN org subtree by core.ResolveScope/ScopedOrgs.
//
// An operation that CHANGES something asks core.Change / core.ChangeScoped instead —
// the same admission plus the estate's anti-forgery control, because this board is
// driven from a browser session cookie that any origin's request to us carries, and
// admission alone cannot tell an operator's click from a page they merely visited.
//
// SHAPE — every route is a zip TYPED op (zip.Get[In, Out]), so the /v1/admin surface is
// ONE registry with N projections: REST, the OpenAPI document, the MCP tool list and the
// CLI all derive from these declarations. Out is the /v1 envelope as a Go type, so the
// operator's contract is checked by the compiler instead of restated by hand.
package admin

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/audit"
	"github.com/hanzoai/cloud/apps/admin/commerce"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/customer"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/admin/finance"
	"github.com/hanzoai/cloud/apps/admin/health"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/hanzoai/cloud/apps/admin/infra"
	"github.com/hanzoai/cloud/apps/admin/invoices"
	"github.com/hanzoai/cloud/apps/admin/metrics"
	"github.com/hanzoai/cloud/apps/admin/revenue"
	"github.com/hanzoai/cloud/apps/admin/subscriptions"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Mount registers the /v1/admin/* surface on app. Every handler gates on the validated
// identity first — core.Admit/AdmitScoped for a read, core.Change/ChangeScoped for an
// operation that changes something — then aggregates real upstream data.
//
// The state is built from Deps fields NOT on cloud.Base (deps.Audit, deps.IAMIssuer), so
// it constructs the cloud.Service value directly (cloud.NewBase + &cloud.Service[core.State]{…})
// rather than via cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("admin.Use:  nil app")
	}
	// Every route here is a typed op, and the op registry lives on the App. A Router
	// that is not one cannot carry this surface, so the mount fails rather than
	// registering routes no projection would know about.
	if cloud.ZipApp(app) == nil {
		return fmt.Errorf("admin.Use:  %T does not expose the typed-op registry", app)
	}
	b := cloud.NewBase(deps, "admin")
	s := &cloud.Service[core.State]{
		Base: b,
		State: core.State{
			IAM:        iam.New(iamBase(deps)),
			Commerce:   commerce.New(transport.BaseURL(environ.Or("CLOUD_COMMERCE_HTTP_URL", ""))),
			Health:     health.New(o11yHealthURL()),
			DO:         digitalocean.New(doTokenFromEnv()),
			AuditStore: deps.Audit,
			WLTenants:  wlTenantsFromEnv(),
		},
	}

	routes(app, s)

	b.Log.Info("admin surface mounted",
		"prefix", "/v1/admin",
		"iam", s.State.IAM.Ready(),
		"commerce", s.State.Commerce.Ready(),
		"digitalocean", s.State.DO.Ready(),
		"adminOrg", authz.AdminOrg,
	)
	return nil
}

// routes registers the /v1/admin/* surface on app: the ONE request bridge
// (cloud.Bridge — a typed op is handed only a context, so the request it gates on is
// parked there), then every op. Each carved-out domain (audit/customer/revenue/finance/…)
// owns its own declarations.
//
// The gate is no longer a wrapper here: each handler calls core.Admit (platform) or
// core.AdmitScoped (org-scoped) on its first line, so the tier is read where the handler
// is read and applies to the MCP and CLI projections too, which never touch this router.
func routes(app cloud.Router, s *cloud.Service[core.State]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// Every op below takes the request off the context, and whoever composes the app
	// parks it there — at the root, ahead of these leaves, since fiber runs
	// middleware in registration order. This surface installs none of its own: one
	// it installed for itself could only hang on a /v1/admin node, and every op
	// below registers through the root, so that node would carry middleware over an
	// empty subtree and zip refuses to compose it.

	// Org-scoped panels — AdmitScoped. Cross-tenant reads are impossible for a
	// non-super caller.
	zip.Get(z, "/v1/admin/me", o.me, op("adminMe"))
	zip.Get(z, "/v1/admin/overview", o.overview, op("adminOverview"))
	zip.Get(z, "/v1/admin/orgs", o.orgs, op("adminOrgs"))
	zip.Get(z, "/v1/admin/users", o.users, op("adminUsers"))
	zip.Get(z, "/v1/admin/usage", o.usage, op("adminUsage"))
	// Platform reads — SuperAdmin only (cross-tenant by nature).
	zip.Get(z, "/v1/admin/roles", o.roles, op("adminRoles"))
	zip.Get(z, "/v1/admin/applications", o.applications, op("adminApplications"))
	zip.Get(z, "/v1/admin/products", products, op("adminProducts"))
	zip.Get(z, "/v1/admin/compute", compute, op("adminCompute"))
	zip.Get(z, "/v1/admin/volumes", o.volumes, op("adminVolumes"))
	zip.Get(z, "/v1/admin/o11y", o11y, op("adminO11y"))
	zip.Get(z, "/v1/admin/aimetrics", aimetrics, op("adminAIMetrics"))
	// Per-subsystem lens on the one binary: the mount inventory (what is on/off) fused
	// with the RED signals the request span already carries. See subsystems.go.
	zip.Get(z, "/v1/admin/subsystems", o.Subsystems, op("adminSubsystems"))
	// The ONE consolidated financial view — revenue, credits, spend by org, infra cost.
	// See moneyboard.go.
	zip.Get(z, "/v1/admin/money", o.Money, op("adminMoney"))
	zip.Post(z, "/v1/admin/sync", syncNow, op("adminSync"))

	// Product analytics — org-scoped (SuperAdmin: all-orgs; org admin: their own org).
	zip.Get(z, "/v1/admin/analytics", o.analytics, op("adminAnalytics"))
	// Bases — the tenant Base-instance panel, org-scoped (bases.go).
	zip.Get(z, "/v1/admin/bases", o.bases, op("adminBases"))

	// ── Platform control plane — SuperAdmin ONLY (launch/release/flags + access). ──
	zip.Get(z, "/v1/admin/flags", flagsBoard, op("adminFlags"))
	zip.Put(z, "/v1/admin/flags/:key", setFlag, op("adminSetFlag"))
	// Launch-control services board — the waitlist-mode lens on the flag engine (twin
	// of /v1/admin/flags), reading the registry + decide the admission gate owns.
	zip.Get(z, "/v1/admin/services", services, op("adminServices"))
	zip.Post(z, "/v1/admin/services", upsertService, op("adminUpsertService"))
	zip.Post(z, "/v1/admin/services/:service/mode", setServiceMode, op("adminSetServiceMode"))
	zip.Get(z, "/v1/admin/waitlist", waitlist, op("adminWaitlist"))
	zip.Post(z, "/v1/admin/waitlist/boost", o.waitlistBoost, op("adminWaitlistBoost"))

	// Usage-cap + promo control plane (promos platform-only; caps org-scoped).
	limitRoutes(z, o)

	// ── Carved-out domains own their routes (audit/customer/revenue/finance +
	// the billing fleet views metrics/invoices/subscriptions). ──
	audit.Routes(z, s)
	customer.Routes(z, s)
	revenue.Routes(z, s)
	finance.Routes(z, s)
	metrics.Routes(z)
	infra.Routes(z, s)
	invoices.Routes(z)
	subscriptions.Routes(z)
}

// ops binds the kernel to admin's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op that reads an upstream is a method value (o.orgs).
// An op that needs only the gate stays a plain function. ops carries STATE and no logic.
type ops struct{ s *cloud.Service[core.State] }

// op is the per-route metadata every admin declaration carries: a stable operation id
// (the name the OpenAPI document, the MCP tool and the CLI command all take) under the
// one "admin" tag. The summary and the prose are NOT set here — cmd/zipdoc lifts them
// from the handler's own doc comment, so they are written once, in the one place a Go
// reader already looks.
func op(id string) zip.OpOption { return zip.WithOperationID(id) }

// ── /v1/admin/me — operator identity (AdminMe) ───────────────────────────────

// me answers with the validated operator identity — who the console is signed in as,
// which tier they are, and how wide their tenant window is. The fields come from the
// sanitized identity headers the gate just read, so they are authoritative and never
// client-forgeable; nothing is looked up.
//
// Response: {"status":"ok","msg":"","data":{"owner":"admin","name":"z","email":"z@hanzo.ai",
// "displayName":"z","isSuperAdmin":true,"isWhiteLabel":false}}
func (o ops) me(ctx context.Context, _ *core.None) (*meOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	sc := core.ResolveScope(o.s, c)
	owner, _ := principal.Org(c)
	if owner == "" && sc.Super {
		owner = authz.AdminOrg
	}
	name := strings.TrimSpace(c.User())
	return &meOut{Status: core.OK, Data: &adminMe{
		Owner:        owner,
		Name:         name,
		Email:        strings.TrimSpace(c.UserEmail()),
		DisplayName:  name,
		IsSuperAdmin: sc.Super,
		// The gate (AdmitScoped) already proved this caller is either a SuperAdmin or an
		// admin of an ENABLED WL tenant, so an admitted non-super IS the WL tier — no
		// separate lookup needed. ScopeOrgs is the resolved subtree (empty ⇒ all, for super).
		IsWhiteLabel: !sc.Super,
		ScopeOrgs:    sc.Orgs,
	}}, nil
}

// ── /v1/admin/orgs — tenant directory (OrgRow[]) ─────────────────────────────

// orgs lists the tenant directory one row per org, sorted by slug: member count and the
// org's month-to-date spend and credit balance, read live from IAM and commerce.
//
// The rows are the caller's tenant window, not the fleet: a SuperAdmin gets every org, a
// white-label admin only their own subtree. A per-org read that fails degrades THAT row
// to an honest zero — this panel carries no sources[] channel to report freshness on, so
// the alternative would be a fleet total that silently reads healthy.
//
// Response: {"status":"ok","msg":"","data":[{"org":"acme","display":"Acme","users":7,
// "products":0,"spendCents":12500,"creditsCents":5000,"tokens":0,
// "created":"2026-01-04T00:00:00Z"}],"total":1}
func (o ops) orgs(ctx context.Context, in *orgsIn) (*orgsOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	cr := core.CallerCreds(c)
	all, err := core.ScopedOrgs(o.s, ctx, c, cr)
	if err != nil {
		return &orgsOut{Status: core.Err, Msg: err.Error()}, nil
	}
	// Page BEFORE the fan-out, which is the whole point: the rows we are about to
	// return decide how many per-org reads happen. Paging the answer but not the
	// work would render one screen and still pay for the directory.
	total := len(all)
	orgs := pageOrgs(all, in)
	// The whole directory's AI usage in ONE read, keyed by org — the spend and token
	// columns for every row. Per-org it would be a query per tenant, and this fleet has
	// eighty-one; a directory that costs O(orgs) round-trips gets slower every signup.
	// A tenant with no rows in the window is absent from the map and reads a true zero.
	ledger, _ := foldLedgerByOrg(ctx, ledgerScope{Since: core.WarehouseSince(usageRange)})
	members := foldUsersByOrg(o.s, ctx, cr, orgs)
	money := core.Delegate(ctx)

	// FAN OUT, for the reason the overview already does: each row costs two independent
	// reads (members, wallet) and this fleet has eighty-one tenants, so serially that is
	// 162 blocking round trips before the first row renders — and it grows with every
	// signup. It matters MORE now than it did: the wallet read used to 404 immediately,
	// which is fast in the way that a read returning nothing is fast.
	rows := make([]orgRow, len(orgs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	for i, row := range orgs {
		wg.Add(1)
		go func(i int, row iam.Org) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// orgs is a per-ROW panel (orgRow[]; it carries NO sources[] channel):
			// a failed read degrades THAT org's row to an honest zero, never a fleet total
			// that falsely reads healthy. The aggregate-freshness signal lives on /overview.
			_, credits, _ := core.OrgMoney(o.s, money, row.Name)
			used := ledger[row.Name]
			rows[i] = orgRow{
				Org:          row.Name,
				Display:      core.Display(row.DisplayName, row.Name),
				Users:        members[row.Name],
				Products:     0, // workload registry feed pending (platform apps table)
				SpendCents:   used.CostCents,
				CreditsCents: credits,
				Tokens:       used.Tokens,
				Created:      row.CreatedTime,
			}
		}(i, row)
	}
	wg.Wait()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Org < rows[j].Org })
	return &orgsOut{Status: core.OK, Data: rows, Total: core.Total(total)}, nil
}

// ── /v1/admin/users — cross-org directory (OperatorUser[]) ───────────────────

// users lists the user directory across the caller's tenant window, one page at a time.
// total is IAM's REAL total, so the console can page through it.
//
// A SuperAdmin may aim the read at one tenant with org; a white-label admin cannot — for
// them the owner is hard-pinned to their own org and org is ignored, which is what keeps
// the directory from becoming a cross-tenant read.
//
// Example: {"org":"acme","q":"ada","p":"1","pageSize":"50"}
// Response: {"status":"ok","msg":"","data":[{"owner":"acme","name":"ada","email":"ada@acme.com",
// "displayName":"Ada","isAdmin":true,"isSuperAdmin":false,"tag":"","created":"2026-01-04T00:00:00Z",
// "lastSignin":"2026-07-01T09:12:00Z","forbidden":false}],"total":222}
func (o ops) users(ctx context.Context, in *usersIn) (*usersOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	cr := core.CallerCreds(c)
	sc := core.ResolveScope(o.s, c)
	q := url.Values{}
	if !sc.Super {
		// A scoped caller lists ONLY their own org's users — the client org is ignored,
		// the owner hard-pinned to the sanitized org subtree.
		if len(sc.Orgs) > 0 {
			q.Set("owner", sc.Orgs[0])
		}
	} else if owner := strings.TrimSpace(in.Org); owner != "" {
		q.Set("owner", owner)
	}
	// The page the client asked for, in the terms the user list takes: a row
	// LIMIT and an OFFSET into the org, with the real unpaged count answered
	// alongside so the console can page through it. A 1-based page number is the
	// console's spelling and the offset is derived from it here, at the one place
	// that talks to IAM.
	term := strings.TrimSpace(in.Query)
	size := pageSizeOf(in.PageSize, 200)
	if term == "" {
		q.Set("limit", strconv.Itoa(size))
		if off := (pageOf(in.Page) - 1) * size; off > 0 {
			q.Set("offset", strconv.Itoa(off))
		}
	}
	// A free-text search reads the org WHOLE and matches here, because the user
	// list filters by owner and by nothing else. Matching a single page instead
	// would answer "no such user" for anyone who happened to sit on page two —
	// a search that is wrong exactly when it is used.
	res, err := o.s.State.IAM.Users(ctx, cr, q)
	if err != nil {
		return &usersOut{Status: core.Err, Msg: err.Error()}, nil
	}
	var raw []iam.User
	if len(res.Rows) > 0 {
		if err := json.Unmarshal(res.Rows, &raw); err != nil {
			return &usersOut{Status: core.Err, Msg: "users decode: " + err.Error()}, nil
		}
	}
	if term != "" {
		raw = matchUsers(raw, term)
		res.Total = len(raw)
	}
	rows := make([]operatorUser, 0, len(raw))
	for _, u := range raw {
		rows = append(rows, operatorUser{
			Owner:        u.Owner,
			Name:         u.Name,
			Email:        u.Email,
			DisplayName:  u.DisplayName,
			IsAdmin:      u.IsAdmin,
			IsSuperAdmin: u.Owner == authz.AdminOrg,
			Tag:          u.Tag,
			Created:      u.CreatedTime,
			LastSignin:   u.LastSigninTime,
			Forbidden:    u.IsForbidden,
		})
	}
	total := max(res.Total, len(rows))
	return &usersOut{Status: core.OK, Data: rows, Total: core.Total(total)}, nil
}

// ── /v1/admin/roles and /applications — verbatim IAM passthrough ─────────────

// roles lists IAM roles for one owner org, forwarded VERBATIM from IAM's role list.
//
// Example: {"owner":"admin","p":"1","pageSize":"50"}
// Response: {"status":"ok","msg":"","data":[{"owner":"admin","name":"ops","displayName":"Ops"}],"total":1}
func (o ops) roles(ctx context.Context, in *iamPageIn) (*iamRowsOut, error) {
	return o.iamPassthrough(ctx, in, "/v1/iam/roles", "roles")
}

// applications lists IAM applications for one owner org, forwarded VERBATIM from IAM's
// application list. These are the platform's OIDC clients — the console reads clientId
// off each row.
//
// Example: {"owner":"admin","p":"1","pageSize":"50"}
// Response: {"status":"ok","msg":"","data":[{"owner":"admin","name":"hanzo-cloud","clientId":"cid"}],"total":1}
func (o ops) applications(ctx context.Context, in *iamPageIn) (*iamRowsOut, error) {
	return o.iamPassthrough(ctx, in, "/v1/iam/applications", "applications")
}

// iamPassthrough forwards a paginated IAM read verbatim — the ONE body both IAM reads
// share. `owner` defaults to the admin org, which owns the platform applications.
//
// The rows are NOT re-decoded: they reach the operator as the exact bytes IAM sent, so
// this layer never becomes a second, drifting copy of IAM's Role/Application schema.
// That is also why the response is declared opaque rather than typed.
// Both reads answer their owner's WHOLE set, so the page fields of the shared
// input are not forwarded: neither route reads them, and sending a selector the
// far end ignores invites the reader to believe a page was asked for.
func (o ops) iamPassthrough(ctx context.Context, in *iamPageIn, path, rows string) (*iamRowsOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	owner := strings.TrimSpace(in.Owner)
	if owner == "" {
		owner = authz.AdminOrg
	}
	q.Set("owner", owner)
	res, err := o.s.State.IAM.List(ctx, core.CallerCreds(c), path, rows, q)
	if err != nil {
		return &iamRowsOut{Status: core.Err, Msg: err.Error()}, nil
	}
	page := res.Rows
	if len(page) == 0 {
		page = json.RawMessage("[]") // an absent page is an empty list, never a null
	}
	return &iamRowsOut{Status: core.OK, Data: page, Total: core.Total(res.Total)}, nil
}

// ── /v1/admin/usage — fleet usage roll-up (UsageData) ────────────────────────

// usage returns the trailing 30 days of AI usage: one org's when org names one, else the
// whole fleet's — the spend, the tokens and the requests, the daily curve behind them,
// and the split by model.
//
// It reads the AI ledger (ledger.go), which is the plane that owns this question. It used
// to ask the commerce billing API instead, once per org, and answer with a hardcoded
// empty series, zero tokens and zero requests, on the reasoning that a trend and a split
// were "not derivable from the commerce billing API". They are not — but the question was
// never commerce's. hanzo.cloud_usage carries a row per served request, so all three fall
// out of the same window the totals do.
//
// Example: {"org":"acme"}
// Response: {"status":"ok","msg":"","data":{"totals":{"spendCents":12500,"tokens":170000,"requests":42},
// "series":[{"date":"2026-08-13","spendCents":900,"tokens":12000,"requests":3}],
// "byModel":[{"model":"claude-opus-4-8","spendCents":9000,"tokens":80000}]}}
func (o ops) usage(ctx context.Context, in *usageIn) (*usageOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	sc := core.ResolveScope(o.s, c)
	org := strings.TrimSpace(in.Org)
	if !sc.Super {
		// A scoped caller reads ONLY their own org's usage — the client org is ignored,
		// the org hard-pinned to the sanitized subtree.
		org = ""
		if len(sc.Orgs) > 0 {
			org = sc.Orgs[0]
		}
	}

	// ONE scope, four reads: the totals, the daily curve and the model split all describe
	// the same window of the same rows, so they cannot disagree about which window it was.
	scope := ledgerScope{Since: core.WarehouseSince(usageRange), Org: org}
	totals := foldOf(core.CHFirstRow(ledgerRows(ctx, ledgerTotals(scope))))

	return &usageOut{Status: core.OK, Data: &usageData{
		Totals: usageTotals{
			SpendCents: totals.CostCents,
			Tokens:     totals.Tokens,
			Requests:   totals.Requests,
		},
		Series:  usagePointsFrom(ledgerRows(ctx, ledgerSeries(scope, usageBucket))),
		ByModel: usageByModelFrom(ledgerRows(ctx, ledgerByModel(scope, usageModelCap))),
	}}, nil
}

// The usage board's window and shape. The console renders a month of daily points and a
// donut that shows its top six, so a cap of ten leaves the ranking honest without paying
// for a tail nothing draws.
const (
	usageRange    = "30d"
	usageBucket   = "1 DAY"
	usageModelCap = 10
)

// usagePointsFrom projects the ledger's daily buckets onto the board's points. The bucket
// key is a DAY, not an instant — see core.CHDate.
func usagePointsFrom(rows []map[string]any) []usagePoint {
	out := make([]usagePoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, usagePoint{
			Date:       core.CHDate(r["ts"]),
			SpendCents: core.CHInt64(r["cost_cents"]),
			Tokens:     core.CHInt64(r["tokens"]),
			Requests:   core.CHInt64(r["requests"]),
		})
	}
	return out
}

// usageByModelFrom projects the ledger's model split. The model is the unit the fleet
// actually sells, so it is what this split names — the field used to be called `product`
// and was never populated, which read as "there are no products" rather than "nobody
// asked the ledger".
func usageByModelFrom(rows []map[string]any) []usageByModel {
	out := make([]usageByModel, 0, len(rows))
	for _, r := range rows {
		out = append(out, usageByModel{
			Model:      core.CHStr(r["model"]),
			SpendCents: core.CHInt64(r["cost_cents"]),
			Tokens:     core.CHInt64(r["tokens"]),
		})
	}
	return out
}

// ── /v1/admin/products — workload registry (ProductRow[]) ────────────────────
// The handler + the fleet projection live in products.go: it reads the operator App-CR +
// drift observation through the in-process paas.CurrentFleet client (reuse, never fork).

// ── /v1/admin/overview — Platform Overview tiles (OverviewData) ───────────────

// overview is the Platform Overview tiles: how many orgs and users are in the caller's
// tenant window, the fleet workload counts, and month-to-date spend and credits.
//
// It ALWAYS answers 200 — a tile board that fails as a whole because one upstream is
// down is useless. Instead every upstream reports itself in sources[]: ok, degraded, or
// not-configured. A commerce read that failed for ANY org marks that source degraded,
// because the spend/credits totals are then an undercount and must not read healthy.
//
// The AI tiles — 30-day spend and tokens — come from the AI ledger (ledger.go), the
// plane that owns "what was served". They used to come from the money plane with the
// token counter hardcoded to zero, so the board read $0.00 and 0 tokens over a month in
// which the fleet served fifteen thousand requests. Credits still come from commerce,
// which owns the wallet.
//
// Response: {"status":"ok","msg":"","data":{"orgs":2,"users":14,"products":31,
// "activeProducts":29,"drift":1,"spendCents30d":250000,"tokens30d":0,"creditsCents":10000,
// "lastSync":"2026-07-27T00:00:00Z","sources":[{"name":"iam","ok":true,"rows":2,
// "lastSync":"2026-07-27T00:00:00Z"}]}}
func (o ops) overview(ctx context.Context, _ *core.None) (*overviewOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	cr := core.CallerCreds(c)
	now := time.Now().UTC().Format(time.RFC3339)

	var sources []core.SourceStatus
	orgCount, userCount, credits := 0, 0, int64(0)

	orgs, orgErr := core.ScopedOrgs(o.s, ctx, c, cr)
	sources = append(sources, core.SrcOf("iam", orgErr, len(orgs), now))
	// commerceLedger records that a ledger ANSWERED at all — the router's word, not an
	// env var. commercePartial records that one that answered, failed.
	commercePartial, commerceLedger := false, false
	if orgErr == nil {
		orgCount = len(orgs)
		// FAN OUT. Each org costs two independent reads (users, money), so doing this
		// serially made the dashboard's latency O(orgs): at 122 orgs that is ~244
		// blocking round-trips before a single tile renders, and it grows every time a
		// tenant signs up. The reads do not depend on each other, so they run
		// concurrently under a fixed ceiling — bounded so a large fleet cannot stampede
		// the finance ledger or the IAM store.
		// ONE delegation for the whole fan-out (core.Delegate) — building it per
		// goroutine would have every one of them reading the same request.
		money := core.Delegate(ctx)
		members := foldUsersByOrg(o.s, ctx, cr, orgs)
		const maxParallelOrgReads = 12
		var (
			mu  sync.Mutex
			wg  sync.WaitGroup
			sem = make(chan struct{}, maxParallelOrgReads)
		)
		for _, row := range orgs {
			wg.Add(1)
			go func(org string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				uc := members[org]
				_, cr2, mErr := core.OrgMoney(o.s, money, org)
				mu.Lock()
				defer mu.Unlock()
				userCount += uc
				credits += cr2
				switch {
				case core.MoneyFailed(mErr):
					// This org's money did not read — the fleet credits total is now an
					// UNDERCOUNT, so the commerce source must report degraded, not healthy.
					commercePartial = true
					commerceLedger = true
				case mErr == nil:
					commerceLedger = true
				}
			}(row.Name)
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
	case orgCount > 0 && !commerceLedger:
		commerceErr = core.ErrNoLedger
	case commercePartial:
		commerceErr = core.ErrPartialRevenue
		commerceRows = orgCount
	default:
		commerceRows = orgCount
	}
	sources = append(sources, core.SrcOf("commerce", commerceErr, commerceRows, now))

	// The AI usage ledger — the 30-day spend + token tiles, from ONE fold of the window
	// the tiles name. It reports itself like every other upstream: a warehouse that is
	// not connected is a source that is DOWN, never a fleet that served nothing.
	aiSpend, aiErr := foldLedger(ctx, ledgerScope{Since: core.WarehouseSince(usageRange)})
	sources = append(sources, core.SrcOf("usage", aiErr, int(aiSpend.Requests), now))

	// o11y System Health.
	o11yRows := 0
	oOK, oErr := o.s.State.Health.Up(ctx)
	if oOK {
		o11yRows = 1
	}
	sources = append(sources, core.SrcOf("o11y", oErr, o11yRows, now))

	// Fleet workload registry — the operator App-CR + drift observation via the paas client
	// (products.go). A nil/unready client degrades to an honest-empty rollup (zeros, no error);
	// a hard observation error marks the "fleet" source degraded without failing the overview.
	fleetRows, fleetRoll, fleetErr := fleetProducts(ctx, c)
	sources = append(sources, core.SrcOf("fleet", fleetErr, len(fleetRows), now))

	return &overviewOut{Status: core.OK, Data: &overviewData{
		Orgs:           orgCount,
		Users:          userCount,
		Products:       fleetRoll.Total,
		ActiveProducts: fleetRoll.Active,
		Drift:          fleetRoll.Drift,
		SpendCents30d:  aiSpend.CostCents,
		Tokens30d:      aiSpend.Tokens,
		CreditsCents:   credits,
		LastSync:       now,
		Sources:        sources,
	}}, nil
}

// ── /v1/admin/sync — refresh trigger ─────────────────────────────────────────

// syncNow answers the operator's "Sync now" button. There is nothing to kick: admin
// aggregates LIVE on every read, so the button is just a re-read. It acknowledges
// honestly with started:true rather than pretending a batch job was queued.
//
// Response: {"status":"ok","msg":"","data":{"started":true}}
func syncNow(ctx context.Context, _ *core.None) (*syncOut, error) {
	if _, err := core.Change(ctx); err != nil {
		return nil, err
	}
	return &syncOut{Status: core.OK, Data: &syncStarted{Started: true}}, nil
}

// ── aggregation helpers ──────────────────────────────────────────────────────

// Directory page sizes. 20 is what an admin table shows before anyone scrolls;
// 100 is the ceiling the common APIs settled on (GitHub 30/100, Stripe 10/100)
// and is enough for a bulk read without letting one query walk the whole fleet.
const (
	defaultOrgPage = 20
	maxOrgPage     = 100
)

// pageOf reads a 1-based page number off the wire. Anything unreadable is the
// first page — a client that mistypes a page gets the directory, not an error.
func pageOf(s string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return 1
}

// pageSizeOf reads a row count off the wire, falling back to fallback.
func pageSizeOf(s string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// matchUsers keeps the rows whose name, display name or email contains term,
// case-insensitively — the three handles an operator types a person's name into.
func matchUsers(all []iam.User, term string) []iam.User {
	term = strings.ToLower(term)
	kept := make([]iam.User, 0, len(all))
	for _, u := range all {
		if strings.Contains(strings.ToLower(u.Name), term) ||
			strings.Contains(strings.ToLower(u.DisplayName), term) ||
			strings.Contains(strings.ToLower(u.Email), term) {
			kept = append(kept, u)
		}
	}
	return kept
}

// pageOrgs narrows the directory to one page. An out-of-range page is an empty
// page, not an error: a client that walks past the end gets a clean stop.
func pageOrgs(all []iam.Org, in *orgsIn) []iam.Org {
	page, size := 1, defaultOrgPage
	if in != nil {
		page = pageOf(in.Page)
		size = pageSizeOf(in.PageSize, defaultOrgPage)
	}
	// A ceiling, not a suggestion. Without it a caller asking for ten thousand
	// rows reinstates the unbounded directory this paging exists to remove, and
	// it would arrive as an ordinary query string rather than as a change anyone
	// reviewed.
	if size > maxOrgPage {
		size = maxOrgPage
	}
	start := (page - 1) * size
	if start >= len(all) {
		return nil
	}
	end := start + size
	if end > len(all) {
		end = len(all)
	}
	return all[start:end]
}

// foldUsersByOrg counts the members of each named org, keyed by org.
//
// It ASKS EACH ORG, because the user list is owner-scoped and answers no other
// way: the org IS the tenancy boundary IAM enforces, so "every user, then group
// them" is not a read this surface has. What it costs is one round trip per row
// the directory is about to render, and the answer to each is a single integer —
// `limit=1` so the count travels without the roster behind it.
//
// The reads do not depend on each other, so they run concurrently under the same
// fixed ceiling the customer fan-out uses: bounded so a large fleet cannot
// stampede the IAM store, and the callers page BEFORE calling this, so the work
// is bounded by the page and not by the fleet.
//
// Best-effort, as the per-org count was: a read that fails leaves that org out of
// the map, and an absent org reads a true zero rather than failing the directory.
// IAM authorizes each list as the caller, so a non-super caller counts only what
// they can already see.
func foldUsersByOrg(s *cloud.Service[core.State], ctx context.Context, cr iam.Creds, orgs []iam.Org) map[string]int {
	counts := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	for _, org := range orgs {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.State.IAM.Users(ctx, cr, url.Values{"owner": {name}, "limit": {"1"}})
			if err != nil {
				return
			}
			mu.Lock()
			counts[name] = res.Total
			mu.Unlock()
		}(org.Name)
	}
	wg.Wait()
	return counts
}

// ── config resolution ────────────────────────────────────────────────────────

// iamBase resolves the IAM management HTTP base. CLOUD_IAM_HTTP_URL wins (the in-cluster
// Service); otherwise the public issuer (deps.IAMIssuer) which also serves /v1/iam/*.
func iamBase(deps cloud.Deps) string {
	if v := environ.Or("CLOUD_IAM_HTTP_URL", ""); v != "" {
		return v
	}
	return strings.TrimSpace(deps.IAMIssuer)
}

// o11yHealthURL resolves the o11y health probe URL for the System Health source.
// CLOUD_O11Y_HEALTH_URL wins; else the in-cluster o11y Service default.
func o11yHealthURL() string {
	if v := environ.Or("CLOUD_O11Y_HEALTH_URL", ""); v != "" {
		return v
	}
	return "http://o11y.hanzo.svc.cluster.local:80/v1/o11y/health"
}

// wlTenantsFromEnv resolves the enabled white-label tenant allowlist from
// ADMIN_WL_TENANT_ORGS (comma-separated org slugs). It is the ONE seed of
// State.WLTenants — the fail-closed second admission tier: EMPTY/unset ⇒ no customer
// org-admin is admitted (SuperAdmins only), so an absent/mis-set env fails CLOSED.
// Each entry is trimmed and matched verbatim against principal.Org (the validated
// owner), never folded; blank entries are dropped. Onboarding a reseller is a
// deliberate, KMS-/git-auditable edit to this env, not a runtime self-service flip.
func wlTenantsFromEnv() map[string]bool {
	raw := environ.Or("ADMIN_WL_TENANT_ORGS", "")
	if raw == "" {
		return nil
	}
	set := map[string]bool{}
	for part := range strings.SplitSeq(raw, ",") {
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
	return environ.Or("DO_API_TOKEN", "")
}

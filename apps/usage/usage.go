// Package usage is what your org ran and what it cost, broken down per account.
//
// It serves /v1/usage over one window grammar, and absorbed the account-usage board
// from apps/links, which owns links and nothing usage. It is NOT the only usage
// address — billing serves the wallet's own /v1/billing/usage{,/accounts} — and the
// two answer different questions: this one composes the categorized roll-up, billing
// reports the raw drain on the wallet.
//
// The surface:
//
//   - POST /v1/usage          record account-usage samples (the collector's data
//     plane; write path over the hanzo.account_usage warehouse series, datastore.go).
//   - GET  /v1/usage/samples  one provider account's own lane dash (the time series).
//   - GET  /v1/usage/summary  the flagship own-scoped footprint roll-up (below).
//   - GET  /v1/usage/analytics{,/access}  the entitlement-gated rich per-org read.
//
// The summary answers "what am I running and what does it cost" by composing THREE
// complementary sources, each degrading independently to honest zeros (a source
// marker says which answered — a partial deploy never fabricates spend or usage):
//
//   - Spend (the genuinely-missing categorized cost roll-up): the commerce ledger —
//     usage/rollup (authoritative month-to-date consumed + prepaid wallet) plus the
//     raw transaction ledger, rolled up server-side into spend-by-category over
//     time. Every metered resource (GPU, machine-hours, LLM tokens, datastore
//     footprint) debits this ONE ledger with a category tag, so it is already the
//     unified cost source — this endpoint is the categorizing lens over it.
//   - LLM usage totals: hanzo.cloud_usage, the per-org warehouse ledger (the same
//     table /v1/event/* and the o11y board read). Totals only here — the
//     per-model / timeseries detail stays at /v1/event/*.
//   - The account board: the caller's OWN linked provider accounts (a Claude Max
//     plan's window %, metered from the provider's own login) beside the org's
//     Hanzo-routed usage, every row labelled by source/scope and NEVER summed. This
//     is the account-usage global view, unified here from apps/links.
//
// The console Usage view composes THIS with the existing org-scoped inventory
// endpoints (/v1/compute/machines, /v1/compute/gpus, /v1/agent, provisioning lists) for the
// per-kind counts — one screen, one categorized cost lens.
//
// TENANT ISOLATION (the bar). The org is the VALIDATED IAM owner claim (principal.Org
// — the trusted X-Org-Id the identity middleware minted from the caller's verified
// bearer, HIP-0026; NEVER a client header) AND a validated principal is required
// (c.User() set only for a verified bearer). The commerce subject is pinned
// server-side to that org; every warehouse query binds org (and, for the account
// board, subject) POSITIONALLY. A caller can only ever read/write its OWN org —
// fail-closed: no principal → 401.
//
// Its plugin does not declare OwnsHealth, so the host's generic GET /v1/usage/health
// liveness route stands — a distinct path that never shadows these. The manifest.Apps
// sequence is what binds /v1/usage/* ahead of the ai subsystem's /v1/* catch-all.
package usage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/types"
	"github.com/zap-proto/zip"
)

// maxLedgerRows bounds the ledger read backing the category/series breakdown. The
// authoritative window/MTD totals come from the rollup; this is the best-effort
// breakdown, so a generous cap keeps it accurate for normal orgs without an
// unbounded read.
const maxLedgerRows = 1000

// state is the usage subsystem's own data: the commerce S2S reader (spend) and the
// account-usage warehouse projection (samples + account board). The shared deps
// (logger, billing, brand) live in the embedded cloud.Base, reached as s.Log /
// s.Bill — never re-plumbed here.
type state struct {
	commerce  ledgerReader
	warehouse *warehouse
}

// Mount wires the usage surface onto app per HIP-0106 — one line over the generic
// subsystem entrypoint: build the state, register the routes.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "usage", build, routes)
}

// build constructs the usage state: the ledger reader (which takes no
// configuration — a peer is reached by name) and the account-usage warehouse (a
// DDL latch over clients/datastore's shared connection — no handle of its own, so
// the subsystem needs no Shutdown).
func build(b cloud.Base) (state, error) {
	// No "commerce configured" bit to report: the ledger is reached BY NAME, so
	// there is nothing a deployment sets and nothing that can be set wrong.
	b.Log.Info("usage surface", "prefix", "/v1/usage")
	return state{commerce: ledgerReader{}, warehouse: &warehouse{}}, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/usage openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic: every op
// resolves its own scope through caller() and then reads the same sources the
// helpers below read.
type ops struct{ s *cloud.Service[state] }

// routes registers the ONE usage surface — the read faces plus the account-usage
// data plane (moved here from clients/link so usage owns ALL usage).
//   - POST /usage          record account-usage samples (the collector).
//   - GET  /usage/samples  one provider account's own lane dash (time series).
//   - GET  /summary        the own-scoped footprint roll-up (spend + LLM + the
//     account board), UNGATED.
//   - GET  /analytics/access echoes a plan's resolved AnalyticsAccess (always 200,
//     free floor on error) so dashboards self-configure against the live catalog.
//   - GET  /analytics is the rich per-org read, entitlement-gated (access.Datastore).
//
// No :param routes here, so registration order is irrelevant; the generic
// /v1/usage/health liveness route (OwnsHealth=false) is a distinct path and never
// shadows these.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// cloud.Bridge is not installed here. Whoever composes the program installs it
	// once at the root — after the identity check that mints the validated org and
	// before any subsystem registers a route (serve.go) — because that order is a
	// property of the whole program and no subsystem can assert it for itself. The
	// validated org — and the request the caller's SUBJECT and the no-store header
	// ride on — still reach a typed op only by being parked on the context.
	o := ops{s: s}
	// Collection root (/v1/usage) stays flat — Group(p).Post("") yields "p/".
	zip.Post(cloud.ZipApp(app), "/v1/usage", o.record, zip.WithStatus(http.StatusAccepted))

	// Declared on the GROUP: the op's path is the prefix composed with the leaf,
	// which is the identity every projection keys on, and cmd/zipdoc resolves the
	// prefix the same way, so the prose below reaches the document and the tool list.
	g := app.Group("/v1/usage")
	zip.Get(g, "/samples", o.samples)
	zip.Get(g, "/summary", o.summary)
	zip.Get(g, "/analytics/access", o.analyticsAccess)
	zip.Get(g, "/analytics", o.analytics)
}

// usageWindowQuery is the window every money read shares: the SAME grammar
// /v1/event/* uses, so the two surfaces cannot drift.
type usageWindowQuery struct {
	// Range is the window: a count and a unit — 24h, 7d, 90d, any <N>h or <N>d —
	// or day, week, month, all, custom. Empty means 24h. A label this surface
	// does not know, or one reaching past the 730-day horizon, is refused rather
	// than silently replaced.
	Range string `json:"range"`
	// Start is the inclusive window start, RFC3339. Read only when Range is
	// custom.
	Start string `json:"start"`
	// End is the exclusive window end, RFC3339. Read only when Range is custom.
	End string `json:"end"`
}

// summary answers GET /v1/usage/summary: the caller's own usage footprint over one
// window — the categorized spend roll-up from the commerce ledger, the org's LLM
// usage totals from the warehouse, and the caller's OWN linked provider accounts
// beside the org's Hanzo-routed usage.
//
// Every source degrades INDEPENDENTLY to honest zeros and says so in `sources` and
// in its own `available` flag, so a partial deploy reports "no data" rather than
// fabricating spend. The account rows and the Hanzo rows are concatenated and never
// summed: a plan's percent is not money.
//
// The response is org-scoped from the validated principal and marked no-store — a
// signed-out caller is refused.
func (o ops) summary(ctx context.Context, in *usageWindowQuery) (*usageSummary, error) {
	// caller composes principal.Org (a VALIDATED principal — c.User() set only for a
	// verified bearer — returning the trusted, minted X-Org-Id, refusing a forged
	// header) with the subject. This is the "org from validated bearer ONLY" contract;
	// user scopes the account board to the caller's OWN linked accounts.
	org, user, ok := caller(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to view usage")
	}
	w, err := types.ParseWindow(in.Range, in.Start, in.End, time.Now())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	rangeLabel, start, end, interval := w.Label, w.Start, w.End, string(w.Interval)

	// ── Spend (commerce) ── best-effort; unconfigured/unreachable → honest zeros.
	spend := buildSpendBlock(o.s, ctx, org, start, end, interval)

	// ── LLM totals (warehouse) ── honest-empty when the datastore is not connected.
	llm, warehouseOK := buildLLMBlock(o.s, ctx, org, start, end)

	// ── Account board ── the caller's own linked provider accounts beside the org's
	// Hanzo-routed usage, over the SAME window; each side degrades independently.
	accounts := buildAccountsBlock(o.s, ctx, org, user, start, end)

	// Per-tenant money must never be cached by the browser or an intermediary.
	noStore(ctx)
	return &usageSummary{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Interval: interval,
		Scope:    usageScope{Org: org, User: user},
		Spend:    spend,
		LLM:      llm,
		Accounts: accounts,
		Sources:  Sources{Commerce: spend.Available, Warehouse: warehouseOK},
	}, nil
}

// usagePlanQuery names the plan whose analytics entitlement to resolve.
type usagePlanQuery struct {
	// Plan is a plan id from the live @hanzo/plans catalog. Empty resolves the
	// free floor, and so does an id the catalog does not know — this never fails
	// on an unknown plan.
	Plan string `json:"plan"`
}

// usageAnalyticsAccess is a plan's resolved analytics entitlement.
type usageAnalyticsAccess struct {
	// Plan echoes the plan id that was resolved, exactly as it was asked for.
	Plan string `json:"plan"`
	// Access is what that plan grants.
	Access usageAnalyticsGrant `json:"access"`
}

// usageAnalyticsGrant is the analytics decision a plan carries.
type usageAnalyticsGrant struct {
	// Datastore is whether the plan may read GET /v1/usage/analytics at all. The
	// free floor is false, and that is what a catalog outage resolves to.
	Datastore bool `json:"datastore"`
	// RetentionDays is how far back the plan may read. GET /v1/usage/analytics
	// clamps a custom window's start to this, so an older `start` returns the
	// clamped window rather than an error.
	RetentionDays int `json:"retentionDays"`
	// Export is whether the plan may export the analytics it can read.
	Export bool `json:"export"`
}

// analyticsAccess echoes a plan's resolved analytics entitlement so a dashboard can
// configure itself against the LIVE catalog instead of hardcoding tier numbers. An
// empty plan resolves the free floor, and a catalog resolution failure serves that
// same floor rather than erroring — so this always answers 200. It is a read-only
// contract echo and carries no tenant data.
func (o ops) analyticsAccess(ctx context.Context, in *usagePlanQuery) (*usageAnalyticsAccess, error) {
	planID := strings.TrimSpace(in.Plan)
	access, err := ResolveAnalyticsAccess(ctx, planID)
	if err != nil {
		o.s.Log.Warn("analytics access resolve failed; serving free floor", "plan", planID, "err", err)
	}
	return &usageAnalyticsAccess{
		Plan: planID,
		Access: usageAnalyticsGrant{
			Datastore:     access.Datastore,
			RetentionDays: access.RetentionDays,
			Export:        access.Export,
		},
	}, nil
}

// usageAnalyticsQuery is the gated analytics read's window plus the plan the gate
// resolves against.
type usageAnalyticsQuery struct {
	// End is the exclusive window end, RFC3339. Read only when Range is custom.
	End string `json:"end"`
	// Plan is the plan id whose entitlement decides access and retention. INTERIM:
	// cloud has no org-to-plan resolver yet, so the caller names the plan; when
	// that resolver lands this becomes the caller org's own plan.
	Plan string `json:"plan"`
	// Range is the window: a count and a unit — 24h, 7d, 90d, any <N>h or <N>d —
	// or day, week, month, all, custom. Empty means 24h. The window is then
	// clamped forward to the plan's retention entitlement.
	Range string `json:"range"`
	// Start is the inclusive window start, RFC3339. Read only when Range is
	// custom, and clamped forward to the plan's retention floor.
	Start string `json:"start"`
}

// analytics is the entitlement-GATED per-provider breakdown of the caller org's LLM
// usage — the paid lens over the same warehouse ledger GET /v1/usage/summary reads
// its totals from. Basic own-org usage stays ungated at /v1/usage/summary.
//
// A plan that does not grant the analytics datastore is refused with 402, and an
// unresolvable plan fails closed to the free floor, which does not grant it. The
// window is clamped forward to the plan's retention entitlement, so a tenant can
// never read older than its plan allows even with a custom start. The response is
// marked no-store.
//
// INTERIM (mirrors apps/world's limits echo): no org→plan resolver exists in cloud
// yet — the subscription lookup is owned by the billing plane and the gateway
// principal carries no plan claim — so the caller passes the plan and the gate
// resolves THAT plan's access.
func (o ops) analytics(ctx context.Context, in *usageAnalyticsQuery) (*usageAnalyticsView, error) {
	// Org from the VALIDATED bearer owner claim ONLY (never a client header). A caller
	// can only ever read its OWN org; no principal → 401 (fail closed).
	org, ok := orgOf(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to view analytics")
	}

	planID := strings.TrimSpace(in.Plan) // INTERIM: org→plan resolver replaces this line.
	access, err := ResolveAnalyticsAccess(ctx, planID)
	if err != nil {
		// Fail closed to the free floor (Datastore=false) while logging the degradation.
		o.s.Log.Warn("analytics access resolve failed; failing closed to free floor", "org", org, "plan", planID, "err", err)
	}

	// GATE: the rich analytics/datastore surface is a paid entitlement. Unknown plan
	// / catalog blip → free floor → Datastore=false → 402, never above.
	if !access.Datastore {
		return nil, zip.Errorf(http.StatusPaymentRequired, "analytics datastore is a paid feature — upgrade at console.hanzo.ai")
	}

	now := time.Now()
	w, werr := types.ParseWindow(in.Range, in.Start, in.End, now)
	if werr != nil {
		return nil, zip.ErrBadRequest(werr.Error())
	}
	rangeLabel, start, end := w.Label, w.Start, w.End
	// Clamp the window to the plan's retention entitlement: a tenant may never read
	// older than access.RetentionDays, even with a custom ?start.
	if floor := now.Add(-time.Duration(access.RetentionDays) * 24 * time.Hour); start.Before(floor) {
		start = floor
	}

	providers := buildAnalyticsBlock(o.s, ctx, org, start, end)

	// Per-tenant analytics must never be cached by the browser or an intermediary.
	noStore(ctx)
	return &usageAnalyticsView{
		Scope:         usageScope{Org: org},
		Plan:          planID,
		Range:         rangeLabel,
		Start:         start.UTC().Format(time.RFC3339),
		End:           end.UTC().Format(time.RFC3339),
		RetentionDays: access.RetentionDays,
		Export:        access.Export,
		Providers:     providers,
	}, nil
}

// usageAnalyticsView is the entitlement-gated rich read: the per-provider breakdown
// of the org's LLM usage over the retention-clamped window, plus the plan's
// retention + export decision so the console renders the right controls.
type usageAnalyticsView struct {
	// Scope is the tenant the rows were read under — the validated principal's org.
	Scope usageScope `json:"scope"`
	// Plan echoes the plan id the entitlement was resolved from.
	Plan string `json:"plan"`
	// Range is the label that was ASKED for. A plan whose retention is shorter
	// than that window is served the retention instead, so read start and end for
	// the window the rows actually cover and retentionDays for the reason — on a
	// clamped read the label is longer than what was served.
	Range string `json:"range"`
	// Start is the window's inclusive start, RFC3339 UTC, AFTER the retention
	// clamp — so it may be later than the start that was asked for.
	Start string `json:"start"`
	// End is the window's exclusive end, RFC3339 UTC.
	End string `json:"end"`
	// RetentionDays is how far back the resolved plan allows reading.
	RetentionDays int `json:"retentionDays"`
	// Export is whether the resolved plan allows exporting these rows.
	Export bool `json:"export"`
	// Providers is the per-provider roll-up over the window.
	Providers ProviderBreakdown `json:"providers"`
}

// ProviderBreakdown is the per-provider roll-up. Available=false is honest-empty
// (the datastore is not connected) — never fabricated, exactly like the summary's
// LLM block.
type ProviderBreakdown struct {
	// Available is false when the warehouse could not be read, which means "no
	// answer" and NOT "no usage" — Items is then empty for a reason.
	Available bool `json:"available"`
	// Items is one row per provider, most tokens first.
	Items []ProviderRow `json:"items"`
	// Source names the warehouse table the rows came from.
	Source string `json:"source"`
}

// ProviderRow is one provider's windowed totals. BYO/fee/account columns land with
// the hanzoai/ai metering-write branch; see buildAnalyticsBlock for the one-line
// projection extension.
type ProviderRow struct {
	// Provider is the upstream the requests were routed to, e.g. anthropic.
	Provider string `json:"provider"`
	// Requests is how many completions the org made against that provider.
	Requests int64 `json:"requests"`
	// Tokens is the total tokens those completions consumed, prompt plus
	// completion.
	Tokens int64 `json:"tokens"`
	// CostCents is what they cost the org, in US cents.
	CostCents int64 `json:"costCents"`
}

// buildAnalyticsBlock reads the org's per-provider warehouse totals over [start,end).
// It mirrors buildLLMBlock's proven degradation contract EXACTLY: honest-empty
// (Available=false) when the datastore is not connected or a query blips, so the
// gated read never 5xxs on a warehouse outage. Org is bound POSITIONALLY (never
// interpolated) — a caller can never read another org's usage.
func buildAnalyticsBlock(s *cloud.Service[state], ctx context.Context, org string, start, end time.Time) ProviderBreakdown {
	empty := ProviderBreakdown{Available: false, Items: []ProviderRow{}, Source: llmTable}
	if !datastore.Ready() {
		return empty
	}
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		s.Log.Debug("cloud_usage ensure failed; analytics honest-empty", "err", err)
		return empty
	}
	// Per-provider aggregate over the ONE hanzo.cloud_usage ledger — the same table
	// and the same `provider` column clients/analytics groups its top-models read by
	// (buildTopModels). The BYO/fee/account breakdown lands with the hanzoai/ai
	// metering-write branch; when those columns exist, extend this projection with
	// `, sum(byo_tokens) AS byo_tokens, sum(fee_cents) AS fee_cents` and add the
	// matching ProviderRow fields — a one-line projection, NOT a second datastore.
	sql := "SELECT provider, count() AS requests, sum(total_tokens) AS tokens, " +
		datastore.Spend + " AS cost_cents FROM " + llmTable +
		" WHERE timestamp >= ? AND timestamp < ? AND organization = ? " +
		"GROUP BY provider ORDER BY tokens DESC"
	rows, err := datastore.Query(ctx, sql, tsLiteral(start), tsLiteral(end), org)
	if err != nil {
		s.Log.Debug("cloud_usage per-provider query failed; analytics honest-empty", "org", org, "err", err)
		return empty
	}
	out := ProviderBreakdown{Available: true, Items: make([]ProviderRow, 0, len(rows)), Source: llmTable}
	for _, r := range rows {
		out.Items = append(out.Items, ProviderRow{
			Provider:  dsString(r["provider"]),
			Requests:  aInt64(r["requests"]),
			Tokens:    aInt64(r["tokens"]),
			CostCents: aInt64(r["cost_cents"]),
		})
	}
	return out
}

// buildAccountsBlock reads the caller's OWN linked-account usage (AccountTotals,
// user-scoped) beside the org's Hanzo-routed usage (HanzoTotals, org-scoped) over
// [start,end), folding both into the labelled account board. Each side degrades
// independently to honest-unavailable (Available=false), so half a warehouse never
// fabricates the other half. The two row sets are concatenated, NEVER summed — a
// plan's percent is not money, a provider's own spend is not a Hanzo charge — and
// each row is stamped with its source/scope server-side.
func buildAccountsBlock(s *cloud.Service[state], ctx context.Context, org, user string, start, end time.Time) Accounts {
	a := Accounts{
		Rows: []TotalView{},
		Account: SourceState{
			Scope: ScopeUser, Source: accountUsageTable,
			Note: "your own linked accounts, metered from each provider's own login; plan consumption, not a Hanzo charge",
		},
		Hanzo: SourceState{
			Scope: ScopeOrg, Source: llmTable,
			Note: "your org's Hanzo-routed inference; cost of record",
		},
	}
	if rows, ok := s.State.warehouse.AccountTotals(ctx, org, user, start, end); ok {
		a.Account.Available = true
		for _, t := range rows {
			a.Rows = append(a.Rows, toTotalView(t))
		}
	}
	if rows, ok := s.State.warehouse.HanzoTotals(ctx, org, start, end); ok {
		a.Hanzo.Available = true
		for _, t := range rows {
			a.Rows = append(a.Rows, toTotalView(t))
		}
	}
	return a
}

// buildSpendBlock reads the commerce rollup + ledger for org and rolls them into
// the cost roll-up. A commerce error is logged and degrades to honest zeros
// (Available=false) rather than failing the whole summary.
func buildSpendBlock(s *cloud.Service[state], ctx context.Context, org string, start, end time.Time, interval string) Spend {
	roll, rerr := s.State.commerce.spend(ctx, org)
	if rerr != nil {
		// ABSENCE IS THE ROUTER'S WORD. A fleet with no commerce has no spend to
		// show, and that is the honest-empty block. Everything else is an OUTAGE,
		// and it degrades to the same block only because a summary must not 5xx —
		// so it is logged as what it is rather than as a deployment shape.
		if errors.Is(rerr, cloud.ErrNoPeer) {
			s.Log.Debug("no commerce in this fleet; spend honest-empty", "org", org)
		} else {
			s.Log.Warn("commerce spend read FAILED; spend honest-empty", "org", org, "err", rerr)
		}
		return buildSpend(false, rollup{}, nil, start, end, interval)
	}
	txns, terr := s.State.commerce.transactions(ctx, org, maxLedgerRows)
	if terr != nil {
		// The rollup (authoritative totals + wallet) succeeded; only the breakdown
		// failed. Keep the totals, empty breakdown — still honest, still Available.
		s.Log.Warn("commerce ledger read failed; spend breakdown empty", "org", org, "err", terr)
		txns = nil
	}
	return buildSpend(true, roll, txns, start, end, interval)
}

// buildLLMBlock reads the org's warehouse LLM totals. Returns (honest-empty, false)
// when the datastore is not connected; (totals, true) otherwise. A query error also
// degrades to honest-empty so the summary never 5xxs on a warehouse blip.
func buildLLMBlock(s *cloud.Service[state], ctx context.Context, org string, start, end time.Time) (LLM, bool) {
	if !datastore.Ready() {
		return buildLLM(false, nil), false
	}
	// Ensure the ai-owned ledger table exists (idempotent) so a fresh warehouse
	// yields honest zeros, not an error.
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		s.Log.Debug("cloud_usage ensure failed; llm honest-empty", "err", err)
		return buildLLM(false, nil), false
	}
	// Org bound POSITIONALLY (never interpolated) — a hostile slug can't escape SQL,
	// and a caller can never read another org's usage.
	sql := "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		datastore.Spend + " AS cost_cents, uniqExact(model) AS models " +
		"FROM " + llmTable + " WHERE timestamp >= ? AND timestamp < ? AND organization = ?"
	rows, err := datastore.Query(ctx, sql, tsLiteral(start), tsLiteral(end), org)
	if err != nil {
		s.Log.Debug("cloud_usage query failed; llm honest-empty", "org", org, "err", err)
		return buildLLM(false, nil), false
	}
	var row map[string]any
	if len(rows) > 0 {
		row = rows[0]
	}
	return buildLLM(true, row), true
}

// tsLiteral formats a time as a datastore DateTime literal (UTC), bound as a string
// arg — identical to ai/object/cloud_usage.go's transport.
func tsLiteral(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// ── the ledger, asked by name ───────────────────────────────────────────────

// ledgerReader reads the two facts the cost roll-up needs — the org's
// month-to-date consumption with the wallet behind it, and the movement list the
// category/series breakdown folds over — from the process that owns the ledger.
//
// It used to be two HTTP GETs, /v1/billing/usage/rollup and
// /v1/billing/transactions, sent through the commerce transport. That transport
// does not reach a network when commerce is co-resident: it dispatches back into
// this binary's own router BY PATH, and neither route is registered here —
// commerce's api.Route() bundle is behind //go:build cloud and is never compiled
// in. Both reads were 404s. Split into per-app binaries the base URL was empty,
// the reader called itself "not configured", and the whole spend block degraded
// to honest zeros — which is how a customer's usage page came to show a blank
// month while their wallet was being debited all along.
//
// A peer is reached BY NAME, so there is no URL, no service token, and nothing a
// deployment can set wrong.
type ledgerReader struct{}

// spend reads the org's month-to-date consumption and the wallet behind it.
func (ledgerReader) spend(ctx context.Context, org string) (rollup, error) {
	reply, err := commercepeer.FinanceSpend(cloud.For(ctx, org), &client.SpendIn{})
	if err != nil {
		return rollup{}, err
	}
	if reply == nil {
		return rollup{}, errors.New("usage: commerce answered nothing")
	}
	// A month's consumption and a wallet are figures someone READS, and
	// per-token debits are routinely finer than a cent, so the rounding is
	// explicit — an exactness guard here turns a real ledger into a 502.
	consumed, err := reply.Consumed.RoundMinor()
	if err != nil {
		return rollup{}, err
	}
	balance, err := reply.Balance.RoundMinor()
	if err != nil {
		return rollup{}, err
	}
	return rollup{ConsumedCents: consumed, BalanceCents: balance}, nil
}

// transactions reads the org's ledger entries, newest first, up to limit.
func (ledgerReader) transactions(ctx context.Context, org string, limit int) ([]ledgerTxn, error) {
	reply, err := commercepeer.FinanceTxns(cloud.For(ctx, org), &client.TxnsIn{Limit: limit})
	if err != nil {
		return nil, err
	}
	if reply == nil {
		return nil, errors.New("usage: commerce answered nothing")
	}
	rows := make([]ledgerTxn, 0, len(reply.Rows))
	for _, t := range reply.Rows {
		cents, cerr := t.Amount.RoundMinor()
		if cerr != nil {
			return nil, fmt.Errorf("usage: ledger entry %s: %w", t.ID, cerr)
		}
		rows = append(rows, ledgerTxn{
			ID: t.ID,
			// The LEDGER'S own spelling, parsed once by the one recognizer. This
			// was a bare string matched against "withdraw" — a word the ledger has
			// never written; it writes finance.usage — so isSpend was false for
			// every real row and the whole breakdown summed to zero.
			Kind:      finance.ParseKind(t.Kind),
			Amount:    cents,
			Currency:  strings.ToLower(t.Amount.Currency),
			Tags:      t.Ref,
			Notes:     t.Memo,
			CreatedAt: time.Unix(t.CreatedAt, 0).UTC().Format(time.RFC3339),
		})
	}
	return rows, nil
}

// Package usage mounts the Hanzo Cloud UNIFIED USAGE surface (GET /v1/usage/summary):
// one org-scoped roll-up answering "what am I running and what does it cost" — the
// flagship footprint view. It is a native-Go composition over the two canonical
// shared cost sources, and exists because NO single endpoint aggregates this today:
//
//   - Spend (the genuinely-missing categorized cost roll-up): the commerce ledger —
//     usage-rollup (authoritative month-to-date consumed + prepaid wallet) plus the
//     raw transaction ledger, rolled up server-side into spend-by-category over
//     time. Every metered resource (GPU, machine-hours, LLM tokens, datastore
//     footprint) debits this ONE ledger with a category tag, so it is already the
//     unified cost source — this endpoint is the categorizing lens over it.
//   - LLM usage totals: hanzo.cloud_usage, the per-org warehouse ledger (the same
//     table /v1/analytics/* and the o11y board read). Totals only here — the
//     per-model / timeseries detail stays at /v1/analytics/*.
//
// The console Usage view composes THIS (cost roll-up + LLM totals) with the existing
// org-scoped inventory endpoints (/v1/machines, /v1/gpus, /v1/agents, provisioning
// lists) for the per-kind counts — one screen, this one authoritative money source.
//
// TENANT ISOLATION (the bar). The org is the VALIDATED IAM owner claim
// (principal.Org — the trusted X-Org-Id the identity middleware minted from the
// caller's verified bearer, HIP-0026; NEVER a client header) AND a validated
// principal is required (c.User() set only for a verified bearer). The commerce
// subject is pinned server-side to that org; the warehouse query binds it
// POSITIONALLY. A caller can only ever read its OWN org. Fail-closed: no principal
// → 401. Every source degrades independently to honest zeros (source markers say
// which answered) — a partial deploy never fabricates spend or usage.
//
// Registered as "usagesvc" (NOT "usage") + order 131: the name diverges from the
// /v1/usage route so serve.go's generic GET /v1/<name>/health parks at
// /v1/usagesvc/health and never shadows the summary. Order 131 binds /v1/usage/*
// before the ai subsystem's /v1/* catch-all (150).
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// maxLedgerRows bounds the ledger read backing the category/series breakdown. The
// authoritative window/MTD totals come from the rollup; this is the best-effort
// breakdown, so a generous cap keeps it accurate for normal orgs without an
// unbounded read.
const maxLedgerRows = 1000

// state is the usage subsystem's own data: the commerce S2S reader. The shared
// deps (logger, billing, brand) live in the embedded cloud.Base, reached as
// s.Log / s.Bill — never re-plumbed here.
type state struct {
	commerce *commerceReader
}

// Mount wires the usage surface onto app per HIP-0106 — one line over the generic
// subsystem entrypoint: build the state, register the routes.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "usage", build, routes)
}

// build constructs the usage state: the commerce S2S reader from its env
// (COMMERCE_SERVICE_TOKEN is a KMS-sourced secret already on the cloud env,
// never hard-coded).
func build(b cloud.Base) (state, error) {
	cr := newCommerceReader(commerceinproc.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN"))
	b.Log.Info("usage summary surface", "prefix", "/v1/usage", "commerce", cr.configured())
	return state{commerce: cr}, nil
}

// routes registers the usage surface.
//   - /summary — basic own-org usage roll-up, UNGATED.
//   - /analytics/access echoes a plan's resolved AnalyticsAccess (always 200, free
//     floor on error) so dashboards self-configure against the live @hanzo/plans
//     catalog.
//   - /analytics is the rich per-org read, entitlement-gated (access.Datastore).
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/usage/summary", cloud.Handle(s, summary))
	app.Get("/v1/usage/analytics/access", cloud.Handle(s, analyticsAccess))
	app.Get("/v1/usage/analytics", cloud.Handle(s, analytics))
}

func init() {
	cloud.Register("usage", 131, cloud.Typed(Mount))
}

// summary answers GET /v1/usage/summary. ?range=24h|7d|30d|custom (+ ?start/?end
// for custom) bounds the window — the SAME grammar as /v1/analytics/* (one window
// grammar, no drift). Composes commerce spend + warehouse LLM totals, each
// degrading independently to honest zeros.
func summary(s *cloud.Service[state], c *zip.Ctx) error {
	// principal.Org requires a VALIDATED principal (c.User() set only for a
	// verified bearer) and returns the trusted, minted X-Org-Id — refusing a forged
	// header on the no-principal path. This is the "org from validated bearer ONLY"
	// contract.
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view usage")
	}
	rangeLabel := strings.TrimSpace(c.Query("range"))
	start, end, interval, err := aiobject.ResolveCloudUsageWindow(rangeLabel, c.Query("start"), c.Query("end"), time.Now())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if rangeLabel == "" {
		rangeLabel = "24h"
	}
	ctx := c.Context()

	// ── Spend (commerce) ── best-effort; unconfigured/unreachable → honest zeros.
	spend := buildSpendBlock(s, ctx, org, start, end, interval)

	// ── LLM totals (warehouse) ── honest-empty when the datastore is not connected.
	llm, warehouseOK := buildLLMBlock(s, ctx, org, start, end)

	// Per-tenant money must never be cached by the browser or an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, Summary{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Interval: interval,
		Scope:    Scope{Org: org},
		Spend:    spend,
		LLM:      llm,
		Sources:  Sources{Commerce: spend.Available, Warehouse: warehouseOK},
	})
}

// analyticsAccess serves GET /v1/usage/analytics/access?plan=<id> — the
// machine-readable entitlement echo. It returns the resolved AnalyticsAccess for the
// requested plan (empty plan → free floor) so the console self-configures which
// analytics controls to show against the LIVE catalog instead of hardcoding tiers.
// Always 200 with the fail-closed floor on a resolution error, so a catalog blip
// never breaks the client. Read-only public contract — no tenant data here.
func analyticsAccess(s *cloud.Service[state], c *zip.Ctx) error {
	planID := strings.TrimSpace(c.Query("plan"))
	access, err := ResolveAnalyticsAccess(c.Context(), planID)
	if err != nil {
		s.Log.Warn("analytics access resolve failed; serving free floor", "plan", planID, "err", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"plan": planID,
		"access": map[string]any{
			"datastore":     access.Datastore,
			"retentionDays": access.RetentionDays,
			"export":        access.Export,
		},
	})
}

// analytics answers GET /v1/usage/analytics — the entitlement-GATED rich per-org
// lens (per-provider breakdown; BYO/fee once those columns land) over the SAME
// hanzo.cloud_usage ledger /v1/usage/summary and /v1/analytics/* read. This is the
// PAID surface; basic own-org usage stays UNGATED at /v1/usage/summary.
//
// INTERIM (mirrors clients/world/entitlement.go): no org→plan resolver exists in
// cloud yet — the subscription lookup is owned by the billing plane and the gateway
// principal carries no plan claim — so the caller passes ?plan=<id> and the gate
// resolves THAT plan's access. When the org→plan resolver lands, swap the ?plan=
// line below for the resolved caller-org plan; the gate + clamp logic is unchanged.
func analytics(s *cloud.Service[state], c *zip.Ctx) error {
	// Org from the VALIDATED bearer owner claim ONLY (never a client header). A caller
	// can only ever read its OWN org; no principal → 401 (fail closed).
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view analytics")
	}

	planID := strings.TrimSpace(c.Query("plan")) // INTERIM: org→plan resolver replaces this line.
	access, err := ResolveAnalyticsAccess(c.Context(), planID)
	if err != nil {
		// Fail closed to the free floor (Datastore=false) while logging the degradation.
		s.Log.Warn("analytics access resolve failed; failing closed to free floor", "org", org, "plan", planID, "err", err)
	}

	// GATE: the rich analytics/datastore surface is a paid entitlement. Unknown plan
	// / catalog blip → free floor → Datastore=false → 402, never above.
	if !access.Datastore {
		return zip.Errorf(http.StatusPaymentRequired, "analytics datastore is a paid feature — upgrade at console.hanzo.ai")
	}

	rangeLabel := strings.TrimSpace(c.Query("range"))
	now := time.Now()
	start, end, _, werr := aiobject.ResolveCloudUsageWindow(rangeLabel, c.Query("start"), c.Query("end"), now)
	if werr != nil {
		return zip.ErrBadRequest(werr.Error())
	}
	if rangeLabel == "" {
		rangeLabel = "24h"
	}
	// Clamp the window to the plan's retention entitlement: a tenant may never read
	// older than access.RetentionDays, even with a custom ?start.
	if floor := now.Add(-time.Duration(access.RetentionDays) * 24 * time.Hour); start.Before(floor) {
		start = floor
	}

	providers := buildAnalyticsBlock(s, c.Context(), org, start, end)

	// Per-tenant analytics must never be cached by the browser or an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, AnalyticsView{
		Scope:         Scope{Org: org},
		Plan:          planID,
		Range:         rangeLabel,
		Start:         start.UTC().Format(time.RFC3339),
		End:           end.UTC().Format(time.RFC3339),
		RetentionDays: access.RetentionDays,
		Export:        access.Export,
		Providers:     providers,
	})
}

// AnalyticsView is the entitlement-gated rich read: the per-provider breakdown of
// the org's LLM usage over the retention-clamped window, plus the plan's retention
// + export decision so the console renders the right controls.
type AnalyticsView struct {
	Scope         Scope             `json:"scope"`
	Plan          string            `json:"plan"`
	Range         string            `json:"range"`
	Start         string            `json:"start"`
	End           string            `json:"end"`
	RetentionDays int               `json:"retentionDays"`
	Export        bool              `json:"export"`
	Providers     ProviderBreakdown `json:"providers"`
}

// ProviderBreakdown is the per-provider roll-up. Available=false is honest-empty
// (the datastore is not connected) — never fabricated, exactly like the summary's
// LLM block.
type ProviderBreakdown struct {
	Available bool          `json:"available"`
	Items     []ProviderRow `json:"items"`
	Source    string        `json:"source"`
}

// ProviderRow is one provider's windowed totals. BYO/fee/account columns land with
// the hanzoai/ai metering-write branch; see buildAnalyticsBlock for the one-line
// projection extension.
type ProviderRow struct {
	Provider  string `json:"provider"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
}

// buildAnalyticsBlock reads the org's per-provider warehouse totals over [start,end).
// It mirrors buildLLMBlock's proven degradation contract EXACTLY: honest-empty
// (Available=false) when the datastore is not connected or a query blips, so the
// gated read never 5xxs on a warehouse outage. Org is bound POSITIONALLY (never
// interpolated) — a caller can never read another org's usage.
func buildAnalyticsBlock(s *cloud.Service[state], ctx context.Context, org string, start, end time.Time) ProviderBreakdown {
	empty := ProviderBreakdown{Available: false, Items: []ProviderRow{}, Source: llmTable}
	if !aiobject.DatastoreEnabled() {
		return empty
	}
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
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
		"sum(cost_cents) AS cost_cents FROM " + llmTable +
		" WHERE timestamp >= ? AND timestamp < ? AND organization = ? " +
		"GROUP BY provider ORDER BY tokens DESC"
	rows, err := aiobject.DatastoreQuery(ctx, sql, tsLiteral(start), tsLiteral(end), org)
	if err != nil {
		s.Log.Debug("cloud_usage per-provider query failed; analytics honest-empty", "org", org, "err", err)
		return empty
	}
	out := ProviderBreakdown{Available: true, Items: make([]ProviderRow, 0, len(rows)), Source: llmTable}
	for _, r := range rows {
		out.Items = append(out.Items, ProviderRow{
			Provider:  aString(r["provider"]),
			Requests:  aInt64(r["requests"]),
			Tokens:    aInt64(r["tokens"]),
			CostCents: aInt64(r["cost_cents"]),
		})
	}
	return out
}

// aString coerces a ClickHouse string cell to string across the driver/JSON
// transports (mirrors aInt64 in query.go). Non-string → "".
func aString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// buildSpendBlock reads the commerce rollup + ledger for org and rolls them into
// the cost roll-up. A commerce error is logged and degrades to honest zeros
// (Available=false) rather than failing the whole summary.
func buildSpendBlock(s *cloud.Service[state], ctx context.Context, org string, start, end time.Time, interval string) Spend {
	if !s.State.commerce.configured() {
		return buildSpend(false, rollupWire{}, nil, start, end, interval)
	}
	roll, rerr := s.State.commerce.rollup(ctx, org)
	if rerr != nil {
		s.Log.Warn("commerce rollup read failed; spend honest-empty", "org", org, "err", rerr)
		return buildSpend(false, rollupWire{}, nil, start, end, interval)
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
	if !aiobject.DatastoreEnabled() {
		return buildLLM(false, nil), false
	}
	// Ensure the ai-owned ledger table exists (idempotent) so a fresh warehouse
	// yields honest zeros, not an error.
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		s.Log.Debug("cloud_usage ensure failed; llm honest-empty", "err", err)
		return buildLLM(false, nil), false
	}
	// Org bound POSITIONALLY (never interpolated) — a hostile slug can't escape SQL,
	// and a caller can never read another org's usage.
	sql := "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents, uniqExact(model) AS models " +
		"FROM " + llmTable + " WHERE timestamp >= ? AND timestamp < ? AND organization = ?"
	rows, err := aiobject.DatastoreQuery(ctx, sql, tsLiteral(start), tsLiteral(end), org)
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

// tsLiteral formats a time as a ClickHouse DateTime literal (UTC), bound as a string
// arg — identical to ai/object/cloud_usage.go's transport.
func tsLiteral(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// ── commerce reader ──────────────────────────────────────────────────────────

// commerceReader is a thin service-to-service reader for the two commerce billing
// endpoints the cost roll-up needs (usage-rollup + transactions). It authenticates
// with the admin-scoped COMMERCE_SERVICE_TOKEN (a KMS-sourced secret already on the
// cloud env — never hard-coded) and scopes every read to ONE org via the trusted
// X-Org-Id S2S selector, which commerce's EdgeAuth honors only after it verifies
// the bearer is the service token. It is deliberately narrow (two typed reads),
// distinct from the admin god-view reader (typed MRR/COGS) and the billing proxy
// (verbatim passthrough) — a third concern: org-scoped typed roll-up.
type commerceReader struct {
	base  string
	token string
	http  *http.Client
}

func newCommerceReader(base, token string) *commerceReader {
	return &commerceReader{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  commerceinproc.Client(15 * time.Second),
	}
}

func (r *commerceReader) configured() bool { return r != nil && r.base != "" && r.token != "" }

// rollup reads GET /v1/billing/usage-rollup for org — the authoritative MTD consumed
// + prepaid wallet balance.
func (r *commerceReader) rollup(ctx context.Context, org string) (rollupWire, error) {
	var out rollupWire
	body, err := r.get(ctx, "/v1/billing/usage-rollup", org, url.Values{"user": {org}})
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("commerce rollup decode: %w", err)
	}
	return out, nil
}

// transactions reads GET /v1/billing/transactions for org (newest-first, up to
// limit) — the raw ledger the category/series breakdown rolls up. Commerce serves
// it WRAPPED as { transactions:[...] }; a bare array is tolerated so a contract
// change in either direction degrades gracefully.
func (r *commerceReader) transactions(ctx context.Context, org string, limit int) ([]ledgerTxn, error) {
	q := url.Values{"user": {org}}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	body, err := r.get(ctx, "/v1/billing/transactions", org, q)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Transactions []ledgerTxn `json:"transactions"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.Transactions != nil {
		return wrap.Transactions, nil
	}
	var rows []ledgerTxn
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("commerce transactions decode: %w", err)
	}
	return rows, nil
}

// get performs one service-token commerce GET scoped to org. The org rides X-Org-Id
// (the S2S selector commerce keys the per-org wallet under) AND the `user` query
// param — pinned server-side to the caller's OWN org, so a client can never widen
// scope. A non-2xx is an error the caller degrades to honest-empty on.
func (r *commerceReader) get(ctx context.Context, path, org string, q url.Values) ([]byte, error) {
	u := r.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("X-Org-Id", org)
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commerce status %d", resp.StatusCode)
	}
	return body, nil
}

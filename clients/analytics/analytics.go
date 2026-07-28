// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package analytics mounts the Hanzo Cloud /v1/analytics/* surface: a native-Go,
// per-org analytics read API over the `hanzo` datastore warehouse (the
// `datastore` cluster). It is the backend for the console Native Analytics module
// (unified-analytics.md §5) — two read lenses over one warehouse:
//
//   - LLM lens (REAL today): hanzo.cloud_usage, the live per-org usage ledger the
//     cloud o11y path already writes (requests, tokens, spend, models, errors).
//   - Web/commerce lens (honest-empty until the collector emits): hanzo.events.
//
// ONE datastore client. This package does NOT open its own connection: it reads
// through clients/datastore, the leaf that holds the warehouse connection for the
// whole binary and opens it from the environment on first use. DRY: one transport,
// one pool, one set of KMS-injected DATASTORE_* creds — never hard-coded, never a
// second design. (It used to reach the connection through ai/object's Bootstrap;
// that path cost 1,933 packages to call four functions, which is why the connection
// moved to a leaf importing only orm/datastore.)
//
// TENANT ISOLATION is the security bar and is enforced SERVER-SIDE on every
// request. The org is c.Org() — the value SanitizeIdentity minted from the
// VALIDATED bearer owner claim (HIP-0026), never a client header — AND every
// request must carry a validated principal (c.User() set, which SanitizeIdentity
// sets ONLY for a verified bearer). This closes the Phase-1 "no-bearer + forged
// X-Org-Id direct-to-pod" cross-tenant read exactly as clients/s3 does. Every
// datastore query binds the org POSITIONALLY (query.go llmWhere/eventsWhere), so
// a maxpower token can NEVER read another org's analytics.
//
// Surface (all org-scoped; /v1 only; read-only):
//
//	GET /v1/analytics/overview     per-org KPIs (llm real; web/commerce honest-empty)
//	GET /v1/analytics/timeseries   requests/tokens/spend over time (hour|day buckets)
//	GET /v1/analytics/top          top models (real) + products + behavior lenses
//	                               (topPages/topReferrers/topSources over the events lens)
//	GET /v1/analytics/health       subsystem health (datastore connectivity + lens tables)
//
// Registered as id "analytics" with cloud.HealthOwner + order 132: it serves its
// OWN /v1/analytics/health (below), and cloud.HealthOwner makes serve.go skip the
// generic GET /v1/<name>/health so the always-ok route never shadows the real
// probe — the same flag the kms/paas/s3 subsystems use. Order 132 binds
// /v1/analytics/* before the ai subsystem's /v1/* catch-all (150).
package analytics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/hanzoai/types"
	"github.com/zap-proto/zip"
)

const (
	// defaultTop / maxTop bound the /top result cardinality.
	defaultTop = 10
	maxTop     = 100
	// probeTimeout bounds the health-endpoint table-existence probes so an
	// unauthenticated liveness hit can never hang on a slow warehouse.
	probeTimeout = 3 * time.Second
)

// state is analytics' own data: none — it holds no store (it rides the SAME shared
// datastore client the ai subsystem opens). Shared deps live in cloud.Base.
type state struct{}

// Mount wires the analytics surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "analytics", build, routes)
}

// build carries no per-subsystem state — analytics reads the shared warehouse. It
// records the informative mount line and installs the site-host ingest carve.
func build(b cloud.Base) (state, error) {
	b.Log.Info("analytics surface", "warehouse", "hanzo", "brand", b.Brand)
	installHostCarve(b)
	return state{}, nil
}

// installHostCarve wires the published-site-host beacon ingest (the twin of base's
// sites.SetBaseHostHandler): a page served on a site host can POST its OWN analytics
// beacon to the canonical /v1/event (or the deprecated /v1/analytics{,/batch} and
// /v1/insights/e beacons kept working mid-migration) and have it ingested into
// hanzo.events under the site's resolved Org — the server-supplied, host-derived
// tenant, never a body/header claim.
//
// It goes STRAIGHT to the ANONYMOUS lane (publicIngest), and this is the honest
// description of the door rather than a policy applied to it: sites.Middleware runs
// BEFORE the identity boundary (serve.go — sites at 241, IdentityMiddleware at 267),
// so on a site host c.User()/c.Org() are still RAW client headers and NOTHING here can
// be vouched for. A published site is a public artifact and its beacons are anonymous
// by construction, so they get the anonymous capability: the pageview/error allowlist
// and the field projection (no revenue, no personId, no groupId, no property bag), the
// 50-event / 64 KiB bounds, the per-IP and per-peer rate caps, and the DNT gate.
//
// The Site's org is the anonymous TENANT, so a customer's own site analytics keep
// landing in the customer's org — the same host-derived tenant this host is already
// trusted for when the file plane serves its bytes and the Base carve serves its data.
// A caller wanting FULL capability presents a credential to api.hanzo.ai/v1/event,
// which sits behind the identity boundary where a credential can actually be checked.
//
// Gated by the SAME already-existing flag the anonymous ingest path uses —
// CLOUD_ANALYTICS_PUBLIC_CAPTURE (publicCaptureEnabled, default ON) — so a site
// host accepts its own beacons out of the box, and turning public capture off also
// removes this carve (a site host then 405s a beacon POST, unchanged). sites.Middleware
// gates the carve on method POST and on an exact ingest-path set, so the authenticated
// GET read lenses are never hijacked.
func installHostCarve(b cloud.Base) {
	if !publicCaptureEnabled() {
		b.Log.Info("analytics public-host ingest carve disabled", "flag", publicCaptureEnv)
		return
	}
	sites.SetAnalyticsHostHandler(func(org string, c *zip.Ctx) error {
		// Only the WIRE differs per path; capability and tenant are the same for all
		// three, which is the whole point of routing them through one lane.
		if c.Path() == "/v1/insights/e" { // deprecated PostHog-wire beacon
			return publicIngest(c, decodeInsights, org, sourcePostHog)
		}
		if c.Path() == "/v1/event" { // the canonical door, canonical wire
			return publicIngest(c, decodeIngest, org, sourceEvent)
		}
		return publicIngest(c, decodeIngest, org, sourceCapture) // /v1/analytics{,/batch}
	})
	b.Log.Info("analytics public-host ingest carve enabled", "flag", publicCaptureEnv)
}

// routes registers the analytics surface. Health owns /v1/analytics/health
// explicitly (not JWT-gated: liveness must be probe-able); the data endpoints are
// all org-gated in-handler.
func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/analytics/health", cloud.Handle(s, health))
	app.Get("/v1/analytics/overview", cloud.Handle(s, overview))
	app.Get("/v1/analytics/timeseries", cloud.Handle(s, timeseries))
	app.Get("/v1/analytics/top", cloud.Handle(s, top))

	// Capture (WRITE) side — the ingest that fills hanzo.events. POST /v1/event
	// (event.go) is the ONE canonical front door serving EVERY auth context (IAM
	// bearer | pk_ publishable key | site-host-forced | ANONYMOUS, public.go) and
	// EVERY wire shape (Event | [Event] | {batch}) into the ONE write core
	// (ingestEvents). Every other route below is a thin alias/shim delegating to it.
	app.Post("/v1/event", cloud.Handle(s, eventIngest))

	// /v1/ingest — a THIN DEPRECATED ALIAS of /v1/event (delegates to the exact
	// eventHandle logic: pk_ auth now lives on the canonical door). /v1/ingest/keys
	// mints a pk_ for the caller's org (minting is a distinct concern, not ingest);
	// /v1/errors is the type:'error' read lens (validated principal — reads never
	// accept the write-only key).
	app.Post("/v1/ingest", cloud.Handle(s, ingest))
	app.Get("/v1/errors", cloud.Handle(s, errorsLens))

	// DEPRECATED foreign-protocol ingest shims — external-SDK compat ONLY; no Hanzo
	// surface uses these (Hanzo surfaces POST /v1/event). They normalize their own
	// wire onto CaptureEvent and funnel through the SAME write core (log a one-shot
	// deprecation, keep working so external Segment/beacon callers are unbroken).
	// /v1/analytics{,/batch} and /v1/tracker speak the Segment/beacon CaptureBatch
	// wire; /v1/tracker is a bare route (never collides with /v1/tracker/projects*).
	app.Post("/v1/analytics", cloud.Handle(s, capture))
	app.Post("/v1/analytics/batch", cloud.Handle(s, capture))
	app.Post("/v1/tracker", cloud.Handle(s, capture))

	// /v1/insights — console reads over the SAME engine + the DEPRECATED PostHog-
	// wire ingest shim (/v1/insights/e → the ONE write core; external PostHog SDK
	// compat only). Flags live at /v1/flags.
	app.Get("/v1/insights/health", cloud.Handle(s, insightsHealth))
	app.Post("/v1/insights/e", cloud.Handle(s, insightsIngest))
	app.Get("/v1/insights/events", cloud.Handle(s, insightsEvents))
}

// ── shared helpers ──────────────────────────────────────────────────────────

// tenant resolves the org — the tenant-isolation KEY — through principal.Org, the
// ONE org accessor. It requires a validated principal (refusing the Phase-1
// no-bearer forged-X-Org-Id data path exactly as clients/s3 does) and returns the
// org used EXACTLY as minted (only trimmed, never case-folded) but CLONED: the org
// keys the cloud_usage ledger PAST request end (telemetry, async meters), and
// c.Org() is a zero-copy view into the reused fasthttp buffer, so it must be a
// stable owned copy — the retained-buffer fix.
func tenant(c *zip.Ctx) (string, bool) {
	return principal.Org(c)
}

// window resolves the [start,end) window + bucket interval from ?range/?start/?end,
// reusing hanzoai/types.ParseWindow so analytics and the console Overview share ONE
// window grammar (24h|7d|30d|custom). A bad range is a 400.
func window(c *zip.Ctx) (time.Time, time.Time, string, string, error) {
	w, err := types.ParseWindow(c.Query("range"), c.Query("start"), c.Query("end"), time.Now())
	if err != nil {
		return time.Time{}, time.Time{}, "", "", zip.ErrBadRequest(err.Error())
	}
	return w.Start, w.End, string(w.Interval), w.Label, nil
}

// requireDatastore returns the honest 503 when the datastore ledger is not
// connected, rather than fabricating zeros. Mirrors ai/object's read gate.
func requireDatastore() error {
	if !datastore.Ready() {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: datastore (datastore) not connected")
	}
	return nil
}

// warehouseErr maps a datastore query failure to the HONEST HTTP status. A
// connectivity failure — the warehouse became unreachable mid-request (dial /
// i/o timeout / refused / reset / EOF) — is a transient 503 "unavailable", the
// SAME contract requireDatastore() uses when the pool never connected. Only a
// REACHABLE warehouse that rejected the query (bad SQL, protocol error) is a 502
// bad-gateway. This is the fix for /v1/analytics/* surfacing a raw 502 on a
// datastore `:9000` i/o timeout — the caller now gets an honest 503 it can retry.
func warehouseErr(kind string, err error) error {
	if isWarehouseUnreachable(err) {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %s: %v", kind, err)
	}
	return zip.Errorf(http.StatusBadGateway, "analytics %s query: %v", kind, err)
}

// isWarehouseUnreachable reports whether err is a transport/connectivity failure
// to datastore (as opposed to a query the warehouse actively rejected). It checks
// the typed context/net signals first, then the connectivity strings the
// datastore-go driver surfaces without a typed net.Error wrapper.
func isWarehouseUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{
		"i/o timeout", "timeout", "connection refused", "connection reset",
		"no route to host", "broken pipe", "eof", "network is unreachable",
		"no such host", "dial ", "connect: ", "read: connection", "write: connection",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func topLimit(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return defaultTop
	}
	if n > maxTop {
		return maxTop
	}
	return n
}

// ── /v1/analytics/overview ──────────────────────────────────────────────────

func overview(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	start, end, interval, rangeLabel, err := window(c)
	if err != nil {
		return err
	}
	if err := requireDatastore(); err != nil {
		return err
	}
	ctx := c.Context()
	// Ensure the ai-owned ledger table exists (idempotent, latched) so a fresh
	// warehouse yields honest zeros, not an error. We NEVER create hanzo.events —
	// that table is operator-owned (unified-analytics.md §3.1).
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}

	// LLM lens — REAL per-org KPIs.
	where, args := llmWhere(org, start, end)
	llmSQL := "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents, uniqExact(model) AS models, uniqExact(provider) AS providers, " +
		"countIf(status = 'error') AS errors FROM " + llmTable + " WHERE " + where
	llmRows, err := datastore.Query(ctx, llmSQL, args...)
	if err != nil {
		return warehouseErr("llm", err)
	}
	llm := buildLLMOverview(firstRow(llmRows))

	// Web/commerce lens — one events query; degrades to honest-empty if the events
	// table is absent (not yet provisioned) or errors.
	ewhere, eargs := eventsWhere(org, start, end)
	eventsSQL := "SELECT countIf(event = '$pageview') AS pageviews, uniqExact(distinct_id) AS visitors, " +
		"uniqExact(session_id) AS sessions, countIf(event = 'order_completed') AS orders, " +
		"toFloat64(sum(revenue)) AS revenue FROM " + eventsTable + " WHERE " + ewhere
	eventsRows, eerr := datastore.Query(ctx, eventsSQL, eargs...)
	eventsOK := eerr == nil
	if eerr != nil {
		s.Log.Debug("events lens unavailable (honest-empty)", "err", eerr)
	}
	erow := firstRow(eventsRows)

	return c.JSON(http.StatusOK, Overview{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Interval: interval,
		Scope:    Scope{Org: org},
		LLM:      llm,
		Web:      buildWebOverview(erow, eventsOK),
		Commerce: buildCommerceOverview(erow, eventsOK),
	})
}

// ── /v1/analytics/timeseries ────────────────────────────────────────────────

func timeseries(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	start, end, interval, rangeLabel, err := window(c)
	if err != nil {
		return err
	}
	if err := requireDatastore(); err != nil {
		return err
	}
	ctx := c.Context()
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}

	// bucketFn is a CLOSED server-chosen enum (never user input), so interpolating
	// it is injection-safe; the org + time bounds stay bound parameters.
	bucketFn := "Hour"
	if interval == "day" {
		bucketFn = "Day"
	}
	where, args := llmWhere(org, start, end)
	seriesSQL := fmt.Sprintf("SELECT toStartOf%s(timestamp, 'UTC') AS bucket, count() AS requests, "+
		"sum(total_tokens) AS tokens, sum(cost_cents) AS cost_cents FROM %s WHERE %s GROUP BY bucket ORDER BY bucket",
		bucketFn, llmTable, where)
	rows, err := datastore.Query(ctx, seriesSQL, args...)
	if err != nil {
		return warehouseErr("timeseries", err)
	}

	return c.JSON(http.StatusOK, Timeseries{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Interval: interval,
		Scope:    Scope{Org: org},
		Series:   buildSeries(start, end, interval, rows),
		Source:   llmTable,
	})
}

// ── /v1/analytics/top ───────────────────────────────────────────────────────

func top(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	start, end, _, rangeLabel, err := window(c)
	if err != nil {
		return err
	}
	if err := requireDatastore(); err != nil {
		return err
	}
	ctx := c.Context()
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	limit := topLimit(c)

	// Top models — REAL. limit is a validated int (never user text) so %d is safe;
	// org + time stay bound parameters.
	where, args := llmWhere(org, start, end)
	modelSQL := fmt.Sprintf("SELECT model, any(provider) AS provider, count() AS requests, "+
		"sum(total_tokens) AS tokens, sum(cost_cents) AS cost_cents FROM %s WHERE %s "+
		"GROUP BY model ORDER BY cost_cents DESC, requests DESC LIMIT %d", llmTable, where, limit)
	modelRows, err := datastore.Query(ctx, modelSQL, args...)
	if err != nil {
		return warehouseErr("top-models", err)
	}

	// Top products — honest-empty until commerce emits order events.
	ewhere, eargs := eventsWhere(org, start, end)
	prodSQL := fmt.Sprintf("SELECT product_id AS productId, countIf(event = 'order_completed') AS orders, "+
		"toFloat64(sum(revenue)) AS revenue, sum(quantity) AS units FROM %s WHERE %s AND product_id != '' "+
		"GROUP BY product_id ORDER BY revenue DESC LIMIT %d", eventsTable, ewhere, limit)
	prodRows, perr := datastore.Query(ctx, prodSQL, eargs...)

	// Behavior lenses over hanzo.events — WHERE people go / WHAT they look at
	// (topPages) and where they come FROM (topReferrers organic/referral,
	// topSources campaigns). Each is ONE pageview breakdown that degrades to
	// honest-empty if the events table is absent or the query errors — never a 500
	// (mirrors the overview web lens exactly).
	pageSQL, pageArgs := breakdownSQL(pageKeyExpr, org, start, end, limit)
	pageRows, pageErr := datastore.Query(ctx, pageSQL, pageArgs...)
	if pageErr != nil {
		s.Log.Debug("topPages lens unavailable (honest-empty)", "err", pageErr)
	}
	refSQL, refArgs := breakdownSQL(referrerKeyExpr, org, start, end, limit)
	refRows, refErr := datastore.Query(ctx, refSQL, refArgs...)
	if refErr != nil {
		s.Log.Debug("topReferrers lens unavailable (honest-empty)", "err", refErr)
	}
	srcSQL, srcArgs := breakdownSQL(sourceKeyExpr, org, start, end, limit)
	srcRows, srcErr := datastore.Query(ctx, srcSQL, srcArgs...)
	if srcErr != nil {
		s.Log.Debug("topSources lens unavailable (honest-empty)", "err", srcErr)
	}

	return c.JSON(http.StatusOK, Top{
		Range:     rangeLabel,
		Start:     start.UTC().Format(time.RFC3339),
		End:       end.UTC().Format(time.RFC3339),
		Scope:     Scope{Org: org},
		Models:    buildTopModels(modelRows),
		Products:  buildTopProducts(prodRows, perr == nil),
		Pages:     buildBreakdown(pageRows, pageErr == nil),
		Referrers: buildBreakdown(refRows, refErr == nil),
		Sources:   buildBreakdown(srcRows, srcErr == nil),
	})
}

// ── /v1/analytics/health ────────────────────────────────────────────────────

// health is a REAL probe: it reports datastore connectivity (the load-bearing
// signal) and, when connected, the availability of each lens table. Not
// JWT-gated (liveness must be probe-able) and it NEVER reads tenant data — only
// table existence. 503 when the warehouse is unreachable so a readiness probe
// can gate; 200 otherwise even if the events lens is not yet provisioned (that is
// honest-empty, not a failure).
func health(s *cloud.Service[state], c *zip.Ctx) error {
	connected := datastore.Ready()
	res := map[string]any{
		"service":   "analytics",
		"status":    "ok",
		"datastore": connected,
		"warehouse": "hanzo",
	}
	if !connected {
		res["status"] = "degraded"
		res["reason"] = "datastore (datastore) not connected"
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	ctx, cancel := context.WithTimeout(c.Context(), probeTimeout)
	defer cancel()
	res["lenses"] = map[string]any{
		"llm":    map[string]any{"table": llmTable, "available": tableExists(ctx, llmTable)},
		"events": map[string]any{"table": eventsTable, "available": tableExists(ctx, eventsTable)},
	}
	return c.JSON(http.StatusOK, res)
}

// tableExists probes datastore for a table's presence. The name is a package
// constant (never user input), so `EXISTS TABLE` is safe. Any error → false
// (honest "not available") rather than surfacing.
func tableExists(ctx context.Context, qualified string) bool {
	rows, err := datastore.Query(ctx, "EXISTS TABLE "+qualified)
	if err != nil || len(rows) == 0 {
		return false
	}
	for _, v := range rows[0] {
		return aInt64(v) == 1
	}
	return false
}

func firstRow(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return map[string]any{}
	}
	return rows[0]
}

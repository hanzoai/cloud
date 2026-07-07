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
// per-org analytics read API over the `hanzo` ClickHouse warehouse (the
// `datastore` cluster). It is the backend for the console Native Analytics module
// (unified-analytics.md §5) — two read lenses over one warehouse:
//
//   - LLM lens (REAL today): hanzo.cloud_usage, the live per-org usage ledger the
//     cloud o11y path already writes (requests, tokens, spend, models, errors).
//   - Web/commerce lens (honest-empty until the collector emits): hanzo.events.
//
// ONE ClickHouse client. This package does NOT open a second connection: it rides
// the SAME clickhouse-go/v2 client the ai subsystem's o11y ledger opens in the
// shared Bootstrap (ai/object.InitDatastore → object.DatastoreQuery). DRY: one
// transport, one pool, one set of KMS-injected DATASTORE_* creds — never
// hard-coded, never a second design.
//
// TENANT ISOLATION is the security bar and is enforced SERVER-SIDE on every
// request. The org is c.Org() — the value SanitizeIdentity minted from the
// VALIDATED bearer owner claim (HIP-0026), never a client header — AND every
// request must carry a validated principal (c.User() set, which SanitizeIdentity
// sets ONLY for a verified bearer). This closes the Phase-1 "no-bearer + forged
// X-Org-Id direct-to-pod" cross-tenant read exactly as clients/s3 does. Every
// ClickHouse query binds the org POSITIONALLY (query.go llmWhere/eventsWhere), so
// a maxpower token can NEVER read another org's analytics.
//
// Surface (all org-scoped; /v1 only; read-only):
//
//	GET /v1/analytics/overview     per-org KPIs (llm real; web/commerce honest-empty)
//	GET /v1/analytics/timeseries   requests/tokens/spend over time (hour|day buckets)
//	GET /v1/analytics/top          top models (real) + top products (honest-empty)
//	GET /v1/analytics/health       subsystem health (datastore connectivity + lens tables)
//
// Registered as "analyticssvc" (NOT "analytics") + order 132: the name diverges
// from the /v1/analytics route prefix so serve.go's generic GET /v1/<name>/health
// liveness route parks at /v1/analyticssvc/health and our REAL /v1/analytics/health
// (below) owns the probe — the same health-shadow-avoidance the kmssvc/s3svc
// subsystems use. Order 132 binds /v1/analytics/* before the ai subsystem's /v1/*
// catch-all (150).
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
	luxlog "github.com/luxfi/log"
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

type svc struct {
	log luxlog.Logger
}

// Mount wires the analytics surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("analytics.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("analytics.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "analytics")
	s := &svc{log: log}

	// Health owns /v1/analytics/health explicitly (not JWT-gated: liveness must be
	// probe-able). The data endpoints are all org-gated in-handler.
	app.Get("/v1/analytics/health", s.health)
	app.Get("/v1/analytics/overview", s.overview)
	app.Get("/v1/analytics/timeseries", s.timeseries)
	app.Get("/v1/analytics/top", s.top)

	log.Info("analytics mounted", "warehouse", "hanzo", "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("analyticssvc", 132, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("analytics.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// ── shared helpers ──────────────────────────────────────────────────────────

// tenant resolves the org — the tenant-isolation KEY — for a request, and refuses
// the forgeable data path. It REQUIRES a validated principal: c.User() (X-User-Id)
// is set by SanitizeIdentity ONLY when it verified a bearer/cookie; on the Phase-1
// no-principal path it may RESTORE a client's raw X-Org-Id but leaves X-User-Id
// empty. Gating on c.User() therefore refuses an in-cluster caller that forges
// `X-Org-Id: victim` with NO bearer — the same defense clients/s3 uses — while
// breaking no legitimate caller (all reach this via a user-bound bearer).
//
// The org is used EXACTLY as minted (no case-fold/normalize): the cloud_usage
// ledger stored `organization` verbatim from the same owner claim, so an exact
// match is required to see one's own rows (normalizing could collapse or miss).
func tenant(c *zip.Ctx) (string, bool) {
	if strings.TrimSpace(c.User()) == "" {
		return "", false // no validated principal — refuse the forgeable data path
	}
	org := strings.TrimSpace(c.Org())
	if org == "" || len(org) > 128 {
		return "", false
	}
	return org, true
}

// window resolves the [start,end) window + bucket interval from ?range/?start/?end,
// reusing ai/object.ResolveCloudUsageWindow so analytics and the console Overview
// share ONE window grammar (24h|7d|30d|custom). A bad range is a 400.
func window(c *zip.Ctx) (time.Time, time.Time, string, string, error) {
	rangeLabel := strings.TrimSpace(c.Query("range"))
	start, end, interval, err := aiobject.ResolveCloudUsageWindow(rangeLabel, c.Query("start"), c.Query("end"), time.Now())
	if err != nil {
		return time.Time{}, time.Time{}, "", "", zip.ErrBadRequest(err.Error())
	}
	if rangeLabel == "" {
		rangeLabel = "24h"
	}
	return start, end, interval, rangeLabel, nil
}

// requireDatastore returns the honest 503 when the ClickHouse ledger is not
// connected, rather than fabricating zeros. Mirrors ai/object's read gate.
func requireDatastore() error {
	if !aiobject.DatastoreEnabled() {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: datastore (ClickHouse) not connected")
	}
	return nil
}

// warehouseErr maps a ClickHouse query failure to the HONEST HTTP status. A
// connectivity failure — the warehouse became unreachable mid-request (dial /
// i/o timeout / refused / reset / EOF) — is a transient 503 "unavailable", the
// SAME contract requireDatastore() uses when the pool never connected. Only a
// REACHABLE warehouse that rejected the query (bad SQL, protocol error) is a 502
// bad-gateway. This is the fix for /v1/analytics/* surfacing a raw 502 on a
// ClickHouse `:9000` i/o timeout — the caller now gets an honest 503 it can retry.
func warehouseErr(kind string, err error) error {
	if isWarehouseUnreachable(err) {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %s: %v", kind, err)
	}
	return zip.Errorf(http.StatusBadGateway, "analytics %s query: %v", kind, err)
}

// isWarehouseUnreachable reports whether err is a transport/connectivity failure
// to ClickHouse (as opposed to a query the warehouse actively rejected). It checks
// the typed context/net signals first, then the connectivity strings the
// clickhouse-go driver surfaces without a typed net.Error wrapper.
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

func (s *svc) overview(c *zip.Ctx) error {
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
	llmRows, err := aiobject.DatastoreQuery(ctx, llmSQL, args...)
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
	eventsRows, eerr := aiobject.DatastoreQuery(ctx, eventsSQL, eargs...)
	eventsOK := eerr == nil
	if eerr != nil {
		s.log.Debug("events lens unavailable (honest-empty)", "err", eerr)
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

func (s *svc) timeseries(c *zip.Ctx) error {
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
	rows, err := aiobject.DatastoreQuery(ctx, seriesSQL, args...)
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

func (s *svc) top(c *zip.Ctx) error {
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
	modelRows, err := aiobject.DatastoreQuery(ctx, modelSQL, args...)
	if err != nil {
		return warehouseErr("top-models", err)
	}

	// Top products — honest-empty until commerce emits order events.
	ewhere, eargs := eventsWhere(org, start, end)
	prodSQL := fmt.Sprintf("SELECT product_id AS productId, countIf(event = 'order_completed') AS orders, "+
		"toFloat64(sum(revenue)) AS revenue, sum(quantity) AS units FROM %s WHERE %s AND product_id != '' "+
		"GROUP BY product_id ORDER BY revenue DESC LIMIT %d", eventsTable, ewhere, limit)
	prodRows, perr := aiobject.DatastoreQuery(ctx, prodSQL, eargs...)

	return c.JSON(http.StatusOK, Top{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Scope:    Scope{Org: org},
		Models:   buildTopModels(modelRows),
		Products: buildTopProducts(prodRows, perr == nil),
	})
}

// ── /v1/analytics/health ────────────────────────────────────────────────────

// health is a REAL probe: it reports datastore connectivity (the load-bearing
// signal) and, when connected, the availability of each lens table. Not
// JWT-gated (liveness must be probe-able) and it NEVER reads tenant data — only
// table existence. 503 when the warehouse is unreachable so a readiness probe
// can gate; 200 otherwise even if the events lens is not yet provisioned (that is
// honest-empty, not a failure).
func (s *svc) health(c *zip.Ctx) error {
	connected := aiobject.DatastoreEnabled()
	res := map[string]any{
		"service":   "analytics",
		"status":    "ok",
		"datastore": connected,
		"warehouse": "hanzo",
	}
	if !connected {
		res["status"] = "degraded"
		res["reason"] = "datastore (ClickHouse) not connected"
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

// tableExists probes ClickHouse for a table's presence. The name is a package
// constant (never user input), so `EXISTS TABLE` is safe. Any error → false
// (honest "not available") rather than surfacing.
func tableExists(ctx context.Context, qualified string) bool {
	rows, err := aiobject.DatastoreQuery(ctx, "EXISTS TABLE "+qualified)
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

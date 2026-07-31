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

// Package analytics mounts the Hanzo Cloud /v1/analytics/* surface AND the /v1 event
// doors: the read lenses and the ingest that fills them, over the `event` plane.
//
//   - LLM lens: hanzo.cloud_usage, the per-org usage ledger the cloud o11y path
//     writes (requests, tokens, spend, models, errors).
//   - Web/commerce lens: event.event, filled by this package's own ingest doors.
//
// THE PLANE, in four files. An EVENT is the FACT; a MESSAGE is the container it
// travels in, and the two are not conflated anywhere:
//
//	capture.go   the WIRE — decode, scrub, clamp, and the core every door calls.
//	fact.go      the FACT — the envelope, the signal, the route. Pure.
//	bus.go       the CONTAINER — publishing a fact to the EVENT stream.
//	warehouse.go the SINK — the durable consumer that lands facts in their tables.
//
// A signal's SUBJECT and its TABLE are the same name (event.error on the bus is
// event.error in the store), so transport and storage cannot drift apart.
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
//
// THE READ LENSES ARE TYPED OPS (zip.Get with concrete In/Out structs), so REST,
// the OpenAPI document, the MCP tool list and the CLI all derive from the one
// registration; handler prose is lifted into the spec at build time by cmd/zipdoc.
// The INGEST doors are not, and cannot be: each speaks a raw wire (canonical,
// PostHog, team) over bytes, accepting a single event, a bare array or {batch:[…]}
// — a union no In struct states — and each is registered from the doors list rather
// than a constant path literal. The /health probes stay untyped for the same kind of
// reason: their 503 body IS the readiness contract, and a typed op has one success
// status and no vocabulary for a second response.
package analytics

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/types"
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
	b.Log.Info("event plane", "database", plane, "stream", stream, "brand", b.Brand)
	installHostCarve(b)
	startDrain(b.Log)
	return state{}, nil
}

// sinkDrain is the process's warehouse drain — the consumer that lands published facts
// in their tables (warehouse.go). ONE per process, not one per mount: the durables are
// named for the plane, so a second set of consumers on the same durable would only
// split the same stream between two copies of the same code for no gain, and every
// extra copy is another reconnect loop. sync.Once is what makes "one" a property of the
// code rather than of how many times Mount happens to be called.
var (
	sinkDrain *drain
	drainOnce sync.Once
)

// startDrain brings the warehouse consumer up. It is deliberately NOT on the request
// path: a store or a bus that is down delays LANDING, never ACCEPTING, so this never
// fails a mount.
func startDrain(log luxlog.Logger) {
	drainOnce.Do(func() {
		sinkDrain = &drain{log: log.New("plane", plane)}
		sinkDrain.start()
	})
}

// Shutdown stops the warehouse drain and releases the ingest connection. Registered as
// the subsystem's Shutdown hook (plugin/analytics), so a graceful stop drains rather
// than dropping the connection mid-publish.
func Shutdown(_ context.Context) error {
	if sinkDrain != nil {
		sinkDrain.stop()
		sinkDrain = nil
	}
	closeBus()
	return nil
}

// installHostCarve wires the published-site-host beacon ingest (the twin of base's
// sites.SetBaseHostHandler): a page served on a site host can POST its OWN analytics
// beacon to an ingest door and have it ingested into event.event under the site's
// resolved Org — the server-supplied, host-derived tenant, never a body/header claim.
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
// gates the carve on method POST and on the exact path set handed to it here, so the
// authenticated GET read lenses are never hijacked.
//
// That set is doors (event.go) — the SAME list routes registers — so a site host
// carves exactly the doors an API host routes. sites is handed each path already
// bound to its handler, which is why it holds no path literal of its own: the map it
// looks a beacon up in IS the dispatch, so membership and wire are one decision and
// a door added or deleted tomorrow moves both surfaces at once.
func installHostCarve(b cloud.Base) {
	if !publicCaptureEnabled() {
		b.Log.Info("analytics public-host ingest carve disabled", "flag", publicCaptureEnv)
		return
	}
	carve := make(map[string]func(string, *zip.Ctx) error, len(doors))
	for _, d := range doors {
		carve[d.path] = d.anon
	}
	sites.SetAnalyticsHost(carve)
	b.Log.Info("analytics public-host ingest carve enabled", "flag", publicCaptureEnv, "doors", len(carve))
}

// routes registers the analytics surface. Health owns /v1/analytics/health
// explicitly (not JWT-gated: liveness must be probe-able); the data endpoints are
// all org-gated in-handler.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every typed read below
	// resolves its tenant through it. app.Use on a scoped router installs it once
	// per DECLARED prefix, which is exactly the set of read routes; the ingest doors
	// are outside those prefixes and stay ungated here, as they must be.
	app.Use(cloud.Bridge())

	app.Get("/v1/analytics/health", cloud.Handle(s, health))
	zip.Get(z, "/v1/analytics/overview", o.overview)
	zip.Get(z, "/v1/analytics/timeseries", o.timeseries)
	zip.Get(z, "/v1/analytics/top", o.top)

	// Capture (WRITE) side — the ingest that fills event.event. Every ingest door
	// is registered HERE and only here, from doors (event.go): one Post per declared
	// door, no hand-written path beside it. A door contributes its WIRE and nothing
	// else — admission (handle) and the ingest core (ingestEvents) are shared — so
	// this is one pipeline behind N paths, and a path that is not in doors is not an
	// ingest door anywhere: not routed, and not carved on a site host either.
	for _, d := range doors {
		app.Post(d.path, cloud.Handle(s, d.ingest))
	}

	// /v1/errors is the type:'error' read lens over the same rows — a validated
	// principal, since a read never accepts the write-only publishable key. MINTING is
	// a different concern and lives on the key resource, not here: POST /v1/keys with
	// {"type":"publishable"}.
	zip.Get(z, "/v1/errors", o.errors)

	// /v1/insights — console reads over the SAME engine. The PostHog-wire INGEST at
	// /v1/insights/e is a door and is registered in the loop above. Flags live at
	// /v1/flags.
	zip.Get(z, "/v1/insights/health", o.insightsHealth)
	zip.Get(z, "/v1/insights/events", o.insightsEvents)
}

// ops binds the service to analytics' typed read handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// Window is the read window every lens accepts: a named range, or an explicit
// start/end pair.
type Window struct {
	// Range is 24h, 7d or 30d; empty means 24h. Ignored when start and end are given.
	Range string `json:"range"`
	// Start is the RFC3339 window start; honoured only together with end.
	Start string `json:"start"`
	// End is the RFC3339 window end; honoured only together with start.
	End string `json:"end"`
}

// TopWindow is a read window plus the cardinality of each top-N breakdown.
type TopWindow struct {
	// Range is 24h, 7d or 30d; empty means 24h. Ignored when start and end are given.
	Range string `json:"range"`
	// Start is the RFC3339 window start; honoured only together with end.
	Start string `json:"start"`
	// End is the RFC3339 window end; honoured only together with start.
	End string `json:"end"`
	// Limit is how many rows each breakdown returns; 0 means 10, capped at 100.
	Limit int `json:"limit"`
}

// Feed is the bound on a raw-event read.
type Feed struct {
	// Limit is how many events to return, newest first; 0 means 50, capped at 200.
	Limit int `json:"limit"`
}

// tenantFrom resolves the org — the tenant-isolation KEY — for a typed read. It is
// what SanitizeIdentity minted from the validated bearer owner claim, carried across
// the typed seam by cloud.Bridge, so a caller with no validated principal reads
// nothing and the org is never an input field.
func tenantFrom(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid bearer required")
	}
	return org, nil
}

// windowOf resolves the [start,end) window + bucket interval from a lens input,
// reusing hanzoai/types.ParseWindow so analytics and the console Overview share ONE
// window grammar (24h|7d|30d|custom). A bad range is a 400.
func windowOf(rangeIn, startIn, endIn string) (time.Time, time.Time, types.Interval, string, error) {
	w, err := types.ParseWindow(rangeIn, startIn, endIn, time.Now())
	if err != nil {
		return time.Time{}, time.Time{}, "", "", zip.ErrBadRequest(err.Error())
	}
	return w.Start, w.End, w.Interval, w.Label, nil
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

// requireDatastore returns the honest 503 when the datastore ledger is not
// connected, rather than fabricating zeros. Mirrors ai/object's read gate.
func requireDatastore() error {
	if !warehouseReady() {
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

// topLimit bounds a breakdown's cardinality: absent or non-positive means
// defaultTop, and nothing above maxTop is honoured.
func topLimit(n int) int {
	if n <= 0 {
		return defaultTop
	}
	if n > maxTop {
		return maxTop
	}
	return n
}

// feedLimit bounds a raw-event read: absent or non-positive means 50, capped at 200.
func feedLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}

// RawEvent is one row of the raw event feed. Properties and Exception are the
// event's own JSON objects, passed through exactly as stored.
type RawEvent struct {
	// ID is the event id.
	ID string `json:"id"`
	// Timestamp is when the event happened, RFC3339 UTC.
	Timestamp string `json:"timestamp"`
	// Event is the event name (page_viewed, a product event, an error class).
	Event string `json:"event"`
	// Type is the `kind` column — what the caller did (track, page, identify, group).
	Type string `json:"type,omitempty"`
	// Group is the error's grouping fingerprint. Set on the error feed only; it is
	// the key an issue's lifecycle row is kept under.
	Group string `json:"group,omitempty"`
	// DistinctID is the client-side identity the event was captured under.
	DistinctID string `json:"distinctId,omitempty"`
	// SessionID groups events from one browsing session.
	SessionID string `json:"sessionId,omitempty"`
	// Product names the product surface the event came from.
	Product string `json:"product,omitempty"`
	// URL is the full page address the event was captured on.
	URL string `json:"url,omitempty"`
	// Path is the URL path alone.
	Path string `json:"path,omitempty"`
	// Library is the SDK that sent the event.
	Library string `json:"library,omitempty"`
	// LibraryVer is that SDK's version.
	LibraryVer string `json:"libraryVersion,omitempty"`
	// Exception is the error's class, message, level and stack frames, assembled
	// back from event.error's own columns.
	Exception any `json:"exception,omitempty"`
	// Properties is the event's attributes, already scrubbed at capture.
	Properties any `json:"properties,omitempty"`
}

// EventFeed is a page of raw events, newest first.
type EventFeed struct {
	// Data is the page; an empty array when the org has captured nothing in range.
	Data []RawEvent `json:"data"`
}

// InsightsHealth is the liveness of the unified insights surface.
type InsightsHealth struct {
	// OK is true whenever the surface is serving.
	OK bool `json:"ok"`
	// Engine names the analytics engine behind it.
	Engine string `json:"engine"`
	// Surface is the path prefix this health answer covers.
	Surface string `json:"surface"`
}

// errors returns the caller org's most recent captured exceptions, newest first, each
// with its class, message, grouping fingerprint and stack frames. It reads event.error,
// the table the ingest doors land an error signal in — so the frames are real columns
// and "which file throws most" is a GROUP BY rather than a scan of an opaque blob. A
// read always needs a validated principal, never the write-only publishable key.
//
// Example: {"limit": 100}
func (o ops) errors(ctx context.Context, in *Feed) (*EventFeed, error) {
	org, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := datastore.Query(ctx, `
		SELECT id, time, name, kind, distinct_id, session_id, product, url, path,
		       attributes, `+"`group`"+`, message, class, level, handled,
		       `+"`frames.function`"+` AS fn, `+"`frames.file`"+` AS file,
		       `+"`frames.line`"+` AS line, `+"`frames.own`"+` AS own
		FROM `+errorsTable+`
		WHERE org = ?
		ORDER BY time DESC
		LIMIT ?`, org, feedLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	out := make([]RawEvent, 0, len(rows))
	for _, r := range rows {
		e := RawEvent{
			ID: asStr(r["id"]), Timestamp: asStr(r["time"]), Event: asStr(r["name"]),
			Type:       asStr(r["kind"]),
			DistinctID: asStr(r["distinct_id"]), SessionID: asStr(r["session_id"]),
			Product: asStr(r["product"]), URL: asStr(r["url"]), Path: asStr(r["path"]),
			Library: attrOf(r, "library"), LibraryVer: attrOf(r, "library_version"),
			Group:     asStr(r["group"]),
			Exception: exceptionOf(r),
		}
		e.Properties = attributes(r)
		out = append(out, e)
	}
	return &EventFeed{Data: out}, nil
}

// insightsEvents returns the caller org's most recent captured events of every
// kind, newest first — the console's live event feed.
//
// Example: {"limit": 100}
func (o ops) insightsEvents(ctx context.Context, in *Feed) (*EventFeed, error) {
	org, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := datastore.Query(ctx, `
		SELECT id, time, name, kind, distinct_id, session_id,
		       product, url, path, attributes
		FROM `+eventsTable+`
		WHERE org = ?
		ORDER BY time DESC
		LIMIT ?`, org, feedLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	out := make([]RawEvent, 0, len(rows))
	for _, r := range rows {
		e := RawEvent{
			ID: asStr(r["id"]), Timestamp: asStr(r["time"]), Event: asStr(r["name"]),
			Type: asStr(r["kind"]), DistinctID: asStr(r["distinct_id"]),
			SessionID: asStr(r["session_id"]), Product: asStr(r["product"]),
			URL: asStr(r["url"]), Path: asStr(r["path"]),
			Library: attrOf(r, "library"), LibraryVer: attrOf(r, "library_version"),
		}
		e.Properties = attributes(r)
		out = append(out, e)
	}
	return &EventFeed{Data: out}, nil
}

// insightsHealth reports that the unified insights surface is serving. It reads no
// tenant data and touches no warehouse, so it is safe as a liveness probe.
//
// Response: {"ok": true, "engine": "hanzo-analytics", "surface": "/v1/insights"}
func (o ops) insightsHealth(ctx context.Context, _ *struct{}) (*InsightsHealth, error) {
	return &InsightsHealth{OK: true, Engine: "hanzo-analytics", Surface: "/v1/insights"}, nil
}

// ── /v1/analytics/overview ──────────────────────────────────────────────────

// overview reports the caller org's headline analytics for a window: the LLM lens
// (requests, tokens, spend, distinct models and providers, errors) from the usage
// ledger, plus the web and commerce lenses (pageviews, visitors, sessions, orders,
// revenue) from the events warehouse. A lens whose table is not yet provisioned
// reports available:false rather than fabricated zeros; an unreachable warehouse is
// a 503, never invented numbers.
//
// Example: {"range": "7d"}
func (o ops) overview(ctx context.Context, in *Window) (*Overview, error) {
	s := o.s
	org, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	start, end, interval, rangeLabel, err := windowOf(in.Range, in.Start, in.End)
	if err != nil {
		return nil, err
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	// Ensure the ai-owned ledger table exists (idempotent, latched) so a fresh
	// warehouse yields honest zeros, not an error. We NEVER create the event tables —
	// their schema is applied to the store out of band, which is also why nothing in
	// this package carries a CREATE TABLE for them.
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}

	// LLM lens — REAL per-org KPIs.
	where, args := llmWhere(org, start, end)
	llmSQL := "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents, uniqExact(model) AS models, uniqExact(provider) AS providers, " +
		"countIf(status = 'error') AS errors FROM " + llmTable + " WHERE " + where
	llmRows, err := datastore.Query(ctx, llmSQL, args...)
	if err != nil {
		return nil, warehouseErr("llm", err)
	}
	llm := buildLLMOverview(firstRow(llmRows))

	// Web/commerce lens — one events query; degrades to honest-empty if the events
	// table is absent (not yet provisioned) or errors.
	ewhere, eargs := eventsWhere(org, start, end)
	// Page views come from the `kind` discriminator, not a magic name; revenue and
	// quantity are attributes, so they parse out of the Map with toFloat64OrZero —
	// which yields 0 for both an absent key and an unparseable value, exactly as the
	// old Float64 column yielded 0 for an unset one.
	eventsSQL := "SELECT countIf(kind = '" + kindPage + "') AS pageviews, uniqExact(distinct_id) AS visitors, " +
		"uniqExact(session_id) AS sessions, countIf(name = 'order_completed') AS orders, " +
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue FROM " + eventsTable + " WHERE " + ewhere
	eventsRows, eerr := datastore.Query(ctx, eventsSQL, eargs...)
	eventsOK := eerr == nil
	if eerr != nil {
		s.Log.Debug("events lens unavailable (honest-empty)", "err", eerr)
	}
	erow := firstRow(eventsRows)

	return &Overview{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Interval: interval,
		Scope:    Scope{Org: org},
		LLM:      llm,
		Web:      buildWebOverview(erow, eventsOK),
		Commerce: buildCommerceOverview(erow, eventsOK),
	}, nil
}

// ── /v1/analytics/timeseries ────────────────────────────────────────────────

// timeseries reports the caller org's LLM usage bucketed over a window — requests,
// tokens and spend per hour or per day, with empty buckets filled in so the series
// is continuous. The bucket size follows the window; an unreachable warehouse is a
// 503, never fabricated points.
//
// Example: {"range": "30d"}
func (o ops) timeseries(ctx context.Context, in *Window) (*Timeseries, error) {
	org, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	start, end, interval, rangeLabel, err := windowOf(in.Range, in.Start, in.End)
	if err != nil {
		return nil, err
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}

	// bucketFn is a CLOSED server-chosen enum, so interpolating it is injection-safe;
	// the org + time bounds stay bound parameters. types.Interval admits only Hour or
	// Day, so the closure is a property of the type rather than of this switch.
	bucketFn := "Hour"
	if interval == types.Day {
		bucketFn = "Day"
	}
	where, args := llmWhere(org, start, end)
	seriesSQL := fmt.Sprintf("SELECT toStartOf%s(timestamp, 'UTC') AS bucket, count() AS requests, "+
		"sum(total_tokens) AS tokens, sum(cost_cents) AS cost_cents FROM %s WHERE %s GROUP BY bucket ORDER BY bucket",
		bucketFn, llmTable, where)
	rows, err := datastore.Query(ctx, seriesSQL, args...)
	if err != nil {
		return nil, warehouseErr("timeseries", err)
	}

	return &Timeseries{
		Range:    rangeLabel,
		Start:    start.UTC().Format(time.RFC3339),
		End:      end.UTC().Format(time.RFC3339),
		Interval: interval,
		Scope:    Scope{Org: org},
		Series:   buildSeries(start, end, interval, rows),
		Source:   llmTable,
	}, nil
}

// ── /v1/analytics/top ───────────────────────────────────────────────────────

// top reports the caller org's leaderboards for a window: the models it spent the
// most on (from the usage ledger), and — from the events warehouse — its best
// selling products, most visited pages, and the referrers and campaign sources that
// brought people in. Each events-backed lens reports available:false when its table
// is not provisioned rather than an empty answer that looks like real zero traffic.
//
// Example: {"range": "7d", "limit": 20}
func (o ops) top(ctx context.Context, in *TopWindow) (*Top, error) {
	s := o.s
	org, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	start, end, _, rangeLabel, err := windowOf(in.Range, in.Start, in.End)
	if err != nil {
		return nil, err
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	limit := topLimit(in.Limit)

	// Top models — REAL. limit is a validated int (never user text) so %d is safe;
	// org + time stay bound parameters.
	where, args := llmWhere(org, start, end)
	modelSQL := fmt.Sprintf("SELECT model, any(provider) AS provider, count() AS requests, "+
		"sum(total_tokens) AS tokens, sum(cost_cents) AS cost_cents FROM %s WHERE %s "+
		"GROUP BY model ORDER BY cost_cents DESC, requests DESC LIMIT %d", llmTable, where, limit)
	modelRows, err := datastore.Query(ctx, modelSQL, args...)
	if err != nil {
		return nil, warehouseErr("top-models", err)
	}

	// Top products — honest-empty until commerce emits order events.
	ewhere, eargs := eventsWhere(org, start, end)
	prodSQL := fmt.Sprintf("SELECT attributes['product_id'] AS productId, countIf(name = 'order_completed') AS orders, "+
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue, "+
		"sum(toUInt64OrZero(attributes['quantity'])) AS units FROM %s WHERE %s AND attributes['product_id'] != '' "+
		"GROUP BY productId ORDER BY revenue DESC LIMIT %d", eventsTable, ewhere, limit)
	prodRows, perr := datastore.Query(ctx, prodSQL, eargs...)

	// Behavior lenses over event.event — WHERE people go / WHAT they look at
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

	return &Top{
		Range:     rangeLabel,
		Start:     start.UTC().Format(time.RFC3339),
		End:       end.UTC().Format(time.RFC3339),
		Scope:     Scope{Org: org},
		Models:    buildTopModels(modelRows),
		Products:  buildTopProducts(prodRows, perr == nil),
		Pages:     buildBreakdown(pageRows, pageErr == nil),
		Referrers: buildBreakdown(refRows, refErr == nil),
		Sources:   buildBreakdown(srcRows, srcErr == nil),
	}, nil
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

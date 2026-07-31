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

// Package analytics is product analytics: send an event, read back who did what.
//
// It is the product-event plane: it owns the ingest door every Hanzo client
// posts to, lands each event in the `hanzo` warehouse, and serves the per-org
// read lenses — KPIs, time series, rankings, captured errors — over what it
// wrote.
//
// It is BOTH halves, and that is deliberate: one write core (ingestEvents) behind
// N doors (event.go), and the read lenses over the same warehouse, so a fact is
// admitted, stamped with the SERVER-resolved tenant, and read back through one
// vocabulary. Two lenses share one warehouse:
//
//   - LLM lens (REAL today): hanzo.cloud_usage, the live per-org usage ledger the
//     cloud o11y path already writes (requests, tokens, spend, models, errors).
//   - Web/commerce lens: event.event on the o11y-owned event plane — what this
//     package's own doors ingest (as facts, landed by the sink in warehouse.go).
//
// The accepted batch is also handed to registered SINKS (forward.go) — apps/
// destinations forwards it to the org's connected ad platforms. analytics never
// imports a consumer; the seam is one-way and fail-soft.
//
// ONE datastore client. This package does NOT open its own connection: it reads
// through apps/datastore, the leaf that holds the warehouse connection for the
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
// X-Org-Id direct-to-pod" cross-tenant read exactly as apps/s3 does. Every
// datastore query binds the org POSITIONALLY (query.go llmWhere/eventsWhere), so
// a maxpower token can NEVER read another org's analytics.
//
// Surface (all org-scoped; /v1 only):
//
//	READ
//	GET  /v1/analytics/overview     per-org KPIs (llm real; web/commerce honest-empty)
//	GET  /v1/analytics/timeseries   requests/tokens/spend over time (hour|day buckets)
//	GET  /v1/analytics/top          top models (real) + products + behavior lenses
//	                                (topPages/topReferrers/topSources over the events lens)
//	GET  /v1/errors                 the caller org's most recently captured errors
//	GET  /v1/insights/events        the caller org's most recent product events
//	GET  /v1/insights/health        the insights surface is serving
//	GET  /v1/analytics/health       subsystem health (datastore connectivity + lens tables)
//
//	WRITE (the ingest doors — see doors, event.go)
//	POST /v1/event                  the canonical wire (object | array | {batch:[…]})
//	POST /v1/insights/e             the PostHog wire — a second WIRE, not a second name
//	POST /v1/event/:project/envelope|store   the Sentry error wire, same door
//
// The six reads above /v1/analytics/health are TYPED ops, so each publishes its
// prose, its In/Out schema, an MCP tool and a CLI command. /v1/analytics/health and
// the ingest doors are untyped and cannot be typed without moving their wire;
// routes (below) names each one's blocker where it is registered.
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
	"strings"
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
	// defaultRecent / maxRecent bound the two recent-event reads (/v1/errors and
	// /v1/insights/events). ONE pair, because both read the same table the same way
	// and a second pair is a second place for them to disagree.
	defaultRecent = 50
	maxRecent     = 200
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
// records the informative mount line, installs the site-host ingest carve, and brings
// up the event sink.
func build(b cloud.Base) (state, error) {
	b.Log.Info("analytics surface", "warehouse", "hanzo", "brand", b.Brand)
	installHostCarve(b)
	startSink(b.Log)
	return state{}, nil
}

// sink is the process's ONE warehouse drain (warehouse.go), held at package scope for
// the same reason webhooks holds its dispatcher there: Shutdown has to be able to stop
// what Mount started, and Mount's return value is a router and not a handle.
var sink *drain

// startSink brings the drain up, replacing any previous one. It is what makes a
// published fact LAND: without it the ingest path publishes into a stream nothing
// consumes, every event.* table stays empty, and the plane's tables are queryable in
// name only.
//
// Starting is unconditional and cannot fail a mount — the drain retries its own
// connection forever (drain.run) — so a bus that is down at boot delays landing and
// never delays serving. Replacing rather than stacking makes a second Mount in one
// process (the tests do exactly this) idempotent instead of leaving an orphan consumer
// behind.
func startSink(log luxlog.Logger) {
	stopSink()
	sink = &drain{log: log}
	sink.start()
}

// stopSink tears the drain down and forgets it. Idempotent, and the ONE way the sink
// stops — a test that must be the only writer through the warehouse seam calls it for
// the same reason Shutdown does, because a live consumer is a second writer through a
// process-global var.
func stopSink() {
	if sink != nil {
		sink.stop()
		sink = nil
	}
}

// Shutdown stops the sink and releases the ingest connection to the bus. Idempotent.
//
// The order is deliberate: stop CONSUMING before closing the PUBLISHING side, so an
// in-flight insert is never abandoned mid-commit by a connection that went away
// underneath it.
func Shutdown(context.Context) error {
	stopSink()
	closeBus()
	return nil
}

// installHostCarve wires the published-site-host beacon ingest (the twin of base's
// sites.SetBaseHostHandler): a page served on a site host can POST its OWN analytics
// beacon to an ingest door and have it ingested onto the event plane under the site's
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

// zipdoc lifts the doc comment off each typed op and off each field of its In and
// Out into zipdoc_gen.go, which is the ONLY way that prose reaches the published
// document and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/analytics openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the analytics surface: six TYPED read ops, and the writes plus
// the health probe as untyped handlers because their wire cannot be declared.
//
// Health owns /v1/analytics/health explicitly (not JWT-gated: liveness must be
// probe-able); every read lens is org-gated on the validated principal.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// A typed op receives ONLY a context, so the validated org has to be PARKED
	// there — never carried as an In field, which is caller-supplied and would make
	// a cross-tenant read something the caller asserts for itself. cloud.Bridge
	// parks it, and it is installed FIRST because fiber runs middleware in
	// registration order: one installed below a leaf never runs for that leaf.
	//
	// On a scoped mount Use installs it once per prefix the subsystem DECLARES
	// (scope.go), which is why plugin/analytics/main.go now declares all six of
	// this app's prefixes: with only the /v1/<name> default, the typed reads at
	// /v1/errors and /v1/insights/* would sit outside every prefix this subsystem
	// could gate. Serve installs one app-wide too; nesting is harmless, and the
	// tests mount this subsystem on a bare app with no Serve, so the subsystem's
	// own install is what makes them pass.
	app.Use(cloud.Bridge())

	o := readOps{s: s}
	// The read lenses, declared on the GROUP: each op's path is the prefix composed
	// with its leaf — the same composition the router does, and the identity every
	// projection (document, MCP tool, CLI command, call plane) keys on. cmd/zipdoc
	// resolves the prefix the same way, so the prose below reaches both surfaces.
	g := app.Group("/v1/analytics")
	zip.Get(g, "/overview", o.overview)
	zip.Get(g, "/timeseries", o.timeseries)
	zip.Get(g, "/top", o.top)

	// UNTYPED, and it has to be: this probe answers 503 CARRYING the degraded
	// REPORT as its body, and no typed op can say that. zip stamps a non-nil Out
	// with cmp.Or(op.Status, 200) (zip typed.go), WithStatus refuses a non-2xx, and
	// an error is rendered as the flat {status,code,error} HTTPError — so typing it
	// would either turn the 503 into a 200 or drop the report. Writing the body
	// from inside the op and returning nil does not escape it either: a nil Out is
	// stamped cmp.Or(op.Status, 204).
	app.Get("/v1/analytics/health", cloud.Handle(s, health))

	// Capture (WRITE) side — the ingest that fills the event plane. Every ingest door
	// is registered HERE and only here, from doors (event.go): one Post per declared
	// door, no hand-written path beside it. A door contributes its WIRE and nothing
	// else — admission (handle) and the write core (ingestEvents) are shared — so
	// this is one pipeline behind N paths, and a path that is not in doors is not an
	// ingest door anywhere: not routed, and not carved on a site host either.
	//
	// EVERY door is untyped, and none of them is a candidate. A typed op receives a
	// context and a DECODED In, and admission (handle, event.go) is decided from
	// facts that live only on the request: the presented credential (Authorization /
	// x-hanzo-ingest-key / ?ingest_key=, publishable.go ingestKey), the client IP
	// and the socket peer the anonymous rate caps key on, the DNT/Sec-GPC headers,
	// and the RAW body — whose LENGTH is the anonymous lane's 64 KiB → 413 bound
	// (public.go maxPublicBytes) and whose first non-space BYTE selects the wire.
	// The canonical door also accepts a bare JSON ARRAY body, which cannot decode
	// into any struct In: zip's op.invoke answers 400 on a body it cannot unmarshal,
	// and it answers 200 with a receipt. See LLM.md; each door names its own blocker.
	for _, d := range doors {
		app.Post(d.path, cloud.Handle(s, d.ingest))
	}

	// The Sentry error wire, on the SAME door: POST /v1/event/{project}/envelope|store.
	// The project segment is variable, so the door's owner carries the route and
	// forwards to the obs plane's installed consumer (cloud.ObsErrorIngest — the
	// o11y runtime, which authenticates the DSN key itself; no principal here by
	// design). Resolved per-request: the o11y subsystem installs it during its own
	// mount, order-independent of this one.
	obsError := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := cloud.ObsErrorIngest()
		if h == nil {
			http.Error(w, "error ingest not initialized", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	app.Post("/v1/event/:project/envelope", obsError)
	app.Post("/v1/event/:project/store", obsError)

	// /v1/errors is the type:'error' read lens over the same rows — a validated
	// principal, since a read never accepts the write-only publishable key. MINTING is
	// a different concern and lives on the key resource, not here: POST /v1/keys with
	// {"type":"publishable"}.
	//
	// Declared on the APP with its WHOLE path, not on a group with an empty leaf:
	// joining a "/v1/errors" prefix with "" yields "/v1/errors/", a path this API
	// has never served, and op.Path is the identity every projection reads.
	zip.Get(cloud.ZipApp(app), "/v1/errors", o.errors)

	// /v1/insights — console reads over the SAME engine. The PostHog-wire INGEST at
	// /v1/insights/e is a door and is registered in the loop above. Flags live at
	// /v1/flags.
	ig := app.Group("/v1/insights")
	zip.Get(ig, "/health", o.insightsHealth)
	zip.Get(ig, "/events", o.insightsEvents)
}

// readOps binds the service to the typed read ops of the WHOLE package — the three
// warehouse lenses here, the error lens (publishable.go) and the two insights reads
// (insights.go). A TypedHandler is func(context.Context, *In) (*Out, error) with no
// parameter for the service, so it arrives as a RECEIVER and every op is a method
// value, which is also the only bound form cmd/zipdoc can lift prose from.
//
// ONE receiver for the package, because there is one service: a second would be a
// second place for the same binding to be got wrong.
type readOps struct{ s *cloud.Service[state] }

// tenantOf is tenant() for a typed op: the VALIDATED org cloud.Bridge parked on the
// context, never a field of In. An In field is caller-supplied, so a tenant key read
// from one is a cross-tenant read the caller asserted for itself. The 403 it returns
// is byte-identical to the one tenant() produces on the untyped path — both are
// zip.ErrForbidden through the same error handler.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid bearer required")
	}
	return org, nil
}

// noArgs is the In of an op that takes nothing off the wire at all. ONE of these for
// the package: an op with no input has no input to describe twice.
type noArgs struct{}

// windowQuery is the [start,end) window the warehouse lenses read from the URL. It
// is the SAME grammar hanzoai/types.ParseWindow gives the console, so analytics and
// the Overview module cannot disagree about what "7d" means.
type windowQuery struct {
	// Range is a relative window: 24h, 7d or 30d. Default 24h. Ignored when both
	// start and end are given. An unknown value is a 400.
	Range string `json:"range"`
	// Start is the inclusive lower bound of a custom window, RFC3339. Requires end.
	Start string `json:"start"`
	// End is the exclusive upper bound of a custom window, RFC3339. Requires start.
	End string `json:"end"`
}

// topQuery is windowQuery plus the result cardinality the ranked lenses take. The
// window fields are spelled out rather than embedded: zip's schema walk publishes
// an embedded type as a nested object property the flat wire does not carry.
type topQuery struct {
	// Range is a relative window: 24h, 7d or 30d. Default 24h. Ignored when both
	// start and end are given. An unknown value is a 400.
	Range string `json:"range"`
	// Start is the inclusive lower bound of a custom window, RFC3339. Requires end.
	Start string `json:"start"`
	// End is the exclusive upper bound of a custom window, RFC3339. Requires start.
	End string `json:"end"`
	// Limit bounds every ranked lens in the response. Default 10, maximum 100; a
	// value at or below zero, or one that is not a number, takes the default.
	Limit int `json:"limit"`
}

// window resolves the window from the URL-bound query, reusing the SAME
// types.ParseWindow the untyped path uses so the two cannot drift. A bad range is
// the same 400 it has always been.
func (q windowQuery) window() (time.Time, time.Time, types.Interval, string, error) {
	w, err := types.ParseWindow(q.Range, q.Start, q.End, time.Now())
	if err != nil {
		return time.Time{}, time.Time{}, "", "", zip.ErrBadRequest(err.Error())
	}
	return w.Start, w.End, w.Interval, w.Label, nil
}

// window is topQuery's identical resolution — one grammar, read through the one
// parser, whichever In carried the three fields.
func (q topQuery) window() (time.Time, time.Time, types.Interval, string, error) {
	return windowQuery{Range: q.Range, Start: q.Start, End: q.End}.window()
}

// limit clamps the requested cardinality exactly as the untyped topLimit did: a
// value at or below zero (which is also what an unparseable one binds to) takes the
// default, and anything above maxTop is capped there.
func (q topQuery) limit() int {
	if q.Limit <= 0 {
		return defaultTop
	}
	if q.Limit > maxTop {
		return maxTop
	}
	return q.Limit
}

// limitQuery is the row cap the two recent-event reads take from the URL. ONE type,
// because /v1/errors and /v1/insights/events bound the same thing the same way.
type limitQuery struct {
	// Limit is how many rows to return, newest first. Default 50, maximum 200; a
	// value at or below zero, or one that is not a number, takes the default.
	Limit int `json:"limit"`
}

// rows clamps the requested row count to [1, maxRecent], defaulting an absent,
// zero, negative or unparseable value — the same clamp both untyped readers held.
func (q limitQuery) rows() int {
	if q.Limit <= 0 {
		return defaultRecent
	}
	if q.Limit > maxRecent {
		return maxRecent
	}
	return q.Limit
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

// ── /v1/analytics/overview ──────────────────────────────────────────────────

// Overview returns the caller org's analytics KPIs for one time window. Three lenses
// over one warehouse: llm is the live per-org LLM usage ledger (requests, tokens,
// spend, models, providers, errors) and is always real; web (pageviews, visitors,
// sessions) and commerce (orders, revenue, AOV) read the product-event table and
// report available=false rather than fabricating zeros when it holds nothing yet.
//
// The org is the validated principal's — never a parameter — so a caller can only
// ever read its own tenant. 403 without a validated bearer, 400 on an unknown range,
// 503 when the warehouse is unreachable.
//
// Example: {"range": "7d"}
func (o readOps) overview(ctx context.Context, in *windowQuery) (*Overview, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	start, end, interval, rangeLabel, err := in.window()
	if err != nil {
		return nil, err
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	s := o.s
	// Ensure the ai-owned ledger table exists (idempotent, latched) so a fresh
	// warehouse yields honest zeros, not an error. We NEVER create event.event —
	// the plane's DDL owner is hanzoai/o11y (exactly the stance this lens has
	// always taken for tables it does not own).
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

	// Web/commerce lens — one events query over the plane; degrades to honest-empty
	// if event.event is absent (not yet provisioned) or errors. A pageview is
	// kind='page' (the plane's discriminator, not a magic name) and revenue is the
	// numeric read-back of the attributes entry the writer stamped (fact.go
	// attributesOf), so the sum is the same fact forward-era rows carried in a
	// dedicated column.
	ewhere, eargs := eventsWhere(org, start, end)
	eventsSQL := "SELECT countIf(kind = 'page') AS pageviews, uniqExact(distinct_id) AS visitors, " +
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

// Timeseries returns the caller org's LLM usage over time as an evenly-spaced series.
// One point per hour or per day — the bucket the window implies, 24h giving hours and
// 7d/30d giving days — carrying requests, total tokens and spend in cents. Empty
// buckets are filled with zeros so a client charts a continuous line.
//
// The org is the validated principal's — never a parameter. 403 without a validated
// bearer, 400 on an unknown range, 503 when the warehouse is unreachable.
//
// Example: {"range": "30d"}
func (o readOps) timeseries(ctx context.Context, in *windowQuery) (*Timeseries, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	start, end, interval, rangeLabel, err := in.window()
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

// Top returns the caller org's ranked lenses for one window, five of them at once.
// models ranks LLM models by spend and is always real; products ranks commerce orders
// by revenue; topPages ranks requested paths, topReferrers the external referrer
// domains ("(direct)" for a missing or same-origin one) and topSources the utm_source
// campaigns ("(none)" when absent), each by pageviews. Every lens carries each row's
// share of the in-window total, so a top-N honestly shows the long tail.
//
// The four event lenses report available=false rather than fabricating zeros when the
// product-event table holds nothing yet. The org is the validated principal's — never
// a parameter. 403 without a validated bearer, 400 on an unknown range, 503 when the
// warehouse is unreachable.
//
// Example: {"range": "7d", "limit": 25}
func (o readOps) top(ctx context.Context, in *topQuery) (*Top, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	start, end, _, rangeLabel, err := in.window()
	if err != nil {
		return nil, err
	}
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	s := o.s
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	limit := in.limit()

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

	// Top products — honest-empty until commerce emits order events. product_id,
	// revenue and quantity live in the envelope's attributes map (fact.go
	// attributesOf); the numeric reads parse back exactly what the writer stamped.
	ewhere, eargs := eventsWhere(org, start, end)
	prodSQL := fmt.Sprintf("SELECT attributes['product_id'] AS productId, countIf(name = 'order_completed') AS orders, "+
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue, sum(toUInt64OrZero(attributes['quantity'])) AS units "+
		"FROM %s WHERE %s AND attributes['product_id'] != '' "+
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

// healthReport is the probe's answer, and it is the SAME object at 200 and at 503 —
// which is precisely why this route cannot be a typed op: the STATUS is the signal
// and the report is the detail, and zip can declare only one of the two. Stating it
// as a struct rather than building a map is what lets openapi.Register (event.go)
// derive the shape from the code that produces it, instead of a hand-written schema
// beside it that drifts.
type healthReport struct {
	// Service names the subsystem answering, so a probe aggregating several health
	// endpoints can attribute a degraded one.
	Service string `json:"service"`
	// Status is ok or degraded. Degraded is the 503 and means EITHER load-bearing
	// dependency is down — the warehouse this subsystem reads, or the event plane it
	// writes. It is not moved by a missing lens table, which is honest-empty.
	Status string `json:"status"`
	// Datastore reports whether the shared warehouse client has a live connection.
	// It is load-bearing for the READ path: false is one of the two ways this
	// answers 503.
	Datastore bool `json:"datastore"`
	// Warehouse names the datastore database every lens reads.
	Warehouse string `json:"warehouse"`
	// Plane reports the event plane — the bus and the stream every accepted event is
	// published to BEFORE any of it reaches the warehouse. It is load-bearing for the
	// WRITE path, and it is here because its absence was a real outage: this endpoint
	// answered 200/ok on warehouse connectivity alone while every POST /v1/event 503'd
	// on a stream that could not bind, so 100% ingest loss was invisible to monitoring.
	// A probe that cannot see the write path cannot report the write path.
	Plane healthPlane `json:"plane"`
	// Reason is the human-readable cause, present only on a degraded report.
	Reason string `json:"reason,omitempty"`
	// Lenses is per-lens table availability, probed only when connected — so it is
	// absent from a degraded report, which has nothing to say about tables it could
	// not reach.
	Lenses *healthLenses `json:"lenses,omitempty"`
	// Lost is the count of facts the sink irrecoverably dropped since boot
	// (warehouse.go). It is reported on the DEGRADED report too, and deliberately: a
	// warehouse that is unreachable is exactly when facts start failing their
	// deliveries, so suppressing the number here would hide it precisely when it
	// moves. ANY NON-ZERO VALUE IS AN ALARM — it counts data the door already
	// answered 200 for.
	Lost loss `json:"lost"`
}

// healthLenses is the two read lenses this subsystem serves, named rather than
// keyed: the pair is closed (llm and events are the whole surface), so a struct
// says more than a map and says it in the document too.
type healthLenses struct {
	// LLM is the live per-org usage ledger lens (hanzo.cloud_usage).
	LLM healthLens `json:"llm"`
	// Events is the web/commerce lens (event.event), honest-empty until the
	// collector emits.
	Events healthLens `json:"events"`
}

// healthPlane is the write path's availability, said in the plane's own vocabulary:
// the bus it dials and the stream it publishes to (bus.go owns both names, so this
// reports them rather than restating them). Ready is the load-bearing bit; Reason
// carries the plane's own error text when it is false, so the probe names the actual
// break — an unbindable stream, an unreachable bus — instead of a bare degraded.
type healthPlane struct {
	// Bus is the address this process reaches the plane at.
	Bus string `json:"bus"`
	// Stream is the JetStream stream every signal lands on.
	Stream string `json:"stream"`
	// Ready reports whether an ingest would succeed right now. False is a 503.
	Ready bool `json:"ready"`
	// Reason is the plane's own failure text, present only when Ready is false.
	Reason string `json:"reason,omitempty"`
}

// healthLens is one lens's provisioning state: the table it reads and whether that
// table exists yet. An unavailable lens is not a failure — the read endpoints answer
// honest-empty — so it never moves the status.
type healthLens struct {
	// Table is the fully-qualified warehouse table the lens reads.
	Table string `json:"table"`
	// Available reports whether that table exists in the warehouse right now.
	Available bool `json:"available"`
}

// health is a REAL probe of BOTH directions: the warehouse this subsystem reads and
// the event plane it writes. Either one down is a 503, because either one down is a
// subsystem that cannot do its job — and reporting only the read half is what let a
// total ingest outage sit behind a green probe.
//
// The two are probed INDEPENDENTLY and reported side by side, so the answer says
// WHICH half broke rather than collapsing both into one bit. Not JWT-gated (liveness
// must be probe-able) and it NEVER reads tenant data — only table existence and
// stream presence. 200 even if the events lens is not yet provisioned (that is
// honest-empty, not a failure).
func health(s *cloud.Service[state], c *zip.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), probeTimeout)
	defer cancel()

	connected := datastore.Ready()
	res := healthReport{Service: "analytics", Status: "ok", Datastore: connected, Warehouse: "hanzo",
		Plane: healthPlane{Bus: busURL(), Stream: EventStream, Ready: true},
		Lost:  lossReport()}
	if err := planeReady(ctx); err != nil {
		res.Plane.Ready, res.Plane.Reason = false, err.Error()
	}
	// Lenses are reported whenever the warehouse is reachable — including on a report
	// degraded by the PLANE, where the tables genuinely were probed and have something
	// true to say. Only an unreachable warehouse leaves them out.
	if connected {
		res.Lenses = &healthLenses{
			LLM:    healthLens{Table: llmTable, Available: tableExists(ctx, llmTable)},
			Events: healthLens{Table: eventsTable, Available: tableExists(ctx, eventsTable)},
		}
	}
	switch {
	case !connected:
		res.Status, res.Reason = "degraded", "datastore (datastore) not connected"
	case !res.Plane.Ready:
		res.Status, res.Reason = "degraded", res.Plane.Reason
	default:
		return c.JSON(http.StatusOK, res)
	}
	return c.JSON(http.StatusServiceUnavailable, res)
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

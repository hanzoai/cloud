// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

// typed_wire_test.go — the projection gate. The reads and the health probe are
// TYPED ops; the ingest endpoints are not, and each of those has a WIRE FACT that
// keeps it out. Both halves are MEASURED here rather than asserted in prose,
// because prose cannot go red: a route added untyped goes red without anyone
// remembering to name it, a reason naming a route this package no longer serves
// goes red too, and every refusal's bytes are re-proved against the live router.
package event

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of analytics operations that are NOT typed ops,
// each with the wire fact that keeps it out. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI operation, the MCP tool, the CLI command and the
// generated SDK method all come from — so an operation missing from that registry is
// invisible to all four. These are missing on purpose: every one would answer a
// DIFFERENT wire as a typed op, and typing is a description task. Addresses are
// written the way the DOCUMENT writes them, which is the identity every projection
// keys on.
var untypedByDesign = map[string]string{
	"GET /v1/event/tag.js": "the tag is an ASSET, not an operation: its body is JavaScript and zip renders " +
		"a typed Out as JSON, so there is no Out that can carry it. It also answers 304 with an EMPTY " +
		"body on a matching If-None-Match, which a typed op cannot express either — not because 304 is " +
		"non-2xx (WithStatus is variadic now and accepts any status; the clause that said otherwise was " +
		"read against an older zip) but because 304 is DEFINED to carry no body while a typed op always " +
		"writes one. And the caller is a <script src>, which reads no schema, no MCP tool and no SDK " +
		"method, so typing it would buy nothing even if the body could be carried.",

	"POST /v1/event": canonWireReason,
	"POST /v1/event/{project}/envelope": "the Sentry error wire on the one event endpoint: the body is a " +
		"raw Sentry envelope stream and the credential is a DSN key the o11y consumer verifies itself " +
		"(cloud.ObsErrorIngest) — no principal, no struct In, nothing for a typed op to say.",
	"POST /v1/event/{project}/store": "the Sentry error wire on the one event endpoint: the body is a " +
		"raw Sentry envelope stream and the credential is a DSN key the o11y consumer verifies itself " +
		"(cloud.ObsErrorIngest) — no principal, no struct In, nothing for a typed op to say.",

	"POST /v1/event/replay": "the session-replay snapshot endpoint has a declarable BODY — unlike every other " +
		"entry here — and is still not typable, because ADMISSION is what keeps it out. " +
		admissionReason + " That is not academic on this endpoint: it takes a publishable pk- on " +
		"?ingest_key= (a recorder drains its buffer through navigator.sendBeacon on unload, which " +
		"cannot set headers), and a typed op never sees the query string the credential arrived on. " +
		"Its 413 has the same ORDER problem as the anonymous lane's: the RAW body length is refused " +
		"before any decode, and zip's op.invoke decodes first, so a typed op would answer 400 to an " +
		"oversized recording that is answered 413 today — and 413 is the one status that tells a " +
		"recorder to chunk.",
}

const (
	canonWireReason = "the canonical wire is POLYMORPHIC: decodeIngest (event.go) accepts a bare Event " +
		"OBJECT, a bare Event ARRAY, and the {batch:[…]}/{events:[…]} envelope, all on one path. A " +
		"typed In is a struct, and zip's op.invoke jsonenc.Unmarshals every non-empty body into it, so " +
		"an array body — which every @hanzo/event batch and sendBeacon drain sends — would turn today's " +
		"200 receipt into a 400. " + admissionReason

	admissionReason = "Admission (handle, event.go) is also decided from facts that live ONLY on the " +
		"request and never reach a typed op: the presented credential (Authorization / " +
		"x-hanzo-ingest-key / ?ingest_key=, publishable.go), the client IP and socket peer the anonymous " +
		"rate caps key on, the DNT/Sec-GPC headers, and the RAW body length that is the anonymous lane's " +
		"64 KiB -> 413 bound (public.go maxPublicBytes) — invisible to a typed op, and far below the " +
		"fleet's global zip BodyLimit. Reading a tenant off an In field is not the alternative: an In " +
		"field is caller-supplied, so that is a cross-tenant write the caller asserted for itself. " +
		precedenceReason

	// precedenceReason is the blocker that survives even if an endpoint's body were
	// declarable and the request were reachable, so it is stated separately: ORDER.
	// zip decodes the body BEFORE the handler runs (zip v1.18.11 typed.go, op.invoke:
	// `if len(rawIn) > 0 { dec(rawIn, &in) }` → ErrBadRequest), while every one of
	// this lane's refusals is decided AFTER the body is in hand and BEFORE it is
	// parsed — publicCaptureEnabled 403, then the rate-limit 429, then the 64 KiB 413
	// (public.go, in that order), and only then the decode. A typed op inverts that:
	// an oversized or unparseable anonymous beacon would answer 400 where it answers
	// 413 or 429 today. Error precedence is wire, and typing is a description task.
	precedenceReason = "And ORDER is itself a blocker: zip's op.invoke decodes the body before the " +
		"handler is entered, while this lane refuses 403 (capture disabled), then 429 (rate), then 413 " +
		"(64 KiB) BEFORE any decode — so a typed op would answer 400 to a beacon that is answered 413 " +
		"or 429 today."
)

// analyticsOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. Reading the router — the REAL Mount, not a reconstruction of it — is what
// makes this a gate rather than prose.
func analyticsOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "event", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an analytics operation is neither a typed
// op nor one named above — so the next route added here is typed by default, and
// dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := analyticsOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which analytics no longer serves", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema, because
// that prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries it
// into the binary, so an op added without regenerating shows up here as a nameless
// tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := analyticsOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed analytics ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/analytics/...", key)
		}
	}
}

// proseless is the CLOSED list of published components whose properties carry NO
// description, and it is a property of the CLIENT they came through, not of anyone's
// diligence. These are the bodies of the untyped ingest endpoints and the health probe,
// declared through openapi.Register (event.go) because those routes cannot be typed
// ops. Register derives a schema by REFLECTION from the Go type, and Go drops
// comments at compile time — zipdoc, the pass that lifts field prose, walks zip's
// TYPED registrations and can therefore never reach a type that arrives this way.
// So the choice at each of these routes was a bare shape or NO shape, and a bare
// shape is what an SDK needs to offer an ingest call with somewhere to put the
// event at all.
//
// The list is exact in both directions. A bare property in any OTHER component is a
// typed op's, which zipdoc CAN describe, and goes red. A component here that starts
// publishing prose also goes red — that is the day cloud learns to lift comments for
// Register (the fleet ships 1,627 bare properties for exactly this reason), and this
// ledger must shrink when it comes rather than quietly outlive the limitation.
var proseless = map[string]bool{
	// The canonical ingest wire, in its three spellings: one event, an array of
	// them, or {batch:[…]} — all the same shape.
	"CaptureBatch": true, "CaptureEvent": true, "UTM": true, "Exception": true,
	// The signal BODIES and the structured stack, nested inside those same ingest
	// shapes and reaching the document through the same Register client. Their fields
	// carry doc comments in Go — reflection simply cannot see them, which is the one
	// reason they are listed here rather than described.
	"LogBody": true, "SpanBody": true, "MetricBody": true, "ClipBody": true, "Frame": true,
	// Every endpoint's receipt.
	"CaptureResult": true,
	// The PostHog wire — now served on /v1/event, sniffed by decodeEvent.
	"insightsBody": true, "insightsEvent": true,
	// The session-replay snapshot wire (replay.go). Its fields carry doc comments in
	// Go like every other type here; it reaches the document through the same
	// reflection-based Register client, which cannot see them.
	"replayBody": true,
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate
// cannot see. A typed op publishes its Out's whole schema, and a property that
// reaches openapi.yaml with no description reaches every generated SDK and every MCP
// inputSchema without one too — `errorRate` as a bare number nowhere documented as a
// ratio, `pct` nowhere documented as a share of the WINDOW rather than of the rows
// returned.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "event", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Through JSON, because that is the artifact: Components.Schemas is open-typed
	// (Register contributes *Schema, the typed fold contributes zip's map) and only
	// the marshalled form is what an SDK generator actually reads.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	described := map[string]bool{}
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) != "" {
				described[name] = true
				continue
			}
			if proseless[name] {
				continue
			}
			bare = append(bare, name+"."+field)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/analytics/...",
			len(bare), strings.Join(bare, ", "))
	}
	// The ledger may only SHRINK, and only by a component gaining prose or leaving
	// the document — never by an entry rotting unnoticed.
	for name := range proseless {
		if _, published := published.Components.Schemas[name]; !published {
			t.Errorf("proseless names %q, which analytics no longer publishes", name)
			continue
		}
		if described[name] {
			t.Errorf("%q now publishes field prose — drop it from proseless", name)
		}
	}
}

// ── the typed reads keep the wire they always had ───────────────────────────

// orgGatedReads is every typed read that requires a validated principal. /v1/event/errors
// and /v1/event/insights/events are here for the first time — nothing pinned their 403
// before, because nothing read them back.
var orgGatedReads = []string{
	"/v1/event/overview", "/v1/event/timeseries", "/v1/event/top",
	"/v1/event/errors", "/v1/event/insights/events",
}

// TestTypedReadsRefuseWithoutAValidatedPrincipal is the cross-tenant-forge proof for
// the typed lane. A typed op receives only a context, so the org it reads is the one
// cloud.Bridge parked from the VALIDATED principal — never an In field and never a
// raw header. A caller reaching the pod directly with a forged X-Org-Id and no
// X-User-Id is refused 403 on every one of them.
func TestTypedReadsRefuseWithoutAValidatedPrincipal(t *testing.T) {
	app := mountApp(t)
	for _, p := range orgGatedReads {
		if code, body := do(t, app, http.MethodGet, p, "", ""); code != http.StatusForbidden {
			t.Errorf("no-principal GET %s = %d (%s), want 403", p, code, body)
		}
		if code, body := do(t, app, http.MethodGet, p+"?limit=5", "", "maxpower"); code != http.StatusForbidden {
			t.Errorf("forged-org-no-bearer GET %s = %d (%s), want 403", p, code, body)
		}
	}
}

// TestTypedReadsSurviveTheirOwnQueryString proves the ONE thing making these ops
// typable did not change: bindURL fills the In from the query string, and an
// unparseable value leaves the field at its zero rather than failing the request.
// `?limit=abc` was a caller typo about one field before (strconv.Atoi's error was
// discarded) and still is — the alternative, a 400, would make every typed GET
// brittler than the untyped handler it replaced.
func TestTypedReadsSurviveTheirOwnQueryString(t *testing.T) {
	app := mountApp(t)
	// The three window reads reach the datastore gate (503 here) rather than 400 —
	// so a junk ?limit= never turned into a refusal.
	for _, p := range []string{"/v1/event/overview", "/v1/event/timeseries", "/v1/event/top"} {
		if code, body := do(t, app, http.MethodGet, p+"?limit=abc&utm_source=x", "u", "acme"); code != http.StatusServiceUnavailable {
			t.Errorf("GET %s?limit=abc = %d (%s), want 503 — a junk query value must not refuse the call", p, code, body)
		}
	}
	// A bad ?range= is still the 400 it has always been, and still BEFORE the
	// datastore is consulted.
	for _, p := range []string{"/v1/event/overview", "/v1/event/timeseries", "/v1/event/top"} {
		if code, body := do(t, app, http.MethodGet, p+"?range=bogus", "u", "acme"); code != http.StatusBadRequest {
			t.Errorf("GET %s?range=bogus = %d (%s), want 400", p, code, body)
		}
	}
}

// TestLimitClampsAreUnchanged pins the two clamps the typed Ins now carry against the
// exact behaviour the untyped strconv.Atoi readers had, including the case that used
// to be an ERROR and is now a zero: bindURL leaves an unparseable field at its zero
// value, and zero takes the default, which is what Atoi's discarded error did too.
func TestLimitClampsAreUnchanged(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, defaultTop}, {-5, defaultTop}, {1, 1}, {maxTop, maxTop}, {maxTop + 1, maxTop}, {10_000, maxTop},
	} {
		if got := (topQuery{Limit: tc.in}).limit(); got != tc.want {
			t.Errorf("topQuery{%d}.limit() = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want int }{
		{0, defaultRecent}, {-5, defaultRecent}, {1, 1}, {maxRecent, maxRecent}, {maxRecent + 1, maxRecent},
	} {
		if got := (limitQuery{Limit: tc.in}).rows(); got != tc.want {
			t.Errorf("limitQuery{%d}.rows() = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestInsightsHealthIsUnconditional pins the one typed op that needs no principal:
// liveness must be probe-able, so it answers 200 with the same three fields it always
// did, whether or not a bearer was presented.
func TestInsightsHealthIsUnconditional(t *testing.T) {
	app := mountApp(t)
	for _, user := range []string{"", "user-dave"} {
		code, body := do(t, app, http.MethodGet, "/v1/event/insights/health", user, "")
		if code != http.StatusOK {
			t.Fatalf("GET /v1/event/insights/health (user=%q) = %d (%s), want 200", user, code, body)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("health json: %v (%s)", err, body)
		}
		if got["ok"] != true || got["engine"] != "hanzo-analytics" || got["surface"] != "/v1/event/insights" {
			t.Errorf("GET /v1/event/insights/health = %v, want {ok:true, engine:hanzo-analytics, surface:/v1/event/insights}", got)
		}
	}
}

// ── the refusals are MEASURED, not asserted ─────────────────────────────────

// TestArrayBodiedDoorsStillAnswer200 is the measurement behind canonWireReason: the
// four endpoints on an array-tolerant wire accept a BARE JSON ARRAY body today. That is
// exactly the request a typed In would turn into a 400 (zip's op.invoke unmarshals
// every non-empty body into the struct), so this is the refusal's evidence rather
// than its restatement. If a later zip can declare a polymorphic body, this test is
// what tells you the conversion is safe.
func TestArrayBodiedDoorsStillAnswer200(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		var body string
		switch {
		case sameWire(d.decode, decodeIngest):
			body = `[{"event":"$pageview","distinctId":"anon-1"}]`
		case sameWire(d.decode, decodeTeam):
			body = teamPageview
		default:
			continue // decodeInsights is object-only; its blocker is admission, not shape.
		}
		evs, err := d.decode([]byte(body))
		if err != nil {
			t.Fatalf("endpoint %s cannot decode its own array wire: %v", d.path, err)
		}
		if len(evs) != 1 {
			t.Fatalf("endpoint %s decoded %d events from an array body, want 1", d.path, len(evs))
		}
		fakeWarehouse(t)
		app := mountApp(t)
		if code, got := doHost(t, app, d.path, "", "", "api.hanzo.ai", body); code != http.StatusOK {
			t.Errorf("endpoint %s with a bare ARRAY body = %d (%s), want 200 — this is the 400 a typed In "+
				"would produce, and the reason the endpoint stays untyped", d.path, code, got)
		}
	}
}

// TestHealthStillCarriesItsReportAt503 is the health conversion's parity proof:
// the status and the body are ONE answer. The typed op declares WithStatus(200,
// 503) and the report's own StatusCode picks between them, so the degraded answer
// keeps carrying the report — and this test is what goes red if a change ever
// drops it back to an error envelope.
func TestHealthStillCarriesItsReportAt503(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/event/health", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/event/health = %d (%s), want 503", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("health json: %v (%s)", err, body)
	}
	for k, want := range map[string]any{"service": "event", "status": "degraded", "datastore": false} {
		if got[k] != want {
			t.Errorf("503 body[%q] = %v, want %v — the report IS the answer, not a side note", k, got[k], want)
		}
	}
	if s, _ := got["reason"].(string); strings.TrimSpace(s) == "" {
		t.Error("503 body carries no reason — the degraded report is what a typed op would drop")
	}
}

// TestHealthReportKeepsTheMapItReplaced pins the probe's wire against the map literal
// this struct replaced. Moving from map[string]any to a named type is what lets the
// document state the report's shape (openapi.Register, event.go) — but only the field
// NAMES and the presence rules are the wire, and a struct is exactly where a rename or
// a stray omitempty would silently drop one.
func TestHealthReportKeepsTheMapItReplaced(t *testing.T) {
	degraded, err := json.Marshal(healthReport{
		Service: "event", Status: "degraded", Datastore: false, Warehouse: "hanzo",
		Reason: "datastore (datastore) not connected",
		Plane:  healthPlane{Bus: "nats://127.0.0.1:4222", Stream: EventStream, Ready: true},
	})
	if err != nil {
		t.Fatalf("marshal degraded: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(degraded, &got); err != nil {
		t.Fatalf("degraded json: %v", err)
	}
	want := map[string]any{
		"service": "event", "status": "degraded", "datastore": false, "warehouse": "hanzo",
		"reason": "datastore (datastore) not connected",
		// `lost` is present on the DEGRADED report too, and that is the point of it
		// being here rather than under omitempty: an unreachable warehouse is exactly
		// when deliveries start failing, so a zero that disappears at the moment the
		// number would move is worse than useless to whoever is reading this.
		"lost": map[string]any{"undecodable": float64(0), "exhausted": float64(0)},
		// `plane` is present on EVERY report for the same reason, and it is the field
		// whose absence was an outage: this probe answered 200/ok on warehouse
		// connectivity alone while 100% of writes failed on the bus. The two halves are
		// independent — a reachable plane on a degraded report is real information, and
		// so is a broken one — so neither hides behind the other's status.
		"plane": map[string]any{"bus": "nats://127.0.0.1:4222", "stream": EventStream, "ready": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("degraded report = %v, want exactly %v — lenses must be ABSENT, not null: the "+
			"probe could not reach the tables it would describe", got, want)
	}

	ok, err := json.Marshal(healthReport{
		Service: "event", Status: "ok", Datastore: true, Warehouse: "hanzo",
		Lenses: &healthLenses{
			LLM:    healthLens{Table: llmTable, Available: true},
			Events: healthLens{Table: factTable, Available: false},
		},
	})
	if err != nil {
		t.Fatalf("marshal ok: %v", err)
	}
	got = nil
	if err := json.Unmarshal(ok, &got); err != nil {
		t.Fatalf("ok json: %v", err)
	}
	if _, present := got["reason"]; present {
		t.Error("healthy report carries a reason — omitempty must keep it out")
	}
	lenses, _ := got["lenses"].(map[string]any)
	llm, _ := lenses["llm"].(map[string]any)
	events, _ := lenses["events"].(map[string]any)
	if llm["table"] != llmTable || llm["available"] != true {
		t.Errorf("lenses.llm = %v, want {table:%s, available:true}", llm, llmTable)
	}
	if events["table"] != factTable || events["available"] != false {
		t.Errorf("lenses.events = %v, want {table:%s, available:false}", events, factTable)
	}
}

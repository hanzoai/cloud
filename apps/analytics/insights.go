package analytics

// /v1/insights — the UNIFIED native insights surface on the SAME engine.
//
// This file is a WIRE ADAPTER, not a second pipeline: third-party analytics payloads
// (what @hanzo/insights and every PostHog-compatible SDK emit) are mapped onto
// the native CaptureEvent and flow through the ONE capture path (normalize →
// scrub → the event plane), and the console reads recent events back from
// event.fact's act rows through the ONE datastore client. Flags stay at /v1/flags (the
// native flags engine) — this namespace deliberately does not duplicate them.
//
// The PostHog wire had a door of its own here — /v1/insights/e — because external
// SDKs emit this shape and insights.hanzo.ai rewrites every PostHog ingest path onto
// it. It is retired: a wire is a SHAPE, and a shape never earned a path, so decodeEvent
// sniffs this one on /v1/event and hands it to decodeInsights below. The wire did not
// move — the ingress rewrite now targets /v1/event — so no caller did either, and this
// file is the whole of the difference between the two shapes.
//
// Routes (org resolved SERVER-SIDE — same tenant gates as the rest):
//
//	GET  /v1/insights/events  recent events for the org (console read; limit<=200)
//	GET  /v1/insights/health  liveness of the unified surface
//
// SCALE PATH: accept is stateless (any replica); the commit is the JetStream
// publish (bus.go) and the landing is the per-signal durable consumer
// (warehouse.go) — the queue is already between the door and the store, so
// scaling ingest is scaling replicas, no handler changes.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// insightsEvent is the PostHog wire shape (subset that matters for ingest).
// UUID is the top-level per-event id PostHog SDKs mint for idempotency; the rest
// of the identity/attribution the SDKs carry rides inside Properties (mapped in
// toCapture).
type insightsEvent struct {
	UUID       string         `json:"uuid"`
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  string         `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

// insightsBody accepts both the single-event and batch PostHog shapes.
type insightsBody struct {
	insightsEvent
	Batch []insightsEvent `json:"batch"`
}

// toCapture maps one PostHog event onto the native CaptureEvent. Well-known
// $-properties become first-class columns; the rest stay in properties (the
// scrubber runs downstream in normalize, same as every native event).
func (e insightsEvent) toCapture() CaptureEvent {
	props := e.Properties
	str := func(key string) string {
		if props == nil {
			return ""
		}
		if v, ok := props[key].(string); ok {
			return v
		}
		return ""
	}
	typ := "event"
	if e.Event == "$pageview" {
		typ = "pageview"
	}
	return CaptureEvent{
		// Idempotency id: PostHog SDKs carry a top-level event `uuid`; some send it
		// as an `$insert_id` property instead. Preserve it as the client MessageID so
		// a retried batch (insights-go retries with backoff) keeps a STABLE row id
		// rather than the server minting a fresh one per attempt.
		MessageID:  cmp.Or(strings.TrimSpace(e.UUID), strings.TrimSpace(str("$insert_id"))),
		Type:       typ,
		Event:      e.Event,
		Timestamp:  e.Timestamp,
		DistinctID: e.DistinctID,
		SessionID:  str("$session_id"),
		URL:        str("$current_url"),
		Path:       str("$pathname"),
		Referrer:   str("$referrer"),
		// UTM attribution: PostHog SDKs put campaign params in BARE `utm_*`
		// properties (not $-prefixed — confirmed against the SDK/ingest source).
		// The plane normalizer maps CaptureEvent.UTM into the envelope's
		// attributes['utm_*'] entries (fact.go attributesOf), so surfacing them here
		// is what lets the web/commerce lens attribute traffic to a campaign. They
		// were previously dropped on the PostHog-wire front door.
		UTM: UTM{
			Source:   str("utm_source"),
			Medium:   str("utm_medium"),
			Campaign: str("utm_campaign"),
			Term:     str("utm_term"),
			Content:  str("utm_content"),
		},
		Product:    str("product"),
		Library:    str("$lib"),
		LibraryVer: str("$lib_version"),
		Properties: props,
	}
}

// decodeInsights is the PostHog WIRE's decoder — the second and last `decode` in this
// package (decodeIngest is the other). Pure over the raw bytes, exactly like its twin,
// so the ONE pipeline can be handed a wire instead of forking per door: it accepts the
// single-event and {batch:[…]} PostHog shapes and yields the SAME []CaptureEvent the
// write core consumes. An empty/whitespace-only body ⇒ no events (an honest empty
// receipt, not an error), matching decodeIngest.
func decodeInsights(body []byte) ([]CaptureEvent, error) {
	if firstNonWS(body) >= len(body) {
		return nil, nil
	}
	var b insightsBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, err
	}
	events := b.Batch
	if len(events) == 0 && b.Event != "" {
		events = []insightsEvent{b.insightsEvent}
	}
	caps := make([]CaptureEvent, len(events))
	for i, e := range events {
		caps[i] = e.toCapture()
	}
	return caps, nil
}

// productEvent is one stored product event as the console reads it back. The columns
// are event.fact's own columns; everything else the caller sent lives in the attributes
// map, returned as the properties object.
type productEvent struct {
	// ID is the row's stable event id — the client's own idempotency id when it sent
	// one, else the server-minted one.
	ID string `json:"id"`
	// Timestamp is when the event happened, RFC3339 UTC.
	Timestamp string `json:"timestamp"`
	// Event is the event name, e.g. page_viewed or signup_completed.
	Event string `json:"event"`
	// Type is the row's kind — the plane's discriminator: page, track, identify or
	// group. (Errors are not here at all: they land on event.error and are read at
	// /v1/errors.)
	Type string `json:"type"`
	// DistinctID is the person/visitor the event is attributed to.
	DistinctID string `json:"distinctId"`
	// SessionID groups the events of one visit. Omitted when the client sent none.
	SessionID string `json:"sessionId,omitempty"`
	// Product is the surface that emitted the event. Omitted when absent.
	Product string `json:"product,omitempty"`
	// URL is the full page address the event fired on. Omitted when absent.
	URL string `json:"url,omitempty"`
	// Path is the URL's path component, the key the topPages lens groups by.
	Path string `json:"path,omitempty"`
	// Properties is the row's attributes map as a JSON object (string values — the
	// plane stores Map(String,String), so a nested value the caller sent is a
	// JSON-encoded string). Omitted when the row carries none.
	Properties json.RawMessage `json:"properties,omitempty"`
}

// eventList is a page of stored product events, newest first.
type eventList struct {
	// Data is the events, newest first. Empty rather than absent when there are none.
	Data []productEvent `json:"data"`
}

// InsightsEvents returns the caller org's most recent product events, newest first.
// The console's raw-event view over event.event — the same table the capture doors
// fill — one row per stored event, with the row's attributes returned as the
// properties object.
//
// The org is the validated principal's — never a parameter — and a read requires a
// real bearer, never the write-only publishable key. 403 without a validated bearer,
// 503 when the warehouse is unreachable.
//
// Example: {"limit": 100}
func (o readOps) insightsEvents(ctx context.Context, in *limitQuery) (*eventList, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	where, args := scope(org, signalAct)
	rows, err := datastore.Query(ctx, `
		SELECT id, time, name, kind, distinct_id, session_id,
		       product, url, path, attributes
		FROM `+factTable+`
		WHERE `+where+`
		ORDER BY time DESC
		LIMIT ?`, append(args, in.rows())...)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	out := make([]productEvent, 0, len(rows))
	for _, r := range rows {
		e := productEvent{
			ID: asStr(r["id"]), Timestamp: asStr(r["time"]), Event: asStr(r["name"]),
			Type: asStr(r["kind"]), DistinctID: asStr(r["distinct_id"]),
			SessionID: asStr(r["session_id"]), Product: asStr(r["product"]),
			URL: asStr(r["url"]), Path: asStr(r["path"]),
		}
		e.Properties = attrsJSON(aStrMap(r["attributes"]))
		out = append(out, e)
	}
	return &eventList{Data: out}, nil
}

// insightsStatus is the liveness answer of the unified insights surface.
type insightsStatus struct {
	// OK is always true — reaching this route is the liveness fact it reports.
	OK bool `json:"ok"`
	// Engine names the engine serving the surface: hanzo-analytics.
	Engine string `json:"engine"`
	// Surface is the path prefix this status covers: /v1/insights.
	Surface string `json:"surface"`
}

// InsightsHealth reports that the unified insights surface is serving. It reads no
// tenant data and consults no dependency, so it answers 200 unconditionally and needs
// no principal — liveness must be probe-able. The warehouse-connectivity probe is a
// different question and lives at GET /v1/analytics/health.
func (o readOps) insightsHealth(ctx context.Context, _ *noArgs) (*insightsStatus, error) {
	return &insightsStatus{OK: true, Engine: "hanzo-analytics", Surface: "/v1/insights"}, nil
}

func asStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return strings.Trim(string(b), `"`)
	}
}

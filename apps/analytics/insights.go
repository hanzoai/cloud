package analytics

// /v1/insights — the UNIFIED native insights surface on the SAME engine.
//
// This file is a WIRE ADAPTER, not a second pipeline: PostHog-shaped payloads
// (what @hanzo/insights and every PostHog-compatible SDK emit) are mapped onto
// the native CaptureEvent and flow through the ONE capture path (normalize →
// scrub → event.event), and the console reads recent events back through the
// ONE datastore client. Flags stay at /v1/flags (the native flags engine) —
// this namespace deliberately does not duplicate them.
//
// The PostHog wire is why /v1/insights/e is a DOOR and not an alias: external SDKs
// emit this shape and insights.hanzo.ai rewrites every PostHog ingest path onto it,
// so no canonical-wire door can serve them. decodeInsights below is the whole of
// that difference — the door is declared in doors (event.go) and shares admission,
// the ingest core and the receipt with /v1/event.
//
// Routes (org resolved SERVER-SIDE — same tenant gates as the rest):
//
//	POST /v1/insights/e       PostHog wire ingest: one event or {batch:[...]} (a door)
//	GET  /v1/insights/events  recent events for the org (console read; limit<=200)
//	GET  /v1/insights/health  liveness of the unified surface
//
// SCALE PATH: accept is stateless (any replica) and the sink is the pooled
// batch INSERT into Datastore. When ingest volume outgrows direct sink, the
// seam is buildEventsInsert — swap the exec for a queue producer (mq/pubsub)
// with a Datastore consumer, no handler changes.

import (
	"encoding/json"
	"strings"
	"time"
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
// scrubber runs downstream in normalizeEvent, same as every native event).
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
		MessageID:  firstNonEmptyStr(strings.TrimSpace(e.UUID), strings.TrimSpace(str("$insert_id"))),
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
		// event.event has first-class utm_* columns and the native capture path
		// maps CaptureEvent.UTM into them (capture.go), so surfacing them here is
		// what lets the web/commerce lens attribute traffic to a campaign. They were
		// previously dropped on the PostHog-wire front door.
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
// ingest core consumes. An empty/whitespace-only body ⇒ no events (an honest empty
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

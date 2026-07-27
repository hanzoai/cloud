package analytics

// /v1/insights — the UNIFIED native insights surface on the SAME engine.
//
// This file is a WIRE ADAPTER, not a second pipeline: PostHog-shaped payloads
// (what @hanzo/insights and every PostHog-compatible SDK emit) are mapped onto
// the native CaptureEvent and flow through the ONE capture path (normalize →
// scrub → hanzo.events), and the console reads recent events back through the
// ONE datastore client. Flags stay at /v1/flags (the native flags engine) —
// this namespace deliberately does not duplicate them.
//
// Routes (org resolved SERVER-SIDE — same tenant gates as the rest):
//
//	POST /v1/insights/e       PostHog-compatible ingest: one event or {batch:[...]}
//	GET  /v1/insights/events  recent events for the org (console read; limit<=200)
//	GET  /v1/insights/health  liveness of the unified surface
//
// SCALE PATH: accept is stateless (any replica) and the sink is the pooled
// batch INSERT into Datastore. When ingest volume outgrows direct sink, the
// seam is buildEventsInsert — swap the exec for a queue producer (mq/pubsub)
// with a Datastore consumer, no handler changes.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/datastore"
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
		// hanzo.events has first-class utm_* columns and the native capture path
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

// insightsIngest answers POST /v1/insights/e — a DEPRECATED foreign-protocol shim
// for the PostHog wire. External-SDK compat ONLY; no Hanzo surface uses it (Hanzo
// surfaces POST /v1/event). It normalizes the PostHog single/batch shape onto
// CaptureEvent and funnels through the ONE write core (ingestEvents, source=posthog);
// it keeps captureTenant's brand-host path so anonymous external PostHog-wire traffic
// is unbroken — the path the strict canonical door deliberately refuses.
func insightsIngest(s *cloud.Service[state], c *zip.Ctx) error {
	deprecated(s, c, "/v1/event")
	org, ok := captureTenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer or a recognized brand host required")
	}
	return insightsWithOrg(org, c)
}

// insightsWithOrg is the PostHog-wire decode+ingest core with the tenant supplied
// EXPLICITLY by the caller — the twin of captureWithOrg for the PostHog beacon
// shape. The /v1/insights/e alias resolves org via captureTenant; the site-host
// carve FORCES org from the resolved Site (host-derived, never the caller/body).
// Both funnel through the ONE write core (ingestEvents, source=posthog).
func insightsWithOrg(org string, c *zip.Ctx) error {
	var body insightsBody
	if err := c.Bind(&body); err != nil {
		return zip.ErrBadRequest("malformed insights payload")
	}
	events := body.Batch
	if len(events) == 0 && body.Event != "" {
		events = []insightsEvent{body.insightsEvent}
	}
	caps := make([]CaptureEvent, len(events))
	for i, e := range events {
		caps[i] = e.toCapture()
	}
	res, err := ingestEvents(c.Context(), org, sourcePostHog, caps)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

// insightsEvents answers GET /v1/insights/events — the console's recent-events
// read (newest first). Tenant-scoped server-side; limit defaults 50, caps 200.
func insightsEvents(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := datastore.Query(c.Context(), `
		SELECT id, timestamp, event, event_type, distinct_id, session_id,
		       product, url, path, properties
		FROM hanzo.events
		WHERE tenant_id = ?
		ORDER BY timestamp DESC
		LIMIT ?`, org, limit)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	type ev struct {
		ID         string          `json:"id"`
		Timestamp  string          `json:"timestamp"`
		Event      string          `json:"event"`
		Type       string          `json:"type"`
		DistinctID string          `json:"distinctId"`
		SessionID  string          `json:"sessionId,omitempty"`
		Product    string          `json:"product,omitempty"`
		URL        string          `json:"url,omitempty"`
		Path       string          `json:"path,omitempty"`
		Properties json.RawMessage `json:"properties,omitempty"`
	}
	out := make([]ev, 0, len(rows))
	for _, r := range rows {
		e := ev{
			ID: asStr(r["id"]), Timestamp: asStr(r["timestamp"]), Event: asStr(r["event"]),
			Type: asStr(r["event_type"]), DistinctID: asStr(r["distinct_id"]),
			SessionID: asStr(r["session_id"]), Product: asStr(r["product"]),
			URL: asStr(r["url"]), Path: asStr(r["path"]),
		}
		if p := asStr(r["properties"]); p != "" && json.Valid([]byte(p)) {
			e.Properties = json.RawMessage(p)
		}
		out = append(out, e)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func insightsHealth(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "engine": "hanzo-analytics", "surface": "/v1/insights"})
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

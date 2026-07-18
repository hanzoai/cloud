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

// event.go — the ONE canonical event-ingestion front door.
//
//	POST /v1/event   body: Event | [Event]   ->  {accepted, dropped}
//
// A JSON object is one event; a JSON array IS the batch (there is deliberately no
// /v1/event/batch). Every other ingest surface (the PostHog wire at
// /v1/insights/e, the Segment/beacon wire at /v1/analytics{,/batch} and
// /v1/tracker) is a thin DEPRECATED adapter that normalizes its own wire shape
// onto CaptureEvent and funnels through the SAME write core (ingestEvents) into
// the SAME hanzo.events table. One write path, many adapters.
//
// AUTH — IAM ONLY, FAIL-CLOSED: the tenant is resolved SERVER-SIDE from a
// validated bearer principal (its owner org) or, for a keyed bearer-less SDK, an
// access key resolved through the ONE IAM key seam (cloud.OrgForKey). There is NO
// brand-host fallback on this endpoint: an unauthenticated or unresolvable caller
// is refused (403), so the canonical door never writes an event into a tenant IAM
// did not vouch for. The org is NEVER read from the body.
package analytics

import (
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Event is the canonical analytics event — the entire ingest contract in four
// fields. Only these are first-class; everything else a caller wants to record
// travels in Properties (the scrubber runs over it downstream, same as every
// event). The tenant is NOT a field: it is resolved server-side from IAM, so a
// caller can only ever write into its OWN org's partition.
type Event struct {
	Event      string         `json:"event"`      // event name (required; empty ⇒ dropped as unroutable)
	DistinctID string         `json:"distinctId"` // the person/visitor id the caller owns
	Time       string         `json:"time"`       // optional RFC3339; clamped to server-now on skew/absent
	Properties map[string]any `json:"properties"` // everything non-core
}

// toCapture adapts the canonical Event onto the internal CaptureEvent the write
// core consumes. Type is left empty (canonicalType ⇒ "event"); no $-property is
// promoted to a column here — /v1/event stays a strict four-field contract, and
// every non-core field the caller sent stays in Properties.
func (e Event) toCapture() CaptureEvent {
	return CaptureEvent{
		Event:      e.Event,
		DistinctID: e.DistinctID,
		Timestamp:  e.Time,
		Properties: e.Properties,
	}
}

// eventTenant resolves the tenant for POST /v1/event — IAM ONLY, FAIL-CLOSED. A
// validated bearer principal wins (its owner org); otherwise a presented access
// key is resolved to its org through the ONE IAM key seam (resolveKeyOrg →
// cloud.OrgForKey). There is NO brand-host fallback: an unauthenticated or
// unresolvable caller returns ("", false) → 403. (The deprecated aliases keep
// captureTenant's brand-host path for anonymous marketing traffic; the canonical
// endpoint is deliberately stricter — IAM is the only tenant authority here.)
func eventTenant(c *zip.Ctx) (string, bool) {
	if org, ok := tenant(c); ok {
		return org, true
	}
	if key := projectKey(c); key != "" {
		if org, ok := resolveKeyOrg(c.Context(), key); ok {
			return org, true
		}
	}
	return "", false
}

// decodeEvents decodes a request body as Event | []Event. The first non-space
// byte decides: '[' ⇒ the array batch, anything else ⇒ a single Event. An empty
// body yields no events (an honest empty receipt, not an error). Pure over the
// raw bytes (the handler passes c.Body() — fasthttp-buffered, the same bytes
// projectKey peeked) so the decode is driven directly by tests.
func decodeEvents(body []byte) ([]Event, error) {
	i := 0
	for i < len(body) {
		if b := body[i]; b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			i++
			continue
		}
		break
	}
	if i >= len(body) {
		return nil, nil
	}
	if body[i] == '[' {
		var evs []Event
		if err := json.Unmarshal(body, &evs); err != nil {
			return nil, err
		}
		return evs, nil
	}
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return []Event{e}, nil
}

// eventIngest answers POST /v1/event — the ONE canonical ingestion front door.
// Org is IAM-derived and fail-closed (eventTenant); the body is Event | [Event];
// every event flows through the ONE write core (ingestEvents) into the ONE
// hanzo.events table, tagged source=event.
func eventIngest(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := eventTenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer or a resolvable access key required")
	}
	evs, err := decodeEvents(c.Body())
	if err != nil {
		return zip.ErrBadRequest("malformed event payload")
	}
	caps := make([]CaptureEvent, len(evs))
	for i, e := range evs {
		caps[i] = e.toCapture()
	}
	res, err := ingestEvents(c.Context(), org, sourceEvent, caps)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

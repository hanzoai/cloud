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
//	POST /v1/event   body: Event | [Event] | {batch:[…]}   ->  {accepted, dropped}
//
// ONE door, EVERY wire, EVERY auth context. The decoder (decodeIngest) is
// wire-tolerant: a bare canonical Event object, a bare [Event] array, AND the
// CaptureBatch envelope ({batch:[…]} | {events:[…]}) the Segment/beacon/publishable
// paths speak all decode onto the SAME []CaptureEvent the ONE write core
// (ingestEvents) consumes, into the SAME hanzo.events table. There is deliberately
// no /v1/event/batch — a JSON array, or a batch envelope, IS the batch.
//
// AUTH is the orthogonal, PLUGGABLE concern on this one door (eventTenant), resolved
// SERVER-SIDE and FAIL-CLOSED, in strict trust order:
//
//  1. a validated IAM bearer principal — its owner org;
//  2. a publishable key (pk-…) — IAM resolves it to its org; it can write but
//     SAME key publishable.go mints; folded in here so a pk_ caller uses /v1/event
//     directly);
//  3. an out-of-band IAM access key (hk-/sk-…) — resolved through the ONE key seam
//     (cloud.OrgForKey).
//
// None of the above ⇒ 403. There is NO brand-host fallback on the canonical door
// (that path stays only on the deprecated aliases), so /v1/event never writes an
// event into a tenant IAM did not vouch for. The org is NEVER read from the body.
//
// The site-host carve (eventWithOrg) is the ONE exception to in-handler auth: on a
// published site host the tenant is FORCED from the resolved Site BEFORE the handler
// — the same server-supplied, host-derived tenant the file/base carves trust.
//
// Every other ingest route (/v1/ingest, /v1/analytics{,/batch}, /v1/tracker,
// /v1/insights/e) is a thin alias/shim that resolves org its own way and funnels
// through the SAME decode + write core. One write path, many doors.
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

// eventTenant resolves the tenant for the canonical door — PLUGGABLE auth,
// FAIL-CLOSED, in strict trust order:
//
//  1. a validated IAM bearer principal wins (its owner org);
//  2. else a presented write-only publishable key (pk_…) is HMAC-verified to its
//     org with no IAM/DB hop (the SAME verifier publishable.go's /v1/ingest used —
//     folded in here so a pk_ caller uses /v1/event directly);
//  3. else a presented out-of-band IAM access key (hk-/sk-…) is resolved to its org
//     through the ONE key seam (resolveKeyOrg → cloud.OrgForKey).
//
// None matches ⇒ ("", false) → 403. There is NO brand-host fallback (that path
// stays only on the deprecated aliases), so the canonical door is strictly authed —
// IAM or a signed/resolvable key, never the request Host.
func eventTenant(c *zip.Ctx) (string, bool) {
	if org, ok := tenant(c); ok {
		return org, true
	}
	// ONE publishable key, and IAM issues it. A pk- on any ingest-shaped carrier
	// (Bearer, x-hanzo-ingest-key, ?ingest_key= for sendBeacon, which cannot set
	// headers) resolves through the SAME IAM seam as every other key. Cloud used
	// to mint and verify its own pk_ under an HMAC of CLOUD_INGEST_KEY_SECRET —
	// a second publishable-key family with its own prefix, secret and mint
	// endpoint, beside the one IAM already owned.
	//
	// Safe only because a pk- no longer authenticates: IdentityFromRequest
	// refuses it, so it attributes a write and never mints a reading principal.
	if key := ingestKey(c); key != "" {
		if org, ok := resolveKeyOrg(c.Context(), key); ok {
			return org, true
		}
	}
	if key := projectKey(c); key != "" {
		if org, ok := resolveKeyOrg(c.Context(), key); ok {
			return org, true
		}
	}
	return "", false
}

// firstNonWS returns the index of the first non-JSON-whitespace byte, or len(body)
// when the body is empty or all whitespace. The four bytes are JSON's insignificant
// whitespace (RFC 8259 §2). The ONE place the ingest decoders skip leading space.
func firstNonWS(body []byte) int {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return i
		}
	}
	return len(body)
}

// decodeEvents decodes a body as the canonical Event wire: Event | []Event. The
// first non-space byte decides: '[' ⇒ the array batch, anything else ⇒ a single
// Event. An empty body yields no events (an honest empty receipt, not an error).
// Pure over the raw bytes; it is the canonical-Event sub-decoder inside decodeIngest
// (which additionally accepts the CaptureBatch envelope).
func decodeEvents(body []byte) ([]Event, error) {
	i := firstNonWS(body)
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

// decodeIngest is the ONE wire-tolerant decoder of the canonical door. It accepts
// EVERY shape a Hanzo surface emits and yields the SAME []CaptureEvent the write
// core consumes:
//
//   - the CaptureBatch envelope {batch:[…]} | {events:[…]} — the Segment/beacon/
//     publishable wire — detected by a top-level batch/events key;
//   - a bare canonical Event object {"event":…,"distinctId":…};
//   - a bare canonical Event array [ {…}, … ].
//
// An empty/whitespace-only body ⇒ no events (honest empty receipt, not an error).
// Pure over the raw bytes (handlers pass c.Body() — fasthttp-buffered, the same
// bytes projectKey/ingestKey peeked), so the decode is driven directly by tests.
func decodeIngest(body []byte) ([]CaptureEvent, error) {
	i := firstNonWS(body)
	if i >= len(body) {
		return nil, nil
	}
	if body[i] == '{' {
		// An object is the CaptureBatch envelope iff it carries a batch/events key;
		// otherwise it is a bare canonical Event. RawMessage is non-nil whenever the
		// key is present (even `[]`), so an empty batch is still routed as an envelope.
		var probe struct {
			Batch  json.RawMessage `json:"batch"`
			Events json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil, err
		}
		if probe.Batch != nil || probe.Events != nil {
			var batch CaptureBatch
			if err := json.Unmarshal(body, &batch); err != nil {
				return nil, err
			}
			return batch.events(), nil
		}
	}
	evs, err := decodeEvents(body)
	if err != nil {
		return nil, err
	}
	caps := make([]CaptureEvent, len(evs))
	for j, e := range evs {
		caps[j] = e.toCapture()
	}
	return caps, nil
}

// ingestBody is the ONE ingest core given an already-resolved org: wire-tolerant
// decode (decodeIngest) → fold type:'error' events (foldException) → the ONE write
// core (ingestEvents) → the honest receipt. org is the SERVER-resolved tenant (never
// client input); source tags the front door for the $source migration signal. Every
// door — the canonical /v1/event (pluggable auth), the site-host carve (forced org),
// and the deprecated aliases — resolves org its OWN way then calls THIS: auth is the
// only thing that differs between doors, the decode + write path is identical.
func ingestBody(c *zip.Ctx, org, source string) error {
	evs, err := decodeIngest(c.Body())
	if err != nil {
		return zip.ErrBadRequest("malformed event payload")
	}
	for i := range evs {
		evs[i] = foldException(evs[i])
	}
	res, err := ingestEvents(c.Context(), org, source, evs)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

// eventHandle is the canonical-door handler core: pluggable in-handler auth
// (eventTenant, fail-closed) → the ONE ingest core. source tags the door so the
// canonical /v1/event and the /v1/ingest deprecated alias share ONE implementation,
// differing only in origin tag (and the alias's deprecation log).
func eventHandle(c *zip.Ctx, source string) error {
	org, ok := eventTenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer or a resolvable ingest key required")
	}
	return ingestBody(c, org, source)
}

// eventIngest answers POST /v1/event — the ONE canonical ingestion front door.
// Org is resolved fail-closed (eventTenant: bearer | pk_ | access key); the body is
// Event | [Event] | {batch}; every event flows through the ONE write core into the
// ONE hanzo.events table, tagged source=event.
func eventIngest(s *cloud.Service[state], c *zip.Ctx) error {
	return eventHandle(c, sourceEvent)
}

// eventWithOrg is the canonical-door ingest with the tenant supplied EXPLICITLY by
// the caller — the twin of captureWithOrg/insightsWithOrg for the site-host carve.
// The carve FORCES org from the resolved Site (host-derived, never the caller/body)
// and calls this, so a published-site beacon POSTing <host>/v1/event lands as that
// site's Org regardless of any body/header claim. Tagged source=event: it is the
// canonical wire, merely authorized by the host instead of a bearer/key.
func eventWithOrg(org string, c *zip.Ctx) error {
	return ingestBody(c, org, sourceEvent)
}

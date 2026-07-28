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
// CAPABILITY IS DECIDED BY TRUST LEVEL, ONCE, IN ONE PLACE (handle) — never per door.
// A door supplies only its WIRE (a decode) and its origin tag. There is no argument by
// which a door can ASK for full capability, so a door cannot forget the decision or
// route around it: the alternative is not a check to skip, it is not expressible. handle
// reads the credential itself, SERVER-SIDE and FAIL-CLOSED, in strict trust order
// (eventTenant):
//
//  1. a validated IAM bearer principal — its owner org;
//  2. a publishable key (pk-…) — IAM resolves it to its org; it can write but not read
//     (the SAME key publishable.go mints; folded in here so a pk- caller uses
//     /v1/event directly);
//  3. an out-of-band IAM access key (hk-/sk-…) — resolved through the ONE key seam
//     (cloud.OrgForKey).
//
// One of those resolves ⇒ FULL capability into that credential's org, and that branch of
// handle is the ONLY unprojected write in this package. A caller that PRESENTED one and
// did not resolve ⇒ 403 (a misconfigured key is refused, never downgraded). A caller
// that presented NOTHING is CREDENTIAL-LESS and takes the ANONYMOUS lane (publicIngest,
// public.go) — kind allowlist, field PROJECTION, its own size/rate bounds, DNT — no
// matter which door it arrived at. It rejoins this pipeline at ingestDecoded, so decode,
// write core, and receipt are shared; only capability differs.
//
// There is NO host fallback anywhere: no door turns a request Host into a REAL tenant
// with full capability. The org is NEVER read from the body, on either lane.
//
// The published-site host (installHostCarve, analytics.go) is the one door that does not
// ask, because on it there is nothing to ask: sites.Middleware runs BEFORE the identity
// boundary (serve.go — sites at 241, IdentityMiddleware at 267), so c.User()/c.Org() are
// still RAW client headers there and no credential has been validated by anything. It
// calls publicIngest DIRECTLY with the resolved Site's org as the anonymous tenant, so a
// site beacon gets the projection and its pageviews still land where the site's owner
// reads them.
//
// Every ingest route (/v1/event, /v1/ingest, /v1/analytics{,/batch}, /v1/tracker,
// /v1/insights/e) is therefore one line: handle(c, <wire>, <origin tag>). One write
// path, many doors, ONE admission decision.
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

// admission is a RESOLVED credential: the org it names and the capability it carries.
// Capability is a property of the CREDENTIAL, which is why it lives here and not on a
// door — a door still cannot ask for anything. Two levels, because the platform mints
// two kinds of principal:
//
//	full  ⇒ the unprojected write into org. Every API credential, and a workspace
//	        member's session.
//	!full ⇒ the PROJECTED write into org (publicIngest with org as the tenant — the
//	        same lane the site-host carve uses). A reduced principal: it has proven
//	        which org it belongs to, so its pageviews and errors belong there, but it
//	        may not write the revenue/personId/groupId columns or an arbitrary event
//	        kind.
//
// The middle level is not a nicety. Without it a guest is either trusted with the
// whole custom product/billing surface of an org it was invited into for one channel,
// or has its telemetry filed under $public where that org cannot read it. Both are
// wrong, and the projection is exactly the shape that is right.
type admission struct {
	org  string
	full bool
}

// eventTenant resolves the credential for every door — PLUGGABLE auth, FAIL-CLOSED,
// in strict trust order:
//
//  1. a validated IAM bearer principal wins (its owner org), at FULL capability;
//  2. else a presented write-only publishable key (pk_…) is HMAC-verified to its org
//     with no IAM/DB hop (the SAME verifier publishable.go's /v1/ingest used — folded
//     in here so a pk_ caller uses /v1/event directly), at FULL capability;
//  3. else a presented out-of-band IAM access key (hk-/sk-…) is resolved to its org
//     through the ONE key seam (resolveKeyOrg → cloud.OrgForKey), at FULL capability;
//  4. else a verified Hanzo Team workspace token — at FULL capability for a member,
//     and at REDUCED capability for a guest (teamTenant, team.go).
//
// None matches ⇒ (admission{}, false), which handle answers by refusing a presented-
// but-unresolvable credential and otherwise taking the anonymous lane. There is NO
// host fallback on ANY door, so a tenant is only ever IAM, a signed/resolvable key, or
// a signed team claim — never the request Host.
func eventTenant(c *zip.Ctx) (admission, bool) {
	if org, ok := tenant(c); ok {
		return admission{org: org, full: true}, true
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
			return admission{org: org, full: true}, true
		}
	}
	if key := projectKey(c); key != "" {
		if org, ok := resolveKeyOrg(c.Context(), key); ok {
			return admission{org: org, full: true}, true
		}
	}
	// A Hanzo Team workspace token (HS256 over SERVER_SECRET, org and role in the
	// signed extra) — the credential the team SPA already holds. It is a PLATFORM
	// credential, so it belongs in the trust order rather than on the door that
	// happens to need it, and it therefore works on every door (team.go). It is the
	// ONLY entry that can resolve at reduced capability, because it is the only one
	// the platform issues to a principal weaker than "holds an API key".
	//
	// It is LAST because it is the narrowest: the other three are issued to BE API
	// credentials, while this one is a browser session a user's tab carries. Ordering
	// it after them means a request holding both is attributed to the deliberate API
	// credential, never to whatever tab it came from.
	if a, ok := teamTenant(c); ok {
		return a, true
	}
	return admission{}, false
}

// WHY A KEY REFUSES AND A STALE IAM BEARER DOES NOT
//
// presented() below names the ingest KEY carriers and the TEAM bearer, and handle
// answers a presented-but-unresolvable credential with 403. It deliberately does NOT
// name a bare IAM bearer, and the asymmetry is a fact about what is DECIDABLE, not a
// preference:
//
//   - an ingest key is self-identifying by prefix (pk-/hk-/sk-), and a team token is
//     self-identifying by structure (it carries an `account` claim, which an IAM token
//     does not). For both, "the caller presented THIS kind of credential" is answerable
//     without trusting anything, so a failure to resolve is unambiguously a
//     misconfiguration and 403 is the honest answer.
//   - an arbitrary `Authorization: Bearer <jwt>` is not distinguishable from a bearer
//     meant for some other audience entirely. IdentityMiddleware already declines to
//     401 it (validatedPrincipal returns nil rather than refusing), so treating its
//     mere presence as "presented" here would turn every stale or foreign bearer that
//     reaches an ingest door into a 403 — a refusal on evidence we do not have.
//


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

// decode is a WIRE's decoder: raw request bytes → the canonical []CaptureEvent the ONE
// write core consumes. Exactly two exist — decodeIngest (the canonical Event / Segment
// beacon wire) and decodeInsights (the deprecated PostHog wire) — and the wire is the
// ONLY thing that differs between doors. Handing the pipeline a decoder, rather than
// forking the pipeline per wire, is what lets admission stay a single decision instead
// of one copy per door (which is exactly how the credential-less doors drifted).
type decode func([]byte) ([]CaptureEvent, error)

// ingestDecoded is the TAIL of the ingest pipeline, and the ONE place it lives: fold
// type:'error' events (foldException) → the ONE write core (ingestEvents) → the honest
// receipt. Every lane ends here, so "what happens to an admitted event" is written
// once. org is the SERVER-resolved tenant; dropped is what admission already refused
// upstream (0 on the vouched-for lane, so its behavior is unchanged), added to the
// receipt so {accepted,dropped} always totals what the caller sent.
func ingestDecoded(c *zip.Ctx, org, source string, evs []CaptureEvent, dropped int) error {
	for i := range evs {
		evs[i] = foldException(evs[i])
	}
	res, err := ingestEvents(c.Context(), org, source, evs)
	if err != nil {
		return err
	}
	res.Dropped += dropped
	return c.JSON(http.StatusOK, res)
}

// presented reports whether the request PRESENTED an IDENTIFIABLE credential at all,
// independent of whether it resolved. It is the discriminator between "misconfigured"
// (refuse) and "anonymous" (project), and it names exactly the carriers eventTenant
// consults, so the two can never disagree about what "presented" means. When
// eventTenant learned about team tokens and this did not, they DID disagree, and the
// result was the precise failure the team door exists to prevent: an expired team
// token answered 200 with its rows filed under $public, a partition its org cannot
// read.
//
// WHY A KEY AND A TEAM TOKEN REFUSE, AND A STALE IAM BEARER DOES NOT. The asymmetry is
// a fact about what is DECIDABLE, not a preference:
//
//   - an ingest key is self-identifying by PREFIX (pk-/hk-/sk-), and a team token is
//     self-identifying by STRUCTURE (it carries an `account` claim, which an IAM token
//     does not). For both, "the caller presented THIS kind of credential" is answerable
//     without trusting anything, so a failure to resolve is unambiguously a
//     misconfiguration and 403 is the honest answer.
//   - an arbitrary `Authorization: Bearer <jwt>` is not distinguishable from a bearer
//     minted for some other audience entirely. IdentityMiddleware already declines to
//     401 it (validatedPrincipal returns nil rather than refusing), so treating its
//     mere presence as "presented" here would turn every stale or foreign bearer that
//     reaches an ingest door into a 403 — a refusal on evidence we do not have.
//
// So: identifiable credential that fails ⇒ 403. Unidentifiable bearer ⇒ the anonymous
// lane, exactly as before this file learned about team tokens.
func presented(c *zip.Ctx) bool {
	return ingestKey(c) != "" || projectKey(c) != "" || teamPresented(c)
}

// handle is THE ingest pipeline and the ONE place in this package where trust level is
// decided. Every /v1 ingest door is one call to it; the door contributes its WIRE and
// its origin tag and NOTHING ELSE — capability is not a parameter, so no door can grant
// itself full capability, and a door added tomorrow inherits this decision by
// construction rather than by remembering to copy it.
//
//	credential resolves     ⇒ FULL capability into THAT credential's org.
//	credential presented,
//	  does not resolve      ⇒ 403. Never downgraded: filing a misconfigured key's
//	                          events under the public tenant would hide them in a
//	                          partition its owner cannot read — a silent failure worse
//	                          than the refusal.
//	nothing presented       ⇒ the ANONYMOUS lane (publicIngest): the projection, the
//	                          kind allowlist, the size/rate bounds, the DNT gate.
//
// The first branch below is the ONLY unprojected write in this package. It is reached
// only from here, and only with an org eventTenant resolved from a credential — which
// is the whole fix: a tenant derived from a request Host can no longer reach it,
// because the functions that used to take an org and write at full capability
// (ingestBody / eventWithOrg / captureWithOrg / insightsWithOrg) no longer exist.
func handle(c *zip.Ctx, dec decode, source string) error {
	if a, ok := eventTenant(c); ok {
		if !a.full {
			// A REDUCED principal: it proved WHICH org it belongs to, so its rows land
			// in that org — but through the projection, so it cannot write revenue,
			// personId, groupId or an arbitrary event kind into a tenant it was
			// invited into for one channel. Same lane, same gates, same receipt as the
			// anonymous caller; only the tenant differs, and the tenant came from a
			// signed claim.
			return publicIngest(c, dec, a.org, source)
		}
		evs, err := dec(c.Body())
		if err != nil {
			return zip.ErrBadRequest("malformed event payload")
		}
		return ingestDecoded(c, a.org, source, evs, 0)
	}
	if presented(c) {
		return zip.ErrForbidden("valid bearer or a resolvable ingest key required")
	}
	return publicIngest(c, dec, publicTenant, source)
}

// eventIngest answers POST /v1/event — the ONE canonical ingestion front door.
// Capability is resolved fail-closed by handle (bearer | pk- | access key ⇒ full;
// nothing ⇒ the anonymous projection); the body is Event | [Event] | {batch}; every
// admitted event flows through the ONE write core into the ONE hanzo.events table,
// tagged source=event.
func eventIngest(s *cloud.Service[state], c *zip.Ctx) error {
	return handle(c, decodeIngest, sourceEvent)
}

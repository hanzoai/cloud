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
// Every ingest route is therefore one line — handle(c, <wire>, <origin tag>) — and no
// route is written by hand at all: doors below declares them and both the router and
// the site-host carve derive from it. One write path, many doors, ONE admission
// decision.

package analytics

import (
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
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
	// subject is the credential's OWN signed identity. It is only consulted on the
	// reduced lane, where it REPLACES the caller-supplied distinctId — see handle. It
	// is empty for the full-capability credentials, which are trusted to attribute
	// their own writes.
	subject string
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
	if isTeamArray(body, i) {
		return decodeTeam(body)
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

// isTeamArray reports whether a bare-array body speaks the team SPA's wire, so
// the ONE canonical decode can carry it: its elements spell snake_case
// `distinct_id` and a NUMERIC epoch-millis `timestamp`, keys the canonical
// Event wire (`distinctId`, `time`) never uses. Probing only element 0's
// top-level keys keeps the dispatch positive-signal-only: a canonical array can
// never be mis-read as team, and a probe miss just falls through to the
// canonical decode exactly as before.
func isTeamArray(body []byte, i int) bool {
	if i >= len(body) || body[i] != '[' {
		return false
	}
	var probe []map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil || len(probe) == 0 {
		return false
	}
	if _, ok := probe[0]["distinct_id"]; ok {
		return true
	}
	ts, ok := probe[0]["timestamp"]
	return ok && len(ts) > 0 && ts[0] >= '0' && ts[0] <= '9'
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
			// invited into for one channel.
			//
			// AND IT DOES NOT NAME THE PERSON. The projection was designed for an
			// ANONYMOUS caller writing to $public, where a forged distinctId is
			// meaningless. Aimed at a REAL org the same field changes meaning: it is
			// the join key every person-level lens groups by, and the team SPA puts the
			// account identifier there, so a guest could attribute pageviews and errors
			// to a named colleague inside the host org. The projection cannot strip it —
			// it is what makes the lane useful — so the fix is to stop taking it from
			// the caller: on this lane the SIGNED account is the identity. A reduced
			// principal does not name the person; its token does.
			return publicIngest(c, dec, a.org, source, a.subject)
		}
		// ONE door, every event kind: the observability plane gets first refusal
		// on the canonical door's authenticated bodies (cloud.ObsEventIngest —
		// the o11y subsystem claims LLM-obs ingestion batches and declines all
		// else). Only the FULL lane offers: obs events are tenant data, so the
		// anonymous and reduced projections never reach that plane.
		if source == sourceEvent {
			if obs := cloud.ObsEventIngest(); obs != nil {
				if accepted, dropped, claimed, err := obs(c.Context(), a.org, c.Body()); claimed {
					if err != nil {
						return zip.ErrInternal("event ingest failed")
					}
					return c.JSON(http.StatusOK, CaptureResult{Accepted: accepted, Dropped: dropped})
				}
			}
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

// door is one ingest door: a PATH bound to the WIRE it speaks. Capability is not a
// field and cannot become one — handle decides it, once, for every door.
//
// decode and wire are the two halves of ONE fact: what this door accepts. decode is
// the half that runs; wire is the half that is PUBLISHED, and it sits here rather
// than in a table of its own so a door cannot be routed with one wire and documented
// with another — the drift that put /v1/tracker in the router and not in the carve.
//
// summary and description are the PROSE half of that same fact, and they live here
// for the same reason: a door is untyped by construction (typed_wire_test.go names
// the blocker), so zipdoc has no doc comment to lift and declare below is the only
// place its prose can be stated. Keeping it on the row means a door added tomorrow
// carries its own account of what it accepts and from whom, rather than inheriting
// one blurb written about a different door.
type door struct {
	path   string
	decode decode
	wire   any // openapi.Register's request declaration; see declare below
	source string

	summary     string
	description string
}

// doors is THE ingest surface: the ONE place a door is declared, and the ONE list
// both consumers derive from. routes (analytics.go) registers exactly these paths;
// installHostCarve hands sites exactly these paths bound to exactly these wires. So
// "what is an ingest door" has a single answer, and the router and the site-host
// carve cannot hold different ones.
//
// They used to, because the answer was written three times — the route table, sites'
// analyticsPaths literal, and a path switch inside the carve — and the copies had
// already drifted: /v1/tracker and /v1/ingest were routed doors that sites did not
// name, so the same beacon was admitted (503, datastore down) on an API host and
// refused (405) on a site host. Nothing decided that; two lists just disagreed.
//
// TWO WIRES, and no more — the canonical one and PostHog's:
//
//   - /v1/event — the canonical door and the canonical wire (Event | [Event] |
//     {batch:[…]} | the team SPA's bare snake_case array, dispatched by shape —
//     isTeamArray), which every current Hanzo client emits. The SAME door also
//     carries LLM-observability ingestion batches: handle offers each
//     authenticated body to the o11y plane's claim first (cloud.ObsEventIngest),
//     which takes only {"batch":[{"type":"trace-create"|…}]} shapes — consumers
//     and shapes behind ONE door, not more doors.
//
//   - /v1/insights/e — the PostHog wire. A second WIRE, not a second name for the
//     first: PostHog SDKs emit this shape and no canonical-wire door can serve them.
//
//     ALMOST NOTHING CALLS THIS PATH DIRECTLY. Its live traffic arrives through the
//     insights-cloud-ingest-rewrite middleware on insights.hanzo.ai (universe
//     infra/k8s/ingress/routes.yaml), which matches EIGHT SDK spellings — /e, /v1/e,
//     /batch, /capture and each one's trailing-slash form, the forms real PostHog
//     SDKs actually send — and replacePath's them all to this one literal. Two things
//     follow. This door must NEVER be sunset on a $source count: its callers do not
//     name it, so $source='posthog' would not decay even after every SDK moved. And
//     if that middleware is dropped or reordered below the catch-all, eight live
//     ingest paths break at once, here, with no change in this repo.
//
//     insights.hanzo.ai is an API host, so those rewritten requests reach the ROUTER
//     (which tolerates a trailing slash) and never the site-host carve — the carve's
//     byte-exact matching is not what holds this door open.
//
// A door is a WIRE, never a NAME. /v1/analytics, /v1/analytics/batch and /v1/tracker
// were three more spellings of the canonical wire already served above, and the ONE
// thing that made them alternatives rather than duplicates — a caller that named them
// — is gone:
//
//   - @hanzo/event (0.3.x) is the client every Hanzo surface now ships, and it posts
//     the canonical door. The SDK it replaced, @hanzo/capture 0.1.1, POSTed
//     /v1/analytics and beaconed /v1/tracker on unload; the fleet holds no importer
//     of it, and its unload beacon had ALREADY stopped landing anywhere — apps/tracker
//     owns /v1/tracker in the app manifest and registers only /v1/tracker/projects/…,
//     so this package's entry for that path sat behind the tracker product's prefix
//     and answered 405 in the fleet while passing its own single-app tests.
//   - the batch alias was kept for "openapi analytics_batch, the generated python SDK,
//     and `hanzo analytics batch`". Those name analytics.hanzo.ai — the standalone
//     collector, whose batch takes an array of SendPayload and answers
//     {size,processed,errors,details}. This package answers CaptureResult, and cloud
//     serves none of that collector's routes (/v1/analytics/heartbeat is 404 here).
//     They were never a contract on THIS door.
//
// BATCH IS A BODY, NOT A PATH — the same reason there is no /v1/event/batch: a JSON
// array, or a {batch:[…]} envelope, IS the batch, and decodeIngest takes both at the
// one door. A second path for a second body shape is a second way to say one thing.
//
// The prefixes stay in the app manifest, because /v1/analytics still carries the READ
// lenses (overview, timeseries, top, health) and /v1/tracker belongs to the tracker
// product. What ends here is this package's claim on them as WRITE paths.
// decodeEvent is the ONE door's decoder. It picks the wire by SNIFFING THE KEYS,
// never by "did the first decoder return anything".
//
// /v1/insights/e used to be a second door for the second wire. A wire is a SHAPE, and
// a shape has never earned a path — decodeIngest already sniffs object-vs-array and
// bare-vs-envelope on this route, so sniffing one more encoding is the mechanism that
// is already here, not a new one. The wire did not go away: the ingress rewrite that
// fed the old door (insights-cloud-ingest-rewrite: insights.hanzo.ai /e,/batch,
// /capture) now replacePaths onto /v1/event.
//
// Trying canonical first and falling back on an empty result is WRONG, and
// TestMount_HostCarve_IngestsForSiteOrg refutes it: decodeIngest ACCEPTS a PostHog
// body as a bare canonical Event and returns ONE event, which is then dropped whole
// downstream (canonicalType("") is "event", not in publicKinds). The caller gets 200
// and the event vanishes. A count of 1 is not evidence the body was understood.
//
// The wires are distinguishable exactly, with no heuristic: canonical spells the field
// `distinctId` (camel) and carries `type`; the PostHog wire spells it `distinct_id`
// (snake) and carries `api_key`. Neither key exists in the other wire, so presence is
// proof. Batches are probed on their elements because `batch` is shared by both.
func isPostHogWire(body []byte) bool {
	var probe struct {
		DistinctID json.RawMessage `json:"distinct_id"`
		APIKey     json.RawMessage `json:"api_key"`
		Batch      []struct {
			DistinctID json.RawMessage `json:"distinct_id"`
		} `json:"batch"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return false
	}
	if probe.DistinctID != nil || probe.APIKey != nil {
		return true
	}
	for _, e := range probe.Batch {
		if e.DistinctID != nil {
			return true
		}
	}
	return false
}

func decodeEvent(body []byte) ([]CaptureEvent, error) {
	if isPostHogWire(body) {
		return decodeInsights(body)
	}
	return decodeIngest(body)
}

var doors = []door{
	{
		path: "/v1/event", decode: decodeEvent, wire: canonicalWire, source: sourceEvent,
		summary: "Capture product events into your org's warehouse",
		description: "Stores pageviews, browser errors, identifies and custom commerce events as rows " +
			"in the caller's own tenant, and answers a receipt {accepted, dropped} that always totals " +
			"what was sent — a beacon is never silently discarded.\n\n" +
			"ONE door for every wire a Hanzo surface emits, dispatched by the SHAPE of the body and " +
			"never by a second path: a bare event object, a bare array of them, the {batch:[…]} / " +
			"{events:[…]} envelope, the team console's snake_case array, and the PostHog wire (spelled " +
			"`distinct_id`/`api_key`, which the canonical wire never uses). BATCH IS A BODY, NOT A " +
			"PATH — there is no /v1/event/batch, because an array already is one.\n\n" +
			"WHAT THE CALLER PRESENTS DECIDES WHAT IT MAY WRITE, and the door itself grants nothing. A " +
			"validated bearer or an org API key writes the full event at full fidelity. A PUBLISHABLE " +
			"key (pk-, on Authorization: Bearer, x-hanzo-ingest-key, or ?ingest_key= for " +
			"navigator.sendBeacon, which cannot set headers) does the same, and is the credential a " +
			"browser bundle ships: it is deliberately NOT a secret, it resolves WHICH tenant a beacon " +
			"belongs to and nothing more. A pk- never authenticates and can READ NOTHING — not this " +
			"org's errors, not a lens, not any other route on this API — so a leaked one lets a " +
			"stranger write into your stream, and never lets one read out of it. Reading these rows " +
			"back always takes a real bearer. A Hanzo Team workspace token resolves its org at " +
			"REDUCED capability: the signed " +
			"account names the person, so a `distinctId` in the body cannot pin events on a colleague.\n\n" +
			"NO CREDENTIAL IS ALSO ADMITTED, and that is the point — a logged-out visitor has none. " +
			"Such a write is PROJECTED: filed under the reserved `$public` tenant, narrowed to " +
			"pageview and error, renamed server-side to $pageview/$error, and stripped to the fields " +
			"the projection names, so revenue, personId, groupId and the whole client property bag " +
			"cannot reach a row. Everything refused is counted in `dropped`. On a published-site host " +
			"the same projection applies with that site's org as the tenant. But a credential that IS " +
			"presented and does NOT resolve is 403, never quietly downgraded: filing a misconfigured " +
			"key's events under $public would hide them in a partition their owner cannot read.\n\n" +
			"The anonymous lane alone is bounded: 413 over 64 KiB, 400 over 50 events, 429 on the " +
			"per-client-IP and per-peer caps, and a DNT:1 or Sec-GPC:1 request stores nothing and says " +
			"so in the receipt. Where a deployment switches anonymous capture off, a credential-less " +
			"write is 403 instead. Authenticated bodies are offered to the observability plane first, " +
			"which claims LLM-observability ingestion batches and declines everything else.",
	},
}

// canonicalWire is what decodeIngest accepts, said in the document's own vocabulary:
// three shapes on one path, so an SDK generated from it can send any of the three a
// real client sends. Declaring only the bare object — the one shape a lone Go type
// could state — would document an ingest API that cannot batch, which is most of
// what @hanzo/event does.
// insightsBody rides here because the door accepts it: one path, four shapes. Leaving
// it out would publish an ingest API that silently accepts a wire it does not document.
var canonicalWire = openapi.OneOf{Event{}, []Event{}, CaptureBatch{}, insightsBody{}}

// declare publishes what every ingest door ACCEPTS, RETURNS and MEANS. These doors
// cannot be typed ops (typed_wire_test.go names each one's wire fact), and an untyped
// route with no declaration publishes an operationId and NOTHING else —
// indistinguishable, to every SDK generator reading the document, from a route that
// takes no body and returns none. That is how the platform's primary ingest door came
// to offer, in every generated SDK, a call with nowhere to put the event.
//
// Schema alone was only half of that: a call with somewhere to put the event and no
// word about what a publishable key may do with it is a door a reader has to guess at.
// Describe is the seam for the other half, and it derives from the SAME rows — a door
// added tomorrow declares its schema and its prose together, or fails the gate in
// doors_test.go rather than silently publishing neither.
//
// The receipt is the SAME for every door and every lane — the anonymous projection,
// the reduced team principal, the full credential and the o11y plane's claim all
// answer CaptureResult (handle/publicIngest/ingestDecoded, above), so one response
// declaration is the whole truth rather than the common case.
func init() {
	for _, d := range doors {
		openapi.Register(d.path, http.MethodPost, d.wire, CaptureResult{})
		openapi.Describe(d.path, http.MethodPost, d.summary, d.description)
	}
	openapi.Register("/v1/analytics/health", http.MethodGet, nil, healthReport{})
	// What "healthy" ASSERTS, stated exactly, because a probe whose prose overclaims
	// is worse than one with none: an operator wires a readiness gate to it and gets a
	// green pod in front of a warehouse that cannot answer a query.
	openapi.Describe("/v1/analytics/health", http.MethodGet,
		"Whether the analytics warehouse is reachable, and which lens tables exist",
		"Reports the analytics subsystem's own liveness: whether the shared warehouse "+
			"connection is up, and — when it is — whether each read lens's table has been "+
			"provisioned yet (the LLM usage ledger and the product-event table, each named in the "+
			"report).\n\n"+
			"IT DOES NOT PROBE THE WAREHOUSE TO EARN ITS 200. `datastore` here is the state of the "+
			"process's own shared client — established, and not since closed — not the result of a "+
			"query, so a warehouse that is accepting connections and failing reads still reports "+
			"healthy. Degraded means only that the client never came up: 503, CARRYING the report "+
			"(status, datastore:false, reason) as its body rather than an error envelope, so a "+
			"readiness gate can read the cause off the same object it got at 200.\n\n"+
			"A MISSING LENS TABLE IS NOT A FAILURE and never moves the status. The per-lens probe "+
			"runs only once connected, and a lens reported available:false answers honest-empty "+
			"rather than erroring — a fresh deployment whose collector has not emitted yet is "+
			"legitimately 200 with the product-event lens unavailable. The lens block is absent "+
			"entirely from a degraded report, which has nothing to say about tables it could not "+
			"reach.\n\n"+
			"Unauthenticated on purpose — liveness has to be probe-able — and it reads NO tenant "+
			"data: table existence only, never a row.")
	// The Sentry error wire (registered in analytics.go's routes, on the same
	// /v1/event door). Its body is an opaque envelope stream the o11y consumer reads
	// itself, so openapi.Binary is the whole truth — no struct describes it, exactly
	// as none describes an upload. Its RESPONSE is deliberately undeclared: the
	// handler relays cloud.ObsErrorIngest verbatim, so this package does not know
	// what comes back and publishing a shape would be inventing one.
	//
	// The prose is PER ROW, not one blurb over both: these are two different Sentry
	// protocol endpoints — the current framed envelope and the legacy single-event
	// store — and a shared description would tell an SDK author the two are aliases,
	// which is the one thing about this pair worth knowing and getting right.
	for _, d := range []struct{ path, summary, description string }{
		{"/v1/event/:project/envelope",
			"Sentry SDK envelope ingest — errors and traces from an unmodified Sentry client",
			"Accepts the CURRENT Sentry wire — the framed envelope a modern SDK posts, carrying its " +
				"items in one request — so an application already instrumented with Sentry reports into " +
				"Hanzo's error tracking by pointing its DSN here and changing nothing else."},
		{"/v1/event/:project/store",
			"Sentry SDK store ingest — the legacy single-event wire",
			"Accepts the LEGACY Sentry wire: one event per request, what an SDK predating envelopes " +
				"sends. Same door, same credential, same destination as the envelope endpoint — kept " +
				"open so an old client reports without being upgraded first. New instrumentation has " +
				"no reason to choose it."},
	} {
		openapi.Register(d.path, http.MethodPost, openapi.Binary{}, nil)
		openapi.Describe(d.path, http.MethodPost, d.summary, d.description+sentryWire)
	}
}

// sentryWire is the half of both Sentry doors' prose that is identical because the
// HANDLER is identical: one relay, one credential, one tenant rule. Stated once so two
// descriptions cannot drift into two accounts of one forward.
//
// The DSN paragraph is the load-bearing one. Every other write in this package is
// reached with a Hanzo credential, so a reader arrives expecting one here too — and
// sending a bearer to this door accomplishes exactly nothing.
const sentryWire = "\n\nCLOUD ROUTES IT AND READS NONE OF IT. The body is relayed byte-for-byte to the " +
	"observability plane, which parses the wire, verifies the credential and answers; this door " +
	"declares no response shape because it does not know one. A deployment with no observability " +
	"plane mounted answers 503.\n\n" +
	"THE CREDENTIAL IS A SENTRY DSN KEY, NOT A HANZO PRINCIPAL. This is one of the few writes on " +
	"the platform that carries no bearer and no org header by design — a Sentry SDK has neither — " +
	"and it is exempt from the principal gate for that reason. The observability plane verifies the " +
	"DSN key itself, fail-closed: a request without a valid one is refused there, never admitted " +
	"here. Presenting a Hanzo bearer instead does nothing.\n\n" +
	"`project` IS THE DSN'S PROJECT ID — the identifier in the DSN the SDK was configured with, and " +
	"what the tenant is derived from. It is NOT a Hanzo IAM project and NOT a tracker project key. " +
	"Only these two ingest paths map through: no observability READ API is reachable by any other " +
	"suffix under this prefix."

// ingest is the door's API-host handler: admission (handle) over the door's wire.
// Capability is resolved fail-closed there — bearer | pk- | access key ⇒ full;
// presented-but-unresolvable ⇒ 403; nothing ⇒ the anonymous projection.
func (d door) ingest(_ *cloud.Service[state], c *zip.Ctx) error {
	return handle(c, d.decode, d.source)
}

// anon is the door's SITE-HOST handler: the anonymous lane directly, with the
// resolved Site's org as the tenant. It does not consult handle because there is
// nothing to consult — sites.Middleware runs before the identity boundary, so no
// credential on a site host has been validated by anything (installHostCarve).
func (d door) anon(org string, c *zip.Ctx) error {
	return publicIngest(c, d.decode, org, d.source)
}

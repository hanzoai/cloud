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

// event.go — the ONE canonical event-ingestion entry point.
//
//	POST /v1/event   body: Event | [Event] | {batch:[…]}   ->  {accepted, dropped}
//
// ONE endpoint, EVERY wire, EVERY auth context. The decoder (decodeIngest) is
// wire-tolerant: a bare canonical Event object, a bare [Event] array, AND the
// CaptureBatch envelope ({batch:[…]} | {events:[…]}) the Segment/beacon/publishable
// paths speak all decode onto the SAME []CaptureEvent the ONE write core
// (ingestEvents) consumes, onto the SAME event plane. There is deliberately
// no /v1/event/batch — a JSON array, or a batch envelope, IS the batch.
//
// CAPABILITY IS DECIDED BY TRUST LEVEL, ONCE, IN ONE PLACE (handle) — never per
// endpoint. An endpoint supplies only its WIRE (a decode) and its origin tag. There is
// no argument by which an endpoint can ASK for full capability, so an endpoint cannot
// forget the decision or route around it: the alternative is not a check to skip, it is
// not expressible. handle reads the credential itself, SERVER-SIDE and FAIL-CLOSED, in
// strict trust order (eventTenant):
//
//  1. a validated IAM bearer principal — its owner org;
//  2. a publishable key (pk-…) — IAM resolves it to its org; it can write but not read
//     (the SAME key publishable.go mints; folded in here so a pk- caller uses
//     /v1/event directly);
//  3. an out-of-band IAM access key (sk-…) — resolved through the ONE key client
//     (cloud.OrgForKey).
//
// One of those resolves ⇒ FULL capability into that credential's org, and that branch of
// handle is the ONLY unprojected write in this package. A caller that PRESENTED one and
// did not resolve ⇒ 403 (a misconfigured key is refused, never downgraded). A caller
// that presented NOTHING is CREDENTIAL-LESS and takes the ANONYMOUS lane (publicIngest,
// public.go) — kind allowlist, field PROJECTION, its own size/rate bounds, DNT — no
// matter which endpoint it arrived at. It rejoins this pipeline at ingestDecoded, so
// decode, write core, and receipt are shared; only capability differs.
//
// There is NO host fallback anywhere: no endpoint turns a request Host into a REAL
// tenant with full capability. The org is NEVER read from the body, on either lane.
//
// The published-site host (installHostCarve, event.go) is the one endpoint that does not
// ask, because on it there is nothing to ask: sites.Middleware runs BEFORE the identity
// boundary (serve.go — sites at 241, IdentityMiddleware at 267), so c.User()/c.Org() are
// still RAW client headers there and no credential has been validated by anything. It
// calls publicIngest DIRECTLY with the resolved Site's org as the anonymous tenant, so a
// site beacon gets the projection and its pageviews still land where the site's owner
// reads them.
//
// Every ingest route is therefore one line — handle(c, <wire>, <origin tag>) — and no
// route is written by hand at all: doors below declares them and both the router and
// the site-host carve derive from it. One write path, many endpoints, ONE admission
// decision.

package event

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	// attr, not attribute: this package already has an attribute() — the function that
	// stamps a signed identity onto a reduced principal's rows (public.go).
	attr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Event is the canonical analytics event — the entire ingest contract in five
// fields. Only these are first-class; everything else a caller wants to record
// travels in Properties (the scrubber runs over it downstream, same as every
// event). The tenant is NOT a field: it is resolved server-side from IAM, so a
// caller can only ever write into its OWN org's partition.
type Event struct {
	Event      string         `json:"event"`      // event name (required; empty ⇒ dropped as unroutable)
	Type       string         `json:"type"`       // canonical kind: pageview | error | identify | group | event (default)
	DistinctID string         `json:"distinctId"` // the person/visitor id the caller owns
	Time       string         `json:"time"`       // optional RFC3339; clamped to server-now on skew/absent
	Properties map[string]any `json:"properties"` // everything non-core
}

// toCapture adapts the canonical Event onto the internal CaptureEvent the write
// core consumes. No $-property is promoted to a column here — every non-core
// field the caller sent stays in Properties.
//
// TYPE IS CARRIED, and it has to be. The kind is half of what the ANONYMOUS lane
// admits on (publicKinds, public.go — the other half is the closed autocapture
// name): canonicalType maps an empty Type to "event", which is NOT an allowlisted
// kind, so an Event that cannot say "pageview" and does not name an autocaptured
// interaction is dropped — with a 200 receipt — every single time. That made two of the
// three shapes this endpoint PUBLISHES (openapi.OneOf{Event, []Event, CaptureBatch})
// totally lossy without a credential while the third worked, which is a document that
// lies to any SDK generated from it. One wire, three spellings, ONE meaning: whatever
// CaptureBatch can express, the bare object and the bare array express too.
func (e Event) toCapture() CaptureEvent {
	return CaptureEvent{
		Event:      e.Event,
		Type:       e.Type,
		DistinctID: e.DistinctID,
		Timestamp:  e.Time,
		Properties: e.Properties,
	}
}

// admission is a RESOLVED credential: the org it names and the capability it carries.
// Capability is a property of the CREDENTIAL, which is why it lives here and not on an
// endpoint — an endpoint still cannot ask for anything. Two levels, because the
// platform mints two kinds of principal:
//
//	full  ⇒ the unprojected write into org. Every API credential, and a space
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
	// project is the site the credential named, when it named one. Only a project
	// key can: it is minted with a project and resolves to nothing else, so this is
	// the one attribution the server can state rather than accept. It REPLACES the
	// caller's `product` on every admitted row (attributeProject). Empty for the
	// org-level credentials — a bearer and an IAM key name an org and no site, and
	// an empty project honestly says "this write names no site".
	project string
	// subject is the credential's OWN signed identity. It is only consulted on the
	// reduced lane, where it REPLACES the caller-supplied distinctId — see handle. It
	// is empty for the full-capability credentials, which are trusted to attribute
	// their own writes.
	subject string
}

// eventTenant resolves the credential for every endpoint — PLUGGABLE auth, FAIL-CLOSED,
// in strict trust order:
//
//  1. a validated IAM bearer principal wins (its owner org), at FULL capability;
//  2. else a presented key on either carrier resolves through keyAdmission — the
//     project that minted it (org AND site), else the org IAM issued it to — at FULL
//     capability;
//  3. else a verified Hanzo Team space token — at FULL capability for a member,
//     and at REDUCED capability for a guest (teamTenant, team.go).
//
// None matches ⇒ (admission{}, false), which handle answers by refusing a presented-
// but-unresolvable credential and otherwise taking the anonymous lane. There is NO
// host fallback on ANY endpoint, so a tenant is only ever IAM, a signed/resolvable
// key, or a signed team claim — never the request Host.
func eventTenant(c *zip.Ctx) (admission, bool) {
	if org, ok := tenant(c); ok {
		return admission{org: org, full: true}, true
	}
	// ONE publishable key, and IAM issues it. A pk- on any ingest-shaped carrier
	// (Bearer, x-hanzo-ingest-key, ?ingest_key= for sendBeacon, which cannot set
	// headers) resolves through the SAME IAM client as every other key. Cloud used
	// to mint and verify its own pk_ under an HMAC of CLOUD_INGEST_KEY_SECRET —
	// a second publishable-key family with its own prefix, secret and mint
	// endpoint, beside the one IAM already owned.
	//
	// Safe only because a pk- no longer authenticates: IdentityFromRequest
	// refuses it, so it attributes a write and never mints a reading principal.
	if key := ingestKey(c); key != "" {
		if a, ok := keyAdmission(c, key); ok {
			return a, true
		}
	}
	if key := projectKey(c); key != "" {
		if a, ok := keyAdmission(c, key); ok {
			return a, true
		}
	}
	// A Hanzo Team space token (HS256 over SERVER_SECRET, org and role in the
	// signed extra) — the credential the team SPA already holds. It is a PLATFORM
	// credential, so it belongs in the trust order rather than on the endpoint that
	// happens to need it, and it therefore works on every endpoint (team.go). It is
	// the ONLY entry that can resolve at reduced capability, because it is the only
	// one the platform issues to a principal weaker than "holds an API key".
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

// keyAdmission resolves ONE presented key, on either carrier, to what it names and
// what it may do. Both carriers call it so they cannot drift into meaning different
// things by the same string.
//
// WHAT the key names is Admit (attribution.go) — both issuers, asked in one place,
// so this endpoint and every other endpoint that admits a key answer the same key
// the same way. All this adds is the capability, which is an event write's question
// and not a key's: a resolved credential writes unprojected into its org.
//
// A project key is the credential a site's own beacon carries, so it also carries
// the property the whole change is for: it stops resolving the moment the project
// stops existing.
func keyAdmission(c *zip.Ctx, key string) (admission, bool) {
	at, ok := Admit(c.Context(), key)
	if !ok {
		return admission{}, false
	}
	return admission{org: at.Org, project: at.Project, full: true}, true
}

// firstNonWS returns the index of the first non-JSON-whitespace byte, or len(body)
// when the body is empty or all whitespace. The four bytes are JSON's insignificant
// whitespace (RFC 8259 §2). The ONE place the ingest decoders skip leading space.
func firstNonWS(body []byte) int {
	for i := range body {
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

// decodeIngest is the ONE wire-tolerant decoder of the canonical endpoint. It accepts
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
	// The PostHog wire is probed FIRST because its batch envelope spells the same
	// `batch` key as the canonical one — routing on the key alone would hand a
	// PostHog batch to the canonical decoder, which cannot see `distinct_id` and
	// yields events with no person and no kind. Shape wins over key.
	if isInsightsWire(body, i) {
		return decodeInsights(body)
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

// isInsightsWire reports whether an OBJECT body speaks the PostHog wire, which
// the canonical decode would otherwise mangle: PostHog spells the person
// `distinct_id` where the canonical wire spells `distinctId`, so a bare PostHog
// event decodes with an EMPTY person and an unnamed kind, and admitPublic then
// drops the whole batch — a silent 200 that stores nothing, the same failure the
// team wire had. The signal is that snake_case key, at the top level or on the
// first batch element; the canonical wire never uses it, and the team wire is a
// bare ARRAY, so the three are disjoint. Probing only element 0 keeps this
// positive-signal-only: a miss falls through to the canonical decode unchanged.
func isInsightsWire(body []byte, i int) bool {
	if i >= len(body) || body[i] != '{' {
		return false
	}
	var probe struct {
		DistinctID json.RawMessage `json:"distinct_id"`
		Batch      []struct {
			DistinctID json.RawMessage `json:"distinct_id"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	if len(probe.DistinctID) > 0 {
		return true
	}
	return len(probe.Batch) > 0 && len(probe.Batch[0].DistinctID) > 0
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
// ONLY thing that differs between endpoints. Handing the pipeline a decoder, rather than
// forking the pipeline per wire, is what lets admission stay a single decision instead
// of one copy per endpoint (which is exactly how the credential-less endpoints drifted).
type decode func([]byte) ([]CaptureEvent, error)

// refusal is what ADMISSION already refused before the write core ever saw it, and what
// that refusal MEANS — two halves of ONE fact, kept together so the receipt can never
// report a count with the wrong reason. Only the PROJECTED lanes produce one; the
// full-capability lane refuses nothing and passes the zero value, which is why its
// behavior is untouched.
//
// why is built ONLY when something was actually refused (publicIngest), so the accepted
// path allocates nothing it did not allocate before.
type refusal struct {
	n   int
	why *zip.HTTPError // answered ONLY when nothing else landed
}

// cannotWrite names why a PROJECTED lane refused an event. The two answers are different
// facts about the caller, not two spellings of one, and getting it wrong sends a caller
// after the wrong fix:
//
//	unsigned ⇒ 401. Nobody vouched for this request. These same events land with a key,
//	           so the missing key is the whole of it.
//	signed   ⇒ 403. A guest's space token RESOLVED — it has a credential and it is
//	           not the problem. What it lacks is capability into an org it was invited
//	           into for one channel. Telling it "key required" would send it to mint a
//	           second key and hit the identical wall.
func cannotWrite(signed bool) *zip.HTTPError {
	if signed {
		return &zip.HTTPError{
			Status: http.StatusForbidden, Code: "insufficient_capability",
			Msg: "no event could be stored: this credential may not write these events",
		}
	}
	return &zip.HTTPError{
		Status: http.StatusUnauthorized, Code: "ingest_key_required",
		Msg: "no event could be attributed: an ingest key is required to write these events",
	}
}

// cannotAttribute names why ADMISSION refused — the wall before cannotWrite's. Same
// two-answer shape and the same reason: the caller's next move differs.
//
//	nothing presented ⇒ 401 ingest_key_required. The one code every client already
//	                    branches on, so a beacon that lost its key reads the same
//	                    whether it never had one or the projection dropped it.
//	presented, unresolved ⇒ 403. It HAS a key; the key names no project. Minting
//	                    another would hit the identical wall, so the fix named is the
//	                    project, not the key.
func cannotAttribute(presented bool) *zip.HTTPError {
	if presented {
		return &zip.HTTPError{
			Status: http.StatusForbidden, Code: "ingest_key_unknown",
			Msg: "this ingest key names no project: create one (POST /v1/projects) and send the key it mints",
		}
	}
	return &zip.HTTPError{
		Status: http.StatusUnauthorized, Code: "ingest_key_required",
		Msg: "no event could be attributed: create a project (POST /v1/projects) and send its key as ?ingest_key= or Authorization: Bearer",
	}
}

// ingestDecoded is the TAIL of the ingest pipeline, and the ONE place it lives: fold
// type:'error' events (foldException) → the ONE write core (ingestEvents) → the honest
// receipt. Every lane ends here, so "what happens to an admitted event" is written
// once. org is the SERVER-resolved tenant; refused is what admission already refused
// upstream (the zero value on the vouched-for lane, so its behavior is unchanged),
// added to the receipt so {accepted,dropped} always totals what the caller sent.
func ingestDecoded(c *zip.Ctx, org, source string, evs []CaptureEvent, refused refusal) error {
	for i := range evs {
		evs[i] = foldException(evs[i])
	}
	res, err := ingestEvents(c.Context(), org, source, evs)
	if err != nil {
		return err
	}
	res.Dropped += refused.n
	return answer(c, org, source, res, refused)
}

// answer is THE receipt, and the ONE place an endpoint's ingest STATUS is decided. Every
// lane reaches it — the anonymous projection, the reduced team principal, the full
// credential, and the o11y plane's claim — so "what the caller is told happened" is
// written once, beside the counts it is derived from.
//
// A 200 MEANT NOTHING, AND THAT IS WHAT MADE IT DANGEROUS. Every wire shape this endpoint
// accepts, posted with no resolvable tenant, answered 200 {"accepted":0,"dropped":1}:
// the projection refuses a kind it cannot name (publicKinds — the anonymous lane stores
// pageviews and errors, and a log, a span and an exception envelope are none of those),
// and the receipt said so in a field nobody parses. A client whose key is absent,
// revoked or mistyped therefore loses 100% of what it sends while every status check
// it has stays green, for as long as nobody reads the body. The counts were never
// wrong. The STATUS was, and the status is what clients and probes actually read.
//
// So the receipt now says what happened in the one field every HTTP client already
// understands, and the rule is exactly "did anything land":
//
//	accepted > 0   ⇒ 200. THE ACCEPTED PATH IS UNCHANGED, including the partial batch:
//	                 some events landing is a success with an honest drop count beside
//	                 it, and a batch is never failed whole for its worst element.
//	nothing sent   ⇒ 200. An empty body drops nothing, so nothing was lost.
//	nothing landed ⇒ 4xx, naming the ONE thing the caller can do about it. Admission's
//	                 refusal wins when there was one (cannotWrite: 401 with no
//	                 credential, 403 for a guest that has one and lacks capability),
//	                 because that is the caller's first wall. Otherwise the caller HAD
//	                 capability and nothing was routable anyway — no name, or a signal
//	                 no writer drains — so the BODY is what has to change: 400.
//
// The reason travels in HTTPError.Code, which is machine-readable and already on the
// wire for every other refusal on this API — an SDK branches on `ingest_key_required`
// without parsing prose.
//
// DNT IS NOT HERE, and must not move here: an opted-out request drops everything and
// still answers 200 (publicIngest). It is the one total drop that is not a failure —
// the client asked to be forgotten and the server obeyed, so there is nothing for the
// caller to fix and nothing for an alert to page on.
func answer(c *zip.Ctx, org, source string, res CaptureResult, refused refusal) error {
	// The admission receipt, counted. Reported HERE rather than at each lane's tail
	// for the same reason the status is decided here: every lane reaches this point,
	// so the pair is emitted once. The pair travels TOGETHER on purpose — the
	// answerable question is a RATIO. "8,000 items were dropped" needs a second
	// series before it means anything; "88% of what was offered was dropped" is an
	// outage on its own, and that exact loss ran unnoticed for months.
	cloud.ObserveIngest(source, res.Accepted, res.Dropped)
	if res.Dropped > 0 {
		observeDropped(c, org, source, refused.n, res.Dropped-refused.n)
	}
	if res.Accepted > 0 || res.Dropped == 0 {
		return c.JSON(http.StatusOK, res)
	}
	if refused.why != nil {
		return refused.why
	}
	return &zip.HTTPError{
		Status: http.StatusBadRequest, Code: "unroutable_events",
		Msg: "no event could be stored: nothing in this body names a landable event",
	}
}

// The ingest-drop instrument. It is resolved LAZILY for the reason metrics_http.go
// documents: the meter provider is installed by the composition root, so binding at
// init would attach every measurement to the no-op provider that exists before it runs
// and discard them while the code looks perfectly instrumented — the exact failure mode
// this counter exists to catch.
//
// CARDINALITY is bounded on all three labels: org is a SERVER-resolved tenant (an IAM
// owner, a resolved key's org, or the $public constant) and never a caller-chosen
// string; source is the endpoint's own origin tag, from the finite doors table; reason
// is two values. Bounded by real orgs × endpoints × 2 — the same envelope
// hanzo_http_requests_total already lives in.
var (
	dropOnce    sync.Once
	dropCounter metric.Int64Counter
)

// observeDropped makes a nonzero drop VISIBLE — the whole defect was that it was not.
// It emits per REASON rather than one total, because the two are different incidents:
// `unattributable` is a fleet of clients writing with no usable credential, and
// `unroutable` is one client sending bodies nothing can store. An alert that cannot
// tell them apart pages the wrong team.
//
// Both a counter and a log line, deliberately: the counter is what an alert rule reads
// (it reaches the telemetry store in-process, via apps/o11y's metrics push), and the
// log line is what names the tenant and endpoint to whoever the alert wakes.
func observeDropped(c *zip.Ctx, org, source string, unattributable, unroutable int) {
	dropOnce.Do(func() {
		dropCounter, _ = otel.Meter("github.com/hanzoai/cloud/apps/event").Int64Counter("hanzo_ingest_dropped_total",
			metric.WithDescription("Events an ingest endpoint received and did not land, by tenant, endpoint origin and reason."))
	})
	for _, d := range []struct {
		n      int
		reason string
	}{{unattributable, "unattributable"}, {unroutable, "unroutable"}} {
		if d.n == 0 {
			continue
		}
		if dropCounter != nil {
			dropCounter.Add(context.Background(), int64(d.n), metric.WithAttributes(
				attr.String("org", org),
				attr.String("source", source),
				attr.String("reason", d.reason),
			))
		}
		c.Log().Warn("ingest dropped events", "org", org, "source", source, "reason", d.reason, "count", d.n)
	}
}

// presented reports whether the request PRESENTED an IDENTIFIABLE credential at
// all, independent of whether it resolved. It picks which refusal handle answers:
// 403 (you sent one and it is broken) or 401 (you sent none — here is what to get).
// It names exactly the carriers eventTenant consults, so the two cannot disagree
// about what "presented" means.
//
// A key is identifiable by PREFIX (pk-/sk-) and a team token by STRUCTURE (an
// `account` claim an IAM token lacks), so a failure to resolve is decidably a
// misconfiguration. An arbitrary Bearer JWT is not distinguishable from one minted
// for another audience — IdentityMiddleware itself declines to 401 it — so it
// reads as "presented nothing", and its caller is told to get a key rather than
// that its key is broken.
func presented(c *zip.Ctx) bool {
	return ingestKey(c) != "" || projectKey(c) != "" || bearerAPIKey(c) || teamPresented(c)
}

// bearerAPIKey reports whether Authorization carries an opaque platform key. It
// reads cloud.APIKeyPrefixes — THE authority (auth_identity.go) — rather than
// spelling the prefixes again, so widening the key family cannot leave this
// predicate behind.
func bearerAPIKey(c *zip.Ctx) bool {
	tok := teamBearer(c.Header("Authorization"))
	if tok == "" {
		return false
	}
	for _, p := range cloud.APIKeyPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

// handle is THE ingest pipeline and the ONE place in this package where trust level is
// decided. Every /v1 ingest endpoint is one call to it; the endpoint contributes its
// WIRE and its origin tag and NOTHING ELSE — capability is not a parameter, so no
// endpoint can grant itself full capability, and an endpoint added tomorrow inherits
// this decision by construction rather than by remembering to copy it.
//
//	credential resolves     ⇒ FULL capability into THAT credential's org, and into
//	                          the site it named when it named one.
//	credential presented,
//	  does not resolve      ⇒ 403. Never downgraded: filing a misconfigured key's
//	                          events under a reserved tenant would hide them in a
//	                          partition its owner cannot read — a silent failure worse
//	                          than the refusal.
//	nothing presented       ⇒ 401, naming the key to get and where to put it.
//
// THERE IS NO ANONYMOUS LANE. A keyless beacon used to be ACCEPTED into a reserved
// `$public` tenant and answered {"accepted":1} — an org could not read those rows,
// so every such caller lost everything it sent while every status check it had
// stayed green. Three first-party properties shipped keyless without one failed
// build, and a fleet-wide outage answered 200 for two days. A 200 that discards
// data is worse than a 4xx, so the lane is gone rather than gated: attribution is
// the key, a project mints one at create, and a write nobody can attribute is
// refused in the one field every client already reads.
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
		// THE ENDPOINT RUNS ITS OWN WIRE, and there is no longer anything in front
		// of it. Every authenticated body used to be offered to the observability
		// plane first (plane op obs_event_claim) so an LLM-observability batch
		// could be filed in its own store instead of the product warehouse. That
		// sink is deleted: it inserted UNQUALIFIED `traces`/`observations`/
		// `scores` over a DSN naming no database, so the names resolved to
		// `default` — where no migration in this platform has ever created them,
		// and where the datastore's query log records no such INSERT, ever. The
		// concept it claimed is served twice over already: LLM observability is
		// READ off gen_ai spans in event.span by the o11y runtime, and the eval
		// product owns the grounded projections (apps/eval/telemetry.go).
		//
		// So the claim could only ever decline, at the cost of a synchronous
		// cross-process round-trip per event on the fleet's busiest endpoint.
		//
		// IT IS ALSO ONE FEWER LANE REACHING `answer`, and the receipt is the
		// same either way. An LLM-obs-shaped body names no event kind, so the
		// canonical decode yields nothing routable, and a caller holding a full
		// credential that stored nothing is told 400 — which is exactly what the
		// claim's own branch was made to answer. The lane that used to be
		// exempt from the honest receipt is now the ordinary path through it.
		evs, err := dec(c.Body())
		if err != nil {
			return zip.ErrBadRequest("malformed event payload")
		}
		return ingestDecoded(c, a.org, source, attributeProject(evs, a.project), refusal{})
	}
	return cannotAttribute(presented(c))
}

// door is one ingest endpoint: a PATH bound to the WIRE it speaks. Capability is not a
// field and cannot become one — handle decides it, once, for every endpoint.
//
// decode and wire are the two halves of ONE fact: what this endpoint accepts. decode is
// the half that runs; wire is the half that is PUBLISHED, and it sits here rather than
// in a table of its own so an endpoint cannot be routed with one wire and documented
// with another — the drift that put /v1/todo in the router and not in the carve.
//
// summary and description are the PROSE half of that same fact, and they live here
// for the same reason: an endpoint is untyped by construction (typed_wire_test.go names
// the blocker), so zipdoc has no doc comment to lift and declare below is the only
// place its prose can be stated. Keeping it on the row means an endpoint added tomorrow
// carries its own account of what it accepts and from whom, rather than inheriting
// one blurb written about a different endpoint.
type door struct {
	path   string
	decode decode
	wire   any // openapi.Register's request declaration; see declare below
	source string

	summary     string
	description string
}

// doors is THE ingest surface: the ONE place an endpoint is declared, and the ONE list
// both consumers derive from. routes (event.go) registers exactly these paths;
// installHostCarve hands sites exactly these paths bound to exactly these wires. So
// "what is an ingest endpoint" has a single answer, and the router and the site-host
// carve cannot hold different ones.
//
// They used to, because the answer was written three times — the route table, sites'
// analyticsPaths literal, and a path switch inside the carve — and the copies had
// already drifted: /v1/todo and /v1/ingest were routed endpoints that sites did not
// name, so the same beacon was admitted (503, datastore down) on an API host and
// refused (405) on a site host. Nothing decided that; two lists just disagreed.
//
// TWO WIRES, and no more — the canonical one and PostHog's:
//
//   - /v1/event — the canonical endpoint and the canonical wire (Event | [Event] |
//     {batch:[…]} | the team SPA's bare snake_case array, dispatched by shape —
//     isTeamArray), which every current Hanzo client emits. Nothing gets first
//     refusal on it: the o11y plane's claim on this endpoint (obs_event_claim) is
//     retired with the sink behind it, so the wire the shape selects is the wire
//     that runs — consumers and shapes behind ONE endpoint, not more endpoints.
//
//   - the PostHog wire has NO endpoint of its own: decodeIngest dispatches it by
//     shape (isInsightsWire), so PostHog SDKs land on /v1/event like everything
//     else. Its rows carry $source='event' now — the endpoint they actually arrived
//     on — because the path that used to name them is gone.
//
//     ALMOST NOTHING CALLS THIS PATH DIRECTLY. Its live traffic arrives through the
//     insights-cloud-ingest-rewrite middleware on insights.hanzo.ai (universe
//     infra/k8s/ingress/routes.yaml), which matches EIGHT SDK spellings — /e, /v1/e,
//     /batch, /capture and each one's trailing-slash form, the forms real PostHog
//     SDKs actually send — and replacePath's them all to this one literal. Two things
//     follow. This endpoint must NEVER be sunset on a $source count: its callers do not
//     name it, so $source='posthog' would not decay even after every SDK moved. And
//     if that middleware is dropped or reordered below the catch-all, eight live
//     ingest paths break at once, here, with no change in this repo.
//
//     insights.hanzo.ai is an API host, so those rewritten requests reach the ROUTER
//     (which tolerates a trailing slash) and never the site-host carve — the carve's
//     byte-exact matching is not what keeps this endpoint reachable.
//
// An endpoint is a WIRE, never a NAME. /v1/event, /v1/event/batch and /v1/todo
// were three more spellings of the canonical wire already served above, and the ONE
// thing that made them alternatives rather than duplicates — a caller that named them
// — is gone:
//
//   - @hanzo/event (0.3.x) is the client every Hanzo surface now ships, and it posts
//     the canonical endpoint. The SDK it replaced, @hanzo/capture 0.1.1, POSTed
//     /v1/event and beaconed /v1/todo on unload; the fleet holds no importer
//     of it, and its unload beacon had ALREADY stopped landing anywhere — apps/todo
//     owns /v1/todo in the app manifest and registers only /v1/todo/projects/…,
//     so this package's entry for that path sat behind the todo product's prefix
//     and answered 405 in the fleet while passing its own single-app tests.
//   - the batch alias was kept for "openapi analytics_batch, the generated python SDK,
//     and `hanzo analytics batch`". Those name event.hanzo.ai — the standalone
//     collector, whose batch takes an array of SendPayload and answers
//     {size,processed,errors,details}. This package answers CaptureResult, and cloud
//     serves none of that collector's routes (/v1/event/heartbeat is 404 here).
//     They were never a contract on THIS endpoint.
//
// BATCH IS A BODY, NOT A PATH — the same reason there is no /v1/event/batch: a JSON
// array, or a {batch:[…]} envelope, IS the batch, and decodeIngest takes both at the
// one endpoint. A second path for a second body shape is a second way to say one thing.
//
// The prefixes stay in the app manifest, because /v1/event still carries the READ
// lenses (overview, timeseries, top, health) and /v1/todo belongs to the todo
// product. What ends here is this package's claim on them as WRITE paths.
// decodeEvent is the ONE endpoint's decoder. It picks the wire by SNIFFING THE KEYS,
// never by "did the first decoder return anything".
//
// /v1/event/insights/e used to be a second endpoint for the second wire. A wire is a
// SHAPE, and a shape has never earned a path — decodeIngest already sniffs
// object-vs-array and bare-vs-envelope on this route, so sniffing one more encoding is
// the mechanism that is already here, not a new one. The wire did not go away: the
// ingress rewrite that fed the old endpoint (insights-cloud-ingest-rewrite:
// insights.hanzo.ai /e,/batch, /capture) now replacePaths onto /v1/event.
//
// Trying canonical first and falling back on an empty result is WRONG, and
// TestPostHogWireRidesTheCanonicalDoor (obs_door_test.go) refutes it: decodeIngest
// ACCEPTS a PostHog body as a bare canonical Event and returns ONE event, which is
// then dropped whole downstream (canonicalType("") is "event", which is not an
// allowlisted kind, and $pageview is not an autocapture name — it is a KIND). The
// caller gets 200 and the event vanishes. A count of 1 is not evidence the body was
// understood.
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

// peerO11y is the observability peer this package asks over the plane socket.
// The ONE thing it is asked for is the Sentry relay (obs_error_post, whose
// budget is obsErrorTimeout in event.go): the Sentry wire's project segment
// is variable, so analytics has to own the route while o11y owns the runtime.
const peerO11y = "o11y"

var doors = []door{
	{
		path: "/v1/event", decode: decodeEvent, wire: canonicalWire, source: sourceEvent,
		summary: "Capture product events into your org's warehouse",
		description: "Stores pageviews, browser errors, identifies and custom commerce events as rows " +
			"in the caller's own tenant, and answers a receipt {accepted, dropped} that always totals " +
			"what was sent — a beacon is never silently discarded.\n\n" +
			"THE STATUS SAYS WHETHER ANYTHING LANDED, so a green check can never mean an empty " +
			"warehouse. 200 means at least one event was stored (or that nothing was sent), and a " +
			"nonzero `dropped` beside a nonzero `accepted` is a PARTIAL batch, never a failed one — a " +
			"batch is not refused whole for its worst element. If NOTHING was stored the request is an " +
			"error, and it names the one thing that fixes it: 401 `ingest_key_required` when every " +
			"event was refused for want of a credential (the same events land with a key), and 400 " +
			"`unroutable_events` when the caller HAD capability and the body still named nothing " +
			"storable.\n\n" +
			"ONE endpoint for every wire a Hanzo surface emits, dispatched by the SHAPE of the body and " +
			"never by a second path: a bare event object, a bare array of them, the {batch:[…]} / " +
			"{events:[…]} envelope, the team console's snake_case array, and the PostHog wire (spelled " +
			"`distinct_id`/`api_key`, which the canonical wire never uses). BATCH IS A BODY, NOT A " +
			"PATH — there is no /v1/event/batch, because an array already is one.\n\n" +
			"WHAT THE CALLER PRESENTS DECIDES WHAT IT MAY WRITE, and the endpoint itself grants nothing. A " +
			"validated bearer or an org API key writes the full event at full fidelity. A PUBLISHABLE " +
			"key (pk-, on Authorization: Bearer, x-hanzo-ingest-key, or ?ingest_key= for " +
			"navigator.sendBeacon, which cannot set headers) does the same, and is the credential a " +
			"browser bundle ships: it is deliberately NOT a secret, it resolves WHICH tenant a beacon " +
			"belongs to and nothing more. A pk- never authenticates and can READ NOTHING — not this " +
			"org's errors, not a lens, not any other route on this API — so a leaked one lets a " +
			"stranger write into your stream, and never lets one read out of it. Reading these rows " +
			"back always takes a real bearer. A Hanzo Team space token resolves its org at " +
			"REDUCED capability: the signed " +
			"account names the person, so a `distinctId` in the body cannot pin events on a colleague.\n\n" +
			"NO CREDENTIAL IS REFUSED: a write the server cannot attribute to a project is 401 " +
			"`ingest_key_required`, and a credential that IS presented but resolves to no project is " +
			"403 `ingest_key_unknown`. Nothing is filed under a shared tenant — events nobody can " +
			"read are worse than events nobody sent, because the caller is told it succeeded. A " +
			"browser bundle therefore always ships a pk-, which is what /v1/event/tag.js takes.\n\n" +
			"A REDUCED principal — a Hanzo Team space token — writes through the PROJECTION into " +
			"its own org: narrowed to what the SERVER can name (pageviews and errors, plus the closed " +
			"autocapture vocabulary $click, $input, $change, $submit, $view), where every one of those " +
			"names is resolved through a server-owned table and stored as that table's value, so the " +
			"name on the wire is never the name in the row. Stripped, too, to the fields the projection " +
			"names, so revenue, personId, groupId and every property but the element annotation cannot " +
			"reach a row — and an exception is carried only on an error, never on an interaction, so a " +
			"click cannot ship a stack trace into a row's attributes. It does NOT name the person: the " +
			"signed account is the identity, so a `distinctId` in the body cannot pin events on a " +
			"colleague. Everything refused is counted in `dropped`.\n\n" +
			"The projected lane alone is bounded: 413 over 64 KiB, 400 over 50 events, 429 on the " +
			"per-client-IP and per-peer caps, and a DNT:1 or Sec-GPC:1 request stores nothing and says " +
			"so in the receipt. Two stored values carry their own bounds on top, because a request cap " +
			"does not bound one value: an element annotation over 2 KiB (or a trail over 32 steps) and " +
			"an exception class over 256 bytes are dropped from the row, which still lands. " +
			"Authenticated bodies are offered to the observability plane first, " +
			"which claims LLM-observability ingestion batches and declines everything else.",
	},
}

// canonicalWire is what decodeIngest accepts, said in the document's own vocabulary:
// three shapes on one path, so an SDK generated from it can send any of the three a
// real client sends. Declaring only the bare object — the one shape a lone Go type
// could state — would document an ingest API that cannot batch, which is most of
// what @hanzo/event does.
// insightsBody rides here because the endpoint accepts it: one path, four shapes. Leaving
// it out would publish an ingest API that silently accepts a wire it does not document.
var canonicalWire = openapi.OneOf{Event{}, []Event{}, CaptureBatch{}, insightsBody{}}

// declare publishes what every ingest endpoint ACCEPTS, RETURNS and MEANS. These
// endpoints cannot be typed ops (typed_wire_test.go names each one's wire fact), and an
// untyped route with no declaration publishes an operationId and NOTHING else —
// indistinguishable, to every SDK generator reading the document, from a route that
// takes no body and returns none. That is how the platform's primary ingest endpoint
// came to offer, in every generated SDK, a call with nowhere to put the event.
//
// Schema alone was only half of that: a call with somewhere to put the event and no
// word about what a publishable key may do with it is an endpoint a reader has to
// guess at. Describe is the client for the other half, and it derives from the SAME
// rows — an endpoint added tomorrow declares its schema and its prose together, or
// fails the gate in doors_test.go rather than silently publishing neither.
//
// The receipt is the SAME for every endpoint and every lane — the anonymous projection,
// the reduced team principal, the full credential and the o11y plane's claim all
// answer CaptureResult (handle/publicIngest/ingestDecoded, above), so one response
// declaration is the whole truth rather than the common case.
func init() {
	for _, d := range doors {
		openapi.Register(d.path, http.MethodPost, d.wire, CaptureResult{})
		openapi.Describe(d.path, http.MethodPost, d.summary, d.description)
	}
	// The Sentry error wire (registered in event.go's routes, on the same
	// /v1/event endpoint). Its body is an opaque envelope stream the o11y consumer reads
	// itself, so openapi.Binary is the whole truth — no struct describes it, exactly
	// as none describes an upload. Its RESPONSE is deliberately undeclared: the
	// handler relays the o11y plane op obs_error_post verbatim, so this package does not know
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
				"sends. Same handler, same credential, same destination as the envelope endpoint — kept " +
				"open so an old client reports without being upgraded first. New instrumentation has " +
				"no reason to choose it."},
	} {
		openapi.Register(d.path, http.MethodPost, openapi.Binary{}, nil)
		openapi.Describe(d.path, http.MethodPost, d.summary, d.description+sentryWire)
	}
	// The session-replay snapshot endpoint (replay.go). It is not a `doors` row — its
	// body is not the canonical wire and it lands no warehouse row — so it declares
	// itself here beside the other route on this surface that is registered by hand.
	// Its RESPONSE is the same CaptureResult every endpoint answers, because the receipt
	// is the one thing every write on this surface does share.
	openapi.Register(replayPath, http.MethodPost, replayBody{}, CaptureResult{})
	openapi.Describe(replayPath, http.MethodPost,
		"Record a session-replay snapshot batch",
		"Accepts a batch of rrweb events from a browser recorder and hands it to the session-replay "+
			"pipeline, which stores the recording and derives the session summary a player reads back.\n\n"+
			"ONE REQUEST IS ONE BATCH, and it is all-or-nothing: the recording is made durable before "+
			"this answers, so a 200 {\"accepted\":1} means stored and never \"buffered somewhere\". "+
			"There is no partial count, because a half-written recording is not a recording.\n\n"+
			"`sessionId` is REQUIRED and bounded — at most 70 characters of ASCII letters, digits or "+
			"'-'. It is the key every batch of one visit is grouped and ordered by, so an id outside "+
			"that grammar is refused 400 here rather than accepted and dropped further down. "+
			"`windowId` separates two tabs of one session and `distinctId` attributes the recording to "+
			"a person; both are optional. `events` is the rrweb batch, each element a raw eventWithTime "+
			"object, carried VERBATIM — the summary (click, keypress and mouse-activity counts, size) "+
			"is derived downstream from exactly these bytes, so nothing is re-encoded or dropped.\n\n"+
			"THE CALLER'S CREDENTIAL DECIDES THE TENANT, and the body never does: the recording lands "+
			"in the org the presented credential resolves to. It takes the SAME credentials as "+
			"/v1/event — a validated bearer, an org API key, or a publishable pk- key on "+
			"Authorization: Bearer, x-hanzo-ingest-key or ?ingest_key= — so a browser bundle already "+
			"holding a pk- for events needs nothing new to record. A caller that presents nothing is "+
			"401 `ingest_key_required`; one whose key resolves to no project is 403 "+
			"`ingest_key_unknown`; a reduced principal (a Hanzo Team space token) is 403 "+
			"`insufficient_capability`, because a full-fidelity screen recording has no projected form "+
			"that is safe for a guest to write into a host org.\n\n"+
			"BOUNDS: 413 over 512 KiB of body, and that is the only bound on one batch — a recorder is "+
			"expected to chunk a long session rather than send it whole, and the cap is the size one "+
			"message can carry rather than an arbitrary number. 503 when the pipeline cannot take the "+
			"batch: honest unavailability the caller can retry, never a 200 over a discarded "+
			"recording.")
}

// sentryWire is the half of both Sentry endpoints' prose that is identical because the
// HANDLER is identical: one relay, one credential, one tenant rule. Stated once so two
// descriptions cannot drift into two accounts of one forward.
//
// The DSN paragraph is the load-bearing one. Every other write in this package is
// reached with a Hanzo credential, so a reader arrives expecting one here too — and
// sending a bearer to this endpoint accomplishes exactly nothing.
const sentryWire = "\n\nCLOUD ROUTES IT AND READS NONE OF IT. The body is relayed byte-for-byte to the " +
	"observability plane, which parses the wire, verifies the credential and answers; this endpoint " +
	"declares no response shape because it does not know one. A deployment with no observability " +
	"plane mounted answers 503.\n\n" +
	"THE CREDENTIAL IS A SENTRY DSN KEY, NOT A HANZO PRINCIPAL. This is one of the few writes on " +
	"the platform that carries no bearer and no org header by design — a Sentry SDK has neither — " +
	"and it is exempt from the principal gate for that reason. The observability plane verifies the " +
	"DSN key itself, fail-closed: a request without a valid one is refused there, never admitted " +
	"here. Presenting a Hanzo bearer instead does nothing.\n\n" +
	"`project` IS THE DSN'S PROJECT ID — the identifier in the DSN the SDK was configured with, and " +
	"what the tenant is derived from. It is NOT a Hanzo IAM project and NOT a todo project key. " +
	"Only these two ingest paths map through: no observability READ API is reachable by any other " +
	"suffix under this prefix."

// ingest is the endpoint's API-host handler: admission (handle) over the endpoint's wire.
// Capability is resolved fail-closed there — bearer | pk- | access key ⇒ full;
// presented-but-unresolvable ⇒ 403; nothing ⇒ the anonymous projection.
func (d door) ingest(_ *cloud.Service[state], c *zip.Ctx) error {
	return handle(c, d.decode, d.source)
}

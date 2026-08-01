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

// public.go — the ANONYMOUS lane. EVERY credential-less write in this package lands
// here, whichever door it arrived at.
//
// A logged-out visitor on a marketing surface, and a visitor on a customer's published
// site, carry no bearer and no key, so eventTenant resolves nothing. This file is the
// lane such a request falls through to, so a pageview or a browser error from a
// logged-out page lands in the warehouse instead of being refused.
//
// Anonymous input is attested by nobody. It is therefore admitted under a policy the
// vouched-for lane never applies, and the two lanes are SEPARATE FUNCTIONS rather
// than one function with a mode flag: handle's credentialed branch is untouched by
// anything in this file, and every restriction below is unreachable from it.
//
// The policy is in two pieces and no more: the request-scoped gates (publicIngest)
// and the pure decision (admitPublic).
//
//   - CAPABILITY is decided by TRUST LEVEL, not by door. There is exactly one caller
//     shape for this lane and no way to reach the write core at full capability
//     without a credential: the functions that used to take an org and write
//     unprojected (ingestBody / eventWithOrg / captureWithOrg / insightsWithOrg) are
//     gone, so a door that resolved a tenant from a request Host has nowhere else to
//     go. That is the isolation proof — not a check that can be bypassed, but an
//     argument list that cannot express the alternative.
//   - TENANT is the DOOR's, and admitPublic cannot influence it: the projection takes
//     no *zip.Ctx and no org at all, so no header, query, or body field reaches
//     attribution. There are exactly two anonymous tenants, both server-side:
//     publicTenant (the compile-time constant every /v1 door passes) and the resolved
//     Site's org on a published-site host, which is the SAME host-derived tenant the
//     file plane and the Base carve already serve that host's bytes under.
//   - WHAT MAY BE STORED is ONE rule: THE SERVER NAMES THE ROW. An anonymous event is
//     admitted only when its stored name comes from THIS FILE and not from the caller's
//     bytes (publicName). That is what keeps the anonymous name space closed — an
//     unattested caller can introduce neither a new name into the read lenses nor
//     unbounded cardinality into the warehouse's keys. Two families satisfy the rule:
//
//     KIND — pageview and error (publicKinds), whose name the ROUTE derives
//     (resolveName ⇒ page_viewed / error). The caller's own `event` string is dropped
//     on the way through, so the body cannot reach the name at all.
//
//     NAME — the closed autocapture vocabulary carried by the `event` kind
//     (publicNames: $click, $input, $change, $submit, $view). An interaction's whole
//     identity IS its name — strip it and there is no event — so the kind allowlist
//     alone could not admit one. The name is instead RESOLVED through a server-owned
//     table, and the TABLE'S VALUE is what gets stored.
//
//     `identify` and `group` (which name a person and a group) and every OTHER custom
//     event — the whole commerce/billing/metering surface — are dropped, counted in the
//     honest receipt, never stored.
//
//     THE KIND HALF WAS UNTRUE UNTIL RECENTLY, and it is worth saying why rather than
//     just asserting the rule again. resolveName is shared with the credentialed lane,
//     and its error branch fell back to naming a row after the caller's exception class.
//     So an anonymous `{"type":"error","error":{"type":"…"}}` put 60 KiB of chosen bytes
//     into `name`, fifty distinct per request, and on a published-site host into a REAL
//     org — while this comment claimed it could not. The fix is in resolveName, because
//     the class was never `name`'s fact to hold; a special case here would have left the
//     lane with a rule and an exception instead of a rule.
//   - FIELDS are a PROJECTION, not a filter: admitPublic builds a fresh CaptureEvent
//     from the fields it names, so a field it does not name — personId, groupId,
//     revenue, productId, quantity, currency, refCode, channel, signupWeek — cannot
//     reach the row. The property bag is projected the SAME way and by the same
//     argument (publicProps): the @hanzo/observe annotation and NOTHING else. So an
//     anonymous row's attributes still hold exactly what the SERVER put there — the
//     folded $exception and the write core's $source.
//     A FIELD IS ALSO PROJECTED BY FAMILY, not only by name: an exception is carried on
//     the error kind and nowhere else (publicException). It has to be, because the
//     projection runs BEFORE the fold — foldException stamps `attributes['$exception']`
//     on whatever ingestDecoded hands it — so carrying Error onto a `$click` put the
//     caller's message and stack, measured at 32 KiB, into an interaction row's
//     attributes dictionary. The invariant two bullets up says an anonymous row's
//     attributes hold only what the SERVER put there; this is the other half of making
//     that true.
//   - BYTES and COUNT are bounded first, and REFUSED rather than truncated. Bounding the
//     REQUEST is not bounding a stored VALUE, so the two fields an anonymous caller can
//     spend a whole request on — the annotation and the exception's class — carry their
//     own bounds (publicProps, publicException). Both are read off what the real client
//     emits, and both DROP the offending field rather than clip it.
//   - IDENTITY is NAMESPACED, because nobody signed for it (publicSubject). The ids an
//     anonymous caller supplies are its own browser's, and they are stored under a
//     reserved prefix that no identified subject can carry — so an unattested row can
//     never join, in any lens, to a person the org actually knows.
//   - RATE is capped per client IP and, independently, per socket peer.
//   - DNT / Sec-GPC on the wire is honored: nothing is stored and the receipt says so.
//
// Everything admitted here flows through the SAME ONE write core (ingestEvents)
// onto the SAME event plane. One write path; this file only decides what a caller
// nobody vouched for may put on it.

package analytics

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// publicTenant is the reserved tenant every /v1 door attributes anonymous events to.
// The '$' prefix is load-bearing: an IAM org slug is lowercase ASCII alphanumerics and
// '-' (the IAM slugifier emits nothing else), so this value lies outside the org
// namespace and cannot collide with a real tenant. It is also the reason the anonymous
// stream is legible: on an API host a row's tenant_id alone says whether IAM vouched
// for it.
//
// It is a CONSTANT and not a fallback: nothing derives it from the request. The one
// door that passes a different anonymous tenant is the published-site host, which
// passes the org the site resolver returned for that host, so a customer's own site
// analytics keep landing in the customer's org — under this same projection.
const publicTenant = "$public"

// maxPublicBytes / maxPublicBatch bound ONE anonymous request. @hanzo/event's default
// batchSize is 20 and it also drains the queue on page-unload, so 50 leaves real
// headroom; 64 KiB is the browser's own sendBeacon ceiling, which makes it the honest
// cap for the transport that reaches here. Over either bound is REFUSED, never
// truncated — a silent truncation would make the receipt a lie.
const (
	maxPublicBytes = 64 << 10
	maxPublicBatch = 50
)

// publicKinds is the ALLOWLIST of canonical kinds (canonicalType's closed set) whose
// name the ROUTE derives, so an anonymous caller may store one whatever its body says:
// admitPublic drops the caller's `event` string and resolveName supplies page_viewed /
// error. `identify` and `group` bind an event to a named person and a named group, and
// are refused here for that reason.
//
// Delegating the name to resolveName is only sound because resolveName has no
// caller-bytes path left. It HAD one: its error branch fell back to e.Error.Type, so this
// table admitted a kind whose name an anonymous caller then chose — 60 KiB of it, fifty
// distinct per request, in a real org on a published-site host. That is fixed where it
// was wrong (fact.go) rather than worked around here, so this stays a plain allowlist and
// the lane keeps ONE name rule instead of a rule and an exception.
var publicKinds = map[string]bool{"pageview": true, "error": true}

// publicNames is the other half of the same rule, for the ONE kind whose name is
// load-bearing. An autocaptured interaction arrives as the `event` kind, and its entire
// identity is its name — drop the name and there is no event at all, because
// resolveName returns "" for an unnamed track and the write core discards it. So the
// kind allowlist could never carry one: `event` as a KIND is the whole custom
// commerce/billing/metering surface, and that stays refused.
//
// The set is CLOSED, and it is the client's own reserved vocabulary rather than a
// server invention: @hanzo/observe derives an interaction's name from its kind
// (observer.ts NAME) and emits exactly these five through capture(). Its sixth, nav,
// calls pageview() and arrives as the pageview KIND — which is why $pageview is not a
// name here. It would be a second way to say the first thing.
//
// THE MAP'S VALUE IS WHAT GETS STORED, and that is the security property. The lookup
// folds case and space (the same fold canonicalType already applies to the sibling
// field), so what lands in `name` is a constant declared HERE and never the caller's
// bytes: an anonymous caller can introduce neither a new name nor a second SPELLING of
// an admitted one — $Click and $click are one name, not two. Adding an entry here, or a
// kind above, is the ONLY way to widen the anonymous surface.
var publicNames = map[string]string{
	"$click":  "$click",
	"$input":  "$input",
	"$change": "$change",
	"$submit": "$submit",
	"$view":   "$view",
}

// publicRateWindow, publicRateLimit and publicPeerRateLimit cap anonymous ingest.
// TWO independent buckets, because neither key alone suffices:
//
//   - the CLIENT IP (leftmost X-Forwarded-For, via cloud.ClientIP) is the real
//     per-visitor key at the edge, but it is a header, so a caller reaching the pod
//     directly can rotate it and reset its own bucket at will;
//   - the SOCKET PEER cannot be rotated, but at the edge it is the ingress for ALL
//     public traffic, so it can only carry a total-volume ceiling, never a per-visitor
//     cap.
//
// Together they bound both a single source and the aggregate. Sized so real marketing
// traffic never notices: a browser emits a handful of events per page load, so 300/min
// per client is orders of magnitude of headroom, while 6000/min is the pod's anonymous
// ingest ceiling. Per-pod and in-memory — a per-replica soft cap, which is what a
// flood control needs to be, not a distributed quota.
const (
	publicRateWindow    = time.Minute
	publicRateLimit     = 300
	publicPeerRateLimit = 6000
)

// counter is a fixed-window request counter keyed on a caller string, with
// opportunistic eviction so the map stays bounded at the edge's IP cardinality. This
// is deliberately a counter and not the zip token-bucket primitive for the same reason
// cloud's edge limiter is not: that primitive never evicts, which is fine for a
// bounded per-org keyspace and unbounded growth when keyed on raw IPs.
type counter struct {
	limit  int
	window time.Duration

	mu    sync.Mutex
	seen  map[string]*tally
	swept time.Time
}

// tally is one key's window: how many requests it has spent, and when it resets.
type tally struct {
	n     int
	reset time.Time
}

func newCounter(limit int, window time.Duration) *counter {
	return &counter{limit: limit, window: window, seen: map[string]*tally{}}
}

// ok charges one request to key and reports whether it is within the window's limit.
// A request over the limit is still charged, so a caller cannot ride a rejected
// request for free.
func (c *counter) ok(key string) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.swept) >= c.window {
		c.swept = now
		for k, t := range c.seen {
			if now.After(t.reset) {
				delete(c.seen, k)
			}
		}
	}
	t := c.seen[key]
	if t == nil || now.After(t.reset) {
		t = &tally{reset: now.Add(c.window)}
		c.seen[key] = t
	}
	t.n++
	return t.n <= c.limit
}

// publicRate / publicPeerRate are the two anonymous-ingest buckets. Package-level
// because the cap is a property of the pod, which outlives any request. Vars, not
// consts, so a test can install a tighter pair.
var (
	publicRate     = newCounter(publicRateLimit, publicRateWindow)
	publicPeerRate = newCounter(publicPeerRateLimit, publicRateWindow)
)

// publicRateOK charges one anonymous request against BOTH buckets and reports whether
// it may proceed. An unkeyable caller shares one bucket rather than being exempt, so
// an absent header never buys an uncapped lane. Both buckets are always charged (no
// short-circuit) so a flood keeps counting against the ceiling even while its own
// per-client bucket is already over.
func publicRateOK(c *zip.Ctx) bool {
	client := cloud.ClientIP(c)
	if client == "" {
		client = "-"
	}
	within := publicRate.ok(client)
	return publicPeerRate.ok(peerIP(c)) && within
}

// peerIP is the L4 socket peer — the one address a caller cannot set. Through the
// ingress this is the proxy, so it keys the aggregate ceiling rather than a visitor.
func peerIP(c *zip.Ctx) string {
	ip := c.Fiber().IP()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if ip == "" {
		return "-"
	}
	return ip
}

// optedOut reports whether the visitor signalled Do-Not-Track or Global Privacy
// Control on the wire.
//
// This is a SECOND, independent line rather than a duplicate of the client's. On the
// client, consent is the host app's `enabled` config (@hanzo/event turns the whole
// client off when it is false, so an opted-out visitor normally emits no request at
// all) — which means the app owns that decision and a request can still arrive
// carrying the header: a surface that has not wired `enabled` to DNT/GPC, a browser
// or extension that sets the header itself, or a privacy proxy that adds one. When it
// does, the server stores nothing.
func optedOut(c *zip.Ctx) bool {
	if strings.TrimSpace(c.Header("DNT")) == "1" {
		return true
	}
	return strings.TrimSpace(c.Header("Sec-GPC")) == "1"
}

// publicName is the anonymous admission decision AND the name it yields, in one
// function because it is one rule: AN EVENT IS ADMITTED ONLY WHEN THE SERVER CAN NAME
// IT WITHOUT READING THE CALLER'S NAME.
//
// ok=false ⇒ dropped. ok=true with an EMPTY name ⇒ the route names itself downstream
// (resolveName's server-chosen default), which is how pageview and error are named on
// this lane — admitPublic rebuilds without the caller's `event`, so the empty string is
// not a gap in the decision but the whole of it. A non-empty name is a publicNames value,
// and nothing else can be.
//
// The empty case is only as strong as resolveName's defaults, and that is where this rule
// was once broken rather than here: resolveName named an error after its exception class,
// so "the route names it" was false for exactly one admitted kind. Both halves have to
// hold for the sentence above to be true, which is why the fix went there.
func publicName(e CaptureEvent) (string, bool) {
	kind := canonicalType(e.Type)
	if publicKinds[kind] {
		return "", true
	}
	if kind != "event" {
		return "", false
	}
	name, ok := publicNames[strings.ToLower(strings.TrimSpace(e.Event))]
	return name, ok
}

// publicProps is the property bag's PROJECTION — the field projection's own argument,
// applied one level down. It keeps exactly the @hanzo/observe annotation
// (annotationKeys, fact.go) and drops every other key, so the property names an
// anonymous row may carry are a set this SERVER declares.
//
// The annotation is what makes an anonymous interaction worth storing: a $click with a
// url and no element identity is a count, not a heatmap. It is also the one property
// family that widens nothing, and that is why it is the one that may cross: the
// annotation keys are LIFTED OUT of the bag into the `el` tuple (annotationOf), and
// attributesOf skips exactly the same keys — so admitting them adds no key to
// attributes, whose Map(LowCardinality(String), String) dictionary is the thing an
// unbounded anonymous property bag would actually attack. The invariant above survives
// verbatim: an anonymous row's attributes hold the folded $exception and the write
// core's $source, and nothing a caller sent.
//
// A value is projected only when it is WITHIN BOUNDS (maxAnnotation / maxAnnotationPath);
// an out-of-bounds value is simply not carried. That is this same projection with a
// complete predicate rather than a second mechanism bolted beside it — the function
// already answers "may this key be stored", and a key whose value cannot be stored
// safely is a key that may not be stored.
//
// The bound is necessary because maxPublicBytes does NOT subsume it. That cap bounds a
// REQUEST; it does not bound one stored VALUE, and inside 64 KiB a caller can spend
// nearly all of it on a single $el or a 6000-element $path. Those land in the `el`
// tuple, and cloud cannot narrow that column: this package is forbidden from declaring
// schema at all (capture_test.go enforces it — cloud writes and reads the plane, o11y
// owns its shape). When the writer cannot narrow the column, the writer must narrow the
// value.
func publicProps(p map[string]any) map[string]any {
	if len(p) == 0 {
		return nil
	}
	// Ranging over annotationKeys rather than over p is what binds this to the reader:
	// a key fact.go starts lifting into the tuple is carried here by construction,
	// instead of by remembering to spell the set a second time.
	out := make(map[string]any, len(annotationKeys))
	for _, k := range annotationKeys {
		v, ok := p[k]
		if !ok {
			continue
		}
		if v, ok := boundedAnnotation(v); ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// maxAnnotation / maxAnnotationPath bound ONE stored annotation value on the anonymous
// lane. They are READ OFF THE CLIENT rather than invented, so no real annotation can hit
// them: @hanzo/observe walks at most maxDepth=12 ancestors (annotate.ts) and clips every
// accessible name to MAX_NAME=80, which puts a genuine $el label — twelve `role[name]`
// steps joined by '/' — around 1.4 KiB at its very worst. 2 KiB clears that with room to
// spare while cutting the demonstrated 60 KiB value by thirty times, and 32 path elements
// is 2.6x the client's own depth.
//
// A caller mounting this attack is not running the client at all, which is exactly why
// the server keeps its own copy of the bound.
const (
	maxAnnotation     = 2 << 10
	maxAnnotationPath = 32
)

// boundedAnnotation reports whether one annotation value is within bounds, and yields the
// value to store. It is a FILTER, never a truncator: a clipped label would be a different
// element identity than the one the visitor touched, and a heatmap drawn on silently
// altered identities is worse than one with a gap. Over the bound, the key is dropped and
// the event still lands — the annotation is enrichment, so losing it costs context, never
// the interaction.
//
// The shapes are the two the annotation actually has: $path is a list of steps, every
// other key is a string. Anything else (an object, a number, a nested array) is not an
// annotation and is refused, so the `el` tuple's six fixed fields are the only thing this
// can ever produce.
func boundedAnnotation(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		return t, len(t) <= maxAnnotation
	case []any:
		if len(t) > maxAnnotationPath {
			return nil, false
		}
		for _, el := range t {
			s, ok := el.(string)
			if !ok || len(s) > maxAnnotation {
				return nil, false
			}
		}
		return t, true
	default:
		return nil, false
	}
}

// maxClass bounds an anonymous exception's CLASS. A class is an identifier — TypeError,
// ReferenceError, java.lang.IllegalStateException — so 256 bytes is far past anything a
// runtime emits, and a value beyond it is not a class.
const maxClass = 256

// publicException decides whether an anonymous exception may be carried at all, and
// then bounds the one field of it a caller can spend real cardinality on.
//
// AN EXCEPTION IS THE ERROR FAMILY'S FIELD, so it is carried on the error kind and
// nowhere else. Every other admitted kind returns nil. This is not defensive tidying:
// the projection ran BEFORE the fold, and foldException (publishable.go, called from
// ingestDecoded) stamps `attributes['$exception']` on whatever it is handed — so
// carrying Error onto a `$click` put the caller's whole exception, message and stack
// included, into the attributes dictionary of an autocapture row. Measured at 32 KiB
// from one request. The class bound below does not help there, because the bytes are in
// Message and Stack.
//
// It is closed by ASKING WHICH FAMILY THE ROW IS rather than by bounding two more
// fields, because the fields were never the problem: an interaction is not a fault, and
// a `$click` carrying an exception is not a bounded version of a real thing — it is a
// row with a field that has no meaning on it. Bounding Message and Stack here would have
// kept the meaningless field and merely made it smaller, and would owe an explanation
// for why a click may carry a stack trace at all.
//
// On the error kind, Type is bounded and Message and Stack are deliberately left alone:
// Type is not free text — it lands in the fault's `class` column and is the first thing
// fingerprint() hashes into `group`, which leads event.error's ORDER BY — while Message
// and Stack ARE free text, a real stack is legitimately long, they are redacted by
// scrubException, and maxPublicBytes is the right bound for text nobody keys on.
//
// An over-long class is DROPPED, not clipped, and the exception still lands: fingerprint
// already falls back to the message's shape when it has no class, so grouping degrades
// to the designed fallback instead of grouping two unrelated failures under a shared
// prefix.
func publicException(kind string, e *Exception) *Exception {
	if e == nil || kind != "error" {
		return nil
	}
	if len(e.Type) <= maxClass {
		return e
	}
	c := *e
	c.Type = ""
	return &c
}

// anonymousSubject is the namespace an UNATTESTED subject is stored under, and maxSubject
// bounds one. The '$' is the same reserved marker publicTenant carries and holds by the
// same argument: an identified subject is an IAM subject or the app's own person id, and
// neither is spelled with a leading '$', so a namespaced id lies outside the identified
// space and cannot collide with a person a real org knows.
//
// 256 bytes is far past any minted id (@hanzo/event's is a uuid, 36) and the value is
// keyed — uniqExact(distinct_id) is how every lens counts visitors — so an over-long one
// is not an id at all.
const (
	anonymousSubject = "$anon:"
	maxSubject       = 256
)

// publicSubject namespaces the ONE identity an anonymous caller supplies. NOBODY SIGNED
// FOR IT, so it may not be stored as a name that identifies a person.
//
// The signed lane's answer is attribute(): the token's own subject REPLACES whatever the
// caller sent, "so a `distinctId` in the body cannot pin events on a colleague". This
// lane has no token to substitute, and a bare drop is not the answer either — distinct_id
// is what uniqExact counts, so an empty one collapses every anonymous visitor into one.
// Namespacing keeps the count (one browser, one id, unchanged bytes after the prefix) and
// takes away the collision: `victim@corp.com` off the wire lands as `$anon:victim@corp.com`
// and joins nothing that org's identified rows hold.
//
// Empty in, empty out — an anonymous row legitimately carries no subject at all (a
// sendBeacon from a browser with no storage), and a bare prefix would be a subject that
// names nothing pretending to be one.
func publicSubject(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxSubject {
		return ""
	}
	return anonymousSubject + id
}

// admitPublic is the anonymous lane's WHOLE capability decision, and it is PURE over
// the decoded batch: it returns the events that may be stored and how many were
// dropped. It decides WHAT, never WHERE — it takes no *zip.Ctx AND no org, so neither
// a request field nor a caller argument can reach attribution through it. Where an
// anonymous row lands is the DOOR's decision (publicIngest's org), and the doors pass
// only server-side values: the publicTenant constant, or the org the site resolver
// returned for the request's host.
//
// Each admitted event is REBUILT from the allowlisted fields rather than edited, so a
// field this function does not name cannot reach the row. Every caller-controlled
// vocabulary comes back through a server-owned decision on the way in — the name from
// publicName, the property keys from publicProps, the subject from publicSubject — so
// none of them is copied across.
func admitPublic(evs []CaptureEvent) ([]CaptureEvent, int) {
	out := make([]CaptureEvent, 0, len(evs))
	dropped := 0
	for _, e := range evs {
		name, ok := publicName(e)
		if !ok {
			dropped++
			continue
		}
		// ONE canonical kind per row, read once: it is the stored Type AND the
		// question publicException answers, and computing it twice would let the
		// projection and the exception decision drift apart.
		kind := canonicalType(e.Type)
		out = append(out, CaptureEvent{
			MessageID:   e.MessageID,
			Type:        kind,
			Event:       name,
			Timestamp:   e.Timestamp,
			DistinctID:  publicSubject(e.DistinctID),
			AnonymousID: publicSubject(e.AnonymousID),
			SessionID:   e.SessionID,
			Product:     e.Product,
			URL:         e.URL,
			Path:        e.Path,
			Referrer:    e.Referrer,
			UTM:         e.UTM,
			Library:     e.Library,
			LibraryVer:  e.LibraryVer,
			Error:       publicException(kind, e.Error),
			Properties:  publicProps(e.Properties),
		})
	}
	return out, dropped
}

// attribute stamps the SIGNED identity onto every admitted row, replacing whatever the
// caller sent. It runs AFTER admitPublic so the projection still decides which rows
// exist; this only decides who they belong to.
//
// DistinctID is the join key every person-level lens groups by, and AnonymousID is the
// pre-login alias that stitches to it — both are caller-supplied, so on a lane where a
// real org will read the rows, both have to come from the token instead. AnonymousID is
// CLEARED rather than overwritten: it exists to link an anonymous session to a person
// later, and there is nothing to link when the person is already known.
//
// Not addressed here, and named rather than hidden: Timestamp is still the caller's.
// clampTS only pulls the FUTURE back to now, so a reduced principal can back-date its
// own events within its own org. Clamping the past would break the SPA's legitimate
// batching and its retry queue, which is why it is left alone.
func attribute(evs []CaptureEvent, subject string) []CaptureEvent {
	for i := range evs {
		evs[i].DistinctID = subject
		evs[i].AnonymousID = ""
	}
	return evs
}

// publicIngest answers a CREDENTIAL-LESS POST on any door: the request-scoped gates
// (capture flag, rate, size, opt-out) then the pure decision (admitPublic) then the ONE
// write core. dec is the door's wire; source stays the door's origin tag.
//
// org is where this lane's PROJECTED rows land, and it is the caller's ONLY influence
// over the outcome. It is always a server-side value — publicTenant from handle, or a
// resolved Site.Org from the published-site host — because the two callers are the only
// two, and neither reads it from the request:
//
//   - handle reaches here only when the caller presented no credential at all; a
//     presented-but-unresolvable key is refused there rather than downgraded.
//   - the site-host carve reaches here unconditionally, because it runs BEFORE the
//     identity boundary and so has no credential it could trust (see event.go).
//
// subject, when non-empty, is the credential's OWN signed identity and REPLACES the
// caller-supplied one on every admitted row (see handle). It is variadic so the two
// genuinely anonymous callers — the credential-less lane and the site-host carve — stay
// exactly as they were: nobody signed for them, so there is no identity to substitute.
func publicIngest(c *zip.Ctx, dec decode, org, source string, subject ...string) error {
	// CLOUD_ANALYTICS_PUBLIC_CAPTURE is the ONE existing anonymous-capture switch
	// (it also gates the site-host carve). Off ⇒ the canonical door keeps its
	// strict, principal-only contract.
	if !publicCaptureEnabled() {
		return zip.ErrForbidden("valid bearer or a resolvable ingest key required")
	}
	if !publicRateOK(c) {
		return zip.Errorf(http.StatusTooManyRequests, "rate limit exceeded")
	}
	body := c.Body()
	if len(body) > maxPublicBytes {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "event payload too large")
	}
	evs, err := dec(body)
	if err != nil {
		return zip.ErrBadRequest("malformed event payload")
	}
	if len(evs) > maxPublicBatch {
		return zip.ErrBadRequest("batch too large")
	}
	if optedOut(c) {
		return c.JSON(http.StatusOK, CaptureResult{Dropped: len(evs)})
	}
	// Rejoin the ONE pipeline: admission decided the projection, the door decided the
	// tenant, and ingestDecoded (event.go) does the rest exactly as it does for a bearer.
	admitted, dropped := admitPublic(evs)
	if len(subject) > 0 && subject[0] != "" {
		admitted = attribute(admitted, subject[0])
	}
	return ingestDecoded(c, org, source, admitted, dropped)
}

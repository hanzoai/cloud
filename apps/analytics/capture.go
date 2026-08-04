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

// Capture (WRITE) side of the analytics plane. analytics.go serves the read
// lenses over the event plane (event.fact, discriminated by signal); this
// file is the symmetric ingest that FILLS that plane. Products emit here (the ONE
// native front door) instead of talking to the insights capture service directly —
// cloud owns the tenant boundary; the PLANE's schema is owned by hanzoai/o11y.
//
// This file holds the WIRE TYPES and the ONE WRITE CORE (ingestEvents) every door
// funnels into. It owns no route: which paths accept an event is doors (event.go),
// and admission is handle (event.go). CaptureEvent below is the shape all wires
// normalize onto, which is why the canonical and PostHog decoders can share one
// pipeline instead of forking it.
//
// TENANCY: the row's tenant_id is ALWAYS principal.Org (the validated IAM owner
// slug), never a client-supplied field — a caller can only ever write into its
// OWN org's partition, the same isolation invariant the read side enforces. The
// client controls distinct_id/session_id/properties (its own visitors), never the
// tenant.
//
// PRIVACY: normalize scrubs credential- and PII-shaped property keys and any
// email-shaped value before the row is built (scrubProps). Only user/org
// identifiers (distinct_id, person_id, group_id, org) are retained as identity.
//
// ONE datastore client: writes ride apps/datastore — the SAME pooled,
// KMS-credentialed connection the read side queries through — so there is no
// second transport, pool, or credential path.

package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxBatch bounds one ingest request so a single POST cannot pin the warehouse.
// Larger batches are rejected (400) rather than silently truncated.
const maxBatch = 500

// maxClockSkew and maxBackdate are the TWO bounds on the one caller-chosen value
// that reaches a key column, and they exist for different reasons.
//
// maxClockSkew (FUTURE) keeps a skewed or hostile clock from parking rows outside
// the queryable window.
//
// maxBackdate (PAST) bounds the DOMAIN of `time`, which sits second in every plane
// table's ORDER BY ((org, time, id) on event.fact). A MergeTree part is skippable
// only when its key range misses the query's, so ONE small batch spanning 2019..now
// yields a part whose range intersects every window any tenant will ever ask for —
// O(1) to write, O(table) to read, for everyone. Bounding the domain is what makes
// that unbuildable.
//
// Seven days is a late-beacon flush (localStorage/service-worker queues survive a
// weekend, not a quarter). Backfilling real history is a different job than a public
// beacon door and does not get to arrive here.
//
// Both CLAMP rather than refuse, which is the pre-existing choice for the future
// side: an out-of-window beacon is still a real event, and dropping analytics data
// to protect a key range trades one loss for another. Retention does not depend on
// this value at all — the plane's TTL is measured from the server-stamped
// ingested_at, a column DEFAULT nothing on the wire can reach (warehouse.go).
const (
	maxClockSkew = 5 * time.Minute
	maxBackdate  = 7 * 24 * time.Hour
)

// THERE IS NO EVENTS-TABLE DDL HERE ANY MORE, AND THAT IS THE POINT. This package
// used to own eventsTableDDL/EnsureEventsTable for the legacy wide table
// (hanzo.events) — a second, per-tenant-partitioned copy of every product event.
// The plane's schema (event.fact, event.sample and their rollups) has ONE owner,
// hanzoai/o11y (schema.sql there), and cloud is a WRITER and READER of it, never a
// creator: the write core commits facts onto the plane and the sink lands them
// (warehouse.go); every read lens answers honest-empty when the plane is absent,
// exactly the stance analytics.go has always taken for tables it does not own.
// hanzo.events itself is retired AFTER this code is live fleet-wide — dropping it
// first would 500 the still-deployed writer.

// ── wire types ───────────────────────────────────────────────────────────────

// CaptureEvent is one client-emitted analytics event. The client sends a batch of
// these; the server owns the tenant (tenant_id is NOT a field here — it can never
// be set by the client).
type CaptureEvent struct {
	MessageID   string `json:"messageId"`  // client idempotency id; server mints one if empty
	Type        string `json:"type"`       // pageview | event | identify | group
	Event       string `json:"event"`      // event name (type=event); pageview→$pageview
	Timestamp   string `json:"timestamp"`  // RFC3339; clamped to server-now on skew/absent
	DistinctID  string `json:"distinctId"` // resolved person/visitor id
	AnonymousID string `json:"anonymousId"`
	PersonID    string `json:"personId"`
	SessionID   string `json:"sessionId"`
	Product     string `json:"product"` // emitting surface: console|chat|app|site|admin
	URL         string `json:"url"`
	Path        string `json:"path"`
	Referrer    string `json:"referrer"`
	UTM         UTM    `json:"utm"`
	RefCode     string `json:"refCode"`
	Channel     string `json:"channel"`
	GroupID     string `json:"groupId"`
	// GroupType names WHICH grouping the id belongs to (organization, workspace,
	// account). It is a map key rather than a column so the second grouping is a data
	// change: the plane stores `groups[type] = id`, never group0..group4, because a
	// fifth positional slot is a sixth one waiting to become a version suffix.
	// Absent means the only grouping anything sends today, `organization`.
	GroupType  string     `json:"groupType"`
	SignupWeek string     `json:"signupWeek"`
	ProductID  string     `json:"productId"`
	Quantity   uint32     `json:"quantity"`
	Revenue    float64    `json:"revenue"`
	Currency   string     `json:"currency"`
	Error      *Exception `json:"error"` // set on type:'error' events (folded into properties.$exception)

	// THE OTEL SIGNALS. One envelope carries all four — event, log, span, metric —
	// because they differ in their BODY, not in who sent them or when. Splitting the
	// envelope per signal duplicates org, time, session and identity four ways and
	// lets them drift; the fact plane (fact.go) reads one shape and writes one row.
	//
	// Each is a pointer so absent is distinct from empty: a pageview carries no Span,
	// and a zero-valued one would be a span of duration 0 rather than no span at all.
	Log    *LogBody    `json:"log"`
	Span   *SpanBody   `json:"span"`
	Metric *MetricBody `json:"metric"`
	Clip   *ClipBody   `json:"clip"`

	// Kind narrows Type when the surface knows more than the wire word does — a
	// span's client/server role, a page's navigation kind. Empty means the route's
	// own default, which is what every existing client sends.
	Kind string `json:"kind"`

	// SITE is the deployed property a signal came from, and it is carried on EVERY
	// event, not only on a failure.
	//
	// It lived on the exception alone, so sentry.hanzo.ai could group faults by site
	// while analytics.hanzo.ai — reading the event stream — had no site column at
	// all, and "all sites" was a question the data could not answer. One property
	// per row is what makes the two surfaces read the same world.
	Site string `json:"site"`

	// Level, Release, Environment and Service qualify a signal the same way for
	// every kind: which severity, which build, which deployment, which service. They
	// are not error-only either, for the same reason Site is not.
	Level       string `json:"level"`
	Release     string `json:"release"`
	Environment string `json:"environment"`
	Service     string `json:"service"`

	// TraceID and SpanID correlate a signal to a trace whatever its body is, so a log
	// and the span it was emitted inside join without either owning the other.
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`

	// Resource is the fingerprint of the emitting resource — the host, pod and
	// service attributes a collector already deduplicates into its own dimension
	// table. It joins event.log_resource / event.span_resource on `fingerprint`.
	// ONE name for it: the plane previously carried this fact as `resource UInt64`
	// AND `resource_fingerprint String` on the same row, one of them always zero.
	Resource string `json:"resource"`

	Properties map[string]any `json:"properties"`
	Library    string         `json:"library"`
	LibraryVer string         `json:"libraryVersion"`
}

// LogBody is the body of a log signal: OTel severity, its numeric rank, and the
// message. The body is scrubbed at the one point it enters a fact, never here.
type LogBody struct {
	// Severity is the OTel severity text, lowercased on the way into a fact.
	Severity string `json:"severity"`
	// Number is the OTel severity NUMBER (1..24), which orders severities without
	// parsing their text and survives a client that spells one differently.
	Number uint8 `json:"number"`
	// Body is the log message. It is scrubbed where it enters a fact, not here, so
	// there is one scrub on one path.
	Body string `json:"body"`
}

// SpanBody is the body of a span: its place in the trace and how it ended. ID and
// Trace are carried here as well as on the envelope because a client that sends a
// span knows them precisely, while a log only correlates.
type SpanBody struct {
	// ID is this span's own id, which a client emitting a span knows precisely.
	ID string `json:"id"`
	// Trace is the trace this span belongs to.
	Trace string `json:"trace"`
	// Parent is the enclosing span's id, empty for a root span.
	Parent string `json:"parent"`
	// Kind is the span's role — client, server, producer, consumer, internal.
	Kind string `json:"kind"`
	// Status is how the span ended: ok, error, or unset.
	Status string `json:"status"`
	// Duration is the elapsed time in nanoseconds. Unsigned because a span cannot
	// take negative time.
	Duration uint64 `json:"duration"`
}

// ClipBody is the body of a session-replay clip: WHERE THE BLOB IS, not the blob.
//
// A clip's recording is a multi-megabyte time-ordered binary. It is wrong for a bus
// message (which is a hand-off, sized for many small facts) and wrong for a warehouse
// row (which is a column store optimized for scanning narrow values), so it goes
// neither place. It is written to object storage by whoever recorded it, and the fact
// plane carries the INDEX: the address, the size and the span of time it covers.
//
// That is what makes a whole product cost two columns. Everything else a replay needs
// — which session, which org, which page, when — is already the envelope.
type ClipBody struct {
	// Object is the blob's address in object storage.
	Object string `json:"object"`
	// Bytes is its size, so a session list can show weight without opening it.
	Bytes uint64 `json:"bytes"`
	// Duration is the wall time the clip covers, in nanoseconds — the same column a
	// span's elapsed time uses, because it is the same fact.
	Duration uint64 `json:"duration"`
}

// MetricBody is the body of a metric sample: what was measured, its value, and the
// labels it is sliced by.
type MetricBody struct {
	// Name is what was measured, and it is also the fact's event name for a metric.
	Name string `json:"name"`
	// Value is the sample.
	Value float64 `json:"value"`
	// Labels are the dimensions the sample is sliced by.
	Labels map[string]any `json:"labels"`
}

// UTM is the first-touch attribution the client persists and re-sends per event.
type UTM struct {
	Source   string `json:"source"`
	Medium   string `json:"medium"`
	Campaign string `json:"campaign"`
	Term     string `json:"term"`
	Content  string `json:"content"`
}

// CaptureBatch is the ingest envelope. `batch` is canonical; `events` is accepted
// as an alias so a Segment-shaped client works unchanged.
type CaptureBatch struct {
	Batch  []CaptureEvent `json:"batch"`
	Events []CaptureEvent `json:"events"`
}

func (b CaptureBatch) events() []CaptureEvent {
	if len(b.Batch) > 0 {
		return b.Batch
	}
	return b.Events
}

// CaptureResult is the honest receipt: persisted vs dropped (unroutable) counts.
type CaptureResult struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"`
}

// ── pure core ──────────────────────────────────────────────────────────────
//
// The ONE normalizer is the plane's own: normalize (fact.go) turns a wire event
// into the fact that lands in event.<signal>. The legacy wide-row normalizer
// (normalizeEvent → eventRow → buildEventsInsert → hanzo.events) is gone with the
// table it fed; resolveEventName below survives because it is the SUBSCRIBER
// vocabulary — the $pageview/$error names the destinations fan-out and the
// webhook envelope contract publish (forward.go, bus.go subjectFor), a published
// grammar that cannot move when storage does.

// resolveEventName maps the client (type,event) to the PUBLISHED event name the
// fan-out contracts carry ($pageview et al — PostHog-style reserved names orgs
// already subscribe to). The STORED name is resolveName (fact.go), which uses the
// plane vocabulary (kind=page + name=page_viewed). A type=event with no name is
// dropped on both.
func resolveEventName(e CaptureEvent) string {
	name := strings.TrimSpace(e.Event)
	switch canonicalType(e.Type) {
	case "pageview":
		if name == "" {
			return "$pageview"
		}
		return name
	case "identify":
		return "$identify"
	case "group":
		return "$group"
	case "error":
		if name == "" {
			return "$error"
		}
		return name
	default: // "event"
		return name
	}
}

// canonicalType folds the type to the closed set
// {pageview,identify,group,error,event}. `error` is first-class so the ingest can
// store type:'error' events under event_type='error' — the key the /v1/errors
// read lens filters on. An unknown type still folds to "event".
func canonicalType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "pageview", "page":
		return "pageview"
	case "identify":
		return "identify"
	case "group":
		return "group"
	case "error":
		return "error"
	default:
		return "event"
	}
}

// clampTS parses an RFC3339 timestamp and clamps anything outside
// [now-maxBackdate, now+maxClockSkew] — and anything unparseable or absent — to now.
// Both anchored UTC.
//
// The PAST bound is the one that matters for the warehouse: this value leads ORDER BY
// and is half the partition key, so an unbounded past is an unbounded key range (see
// maxBackdate). It is also the ONE place that bound belongs. Every wire funnels through
// here, and the ways to reach 1970 are per-wire and easy to miss: the team SPA posts
// epoch MILLIS, so `"timestamp":1` decodes to 1970-01-01 through a decoder that is
// correct about zero (teamTime returns "" so this function anchors it) and has no
// opinion about one. A bound in each decoder would be several chances to forget.
func clampTS(s string, now time.Time) time.Time {
	now = now.UTC()
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return now
	}
	ts = ts.UTC()
	if ts.After(now.Add(maxClockSkew)) || ts.Before(now.Add(-maxBackdate)) {
		return now
	}
	return ts
}

// ── privacy scrub ────────────────────────────────────────────────────────────

var emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

// secretRe redacts credential shapes that leak in free-text — chiefly error
// stacks/messages (which bypass the key-based denylist): bearer tokens, the key
// families (pk-/sk-), and ?token=/api_key=/access_token=/password=/secret=
// query params. Applied to every scrubbed string so a token in a URL property or
// an exception frame is redacted before storage AND before the destinations
// fan-out.
//
// A published key is not a secret — it ships in public bundles by design — but it
// is redacted anyway: a key in an error frame is noise, and telling the two apart
// here would be a second place that has to know the families.
var secretRe = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/\-]{8,}={0,2}|(?:pk|sk)-[a-z0-9._\-]{8,}|[?&](?:access_token|refresh_token|id_token|api[_-]?key|token|password|secret|auth)=[^&\s"']+)`)

// scrubText redacts email- and credential-shaped substrings from a free-text
// string. This is the ONE string scrubber; scrubValue and scrubException both
// route through it so the redaction policy lives in one place.
func scrubText(s string) string {
	if s == "" {
		return s
	}
	s = emailRe.ReplaceAllString(s, "[redacted]")
	s = secretRe.ReplaceAllString(s, "[redacted]")
	return s
}

// scrubException returns a COPY of e with its free-text fields (Message, Stack)
// redacted; never mutates the caller's struct. nil-safe. This is what makes a
// type:'error' event safe to both store and fan out to third parties — the raw
// stack/message can carry tokens, API URLs with query secrets, or PII.
func scrubException(e *Exception) *Exception {
	if e == nil {
		return nil
	}
	c := *e
	c.Message = scrubText(c.Message)
	c.Stack = scrubText(c.Stack)
	return &c
}

// denySubstr: any property key CONTAINING one of these (case-insensitive) is
// dropped — the credential/secret family.
var denySubstr = []string{
	"password", "passwd", "secret", "authorization", "api_key", "apikey",
	"access_token", "refresh_token", "id_token", "private_key", "client_secret",
	"credit_card", "card_number", "cardnumber", "cvv", "cvc", "ssn", "cookie",
}

// denyExact: keys equal (case-insensitive) to one of these are dropped — the PII
// family. Only user/org IDENTIFIERS are retained (distinct_id etc), never names,
// emails, or phone numbers.
var denyExact = map[string]bool{
	"email": true, "e-mail": true, "phone": true, "phone_number": true,
	"name": true, "first_name": true, "last_name": true, "full_name": true,
	"address": true, "token": true, "auth": true,
}

// scrubProps drops credential/PII-shaped keys, redacts email-shaped string values,
// and returns compact JSON ("" for empty). Nested maps are scrubbed recursively.
func scrubProps(p map[string]any) string {
	clean := scrubMap(p)
	if len(clean) == 0 {
		return ""
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return ""
	}
	return string(b)
}

func scrubMap(p map[string]any) map[string]any {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]any, len(p))
	for k, v := range p {
		if deny(k) {
			continue
		}
		out[k] = scrubValue(v)
	}
	return out
}

func scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return scrubText(t)
	case *Exception:
		return scrubException(t)
	case map[string]any:
		return scrubMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = scrubValue(e)
		}
		return out
	default:
		return v
	}
}

func deny(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if denyExact[k] {
		return true
	}
	for _, s := range denySubstr {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// ── small pure helpers ───────────────────────────────────────────────────────

func trim(s string) string { return strings.TrimSpace(s) }

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// hostOf extracts the bare host from a referrer URL (no scheme/path), for the
// referrer_domain column the channel derivation and reports use. Best-effort; ""
// on anything unparseable.
func hostOf(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// randID mints a 128-bit hex id when the client omits a messageId.
func randID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "evt-" + strconv64(time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func strconv64(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

// ── handler ──────────────────────────────────────────────────────────────────

// resolveKeyOrg maps a presented project/API key to its org through the ONE IAM
// key seam (cloud.OrgForKey). It is a package var ONLY so a test can substitute a
// resolver without standing up IAM; production is always cloud.OrgForKey.
var resolveKeyOrg = cloud.OrgForKey

// warehouseReady and warehouseExec are the warehouse gate and the statement
// executor, held as values for the SAME reason and on the SAME terms as
// resolveKeyOrg: production is always the one datastore client, and a test
// substitutes them to drive the write path without standing up a warehouse.
//
// They are what makes the TENANT observable. Without a warehouse the pipeline stops
// at the readiness gate, so which org a lane resolved never reaches anything a test
// can read — the site-host carve could file a customer's beacons under the public
// tenant, or the reverse, and every status code would be identical. The row's
// tenant_id is the fact that matters here, so it has to be reachable.
// warehouseReady and warehouseExec now live in warehouse.go, beside the writer that
// uses them — one declaration, so a test substituting the store cannot substitute
// only half of it.

// projectKey returns the project/API key a keyed SDK presents OUT-OF-BAND of the
// Authorization header — the transports SanitizeIdentity does NOT mint identity
// from, so they never reach tenant() as a principal. In priority order: the
// ?api_key= query, the x-api-key / api-key headers, and the PostHog-wire body
// field `api_key` (posthog-js and the insights-go batch envelope put it there).
// "" when none is present.
//
// The body is PEEKED via c.Body() — fasthttp buffers the full body, so the later
// c.Bind in the handler re-reads the same bytes; peeking does not consume it. Only
// the api_key field is decoded (a bad/unrelated JSON body simply yields "").
func projectKey(c *zip.Ctx) string {
	if k := trim(c.Query("api_key")); k != "" {
		return k
	}
	if k := trim(c.Header("x-api-key")); k != "" {
		return k
	}
	if k := trim(c.Header("api-key")); k != "" {
		return k
	}
	if body := c.Body(); len(body) > 0 {
		var probe struct {
			APIKey string `json:"api_key"`
		}
		if json.Unmarshal(body, &probe) == nil {
			if k := trim(probe.APIKey); k != "" {
				return k
			}
		}
	}
	return ""
}

// There is deliberately no captureTenant here. The deprecated aliases used to resolve
// their own tenant, and their last resort was the request Host: an unattested POST to
// a recognized brand host (cloud.BrandForHostOK) was attributed to that brand's REAL
// org — 'hanzo', 'lux', 'zoo' — at FULL CaptureEvent capability. Anyone on the
// internet could therefore set a Host header and inject revenue, orders, personId and
// groupId rows into a brand's own partition, which the /v1/analytics overview + top
// lenses, /v1/analytics/campaign and the GTM funnel (clients/guide) all read.
//
// That was a per-DOOR copy of a decision that belongs to the TRUST LEVEL. Both alias
// handlers now call handle (event.go) like every other door: a credential resolves to
// its own org, at full capability or through the projection, and a credential-less
// caller is refused. A Host header no longer names a tenant anywhere.

// ── ONE write core ───────────────────────────────────────────────────────────

// event source tags — the WIRE each row arrived on. Stamped into properties.$source
// by ingestEvents, which the plane normalizer carries into attributes['$source'], so
// the ONE event.fact table stays honest about origin WITHOUT a second table or a
// schema migration: $source is queryable in the attributes map. One tag per door,
// and doors (event.go) is the only list that binds them.
//
// There is no 'capture' tag: rows carrying it were written by the retired
// /v1/analytics{,/batch} and /v1/tracker name-aliases of the canonical wire. Those
// rows keep their value in the warehouse — history is not rewritten — but no code
// path can mint another, which is what makes the retirement a fact rather than a
// convention.
const (
	sourceEvent   = "event"   // canonical POST /v1/event (canonical wire)
	sourcePostHog = "posthog" // POST /v1/insights/e (PostHog wire)
)

// withSource returns a copy of p carrying $source=source (the ingest adapter), so
// normalize's scrub+store path records origin as a property. nil-safe; never
// mutates the caller's map (the adapters share their event structs).
func withSource(p map[string]any, source string) map[string]any {
	if source == "" {
		return p
	}
	out := make(map[string]any, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	out["$source"] = source
	return out
}

// ingestEvents is the ONE write core: normalize → scrub → PUBLISH the fact onto the
// event plane. org is the SERVER-resolved tenant (never client input); source tags
// the ingest adapter. Every front door — the canonical /v1/event and the deprecated
// PostHog / Segment / beacon adapters — funnels here, so there is exactly one write
// path. Returns the honest accepted/dropped receipt; the errors it returns are
// already HTTP-shaped (zip) for the handler to pass straight up.
//
// ONE ADMISSION, ONE STORAGE PROJECTION. The fact is the ONLY durable copy: the sink
// (warehouse.go) lands it in event.fact under its own signal — so "stored" and "queryable by
// signal" are the same claim. The legacy second projection (a wide hanzo.events row
// per event, inserted here beside the publish) is GONE: it was the measured
// 59.7-byte/row double-write the o11y MV then copied BACK onto the plane minus its
// errors. What remains beside the commit is fanOut — consumer hand-offs (the
// destinations sink and the webhook envelope), copies for subscribers and never a
// second write to storage.
//
// The bus is a COMMIT and not a best-effort fan-out (contrast fanOut below, which is
// detached and fail-soft because its facts are already committed here). A fact that
// could not be published is a fact that will never be queryable, so it is a 503 — the
// one answer this whole design exists to give instead of a 200. Retrying the whole
// batch is safe BECAUSE the ids survive the retry: id is the client messageId when
// one was sent (minted once server-side otherwise), every event.* table is a
// ReplacingMergeTree keyed (org, time, id), and a re-published fact collapses on
// merge instead of duplicating.
//
// requireDatastore still gates the door even though the commit is the publish: this
// binary is also the plane's sink, so accepting into the stream while the one
// warehouse client is down would answer 200 for facts that only ever age on the bus.
// A caller gets the honest 503 while nothing can land.
func ingestEvents(ctx context.Context, org, source string, evs []CaptureEvent) (CaptureResult, error) {
	if len(evs) == 0 {
		return CaptureResult{}, nil
	}
	if len(evs) > maxBatch {
		return CaptureResult{}, zip.ErrBadRequest("batch too large")
	}
	if err := requireDatastore(); err != nil {
		return CaptureResult{}, err
	}
	now := time.Now().UTC()
	facts := make([]fact, 0, len(evs))
	dropped := 0
	for _, e := range evs {
		e.Properties = withSource(e.Properties, source)
		// ONE ADMISSION DECISION, and it is the plane's own normalizer (fact.go) that
		// makes it — the canonical answer to "what is this event", which is exactly
		// what admission is asking. It is also the only normalizer that can see the
		// signal, so it is the only one that can refuse a signal nothing can land.
		f, ok := normalize(org, now, e)
		if !ok || !landableSignals[f.signal] {
			dropped++
			continue
		}
		facts = append(facts, f)
	}
	if len(facts) == 0 {
		return CaptureResult{Dropped: dropped}, nil
	}
	if err := publish(ctx, facts); err != nil {
		return CaptureResult{}, err
	}
	// Fan the accepted batch out to the downstream consumers (destinations sink +
	// webhook envelopes), detached and fail-soft — never blocks or fails an ingest
	// (forward.go). No-op when unset.
	fanOut(org, evs)
	return CaptureResult{Accepted: len(facts), Dropped: dropped}, nil
}

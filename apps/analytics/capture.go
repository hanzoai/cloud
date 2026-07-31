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

// Capture (WRITE) side of the event plane — the ONE native front door products emit
// to. It holds the WIRE TYPES and the ONE INGEST CORE (ingestEvents) every door
// funnels into. It owns no route: which paths accept an event is doors (event.go), and
// admission is handle (event.go). CaptureEvent below is the shape all wires normalize
// onto, which is why the canonical, PostHog and team decoders share one pipeline
// instead of forking it.
//
// THREE FILES, THREE JOBS, and this one is the wire:
//
//	capture.go   the WIRE — decode, scrub, clamp, and the core every door calls.
//	fact.go      the FACT — what an event IS: the envelope, the signal, the route.
//	bus.go       the CONTAINER — publishing that fact to the EVENT stream.
//	warehouse.go the SINK — the durable consumer that lands facts in their tables.
//
// TENANCY: a fact's org is ALWAYS the SERVER-resolved tenant (the validated IAM owner
// slug), never a client-supplied field — a caller can only ever write into its OWN
// org's partition. `org` is not a field on any wire type in this file, so that is a
// property of the types and not of a check someone has to remember. The client
// controls distinct_id/session_id/properties (its own visitors), never the tenant.
//
// PRIVACY: the scrub below drops credential- and PII-shaped property keys and redacts
// any email- or token-shaped value BEFORE a fact is built, so nothing unscrubbed
// reaches the bus. Only user/org identifiers (distinct_id, person_id, org) are
// retained as identity.
//
// NO WAREHOUSE HERE. This file does not know what a table is: it publishes, and the
// warehouse is one consumer of the stream (warehouse.go). That is what lets a second
// consumer be added without touching ingest.
package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxBatch bounds one ingest request so a single POST cannot pin the plane.
// Larger batches are rejected (400) rather than silently truncated.
const maxBatch = 500

// publicCaptureEnv gates anonymous (no-principal) capture. Default ON: the
// marketing sites emit anonymous pageviews, and cloud is REPLACING the already-
// public insights-capture ingest, so refusing anonymous events would drop that
// traffic. Set to a falsey value to require a validated principal on every event.
const publicCaptureEnv = "CLOUD_ANALYTICS_PUBLIC_CAPTURE"

// maxClockSkew and maxBackdate are the TWO bounds on the one caller-chosen value
// that reaches a key column, and they exist for different reasons.
//
// maxClockSkew (FUTURE) keeps a skewed or hostile clock from parking rows outside
// the queryable window.
//
// maxBackdate (PAST) bounds the DOMAIN of `timestamp`, which is the leading column
// of ORDER BY and half the partition key. A MergeTree part is skippable only when
// its key range misses the query's, so ONE small batch spanning 2019..now yields a
// part whose range intersects every window any tenant will ever ask for — O(1) to
// write, O(table) to read, for everyone. Bounding the domain is what makes that
// unbuildable; the partition key then keeps a wide batch inside its own tenant's
// partitions rather than the shared "all".
//
// Seven days is a late-beacon flush (localStorage/service-worker queues survive a
// weekend, not a quarter). Backfilling real history is a different job than a public
// beacon door and does not get to arrive here.
//
// Both CLAMP rather than refuse, which is the pre-existing choice for the future
// side: an out-of-window beacon is still a real event, and dropping analytics data
// to protect a key range trades one loss for another. Retention no longer depends on
// this value at all — TTL is measured from the server-stamped ingested_at.
const (
	maxClockSkew = 5 * time.Minute
	maxBackdate  = 7 * 24 * time.Hour
)

// ── wire types ───────────────────────────────────────────────────────────────

// CaptureEvent is one client-emitted event. The client sends a batch of these; the
// server owns the tenant (org is NOT a field here — it can never be set by the client).
//
// `type` is the ROUTE SELECTOR and its only job: it picks the signal, hence the table
// and the subject (routeOf, fact.go). Everything below it splits three ways — the
// ENVELOPE fields every signal shares, the attribution fields that travel in
// `attributes`, and the four typed bodies, of which at most one is meaningful for a
// given type. A caller sends the body its signal names and leaves the rest nil.
type CaptureEvent struct {
	MessageID string `json:"messageId"` // client idempotency id; server mints one if empty
	Type      string `json:"type"`      // ROUTE: event|page|identify|group|error|log|span|metric
	Kind      string `json:"kind"`      // optional override of the route's default kind column
	Event     string `json:"event"`     // event name; defaulted per route when empty
	Timestamp string `json:"timestamp"` // RFC3339; clamped to server-now on skew/absent

	// Envelope identity — the client's own visitors, never the tenant.
	DistinctID  string `json:"distinctId"`
	AnonymousID string `json:"anonymousId"`
	PersonID    string `json:"personId"`
	SessionID   string `json:"sessionId"`
	Product     string `json:"product"` // emitting surface: console|chat|app|site|admin
	URL         string `json:"url"`
	Path        string `json:"path"`

	// Attribution and commerce. These had their own columns on the old wide table and
	// are the SAME facts under the SAME names in `attributes` — a caller's own
	// vocabulary, which is exactly what a map is for.
	Referrer   string  `json:"referrer"`
	UTM        UTM     `json:"utm"`
	RefCode    string  `json:"refCode"`
	Channel    string  `json:"channel"`
	GroupID    string  `json:"groupId"`
	SignupWeek string  `json:"signupWeek"`
	ProductID  string  `json:"productId"`
	Quantity   uint32  `json:"quantity"`
	Revenue    float64 `json:"revenue"`
	Currency   string  `json:"currency"`
	Library    string  `json:"library"`
	LibraryVer string  `json:"libraryVersion"`

	// Shared by the error, log and span signals.
	Service     string `json:"service"`
	TraceID     string `json:"traceId"`
	SpanID      string `json:"spanId"`
	Site        string `json:"site"`
	Level       string `json:"level"`
	Release     string `json:"release"`
	Environment string `json:"environment"`

	// The typed bodies — at most one is read, chosen by Type.
	Error  *Exception `json:"error"`
	Log    *LogRecord `json:"log"`
	Span   *SpanBody  `json:"span"`
	Metric *Metric    `json:"metric"`

	Properties map[string]any `json:"properties"`
}

// LogRecord is the log signal's body (event.log's own columns).
type LogRecord struct {
	Severity string `json:"severity"` // severity_text: debug|info|warn|error|fatal
	Number   uint8  `json:"number"`   // severity_number, OTel's numeric scale
	Body     string `json:"body"`     // the message; scrubbed like every free-text field
}

// SpanBody is the span signal's body (event.span's own columns). Trace and ID fall
// back to the shared TraceID/SpanID so a caller may set either.
type SpanBody struct {
	Trace    string `json:"trace"`
	ID       string `json:"id"`
	Parent   string `json:"parent"`
	Kind     string `json:"kind"`     // OTel span kind; becomes the `kind` column
	Duration uint64 `json:"duration"` // nanoseconds
	Status   string `json:"status"`
}

// Metric is the metric signal's body. It is normalized and published like every other
// signal; see writers (warehouse.go) for why it is not yet warehoused.
type Metric struct {
	Name   string         `json:"name"`
	Value  float64        `json:"value"`
	Labels map[string]any `json:"labels"`
	Unit   string         `json:"unit"`
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
// families (pk-/sk-/hk-), and ?token=/api_key=/access_token=/password=/secret=
// query params. Applied to every scrubbed string so a token in a URL property or
// an exception frame is redacted before storage AND before the destinations
// fan-out.
//
// A published key is not a secret — it ships in public bundles by design — but it
// is redacted anyway: a key in an error frame is noise, and telling the two apart
// here would be a second place that has to know the families.
var secretRe = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/\-]{8,}={0,2}|(?:pk|sk|hk)-[a-z0-9._\-]{8,}|[?&](?:access_token|refresh_token|id_token|api[_-]?key|token|password|secret|auth)=[^&\s"']+)`)

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

// publicCaptureEnabled reports whether anonymous capture is allowed (default ON).
func publicCaptureEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(publicCaptureEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// resolveKeyOrg maps a presented project/API key to its org through the ONE IAM
// key seam (cloud.OrgForKey). It is a package var ONLY so a test can substitute a
// resolver without standing up IAM; production is always cloud.OrgForKey.
var resolveKeyOrg = cloud.OrgForKey

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
// its own org at full capability, and a credential-less caller gets the anonymous
// projection under publicTenant. A Host header no longer names a tenant anywhere.

// ── ONE ingest core ──────────────────────────────────────────────────────────

// event source tags — the ingest adapter each fact arrived through. Stamped into
// attributes.source by ingestEvents so the plane stays honest about origin (canonical
// vs. deprecated wire) with no second table and no schema change: `source` is a plain
// attribute, which is exactly the migration signal for the alias sunset
// (attributes['source'] = 'capture').
const (
	sourceEvent   = "event"   // canonical POST /v1/event (canonical wire)
	sourcePostHog = "posthog" // POST /v1/insights/e (PostHog wire)
	sourceCapture = "capture" // POST /v1/analytics{,/batch}, /v1/tracker (@hanzo/capture, sunsetting)
)

// withSource returns a copy of p carrying source=source (the ingest adapter), so
// normalize records origin as an attribute. nil-safe; never mutates the caller's map
// (the adapters share their event structs).
func withSource(p map[string]any, source string) map[string]any {
	if source == "" {
		return p
	}
	out := make(map[string]any, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	out["source"] = source
	return out
}

// ingestEvents is the ONE ingest core: normalize → PUBLISH. org is the SERVER-resolved
// tenant (never client input); source tags the ingest adapter. Every front door — the
// canonical /v1/event and the PostHog / Segment / beacon / team adapters — funnels
// here, so a fact is built in exactly one place.
//
// PUBLISH IS THE COMMIT. The door does not touch the warehouse: it hands each fact to
// the EVENT stream and JetStream answers with a PubAck only once the fact is on file
// storage. So an "accepted" receipt means DURABLE, not "queued in this process's
// memory" — and the warehouse is just the first of the stream's consumers
// (warehouse.go), beside error grouping, alerts, sessions, live dashboards and
// exports. Adding the next consumer is a subscription, not an edit to this function;
// that is the whole reason the fan-out is on the bus and not in this handler.
//
// Returns the honest accepted/dropped receipt; the errors it returns are already
// HTTP-shaped (zip) for the handler to pass straight up.
func ingestEvents(ctx context.Context, org, source string, evs []CaptureEvent) (CaptureResult, error) {
	if len(evs) == 0 {
		return CaptureResult{}, nil
	}
	if len(evs) > maxBatch {
		return CaptureResult{}, zip.ErrBadRequest("batch too large")
	}
	now := time.Now().UTC()
	facts := make([]fact, 0, len(evs))
	dropped := 0
	for _, e := range evs {
		e.Properties = withSource(e.Properties, source)
		f, ok := normalize(org, now, e)
		if !ok {
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
	// Fan the accepted batch out to the downstream sink (destinations), detached and
	// fail-soft — never blocks or fails an ingest (forward.go). No-op when unset.
	fanOut(org, evs)
	return CaptureResult{Accepted: len(facts), Dropped: dropped}, nil
}

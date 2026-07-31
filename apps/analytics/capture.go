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
// lenses over hanzo.events; this file is the symmetric ingest that FILLS that
// table, so the web/commerce lenses stop being honest-empty. Products emit here
// (the ONE native front door) instead of talking to the insights capture service
// directly — cloud owns the tenant boundary and the warehouse schema.
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
// PRIVACY: normalizeEvent scrubs credential- and PII-shaped property keys and any
// email-shaped value before the row is built (scrubProps). Only user/org
// identifiers (distinct_id, person_id, group_id, org) are retained as identity.
//
// ONE datastore client: writes ride clients/datastore — the SAME pooled,
// KMS-credentialed connection the read side queries through — so there is no
// second transport, pool, or credential path.
package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxBatch bounds one ingest request so a single POST cannot pin the warehouse.
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

// eventsTableDDL is the ONE definition of the hanzo.events schema — owned here by
// the WRITER. The read lenses (query.go) SELECT a subset of these columns; keep the
// two in lockstep. MergeTree ordered for the read side's (window, tenant, event)
// predicate; 2-year retention mirrors cloud_usage.
//
// TTL IS MEASURED FROM ingested_at, NOT timestamp. `timestamp` is the CALLER's, so a
// TTL over it made retention a request parameter: a back-dated event inserted, fanned
// out to every configured destination (forward.go), and was TTL-eligible the moment it
// landed — a write that answers 200 and vanishes. `ingested_at DateTime DEFAULT now()`
// is stamped by the server and is not in eventColumns, so nothing on the wire can
// influence when a row expires. It is the only column that can carry a retention
// clause honestly.
//
// PARTITION BY (tenant_id, toYYYYMM(timestamp)) — the shape every other tenant-scoped
// table in this database uses (observations, persons, scores, sessions, traces,
// usage_records, and this table's own rollups events_hourly/events_daily). It is not
// decoration:
//   - it is the TENANT BOUNDARY in the storage layer. Unpartitioned, all tenants share
//     one "all" partition, so one part's key range is compared against every tenant's
//     query. Partitioned by tenant, a part physically cannot appear in another
//     tenant's scan — the isolation stops being a property of the key range and
//     becomes a property of the layout.
//   - it matches the read lens exactly. query.go filters
//     `timestamp >= ? AND timestamp < ? AND tenant_id = ?`, which is both halves of
//     this key, so pruning happens before the primary index is consulted.
//   - it makes retention a partition DROP instead of a merge that rewrites parts.
//   - it makes the part key range VISIBLE: system.parts.min_time/max_time are only
//     populated for a time-based partition key, so unpartitioned an operator cannot
//     even see a part that spans years (measured: every live part reports 1970/1970).
//
// Partition count is bounded because a batch carries ONE tenant (the server owns
// tenant_id) and `timestamp` is clamped to [now-maxBackdate, now] — so an insert
// touches at most two months of one tenant, far under max_partitions_per_insert_block.
//
// THIS IS THE DEFINITION, NOT A MIGRATION. `CREATE TABLE IF NOT EXISTS` is a NO-OP
// against a table that already exists, and hanzo.events does exist on hanzo-k8s
// (unpartitioned, `TTL timestamp + toIntervalYear(2)`). Existing deployments need the
// one-time reconcile in LLM.md ("hanzo.events: retention and partitioning"); the TTL
// half is a metadata ALTER, the partition half is not expressible as an ALTER at all
// and needs a table swap. Fresh deployments get this schema and need nothing.
const eventsTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.events (
		id String,
		timestamp DateTime,
		tenant_id String,
		event String,
		event_type String,
		distinct_id String,
		anonymous_id String,
		person_id String,
		session_id String,
		product String,
		url String,
		path String,
		referrer String,
		referrer_domain String,
		utm_source String,
		utm_medium String,
		utm_campaign String,
		utm_term String,
		utm_content String,
		ref_code String,
		channel String,
		group_id String,
		signup_week String,
		product_id String,
		quantity UInt32,
		revenue Float64,
		currency String,
		properties String,
		library String,
		library_version String,
		ingested_at DateTime DEFAULT now()
	) ENGINE = MergeTree()
	PARTITION BY (tenant_id, toYYYYMM(timestamp))
	ORDER BY (timestamp, tenant_id, event)
	TTL ingested_at + INTERVAL 2 YEAR`

// eventColumns is the INSERT column list — ingested_at is omitted (server DEFAULT
// now()). eventRow.args returns values in EXACTLY this order.
var eventColumns = []string{
	"id", "timestamp", "tenant_id", "event", "event_type",
	"distinct_id", "anonymous_id", "person_id", "session_id", "product",
	"url", "path", "referrer", "referrer_domain",
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
	"ref_code", "channel", "group_id", "signup_week",
	"product_id", "quantity", "revenue", "currency",
	"properties", "library", "library_version",
}

var eventsTableReady atomic.Bool

// EnsureEventsTable creates hanzo.events if absent. Idempotent; only latches on
// success so a transient datastore outage at first-write does not poison retries.
// The writer owns this DDL (the read side deliberately never creates the table).
//
// CREATES — it does not RECONCILE. `IF NOT EXISTS` returns success without looking at
// the existing table, so an schema change in eventsTableDDL reaches fresh deployments
// only. That is deliberate: the partition key cannot be ALTERed in ClickHouse at all
// (the ALTER grammar has no such command), so a self-migrating boot path could deliver
// only half a reconcile and a human would still be needed for the other half — two
// mechanisms for one migration. There is one: the reconcile in LLM.md, run once per
// deployment. A boot latch on the ingest path is also the wrong place to start an
// unbounded table rewrite.
func EnsureEventsTable(ctx context.Context) error {
	if eventsTableReady.Load() {
		return nil
	}
	if err := warehouseExec(ctx, eventsTableDDL); err != nil {
		return err
	}
	eventsTableReady.Store(true)
	return nil
}

// ── wire types ───────────────────────────────────────────────────────────────

// CaptureEvent is one client-emitted analytics event. The client sends a batch of
// these; the server owns the tenant (tenant_id is NOT a field here — it can never
// be set by the client).
type CaptureEvent struct {
	MessageID   string     `json:"messageId"`  // client idempotency id; server mints one if empty
	Type        string     `json:"type"`       // pageview | event | identify | group
	Event       string     `json:"event"`      // event name (type=event); pageview→$pageview
	Timestamp   string     `json:"timestamp"`  // RFC3339; clamped to server-now on skew/absent
	DistinctID  string     `json:"distinctId"` // resolved person/visitor id
	AnonymousID string     `json:"anonymousId"`
	PersonID    string     `json:"personId"`
	SessionID   string     `json:"sessionId"`
	Product     string     `json:"product"` // emitting surface: console|chat|app|site|admin
	URL         string     `json:"url"`
	Path        string     `json:"path"`
	Referrer    string     `json:"referrer"`
	UTM         UTM        `json:"utm"`
	RefCode     string     `json:"refCode"`
	Channel     string     `json:"channel"`
	GroupID     string     `json:"groupId"`
	SignupWeek  string     `json:"signupWeek"`
	ProductID   string     `json:"productId"`
	Quantity    uint32     `json:"quantity"`
	Revenue     float64    `json:"revenue"`
	Currency    string     `json:"currency"`
	Error       *Exception `json:"error"` // set on type:'error' events (folded into properties.$exception)

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
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
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

// eventRow is the normalized, tenant-stamped row in eventColumns order.
type eventRow struct {
	id, event, eventType                      string
	timestamp                                 time.Time
	tenant, distinctID, anonymousID, personID string
	sessionID, product, url, path             string
	referrer, referrerDomain                  string
	utmSource, utmMedium, utmCampaign         string
	utmTerm, utmContent                       string
	refCode, channel, groupID, signupWeek     string
	productID                                 string
	quantity                                  uint32
	revenue                                   float64
	currency, properties, library, libraryVer string
}

// args returns the row values in eventColumns order (positional bind).
func (r eventRow) args() []any {
	return []any{
		r.id, r.timestamp, r.tenant, r.event, r.eventType,
		r.distinctID, r.anonymousID, r.personID, r.sessionID, r.product,
		r.url, r.path, r.referrer, r.referrerDomain,
		r.utmSource, r.utmMedium, r.utmCampaign, r.utmTerm, r.utmContent,
		r.refCode, r.channel, r.groupID, r.signupWeek,
		r.productID, r.quantity, r.revenue, r.currency,
		r.properties, r.library, r.libraryVer,
	}
}

// normalizeEvent turns one client event into a tenant-stamped row. org is the
// SERVER-resolved tenant (never client input). now anchors clock-skew clamping.
// ok=false ⇒ the event is unroutable (no resolvable event name) and is dropped.
// Pure: no I/O, so tests drive it directly.
func normalizeEvent(org string, now time.Time, e CaptureEvent) (eventRow, bool) {
	name := resolveEventName(e)
	if name == "" {
		return eventRow{}, false
	}
	r := eventRow{
		id:             firstNonEmptyStr(strings.TrimSpace(e.MessageID), randID()),
		event:          name,
		eventType:      canonicalType(e.Type),
		timestamp:      clampTS(e.Timestamp, now),
		tenant:         org,
		distinctID:     trim(e.DistinctID),
		anonymousID:    trim(e.AnonymousID),
		personID:       trim(e.PersonID),
		sessionID:      trim(e.SessionID),
		product:        trim(e.Product),
		url:            trim(e.URL),
		path:           trim(e.Path),
		referrer:       trim(e.Referrer),
		referrerDomain: hostOf(e.Referrer),
		utmSource:      trim(e.UTM.Source),
		utmMedium:      trim(e.UTM.Medium),
		utmCampaign:    trim(e.UTM.Campaign),
		utmTerm:        trim(e.UTM.Term),
		utmContent:     trim(e.UTM.Content),
		refCode:        trim(e.RefCode),
		channel:        trim(e.Channel),
		groupID:        trim(e.GroupID),
		signupWeek:     trim(e.SignupWeek),
		productID:      trim(e.ProductID),
		quantity:       e.Quantity,
		revenue:        e.Revenue,
		currency:       trim(e.Currency),
		properties:     scrubProps(e.Properties),
		library:        trim(e.Library),
		libraryVer:     trim(e.LibraryVer),
	}
	return r, true
}

// resolveEventName maps the client (type,event) to the stored event name. The
// implicit types get PostHog-style reserved names so the read lens ($pageview)
// and downstream goals share ONE vocabulary. A type=event with no name is dropped.
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

// buildEventsInsert renders ONE multi-row INSERT for the batch, flattening every
// row's args. Column names are the fixed package list (never user input); values
// bind positionally through `?`. Returns ("",nil) for an empty batch.
func buildEventsInsert(rows []eventRow) (string, []any) {
	if len(rows) == 0 {
		return "", nil
	}
	ph := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(eventColumns)), ", ") + ")"
	tuples := make([]string, len(rows))
	args := make([]any, 0, len(rows)*len(eventColumns))
	for i, r := range rows {
		tuples[i] = ph
		args = append(args, r.args()...)
	}
	stmt := "INSERT INTO " + eventsTable + " (" + strings.Join(eventColumns, ", ") + ") VALUES " +
		strings.Join(tuples, ", ")
	return stmt, args
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
// its own org at full capability, and a credential-less caller gets the anonymous
// projection under publicTenant. A Host header no longer names a tenant anywhere.

// ── ONE write core ───────────────────────────────────────────────────────────

// event source tags — the ingest adapter each row arrived through. Stamped into
// properties.$source by ingestEvents so the ONE hanzo.events table stays honest
// about origin (canonical vs. deprecated wire) WITHOUT a second table or a schema
// migration: the read lenses are unchanged and $source is queryable in the
// properties JSON, which is exactly the migration signal for the alias sunset.
const (
	sourceEvent   = "event"   // canonical POST /v1/event (canonical wire)
	sourcePostHog = "posthog" // POST /v1/insights/e (PostHog wire)
	sourceCapture = "capture" // POST /v1/analytics{,/batch}, /v1/tracker (@hanzo/capture, sunsetting)
)

// withSource returns a copy of p carrying $source=source (the ingest adapter), so
// normalizeEvent's scrub+store path records origin as a property. nil-safe; never
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

// ingestEvents is the ONE write core: normalize → scrub → batch INSERT into the
// ONE hanzo.events table. org is the SERVER-resolved tenant (never client input);
// source tags the ingest adapter. Every front door — the canonical /v1/event and
// the deprecated PostHog / Segment / beacon adapters — funnels here, so there is
// exactly one write path. Returns the honest accepted/dropped receipt; the errors
// it returns are already HTTP-shaped (zip) for the handler to pass straight up.
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
	if err := EnsureEventsTable(ctx); err != nil {
		return CaptureResult{}, zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}
	now := time.Now().UTC()
	rows := make([]eventRow, 0, len(evs))
	dropped := 0
	for _, e := range evs {
		e.Properties = withSource(e.Properties, source)
		row, ok := normalizeEvent(org, now, e)
		if !ok {
			dropped++
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return CaptureResult{Dropped: dropped}, nil
	}
	stmt, args := buildEventsInsert(rows)
	if err := warehouseExec(ctx, stmt, args...); err != nil {
		return CaptureResult{}, warehouseErr("capture", err)
	}
	// Fan the accepted batch out to the downstream sink (destinations), detached and
	// fail-soft — never blocks or fails an ingest (forward.go). No-op when unset.
	fanOut(org, evs)
	return CaptureResult{Accepted: len(rows), Dropped: dropped}, nil
}

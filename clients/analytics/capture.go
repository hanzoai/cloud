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
// Routes (all POST; org resolved SERVER-SIDE from the validated principal):
//
//	POST /v1/analytics        capture one batch of events        -> {accepted,dropped}
//	POST /v1/analytics/batch  alias of the above (Segment-style)
//	POST /v1/tracker          beacon alias — navigator.sendBeacon / fetch(keepalive)
//	                          on page-unload posts here; SAME handler, SAME tenant
//	                          gate. It is a bare route: the /v1/tracker/* issue
//	                          tracker (clients/tracker) owns only /v1/tracker/projects*,
//	                          so bare POST /v1/tracker never collides with it.
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
// ONE datastore client: writes ride ai/object.DatastoreExec — the SAME pooled,
// KMS-credentialed connection the read side queries through — so there is no
// second transport, pool, or credential path.
package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxBatch bounds one ingest request so a single POST cannot pin the warehouse.
// Larger batches are rejected (400) rather than silently truncated.
const maxBatch = 500

// maxClockSkew clamps a client timestamp: a ts more than this into the FUTURE (or
// unparseable/absent) falls back to server-now, so a skewed or hostile clock can
// never park rows outside the queryable window. Past timestamps are allowed (late
// beacon flush) up to the table TTL.
const maxClockSkew = 5 * time.Minute

// eventsTableDDL is the ONE definition of the hanzo.events schema — owned here by
// the WRITER. The read lenses (query.go) SELECT a subset of these columns; keep the
// two in lockstep. MergeTree ordered for the read side's (window, tenant, event)
// predicate; 2-year TTL mirrors cloud_usage.
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
	ORDER BY (timestamp, tenant_id, event)
	TTL timestamp + INTERVAL 2 YEAR`

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
func EnsureEventsTable(ctx context.Context) error {
	if eventsTableReady.Load() {
		return nil
	}
	if err := aiobject.DatastoreExec(ctx, eventsTableDDL); err != nil {
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
	MessageID   string         `json:"messageId"`  // client idempotency id; server mints one if empty
	Type        string         `json:"type"`       // pageview | event | identify | group
	Event       string         `json:"event"`      // event name (type=event); pageview→$pageview
	Timestamp   string         `json:"timestamp"`  // RFC3339; clamped to server-now on skew/absent
	DistinctID  string         `json:"distinctId"` // resolved person/visitor id
	AnonymousID string         `json:"anonymousId"`
	PersonID    string         `json:"personId"`
	SessionID   string         `json:"sessionId"`
	Product     string         `json:"product"` // emitting surface: console|chat|app|site|admin
	URL         string         `json:"url"`
	Path        string         `json:"path"`
	Referrer    string         `json:"referrer"`
	UTM         UTM            `json:"utm"`
	RefCode     string         `json:"refCode"`
	Channel     string         `json:"channel"`
	GroupID     string         `json:"groupId"`
	SignupWeek  string         `json:"signupWeek"`
	ProductID   string         `json:"productId"`
	Quantity    uint32         `json:"quantity"`
	Revenue     float64        `json:"revenue"`
	Currency    string         `json:"currency"`
	Properties  map[string]any `json:"properties"`
	Library     string         `json:"library"`
	LibraryVer  string         `json:"libraryVersion"`
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
	default: // "event"
		return name
	}
}

// canonicalType folds the type to the closed set {pageview,identify,group,event}.
func canonicalType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "pageview", "page":
		return "pageview"
	case "identify":
		return "identify"
	case "group":
		return "group"
	default:
		return "event"
	}
}

// clampTS parses an RFC3339 timestamp and clamps a future value (> now+skew) or an
// unparseable/absent value to now. Both anchored UTC.
func clampTS(s string, now time.Time) time.Time {
	now = now.UTC()
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return now
	}
	ts = ts.UTC()
	if ts.After(now.Add(maxClockSkew)) {
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
		return emailRe.ReplaceAllString(t, "[redacted]")
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

// capture ingests one batch into hanzo.events, tenant-scoped. Shared by
// /v1/analytics, /v1/analytics/batch, and /v1/tracker.
func capture(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	var batch CaptureBatch
	if err := c.Bind(&batch); err != nil {
		return zip.ErrBadRequest("malformed capture batch")
	}
	evs := batch.events()
	if len(evs) == 0 {
		return c.JSON(http.StatusOK, CaptureResult{})
	}
	if len(evs) > maxBatch {
		return zip.ErrBadRequest("batch too large")
	}
	if err := requireDatastore(); err != nil {
		return err
	}
	ctx := c.Context()
	if err := EnsureEventsTable(ctx); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics warehouse unavailable: %v", err)
	}

	now := time.Now().UTC()
	rows := make([]eventRow, 0, len(evs))
	dropped := 0
	for _, e := range evs {
		row, ok := normalizeEvent(org, now, e)
		if !ok {
			dropped++
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return c.JSON(http.StatusOK, CaptureResult{Dropped: dropped})
	}

	stmt, args := buildEventsInsert(rows)
	if err := aiobject.DatastoreExec(ctx, stmt, args...); err != nil {
		return warehouseErr("capture", err)
	}
	return c.JSON(http.StatusOK, CaptureResult{Accepted: len(rows), Dropped: dropped})
}

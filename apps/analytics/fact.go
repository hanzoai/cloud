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

// fact.go — WHAT AN EVENT IS, once, for the whole plane.
//
// An event is the FACT. A message is the container it travels in. This file owns the
// fact; bus.go owns the container. Nothing here knows about NATS, and nothing here
// knows about SQL — normalize is pure, so the tests drive it directly.
//
// ONE TABLE, DISCRIMINATED BY A COLUMN. Every occurrence — a click, a crash, a log
// line, a span, a replay clip — is one row of event.fact, and `signal` says which sort
// it is. It was five tables with an identical envelope, which is CONSISTENT but not
// UNIFIED: no cross-signal question could be asked without a five-way UNION ALL, and a
// new product meant a new table name, which is how a namespace grows a `_v2`.
//
// A SIGNAL IS A VALUE, NOT A PLACE. That is the whole of the change. `clip` — the
// session-replay index — is the worked example: it costs one signal value and two
// columns (object, bytes), reuses duration/session_id/url, and adds no name to the
// namespace at all. Under the old shape it would have been a table, and then a
// session-summary table, and then a partition-statistics table beside it.
//
// SIGNAL AND KIND ARE DIFFERENT THINGS, and conflating them is the mistake this file
// exists to prevent. The SIGNAL picks the partition (and the subject, and the durable).
// The KIND is a COLUMN — the discriminator WITHIN a signal:
//
//   - on act, kind is track | page | identify | group. Those are things a CALLER DOES
//     (event.Track(), event.Page()), not durable types, so they are a column value and
//     never a table. `name` carries the specific (button_clicked, page_viewed).
//   - on span, kind is the OTel span kind (server | client | internal | …).
//   - on error, log and clip the caller owns the sub-vocabulary, so kind is whatever it
//     stated and otherwise EMPTY. Defaulting it to the signal name would make a column
//     that always equals its own discriminator — information-free.
//
// TWO GRAINS, TWO TABLES, AND NO MORE. An occurrence HAPPENED; a sample was MEASURED.
// They are 326:1 in row count, they key differently (a sample has no id, no name, no
// session) and rate()/increase() need a fingerprint-major ordering that no time-ordered
// key can express. So `sample` lands in event.sample and everything else in event.fact
// — see writers (warehouse.go), which is the one place that mapping is written down.
//
// The org is NOT on the wire. It is stamped here from the SERVER-resolved tenant, so a
// caller can only ever write into its own partition — the one tenancy invariant, in the
// one place a row is built.

package analytics

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// plane is the database, the subject root and — with the case NATS conventionally
// gives a stream — the stream name. ONE word at three layers: no brand ("hanzo."), no
// numeronym ("o11y_"), no product name ("insights"/"analytics"), no version suffix.
const plane = "event"

// signal names the KIND OF FACT. It picks the subject, the durable and the partition.
// It is not the `kind` column — see the file header. The zero value is deliberately not
// a valid signal so an unrouted fact cannot silently become an act.
type signal string

const (
	// signalAct is something a person or a surface DID: track | page | identify |
	// group. It is `act` and not `event` because `event` is the NAMESPACE — a value
	// cannot also be the set it belongs to, and `event.fact WHERE signal='event'` is
	// exactly the stutter that reads as a schema nobody finished naming.
	signalAct signal = "act"
	// signalClip is one recorded slice of a session. The row is the INDEX — where the
	// blob lives and how big it is. The blob itself never travels on the bus and never
	// lands in a column: it is a multi-megabyte time-ordered binary, wrong for a
	// message and wrong for a row.
	signalClip signal = "clip"
	// signalError is a thrown or reported failure.
	signalError signal = "error"
	// signalLog is a log record.
	signalLog signal = "log"
	// signalSpan is one span of a trace.
	signalSpan signal = "span"
	// signalSample is one measurement. Different grain, different table — see the
	// header, and writers (warehouse.go) for why the door refuses it today.
	signalSample signal = "sample"
)

// subject is the bus subject a signal travels on, and it is the durable name a consumer
// binds. Derived from the signal so a new signal cannot be published to a name nothing
// filters for.
//
// It is no longer also the TABLE name. It was, and that was the right shape when a
// signal was a table; now one table holds five signals, so what the two share is the
// discriminator VALUE rather than the identifier. The sink says where a signal lands,
// once, in writers.
func (s signal) subject() string { return plane + "." + string(s) }

// The two tables of the plane, named for their GRAIN. Everything else in the namespace
// is a rollup of one of them or a dimension beside it.
const (
	factTable   = plane + ".fact"   // one row per thing that HAPPENED
	sampleTable = plane + ".sample" // one row per thing MEASURED
)

// The `kind` vocabulary of act: what a caller DID. These are column values.
const (
	kindTrack    = "track"
	kindPage     = "page"
	kindIdentify = "identify"
	kindGroup    = "group"
)

// kindInternal is OTel's default span kind, used when a span states none.
const kindInternal = "internal"

// route is the pair a wire event resolves to: the signal that picks the partition, and
// the kind column on it. It is a comparable value so the anonymous lane's allowlist is a
// map lookup rather than a chain of conditions (public.go).
type route struct {
	signal signal
	kind   string
}

// spell is the route's CANONICAL wire spelling — the one value routeOf maps back to
// this exact route. It is what lets the anonymous projection (public.go) restate an
// admitted event in the closed vocabulary the allowlist just approved: rebuild with
// spell() and the event routes identically, carrying no other caller spelling along.
// routeSpellRoundTrips pins that inverse.
func (r route) spell() string {
	if r.signal != signalAct {
		return string(r.signal)
	}
	switch r.kind {
	case kindPage:
		return "page"
	case kindIdentify:
		return kindIdentify
	case kindGroup:
		return kindGroup
	default:
		return "event"
	}
}

// routeOf maps the wire's ONE `type` field onto a route. `type` selects the ROUTE and
// nothing else — one field, one job; a signal's own body may refine the kind afterwards
// (a span's OTel kind). An unknown or absent type is a tracked act, which is what every
// pre-existing caller meant by omitting it.
//
// The wire word `event` is still accepted and still means an act: it is what every
// deployed client sends and it is a PUBLISHED spelling, so it maps rather than moves.
// What changed is the name of the thing it maps ONTO.
func routeOf(e CaptureEvent) route {
	t := strings.ToLower(strings.TrimSpace(e.Type))
	// AN EVENT CARRYING AN EXCEPTION IS AN ERROR, whatever it called itself — and a
	// caller that attached one and named no type meant exactly that. This is where the
	// old fold decided it; deciding it in routeOf instead means the ROUTE is a pure
	// function of the wire, with nothing mutating the event on its way past.
	if t == "" && e.Error != nil {
		return route{signal: signalError}
	}
	switch t {
	case "pageview", "page":
		return route{signalAct, kindPage}
	case "identify":
		return route{signalAct, kindIdentify}
	case "group":
		return route{signalAct, kindGroup}
	case "error", "exception":
		return route{signal: signalError}
	case "log":
		return route{signal: signalLog}
	case "span":
		return route{signalSpan, kindInternal}
	case "clip", "replay":
		return route{signal: signalClip}
	case "metric", "sample":
		return route{signal: signalSample}
	default:
		return route{signalAct, kindTrack}
	}
}

// annotation is the @hanzo/observe AST annotation, stored as the `el` named tuple. It
// is a NAMESPACE, not six more envelope columns: $name and $path collide head-on with
// the envelope's own name and path, and a named tuple keeps each element its own
// subcolumn — el.role reads exactly like a flat column while `name` stays the event's.
type annotation struct {
	label     string
	role      string
	testid    string
	name      string
	component string
	path      []string
}

// empty reports whether the caller annotated nothing, so the container can omit the
// tuple entirely rather than shipping six empty strings per message.
func (a annotation) empty() bool {
	return a.label == "" && a.role == "" && a.testid == "" &&
		a.name == "" && a.component == "" && len(a.path) == 0
}

// frame is one stack frame, stored across the parallel frames.* arrays. `own` marks
// first-party code, which is what makes an issue list readable: a browser extension or
// a vendor bundle at the top of a stack is noise, not the fault's location.
type frame struct {
	function string
	file     string
	line     uint32
	column   uint32
	own      bool
}

// sample is what event.sample carries. It is normalized and PUBLISHED like every other
// signal, but it lands in the OTHER table — see writers (warehouse.go) for what the
// door does with it today and what makes it landable.
type sample struct {
	metric string
	value  float64
	labels map[string]string
}

// fact is one normalized occurrence, FLAT, because the table is flat. Its fields are
// the column names.
//
// It was an envelope plus one of four optional bodies, which was the right shape when
// each body had its own table. With one table a body is just the subset of columns a
// signal populates, and a pointer per signal would be a second description of the same
// thing — the one every reader would then have to hold alongside the columns.
//
// SPARSITY IS NOT A COST, measured rather than assumed: on the live event.log
// (798,375 rows) an unpopulated column costs 515 bytes for the WHOLE table. Six of
// them is ~3 KiB against 48 MiB. The instinct that a wide row wastes space is a
// row-store instinct.
type fact struct {
	// ── spine ────────────────────────────────────────────────────────────────
	org    string // THE tenant: the IAM org slug, stamped server-side. Never on the wire.
	signal signal
	time   time.Time
	id     string

	// ── what ─────────────────────────────────────────────────────────────────
	name string
	kind string
	// message is one column because a log's BODY is an error's MESSAGE: the human
	// text of what happened. Two names for it is how two spellings of one fact begin.
	message string
	// severity is the OTLP number (1..24) and the ONLY spelling of it. A row cannot
	// carry a number and a word that disagree; the word is severityText(), read-time.
	severity uint8
	duration uint64 // nanoseconds — a span's, and a clip's

	// ── where ────────────────────────────────────────────────────────────────
	product string // the emitting SURFACE. A Sentry "project" is this.
	env     string
	service string
	release string
	url     string
	path    string

	// ── who ──────────────────────────────────────────────────────────────────
	person    string
	distinct  string
	anonymous string
	// groups is group-type -> group-key. A Map, not group0..group4: five positional
	// slots are a sixth slot waiting to become a `_v2`.
	groups map[string]string

	// ── correlation ──────────────────────────────────────────────────────────
	session  string
	trace    string
	span     string
	parent   string
	resource string // resource fingerprint; joins the *_resource dimensions

	// ── open ─────────────────────────────────────────────────────────────────
	attributes map[string]string
	el         annotation

	// ── error ────────────────────────────────────────────────────────────────
	issue   string // the deterministic grouping fingerprint — see fingerprint()
	class   string
	origin  string // the place of the fault (Sentry's culprit)
	handled bool
	frames  []frame

	// ── span ─────────────────────────────────────────────────────────────────
	status string

	// ── clip ─────────────────────────────────────────────────────────────────
	object string // object-store address of the blob. The blob is never in the row.
	bytes  uint64

	// ── the other grain ──────────────────────────────────────────────────────
	// sample is the ONE body that is not a subset of the occurrence columns, because
	// a measurement is not an occurrence: it has a value and no identity. It lands in
	// event.sample, so it is carried as a pointer rather than flattened into columns
	// that would be empty on every occurrence row.
	sample *sample
}

// normalize turns one wire event into the fact it names. org is the SERVER-resolved
// tenant (never client input); now anchors the clock clamp. ok=false ⇒ the event is
// unroutable and is dropped into the honest receipt.
//
// Pure: no I/O, no clock, no randomness beyond the id the caller may have supplied — so
// the tests drive it directly, which is what keeps the tenancy stamp observable.
func normalize(org string, now time.Time, e CaptureEvent) (fact, bool) {
	r := routeOf(e)
	name := resolveName(r, e)
	if name == "" {
		return fact{}, false
	}
	props := scrubMap(e.Properties)
	f := fact{
		signal:    r.signal,
		org:       org,
		time:      clampTS(e.Timestamp, now),
		id:        cmp.Or(strings.TrimSpace(e.MessageID), randID()),
		name:      name,
		kind:      cmp.Or(trim(e.Kind), r.kind),
		product:   trim(e.Product),
		session:   trim(e.SessionID),
		distinct:  trim(e.DistinctID),
		anonymous: trim(e.AnonymousID),
		person:    trim(e.PersonID),
		url:       trim(e.URL),
		path:      trim(e.Path),
		el:        annotationOf(props),

		// The qualifiers every signal shares. They were duplicated onto the error body
		// alone, which is what let sentry.hanzo.ai group faults by release while the
		// same fact on the event stream had no release at all.
		env:     trim(e.Environment),
		service: trim(e.Service),
		release: trim(e.Release),
		origin:  trim(e.Site),
		trace:   trim(e.TraceID),
		span:    trim(e.SpanID),
		groups:  groupsOf(e),
	}
	// Everything that is not a column and not the annotation travels in attributes.
	// The old wide table gave utm_*, referrer, revenue, channel and the rest their own
	// columns; they are the same facts under the same names, in the one place a
	// caller's own vocabulary belongs.
	f.attributes = attributesOf(props, e)

	switch r.signal {
	case signalError:
		applyFault(&f, e)
	case signalLog:
		applyRecord(&f, e)
	case signalSpan:
		applySpan(&f, e)
	case signalClip:
		applyClip(&f, e)
	case signalSample:
		f.sample = sampleOf(e)
	}
	return f, true
}

// The ROUTE's server-chosen default names, declared once because two files decide with
// them: resolveName picks them when the caller named nothing, and the anonymous lane's
// kind table (publicKinds, public.go) stores them outright. Naming them here is what
// keeps those two from drifting into two spellings of one name.
//
// They are plain verb-object names. The old sentinels ($pageview, $error) were PostHog
// jargon standing in for a discriminator the schema now has: a page view is `kind =
// page`, which is what a reader filters on, so the magic name is no longer load-bearing
// anywhere.
const (
	namePageView = "page_viewed"
	nameError    = "error"
	nameIdentify = "user_identified"
	nameGroup    = "group_identified"
	nameLog      = "log_record"
	nameSpan     = "span"
	nameClip     = "clip"
)

// resolveName picks the stored event name. A tracked act MUST name itself (an unnamed
// one is unroutable and dropped — the pre-existing rule); every other route has a
// server-chosen default, so a caller that names nothing cannot leave the row unnamed
// and cannot choose what it is called.
//
// AN ERROR IS NAMED `error`, NEVER ITS EXCEPTION CLASS. This branch used to fall back to
// e.Error.Type, and that was a caller string on a function the ANONYMOUS lane reaches:
// `{"type":"error","error":{"type":"…"}}` with no credential wrote 60 KiB of chosen bytes
// into `name`, fifty distinct per request, and on a published-site host into a real org's
// partition — unbounded cardinality in the column the plane indexes, from a caller
// nobody vouched for.
//
// Dropping it costs nothing, which is why the fix belongs here and not in a per-lane
// special case. The class was never this column's fact to hold: it is stored in `class`,
// it is the first thing fingerprint() hashes into `issue`, and the error lens surfaces
// it from attributes['$exception']. Naming the row after it was a THIRD copy of one
// fact under a third spelling.
func resolveName(r route, e CaptureEvent) string {
	if n := strings.TrimSpace(e.Event); n != "" {
		return n
	}
	switch r.signal {
	case signalError:
		return nameError
	case signalLog:
		return nameLog
	case signalSpan:
		return nameSpan
	case signalClip:
		return nameClip
	case signalSample:
		if e.Metric != nil {
			return trim(e.Metric.Name)
		}
		return ""
	}
	switch r.kind {
	case kindPage:
		return namePageView
	case kindIdentify:
		return nameIdentify
	case kindGroup:
		return nameGroup
	}
	return "" // a tracked act with no name is unroutable
}

// ── severity, one spelling ───────────────────────────────────────────────────
//
// OTLP numbers severity 1..24 in bands of four: TRACE 1-4, DEBUG 5-8, INFO 9-12,
// WARN 13-16, ERROR 17-20, FATAL 21-24. The NUMBER is what is stored, because it
// orders, it fits a UInt8 minmax index, and it survives a client that spells the word
// differently. The word is a function of it and is never stored beside it — a row
// carrying both is a row that can contradict itself, which is precisely what
// `severity_text` + `severity_number` + `level` was.

const (
	severityTrace = 1
	severityDebug = 5
	severityInfo  = 9
	severityWarn  = 13
	severityError = 17
	severityFatal = 21
)

// severityOf resolves the ONE stored number from whatever the caller sent. An explicit
// in-range number wins — it is the precise form. Otherwise the word is mapped by its
// band, and an unrecognized word yields fallback rather than 0, so a signal whose
// severity is meaningful (an error) can never be stored as "unspecified".
func severityOf(text string, number uint8, fallback uint8) uint8 {
	if number > 0 && number <= 24 {
		return number
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "trace":
		return severityTrace
	case "debug":
		return severityDebug
	case "info", "information", "notice", "log":
		return severityInfo
	case "warn", "warning":
		return severityWarn
	case "error", "err", "severe":
		return severityError
	case "fatal", "critical", "crit", "panic", "emergency", "alert":
		return severityFatal
	}
	return fallback
}

// severityText is the read-time inverse: the band's word. It is the only place a
// severity becomes text, so a lens and a log line cannot disagree about what 13 means.
func severityText(n uint8) string {
	switch {
	case n == 0:
		return ""
	case n < severityDebug:
		return "trace"
	case n < severityInfo:
		return "debug"
	case n < severityWarn:
		return "info"
	case n < severityError:
		return "warn"
	case n < severityFatal:
		return "error"
	default:
		return "fatal"
	}
}

// ── per-signal bodies ────────────────────────────────────────────────────────

// annotationKeys are the @hanzo/observe AST properties, lifted OUT of the property bag
// into the `el` tuple so they are not stored twice under two spellings.
var annotationKeys = []string{"$el", "$role", "$testid", "$name", "$component", "$path"}

// annotationOf lifts the AST annotation out of the scrubbed property bag. It READS the
// map; attributesOf then skips the same keys, so each annotation fact lands in exactly
// one place.
func annotationOf(props map[string]any) annotation {
	if len(props) == 0 {
		return annotation{}
	}
	return annotation{
		label:     asStr(props["$el"]),
		role:      asStr(props["$role"]),
		testid:    asStr(props["$testid"]),
		name:      asStr(props["$name"]),
		component: asStr(props["$component"]),
		path:      strSlice(props["$path"]),
	}
}

// groupsOf builds the group membership map. The wire carries ONE group today
// (`groupId`), so the map holds one entry — but it is a MAP and not a column because
// the second group type is a data change and not a schema change. That is the whole
// difference between this and the group0..group4 slots it replaces.
func groupsOf(e CaptureEvent) map[string]string {
	id := trim(e.GroupID)
	if id == "" {
		return nil
	}
	kind := trim(e.GroupType)
	if kind == "" {
		kind = "organization"
	}
	return map[string]string{kind: id}
}

// attributesOf flattens the scrubbed property bag plus the wire's own attribution
// fields into the attributes map. Values are strings because the column is
// Map(LowCardinality(String), String); a non-scalar is stored as compact JSON so it is
// still readable, and a numeric still parses back through toFloat64OrZero() at read
// time (the read lenses do exactly that for revenue and quantity).
//
// Empty values are OMITTED rather than stored blank: an absent key and an empty one
// read identically out of a datastore Map, so writing the blank buys nothing and costs
// a key in every row's LowCardinality dictionary.
func attributesOf(props map[string]any, e CaptureEvent) map[string]string {
	out := make(map[string]string, len(props)+8)
	set := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	for k, v := range props {
		if isAnnotationKey(k) {
			continue
		}
		set(k, asStr(v))
	}
	set("referrer", trim(e.Referrer))
	set("referrer_domain", hostOf(e.Referrer))
	set("utm_source", trim(e.UTM.Source))
	set("utm_medium", trim(e.UTM.Medium))
	set("utm_campaign", trim(e.UTM.Campaign))
	set("utm_term", trim(e.UTM.Term))
	set("utm_content", trim(e.UTM.Content))
	set("ref_code", trim(e.RefCode))
	set("channel", trim(e.Channel))
	set("signup_week", trim(e.SignupWeek))
	set("product_id", trim(e.ProductID))
	set("currency", trim(e.Currency))
	set("library", trim(e.Library))
	set("library_version", trim(e.LibraryVer))
	if e.Quantity > 0 {
		set("quantity", strconv.FormatUint(uint64(e.Quantity), 10))
	}
	if e.Revenue != 0 {
		set("revenue", strconv.FormatFloat(e.Revenue, 'f', -1, 64))
	}
	return out
}

func isAnnotationKey(k string) bool {
	for _, a := range annotationKeys {
		if k == a {
			return true
		}
	}
	return false
}

// applyFault fills the error columns and computes the grouping fingerprint.
//
// GROUPING IS COMPUTED HERE, ISSUE LIFECYCLE IS NOT. `issue` is a pure function of the
// failure's shape, which makes it enrichment and puts it on the ingest path. What a
// downstream consumer owns is the ISSUE ROW: status, assignee, first_seen, count, keyed
// (org, issue). That is the one non-telemetry concept in this plane and it stays
// relational; nothing about it belongs in a columnar fact.
func applyFault(f *fact, e CaptureEvent) {
	// ONE scrub, at the ONE point the exception enters a fact: scrubException copies
	// and redacts the free text (a stack frame carries API URLs with query secrets and
	// PII as readily as a message does), and everything below reads the copy — so
	// nothing unredacted can reach a column or the bus.
	if ex := scrubException(e.Error); ex != nil {
		f.message = ex.Message
		f.class = trim(ex.Type)
		// handled is absent on most wires. Absent reads as UNHANDLED: under-reporting
		// severity hides a crash, over-reporting merely makes an issue list noisier.
		f.handled = ex.Handled != nil && *ex.Handled
		f.frames = framesOf(ex)
	}
	// An error with no stated level IS an error. That is what makes the fallback
	// severityError rather than zero: a failure stored as "unspecified" sorts below
	// every warning in the one list that exists to surface it.
	f.severity = severityOf(e.Level, 0, severityError)
	f.issue = fingerprint(f)
}

// applyRecord fills the log columns.
func applyRecord(f *fact, e CaptureEvent) {
	f.resource = trim(e.Resource)
	if e.Log != nil {
		f.message = scrubText(e.Log.Body)
		f.severity = severityOf(cmp.Or(e.Log.Severity, e.Level), e.Log.Number, 0)
		return
	}
	f.severity = severityOf(e.Level, 0, 0)
}

// applySpan fills the span columns. A span with no trace id is still stored: dropping
// an orphan span would hide a real observation, and the fact table is ordered by time
// rather than by trace, so it costs nothing to keep.
func applySpan(f *fact, e CaptureEvent) {
	f.resource = trim(e.Resource)
	if e.Span == nil {
		return
	}
	f.parent = trim(e.Span.Parent)
	f.duration = e.Span.Duration
	f.status = strings.ToLower(trim(e.Span.Status))
	if k := trim(e.Span.Kind); k != "" {
		f.kind = strings.ToLower(k)
	}
	if f.span == "" {
		f.span = trim(e.Span.ID)
	}
	if f.trace == "" {
		f.trace = trim(e.Span.Trace)
	}
}

// applyClip fills the two columns a replay clip costs. THE BLOB IS NOT ONE OF THEM:
// `object` is its address in object storage, and the bytes stay there. A multi-megabyte
// binary is wrong for a bus message and wrong for a warehouse row, and the answer to
// "where does the blob go then" is: the same place it already goes.
func applyClip(f *fact, e CaptureEvent) {
	if e.Clip == nil {
		return
	}
	f.object = trim(e.Clip.Object)
	f.bytes = e.Clip.Bytes
	f.duration = e.Clip.Duration
}

// sampleOf builds the measurement body.
func sampleOf(e CaptureEvent) *sample {
	if e.Metric == nil {
		return &sample{}
	}
	return &sample{
		metric: trim(e.Metric.Name),
		value:  e.Metric.Value,
		labels: strMap(e.Metric.Labels),
	}
}

// fingerprint is the deterministic grouping key: the same failure shape always yields
// the same value, in any process, with no state. It prefers the FIRST-PARTY frame —
// two failures thrown from the same line of our code are one issue even when the
// message differs — and falls back to the message with its variable parts removed so
// "user 41 not found" and "user 907 not found" do not become two issues.
func fingerprint(f *fact) string {
	h := sha256.New()
	_, _ = h.Write([]byte(f.class))
	_, _ = h.Write([]byte{0})
	if fr, ok := firstOwn(f.frames); ok {
		_, _ = h.Write([]byte(fr.function))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(fr.file))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strconv.FormatUint(uint64(fr.line), 10)))
	} else {
		_, _ = h.Write([]byte(shape(f.message)))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// firstOwn returns the first first-party frame, else the first frame at all, so a fault
// with only vendor frames still groups by its own top frame rather than by message.
func firstOwn(frames []frame) (frame, bool) {
	for _, fr := range frames {
		if fr.own {
			return fr, true
		}
	}
	if len(frames) > 0 {
		return frames[0], true
	}
	return frame{}, false
}

// shape strips the variable parts of a message so one failure is one issue: every run
// of digits collapses to 0, and every long hex run (ids, hashes, uuids) to x.
func shape(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	run := 0
	isHex := true
	flush := func() {
		if run == 0 {
			return
		}
		if isHex && run >= 8 {
			b.WriteByte('x')
		} else {
			b.WriteByte('0')
		}
		run, isHex = 0, true
	}
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		switch {
		case c >= '0' && c <= '9':
			run++
		case (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
			if run > 0 {
				run++
			} else {
				flush()
				b.WriteByte(c)
			}
		default:
			flush()
			b.WriteByte(c)
		}
	}
	flush()
	return b.String()
}

// ── stack frames ─────────────────────────────────────────────────────────────

// framesOf returns the fault's frames: the STRUCTURED ones when the client sent them
// (the honest wire — the client knows its own source map), else the ones parsed out of
// the text stack, which is what every browser SDK actually emits today.
func framesOf(ex *Exception) []frame {
	if len(ex.Frames) > 0 {
		out := make([]frame, 0, min(len(ex.Frames), maxFrames))
		for _, f := range ex.Frames {
			if len(out) >= maxFrames {
				break
			}
			// A file is a URL and can carry a token in its query, so it is scrubbed
			// like every other free-text field rather than trusted for being a path.
			file := scrubText(trim(f.File))
			out = append(out, frame{
				function: trim(f.Function),
				file:     file,
				line:     f.Line,
				column:   f.Column,
				own:      ownFile(file),
			})
		}
		return out
	}
	return parseStack(ex.Stack)
}

// maxFrames bounds one fault's stored stack. A runaway recursion produces thousands of
// frames, and the columns are parallel arrays in a row — the tail is never read.
const maxFrames = 64

// parseStack lifts frames out of a text stack. It reads the two forms every browser and
// Node emit — V8's `at fn (file:line:col)` and SpiderMonkey's `fn@file:line:col` — and
// silently skips a line that is neither, so a stack it does not recognize costs nothing
// (the fault still stores, and still groups by its message shape).
func parseStack(stack string) []frame {
	if stack == "" {
		return nil
	}
	var out []frame
	for _, line := range strings.Split(stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(out) >= maxFrames {
			continue
		}
		fn, loc, ok := splitFrame(line)
		if !ok {
			continue
		}
		file, ln, col := splitLocation(loc)
		if file == "" {
			continue
		}
		out = append(out, frame{function: fn, file: file, line: ln, column: col, own: ownFile(file)})
	}
	return out
}

// splitFrame separates a stack line into its function and its location.
func splitFrame(line string) (fn, loc string, ok bool) {
	if rest, cut := strings.CutPrefix(line, "at "); cut { // V8: at fn (loc) | at loc
		if i := strings.LastIndex(rest, " ("); i >= 0 && strings.HasSuffix(rest, ")") {
			return strings.TrimSpace(rest[:i]), rest[i+2 : len(rest)-1], true
		}
		return "", strings.TrimSpace(rest), true
	}
	if i := strings.LastIndex(line, "@"); i >= 0 { // SpiderMonkey: fn@loc
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
	}
	return "", "", false
}

// splitLocation splits `file:line:column` from the RIGHT, because the file is a URL and
// carries its own colons (https://host:443/a.js:12:3).
func splitLocation(loc string) (file string, line, col uint32) {
	file = loc
	for i := 0; i < 2; i++ {
		j := strings.LastIndex(file, ":")
		if j < 0 {
			break
		}
		n, err := strconv.ParseUint(file[j+1:], 10, 32)
		if err != nil {
			break
		}
		file = file[:j]
		if i == 0 {
			col = uint32(n)
		} else {
			line = uint32(n)
		}
	}
	// A single trailing number is the LINE, not the column.
	if line == 0 && col != 0 {
		line, col = col, 0
	}
	return strings.TrimSpace(file), line, col
}

// vendorPath marks the file prefixes and segments that are NOT first-party: browser
// extensions injecting into the page (the single loudest source of noise in a real
// issue list — a wallet extension's inpage.js is not our bug) and vendored code.
var vendorPath = []string{
	"chrome-extension://", "moz-extension://", "safari-extension://", "safari-web-extension://",
	"/node_modules/", "<anonymous>", "native code",
}

// ownFile reports whether a frame is first-party.
func ownFile(file string) bool {
	if file == "" {
		return false
	}
	low := strings.ToLower(file)
	for _, v := range vendorPath {
		if strings.Contains(low, v) {
			return false
		}
	}
	return true
}

// ── small pure helpers ───────────────────────────────────────────────────────

// strSlice coerces a property value to a string slice (the annotation's $path).
func strSlice(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, asStr(e))
		}
		return out
	default:
		if s := asStr(v); s != "" {
			return []string{s}
		}
		return nil
	}
}

// strMap flattens a wire map to string values, matching the store's Map(…, String).
func strMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = asStr(v)
	}
	return out
}

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
// ONE NAME FOR A THING, ACROSS TRANSPORT AND STORAGE. A signal's subject and its table
// are the same string, derived from the same constant, so they cannot drift:
//
//	subject event.error  -> table event.error
//	subject event.span   -> table event.span
//	subject event.log    -> table event.log
//	subject event.event  -> table event.event
//	subject event.metric -> table event.metric
//
// SIGNAL AND KIND ARE DIFFERENT THINGS, and conflating them is the mistake this file
// exists to prevent. The SIGNAL picks the table (and the subject). The KIND is a COLUMN
// on it — the discriminator WITHIN a signal:
//
//   - on event.event, kind is track | page | identify | group. Those are things a CALLER
//     DOES (event.Track(), event.Page()), not durable types, so they are a column value
//     and never a table. `name` carries the specific (button_clicked, page_viewed).
//   - on event.span, kind is the OTel span kind (server | client | internal | …).
//   - on event.error and event.log the caller owns the sub-vocabulary, so kind is
//     whatever it stated and otherwise EMPTY. Defaulting it to the signal name would
//     make a column that always equals its own table — information-free.
//
// SEPARATE TABLES, ONE ENVELOPE. Each table has its own ORDER BY because each is read
// differently (event by (org,time) for funnels, error by (org,group,time) for issue
// lists, log by (org,service,time), span by (org,trace_id) to assemble a trace) — the
// sort key is the reason they are separate, not sparse columns, which the store
// compresses away. They are held together by an IDENTICAL envelope: same names, same
// semantics, so a cross-signal correlation is a UNION ALL and not a translation layer.
//
// The org is NOT on the wire. It is stamped here from the SERVER-resolved tenant, so a
// caller can only ever write into its own partition — the one tenancy invariant, in the
// one place a row is built.

package analytics

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// plane is the database, the subject root and — with the case NATS conventionally
// gives a stream — the stream name. ONE word at three layers: no brand ("hanzo."), no
// numeronym ("o11y_"), no product name ("insights"/"analytics"), no version suffix.
// A query reads FROM error because the connection's default database is this one.
const plane = "event"

// signal names the KIND OF FACT, which is what picks a table and a subject. It is not
// the `kind` column — see the file header. The zero value is deliberately not a valid
// signal so an unrouted fact cannot silently become an event.
type signal string

const (
	signalEvent  signal = "event"  // a product event: track | page | identify | group
	signalError  signal = "error"  // a thrown/reported failure
	signalLog    signal = "log"    // a log record
	signalSpan   signal = "span"   // one span of a trace
	signalMetric signal = "metric" // one metric sample
)

// subject is the bus subject this signal travels on, and table is the warehouse table
// it lands in. They are the SAME name by construction — one string, two layers — which
// is the whole point of deriving both here instead of writing either down twice.
func (s signal) subject() string { return plane + "." + string(s) }
func (s signal) table() string   { return plane + "." + string(s) }

// The `kind` vocabulary of event.event: what a caller DID. These are column values.
const (
	kindTrack    = "track"
	kindPage     = "page"
	kindIdentify = "identify"
	kindGroup    = "group"
)

// kindInternal is OTel's default span kind, used when a span states none.
const kindInternal = "internal"

// route is the pair a wire event resolves to: the signal that picks the table, and the
// kind column on it. It is a comparable value so the anonymous lane's allowlist is a
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
	if r.signal != signalEvent {
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
		return string(signalEvent)
	}
}

// routeOf maps the wire's ONE `type` field onto a route. `type` selects the ROUTE and
// nothing else — one field, one job; a signal's own body may refine the kind afterwards
// (a span's OTel kind, an error's level). An unknown or absent type is a tracked
// product event, which is what every pre-existing caller meant by omitting it.
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
		return route{signalEvent, kindPage}
	case "identify":
		return route{signalEvent, kindIdentify}
	case "group":
		return route{signalEvent, kindGroup}
	case "error", "exception":
		return route{signal: signalError}
	case "log":
		return route{signal: signalLog}
	case "span":
		return route{signalSpan, kindInternal}
	case "metric":
		return route{signal: signalMetric}
	default:
		return route{signalEvent, kindTrack}
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

// envelope is the IDENTICAL 15-column head of every signal — same names, same
// semantics, in the same positions. ingested_at is NOT here: the server stamps it
// through a column DEFAULT, so nothing on the wire can influence when a row expires
// (retention is measured from it). That is the same reason it was kept off the old
// insert list, and it is the only column that can carry a TTL honestly.
type envelope struct {
	org        string
	time       time.Time
	id         string
	name       string
	kind       string
	product    string
	session    string
	distinct   string
	anonymous  string
	person     string
	url        string
	path       string
	attributes map[string]string
	el         annotation
}

// fault is what event.error adds to the envelope: the failure's identity (class,
// message, the grouping fingerprint) and its frames.
type fault struct {
	group       string // the deterministic fingerprint — see fingerprint()
	message     string
	class       string
	site        string
	handled     bool
	level       string
	release     string
	environment string
	service     string
	trace       string
	span        string
	frames      []frame
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

// record is what event.log adds: the OTel log-record fields.
type record struct {
	service  string
	severity string
	number   uint8
	body     string
	trace    string
	span     string
	resource uint64
}

// span is what event.span adds: the trace linkage and the timing.
type span struct {
	service  string
	trace    string
	id       string
	parent   string
	duration uint64 // nanoseconds
	status   string
}

// sample is what event.metric carries. It is normalized and PUBLISHED like every other
// signal, but it is deliberately NOT warehoused — see writers (warehouse.go) for why
// and for the exact fix.
type sample struct {
	metric string
	value  float64
	labels map[string]string
}

// fact is one normalized signal: the shared envelope, the signal that routes it, and
// exactly the one body that signal carries. Every lane produces these and nothing else,
// so "what is an event" has a single answer on the wire, on the bus and in the store.
type fact struct {
	envelope
	signal signal
	fault  *fault
	record *record
	span   *span
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
		signal: r.signal,
		envelope: envelope{
			org:       org,
			time:      clampTS(e.Timestamp, now),
			id:        firstNonEmptyStr(strings.TrimSpace(e.MessageID), randID()),
			name:      name,
			kind:      firstNonEmptyStr(trim(e.Kind), r.kind),
			product:   trim(e.Product),
			session:   trim(e.SessionID),
			distinct:  trim(e.DistinctID),
			anonymous: trim(e.AnonymousID),
			person:    trim(e.PersonID),
			url:       trim(e.URL),
			path:      trim(e.Path),
			el:        annotationOf(props),
		},
	}
	// Everything that is not an envelope column and not the annotation travels in
	// attributes. The old wide table gave utm_*, referrer, revenue, channel and the
	// rest their own columns; they are the same facts under the same names, in the one
	// place a caller's own vocabulary belongs.
	f.attributes = attributesOf(props, e)

	switch r.signal {
	case signalError:
		f.fault = faultOf(e)
	case signalLog:
		f.record = recordOf(e)
	case signalSpan:
		f.span = spanOf(e)
		if e.Span != nil && trim(e.Span.Kind) != "" {
			f.kind = strings.ToLower(trim(e.Span.Kind))
		}
	case signalMetric:
		f.sample = sampleOf(e)
	}
	return f, true
}

// resolveName picks the stored event name. A tracked product event MUST name itself
// (an unnamed one is unroutable and dropped — the pre-existing rule); every other route
// has a server-chosen default, so a caller that names nothing cannot leave the row
// unnamed AND cannot choose what it is called.
//
// AN ERROR IS NAMED `error`, NEVER ITS EXCEPTION CLASS. This branch used to fall back to
// e.Error.Type, and that was a caller string on the one function the ANONYMOUS lane
// leans on for its whole name rule: `{"type":"error","error":{"type":"…"}}` with no
// credential wrote chosen bytes into `name`, fifty distinct per request, and on a
// published-site host into a REAL org's partition — unbounded cardinality in a column
// every signal here treats as low-cardinality, from a caller nobody vouched for.
//
// Dropping the fallback costs nothing, which is why the fix belongs here rather than in
// a per-lane special case: the class was never this column's fact to hold. It is stored
// in the fault's own `class`, it is the first thing fingerprint() hashes into `group`,
// and the error lens reads it back from attributes['$exception']. Naming the row after
// it was a third copy of one fact under a third spelling.
//
// The defaults are plain verb-object names. The old sentinels ($pageview, $error) were
// PostHog jargon standing in for a discriminator the schema now has: a page view is
// `kind = page`, which is what a reader filters on, so the magic name is no longer
// load-bearing anywhere.
func resolveName(r route, e CaptureEvent) string {
	if n := strings.TrimSpace(e.Event); n != "" {
		return n
	}
	switch r.signal {
	case signalError:
		return "error"
	case signalLog:
		return "log_record"
	case signalSpan:
		return "span"
	case signalMetric:
		if e.Metric != nil {
			return trim(e.Metric.Name)
		}
		return ""
	}
	switch r.kind {
	case kindPage:
		return "page_viewed"
	case kindIdentify:
		return "user_identified"
	case kindGroup:
		return "group_identified"
	}
	return "" // a tracked event with no name is unroutable
}

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
	set("group_id", trim(e.GroupID))
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

// faultOf builds the error body and computes its grouping fingerprint.
//
// GROUPING IS COMPUTED HERE, ISSUE LIFECYCLE IS NOT. `group` leads event.error's ORDER
// BY after org, so a row cannot be written without it — it is a pure function of the
// failure's shape, which makes it enrichment and puts it on the ingest path. What a
// downstream consumer owns is the ISSUE: status, assignee, first_seen, count, keyed
// (org, group). That is the one non-telemetry concept in this plane and it stays
// relational; nothing about it belongs in a columnar fact.
func faultOf(e CaptureEvent) *fault {
	f := &fault{
		site:        trim(e.Site),
		level:       strings.ToLower(trim(e.Level)),
		release:     trim(e.Release),
		environment: trim(e.Environment),
		service:     trim(e.Service),
		trace:       trim(e.TraceID),
		span:        trim(e.SpanID),
	}
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
	if f.level == "" {
		f.level = "error"
	}
	f.group = fingerprint(f)
	return f
}

// fingerprint is the deterministic grouping key: the same failure shape always yields
// the same value, in any process, with no state. It prefers the FIRST-PARTY frame —
// two failures thrown from the same line of our code are one issue even when the
// message differs — and falls back to the message with its variable parts removed so
// "user 41 not found" and "user 907 not found" do not become two issues.
func fingerprint(f *fault) string {
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

// recordOf builds the log body.
//
// `resource` stays 0. It is a fingerprint into a resource dimension table, and this
// plane has no such table — writing a hash that nothing can resolve would be a number
// that looks like a join key and is not one. It becomes real when a resource plane
// exists, and nothing else has to change here.
func recordOf(e CaptureEvent) *record {
	r := &record{service: trim(e.Service), trace: trim(e.TraceID), span: trim(e.SpanID)}
	if e.Log != nil {
		r.severity = strings.ToLower(trim(e.Log.Severity))
		r.number = e.Log.Number
		r.body = scrubText(e.Log.Body)
	}
	return r
}

// spanOf builds the span body. A span with no trace id is still stored: event.span is
// ordered (org, trace_id, time, id), so it lands in the empty-trace bucket rather than
// being refused — an orphan span is a real observation and dropping it would hide it.
func spanOf(e CaptureEvent) *span {
	s := &span{service: trim(e.Service), trace: trim(e.TraceID), id: trim(e.SpanID)}
	if e.Span != nil {
		s.parent = trim(e.Span.Parent)
		s.duration = e.Span.Duration
		s.status = strings.ToLower(trim(e.Span.Status))
		if s.id == "" {
			s.id = trim(e.Span.ID)
		}
		if s.trace == "" {
			s.trace = trim(e.Span.Trace)
		}
	}
	return s
}

// sampleOf builds the metric body.
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

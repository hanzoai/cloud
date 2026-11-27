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

// forward.go is the CONSUMER fan-out client of the canonical event plane. The ONE
// write core (ingestEvents) commits a batch as FACTS — the publish that the sink
// lands in event.fact under its own signal; nothing here writes storage.
// This file used to sit beside a second storage write (the wide hanzo.events INSERT)
// and hand the plane its only copy of each batch; that double-write is gone — the
// fact publish IS the commit — and what remains here are the SUBSCRIBER hand-offs an
// accepted batch still owes:
//
//   - the ENVELOPE onto the plane (PublishEvents, bus.go) — the webhook-delivery
//     contract (apps/webhooks): orgs subscribe to event.<folded name> subjects and
//     receive the EventEnvelope verbatim. It is a second VOCABULARY on the one
//     stream (EventSignalKey, bus.go), not a second write to any table — the
//     warehouse drain acks it as errNotAFact and lands nothing for it.
//   - a COPY-taking downstream SINK — the destinations subsystem — which translates
//     and forwards each event to the org's connected ad/analytics platforms (GA4, Meta
//     CAPI, …).
//   - the SPAN sinks: the span-signal slice, on identical terms. apps/o11y installs one
//     (spansink.go) that lands each LLM-shaped span on event.span, so a /v1/event span
//     surfaces in the LLM views (GET /v1/o11y/llm/observations, /llm/traces, and the
//     eval board) beside the gen_ai spans the ai emit path sends over the ZAP wire.
//
// The client is:
//
//   - ONE-WAY. analytics never imports its consumers; a sink (destinations, o11y)
//     calls AddSink / AddSpanSink from its own Mount. No sinks means no
//     sink fan-out, so this file changes nothing about ingest when they are absent.
//   - RAW, WHERE THE CONSUMER FORWARDS. The destination sink receives the event BEFORE
//     the warehouse privacy scrub, because a server-side Conversions-API forwarder must
//     hash the match keys (email/phone/click ids) the warehouse deliberately drops. The
//     org connected the destination and owns that consent; the destination adapters
//     SHA-256 every PII field before it leaves the process.
//   - SCRUBBED, WHERE THE CONSUMER STORES. The span slice carries the same scrubMap copy
//     the write core stores in the fact's attributes, because its consumer writes a ROW:
//     scrubText's whole contract is that a token in a property is redacted before
//     storage, and a projection that stored more than the plane stores would be a second
//     copy of the batch under a weaker rule.
//   - FAIL-SOFT. Each sink runs detached (a panic-guarded goroutine) so a slow or
//     broken consumer can never block, fail, or crash an ingest.
//   - ADDITIVE. A projection failure is invisible to the ingest — the fact publish
//     already committed and the honest receipt already returned.

package event

import (
	"cmp"
	"strings"
	"time"
)

// SinkEvent is one accepted event handed to the downstream fan-out. It carries the
// resolved canonical name plus the commerce + identity fields a conversion needs;
// Properties is the RAW (pre-scrub) property bag the translator lifts match keys and
// custom data from. The tenant is the org argument to the sink, never a field here.
type SinkEvent struct {
	MessageID string
	Name      string
	// Source is the wire the event arrived on (event | posthog | plane), stamped
	// by the write core and never read from the body. plane means a peer app
	// stated it over the internal socket, which no browser can reach — so it is
	// the one fact a consumer can trust about who said a thing happened.
	Source string
	// Product is the emitting surface: the project a key named, else what the
	// caller wrote (site, commerce, chat, …).
	Product string
	// Vouched is true when the org's own secret key admitted the event, the one
	// credential no browser holds. Anything else is its sender's word.
	Vouched     bool
	DistinctID  string
	AnonymousID string
	Time        time.Time
	URL         string
	Path        string
	Referrer    string
	Revenue     float64
	Currency    string
	ProductID   string
	Quantity    uint32
	Properties  map[string]any
	// IP and UserAgent are the visitor's own, stamped from the ingest request.
	IP        string
	UserAgent string
}

// sinks are the downstream fan-out consumers, registered by subsystem Mounts
// (destinations). Mounts run before request traffic, so no lock is needed on this
// package-global. A BUS consumer is never registered here — it subscribes to the
// stream instead, which is what keeps this list to integrations that genuinely need
// the raw, pre-scrub batch in-process.
var sinks []func(org string, evs []SinkEvent)

// AddSink registers a downstream fan-out consumer and returns its remover.
// Every registered sink receives every accepted batch, each on its own
// detached, panic-guarded dispatch — one client, N consumers, none of which can
// block or fail ingest or each other.
func AddSink(fn func(org string, evs []SinkEvent)) (remove func()) {
	i := len(sinks)
	sinks = append(sinks, fn)
	return func() { sinks[i] = nil }
}

// SpanEvent is one accepted span handed to the span fan-out. It is a carrier of exactly
// the fields a span projection needs, so analytics stays orthogonal to its consumers —
// apps/o11y decides on its side which of these spans are LLM calls and what a row of
// event.span looks like. Properties is where a span states its OTel semantic attributes
// (gen_ai.*), carried as the SCRUBBED copy the fact row stores (see the header); the
// tenant is the org argument to the sink, never a field here.
type SpanEvent struct {
	MessageID   string // client idempotency id / minted; the fallback row identity
	Time        time.Time
	Name        string // the span's name, resolved by the plane's own rule
	Kind        string // client|server|producer|consumer|internal
	Status      string // how the span ended: ok|error|unset, lowercased
	Duration    uint64 // elapsed nanoseconds
	TraceID     string // the trace this span belongs to
	SpanID      string // this span's own id
	Parent      string // the enclosing span's id, empty for a root span
	Service     string // emitting service, when the wire named one
	Product     string // emitting surface: console|chat|app|site|admin
	Site        string // deployed property the span came from
	Release     string // build the span was recorded in
	Environment string // deployment the span was recorded in
	DistinctID  string // the reporting visitor (user id — never PII)
	SessionID   string
	Properties  map[string]any // RAW; where gen_ai.* semantic attributes travel
}

// spanSinks are the span fan-out consumers, on the same terms as sinks:
// registered at Mount, package-global, each dispatch detached and panic-guarded.
var spanSinks []func(org string, spans []SpanEvent)

// AddSpanSink registers a span fan-out consumer and returns its remover.
func AddSpanSink(fn func(org string, spans []SpanEvent)) (remove func()) {
	i := len(spanSinks)
	spanSinks = append(spanSinks, fn)
	return func() { spanSinks[i] = nil }
}

// fanOut hands the accepted batch to the plane envelope and to every installed sink,
// each detached and fail-soft. org is the SERVER-resolved tenant (already an owned
// copy from principal.Org). One call site — the ingestEvents tail.
func fanOut(org, source string, evs []CaptureEvent) {
	if len(evs) == 0 {
		return
	}
	fanOutEvents(org, source, evs)
	fanOutSpans(org, evs)
}

// fanOutEvents builds SinkEvents from the RAW events (skipping unroutable ones,
// mirroring the write core's drop rule), publishes the envelope onto the plane, and
// dispatches each installed sink on a panic-guarded goroutine so ingest is never
// blocked or failed by a consumer.
func fanOutEvents(org, source string, evs []CaptureEvent) {
	live := make([]func(string, []SinkEvent), 0, len(sinks))
	for _, fn := range sinks {
		if fn != nil {
			live = append(live, fn)
		}
	}
	now := time.Now()
	out := make([]SinkEvent, 0, len(evs))
	for _, e := range evs {
		name := resolveEventName(e)
		if name == "" {
			continue // unroutable — the write core dropped it too
		}
		out = append(out, SinkEvent{
			MessageID:   cmp.Or(trim(e.MessageID), randID()),
			Name:        name,
			Source:      source,
			Product:     trim(e.Product),
			Vouched:     e.vouched,
			DistinctID:  trim(e.DistinctID),
			AnonymousID: trim(e.AnonymousID),
			Time:        clampTS(e.Timestamp, now),
			URL:         trim(e.URL),
			Path:        trim(e.Path),
			Referrer:    trim(e.Referrer),
			Revenue:     e.Revenue,
			Currency:    trim(e.Currency),
			ProductID:   trim(e.ProductID),
			Quantity:    e.Quantity,
			Properties:  e.Properties,
			IP:          e.ip,
			UserAgent:   propStr(e.Properties, "user_agent"),
		})
	}
	if len(out) == 0 {
		return
	}
	// Onto the plane FIRST, and unconditionally: this is the platform's own fan-out,
	// not an integration an org opted into. Detached on the same terms as a sink, so a
	// slow bus costs a goroutine and never an ingest.
	go func() {
		defer func() { _ = recover() }()
		PublishEvents(org, out)
	}()
	// Our own people, test accounts and programs stay in our stream and never reach a
	// sink an integration forwards to an ad platform.
	sunk := out[:0:0]
	for _, e := range out {
		if !testTraffic(e.Properties) && !operated(e.Properties) {
			sunk = append(sunk, e)
		}
	}
	if len(sunk) == 0 {
		return
	}
	for _, fn := range live {
		go func() {
			defer func() { _ = recover() }()
			fn(org, sunk)
		}()
	}
}

// fanOutSpans filters the batch to the events the plane routes as the span signal —
// routeOf is the ONE routing rule, so a projection can
// never carry a fact the plane filed as something else — builds SpanEvents, and
// dispatches each installed span sink on a panic-guarded goroutine. Non-span events
// (the overwhelming majority) are skipped, so a normal batch never touches this path.
func fanOutSpans(org string, evs []CaptureEvent) {
	live := make([]func(string, []SpanEvent), 0, len(spanSinks))
	for _, fn := range spanSinks {
		if fn != nil {
			live = append(live, fn)
		}
	}
	if len(live) == 0 {
		return
	}
	now := time.Now()
	out := make([]SpanEvent, 0)
	for _, e := range evs {
		r := routeOf(e)
		if r.signal != signalSpan {
			continue
		}
		b := spanBodyOf(e)
		// The SCRUB the write core applies before it stores a fact's attributes, applied
		// once here: the projection reads its property fallbacks out of the very map it
		// hands the consumer, so what a row carries and what a fallback saw are the same
		// values.
		props := scrubMap(e.Properties)
		// The plane's OWN precedence, restated (applySpan, fact.go): the route's default
		// kind stands unless the envelope names one, and the span BODY refines both —
		// a client emitting a span knows its role precisely.
		kind := cmp.Or(trim(e.Kind), r.kind)
		if k := trim(b.Kind); k != "" {
			kind = k
		}
		out = append(out, SpanEvent{
			MessageID: cmp.Or(trim(e.MessageID), randID()),
			Time:      clampTS(e.Timestamp, now),
			Name:      resolveName(r, e),
			Kind:      strings.ToLower(kind),
			Status:    strings.ToLower(trim(b.Status)),
			Duration:  b.Duration,
			// The first-class envelope ids win over the body's — the SAME order the fact
			// row resolves them in (the envelope fills trace/span at build, applySpan
			// fills only what is still empty), so the projected span and the fact carry
			// one identity rather than two. The legacy $ spellings are the last resort
			// for a client that has not moved to the first-class field, exactly as the
			// error slice above reads them.
			TraceID:     cmp.Or(trim(e.TraceID), trim(b.Trace), propStr(props, "$trace_id")),
			SpanID:      cmp.Or(trim(e.SpanID), trim(b.ID), propStr(props, "$span_id")),
			Parent:      trim(b.Parent),
			Service:     trim(e.Service),
			Product:     trim(e.Product),
			Site:        trim(e.Site),
			Release:     cmp.Or(trim(e.Release), propStr(props, "$release")),
			Environment: cmp.Or(trim(e.Environment), propStr(props, "$environment")),
			DistinctID:  trim(e.DistinctID),
			SessionID:   trim(e.SessionID),
			Properties:  props,
		})
	}
	if len(out) == 0 {
		return
	}
	for _, fn := range live {
		go func() {
			defer func() { _ = recover() }()
			fn(org, out)
		}()
	}
}

// spanBodyOf returns the event's span body, or the ZERO body when the wire carried
// none. A span-typed event without a body is still a span — applySpan stores it and the
// fact lands — and the zero value states exactly the "nothing further known" that row
// records, so the projection reads one shape instead of branching on a pointer.
func spanBodyOf(e CaptureEvent) SpanBody {
	if e.Span == nil {
		return SpanBody{}
	}
	return *e.Span
}

// propStr reads a string-valued property, "" when absent or non-string. The ingest
// never trusts these for tenancy — they are descriptive only.
func propStr(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if v, ok := p[key].(string); ok {
		return trim(v)
	}
	return ""
}

// testTraffic reports an event that is no person's visit (a bot's, or the site
// team's: FromPerson) or a test user's.
func testTraffic(props map[string]any) bool {
	return !FromPerson(props) || flagged(props, "test")
}

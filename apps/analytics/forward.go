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

// forward.go is the CONSUMER fan-out seam of the canonical event plane. The ONE
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
//   - the ERROR sinks: the error-signal slice of the batch, handed to consumers that
//     project a failure onto another surface. apps/o11y installs one (errorsink.go)
//     that lands each error on the embedded Sentry plane, so a /v1/event error
//     surfaces on sentry.hanzo.ai beside the errors a Sentry SDK posts directly.
//   - the SPAN sinks: the span-signal slice, on identical terms. apps/o11y installs one
//     (spansink.go) that lands each LLM-shaped span on event.span, so a /v1/event span
//     surfaces in the LLM views (GET /v1/o11y/llm/observations, /llm/traces, and the
//     eval board) beside the gen_ai spans the ai emit path sends over the ZAP wire.
//
// The seam is:
//
//   - ONE-WAY. analytics never imports its consumers; a sink (destinations, o11y)
//     calls AddSink / AddErrorSink / AddSpanSink from its own Mount. No sinks means no
//     sink fan-out, so this file changes nothing about ingest when they are absent.
//   - RAW, WHERE THE CONSUMER FORWARDS. The destination sink receives the event BEFORE
//     the warehouse privacy scrub, because a server-side Conversions-API forwarder must
//     hash the match keys (email/phone/click ids) the warehouse deliberately drops. The
//     org connected the destination and owns that consent; the destination adapters
//     SHA-256 every PII field before it leaves the process. The exception text an error
//     sink reads is already the folded copy foldException scrubbed, and the Sentry
//     normalizer scrubs again on its side.
//   - SCRUBBED, WHERE THE CONSUMER STORES. The span slice carries the same scrubMap copy
//     the write core stores in the fact's attributes, because its consumer writes a ROW:
//     scrubText's whole contract is that a token in a property is redacted before
//     storage, and a projection that stored more than the plane stores would be a second
//     copy of the batch under a weaker rule.
//   - FAIL-SOFT. Each sink runs detached (a panic-guarded goroutine) so a slow or
//     broken consumer can never block, fail, or crash an ingest.
//   - ADDITIVE. A projection failure is invisible to the ingest — the fact publish
//     already committed and the honest receipt already returned.

package analytics

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
	MessageID   string
	Name        string
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
}

// sinks are the downstream fan-out consumers, registered by subsystem Mounts
// (destinations). Mounts run before request traffic, so no lock is needed on this
// package-global. A BUS consumer is never registered here — it subscribes to the
// stream instead, which is what keeps this list to integrations that genuinely need
// the raw, pre-scrub batch in-process.
var sinks []func(org string, evs []SinkEvent)

// AddSink registers a downstream fan-out consumer and returns its remover.
// Every registered sink receives every accepted batch, each on its own
// detached, panic-guarded dispatch — one seam, N consumers, none of which can
// block or fail ingest or each other.
func AddSink(fn func(org string, evs []SinkEvent)) (remove func()) {
	i := len(sinks)
	sinks = append(sinks, fn)
	return func() { sinks[i] = nil }
}

// ErrorEvent is one accepted error occurrence handed to the error fan-out. It is a
// carrier of exactly the fields an error projection needs, so analytics stays
// orthogonal to its consumers — apps/o11y builds the Sentry wire event on its side.
// The exception text is the folded, scrubbed copy (foldException); the tenant is the
// org argument to the sink, never a field here.
type ErrorEvent struct {
	MessageID     string // client idempotency id / minted; becomes the projection's event id
	Time          time.Time
	ExceptionType string // e.g. "TypeError"; "" ⇒ the consumer groups on the message
	Message       string // the exception message (the grouping value)
	Stack         string // raw client stack string, when the wire carried one
	Handled       *bool  // whether the app caught it (nil ⇒ unknown)
	Level         string // "error" for these events
	Platform      string // e.g. "javascript" (properties.$platform; descriptive)
	Release       string // build the error fired in
	Environment   string // deployment the error fired in
	Transaction   string // the route the error fired on (path, else url)
	URL           string
	Path          string
	DistinctID    string // the reporting visitor (user id — never PII)
	SessionID     string
	Product       string // emitting surface: console|chat|app|site|admin
	Site          string // deployed property the error came from
	Service       string // emitting service, when the wire named one
	Library       string
	TraceID       string // trace linkage
	SpanID        string
}

// errorSinks are the error fan-out consumers, on the same terms as sinks: registered
// at Mount, package-global, each dispatch detached and panic-guarded.
var errorSinks []func(org string, errs []ErrorEvent)

// AddErrorSink registers an error fan-out consumer and returns its remover.
func AddErrorSink(fn func(org string, errs []ErrorEvent)) (remove func()) {
	i := len(errorSinks)
	errorSinks = append(errorSinks, fn)
	return func() { errorSinks[i] = nil }
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

// spanSinks are the span fan-out consumers, on the same terms as sinks and errorSinks:
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
func fanOut(org string, evs []CaptureEvent) {
	if len(evs) == 0 {
		return
	}
	fanOutEvents(org, evs)
	fanOutErrors(org, evs)
	fanOutSpans(org, evs)
}

// fanOutEvents builds SinkEvents from the RAW events (skipping unroutable ones,
// mirroring the write core's drop rule), publishes the envelope onto the plane, and
// dispatches each installed sink on a panic-guarded goroutine so ingest is never
// blocked or failed by a consumer.
func fanOutEvents(org string, evs []CaptureEvent) {
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
	for _, fn := range live {
		go func() {
			defer func() { _ = recover() }()
			fn(org, out)
		}()
	}
}

// fanOutErrors filters the batch to the events the plane routes as the error signal —
// routeOf is the ONE routing rule, so the projection can never carry a fact the plane
// filed as something else — builds ErrorEvents, and dispatches each installed error
// sink on a panic-guarded goroutine. Non-error events (the overwhelming majority) are
// skipped, so a normal batch never touches this path.
func fanOutErrors(org string, evs []CaptureEvent) {
	live := make([]func(string, []ErrorEvent), 0, len(errorSinks))
	for _, fn := range errorSinks {
		if fn != nil {
			live = append(live, fn)
		}
	}
	if len(live) == 0 {
		return
	}
	now := time.Now()
	out := make([]ErrorEvent, 0)
	for _, e := range evs {
		if routeOf(e).signal != signalError {
			continue
		}
		typ, msg, stack, handled := exceptionOf(e)
		out = append(out, ErrorEvent{
			MessageID:     cmp.Or(trim(e.MessageID), randID()),
			Time:          clampTS(e.Timestamp, now),
			ExceptionType: trim(typ),
			Message:       trim(msg),
			Stack:         stack,
			Handled:       handled,
			Level:         "error",
			Platform:      propStr(e.Properties, "$platform"),
			Release:       cmp.Or(trim(e.Release), propStr(e.Properties, "$release")),
			Environment:   cmp.Or(trim(e.Environment), propStr(e.Properties, "$environment")),
			Transaction:   cmp.Or(trim(e.Path), trim(e.URL)),
			URL:           trim(e.URL),
			Path:          trim(e.Path),
			DistinctID:    trim(e.DistinctID),
			SessionID:     trim(e.SessionID),
			Product:       trim(e.Product),
			Site:          trim(e.Site),
			Service:       trim(e.Service),
			Library:       trim(e.Library),
			TraceID:       cmp.Or(trim(e.TraceID), propStr(e.Properties, "$trace_id")),
			SpanID:        cmp.Or(trim(e.SpanID), propStr(e.Properties, "$span_id")),
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

// fanOutSpans filters the batch to the events the plane routes as the span signal —
// routeOf is the ONE routing rule, the same pin fanOutErrors holds, so a projection can
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

// exceptionOf extracts the exception's (type, message, stack, handled) from whichever
// carrier holds it: the typed Error (folded events carry it), or properties.$exception
// as either the typed *Exception (post-fold, same process) or a decoded map (from the
// JSON wire). Returns zero values for an error event that carries no exception — a
// bare error-typed event, which the consumer then groups on its message/transaction.
func exceptionOf(e CaptureEvent) (typ, message, stack string, handled *bool) {
	if e.Error != nil {
		return e.Error.Type, e.Error.Message, e.Error.Stack, e.Error.Handled
	}
	raw, ok := e.Properties["$exception"]
	if !ok {
		return "", "", "", nil
	}
	switch x := raw.(type) {
	case *Exception:
		if x == nil {
			return "", "", "", nil
		}
		return x.Type, x.Message, x.Stack, x.Handled
	case Exception:
		return x.Type, x.Message, x.Stack, x.Handled
	case map[string]any:
		return mapStr(x, "type"), mapStr(x, "message"), mapStr(x, "stack"), mapBool(x, "handled")
	default:
		return "", "", "", nil
	}
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

// mapStr reads a string value from a decoded exception map.
func mapStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// mapBool reads a *bool from a decoded exception map (JSON bools decode to bool).
func mapBool(m map[string]any, key string) *bool {
	if v, ok := m[key].(bool); ok {
		return &v
	}
	return nil
}

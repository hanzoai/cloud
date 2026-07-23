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

// forward.go is the fan-out seam of the canonical event plane. After the ONE write
// core (ingestEvents) commits a batch to hanzo.events, it hands a COPY of that batch
// to the optional downstream sinks — additive PROJECTIONS onto the OTHER stores in
// the ONE datastore. hanzo.events stays the unified spine; a projection never
// re-routes, it fans a copy out. Two orthogonal sinks live behind this ONE seam:
//
//   - the DESTINATIONS sink (SetSink): every accepted event → the org's connected
//     ad/analytics platforms (GA4, Meta CAPI, …), owned by clients/destinations.
//   - the ERROR/SENTRY sink (SetErrorSink): every type:'error' event → the o11y
//     Sentry plane (o11y_sentry_events + the o11y_issues lifecycle), so /v1/event
//     errors surface on sentry.hanzo.ai, owned by clients/o11y.
//
// Every sink shares the same seam discipline:
//
//   - ONE-WAY. analytics imports NEITHER subsystem; each subsystem calls its own
//     Set*Sink from its Mount. A nil sink means no fan-out (the default when the
//     subsystem is off), so this file changes nothing about ingest when it is absent.
//   - RAW. The sink receives the event BEFORE the warehouse privacy scrub. A
//     Conversions-API forwarder must hash the match keys the warehouse drops; the
//     Sentry normalizer runs its OWN fail-secure scrub (secrets always, PII unless
//     configured) before the value is stored or hashed. Each sink owns its scrub.
//   - FAIL-SOFT. Each sink runs detached (a panic-guarded goroutine) so a slow or
//     broken projection can never block, fail, or crash an ingest.
//   - ADDITIVE. A projection failure is invisible to the ingest — the ONE
//     hanzo.events write already committed and the honest receipt already returned.
package analytics

import (
	"time"
)

// ── destinations sink (all events → CDP) ─────────────────────────────────────

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

// sink is the downstream fan-out hook, installed once by the destinations subsystem
// at Mount (nil ⇒ no fan-out). A subsystem Mount runs before request traffic, so no
// lock is needed on this package-global.
var sink func(org string, evs []SinkEvent)

// SetSink installs (nil clears) the downstream fan-out hook.
func SetSink(fn func(org string, evs []SinkEvent)) { sink = fn }

// ── error/Sentry sink (type:'error' events → o11y Sentry plane) ──────────────

// ErrorEvent is one accepted error occurrence handed to the Sentry fan-out. It is a
// cloud-native, o11y-FREE carrier of exactly the fields the Sentry normalizer needs —
// so clients/analytics stays orthogonal to clients/o11y (the consumer builds the o11y
// wire event on its side). Fields are RAW; the Sentry normalizer scrubs secrets/PII.
// The tenant is the org argument to the sink, never a field here.
type ErrorEvent struct {
	MessageID     string // client idempotency id / minted; becomes the Sentry event id
	Time          time.Time
	ExceptionType string // e.g. "TypeError"; "" ⇒ normalizer groups on the message
	Message       string // the exception message (the grouping value)
	Stack         string // raw client stack string (folded wire carries no structured frames)
	Handled       *bool  // whether the app caught it (nil ⇒ unknown)
	Level         string // "error" for these events
	Platform      string // e.g. "javascript" (from properties.$platform; best-effort)
	Release       string // properties.$release (best-effort)
	Environment   string // properties.$environment (best-effort)
	Transaction   string // the route the error fired on (path, else url)
	URL           string
	Path          string
	DistinctID    string // the reporting visitor (user.id — never PII)
	SessionID     string
	Product       string // emitting surface: console|chat|app|site|admin
	Library       string
	TraceID       string // properties.$trace_id (best-effort trace linkage)
	SpanID        string // properties.$span_id
}

// errorSink is the Sentry fan-out hook, installed once by the o11y subsystem at Mount
// (nil ⇒ no error projection). Same lock-free package-global discipline as sink.
var errorSink func(org string, errs []ErrorEvent)

// SetErrorSink installs (nil clears) the error/Sentry fan-out hook.
func SetErrorSink(fn func(org string, errs []ErrorEvent)) { errorSink = fn }

// ── fan-out ──────────────────────────────────────────────────────────────────

// fanOut hands the accepted batch to EVERY installed sink, each detached and
// fail-soft. org is the SERVER-resolved tenant (already an owned copy from
// principal.Org). One call site (the ingestEvents tail), two orthogonal projections.
func fanOut(org string, evs []CaptureEvent) {
	fanOutDestinations(org, evs)
	fanOutErrors(org, evs)
}

// fanOutDestinations builds SinkEvents from the RAW events (skipping unroutable ones,
// mirroring the write core's drop rule) and, if any remain and a sink is installed,
// dispatches them on a panic-guarded goroutine so ingest is never blocked or failed.
func fanOutDestinations(org string, evs []CaptureEvent) {
	fn := sink
	if fn == nil || len(evs) == 0 {
		return
	}
	now := time.Now()
	out := make([]SinkEvent, 0, len(evs))
	for _, e := range evs {
		name := resolveEventName(e)
		if name == "" {
			continue // unroutable — the write core dropped it too
		}
		out = append(out, SinkEvent{
			MessageID:   firstNonEmptyStr(trim(e.MessageID), randID()),
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
	go func() {
		defer func() { _ = recover() }()
		fn(org, out)
	}()
}

// fanOutErrors filters the batch to type:'error' events, builds o11y-free ErrorEvents,
// and dispatches them to the error sink on a panic-guarded goroutine. Non-error events
// (the overwhelming majority) are skipped, so a normal batch never touches this path.
func fanOutErrors(org string, evs []CaptureEvent) {
	fn := errorSink
	if fn == nil || len(evs) == 0 {
		return
	}
	now := time.Now()
	out := make([]ErrorEvent, 0)
	for _, e := range evs {
		if !isErrorEvent(e) {
			continue
		}
		typ, msg, stack, handled := exceptionOf(e)
		out = append(out, ErrorEvent{
			MessageID:     firstNonEmptyStr(trim(e.MessageID), randID()),
			Time:          clampTS(e.Timestamp, now),
			ExceptionType: trim(typ),
			Message:       trim(msg),
			Stack:         stack,
			Handled:       handled,
			Level:         "error",
			Platform:      propStr(e.Properties, "$platform"),
			Release:       propStr(e.Properties, "$release"),
			Environment:   propStr(e.Properties, "$environment"),
			Transaction:   firstNonEmptyStr(trim(e.Path), trim(e.URL)),
			URL:           trim(e.URL),
			Path:          trim(e.Path),
			DistinctID:    trim(e.DistinctID),
			SessionID:     trim(e.SessionID),
			Product:       trim(e.Product),
			Library:       trim(e.Library),
			TraceID:       propStr(e.Properties, "$trace_id"),
			SpanID:        propStr(e.Properties, "$span_id"),
		})
	}
	if len(out) == 0 {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		fn(org, out)
	}()
}

// isErrorEvent reports whether e is an error the Sentry projection should carry. It is
// robust across the wire shapes: the canonical type:'error' (the primary signal), a
// pre-fold top-level error object, and a native properties.$exception (a client that
// set the exception directly). ONE detector for every door.
func isErrorEvent(e CaptureEvent) bool {
	if canonicalType(e.Type) == "error" || e.Error != nil {
		return true
	}
	_, ok := e.Properties["$exception"]
	return ok
}

// exceptionOf extracts the exception's (type, message, stack, handled) from whichever
// source carries it: the typed pre-fold Error, or properties.$exception as either the
// typed *Exception (post-fold, same process) or a decoded map (from the JSON wire).
// Returns zero values for an error event that carries no exception (a bare error-typed
// event) — the normalizer then groups it on its message/transaction.
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

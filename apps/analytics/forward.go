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
// to an optional downstream sink — the destinations subsystem — which translates and
// forwards each event to the org's connected ad/analytics platforms (GA4, Meta CAPI,
// …). The seam is:
//
//   - ONE-WAY. analytics never imports its consumers; each (destinations, the
//     platform event-bus bridge) calls AddSink from its own Mount. No sinks means
//     no fan-out, so this file changes nothing about ingest when they are absent.
//   - RAW. The sink receives the event BEFORE the warehouse privacy scrub, because a
//     server-side Conversions-API forwarder must hash the match keys (email/phone/
//     click ids) the warehouse deliberately drops. The org connected the destination
//     and owns that consent; the destination adapters SHA-256 every PII field before
//     it leaves the process.
//   - FAIL-SOFT. The sink runs detached (a panic-guarded goroutine) so a slow or
//     broken destination can never block, fail, or crash an ingest.
package analytics

import "time"

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
// (destinations; the platform event-bus bridge in apps/webhooks). Mounts run
// before request traffic, so no lock is needed on this package-global.
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

// fanOut hands the accepted batch to the sink, detached and fail-soft. org is the
// SERVER-resolved tenant (already an owned copy from principal.Org). It builds
// SinkEvents from the RAW events (skipping unroutable ones, mirroring the write
// core's drop rule) and, if any remain and a sink is installed, dispatches them on a
// panic-guarded goroutine so ingest is never blocked or failed by a destination.
func fanOut(org string, evs []CaptureEvent) {
	if len(evs) == 0 {
		return
	}
	live := make([]func(string, []SinkEvent), 0, len(sinks))
	for _, fn := range sinks {
		if fn != nil {
			live = append(live, fn)
		}
	}
	if len(live) == 0 {
		return
	}
	// The public tenant never fans out. A destination is a connection an ORG made, and
	// this sink is handed the RAW pre-scrub event so a Conversions API can hash match
	// keys — so forwarding an unattested event would push it into an external platform
	// on an org's behalf. publicTenant holds no connection, so the lookup is already
	// empty; stating it here makes that a property of the SEAM rather than a property of
	// the destination table.
	if org == publicTenant {
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
	for _, fn := range live {
		fn := fn
		go func() {
			defer func() { _ = recover() }()
			fn(org, out)
		}()
	}
}

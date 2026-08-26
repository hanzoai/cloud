package o11y

// signals.go — GET /v1/o11y/signals, the answer to "where does each signal live".
//
// github.com/hanzoai/metrics mounted eleven routes: a probe, a write door and a
// read door for each of metrics, logs and traces, over a per-org in-process map.
// Eight of them were a SECOND way to write and read signals this plane already
// stores durably, and the second way was the worse one — bounded, per-process,
// lost on restart, and on api.hanzo.ai holding nothing at all (records=0,
// spans=0, every query door answering count=0). They are retired, not ported.
//
// The three PROBES are what is left, and this is where they land. They named
// their signal by prefix and nothing else — /v1/metrics/health,
// /v1/logs/health, /v1/traces/health — and those addresses cannot come back:
// every route a capability serves is under the capability's own name
// (HIP-0139 §3), the fleet has driven that to zero misfiled pairs, and
// openapi/misfiled.txt only shrinks. An address answered by o11y that does not
// say o11y is the exact defect that ratchet exists to refuse. /v1/o11y/summary
// moved off a top-level /v1/summary for this reason and is the precedent.
//
// ONE ADDRESS FOR THE THREE, because it is one question. Three probes were three
// spellings of "which store holds this", and a caller asking about logs is
// asking about the plane. Whether this process is up is /v1/o11y/livez's answer
// and it has exactly one; whether the plane can be READ is /v1/o11y/summary's. A
// per-signal probe re-deriving either would be a third answer to a question that
// already has one, and three answers is how a green board outlives the thing it
// was watching.
//
// Anonymous by construction, the same way /v1/o11y/summary is: the identity
// middleware strips and re-mints rather than rejecting, so a handler that never
// asks who is calling is public, and a GET is never billable. There is nothing
// to scope — the shape of the plane is identical for every caller and carries no
// tenant's data.

import (
	"context"

	"github.com/zap-proto/zip"
)

// Signal is one telemetry signal: what it is called, the event-plane stream that
// holds it, and the address it is read at.
type Signal struct {
	// Name is the signal, as the plane names it.
	Name string `json:"signal"`
	// Store is the event-plane stream its rows land in.
	Store string `json:"store"`
	// Read is the one address it is read at.
	Read string `json:"read"`
}

// Telemetry is every signal this deployment carries.
type Telemetry struct {
	// Signals is one entry per signal, in the order they were built.
	Signals []Signal `json:"signals"`
}

// The three, written out rather than derived: pairing a signal to its store and
// its read address is the whole content of the answer.
var telemetry = Telemetry{Signals: []Signal{
	{Name: "metric", Store: "event.metric", Read: "/v1/o11y/metrics"},
	{Name: "log", Store: "event.log", Read: "/v1/o11y/logs"},
	{Name: "span", Store: "event.span", Read: "/v1/o11y/traces/{traceId}"},
}}

// mountSignals registers the address. Typed, like every other published route
// here, so the registry the OpenAPI document, the MCP surface and the CLI are
// projected from carries it — a raw route would be invisible to all three, and
// this exists for callers that are not in this repository.
func mountSignals(a *zip.App) { zip.Get(a, "/v1/o11y/signals", handleSignals) }

// GetSignals lists the telemetry signals this deployment carries: for each one,
// the durable store its rows land in and the single address it is read at.
//
// It answers 200 to any caller and is the same for all of them — the shape of
// the plane, not anyone's telemetry. It replaces the three probes the retired
// metrics module served, which reported a per-process in-memory map that held
// nothing; everything they described is on the event plane now.
//
// Example: {}
func handleSignals(context.Context, *noArgs) (*Telemetry, error) { return &telemetry, nil }

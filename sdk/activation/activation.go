// Package activation times how long an agent session took to be ready to work —
// its compute found alive, started, or moved — APART FROM how long its model took
// to answer.
//
// Readiness and first-token latency are two facts that never share a row: a total
// that sums them reads as "the model is slow" when the time went to making compute
// ready. So they are recorded separately:
//
//   - activation: one span per session made ready, named Span, its duration the
//     latency, labelled with HOW it was made ready (Kind). It carries no gen_ai
//     attribute, so no reader of model calls ever counts it.
//   - time to first token: an attribute of the model call's own gen_ai span
//     (sdk/infer AttrTTFT), measured from the moment that call is made, so no
//     activation can ever be inside it.
//
// Spans and not a histogram, because a span is the one signal that leaves every
// process: each plugin sends its spans to the plane over ZAP, while a metric
// stays in its own process's registry unless that process is the one pushing it
// (apps/o11y metricspush.go). p50 and p99 are read off the span durations by
// kind (apps/admin o11y.go), the way the fleet's request latency already is.
package activation

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Kind is how a session was made ready. The set is closed: a reader groups on it,
// and a value outside it would be a series nobody asked for.
type Kind string

const (
	// Cold is a session given NEW compute: a session started for the first time,
	// or one resumed after it was parked for inactivity. Its latency is the pod's
	// scheduling, its image and its disk — the path a long-idle session pays.
	Cold Kind = "cold"
	// Relocated is a live session whose compute went away under it (evicted,
	// deleted, its node gone) and was replaced under the same id. The session
	// moved; its caller did not ask it to.
	Relocated Kind = "relocated"
	// Warm is a session whose compute was alive when it was asked for: nothing
	// was started, and its latency is the cost of finding that out.
	Warm Kind = "warm"
)

// Kinds is every Kind, in the order a board shows them.
func Kinds() []Kind { return []Kind{Cold, Relocated, Warm} }

// The span and its attributes, spelled once: the emitter writes them and the
// readers (apps/admin) select on them, so a rename is one edit both sides see.
const (
	// Span is the name of an activation's span on the plane (event.span.name).
	Span = "hanzo.session.activate"
	// AttrKind is the activation's Kind.
	AttrKind = "hanzo.session.activation"
	// AttrResumed says whether the caller named a session that already existed
	// ("true"), as opposed to asking for a new one ("false"). It splits a cold
	// start after inactivity from a session's first start, both of which are Cold.
	AttrResumed = "hanzo.session.resumed"
	// AttrSurface is which plane made the session ready ("sandbox").
	AttrSurface = "hanzo.session.surface"
	// AttrReactivationMs is how long the session took to be made ready, in
	// milliseconds — the reactivation latency, recorded as an attribute of the
	// activation's own span so it is readable as a number on the span plane
	// (a filter, a percentile) and not only as the span's duration. It is the
	// same interval the span spans (Start to End), surfaced as a value so a
	// reader does not have to derive it from timestamps. It is recorded apart
	// from gen_ai.latency.ttft_ms: readiness and first-token latency are two
	// facts that never share a row.
	AttrReactivationMs = "hanzo.session.reactivation_ms"
	// attrOrg is the tenant, under the key the plane reads a row's org from
	// (apps/o11y planeOrg), so an activation is scoped like every other span.
	attrOrg = "hanzo.org"
)

const tracerName = "github.com/hanzoai/cloud/sdk/activation"

// Clock is one activation being timed. It is started where the session is asked
// for, told what the activation turned out to be where that is decided (Is), and
// ended once (End). One that was never told a Kind records nothing: a request
// refused before any compute was looked at is not an activation, and counting it
// would pull every percentile toward the cost of a refusal.
type Clock struct {
	at      time.Time
	mu      sync.Mutex
	kind    Kind
	resumed bool
}

// Start starts timing an activation now.
func Start() *Clock { return &Clock{at: time.Now()} }

// Is records what this activation turned out to be. The last call wins, so a path
// that discovers it is somebody else's activation after all (a racing resume that
// lost) says Is("", false) and records nothing.
func (c *Clock) Is(k Kind, resumed bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.kind, c.resumed = k, resumed
	c.mu.Unlock()
}

// Kind is what this activation was found to be, "" while undecided.
func (c *Clock) Kind() Kind {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kind
}

// End records the activation as one span from Start to now, under ctx's trace, and
// reports whether it recorded one. err marks an activation that was attempted and
// failed (its pod did not start): it is recorded with an error status, so a reader
// can count failures and keep them out of the latency.
func (c *Clock) End(ctx context.Context, surface, org string, err error, attrs ...attribute.KeyValue) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	kind, resumed := c.kind, c.resumed
	c.mu.Unlock()
	if kind == "" {
		return false
	}
	// The tracer is asked for at each activation rather than held: the global
	// provider is installed by the composition root after this package is
	// loaded, and a tracer resolved before it would keep reporting to the one it
	// found first.
	_, span := otel.Tracer(tracerName).Start(ctx, Span,
		trace.WithTimestamp(c.at), trace.WithSpanKind(trace.SpanKindInternal))
	// The reactivation latency as a number on the span, apart from the span's own
	// duration: a reader filters and percentiles on the attribute without deriving
	// it from timestamps, and it never shares a row with gen_ai.latency.ttft_ms.
	reactivationMs := float64(time.Since(c.at)) / float64(time.Millisecond)
	span.SetAttributes(
		attribute.String(AttrKind, string(kind)),
		attribute.Bool(AttrResumed, resumed),
		attribute.String(AttrSurface, surface),
		attribute.Float64(AttrReactivationMs, reactivationMs),
	)
	if org != "" {
		span.SetAttributes(attribute.String(attrOrg, org))
	}
	span.SetAttributes(attrs...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "activation failed")
	}
	span.End(trace.WithTimestamp(time.Now()))
	return true
}

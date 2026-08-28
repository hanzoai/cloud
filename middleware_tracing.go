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

package cloud

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation scope for cloud's HTTP request spans. It is
// resolved off the GLOBAL tracer provider — the ZAP provider installed once by
// the composition root (cloud.Listen initTelemetry, in each plugin) — so a request span ships over
// the SAME ZAP wire to hanzoai/datastore as every log and GenAI span. One
// transport, one provider.
const TracerName = "hanzo-cloud"

// httpTracer is captured at package init, BEFORE the composition root installs
// the global provider. OTel's global delegation upgrades this handle to the real
// (ZAP) provider on the first otel.SetTracerProvider, so the package-level
// capture is safe and binds to ZAP — never to a later, competing provider.
var httpTracer = otel.Tracer(TracerName)

// propagator is how trace context crosses a process boundary: W3C trace context,
// the format the framework already stamps on every request and forwards on every
// call it makes.
//
// It is a value HERE rather than a read of otel's global, and that is the point.
// The global default is an EMPTY composite whose Extract silently returns the
// context unchanged, so a middleware that reached for it would join no trace at
// all in any process where installation had not run first — the exact silent
// no-op this file exists to remove. Owning the value makes extraction independent
// of initialization order; initTelemetry publishes this same value to the global
// so anything that does reach for it there agrees.
var propagator propagation.TextMapPropagator = propagation.TraceContext{}

// inbound adapts one request's headers to the propagator's carrier, so the
// server span can be extracted from whatever the caller sent.
type inbound struct{ c *zip.Ctx }

// Get reads one header. TraceContext asks for exactly two — traceparent and
// tracestate — and this is the whole of what extraction needs.
func (i inbound) Get(k string) string { return i.c.Header(k) }

// Set writes a header back onto the inbound request. Extraction never calls it;
// it is here because the carrier interface is shared with injection.
func (i inbound) Set(k, v string) { i.c.Fiber().Request().Header.Set(k, v) }

// Keys lists what the carrier holds, read off the request rather than declared,
// so it stays true if a propagator that enumerates is ever composed in.
func (i inbound) Keys() []string {
	var keys []string
	i.c.Fiber().Request().Header.VisitAll(func(k, _ []byte) { keys = append(keys, string(k)) })
	return keys
}

// hop is a request's place in a trace, as the framework settled it before any
// middleware ran: the trace it belongs to, and the span this hop IS.
//
// zip parses the caller's traceparent (or starts a trace when there is none),
// mints THIS hop's span id, rewrites the inbound header to name it, and prints
// the pair on every log line the request produces. So by the time this
// middleware runs, the hop already has an identity and every line of the request
// already carries it.
//
// What the header names after that rewrite is not the caller's span — it is
// ours. Read as a parent it made every span on the plane a child of a row
// nothing writes: measured on a live store, 10,307 spans, ZERO with parent=”,
// and not one log line joining a span on span_id while 3,169 of them joined one
// on `parent`. The read plane selects a trace's root with `parent = ”`, so the
// service list, the waterfall and the flamegraph answered empty over a complete
// span table.
//
// So the server span takes the hop instead of parenting itself to it: same
// trace, same span id, and no parent. One hop, one span, one id — the log line
// and the span row now name the same thing.
//
// The caller's own span id is the one fact still missing, and it is missing
// upstream: zip resolves it and then overwrites the header field that carried
// it, so no middleware can read it. Until the framework carries it past its own
// restamp, the first hop this plane records is the root of what it records.
type hop struct {
	trace trace.TraceID
	span  trace.SpanID
}

// hopKey is the private context key the settled identity travels under, from
// the middleware to the SDK's id source and no further.
type hopKey struct{}

// hopIDs is where a span's identity comes from in this process.
//
// The SDK asks NewIDs only for a span with no parent, which is exactly the
// server span below (it is started WithNewRoot). Every other span in the process
// has a parent, so it asks NewSpanID and gets a fresh random id — nested work
// keeps its real tree, and only the request boundary adopts an identity that was
// settled before it.
type hopIDs struct{}

var _ sdktrace.IDGenerator = hopIDs{}

func (hopIDs) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	if h, ok := ctx.Value(hopKey{}).(hop); ok && h.trace.IsValid() && h.span.IsValid() {
		return h.trace, h.span
	}
	var t trace.TraceID
	var s trace.SpanID
	_, _ = rand.Read(t[:])
	_, _ = rand.Read(s[:])
	return t, s
}

func (hopIDs) NewSpanID(context.Context, trace.TraceID) trace.SpanID {
	var s trace.SpanID
	_, _ = rand.Read(s[:])
	return s
}

// hopOf reads the settled identity off the request, through the ONE parser this
// process has for it — the W3C propagator. A request carrying nothing parsable
// yields a zero hop and the SDK mints a fresh pair, which is the right answer for
// a span that genuinely begins here.
func hopOf(c *zip.Ctx) hop {
	sc := trace.SpanContextFromContext(propagator.Extract(context.Background(), inbound{c}))
	return hop{trace: sc.TraceID(), span: sc.SpanID()}
}

// traceSkip reports paths that must NOT open a span: the liveness/readiness/
// metrics surface (the ops port at :9090 already owns it) and the per-subsystem
// HIP-0106 health contract (GET /v1/<name>/health). Excluding them keeps traces
// signal — a probe every second is noise, not a request worth a trace.
func traceSkip(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/health", "/livez", "/metrics", "/favicon.ico":
		return true
	}
	// HIP-0106 per-subsystem liveness: GET /v1/<name>/health.
	if strings.HasPrefix(path, "/v1/") && strings.HasSuffix(path, "/health") {
		return true
	}
	return false
}

// traceable reports whether a request path gets a span. Scope is the versioned
// API surface (/v1/*) — the LLM/agent/data plane the o11y Monitoring tab tracks —
// minus the health noise above. Non-API paths (the console SPA, published
// <slug>.hanzo.app sites, the /zap WebSocket) are not traced here: they are not
// the api.hanzo.ai/v1 request plane this instrumentation exists to observe.
func traceable(path string) bool {
	if !strings.HasPrefix(path, "/v1/") {
		return false
	}
	return !traceSkip(path)
}

// TracingMiddleware emits ONE OpenTelemetry SERVER span per /v1/* request through
// the global (ZAP) tracer provider, so every api.hanzo.ai request lands in
// hanzoai/datastore over the ZAP wire alongside the log pipeline. It records the
// OTel HTTP semantic-convention attributes (method, route, status), sets the span
// status on error/5xx, and — critically — PROPAGATES the span context onto the
// request via SetContext so nested spans (agent.run, agent.step, the LLM chat
// client span in clients/aihttp.go) parent correctly under the request span. That
// makes a full agent trace a single tree: request → run → step → chat.
//
// It is a plain zip.Handler wrapping c.Continue(), the framework's idiomatic
// middleware form (a plain request-scoped handler); no otelfiber shim is
// needed because zip already exposes method/path/status/context.
func TracingMiddleware() zip.Handler {
	return func(c *zip.Ctx) error {
		// c.Path()/c.Method()/headers are ZERO-COPY views over the fasthttp request
		// buffer (Fiber's default, non-Immutable). The batch span processor
		// serializes a span ASYNCHRONOUSLY, after this request's ctx is recycled
		// for the next request — so a retained view is silently overwritten with
		// the NEXT request's bytes. strings.Clone pins our own copy; without it
		// span attributes corrupt under load (observed live: GET /v1/models spans
		// exported with http.route="/v1/chat/c…"). The span NAME already survives
		// because concatenation allocates, but every retained attribute must copy.
		path := strings.Clone(c.Path())
		if !traceable(path) {
			return c.Continue()
		}
		method := strings.Clone(c.Method())
		start := time.Now()

		// JOIN the caller's trace rather than starting a new one, then write the
		// enriched context back so the rest of the chain (and every downstream
		// client that pulls c.Context()) nests under it.
		//
		// The framework settles the request's trace before this runs — parsing the
		// caller's traceparent when there is one, starting a trace when there is
		// not — and forwards that same header on every call it makes to another
		// process. So the id that makes one request's spans ONE trace is already on
		// the wire at every hop. Nothing here had ever read it, which is why a span
		// tree stopped dead at each process boundary: the Slack webhook and the
		// agent run it caused were two unrelated traces, and no query could join
		// them.
		//
		// It is read for BOTH ids and taken as an identity rather than a parent —
		// see [hop] for what the second half fixes.
		//
		// WithNewRoot says the rule at the call rather than trusting the context to
		// hold it: a span in c.Context() (a middleware added later, a context a
		// handler chain carried in) would silently make the request boundary a
		// child again and the plane would have no roots a second time. It is also
		// what sends this span to hopIDs for the pair below, since the SDK asks for
		// new ids exactly when there is no parent.
		h := hopOf(c)
		ctx, span := httpTracer.Start(
			context.WithValue(c.Context(), hopKey{}, h),
			method+" "+path,
			trace.WithNewRoot(),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", method),
				attribute.String("http.route", path),
				attribute.String("url.path", path),
				attribute.String("server.address", strings.Clone(c.Header("Host"))),
			),
		)
		// The identity does not travel past the span it belongs to: a nested span
		// that asked for a root of its own would otherwise be handed this request's
		// id and two spans would share one row.
		c.Fiber().SetContext(context.WithValue(ctx, hopKey{}, hop{}))
		defer span.End()

		if rid := strings.Clone(c.RequestID()); rid != "" {
			span.SetAttributes(attribute.String("hanzo.request_id", rid))
		}

		// Which of the ~60 co-resident subsystems served this request. Stamped HERE —
		// the one place every /v1 request is already traced — so per-subsystem request
		// rate / error rate / latency / last-error fall out of the trace table the
		// admin board already reads, with no per-package instrumentation and no second
		// metrics path. Empty (nothing owns the path) means no label, never a guess.
		sub := SubsystemOf(path)
		if sub != "" {
			span.SetAttributes(attribute.String("hanzo.subsystem", sub))
		}

		err := c.Continue()

		status := c.Fiber().Response().StatusCode()
		// A handler that RETURNS an error has not written a response yet: fiber
		// unwinds the chain and calls ErrorHandler after, so the response object
		// still holds its default 200 here. The error carries the status the
		// caller receives, and mapError is what ErrorHandler renders with — one
		// answer to what status an error is, and pure. observeRequest below takes
		// the same value, so the span and the metric agree.
		if err != nil {
			status = mapError(err).Status
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))

		// A request's telemetry names the org its identity was VALIDATED for, or
		// none. That org is principal.Minted — the attestation the identity
		// boundary parks in a request-local slot, which is server-side state: it
		// is not a header, it does not cross the wire, and there is no request
		// that creates one. So it is the org for a caller the boundary resolved,
		// and empty for a caller it did not, in both cases independently of where
		// in a chain this middleware sits or whether a boundary ran at all.
		//
		// It is read HERE rather than from c.Org(), which answers a different
		// question: that header carries the org a request ACTS IN for the data
		// path, and on the bearer-less path the boundary restores the caller's own
		// value into it (middleware_identity.go, the Phase-1 residual). Telemetry
		// asks who this request was PROVED to be, and only the attestation
		// answers that. Both signals take the one value, so a span's tenant and
		// its metric series name the same org or neither does — the row's tenant
		// column is read straight off this attribute (apps/o11y planeOrg), and a
		// span carrying none is the platform's own.
		p, _ := principal.Minted(c)
		org := p.Org
		if org != "" {
			span.SetAttributes(attribute.String("hanzo.org", org))
		}

		// The METRIC half of the same observation. It belongs here and nowhere
		// else: this is the one place every /v1 request already has its path,
		// its status and its attested org in hand, and computing them twice in
		// a second middleware would be the same fact measured two ways.
		//
		// It had been written (metrics_http.go) and never called, so
		// hanzo_http_requests_total did not exist in the store — which is why
		// "/v1/event is 5xx" was unalertable while the Sentry envelope returned
		// 503 for a day with nobody paged. A metric nothing calls is not
		// instrumentation, it is a comment.
		// The subsystem stamped on the span above is the value the metric carries
		// too — one prefix scan per request, read twice, so a request's series and
		// its span name one app or neither does.
		observeRequest(sub, productFromPath(path), org, status, time.Since(start))
		switch {
		case err != nil:
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		case status >= 500:
			span.SetStatus(codes.Error, "")
		default:
			span.SetStatus(codes.Ok, "")
		}
		return err
	}
}

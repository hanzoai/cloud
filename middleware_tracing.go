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
	"strings"
	"time"

	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
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
		// The framework stamps a W3C traceparent on every inbound request before
		// this middleware runs — parsing the caller's when there is one, minting a
		// sampled one at the edge when there is not — and forwards that same header
		// on every call it makes to another process. So the id that makes one
		// request's spans ONE trace is already on the wire at every hop. Nothing
		// here had ever read it, which is why a span tree stopped dead at each
		// process boundary: the Slack webhook and the agent run it caused were two
		// unrelated traces, and no query could join them.
		//
		// Extracting collapses the two id spaces into one. A span's trace id now
		// equals the `trace` field the logger already prints for the same request,
		// so a log line and a span are reachable from each other.
		ctx, span := httpTracer.Start(
			propagator.Extract(c.Context(), inbound{c}),
			method+" "+path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", method),
				attribute.String("http.route", path),
				attribute.String("url.path", path),
				attribute.String("server.address", strings.Clone(c.Header("Host"))),
			),
		)
		c.Fiber().SetContext(ctx)
		defer span.End()

		if rid := strings.Clone(c.RequestID()); rid != "" {
			span.SetAttributes(attribute.String("hanzo.request_id", rid))
		}

		// Which of the ~60 co-resident subsystems served this request. Stamped HERE —
		// the one place every /v1 request is already traced — so per-subsystem request
		// rate / error rate / latency / last-error fall out of the trace table the
		// admin board already reads, with no per-package instrumentation and no second
		// metrics path. Empty (nothing owns the path) means no label, never a guess.
		if sub := SubsystemOf(path); sub != "" {
			span.SetAttributes(attribute.String("hanzo.subsystem", sub))
		}

		err := c.Continue()

		// Identity headers are validated upstream (IdentityMiddleware) before the
		// handler runs, so by here c.Org() reflects the authenticated org.
		status := c.Fiber().Response().StatusCode()
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		org := strings.Clone(c.Org())
		if org != "" {
			span.SetAttributes(attribute.String("hanzo.org", org))
		}

		// The METRIC half of the same observation. It belongs here and nowhere
		// else: this is the one place every /v1 request already has its path,
		// its status and its VALIDATED org in hand, and computing them twice in
		// a second middleware would be the same fact measured two ways.
		//
		// It had been written (metrics_http.go) and never called, so
		// hanzo_http_requests_total did not exist in the store — which is why
		// "/v1/event is 5xx" was unalertable while the Sentry envelope returned
		// 503 for a day with nobody paged. A metric nothing calls is not
		// instrumentation, it is a comment.
		observeRequest(productFromPath(path), org, status, time.Since(start))
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

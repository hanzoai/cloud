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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// newRecordingTracer wires httpTracer to an in-memory SpanRecorder so a test can
// assert what the middleware emitted WITHOUT a live collector. It rebinds the
// package-level httpTracer (restored via t.Cleanup) rather than touching the OTel
// global — the global's first-writer-wins delegation makes it unusable across
// tests, and the middleware reads httpTracer directly.
func newRecordingTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	// The SAME id source the composition root installs (telemetry.go). A test
	// provider without it would mint a request boundary's ids where production
	// adopts them, so the one property these tests exist to hold could not be seen.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr), sdktrace.WithIDGenerator(hopIDs{}))
	prev := httpTracer
	httpTracer = tp.Tracer(TracerName)
	t.Cleanup(func() { httpTracer = prev; _ = tp.Shutdown(t.Context()) })
	return sr
}

func attrOf(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func findSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// TestTracingMiddleware_EmitsServerSpan drives a real request through a zip app
// with the middleware and asserts a SERVER span with the HTTP semantic-convention
// attributes and the mapped status.
func TestTracingMiddleware_EmitsServerSpan(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"ok": "yes"})
	})

	req := httptest.NewRequest("GET", "/v1/models", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.Name() != "GET /v1/models" {
		t.Errorf("span name = %q, want %q", s.Name(), "GET /v1/models")
	}
	if s.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want Server", s.SpanKind())
	}
	if v, ok := attrOf(s, "http.request.method"); !ok || v.AsString() != "GET" {
		t.Errorf("http.request.method = %v (ok=%v), want GET", v.AsString(), ok)
	}
	if v, ok := attrOf(s, "http.route"); !ok || v.AsString() != "/v1/models" {
		t.Errorf("http.route = %v (ok=%v), want /v1/models", v.AsString(), ok)
	}
	if v, ok := attrOf(s, "http.response.status_code"); !ok || v.AsInt64() != 200 {
		t.Errorf("http.response.status_code = %d (ok=%v), want 200", v.AsInt64(), ok)
	}
	if s.Status().Code != codes.Ok {
		t.Errorf("status code = %v, want Ok", s.Status().Code)
	}
}

// TestTracingMiddleware_PropagatesContext proves the span written onto the
// request context parents a downstream (handler-created) span — the invariant
// that makes an agent run one trace tree (request → run → step → chat).
func TestTracingMiddleware_PropagatesContext(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Post("/v1/agents/run", func(c *zip.Ctx) error {
		// Downstream span opened off c.Context() — exactly how clients/agents and
		// clients/aihttp open their spans.
		_, child := httpTracer.Start(c.Context(), "agent.run")
		child.End()
		return c.JSON(200, map[string]string{"ok": "yes"})
	})

	req := httptest.NewRequest("POST", "/v1/agents/run", nil)
	if _, err := app.Test(req); err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2 (server + child)", len(spans))
	}
	server := findSpan(spans, "POST /v1/agents/run")
	child := findSpan(spans, "agent.run")
	if server == nil || child == nil {
		t.Fatalf("missing spans: server=%v child=%v", server != nil, child != nil)
	}
	if child.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Errorf("child not parented under server span: child.parent=%v server=%v",
			child.Parent().SpanID(), server.SpanContext().SpanID())
	}
	if child.SpanContext().TraceID() != server.SpanContext().TraceID() {
		t.Errorf("child/server trace IDs differ: %v vs %v",
			child.SpanContext().TraceID(), server.SpanContext().TraceID())
	}
}

// TestTracingMiddleware_ErrorStatus maps a 5xx to an error span status.
func TestTracingMiddleware_ErrorStatus(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/boom", func(c *zip.Ctx) error {
		return zip.Errorf(500, "boom")
	})

	req := httptest.NewRequest("GET", "/v1/boom", nil)
	if _, err := app.Test(req); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", spans[0].Status().Code)
	}
}

// TestTracingMiddleware_SkipsNoise asserts health/metrics + non-/v1 paths open no
// span, so probes and static paths never flood the trace store.
func TestTracingMiddleware_SkipsNoise(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/agents/health", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/healthz", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	app.Get("/", func(c *zip.Ctx) error { return c.JSON(200, "ok") })

	for _, p := range []string{"/v1/agents/health", "/healthz", "/"} {
		if _, err := app.Test(httptest.NewRequest("GET", p, nil)); err != nil {
			t.Fatalf("app.Test %s: %v", p, err)
		}
	}
	if n := len(sr.Ended()); n != 0 {
		t.Fatalf("recorded %d spans, want 0 (all skipped)", n)
	}
}

func TestTraceable(t *testing.T) {
	cases := map[string]bool{
		"/v1/chat/completions": true,
		"/v1/models":           true,
		"/v1/agents/x/run":     true,
		"/v1/agents/health":    false,
		"/v1/kms/health":       false,
		"/healthz":             false,
		"/metrics":             false,
		"/":                    false,
		"/zap":                 false,
		"/assets/app.js":       false,
	}
	for path, want := range cases {
		if got := traceable(path); got != want {
			t.Errorf("traceable(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestTracingMiddleware_JoinsTheCallersTrace is the correlation contract: a
// request that arrives carrying trace context is recorded IN that trace, not in a
// new one of its own.
//
// This is what makes a run's spans one chain across the fleet. The framework
// stamps a W3C traceparent at the edge and forwards it on every call it makes to
// another process, so the webhook, the plane hop that dispatches the agent and
// the run itself all arrive carrying the same id. Until this was extracted, each
// process started a fresh root and the three were three unrelated traces — every
// one of them individually well-formed, which is why nothing looked broken.
//
// It fails without the extraction: the middleware rooted its own trace and the
// assertion below reads a different id.
func TestTracingMiddleware_JoinsTheCallersTrace(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	// The handler reads the header the framework settled — trace id kept, span id
	// this hop's — which is the pair every log line of this request prints.
	var settled string
	app.Get("/v1/models", func(c *zip.Ctx) error {
		settled = c.Header("traceparent")
		return c.JSON(200, map[string]string{"ok": "yes"})
	})

	// A caller's trace, sampled, in the exact shape the framework forwards.
	const caller = "4bf92f3577b34da6a3ce929d0e0e4736"
	const parent = "00f067aa0ba902b7"
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("traceparent", "00-"+caller+"-"+parent+"-01")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("request: %v", err)
	}

	span := findSpan(sr.Ended(), "GET /v1/models")
	if span == nil {
		t.Fatal("no server span recorded")
	}
	if got := span.SpanContext().TraceID().String(); got != caller {
		t.Fatalf("server span is in trace %q, want the caller's %q — the request "+
			"started a trace of its own, so its spans cannot be joined to the caller's", got, caller)
	}
	// THE SPAN IS THE HOP, NOT A CHILD OF IT. What the header names by the time
	// this middleware runs is the framework's own hop id, and the framework emits
	// no OTel span of its own — so read as a parent it names a row nothing writes.
	// Measured live before this: 10,307 spans on the plane, ZERO with parent='',
	// and not one log line joining a span on span_id while 3,169 joined one on
	// `parent`. The read plane selects a trace's root with `parent = ''`, so the
	// service list and every waterfall answered empty over a complete span table.
	if hop := hopFrom(settled); span.SpanContext().SpanID().String() != hop {
		t.Errorf("span id = %s, want the hop %s the framework settled — the log lines "+
			"of this request name that one, so the line and the span cannot be joined",
			span.SpanContext().SpanID(), hop)
	}
	if span.Parent().HasSpanID() {
		t.Errorf("server span reports parent %s — that span is written nowhere, so this "+
			"row is a child of nothing and its trace has no root", span.Parent().SpanID())
	}
	// Sampling has to survive the join too. A parent that arrives sampled and a
	// child that is not recorded is the same silent drop in a different place.
	if !span.SpanContext().IsSampled() {
		t.Fatal("a span joined to a sampled caller must itself be sampled")
	}
}

// hopFrom reads the span id out of a W3C traceparent: 00-<32 trace>-<16 span>-<flags>.
func hopFrom(header string) string {
	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

// TestTracingMiddleware_RefusesAnUnusableCallerContext: a request that arrives
// with no trace context, or with a malformed or all-zero one, still gets a valid
// sampled trace — and never adopts the bogus id.
//
// Accepting an all-zero trace id is the failure worth naming: every broken sender
// emits the same one, so honouring it would merge unrelated requests from
// unrelated tenants into a single enormous trace. The framework rejects those and
// mints a fresh id before this middleware reads the header, so what extraction
// sees is always well-formed; this pins that the outcome is a real trace rather
// than the placeholder.
func TestTracingMiddleware_RefusesAnUnusableCallerContext(t *testing.T) {
	const zeros = "00000000000000000000000000000000"
	for _, tc := range []struct{ name, header string }{
		{"absent", ""},
		{"malformed", "not-a-traceparent"},
		{"all-zero trace id", "00-" + zeros + "-00f067aa0ba902b7-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := newRecordingTracer(t)

			app := zip.New(zip.Config{})
			app.Use(TracingMiddleware())
			app.Get("/v1/models", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "yes"}) })

			req := httptest.NewRequest("GET", "/v1/models", nil)
			if tc.header != "" {
				req.Header.Set("traceparent", tc.header)
			}
			if _, err := app.Test(req); err != nil {
				t.Fatalf("request: %v", err)
			}

			span := findSpan(sr.Ended(), "GET /v1/models")
			if span == nil {
				t.Fatal("no server span recorded")
			}
			id := span.SpanContext().TraceID()
			if !id.IsValid() {
				t.Fatal("a request with no usable caller context must still get a valid trace id")
			}
			if id.String() == zeros {
				t.Fatal("the all-zero placeholder was adopted as a trace id — every broken " +
					"sender would land in this one trace")
			}
			if !span.SpanContext().IsSampled() {
				t.Fatal("a minted trace must be sampled, or the request records nothing")
			}
		})
	}
}

// THE HOP BELONGS TO ONE SPAN. The request boundary adopts an identity that was
// settled before it, and everything nested under it mints its own — otherwise a
// child would land on the same row as its parent and the waterfall would be one
// span deep forever.
func TestNestedWorkKeepsItsOwnIdentity(t *testing.T) {
	sr := newRecordingTracer(t)

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/models", func(c *zip.Ctx) error {
		// A nested span the way a handler makes one, and a second that asks for a
		// root of its own — the case where a leaked identity would surface.
		_, child := httpTracer.Start(c.Fiber().Context(), "agent.run")
		child.End()
		_, detached := httpTracer.Start(c.Fiber().Context(), "job", trace.WithNewRoot())
		detached.End()
		return c.JSON(200, map[string]string{"ok": "yes"})
	})

	if _, err := app.Test(httptest.NewRequest("GET", "/v1/models", nil)); err != nil {
		t.Fatalf("request: %v", err)
	}

	seen := map[string]string{}
	for _, s := range sr.Ended() {
		id := s.SpanContext().SpanID().String()
		if other, dup := seen[id]; dup {
			t.Fatalf("%q and %q share span id %s — two spans, one row", other, s.Name(), id)
		}
		seen[id] = s.Name()
	}
	server := findSpan(sr.Ended(), "GET /v1/models")
	child := findSpan(sr.Ended(), "agent.run")
	if server == nil || child == nil {
		t.Fatalf("recorded %v, want the server span and its child", seen)
	}
	if child.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Errorf("agent.run parents to %s, want the request boundary %s — nested work "+
			"must still hang off the span the request is", child.Parent().SpanID(), server.SpanContext().SpanID())
	}
}

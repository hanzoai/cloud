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
	"os"
	"strconv"
	"testing"

	"github.com/hanzoai/cloud/zaptrace"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"net/http/httptest"
)

// TestTracingMiddleware_LiveZAP is the on-wire proof: it ships real request spans
// through the EXACT production path — TracingMiddleware → the global tracer → the
// zaptrace exporter → the ZAP wire — to a live collector's ZAP receiver, so an
// operator can watch receiver="zap" accept them and see hanzo-cloud rows land in
// hanzoai/datastore. Skipped unless CLOUD_ZAP_LIVE_ENDPOINT is set (host:4319 of a
// reachable collector, e.g. a `kubectl port-forward` to otel-collector:4319), so
// CI never depends on a collector.
//
//	CLOUD_ZAP_LIVE_ENDPOINT=localhost:4319 CLOUD_ZAP_LIVE_N=8 \
//	  go test -run TestTracingMiddleware_LiveZAP -v .
func TestTracingMiddleware_LiveZAP(t *testing.T) {
	ep := os.Getenv("CLOUD_ZAP_LIVE_ENDPOINT")
	if ep == "" {
		t.Skip("set CLOUD_ZAP_LIVE_ENDPOINT=host:4319 to ship spans to a live ZAP receiver")
	}
	n := 8
	if v := os.Getenv("CLOUD_ZAP_LIVE_N"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}

	ctx := t.Context()
	exp, err := otlptrace.New(ctx, zaptrace.New(ep))
	if err != nil {
		t.Fatalf("zap trace exporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "hanzo-cloud"))),
	)
	// Bind the middleware's tracer to this real ZAP provider for the test, then
	// restore — deterministic regardless of the OTel global's first-writer state.
	prev := httpTracer
	httpTracer = tp.Tracer(TracerName)
	t.Cleanup(func() { httpTracer = prev })

	app := zip.New(zip.Config{})
	app.Use(TracingMiddleware())
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"object": "list"})
	})
	app.Post("/v1/chat/completions", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]any{"id": "live-proof"})
	})

	for i := 0; i < n; i++ {
		if _, err := app.Fiber().Test(httptest.NewRequest("GET", "/v1/models", nil)); err != nil {
			t.Fatalf("GET /v1/models #%d: %v", i, err)
		}
		if _, err := app.Fiber().Test(httptest.NewRequest("POST", "/v1/chat/completions", nil)); err != nil {
			t.Fatalf("POST /v1/chat/completions #%d: %v", i, err)
		}
	}

	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("force flush: %v", err)
	}
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	t.Logf("shipped %d spans (service.name=hanzo-cloud) to ZAP receiver %s", 2*n, ep)
}

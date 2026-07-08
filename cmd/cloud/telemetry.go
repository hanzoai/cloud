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

// OTel telemetry bootstrap — installs the global tracer provider that ships this
// service's spans to the shared o11y backend (SigNoz) over OTLP. This is the ONE
// way a Hanzo Go daemon emits OpenTelemetry: one call, one service.name, so the
// console's per-product Monitoring tab (which filters by service.name) lights up
// for this product.
//
// Posture mirrors the ai module (ai/object/telemetry.go): opt-in via
// OTEL_EXPORTER_OTLP_ENDPOINT, non-fatal, and a clean no-op when the endpoint is
// unset — SAFE to ship before the collector is live. The OTLP HTTP exporter
// self-configures from the standard OTEL_EXPORTER_OTLP_* env (endpoint, headers,
// per-scheme TLS, timeout) — never hard-coded; New() does not dial and the batch
// span processor exports in the background, so boot never blocks on the collector.
package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/hanzoai/cloud/zaptrace"
)

// defaultZapEndpoint is the collector's ZAP-native OTLP receiver (zapreceiver).
const defaultZapEndpoint = "otel-collector.hanzo.svc:4319"

// initTelemetry installs the global OTel tracer provider for serviceName and
// returns a shutdown func that flushes and stops the exporter. The returned func
// is ALWAYS non-nil, so callers defer it unconditionally. An operator may
// override serviceName at runtime with OTEL_SERVICE_NAME.
func initTelemetry(ctx context.Context, serviceName string) func(context.Context) {
	// Enable when a ZAP endpoint is set OR (legacy) any OTLP endpoint is set —
	// keep the clean no-op-when-unset posture so this is safe before the
	// collector is live.
	zapEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_ZAP_ENDPOINT"))
	legacy := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	if zapEndpoint == "" && legacy == "" {
		log.Printf("telemetry: disabled (set OTEL_EXPORTER_ZAP_ENDPOINT to emit OTel spans over ZAP to o11y)")
		return func(context.Context) {}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		serviceName = v
	}
	exp, wire, err := newTraceExporter(ctx, zapEndpoint)
	if err != nil {
		log.Printf("telemetry: create trace exporter: %v", err)
		return func(context.Context) {}
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	)
	otel.SetTracerProvider(tp)

	// Composition-root ownership: cloud installs the ONE tracer provider (ZAP),
	// so retire the OTLP-exporter env before any subsystem boots. An embedded
	// subsystem — notably hanzoai/ai's object.InitTelemetry, invoked during
	// ai.Bootstrap at mount — reads OTEL_EXPORTER_OTLP_*ENDPOINT and, if set,
	// installs a SECOND, competing OTLP tracer provider. That second provider
	// forks the trace path: it wins the global slot for any tracer created
	// afterward (the ai GenAI tracer), stranding those spans on OTLP(:4318) while
	// this ZAP provider owns the rest — the exact split that left receiver="zap"
	// at zero. Clearing it here guarantees exactly one provider (ZAP) and one
	// wire, deterministically, regardless of CR env drift. In the fused binary
	// OTLP is only ever the collector's interop RECEIVER, never cloud's exporter;
	// standalone cmd/aid (no ZAP endpoint) is unaffected and keeps its OTLP path.
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	log.Printf("telemetry: OTel spans -> o11y over %s (service.name=%s)", wire, serviceName)
	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			log.Printf("telemetry: shutdown: %v", err)
		}
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// newTraceExporter builds the ZAP-native trace exporter. There is ONE wire:
// spans ride the ZAP transport (zap-proto/http) to the collector's zapreceiver.
//
// A legacy OTLP endpoint (OTEL_EXPORTER_OTLP_*ENDPOINT) only signals INTENT to
// emit — it never selects a transport. It maps to the ZAP endpoint (explicit
// override, else the default collector), so a stray/standard OTLP env var can
// never silently downgrade tenant-carrying spans to plaintext OTLP-HTTP(:4318).
// OTLP is only ever the collector's interop RECEIVER, never cloud's exporter.
func newTraceExporter(ctx context.Context, zapEndpoint string) (*otlptrace.Exporter, string, error) {
	if zapEndpoint == "" {
		zapEndpoint = defaultZapEndpoint
	}
	exp, err := otlptrace.New(ctx, zaptrace.New(zapEndpoint))
	return exp, "ZAP wire=" + zapEndpoint, err
}

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
	if zapEndpoint == "" {
		zapEndpoint = defaultZapEndpoint
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		serviceName = v
	}
	// ZAP-native transport: spans ride the ZAP wire (zap-proto/http), never
	// OTLP-HTTP(:4318)/gRPC(:4317). Payload stays standard OTLP protobuf.
	exp, err := otlptrace.New(ctx, zaptrace.New(zapEndpoint))
	if err != nil {
		log.Printf("telemetry: create ZAP trace exporter: %v", err)
		return func(context.Context) {}
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", serviceName))),
	)
	otel.SetTracerProvider(tp)
	log.Printf("telemetry: OTel spans -> o11y over ZAP wire=%s (service.name=%s)", zapEndpoint, serviceName)
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

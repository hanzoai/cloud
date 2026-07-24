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

// OTel tracer-provider bootstrap — installs the process-global provider that ships
// this service's spans to o11y over the in-process trace sink (Cost 0), and ADOPTS
// that provider into the embedded hanzoai/ai module so ai emits its gen_ai span per
// LLM call through the SAME provider (one provider, one wire). It lives HERE, in
// clients/o11y, because only this package may import both the o11y trace sink
// (NewTraceExporter) and the ai module — the cloud root package cannot (clients/o11y
// imports cloud, so cloud importing clients/o11y would be a cycle). It registers
// itself with cloud.RegisterTelemetryInstaller in init(); cloud.Serve invokes it ONCE,
// before MountAll, on EVERY entrypoint (cmd/cloud + every `hanzo <svc>`) — the ONE
// bootstrap site, so no entrypoint is telemetry-dark.
//
// Posture: opt-in and non-fatal. Enabled when the in-process sink is on
// (O11Y_TRACES_ZAP_INPROCESS) OR a ZAP/OTLP wire endpoint is set; a clean no-op
// otherwise. New() does not dial and the batch span processor exports in the
// background, so boot never blocks on the collector.
package o11y

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/zaptrace"
)

// defaultZapEndpoint is the collector's ZAP-native OTLP receiver (zapreceiver), used
// as the wire fallback when a legacy OTLP endpoint (intent to ship remotely) is set
// but no explicit ZAP endpoint is.
const defaultZapEndpoint = "otel-collector.hanzo.svc:4319"

func init() {
	// Register the tracer-provider bootstrap so cloud.Serve installs it on EVERY
	// entrypoint (cmd/cloud + every `hanzo <svc>`), before ai mounts. The inversion
	// (clients/o11y -> cloud) is the same one the trace sink already uses.
	cloud.RegisterTelemetryInstaller(installTraceProvider)
}

// installTraceProvider installs the global OTel tracer provider for serviceName,
// adopts it into the embedded ai module, and returns a shutdown func that flushes and
// stops the exporter. The returned func is ALWAYS non-nil, so Serve defers it
// unconditionally. An operator may override serviceName at runtime with
// OTEL_SERVICE_NAME.
func installTraceProvider(ctx context.Context, serviceName string) func(context.Context) {
	// Enable when a ZAP endpoint is set OR (legacy) any OTLP endpoint is set OR the
	// in-process o11y sink is enabled (spans route in-process, no wire endpoint
	// needed). Keep the clean no-op-when-unset posture so this is safe before any
	// path is live.
	zapEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_ZAP_ENDPOINT"))
	legacy := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	if zapEndpoint == "" && legacy == "" && !TraceInprocEnabled() {
		log.Printf("telemetry: disabled (set O11Y_TRACES_ZAP_INPROCESS=true or OTEL_EXPORTER_ZAP_ENDPOINT to emit OTel spans to o11y)")
		return func(context.Context) {}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		serviceName = v
	}
	// Resolve the wire fallback endpoint: an explicit ZAP endpoint, else (legacy OTLP
	// intent) the default collector, else none — in-process only. The wire client is
	// used only when the in-process o11y sink isn't registered.
	wireEndpoint := zapEndpoint
	if wireEndpoint == "" && legacy != "" {
		wireEndpoint = defaultZapEndpoint
	}
	var wire otlptrace.Client
	wireDesc := "none (in-process only)"
	if wireEndpoint != "" {
		wire = zaptrace.New(wireEndpoint)
		wireDesc = "ZAP wire=" + wireEndpoint
	}
	exp, err := NewTraceExporter(ctx, wire)
	if err != nil {
		log.Printf("telemetry: create trace exporter: %v", err)
		return func(context.Context) {}
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
			// deployment.environment on the resource so o11y's Environment column
			// resolves instead of defaulting to "default". Every span this provider
			// exports (cloud's own + the adopted ai gen_ai spans) inherits it.
			attribute.String("deployment.environment", deploymentEnvironment()),
		)),
	)
	otel.SetTracerProvider(tp)

	// Composition-root ownership: cloud installs the ONE tracer provider (wired to the
	// o11y in-process trace sink) and DECLARES it to the embedded hanzoai/ai module so
	// ai emits every gen_ai span through THIS provider instead of forking its own.
	// Without this, ai's object.InitTelemetry (invoked during ai.Bootstrap at mount)
	// finds no exporter endpoint — in-process mode sets none and we clear the OTLP env
	// just below — and DISABLES its emit. That is exactly why the gen_ai plane was
	// dark: cloud's own /v1/* request spans reached o11y_traces but every LLM call's
	// gen_ai span was gated off. One provider, one wire, one signal.
	aiobject.AdoptHostTracerProvider()

	// Defense in depth for the same one-provider invariant: retire the OTLP-exporter
	// env so no OTel-instrumented library auto-configures a SECOND, competing OTLP
	// provider that would win the global slot for tracers created afterward and split
	// the wire. ai already rides this provider via the adopt above; this stops any
	// other subsystem from forking one, deterministically, regardless of CR env drift.
	// In the fused binary OTLP is only ever the collector's interop RECEIVER, never
	// cloud's exporter; a standalone service with only an OTLP endpoint still gets a
	// provider here (that endpoint became the ZAP wire above), so telemetry stays on.
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	log.Printf("telemetry: OTel spans -> o11y in-process (Cost 0), wire fallback %s (service.name=%s)", wireDesc, serviceName)
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

// deploymentEnvironment resolves the process deployment environment for the OTel
// resource. Env-overridable (DEPLOYMENT_ENVIRONMENT / OTEL_DEPLOYMENT_ENVIRONMENT /
// ENVIRONMENT); defaults to "production" because this provider installs only when a
// sink/wire is configured — i.e. a real deployment.
func deploymentEnvironment() string {
	if v := firstNonEmptyEnv("DEPLOYMENT_ENVIRONMENT", "OTEL_DEPLOYMENT_ENVIRONMENT", "ENVIRONMENT"); v != "" {
		return v
	}
	return "production"
}

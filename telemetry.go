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

// Telemetry bootstrap — the ONE site, owned by the HOST.
//
// Every request this process serves gets a span, whether or not o11y happens to
// be co-resident. That makes the tracer provider a HOST concern: the composition
// root installs it once, before anything mounts, and owns it for the process
// life.
//
// It did not used to be. The provider was BUILT inside clients/o11y and handed
// back through a registered installer — which worked only while clients/o11y was
// LINKED INTO the host, because its init() did the registering. The moment o11y
// became a plugin (cloud.PluginSpec -> zip.Load, its own binary) that init() ran
// in the CHILD, the host's installer stayed nil, and this bootstrap became what
// its own doc called "a clean no-op": tracing dark fleet-wide, including the
// gen_ai spans it adopts into the ai module.
//
// What stayed in clients/o11y is what genuinely belongs to o11y: the collector,
// the datastore exporter, and the SDK-span -> pdata conversion (spanconv.go),
// which is coupled to the o11y_index_v3 schema and to nothing here. What moved
// here is the provider, its resource, its lifecycle — and ONE Send.
//
// TRANSPORT — one call site, two deployments:
//
//	spans -> traceRouter.Send(traceDest)
//	          |- Cost 0 : a CO-RESIDENT sink's handler (clients/o11y installs one
//	          |           via RegisterTraceSink) receives the LIVE span batch.
//	          '- ErrNoRoute: the ZAP wire (luxfi/trace) to o11y's ZAP receiver.
//
// When o11y is linked in, RegisterTraceSink ran in THIS process and the router
// takes the Cost-0 leg — no socket, no serialization. When o11y is a plugin,
// RegisterTraceSink ran in the CHILD, this router has no route, and the SAME
// Send falls through to the wire — the ZAP hop to the child's receiver. Neither
// the producer nor the exporter branches on where o11y lives; the routing table
// answers that, which is the whole point of routing it.
//
// Posture: opt-in and non-fatal. Enabled when a co-resident sink is expected
// (O11Y_TRACES_ZAP_INPROCESS) OR a ZAP/OTLP wire endpoint is set; a clean no-op
// otherwise. Nothing dials at install time and the batch span processor exports
// in the background, so boot never blocks on a collector.
package cloud

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"
	luxmetric "github.com/luxfi/metric"
	luxtrace "github.com/luxfi/trace"
	"github.com/luxfi/zap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// defaultZapEndpoint is the collector's ZAP-native OTLP receiver (zapreceiver),
// used as the wire fallback when a legacy OTLP endpoint (intent to ship
// remotely) is set but no explicit ZAP endpoint is.
const defaultZapEndpoint = "otel-collector.hanzo.svc:4319"

// traceDest is the destination cloud's OWN spans route to. One name for the
// aspect "somewhere that stores traces"; who serves it is the router's business.
const traceDest zap.Destination = "hanzo.o11y.traces"

// traceInproc is the Cost-0 interface a co-resident sink registers on, and
// traceRouter is the table the exporter Sends through. Both private: the ONLY
// way in is RegisterTraceSink, so the payload contract cannot drift between the
// producer (below) and the consumer.
var (
	traceInproc = zap.NewInProcessInterface()
	traceRouter = func() *zap.Router {
		r := zap.NewRouter()
		r.Register(traceInproc)
		return r
	}()
)

// tracerProviderInstalled records whether this process installed the OTel
// tracer provider. Read by the composition root (apps/) to decide whether to
// adopt it into the embedded ai module — adopting an uninstalled (global no-op)
// provider would latch ai's telemetry "ready" against a provider that discards
// every span, and suppress ai's own standalone fallback. See TracerProviderInstalled.
var tracerProviderInstalled atomic.Bool

// TracerProviderInstalled reports whether InstallTelemetry installed the OTel
// tracer provider in THIS process. It is the one fact the composition root needs
// in order to adopt the host provider into subsystems that emit their own spans
// (apps/install.go -> aiobject.AdoptHostTracerProvider). A value, not a callback:
// the same shape as TierReader/BalanceReader in ai.go, and for the same reason —
// importing github.com/hanzoai/ai/object here would put 1270 packages under every
// subsystem that imports cloud for Deps.
func TracerProviderInstalled() bool { return tracerProviderInstalled.Load() }

// TraceSink consumes a finished batch of THIS process's spans. It is what a
// co-resident o11y installs so cloud's own spans reach the embedded datastore
// with no socket and no serialization: the batch arrives live, by value.
//
// It takes SDK spans, not collector pdata, deliberately. The pdata conversion is
// coupled to the o11y_index_v3 schema the exporter writes, so it belongs on the
// consuming side (clients/o11y/spanconv.go) — that is what keeps
// go.opentelemetry.io/collector out of the host.
type TraceSink func(ctx context.Context, spans []sdktrace.ReadOnlySpan) error

// RegisterTraceSink installs the co-resident span consumer; a nil sink
// deregisters it. clients/o11y calls this at mount (and with nil at teardown, so
// a late export falls back to the wire instead of writing into a closed
// exporter).
//
// A registration is process-local BY CONSTRUCTION, and that is the mechanism that
// makes one code path serve both deployments: linked-in o11y registers here and
// wins the Cost-0 leg; plugin o11y registers in its OWN process, this router stays
// empty, and the exporter's identical Send falls through to the ZAP wire.
func RegisterTraceSink(sink TraceSink) {
	if sink == nil {
		traceInproc.Register(traceDest, nil)
		return
	}
	traceInproc.Register(traceDest, func(ctx context.Context, _ zap.Destination, p zap.Payload) (zap.Payload, error) {
		spans, ok := p.Value.([]sdktrace.ReadOnlySpan)
		if !ok {
			return zap.Payload{}, errUnexpectedTracePayload
		}
		return zap.Payload{}, sink(ctx, spans)
	})
}

// errUnexpectedTracePayload can only fire if something other than
// routerTraceExporter Sends to traceDest. Both ends live in this file, so it is
// unreachable today and exists so a future second producer fails loudly.
var errUnexpectedTracePayload = errors.New("cloud: trace sink received a non-span payload")

// TraceInprocEnabled reports whether the operator opted the in-process trace
// sink in. It is the ONE gate, read by the producer here (whether to install a
// provider at all when no wire endpoint is set) and by clients/o11y (whether to
// mount the sink) — no second source of truth. Fail-closed: unset ⇒ spans stay
// on the wire path.
func TraceInprocEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("O11Y_TRACES_ZAP_INPROCESS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// InstallTelemetry installs this process's OTel tracer and meter providers and
// returns a shutdown that flushes and stops both. The returned func is ALWAYS
// non-nil, so callers defer it unconditionally.
//
// Call it ONCE per process, from the composition root, BEFORE anything mounts.
// Serve does exactly that — so every per-app plugin entrypoint, which shares its
// body, installs identically and before ai mounts and reads the adopted-ready
// flag. It is exported because Serve is not the only composition root: a plugin
// binary (plugin/o11y) is a host for its own requests and
// needs the same providers, and one bootstrap that every root calls is the only
// way that stays true. No root writes its own.
//
// An operator may override serviceName at runtime with OTEL_SERVICE_NAME.
func InstallTelemetry(ctx context.Context, log luxlog.Logger, serviceName string) func(context.Context) {
	// Telemetry is never allowed to take the process down, and that starts with
	// its own arguments: a root that has no logger yet still gets a provider.
	if log == nil {
		log = luxlog.New("cloud")
	}
	log = log.New("subsystem", "telemetry")

	// Enable when a ZAP endpoint is set OR (legacy) any OTLP endpoint is set OR a
	// co-resident sink is expected (spans route in-process, no wire endpoint
	// needed). Keep the clean no-op-when-unset posture so this is safe before any
	// path is live.
	zapEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_ZAP_ENDPOINT"))
	legacy := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	if zapEndpoint == "" && legacy == "" && !TraceInprocEnabled() {
		log.Info("telemetry disabled: no span destination configured",
			"hint", "set O11Y_TRACES_ZAP_INPROCESS=true (o11y linked in) or OTEL_EXPORTER_ZAP_ENDPOINT=<host:port> (o11y as a plugin or remote)")
		return func(context.Context) {}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		serviceName = v
	}

	// Resolve the wire fallback: an explicit ZAP endpoint, else (legacy OTLP
	// intent) the default collector, else none — Cost-0 in-process only. The wire
	// is used exactly when no co-resident sink is registered, which is precisely
	// the o11y-as-a-plugin case.
	wireEndpoint := zapEndpoint
	if wireEndpoint == "" && legacy != "" {
		wireEndpoint = defaultZapEndpoint
	}
	var wire sdktrace.SpanExporter
	wireDesc := "none (co-resident sink only)"
	if wireEndpoint != "" {
		// luxfi/trace owns the ZAP span wire; it speaks exactly what o11y's
		// zapreceiver decodes, so the same bytes reach a co-located plugin, a
		// sidecar or the fleet collector.
		w, err := luxtrace.NewZAPExporter(
			luxtrace.ExporterConfig{Type: luxtrace.ZAP, Endpoint: wireEndpoint},
			serviceName, "",
		)
		if err != nil {
			log.Warn("ZAP wire exporter unavailable; co-resident sink only", "endpoint", wireEndpoint, "err", err)
		} else {
			wire = w
			wireDesc = "ZAP wire=" + wireEndpoint
		}
	}

	res := resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		// deployment.environment on the RESOURCE so o11y's Environment column
		// resolves instead of defaulting to "default". Every span and metric this
		// process exports (cloud's own + the adopted ai gen_ai spans) inherits it.
		attribute.String("deployment.environment", deploymentEnvironment()),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(&routerTraceExporter{router: traceRouter, dest: traceDest, wire: wire}),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// The SAME composition-root ownership applies to metrics. Without a provider
	// installed here every instrument in the process binds to the global no-op and
	// every measurement is discarded while the code looks instrumented — which is
	// exactly what cloud's request counters (metrics_http.go) did before this.
	// luxfi/metric adapts the OTel SDK onto the ZAP wire, so metrics reach o11y the
	// way traces and logs do.
	var mp *sdkmetric.MeterProvider
	if mexp, err := luxmetric.NewOTelZAPExporter(luxmetric.ZAPExporterConfig{
		Endpoint: wireEndpoint,
		AppName:  serviceName,
		Resource: map[string]string{"deployment.environment": deploymentEnvironment()},
	}); err != nil {
		log.Warn("metric exporter unavailable; metrics disabled", "err", err)
	} else {
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
	}

	// Latch BEFORE MountAll runs, so the composition root can adopt this provider
	// into subsystems that emit their own spans. Without that adoption ai's
	// object.InitTelemetry finds no exporter endpoint — in-process mode sets none
	// and the OTLP env is cleared just below — and DISABLES its emit. That is
	// exactly why the gen_ai plane was dark: cloud's own /v1/* request spans
	// reached o11y_traces but every LLM call's gen_ai span was gated off.
	tracerProviderInstalled.Store(true)

	// Defense in depth for the one-provider invariant: retire the OTLP-exporter
	// env so no OTel-instrumented library auto-configures a SECOND, competing OTLP
	// provider that would win the global slot for tracers created afterward and
	// split the wire. In the fused binary OTLP is only ever the collector's interop
	// RECEIVER, never cloud's exporter; a standalone service with only an OTLP
	// endpoint still gets a provider here (that endpoint became the ZAP wire
	// above), so telemetry stays on.
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	log.Info("telemetry installed",
		"service.name", serviceName,
		"environment", deploymentEnvironment(),
		"inproc_sink", TraceInprocEnabled(),
		"wire", wireDesc)

	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			log.Warn("tracer provider shutdown", "err", err)
		}
		// The periodic reader owns a goroutine and a buffer; stopping it is what
		// flushes the final interval.
		if mp != nil {
			if err := mp.Shutdown(ctx); err != nil {
				log.Warn("meter provider shutdown", "err", err)
			}
		}
		tracerProviderInstalled.Store(false)
	}
}

// routerTraceExporter is the ONE transport decision, expressed as a routing
// table rather than a caller branch: Send to the destination, and take the wire
// only when nothing in this process can reach it.
type routerTraceExporter struct {
	router *zap.Router
	dest   zap.Destination
	wire   sdktrace.SpanExporter // nil ⇒ co-resident sink only
}

var _ sdktrace.SpanExporter = (*routerTraceExporter)(nil)

// ExportSpans hands the LIVE batch to the router. With a co-resident sink that
// is Cost 0 — no encode, no socket, the sink's handler receives these exact
// spans. Without one the wire fallback runs. With neither, ErrNoRoute surfaces
// to the SDK: visible, never a silent drop. Steady state always has one of the
// two; only the brief pre-mount boot window can hit it.
func (e *routerTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if len(spans) == 0 {
		return nil
	}
	_, err := e.router.Send(ctx, e.dest, zap.Payload{Value: spans})
	if errors.Is(err, zap.ErrNoRoute) && e.wire != nil {
		return e.wire.ExportSpans(ctx, spans)
	}
	return err
}

// Shutdown only binds the wire fallback; the in-process interface is stateless
// and the sink's own teardown (clients/o11y) flushes the datastore exporter.
func (e *routerTraceExporter) Shutdown(ctx context.Context) error {
	if e.wire != nil {
		return e.wire.Shutdown(ctx)
	}
	return nil
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
// resource. Env-overridable (DEPLOYMENT_ENVIRONMENT / OTEL_DEPLOYMENT_ENVIRONMENT
// / ENVIRONMENT); defaults to "production" because the providers install only
// when a sink/wire is configured — i.e. a real deployment.
func deploymentEnvironment() string {
	if v := firstNonEmptyEnv("DEPLOYMENT_ENVIRONMENT", "OTEL_DEPLOYMENT_ENVIRONMENT", "ENVIRONMENT"); v != "" {
		return v
	}
	return "production"
}

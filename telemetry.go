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
// It did not used to be. The provider was BUILT inside the o11y package and
// handed back through a registered installer — which worked only while o11y was
// LINKED INTO the host, because its init() did the registering. The moment o11y
// became a plugin (a manifest row -> zip.Load, its own binary) that init() ran in
// the CHILD, the host's installer stayed nil, and this bootstrap became what its
// own doc called "a clean no-op": tracing dark fleet-wide, including the gen_ai
// spans it adopts into the ai module.
//
// What stayed in apps/o11y is what genuinely belongs to o11y: the collector, the
// warehouse exporter, and the SDK-span -> pdata conversion (spanconv.go), which is
// coupled to the span table's schema and to nothing here. What moved here is the
// provider, its resource, its lifecycle — and ONE Send.
//
// TRANSPORT — LOCALITY picks it, never the caller. Three rungs, one routing
// table, one call site:
//
//	spans -> traceRouter.Send(traceDest)
//	          |- Cost 0 : SAME PROCESS. A sink registered here (RegisterTraceSink)
//	          |           receives the LIVE span batch — no socket, no encode.
//	          '- ErrNoRoute: the wire, which is the o11y RECEIVER's address:
//	                         SAME MACHINE  — loopback, the co-located o11y plugin.
//	                         CROSS MACHINE — OTEL_EXPORTER_ZAP_ENDPOINT, an operator
//	                                         naming an o11y that is not on this box.
//
// The same-process rung is decided by the ROUTER, not by configuration. A
// registration is process-local by construction, so "is o11y in this process"
// is a fact the routing table already holds; an env var asserting it is a second
// source of truth that can only ever be wrong. It WAS wrong: every child of the
// host inherits one environment, so a flag meaning "o11y is in this process" read
// true in 112 processes and true-in-fact in one, and the 111 others reached
// ExportSpans with no route and no wire and dropped every span.
//
// The same-machine rung is loopback because o11y is a SIBLING PROCESS: the host
// spawns it from manifest/apps.go and both live in one container. A cluster
// Service address for a process one hop away is a network round trip bought for
// nothing — and the one that was hardcoded named the METRIC port for SPANS, so it
// would not have decoded had anything reached it.
//
// Non-fatal throughout. Nothing dials at install time and the batch span
// processor exports in the background, so boot never blocks on a collector.
// OTEL_SDK_DISABLED=true (the OTel standard spelling, not a Hanzo invention)
// turns every signal off and installs nothing.
package cloud

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
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

// The o11y receiver ports — one per signal, because spans, logs and metrics are
// three SEPARATE listeners in the o11y process (zapreceiver, zaplogreceiver,
// zapmetricreceiver); no two of them share a node, so no two of them share a
// port. Named here, on the DIALLING side, and read by apps/o11y on the listening
// side, so each port is stated once and the halves cannot drift — which they had:
// the wire default said 4319 (metrics) while the span receiver bound 4317.
const (
	O11ySpanPort   = 4317
	O11yLogPort    = 4318
	O11yMetricPort = 4319
)

// localO11y is the SAME-MACHINE rung: the co-located o11y plugin's receiver for
// one signal. Loopback, because the host spawns o11y as a sibling process in this
// container (manifest/apps.go) — a request that leaves the box to reach a process
// on the box is the hop this ladder exists to delete.
//
// It is loopback rather than a unix socket only because the ZAP node is TCP by
// construction: luxfi/zap's Node listens with net.Listen("tcp", …) and dials with
// net.DialTimeout("tcp", …), and zapreceiver derives its bind from a PORT. Every
// hop that CAN be a socket already is one — /v1/o11y and /v1/sentry ride the
// child's private unix socket through zip.Load. Closing the last of it is a
// luxfi/zap change (accept a path, the way zip's own networkOf already does),
// not a change here.
func localO11y(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

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
// co-resident o11y installs so cloud's own spans reach the embedded warehouse
// with no socket and no serialization: the batch arrives live, by value.
//
// It takes SDK spans, not collector pdata, deliberately. The pdata conversion is
// coupled to the schema the exporter writes, so it belongs on the consuming side
// (apps/o11y/spanconv.go) — that is what keeps go.opentelemetry.io/collector out
// of the host.
type TraceSink func(ctx context.Context, spans []sdktrace.ReadOnlySpan) error

// RegisterTraceSink installs the co-resident span consumer; a nil sink
// deregisters it. apps/o11y calls this at mount (and with nil at teardown, so a
// late export falls back to the wire instead of writing into a closed exporter).
//
// A registration is process-local BY CONSTRUCTION, and that is the ONE fact the
// same-process rung is decided on. In the o11y binary this registers and its own
// spans never leave the process; in the host and in all 111 sibling plugins it
// never runs, the router has no route, and the identical Send falls through to
// the wire — which now names the o11y sibling on this machine.
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

// telemetryDisabled reports the OTel standard kill switch. It is the ONE way to
// turn the signals off, and it is spelled the way the specification spells it so
// an operator does not have to learn a Hanzo name for a standard idea.
//
// There is deliberately no switch for WHERE spans go: that is locality, and
// locality is read off the routing table and the environment's one endpoint, not
// declared. See the package doc.
func telemetryDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED"))) {
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

	if telemetryDisabled() {
		log.Info("telemetry disabled by OTEL_SDK_DISABLED")
		return func(context.Context) {}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		serviceName = v
	}

	// THE LADDER, resolved once for every signal. An operator endpoint means an
	// o11y that is NOT on this machine and is honoured exactly as given; with none
	// set, o11y is the sibling process the host spawned and the destination is
	// local. Either way the wire is what the router falls through to — a sink
	// registered IN this process still wins, and that is how the o11y binary's own
	// spans never leave it.
	//
	// Per SIGNAL, because the receivers are per signal. Handing the metric
	// exporter the span port is how metrics silently went nowhere: the span
	// receiver decodes MsgSpanBatch and drops a MsgMetricBatch it cannot parse.
	remote := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_ZAP_ENDPOINT"))
	spanEndpoint, metricEndpoint := localO11y(O11ySpanPort), localO11y(O11yMetricPort)
	locality := "same machine (co-located o11y plugin)"
	if remote != "" {
		spanEndpoint, metricEndpoint = remote, remote
		locality = "cross machine (OTEL_EXPORTER_ZAP_ENDPOINT)"
	}

	// luxfi/trace owns the ZAP span wire; it speaks exactly what o11y's
	// zapreceiver decodes, so the same bytes reach a co-located plugin, a sidecar
	// or a remote collector. Construction failure is not fatal: the router's
	// Cost-0 leg still serves a co-resident o11y, and a process with neither
	// surfaces ErrNoRoute rather than dropping silently.
	var wire sdktrace.SpanExporter
	spanDesc := "unavailable"
	if w, err := luxtrace.NewZAPExporter(
		luxtrace.ExporterConfig{Type: luxtrace.ZAP, Endpoint: spanEndpoint},
		serviceName, "",
	); err != nil {
		log.Warn("ZAP span wire unavailable; co-resident sink only", "endpoint", spanEndpoint, "err", err)
	} else {
		wire = w
		spanDesc = spanEndpoint
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
		Endpoint: metricEndpoint,
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
	// reached the span store but every LLM call's gen_ai span was gated off.
	tracerProviderInstalled.Store(true)

	// Defense in depth for the one-provider invariant: retire the OTLP-exporter
	// env so no OTel-instrumented library auto-configures a SECOND, competing OTLP
	// provider that would win the global slot for tracers created afterward and
	// split the wire. OTLP is only ever the collector's interop RECEIVER, never
	// cloud's exporter — a remote o11y has ONE spelling (OTEL_EXPORTER_ZAP_ENDPOINT),
	// and these two named an address the ZAP wire cannot speak anyway.
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	_ = os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	log.Info("telemetry installed",
		"service.name", serviceName,
		"environment", deploymentEnvironment(),
		"locality", locality,
		"spans", spanDesc,
		"metrics", metricEndpoint)

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

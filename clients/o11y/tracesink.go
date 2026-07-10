// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// In-process trace sink — cloud's OWN spans (service + ai GenAI/LLM-obs, the
// highest-value insights data) reach the embedded o11y ClickHouse trace store
// WITHOUT a socket, via the ZAP locality-adaptive Router.
//
// The wire path (cmd/cloud/telemetry.go) used to ship every one of cloud's own
// spans over ZAP to a REMOTE collector. But cloud already embeds the o11y write
// side (ingest.go's chtraces exporter) against the SAME ClickHouse. So when the
// sender and the sink live in ONE binary, the wire is pure waste: the spans
// should be handed to the sink in-process.
//
// The router makes that a Cost table, not a caller branch. This file registers
// an InProcessInterface (Cost 0) handler on Destination TraceDest; cmd/cloud's
// tracer provider Sends there. When this sink is mounted, the Router delivers the
// LIVE span batch to the handler (zero ZAP-wire serialize, zero socket, no second
// collector hop) and the handler writes it to o11y_traces through the REAL
// chtraces exporter — the one writer that produces the o11y_index_v3 schema the
// embedded query plane reads. When the sink is NOT mounted (standalone aid, embed
// off), Router.Send returns ErrNoRoute and the producer falls back to the wire.
//
// The chtraces pdata->ClickHouse conversion (resource fingerprinting, tag
// attributes, materialized columns) lives unexported inside the exporter, so the
// sink reuses the exporter as a consumer.Traces rather than duplicating ~90 lines
// of schema-coupled conversion. The only cost paid on the in-process hop is one
// in-memory OTLP proto round-trip (SDK proto spans -> pdata) — no network, no
// remote decode, no second process.
//
// Safety posture (this feeds a LIVE, SHARED telemetry store):
//   - OPT-IN: mounts only when O11Y_TRACES_ZAP_INPROCESS is truthy AND a datastore
//     DSN is set. Inert until the flag flips (verify-then-cutover).
//   - Fail-soft: any construction error logs and returns nil, leaving cloud's
//     spans on the existing wire path — activating this can never take cloud down.
//   - Shutdown deregisters the handler (Router falls back to wire) then flushes
//     the exporter's sending queue to ClickHouse before exit.
package o11y

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	chtraces "github.com/hanzoai/otel-collector/exporter/clickhousetracesexporter"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	uberzap "go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/zap"
)

// TraceDest is the capability aspect cloud's OWN spans route to. The tracer
// provider (cmd/cloud/telemetry.go) Sends here; this sink registers the handler.
const TraceDest zap.Destination = "hanzo.o11y.traces"

// traceInproc is the Cost-0 interface the sink registers its handler on. Private:
// only mountTraceSink writes it; Router reads it via Send.
var traceInproc = zap.NewInProcessInterface()

// Router is cloud's shared locality-adaptive telemetry router. It carries the
// in-process interface today; a real ZAP-wire NodeInterface slots in behind the
// same Send call site later without moving the producer. cmd/cloud routes its own
// spans through it: in-process when this sink is mounted (Cost 0), else ErrNoRoute
// and the producer falls back to the ZAP wire client.
var Router = func() *zap.Router {
	r := zap.NewRouter()
	r.Register(traceInproc)
	return r
}()

// traceExporter pins the chtraces exporter for the process life so shutdown can
// flush it, mirroring embeddedIngest.
var traceExporter exporter.Traces

// TraceInprocEnabled reports whether the operator opted the in-process trace sink
// in. It is the ONE gate, read by both this sink (whether to mount) and the
// producer (whether to install the tracer provider at all when no wire endpoint
// is set) — no second source of truth. Fail-closed: unset ⇒ cloud's spans stay on
// the wire path.
func TraceInprocEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("O11Y_TRACES_ZAP_INPROCESS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func init() {
	// Order 73: after o11y-runtime (71) sets the DSN/runtime and o11y-otlp-ingest
	// (72). RegisterWithShutdown so the exporter's sending queue flushes on stop.
	cloud.RegisterWithShutdown("o11y-trace-inproc", 73, mountTraceSink, shutdownTraceSink)
}

// mountTraceSink builds the chtraces exporter and registers the in-process
// handler. Fail-soft at every branch: a disabled flag, a missing DSN, or a
// construction error all return nil, leaving cloud's spans on the wire path.
func mountTraceSink(_ any, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-trace-inproc")

	if !TraceInprocEnabled() {
		log.Info("in-process trace sink disabled (set O11Y_TRACES_ZAP_INPROCESS=true to route cloud's own spans in-process)")
		return nil
	}
	dsn := embeddedDSN()
	if dsn == "" {
		log.Warn("in-process trace sink enabled but no datastore DSN; cloud's spans stay on the wire path (needs O11Y_TELEMETRYSTORE_DATASTORE_DSN)")
		return nil
	}

	exp, err := buildTraceExporter(context.Background(), dsn)
	if err != nil {
		log.Warn("in-process trace sink init failed; cloud's spans stay on the wire path", "err", err)
		return nil // fail-soft
	}
	traceExporter = exp

	// Register the Cost-0 handler: it receives the LIVE proto span batch (no ZAP
	// wire encode, no socket) and writes it to o11y_traces via the real chtraces
	// exporter. The only serialization is an in-memory OTLP proto round-trip to
	// bridge SDK-exporter proto spans -> collector pdata.
	traceInproc.Register(TraceDest, func(ctx context.Context, _ zap.Destination, p zap.Payload) (zap.Payload, error) {
		spans, ok := p.Value.([]*tracepb.ResourceSpans)
		if !ok {
			return zap.Payload{}, fmt.Errorf("o11y trace sink: unexpected payload type %T (want []*tracepb.ResourceSpans)", p.Value)
		}
		td, err := protoToTraces(spans)
		if err != nil {
			return zap.Payload{}, fmt.Errorf("o11y trace sink: proto->pdata: %w", err)
		}
		return zap.Payload{}, exp.ConsumeTraces(ctx, td)
	})

	log.Info("in-process trace sink live: cloud's own spans -> o11y_traces (Cost-0, no socket)", "dest", TraceDest)
	return nil
}

// shutdownTraceSink deregisters the handler (so a late Send falls back to the
// wire) then flushes the exporter's sending queue to ClickHouse. Nil-safe.
func shutdownTraceSink(ctx context.Context) error {
	traceInproc.Register(TraceDest, nil)
	if traceExporter != nil {
		return traceExporter.Shutdown(ctx)
	}
	return nil
}

// buildTraceExporter constructs the chtraces exporter as a consumer.Traces over
// the datastore DSN and Starts it (idempotent schema ensure + sending-queue
// consumers). Same exporter, same DSN target the ingest collector already runs in
// prod — so Start against the live datastore carries no new risk.
func buildTraceExporter(ctx context.Context, dsn string) (exporter.Traces, error) {
	f := chtraces.NewFactory()
	cfg := f.CreateDefaultConfig().(*chtraces.Config)
	cfg.Datasource = dsn

	logger, err := uberzap.NewProduction()
	if err != nil {
		logger = uberzap.NewNop()
	}
	set := exporter.Settings{
		ID: component.NewID(f.Type()),
		TelemetrySettings: component.TelemetrySettings{
			Logger:         logger,
			TracerProvider: nooptrace.NewTracerProvider(),
			MeterProvider:  noopmetric.NewMeterProvider(),
			Resource:       pcommon.NewResource(),
		},
		BuildInfo: component.BuildInfo{
			Command:     "hanzo-cloud-trace-inproc",
			Description: "Hanzo Cloud in-process trace sink (cloud's own spans -> o11y_traces)",
			Version:     "embedded",
		},
	}
	exp, err := f.CreateTraces(ctx, set, cfg)
	if err != nil {
		return nil, fmt.Errorf("create chtraces exporter: %w", err)
	}
	// CreateTraces already opened the ClickHouse conn + spawned the writer's
	// background goroutine; a failed Start must release them, not leak on the
	// fail-soft path (Shutdown is safe to call after a failed Start).
	if err := exp.Start(ctx, nopHost{}); err != nil {
		_ = exp.Shutdown(ctx)
		return nil, fmt.Errorf("start chtraces exporter: %w", err)
	}
	return exp, nil
}

// protoToTraces bridges SDK-exporter OTLP proto spans (what otlptrace.Client hands
// UploadTraces) to collector pdata (what the chtraces exporter consumes). The two
// use distinct generated proto packages but the SAME OTLP wire format
// (resource_spans = field 1), so a marshal+unmarshal is the one public, stable
// bridge — an in-memory round-trip, never a socket.
func protoToTraces(spans []*tracepb.ResourceSpans) (ptrace.Traces, error) {
	b, err := proto.Marshal(&tracepb.TracesData{ResourceSpans: spans})
	if err != nil {
		return ptrace.Traces{}, err
	}
	return (&ptrace.ProtoUnmarshaler{}).UnmarshalTraces(b)
}

// nopHost is the minimal component.Host the exporter needs at Start: it uses no
// extensions (in-memory sending queue, no persistent-storage extension).
type nopHost struct{}

func (nopHost) GetExtensions() map[component.ID]component.Component { return nil }

// NewTraceExporter builds the OTel span exporter cloud installs on its ONE tracer
// provider. It is an otlptrace.Exporter whose Client routes every batch through
// the Router: in-process to the embedded ClickHouse sink when mounted (Cost 0 —
// the native proto batch is delivered by value, never serialized to a socket),
// else falling back to the wire client. otlptrace owns the (public) SDK-span ->
// OTLP-proto conversion; this owns only WHERE the proto batch goes. wire may be
// nil (in-process only). The composition root (cmd/cloud) still installs the
// provider and owns the single-provider invariant; this owns the transport.
func NewTraceExporter(ctx context.Context, wire otlptrace.Client) (*otlptrace.Exporter, error) {
	return otlptrace.New(ctx, &routerTraceClient{router: Router, dest: TraceDest, wire: wire})
}

// routerTraceClient is the otlptrace.Client that routes cloud's own spans through
// the ZAP Router: in-process to the embedded sink when mounted, else the wire.
type routerTraceClient struct {
	router *zap.Router
	dest   zap.Destination
	wire   otlptrace.Client // nil ⇒ in-process only
}

var _ otlptrace.Client = (*routerTraceClient)(nil)

// Start/Stop only bind the wire fallback; the in-process interface is stateless.
func (c *routerTraceClient) Start(ctx context.Context) error {
	if c.wire != nil {
		return c.wire.Start(ctx)
	}
	return nil
}

func (c *routerTraceClient) Stop(ctx context.Context) error {
	if c.wire != nil {
		return c.wire.Stop(ctx)
	}
	return nil
}

// UploadTraces routes the proto batch: the Router prefers the Cost-0 in-process
// interface (the sink's handler receives the LIVE spans), and only when nothing
// can reach the destination does the wire fallback run. With no fallback,
// ErrNoRoute surfaces (visible, never a silent drop) — steady state always has the
// sink; only the brief pre-mount boot window can hit it.
func (c *routerTraceClient) UploadTraces(ctx context.Context, protoSpans []*tracepb.ResourceSpans) error {
	_, err := c.router.Send(ctx, c.dest, zap.Payload{Value: protoSpans})
	if errors.Is(err, zap.ErrNoRoute) && c.wire != nil {
		return c.wire.UploadTraces(ctx, protoSpans)
	}
	return err
}

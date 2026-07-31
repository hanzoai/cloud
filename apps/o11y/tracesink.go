// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// In-process trace sink — the CONSUMER half of cloud's own span path.
//
// The host owns the tracer provider and the one Send that leaves it
// (cloud/telemetry.go). This file is what makes that Send free when o11y is
// linked into the same binary: it registers a handler on the host's trace
// destination, receives the LIVE span batch by value — zero encode, zero socket,
// no second collector hop — and writes it to o11y_traces through the REAL
// dstraces exporter, the one writer that produces the o11y_index_v3 schema the
// embedded query plane reads.
//
// Registration is process-local, and that is the entire deployment story: linked
// in, this handler exists and the host's router prefers it; as a plugin, this
// handler exists in the CHILD, the host's router has no route, and the host's
// identical Send falls through to the ZAP wire — which lands on the collector
// this same package mounts (ingest.go). One producer, one call site, both
// topologies.
//
// The pdata conversion (spanconv.go) lives HERE and not in the host on purpose:
// it is coupled to the datastore exporter's schema, and hoisting it would drag
// go.opentelemetry.io/collector into every package that imports cloud for Deps.
//
// Safety posture (this feeds a LIVE, SHARED telemetry store):
//   - OPT-IN: mounts only when O11Y_TRACES_ZAP_INPROCESS is truthy AND a datastore
//     DSN is set. Inert until the flag flips (verify-then-cutover).
//   - Fail-soft: any construction error logs and returns nil, leaving cloud's
//     spans on the wire path — activating this can never take cloud down.
//   - Shutdown deregisters the handler (host falls back to the wire) then flushes
//     the exporter's sending queue to datastore before exit.

package o11y

import (
	"context"
	"fmt"

	dstraces "github.com/hanzoai/otel-collector/exporter/datastoretracesexporter"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	uberzap "go.uber.org/zap"

	"github.com/hanzoai/cloud"
)

// traceExporter pins the dstraces exporter for the process life so shutdown can
// flush it, mirroring embeddedIngest.
var traceExporter exporter.Traces

// mountTraceSink builds the dstraces exporter and registers it as the host's
// co-resident trace sink. Called by mountO11y (o11y.go). It registers no
// /v1/o11y/* Fiber route, so it is order-independent. Fail-soft at every branch:
// a disabled flag, a missing DSN, or a construction error all return nil,
// leaving cloud's spans on the wire path.
func mountTraceSink(deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-trace-inproc")

	if !cloud.TraceInprocEnabled() {
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

	cloud.RegisterTraceSink(traceSink(exp))

	log.Info("in-process trace sink live: cloud's own spans -> o11y_traces (Cost-0, no socket)")
	return nil
}

// traceSink is the Cost-0 handler itself: the host hands over the LIVE SDK
// batch, this converts it once, in place, and writes it. No proto, no marshal,
// no OTLP — the path that advertised itself as Cost-0 used to serialize and
// deserialize every batch in memory just to reach this same pdata (spanconv.go).
//
// Named, and taking the narrow consumer.Traces rather than the concrete
// exporter, so the function that runs in production is the function a test can
// run — there is no second copy of the wiring to drift.
func traceSink(next consumer.Traces) cloud.TraceSink {
	return func(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
		return next.ConsumeTraces(ctx, spansToTraces(spans))
	}
}

// shutdownTraceSink deregisters the handler (so a late export falls back to the
// wire rather than writing into a closing exporter) then flushes the exporter's
// sending queue to datastore. Nil-safe.
func shutdownTraceSink(ctx context.Context) error {
	cloud.RegisterTraceSink(nil)
	if traceExporter != nil {
		return traceExporter.Shutdown(ctx)
	}
	return nil
}

// buildTraceExporter constructs the dstraces exporter as a consumer.Traces over
// the datastore DSN and Starts it (idempotent schema ensure + sending-queue
// consumers). Same exporter, same DSN target the ingest collector already runs in
// prod — so Start against the live datastore carries no new risk.
func buildTraceExporter(ctx context.Context, dsn string) (exporter.Traces, error) {
	f := dstraces.NewFactory()
	cfg := f.CreateDefaultConfig().(*dstraces.Config)
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
		return nil, fmt.Errorf("create dstraces exporter: %w", err)
	}
	// CreateTraces already opened the datastore conn + spawned the writer's
	// background goroutine; a failed Start must release them, not leak on the
	// fail-soft path (Shutdown is safe to call after a failed Start).
	if err := exp.Start(ctx, nopHost{}); err != nil {
		_ = exp.Shutdown(ctx)
		return nil, fmt.Errorf("start dstraces exporter: %w", err)
	}
	return exp, nil
}

// nopHost is the minimal component.Host the exporter needs at Start: it uses no
// extensions (in-memory sending queue, no persistent-storage extension).
type nopHost struct{}

func (nopHost) GetExtensions() map[component.ID]component.Component { return nil }

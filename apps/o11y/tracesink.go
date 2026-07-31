// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// In-process trace sink — the SAME-PROCESS rung of the span ladder.
//
// Every cloud process owns a tracer provider and one Send that leaves it
// (cloud/telemetry.go). This file is what makes that Send free IN THE O11Y
// BINARY: it registers a handler on the trace destination, receives the LIVE span
// batch by value — zero encode, zero socket, no collector hop — and writes it
// through the REAL dstraces exporter, the one writer the embedded query plane
// reads behind.
//
// Registration is process-local, and that is the WHOLE transport decision. This
// package is linked only into plugin/o11y, so this handler exists in exactly one
// process: o11y's own spans take the Cost-0 leg, and the host's and every sibling
// plugin's fall through to the wire — the loopback hop to the receiver ingest.go
// mounts, one rung down the ladder. Nothing branches on where o11y lives; the
// routing table answers it.
//
// The pdata conversion (spanconv.go) lives HERE and not in the host on purpose:
// it is coupled to the warehouse exporter's schema, and hoisting it would drag
// go.opentelemetry.io/collector into every package that imports cloud for Deps.
//
// Safety posture (this feeds a LIVE, SHARED telemetry store):
//   - Bound by CAPABILITY, not by a flag: it mounts exactly when a warehouse DSN
//     is configured, because the DSN is the thing it writes to — the same rule
//     ingest.go states next door, and for the same reason. The env flag that used
//     to gate it asserted "o11y is in this process", which the router already
//     knows for certain and which one shared environment made true in 112
//     processes and true-in-fact in one.
//   - Fail-soft: any construction error logs and returns nil, leaving these spans
//     on the wire path — mounting this can never take o11y down.
//   - Shutdown deregisters the handler (the Send falls back to the wire) then
//     flushes the exporter's sending queue before exit.
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

// mountTraceSink builds the dstraces exporter and registers it as this process's
// co-resident trace sink. Called by mountO11y (o11y.go). It registers no
// /v1/o11y/* Fiber route, so it is order-independent. Fail-soft at every branch:
// a missing DSN or a construction error returns nil, leaving these spans on the
// wire path.
func mountTraceSink(deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-trace-inproc")

	dsn := embeddedDSN()
	if dsn == "" {
		log.Warn("in-process trace sink not mounted: no warehouse DSN; o11y's own spans stay on the wire path (needs O11Y_TELEMETRYSTORE_DATASTORE_DSN)")
		return nil
	}

	exp, err := buildTraceExporter(context.Background(), dsn)
	if err != nil {
		log.Warn("in-process trace sink init failed; o11y's own spans stay on the wire path", "err", err)
		return nil // fail-soft
	}
	traceExporter = exp

	cloud.RegisterTraceSink(traceSink(exp))

	log.Info("in-process trace sink live: o11y's own spans stay in-process (Cost-0, no socket)")
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
			Description: "Hanzo Cloud in-process trace sink (o11y's own spans, no socket)",
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

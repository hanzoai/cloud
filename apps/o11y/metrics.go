// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"log/slog"
	"time"

	"github.com/hanzoai/o11y/pkg/datastoremetrics"
	"github.com/hanzoai/o11y/pkg/telemetrystore"
	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
	luxlog "github.com/luxfi/log"
)

// metricsIngest holds the native metrics receiver for the process life: write-only
// (a deliberate keepalive; nothing reads it), mirroring embeddedRuntime.
var metricsIngest *zapmetricreceiver.Receiver

// metricsBuffer coalesces batches for metricsIngest; held for the process life
// alongside it so a shutdown can flush what is still pending.
var metricsBuffer *metricBuffer

// startNativeMetricsIngest starts o11y-native datastore metrics ingest in-process:
// a ZAP metric receiver that decodes MsgMetricBatch and writes each batch to the
// datastore over UPSTREAM ch-go via o11y's pkg/datastoremetrics driver — with NO
// histogram-fork dependency (classic bucket/quantile decomposition, never a
// DDSketch/exp_hist). It reuses the embedded runtime's ONE datastore connection
// (store.Datastore), so the query plane (read) and metrics (write) ride the
// same conn — no second pool, no separate DSN.
//
// This is the piece that lets metrics finally live in-process alongside the
// merged traces/logs embed: the metrics exporter that blocked it needed the
// forked ch-go; this native path does not, so it composes with the upstream
// ch-go the query plane pins.
//
// BOUND BY CAPABILITY, not by a flag, and on the canonical address — the same
// posture the span and log ears beside it hold (planesink.go). The ear exists
// when there is a store to write to, because the store is the thing it writes
// to; there is nothing else for an operator to decide. It was gated on an
// address an operator had to name, which meant the fleet's hundred senders had
// nowhere to send: the ear was down, and a metric that reaches no ear is
// indistinguishable from an app that measures nothing.
//
// Fail-soft: any startup error is logged and swallowed — metrics ingest must
// never take the query plane down.
func startNativeMetricsIngest(store telemetrystore.TelemetryStore, log luxlog.Logger) {
	const listen = planeMetricListen
	if store == nil {
		log.Warn("native metrics ingest: no telemetry store; skipping", "listen", listen)
		return
	}
	conn := store.Datastore()
	if conn == nil {
		log.Warn("native metrics ingest: datastore connection unavailable; skipping", "listen", listen)
		return
	}

	writer := datastoremetrics.NewWriter(conn)
	// Batches are coalesced before they reach the datastore — see
	// metricsbuffer.go for the arithmetic. 5s bounds staleness against a store
	// whose finest rollup is five minutes wide; 2048 bounds memory if the
	// datastore stops accepting writes.
	metricsBuffer = newMetricBuffer(writer.WriteMetricsMany, 5*time.Second, 2048, slog.Default())
	rcv, err := zapmetricreceiver.New(zapmetricreceiver.Config{
		Listen:  listen,
		NodeID:  "cloud-o11y-metrics",
		OnBatch: metricsBuffer.add,
		Logger:  slog.Default(),
	})
	if err != nil {
		log.Warn("native metrics ingest failed to start; metrics stay on the collector path",
			"listen", listen, "err", err)
		return
	}
	metricsIngest = rcv
	log.Info("native datastore metrics ingest listening (upstream ch-go, no fork)", "listen", listen)
}

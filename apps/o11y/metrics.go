// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"log/slog"
	"os"
	"strings"

	"github.com/hanzoai/o11y/pkg/datastoremetrics"
	"github.com/hanzoai/o11y/pkg/telemetrystore"
	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
	luxlog "github.com/luxfi/log"
)

// metricsIngest holds the native metrics receiver for the process life: write-only
// (a deliberate keepalive; nothing reads it), mirroring embeddedRuntime.
var metricsIngest *zapmetricreceiver.Receiver

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
// OPT-IN + fail-soft by design. It is gated on O11Y_METRICS_ZAP_LISTEN and is a
// no-op until an operator sets it, so activating the embed never forces the
// metrics WRITE path on before it has been verified against the live datastore.
// Once verified, the standalone metrics collector (and its agents) can repoint
// here and be retired (verify-then-cutover). Any startup error is logged and
// swallowed — metrics ingest must never take the query plane down.
func startNativeMetricsIngest(store telemetrystore.TelemetryStore, log luxlog.Logger) {
	listen := strings.TrimSpace(os.Getenv("O11Y_METRICS_ZAP_LISTEN"))
	if listen == "" {
		return // disabled — the standalone collector keeps the metrics path
	}
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
	rcv, err := zapmetricreceiver.New(zapmetricreceiver.Config{
		Listen:  listen,
		NodeID:  "cloud-o11y-metrics",
		OnBatch: writer.WriteMetrics,
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

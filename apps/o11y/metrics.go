// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"log/slog"

	"github.com/hanzoai/cloud"
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
// It binds cloud.O11yMetricPort — the SAME port cloud/telemetry.go dials for the
// metric signal, named there once. It used to be an operator string
// (O11Y_METRICS_ZAP_LISTEN) with no counterpart on the dialling side, which is
// how the fleet's meter provider ended up pointed at the SPAN port: the span
// receiver decodes MsgSpanBatch, so every MsgMetricBatch that arrived was
// dropped. A port that two halves must agree on is one fact, so it has one name.
//
// Fail-soft by design: any startup error is logged and swallowed — metrics
// ingest must never take the query plane down.
func startNativeMetricsIngest(store telemetrystore.TelemetryStore, log luxlog.Logger) {
	listen := bindAll(cloud.O11yMetricPort)
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

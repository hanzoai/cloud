// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// OTLP telemetry INGEST — the in-process OpenTelemetry Collector that folds the
// standalone otel-collector Deployment into the unified cloud binary.
//
// cloud already embeds the o11y QUERY runtime (embed.go) over the ClickHouse
// datastore. This file adds the WRITE side: a real OpenTelemetry Collector,
// constructed IN-PROCESS, that accepts OTLP (gRPC :4317, HTTP :4318) and writes
// spans + logs into the SAME ClickHouse the embedded query runtime reads
// (o11y_traces / o11y_logs on the `insights` cluster). Consumers — cloud
// itself, console-worker, and third-party OTel SDKs — point at cloud instead of
// the standalone otel-collector Service, so the standalone Deployment can retire.
//
// Pipeline — trimmed from the standalone collector to the components that both
// (a) the live consumers exercise and (b) compile against cloud's UPSTREAM
// ClickHouse driver (clickhouse-go v2.44.0 / ch-go v0.71.0):
//
//	receivers:  otlp (grpc + http)
//	processors: memory_limiter -> resource(service.namespace=hanzo, deployment.environment) -> batch
//	exporters:  clickhousetraces (traces), clickhouselogsexporter (logs)
//
// DEFERRED — the METRICS pipeline (signozclickhousemetrics + the o11yspanmetrics
// connector) is intentionally NOT embedded here. That exporter references
// SigNoz's dd-sketch fork of ch-go (chproto.DD/Store/IndexMapping), which does
// NOT compile against cloud's upstream ch-go — the two driver lines cannot coexist
// in one binary because cloud's embedded o11y QUERY service pins upstream v2.44.0.
// Metrics ingest therefore stays on the standalone collector until the metrics
// exporter is ported onto upstream ch-go. See LLM.md.
//
// Safety posture (this touches a LIVE, SHARED telemetry store):
//   - OFF by default. Enabled only when CLOUD_OTLP_INGEST_ENABLED is truthy AND a
//     datastore DSN is set. Merging/deploying this is inert until the flag flips.
//   - Fail-soft: any construction error logs and returns nil, leaving the
//     standalone collector as the ingest path — activating this can never take
//     cloud down (mirrors embed.go's proxy fallback posture).
//   - Registered with a ShutdownFunc so the collector flushes on graceful stop.
package o11y

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/batchprocessor"
	"go.opentelemetry.io/collector/processor/memorylimiterprocessor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
	"go.opentelemetry.io/collector/service/telemetry/otelconftelemetry"

	resourceprocessor "github.com/open-telemetry/opentelemetry-collector-contrib/processor/resourceprocessor"

	chlogs "github.com/hanzoai/otel-collector/exporter/clickhouselogsexporter"
	chtraces "github.com/hanzoai/otel-collector/exporter/clickhousetracesexporter"

	"github.com/hanzoai/cloud"
)

// dsnEnvVar is the process-env key the embedded collector's config references via
// envprovider (${env:...}). The credential-bearing DSN is passed through the
// environment, never written to disk — the config file on the data volume holds
// only the ${env:} reference, matching the standalone collector's hygiene (its
// ConfigMap referenced ${env:DS_USER}/${env:DS_PASS}, never a plaintext secret).
const dsnEnvVar = "CLOUD_OTLP_INGEST_DSN"

// defaultEnvironment mirrors the standalone collector's resource attribute
// (deployment.environment=production) so spans written by the embed land under
// the same o11y filter as the data already in ClickHouse. Overridable.
const defaultEnvironment = "production"

// embeddedIngest holds the in-process ingest collector so shutdownIngest can
// reach it, mirroring embed.go's embeddedRuntime. Shutdown() signals Run() to return.
var embeddedIngest *otelcol.Collector

func init() {
	// Order 72: after o11y.Mount (70) and the query-runtime handler (71). The
	// ingest collector is independent of the query handler, but keeping it
	// adjacent groups the whole o11y subsystem. RegisterWithShutdown so the
	// collector's batch processor flushes to ClickHouse on graceful stop.
	cloud.RegisterWithShutdown("o11y-otlp-ingest", 72, mountIngest, shutdownIngest)
}

// ingestEnabled reports whether the operator has opted in. Fail-closed: unset or
// any non-truthy value keeps the embed off and the standalone collector live.
func ingestEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLOUD_OTLP_INGEST_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// mountIngest constructs and starts the in-process ingest collector when enabled.
// It is fail-soft at every branch: a disabled flag, a missing DSN, or a
// construction error all return nil, leaving the standalone collector as the
// ingest path.
func mountIngest(_ any, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-otlp-ingest")

	if !ingestEnabled() {
		log.Info("OTLP ingest disabled (set CLOUD_OTLP_INGEST_ENABLED=true to fold otel-collector into cloud)")
		return nil
	}
	dsn := embeddedDSN()
	if dsn == "" {
		log.Warn("OTLP ingest enabled but no datastore DSN set; skipping (needs O11Y_TELEMETRYSTORE_DATASTORE_DSN)")
		return nil
	}

	col, err := buildIngestCollector(deps, dsn)
	if err != nil {
		log.Warn("OTLP ingest init failed; standalone otel-collector remains the ingest path", "err", err)
		return nil // fail-soft
	}

	// Run blocks until Shutdown() (called by shutdownIngest) or a fatal component
	// error. Cloud owns its own HTTP listeners; the collector owns only the OTLP
	// receiver sockets and the ClickHouse exporter connections.
	go func() {
		if runErr := col.Run(context.Background()); runErr != nil {
			log.Error("OTLP ingest collector exited", "err", runErr)
		}
	}()
	embeddedIngest = col
	log.Info("OTLP ingest collector running", "otlp_grpc", ":4317", "otlp_http", ":4318", "sink", "ClickHouse traces+logs")
	return nil
}

// shutdownIngest gracefully stops the collector so the batch processor flushes
// buffered spans/logs to ClickHouse before exit. Idempotent and nil-safe.
func shutdownIngest(_ context.Context) error {
	if embeddedIngest != nil {
		embeddedIngest.Shutdown()
	}
	return nil
}

// buildIngestCollector assembles the trimmed factory set, renders the pipeline
// config to the data volume (secret-free — DSN via ${env:}), and constructs the
// collector. Pure enough to unit-test via DryRun without binding sockets or
// touching ClickHouse.
func buildIngestCollector(deps cloud.Deps, dsn string) (*otelcol.Collector, error) {
	factories, err := ingestFactories()
	if err != nil {
		return nil, fmt.Errorf("assemble factories: %w", err)
	}

	cfgPath, err := writeIngestConfig(deps)
	if err != nil {
		return nil, fmt.Errorf("write ingest config: %w", err)
	}

	// The credential-bearing DSN rides the environment (envprovider expands
	// ${env:CLOUD_OTLP_INGEST_DSN}); it is never written to the config file.
	_ = os.Setenv(dsnEnvVar, dsn)

	return otelcol.NewCollector(otelcol.CollectorSettings{
		BuildInfo: buildInfo(),
		Factories: func() (otelcol.Factories, error) { return factories, nil },
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs: []string{"file:" + cfgPath},
				ProviderFactories: []confmap.ProviderFactory{
					fileprovider.NewFactory(),
					envprovider.NewFactory(),
				},
			},
		},
	})
}

// ingestFactories is the ONE place the embedded pipeline's component set is
// declared — the trimmed, driver-compatible subset of the standalone collector's
// components.go. otelcol validates that every config key maps to a factory here,
// so a config/factory drift fails fast at construction (see ingest_test.go).
func ingestFactories() (otelcol.Factories, error) {
	receivers, err := otelcol.MakeFactoryMap([]receiver.Factory{
		otlpreceiver.NewFactory(),
	}...)
	if err != nil {
		return otelcol.Factories{}, err
	}
	processors, err := otelcol.MakeFactoryMap([]processor.Factory{
		memorylimiterprocessor.NewFactory(),
		resourceprocessor.NewFactory(),
		batchprocessor.NewFactory(),
	}...)
	if err != nil {
		return otelcol.Factories{}, err
	}
	exporters, err := otelcol.MakeFactoryMap([]exporter.Factory{
		chtraces.NewFactory(),
		chlogs.NewFactory(),
	}...)
	if err != nil {
		return otelcol.Factories{}, err
	}
	return otelcol.Factories{
		Receivers:  receivers,
		Processors: processors,
		Exporters:  exporters,
		Telemetry:  otelconftelemetry.NewFactory(),
	}, nil
}

// writeIngestConfig renders the collector pipeline YAML to the data volume and
// returns its path. The file contains NO secret — the DSN is a ${env:} reference.
//
// Collision guard: the collector binds ONLY the OTLP receiver ports (:4317,
// :4318). service.telemetry.metrics.level=none disables the collector's own
// self-metrics Prometheus reader (default :8888) so it can never contend with
// cloud's health listener (:9090) — the exact class of clash embed.go documents.
// No prometheus exporter (:8889), no health_check extension (:13133): cloud owns
// process health and its own OTel self-telemetry.
func writeIngestConfig(deps cloud.Deps) (string, error) {
	dataDir := filepath.Join(firstNonEmpty(deps.DataDir, "/var/lib/cloud"), "o11y")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	environment := firstNonEmpty(strings.TrimSpace(os.Getenv("CLOUD_OTLP_INGEST_ENVIRONMENT")), defaultEnvironment)

	cfg := "" +
		"receivers:\n" +
		"  otlp:\n" +
		"    protocols:\n" +
		"      grpc:\n" +
		"        endpoint: 0.0.0.0:4317\n" +
		"      http:\n" +
		"        endpoint: 0.0.0.0:4318\n" +
		"processors:\n" +
		"  memory_limiter:\n" +
		"    check_interval: 5s\n" +
		"    limit_mib: 768\n" +
		"    spike_limit_mib: 256\n" +
		"  resource:\n" +
		"    attributes:\n" +
		"      - key: service.namespace\n" +
		"        value: hanzo\n" +
		"        action: upsert\n" +
		"      - key: deployment.environment\n" +
		"        value: " + environment + "\n" +
		"        action: upsert\n" +
		"  batch:\n" +
		"    timeout: 2s\n" +
		"    send_batch_size: 2048\n" +
		"    send_batch_max_size: 4096\n" +
		"exporters:\n" +
		"  clickhousetraces:\n" +
		"    datasource: ${env:" + dsnEnvVar + "}\n" +
		"  clickhouselogsexporter:\n" +
		"    dsn: ${env:" + dsnEnvVar + "}\n" +
		"service:\n" +
		"  telemetry:\n" +
		"    metrics:\n" +
		"      level: none\n" +
		"    logs:\n" +
		"      level: warn\n" +
		"  pipelines:\n" +
		"    traces:\n" +
		"      receivers: [otlp]\n" +
		"      processors: [memory_limiter, resource, batch]\n" +
		"      exporters: [clickhousetraces]\n" +
		"    logs:\n" +
		"      receivers: [otlp]\n" +
		"      processors: [memory_limiter, resource, batch]\n" +
		"      exporters: [clickhouselogsexporter]\n"

	path := filepath.Join(dataDir, "otlp-ingest.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// buildInfo stamps the embedded collector so its logs identify the fused binary.
func buildInfo() component.BuildInfo {
	return component.BuildInfo{
		Command:     "hanzo-cloud-otlp-ingest",
		Description: "Hanzo Cloud embedded OTLP ingest (traces+logs -> ClickHouse)",
		Version:     "embedded",
	}
}

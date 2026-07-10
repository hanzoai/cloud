// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"testing"

	aiobject "github.com/hanzoai/ai/object"
)

// TestInstallTraceProvider_AdoptsHostProvider is the MED-1 contract and the fix that
// makes gen_ai spans emit on EVERY entrypoint. When the in-process sink is on but no
// wire endpoint is set (the live cloud config), installTraceProvider must (1) install
// a provider and (2) ADOPT it into the embedded ai module — so ai's InitTelemetry (run
// at mount, AFTER this) rides the host provider instead of disabling its emit. Since
// cloud.Serve calls this ONE bootstrap before MountAll on both the fused (enable=nil)
// and single-service (enable=[svc]) paths, latching here means both paths adopt.
func TestInstallTraceProvider_AdoptsHostProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("O11Y_TRACES_ZAP_INPROCESS", "true") // in-process sink on, no wire

	shutdown := installTraceProvider(context.Background(), "hanzo-cloud")
	if shutdown == nil {
		t.Fatal("installTraceProvider returned a nil shutdown func")
	}
	t.Cleanup(func() { shutdown(context.Background()) })

	if !aiobject.TelemetryEnabled() {
		t.Fatal("installTraceProvider must adopt the host provider into ai (aiobject.TelemetryEnabled() == true), else gen_ai spans stay gated off")
	}
}

// TestInstallTraceProvider_RetiresOTLPExporterEnv is the composition-root ownership
// invariant: once the provider is installed, the OTLP-exporter env is cleared so no
// embedded subsystem installs a second, competing OTLP provider and forks the trace
// path. The ZAP endpoint stays intact.
func TestInstallTraceProvider_RetiresOTLPExporterEnv(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "otel-collector.hanzo.svc:4319")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.hanzo.svc:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://otel-collector.hanzo.svc:4318")

	shutdown := installTraceProvider(context.Background(), "hanzo-cloud")
	t.Cleanup(func() { shutdown(context.Background()) })

	if v := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want cleared", v)
	}
	if v := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); v != "" {
		t.Errorf("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = %q, want cleared", v)
	}
	if v := firstNonEmptyEnv("OTEL_EXPORTER_ZAP_ENDPOINT"); v == "" {
		t.Errorf("OTEL_EXPORTER_ZAP_ENDPOINT was cleared, want intact")
	}
}

// TestInstallTraceProvider_DisabledIsNoop confirms the clean no-op posture: with no
// sink and no endpoint set, installTraceProvider returns a non-nil shutdown and does
// not panic (safe before any telemetry path is live).
func TestInstallTraceProvider_DisabledIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("O11Y_TRACES_ZAP_INPROCESS", "")

	shutdown := installTraceProvider(context.Background(), "hanzo-cloud")
	if shutdown == nil {
		t.Fatal("installTraceProvider returned a nil shutdown func")
	}
	shutdown(context.Background()) // must not panic
}

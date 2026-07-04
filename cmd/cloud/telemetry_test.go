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

package main

import (
	"context"
	"testing"
)

// TestInitTelemetry_RetiresOTLPExporterEnv is the composition-root ownership
// invariant: once cloud installs its ZAP tracer provider, the OTLP-exporter env
// is cleared so no embedded subsystem (hanzoai/ai's object.InitTelemetry) can
// install a second, competing OTLP provider and fork the trace path. The ZAP
// endpoint stays intact.
func TestInitTelemetry_RetiresOTLPExporterEnv(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "otel-collector.hanzo.svc:4319")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector.hanzo.svc:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://otel-collector.hanzo.svc:4318")

	shutdown := initTelemetry(context.Background(), "hanzo-cloud")
	t.Cleanup(func() { shutdown(context.Background()) })

	if v := envOrEmpty("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		t.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want cleared", v)
	}
	if v := envOrEmpty("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); v != "" {
		t.Errorf("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = %q, want cleared", v)
	}
	if v := envOrEmpty("OTEL_EXPORTER_ZAP_ENDPOINT"); v == "" {
		t.Errorf("OTEL_EXPORTER_ZAP_ENDPOINT was cleared, want intact")
	}
}

// TestInitTelemetry_DisabledIsNoop confirms the clean no-op posture: with no
// telemetry env set, initTelemetry returns a non-nil shutdown and touches
// nothing (safe before the collector is live).
func TestInitTelemetry_DisabledIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_ZAP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	shutdown := initTelemetry(context.Background(), "hanzo-cloud")
	if shutdown == nil {
		t.Fatal("initTelemetry returned a nil shutdown func")
	}
	shutdown(context.Background()) // must not panic
}

func envOrEmpty(k string) string { return firstNonEmptyEnv(k) }

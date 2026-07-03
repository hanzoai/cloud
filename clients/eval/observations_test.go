package eval

import (
	"context"
	"testing"
	"time"
)

// toObservationView maps a cloud_usage-ledger row (Observation) into the v3
// GENERATION shape the console Observe surface renders.
func TestToObservationViewSuccess(t *testing.T) {
	ts := time.Date(2026, 7, 3, 21, 35, 38, 0, time.UTC)
	v := toObservationView(Observation{
		ID: "id-1", TraceID: "req-1", Org: "hanzo", UserID: "hanzo/z",
		Model: "zen5-coder", Provider: "do-ai",
		PromptTokens: 95, CompletionTokens: 24, TotalTokens: 119,
		CostCents: 1, Status: "success", Timestamp: ts,
	})
	if v.Type != "GENERATION" {
		t.Fatalf("type = %q, want GENERATION", v.Type)
	}
	if v.TraceID != "req-1" {
		t.Fatalf("traceId = %q", v.TraceID)
	}
	if v.Model != "zen5-coder" || v.Name != "zen5-coder" {
		t.Fatalf("model/name = %q/%q", v.Model, v.Name)
	}
	if v.Level != "DEFAULT" {
		t.Fatalf("level = %q, want DEFAULT on success", v.Level)
	}
	if v.StatusMessage != nil {
		t.Fatalf("statusMessage = %v, want nil on success", *v.StatusMessage)
	}
	if v.Usage.Unit != "TOKENS" || v.Usage.Input != 95 || v.Usage.Output != 24 || v.Usage.Total != 119 {
		t.Fatalf("usage = %+v", v.Usage)
	}
	if v.StartTime != "2026-07-03T21:35:38Z" {
		t.Fatalf("startTime = %q", v.StartTime)
	}
	if got, _ := v.Metadata["costCents"].(int64); got != 1 {
		t.Fatalf("metadata.costCents = %v", v.Metadata["costCents"])
	}
	if v.Metadata["provider"] != "do-ai" {
		t.Fatalf("metadata.provider = %v", v.Metadata["provider"])
	}
}

// An errored generation becomes level=ERROR with the statusMessage set; an empty
// model falls back to a non-empty name (never a blank observation).
func TestToObservationViewError(t *testing.T) {
	v := toObservationView(Observation{
		ID: "id-2", Status: "error", ErrorMsg: "upstream 500", Timestamp: time.Now(),
	})
	if v.Level != "ERROR" {
		t.Fatalf("level = %q, want ERROR", v.Level)
	}
	if v.StatusMessage == nil || *v.StatusMessage != "upstream 500" {
		t.Fatalf("statusMessage = %v", v.StatusMessage)
	}
	if v.Name != "generation" {
		t.Fatalf("name = %q, want fallback 'generation'", v.Name)
	}
}

// asInt64 MUST decode ClickHouse UInt32 (tokens) / UInt64 (cost_cents) — asFloat
// only handles float types, so without this the token/cost columns would read 0.
func TestAsInt64Coercion(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{uint32(95), 95},     // prompt_tokens
		{uint64(4096), 4096}, // total_tokens / cost_cents
		{int64(-3), -3},
		{float64(42), 42},
		{uint8(7), 7},
		{nil, 0}, // absent column → zero, never a panic
		{"nan", 0},
	}
	for _, c := range cases {
		if got := asInt64(c.in); got != c.want {
			t.Fatalf("asInt64(%v[%T]) = %d, want %d", c.in, c.in, got, c.want)
		}
	}
}

// The in-memory telemetry holds no production cloud_usage ledger, so it returns an
// honest empty generations list — but still enforces the authoritative org.
func TestMemTelemetryListObservations(t *testing.T) {
	m := newMemTelemetry()
	obs, err := m.ListObservations(context.Background(), ObservationFilter{Org: "hanzo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("want empty, got %d", len(obs))
	}
	if _, err := m.ListObservations(context.Background(), ObservationFilter{}); err == nil {
		t.Fatal("want error on missing org (tenant isolation), got nil")
	}
}

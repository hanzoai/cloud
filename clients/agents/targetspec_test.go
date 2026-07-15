package agents

import (
	"context"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// TestSpecMetricsSanitize proves the capability + live-metrics values are bounded on
// the way in: no unbounded strings, no absurd GPU counts, no negative sizes, and no
// non-finite float that would break the JSON encode or the display. Sanitize is total
// (never errors) so the write path always has a safe value.
func TestSpecMetricsSanitize(t *testing.T) {
	longVendor := strings.Repeat("x", 500)
	huge := make([]GPU, 100)
	for i := range huge {
		huge[i] = GPU{Vendor: "nvidia", Model: "GB10", Memory: 1 << 40}
	}
	spec := Spec{
		OS: "linux", Arch: "arm64", CPUs: -5, Memory: -1,
		GPUs: append([]GPU{{Vendor: longVendor, Model: "x", Memory: -9}}, huge...),
	}.Sanitize()
	if spec.CPUs != 0 || spec.Memory != 0 {
		t.Fatalf("negative cpus/memory must clamp to 0, got cpus=%d mem=%d", spec.CPUs, spec.Memory)
	}
	if len(spec.GPUs) > maxGPUs {
		t.Fatalf("gpu list must cap at %d, got %d", maxGPUs, len(spec.GPUs))
	}
	if len(spec.GPUs[0].Vendor) > maxSpecField {
		t.Fatalf("gpu vendor must be length-capped, got %d", len(spec.GPUs[0].Vendor))
	}
	if spec.GPUs[0].Memory != 0 {
		t.Fatalf("negative gpu memory must clamp to 0, got %d", spec.GPUs[0].Memory)
	}

	m := Metrics{
		Load1: math.NaN(), Load5: math.Inf(1), Load15: -3,
		MemUsed: -1, MemFree: 1 << 30, GPUUtil: 9.5, At: 999,
	}.Sanitize()
	if m.Load1 != 0 || m.Load5 != 0 || m.Load15 != 0 {
		t.Fatalf("NaN/Inf/negative load must become 0, got %+v", m)
	}
	if m.MemUsed != 0 {
		t.Fatalf("negative memUsed must clamp to 0, got %d", m.MemUsed)
	}
	if m.GPUUtil != 1 {
		t.Fatalf("gpuUtil must clamp to [0,1], got %v", m.GPUUtil)
	}
	if m.At != 0 {
		t.Fatalf("Sanitize must NOT carry a client-supplied At (server owns the clock), got %d", m.At)
	}
	// A JSON encode of the sanitized metrics must succeed (proves no residual NaN/Inf).
	if encodeMetrics(m) == "" && !m.IsZero() {
		t.Fatal("sanitized non-zero metrics must encode to a non-empty blob")
	}
}

// TestTargetStoreSpecMetricsRoundTrip proves the capability + metrics survive a store
// write/read exactly (JSON column codec), and a malformed stored blob decodes to the
// zero value rather than failing the whole target read.
func TestTargetStoreSpecMetricsRoundTrip(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	spec := Spec{OS: "linux", Arch: "arm64", CPUs: 20, Memory: 128 << 30,
		GPUs: []GPU{{Vendor: "nvidia", Model: "GB10", Memory: 96 << 30}}}
	metrics := Metrics{Load1: 2.5, MemFree: 64 << 30, GPUUtil: 0.8}
	tg := Target{ID: "t1", Org: "acme", Label: "spark", Kind: TargetGPU, Status: TargetOnline,
		Host: "spark", Spec: spec, Metrics: metrics, MetricsAt: 1234, CreatedAt: 1, UpdatedAt: 1}
	if err := s.CreateTarget(ctx, tg); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetTarget(ctx, "acme", "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got.Spec, spec) {
		t.Fatalf("spec round-trip mismatch:\n got %+v\nwant %+v", got.Spec, spec)
	}
	if got.Metrics.Load1 != 2.5 || got.Metrics.MemFree != 64<<30 || got.Metrics.GPUUtil != 0.8 {
		t.Fatalf("metrics round-trip mismatch: %+v", got.Metrics)
	}
	if got.MetricsAt != 1234 {
		t.Fatalf("metricsAt column mismatch: %d", got.MetricsAt)
	}
}

// TestGetTargetByHost proves the idempotent-relink lookup is org-scoped (fail-closed
// cross-tenant), empty-host is a miss, and the newest wins on a duplicate host.
func TestGetTargetByHost(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	_ = s.CreateTarget(ctx, Target{ID: "a1", Org: "acme", Host: "box", Label: "old", CreatedAt: 10, UpdatedAt: 10, Kind: TargetMachine, Status: TargetOnline})
	_ = s.CreateTarget(ctx, Target{ID: "a2", Org: "acme", Host: "box", Label: "new", CreatedAt: 20, UpdatedAt: 20, Kind: TargetMachine, Status: TargetOnline})
	_ = s.CreateTarget(ctx, Target{ID: "e1", Org: "evil", Host: "box", Label: "evil", CreatedAt: 15, UpdatedAt: 15, Kind: TargetMachine, Status: TargetOnline})

	got, err := s.GetTargetByHost(ctx, "acme", "box")
	if err != nil || got.ID != "a2" {
		t.Fatalf("newest of acme's host wins, got %+v %v", got, err)
	}
	if _, err := s.GetTargetByHost(ctx, "acme", "  "); err != errTargetNotFound {
		t.Fatalf("empty host must miss, got %v", err)
	}
	if _, err := s.GetTargetByHost(ctx, "nobody", "box"); err != errTargetNotFound {
		t.Fatalf("a foreign org's host must fail-closed, got %v", err)
	}
}

// TestHTTPTargetCapabilityAndHeartbeat proves register carries spec + metrics into the
// view, the server stamps the metrics clock (not the client), a metrics PATCH is a
// heartbeat that refreshes the sample + clock, and a spec PATCH updates capability.
func TestHTTPTargetCapabilityAndHeartbeat(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	code, b := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{
		"label": "spark", "kind": TargetGPU, "host": "spark",
		"spec": map[string]any{"os": "linux", "arch": "arm64", "cpus": 20, "memory": 137438953472,
			"gpus": []map[string]any{{"vendor": "nvidia", "model": "GB10", "memory": 103079215104}}},
		// A client cannot forge the clock: it sends "at" but the server ignores it.
		"metrics": map[string]any{"load1": 1.5, "memFree": 68719476736, "gpuUtil": 0.4, "at": 42},
	})
	if code != http.StatusCreated {
		t.Fatalf("register want 201, got %d (%s)", code, b)
	}
	var tv targetView
	mustJSON(t, b, &tv)
	if tv.Spec == nil || tv.Spec.Arch != "arm64" || tv.Spec.CPUs != 20 || len(tv.Spec.GPUs) != 1 || tv.Spec.GPUs[0].Model != "GB10" {
		t.Fatalf("spec not carried into view: %+v", tv.Spec)
	}
	if tv.Metrics == nil || tv.Metrics.Load1 != 1.5 || tv.Metrics.GPUUtil != 0.4 {
		t.Fatalf("metrics not carried into view: %+v", tv.Metrics)
	}
	if tv.Metrics.At == 42 || tv.Metrics.At <= 0 {
		t.Fatalf("metrics clock must be server-stamped (not client 42), got %d", tv.Metrics.At)
	}
	if tv.MetricsAt == "" {
		t.Fatalf("metricsAt (rfc3339) must be set when metrics present")
	}

	// A metrics PATCH is a heartbeat: refresh the sample, keep the clock owned by us.
	code, b = do(t, app, http.MethodPatch, "/v1/agents/targets/"+tv.ID, "acme", map[string]any{
		"metrics": map[string]any{"load1": 3.0, "memFree": 1000, "at": 99},
	})
	if code != http.StatusOK {
		t.Fatalf("heartbeat patch want 200, got %d (%s)", code, b)
	}
	var hb targetView
	mustJSON(t, b, &hb)
	if hb.Metrics == nil || hb.Metrics.Load1 != 3.0 {
		t.Fatalf("heartbeat did not refresh metrics: %+v", hb.Metrics)
	}
	if hb.Metrics.At <= 0 || hb.Metrics.At == 99 {
		t.Fatalf("heartbeat clock must be server-stamped, got %d", hb.Metrics.At)
	}
	// Spec is untouched by a metrics-only heartbeat.
	if hb.Spec == nil || hb.Spec.Arch != "arm64" {
		t.Fatalf("metrics heartbeat must not drop spec: %+v", hb.Spec)
	}
}

// TestHTTPTargetUpsertByHost proves re-linking the SAME machine (org+host) refreshes
// ONE target (200, not a duplicate), while a different host makes a second target.
func TestHTTPTargetUpsertByHost(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	// First link on host "evo" -> created (201).
	code, b := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{
		"label": "evo", "host": "evo", "capacity": "old", "metrics": map[string]any{"load1": 1},
	})
	if code != http.StatusCreated {
		t.Fatalf("first link want 201, got %d (%s)", code, b)
	}
	var first targetView
	mustJSON(t, b, &first)

	// Re-link the SAME host -> updated in place (200), same id, refreshed fields.
	code, b = do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{
		"label": "evo", "host": "evo", "capacity": "new", "metrics": map[string]any{"load1": 5},
	})
	if code != http.StatusOK {
		t.Fatalf("re-link same host want 200 (upsert), got %d (%s)", code, b)
	}
	var second targetView
	mustJSON(t, b, &second)
	if second.ID != first.ID {
		t.Fatalf("re-link must reuse the machine's target id: %s != %s", second.ID, first.ID)
	}
	if second.Capacity != "new" || second.Metrics == nil || second.Metrics.Load1 != 5 {
		t.Fatalf("re-link must refresh capacity+metrics: %+v", second)
	}

	// A different host is a distinct machine -> a second target.
	_, _ = do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{"label": "dbc", "host": "dbc"})

	_, lb := do(t, app, http.MethodGet, "/v1/agents/targets", "acme", nil)
	var list targetsResp
	mustJSON(t, lb, &list)
	if len(list.Targets) != 2 {
		t.Fatalf("upsert must leave 2 machines (evo, dbc), got %d: %+v", len(list.Targets), list.Targets)
	}
}

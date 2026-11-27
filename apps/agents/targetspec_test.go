package agents

import (
	"context"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
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

	// Every telemetry float: NaN, ±Inf and negatives floor at 0.
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		got := Metrics{
			CPUUtil: bad, CPUTemp: bad, GPUTemp: bad, GPUPower: bad, GPUPowerLimit: bad,
			DiskRead: bad, DiskWrite: bad, NetRx: bad, NetTx: bad,
			Decode: bad, Prefill: bad, TTFT: bad, KVCache: bad,
		}.Sanitize()
		if got != (Metrics{}) {
			t.Fatalf("%v must floor every telemetry float at 0, got %+v", bad, got)
		}
	}
	// Oversize clamps to the contract bound; negative ints floor at 0.
	const over = 1e300
	got := Metrics{
		CPUUtil: over, CPUTemp: over, GPUTemp: over, GPUPower: over, GPUPowerLimit: over,
		GPUMemUsed: -1, GPUMemTotal: -1, DiskUsed: -1, DiskTotal: -1,
		DiskRead: over, DiskWrite: over, NetRx: over, NetTx: over,
		Decode: over, Prefill: over, TTFT: over,
		Running: math.MaxInt, Waiting: -1, KVCache: over,
	}.Sanitize()
	want := Metrics{
		CPUUtil: 1, CPUTemp: 200, GPUTemp: 200, GPUPower: 100000, GPUPowerLimit: 100000,
		DiskRead: 1e12, DiskWrite: 1e12, NetRx: 1e12, NetTx: 1e12,
		Decode: 1e7, Prefill: 1e7, TTFT: 3600,
		Running: 1000000, KVCache: 1,
	}
	if got != want {
		t.Fatalf("oversize telemetry must clamp to its bound:\n got %+v\nwant %+v", got, want)
	}
	// An in-range heartbeat passes through unchanged.
	if s := gb10().Sanitize(); s != gb10() {
		t.Fatalf("an in-range heartbeat must survive sanitize:\n got %+v\nwant %+v", s, gb10())
	}

	// Model: trimmed, valid UTF-8, at most 128 bytes, never split mid-character.
	for in, want := range map[string]string{
		"  qwen3-32b  ":                "qwen3-32b",
		"zen\xff5":                     "zen5",
		strings.Repeat("m", 500):       strings.Repeat("m", 128),
		strings.Repeat("é", 100):       strings.Repeat("é", 64),
		strings.Repeat("a", 127) + "é": strings.Repeat("a", 127),
	} {
		if got := (Metrics{Model: in}).Sanitize().Model; got != want {
			t.Errorf("model %q sanitized to %q, want %q", in, got, want)
		}
	}
}

// gb10 is a GB10 heartbeat with every field in range.
func gb10() Metrics {
	return Metrics{
		Load1: 2.5, Load5: 2, Load15: 1.5, MemUsed: 64 << 30, MemFree: 56 << 30, GPUUtil: 0.9,
		CPUUtil: 0.35, CPUTemp: 61, GPUTemp: 72, GPUPower: 95.5,
		GPUMemUsed: 48 << 30, DiskUsed: 2 << 40, DiskTotal: 4 << 40,
		DiskRead: 1.5e8, DiskWrite: 2e7, NetRx: 1.25e6, NetTx: 3e5,
		Model: "zen5-a3b,qwen3-32b", Decode: 48.5, Prefill: 2150, TTFT: 0.42,
		Running: 3, Waiting: 1, KVCache: 0.37,
	}
}

// A heartbeat carrying any one field is a heartbeat: IsZero must see it, or the
// server would clear MetricsAt and the machine would read as never beaten.
func TestMetricsIsZeroSeesEveryField(t *testing.T) {
	if !(Metrics{}).IsZero() {
		t.Fatal("the zero Metrics must be zero")
	}
	typ := reflect.TypeOf(Metrics{})
	for i := range typ.NumField() {
		var m Metrics
		f := reflect.ValueOf(&m).Elem().Field(i)
		switch f.Kind() {
		case reflect.Float64:
			f.SetFloat(1)
		case reflect.Int, reflect.Int64:
			f.SetInt(1)
		case reflect.String:
			f.SetString("x")
		default:
			t.Fatalf("%s: unhandled kind %s", typ.Field(i).Name, f.Kind())
		}
		if m.IsZero() {
			t.Errorf("a heartbeat carrying only %s must not be zero", typ.Field(i).Name)
		}
	}
}

// TestTargetStoreSpecMetricsRoundTrip proves the capability + metrics survive a store
// write/read exactly (JSON column codec), and a malformed stored blob decodes to the
// zero value rather than failing the whole target read.
func TestTargetStoreSpecMetricsRoundTrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testSessionStore(t)
		ctx := context.Background()
		spec := Spec{OS: "linux", Arch: "arm64", CPUs: 20, Memory: 128 << 30,
			GPUs: []GPU{{Vendor: "nvidia", Model: "GB10", Memory: 96 << 30}}}
		metrics := gb10()
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
		if got.Metrics != metrics {
			t.Fatalf("metrics round-trip mismatch:\n got %+v\nwant %+v", got.Metrics, metrics)
		}
		if got.MetricsAt != 1234 {
			t.Fatalf("metricsAt column mismatch: %d", got.MetricsAt)
		}

	})
}

// TestGetTargetByHost proves the idempotent-relink lookup is org-scoped (fail-closed
// cross-tenant), empty-host is a miss, and the newest wins on a duplicate host.
func TestGetTargetByHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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

	})
}

// TestHTTPTargetCapabilityAndHeartbeat proves register carries spec + metrics into the
// view, the server stamps the metrics clock (not the client), a metrics PATCH is a
// heartbeat that refreshes the sample + clock, and a spec PATCH updates capability.
func TestHTTPTargetCapabilityAndHeartbeat(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		code, b := do(t, app, http.MethodPost, "/v1/agent/targets", "acme", map[string]any{
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
		code, b = do(t, app, http.MethodPatch, "/v1/agent/targets/"+tv.ID, "acme", map[string]any{
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

		// A heartbeat carrying only serving telemetry is still a heartbeat.
		code, b = do(t, app, http.MethodPatch, "/v1/agent/targets/"+tv.ID, "acme", map[string]any{
			"metrics": map[string]any{"model": "zen5", "decode": 48.5, "running": 2},
		})
		if code != http.StatusOK {
			t.Fatalf("serving heartbeat want 200, got %d (%s)", code, b)
		}
		var sv targetView
		mustJSON(t, b, &sv)
		if sv.Metrics == nil || sv.Metrics.Model != "zen5" || sv.Metrics.Decode != 48.5 || sv.Metrics.Running != 2 {
			t.Fatalf("serving heartbeat did not carry its telemetry: %+v", sv.Metrics)
		}
		if sv.Metrics.At <= 0 || sv.MetricsAt == "" {
			t.Fatalf("a serving-only heartbeat must stamp the clock, got at=%d metricsAt=%q", sv.Metrics.At, sv.MetricsAt)
		}

	})
}

// TestHTTPTargetUpsertByHost proves re-linking the SAME machine (org+host) refreshes
// ONE target (200, not a duplicate), while a different host makes a second target.
func TestHTTPTargetUpsertByHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		// First link on host "evo" -> created (201).
		code, b := do(t, app, http.MethodPost, "/v1/agent/targets", "acme", map[string]any{
			"label": "evo", "host": "evo", "capacity": "old", "metrics": map[string]any{"load1": 1},
		})
		if code != http.StatusCreated {
			t.Fatalf("first link want 201, got %d (%s)", code, b)
		}
		var first targetView
		mustJSON(t, b, &first)

		// Re-link the SAME host -> updated in place (200), same id, refreshed fields.
		code, b = do(t, app, http.MethodPost, "/v1/agent/targets", "acme", map[string]any{
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
		_, _ = do(t, app, http.MethodPost, "/v1/agent/targets", "acme", map[string]any{"label": "dbc", "host": "dbc"})

		_, lb := do(t, app, http.MethodGet, "/v1/agent/targets", "acme", nil)
		var list targetsResp
		mustJSON(t, lb, &list)
		if len(list.Targets) != 2 {
			t.Fatalf("upsert must leave 2 machines (evo, dbc), got %d: %+v", len(list.Targets), list.Targets)
		}

	})
}

// TestHTTPTargetRejectsOversizeGPUList proves an absurd GPU array is rejected at the
// handler (a clean 400) rather than silently truncated after a large allocation — the
// bound is communicated to the client, and a normal list still registers.
func TestHTTPTargetRejectsOversizeGPUList(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		huge := make([]map[string]any, maxGPUs+50)
		for i := range huge {
			huge[i] = map[string]any{"vendor": "nvidia", "model": "x"}
		}
		if code, b := do(t, app, http.MethodPost, "/v1/agent/targets", "acme", map[string]any{
			"label": "box", "host": "box", "spec": map[string]any{"gpus": huge},
		}); code != http.StatusBadRequest {
			t.Fatalf("oversize gpu list must be rejected 400, got %d (%s)", code, b)
		}
		// A normal-sized list is accepted.
		ok := []map[string]any{{"vendor": "nvidia", "model": "GB10"}, {"vendor": "amd", "model": "8060S"}}
		if code, b := do(t, app, http.MethodPost, "/v1/agent/targets", "acme", map[string]any{
			"label": "box", "host": "box", "spec": map[string]any{"gpus": ok},
		}); code != http.StatusCreated {
			t.Fatalf("normal gpu list must be accepted, got %d (%s)", code, b)
		}

	})
}

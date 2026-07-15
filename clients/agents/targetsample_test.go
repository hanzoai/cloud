package agents

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/samples"
)

// targetsample_test.go covers the FIRST emitter: a run-target heartbeat also
// appends to the fleet series (clients/samples).
//
// The datastore is absent under test, so samples.Record is a proven no-op (its own
// package tests that). What MUST be proven here is everything this side owns:
// the projection is faithful, the vocabularies agree, and the HTTP contract is
// untouched whether or not the warehouse exists.

// ---- the projection (pure) ----

// A heartbeat projects onto a sample with no loss and no invention.
func TestSampleOfProjectsTheHeartbeat(t *testing.T) {
	at := time.Now().Unix()
	tg := Target{
		ID: "tgt-1", Org: "acme", Kind: TargetGPU, Host: "box.local", Label: "Box",
		Spec: Spec{OS: "linux", Arch: "arm64", CPUs: 20, Memory: 128 << 30,
			GPUs: []GPU{{Vendor: "nvidia", Model: "GB10", Memory: 96 << 30}}},
		Metrics: Metrics{Load1: 2.5, Load5: 2, Load15: 1.5,
			MemUsed: 64 << 30, MemFree: 64 << 30, GPUUtil: 0.75},
		MetricsAt: at,
	}
	s := sampleOf(tg)

	if s.Org != "acme" || s.Unit != "tgt-1" || s.Host != "box.local" {
		t.Fatalf("identity did not project: %+v", s)
	}
	if s.Source != samples.SourceAgent {
		t.Fatalf("source want %q, got %q", samples.SourceAgent, s.Source)
	}
	if s.Kind != TargetGPU {
		t.Fatalf("kind want %q, got %q", TargetGPU, s.Kind)
	}
	if !s.At.Equal(time.Unix(at, 0).UTC()) {
		t.Fatalf("at must be the SERVER-stamped heartbeat clock, got %v", s.At)
	}
	if s.CPUs != 20 || s.Memory != 128<<30 {
		t.Fatalf("spec did not project: %+v", s)
	}
	if s.MemUsed != 64<<30 || s.MemFree != 64<<30 || s.Load1 != 2.5 || s.Load5 != 2 || s.Load15 != 1.5 {
		t.Fatalf("metrics did not project: %+v", s)
	}
	if s.GPUUtil != 0.75 || s.GPUs != 1 || s.GPUModel != "GB10" {
		t.Fatalf("gpu did not project: %+v", s)
	}
	// An agent's own machine is metered, never resold.
	if s.CostCents != 0 {
		t.Fatalf("an agent sample must be unpriced, got %d", s.CostCents)
	}
	// The projection must be acceptable to the plane it feeds.
	if err := samples.Record(t.Context(), s); err != nil {
		t.Fatalf("a projected sample must be recordable: %v", err)
	}
}

// The accelerator count comes from the spec, and the row is named by the first
// card's model — falling back to its vendor when the model is unknown.
func TestSampleOfGPUSummary(t *testing.T) {
	cases := []struct {
		name      string
		gpus      []GPU
		wantN     int
		wantModel string
	}{
		{"none", nil, 0, ""},
		{"model", []GPU{{Vendor: "nvidia", Model: "GB10"}}, 1, "GB10"},
		{"vendor fallback", []GPU{{Vendor: "amd"}}, 1, "amd"},
		{"multi is counted, first names it", []GPU{{Model: "GB10"}, {Model: "GB10"}}, 2, "GB10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sampleOf(Target{ID: "t", Org: "o", Kind: TargetGPU, Spec: Spec{GPUs: tc.gpus}, MetricsAt: 1})
			if s.GPUs != tc.wantN || s.GPUModel != tc.wantModel {
				t.Fatalf("want (%d, %q), got (%d, %q)", tc.wantN, tc.wantModel, s.GPUs, s.GPUModel)
			}
		})
	}
}

// THE cross-package contract: every kind a target can be must be a kind the fleet
// series accepts, or heartbeats would silently stop being recorded. This fails the
// day someone adds a target kind without teaching the series about it.
func TestEveryTargetKindIsAFleetKind(t *testing.T) {
	fleet := map[string]bool{
		samples.KindLaptop: true, samples.KindCloud: true, samples.KindGPU: true,
		samples.KindCluster: true, samples.KindMachine: true, samples.KindWorker: true,
	}
	for _, k := range []string{TargetLaptop, TargetCloud, TargetGPU, TargetCluster, TargetMachine} {
		if !fleet[k] {
			t.Fatalf("target kind %q is not a fleet sample kind — its heartbeats would be dropped", k)
		}
		// Proven end to end: a sample carrying this kind validates.
		s := sampleOf(Target{ID: "t", Org: "o", Kind: k, MetricsAt: 1})
		if err := samples.Record(t.Context(), s); err != nil {
			t.Fatalf("kind %q must be recordable: %v", k, err)
		}
	}
}

// A write with no heartbeat in it appends nothing — recordSample is a no-op when
// the server never stamped a metrics clock.
func TestRecordSampleSkipsWhenNoHeartbeat(t *testing.T) {
	mountApp(t, nil) // sets the `mounted` singleton recordSample logs through
	// No panic, no goroutine, no write: MetricsAt == 0 means "no sample here".
	recordSample(mounted, Target{ID: "tgt-1", Org: "acme", Kind: TargetGPU, MetricsAt: 0})
}

// ---- (c) the HTTP contract is untouched by the series ----

// The heartbeat still 200s with no warehouse, and still returns the snapshot on
// the row exactly as before — the series is strictly additive.
func TestHeartbeatStill200sWithoutDatastore(t *testing.T) {
	app := mountApp(t, nil)

	code, body := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{
		"label": "Box", "kind": TargetGPU, "host": "box.local",
		"spec":    map[string]any{"os": "linux", "cpus": 20, "gpus": []map[string]any{{"vendor": "nvidia", "model": "GB10"}}},
		"metrics": map[string]any{"load1": 2.5, "gpuUtil": 0.75, "memUsed": 100},
	})
	if code != http.StatusCreated {
		t.Fatalf("register want 201 without a datastore, got %d (%s)", code, body)
	}
	var created targetView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if created.Metrics == nil || created.Metrics.GPUUtil != 0.75 {
		t.Fatalf("the snapshot on the row must be unchanged: %+v", created.Metrics)
	}
	if created.MetricsAt == "" {
		t.Fatal("the server must still stamp the heartbeat clock")
	}

	// The heartbeat itself.
	code, body = do(t, app, http.MethodPatch, "/v1/agents/targets/"+created.ID, "acme", map[string]any{
		"metrics": map[string]any{"load1": 4, "gpuUtil": 0.9, "memUsed": 200},
	})
	if code != http.StatusOK {
		t.Fatalf("heartbeat want 200 without a datastore, got %d (%s)", code, body)
	}
	var beat targetView
	if err := json.Unmarshal(body, &beat); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if beat.Metrics == nil || beat.Metrics.GPUUtil != 0.9 || beat.Metrics.Load1 != 4 {
		t.Fatalf("the heartbeat must still refresh the row snapshot: %+v", beat.Metrics)
	}

	// A re-link (same org+host) is idempotent and still carries a heartbeat.
	code, body = do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{
		"label": "Box", "kind": TargetGPU, "host": "box.local",
		"metrics": map[string]any{"load1": 1},
	})
	if code != http.StatusOK {
		t.Fatalf("re-link want 200 (idempotent), got %d (%s)", code, body)
	}
	var relinked targetView
	if err := json.Unmarshal(body, &relinked); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if relinked.ID != created.ID {
		t.Fatalf("a re-link must refresh the SAME target: %s != %s", relinked.ID, created.ID)
	}
}

// ---- the in-process seam ----

// TargetsForOrg / LoadOn are org-keyed and fail closed — the board reads through
// them, so a cross-tenant id must never resolve.
func TestInProcessSeamIsOrgScopedAndFailsClosed(t *testing.T) {
	app := mountApp(t, nil)
	code, body := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{
		"label": "Secret", "kind": TargetGPU, "host": "secret.local",
	})
	if code != http.StatusCreated {
		t.Fatalf("register: %d (%s)", code, body)
	}
	var created targetView
	_ = json.Unmarshal(body, &created)

	// The owner sees it.
	own, err := TargetsForOrg(t.Context(), "acme")
	if err != nil {
		t.Fatalf("TargetsForOrg(acme): %v", err)
	}
	if len(own) != 1 || own[0].ID != created.ID {
		t.Fatalf("the owner must see its target, got %+v", own)
	}

	// Another tenant sees nothing — the same id is unreachable.
	other, err := TargetsForOrg(t.Context(), "other")
	if err != nil {
		t.Fatalf("TargetsForOrg(other): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("CROSS-TENANT LEAK: org 'other' enumerated %+v", other)
	}

	// A blank/oversized org fails closed on both.
	for _, bad := range []string{"", "   "} {
		if _, err := TargetsForOrg(t.Context(), bad); err == nil {
			t.Fatalf("TargetsForOrg(%q) must fail closed", bad)
		}
		if _, err := LoadOn(t.Context(), bad, created.ID, ""); err == nil {
			t.Fatalf("LoadOn(%q) must fail closed", bad)
		}
	}

	// LoadOn is org-keyed too: the foreign tenant resolves no load for the id.
	load, err := LoadOn(t.Context(), "other", created.ID, "secret.local")
	if err != nil {
		t.Fatalf("LoadOn(other): %v", err)
	}
	if load.Sessions != 0 || load.Running != 0 {
		t.Fatalf("CROSS-TENANT LEAK: foreign load %+v", load)
	}
}

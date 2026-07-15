package visor

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/samples"
)

// board_test.go proves the /v1/fleet contract: it is tenant-gated, it unions the
// sources it can reach, it NEVER fails because one of them is broken, and it does
// not disturb /v1/fleet/workers.
//
// The agents subsystem is not mounted in these tests, so agentUnits is exercised
// on its fail-soft path (an unmounted source contributes nothing) — which is
// exactly source-failure case (e). The agent projection itself is unit-tested at
// its home, in clients/agents (sampleOf / TargetsForOrg).

// ---- (e) the union renders with a source erroring ----

// A validated tenant is required; nothing below it is reachable without one.
func TestFleetBoardRequiresTenant(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	if code, _ := do(t, app, http.MethodGet, "/v1/fleet", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org board want 403, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/fleet/samples", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org samples want 403, got %d", code)
	}
}

// The board renders the sources it CAN reach even though others are unreachable:
// agents is unmounted and the warehouse is absent, yet Visor's machines still
// list. A broken source costs its own rows and nothing else.
func TestFleetBoardRendersWithSourcesErroring(t *testing.T) {
	f := &fakeVisor{machinesByOwner: map[string][]map[string]any{
		"acme": {{
			"owner": "acme", "name": "gpu-1", "id": "drop-9", "displayName": "GPU One",
			"size": "s-8vcpu-16gb", "region": "sfo3", "state": "running", "cpuSize": "8",
		}},
	}}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/fleet", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("board want 200 despite unreachable sources, got %d (%s)", code, body)
	}
	var got struct {
		Units []fleetUnit `json:"units"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(got.Units) != 1 {
		t.Fatalf("want the 1 reachable visor machine, got %d: %+v", len(got.Units), got.Units)
	}
	u := got.Units[0]
	if u.Source != samples.SourceVisor || u.Kind != samples.KindMachine {
		t.Fatalf("a visor machine must be tagged (visor, machine), got (%s, %s)", u.Source, u.Kind)
	}
	if u.Unit != "gpu-1" || u.Label != "GPU One" {
		t.Fatalf("identity did not map: %+v", u)
	}
	if u.Spec == nil || u.Spec.CPUs != 8 {
		t.Fatalf("spec must reuse the visor normalizer (cpuSize=8): %+v", u.Spec)
	}
	// No warehouse and no native snapshot => honestly no metrics, never invented.
	if u.Metrics != nil {
		t.Fatalf("metrics must be omitted, not fabricated: %+v", u.Metrics)
	}
}

// Visor itself being down must not 500 the board — it renders empty.
func TestFleetBoardSurvivesVisorDown(t *testing.T) {
	app := mountApp(t, &fakeVisor{}) // no machines for anyone
	t.Setenv("VISOR_URL", "http://127.0.0.1:1")
	code, body := do(t, app, http.MethodGet, "/v1/fleet", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("board want 200 with visor down, got %d (%s)", code, body)
	}
	var got struct {
		Units []fleetUnit `json:"units"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if got.Units == nil {
		t.Fatal("units must be an empty array, never null")
	}
}

// The board is tenant-scoped: org B never sees org A's units.
func TestFleetBoardIsTenantScoped(t *testing.T) {
	f := &fakeVisor{machinesByOwner: map[string][]map[string]any{
		"acme": {{"owner": "acme", "name": "secret-1", "id": "d1", "state": "running", "cpuSize": "2"}},
	}}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/fleet", "other", nil)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got struct {
		Units []fleetUnit `json:"units"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(got.Units) != 0 {
		t.Fatalf("CROSS-TENANT LEAK: org 'other' saw %+v", got.Units)
	}
	if f.lastOwner != "other" {
		t.Fatalf("the board must read as the VALIDATED org, got owner=%q", f.lastOwner)
	}
}

// ---- /v1/fleet/samples ----

// With no warehouse the series is an honest empty list, not a 500 and not zeros.
func TestFleetSamplesHonestEmptyWithoutDatastore(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	code, body := do(t, app, http.MethodGet, "/v1/fleet/samples?range=24h", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got struct {
		Samples []sampleView `json:"samples"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if got.Samples == nil {
		t.Fatal("samples must be an empty array, never null")
	}
	if len(got.Samples) != 0 {
		t.Fatalf("want honest empty, got %+v", got.Samples)
	}
}

// A narrower outside the allowlist is a 400 (the caller's error), never a 500 and
// never a silently-widened read. The message tells them the vocabulary and nothing
// about our internals.
func TestFleetSamplesRejectsUnknownSource(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	code, body := do(t, app, http.MethodGet, "/v1/fleet/samples?source=evil", "acme", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown source want 400, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), samples.SourceAgent) {
		t.Fatalf("the 400 must name the allowed vocabulary: %s", body)
	}
	// A caller-facing error must never name the warehouse it came from.
	if strings.Contains(string(body), "compute_samples") || strings.Contains(string(body), "hanzo.") {
		t.Fatalf("the 400 leaked our internals: %s", body)
	}
	code, _ = do(t, app, http.MethodGet, "/v1/fleet/samples?source=agent", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("an allowlisted source want 200, got %d", code)
	}
}

// An over-long unit is the caller's error too — and is rejected rather than
// truncated onto some other unit's series.
func TestFleetSamplesRejectsOversizedUnit(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	code, _ := do(t, app, http.MethodGet, "/v1/fleet/samples?unit="+strings.Repeat("u", 400), "acme", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("an over-long unit want 400, got %d", code)
	}
}

// A hostile ?range can only ever fall back to the default window — it never
// reaches a statement, so the read still succeeds.
func TestFleetSamplesToleratesHostileRange(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	code, _ := do(t, app, http.MethodGet,
		"/v1/fleet/samples?range=1h%3B+DROP+TABLE+hanzo.compute_samples", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("a hostile range must fall back to the default, got %d", code)
	}
}

// ---- (f) /v1/fleet/workers is unchanged ----

// The new routes must not shadow or alter the existing BYO inventory face: it
// answers on its own path with its own shape.
func TestFleetWorkersUnchanged(t *testing.T) {
	app := mountApp(t, &fakeVisor{})
	if code, _ := do(t, app, http.MethodGet, "/v1/fleet/workers", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org workers want 403 (unchanged), got %d", code)
	}
	code, body := do(t, app, http.MethodGet, "/v1/fleet/workers", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("workers want 200 (unchanged), got %d (%s)", code, body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if _, ok := got["workers"]; !ok {
		t.Fatalf("workers must still answer {workers:[...]}, got %s", body)
	}
	if _, ok := got["units"]; ok {
		t.Fatalf("workers must NOT have become the board: %s", body)
	}
}

// ---- projections ----

// metricsOf carries the sample's live half onto the board, with `at` rendered as
// RFC3339 (the same clock the target views publish).
func TestMetricsOfProjection(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	m := metricsOf(samples.Sample{Load1: 1.5, MemUsed: 8 << 30, MemFree: 24 << 30, GPUUtil: 0.5, At: at})
	if m.Load1 != 1.5 || m.MemUsed != 8<<30 || m.MemFree != 24<<30 || m.GPUUtil != 0.5 {
		t.Fatalf("metrics did not project: %+v", m)
	}
	if m.At != at.Format(time.RFC3339) {
		t.Fatalf("at want RFC3339 %s, got %s", at.Format(time.RFC3339), m.At)
	}
	if got := metricsOf(samples.Sample{}); got.At != "" {
		t.Fatalf("a zero clock must render empty, not epoch: %q", got.At)
	}
}

// The board's sample view keeps every dimension of the row it renders.
func TestSampleViewProjection(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	v := toSampleView(samples.Sample{
		Source: samples.SourceAgent, Unit: "tgt-1", Kind: samples.KindGPU, Host: "box",
		At: at, CPUs: 8, Memory: 32 << 30, MemUsed: 8 << 30, MemFree: 24 << 30,
		Load1: 1.5, Load5: 1, Load15: 0.5, GPUUtil: 0.25, GPUs: 2, GPUModel: "GB10",
		CostCents: 7,
	})
	if v.Source != samples.SourceAgent || v.Unit != "tgt-1" || v.Kind != samples.KindGPU {
		t.Fatalf("identity did not project: %+v", v)
	}
	if v.At != at.Format(time.RFC3339) || v.GPUs != 2 || v.GPUModel != "GB10" || v.CostCents != 7 {
		t.Fatalf("row did not project: %+v", v)
	}
}

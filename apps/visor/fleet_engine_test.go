package visor

// fleet_engine_test.go — the engine.serve contract between the CLI and this reader.
// The CLI stores a `registration` JSON as the presence activity's Input; this file
// decodes it as `fleetRegistration` and emits it as `byoWorker` on GET
// /v1/fleet/workers. These structs are mirrors — the test guards the round-trip so
// the engine endpoint survives decode (server reads CLI Input) and encode (server
// emits to the console/CLI).

import (
	"encoding/json"
	"strings"
	"testing"
)

// cliRegistrationJSON is exactly what `hanzo link --serve-engine` writes as
// the fleet presence activity's Input.
const cliRegistrationJSON = `{
  "hostname": "gb10",
  "os": "linux",
  "version": "1.50.0",
  "jobQueue": "gpu-jobs",
  "gpus": [{"name": "NVIDIA GB10", "memoryTotal": "122880 MiB"}],
  "capabilities": ["studio.render", "engine.serve"],
  "engine": {
    "url": "http://node.example:1234",
    "apis": ["openai", "anthropic"],
    "models": ["default", "zen-omni-30b"],
    "status": "ready"
  }
}`

func TestFleetRegistrationDecodesEngine(t *testing.T) {
	var reg fleetRegistration
	if err := json.Unmarshal([]byte(cliRegistrationJSON), &reg); err != nil {
		t.Fatalf("decode CLI registration Input: %v", err)
	}
	if len(reg.Capabilities) != 2 || reg.Capabilities[1] != "engine.serve" {
		t.Fatalf("capabilities = %v, want [studio.render engine.serve]", reg.Capabilities)
	}
	if reg.Engine == nil {
		t.Fatal("engine advertisement dropped on decode")
	}
	if reg.Engine.URL != "http://node.example:1234" || reg.Engine.Status != "ready" {
		t.Fatalf("engine = %+v", reg.Engine)
	}
	if len(reg.Engine.Models) != 2 || len(reg.Engine.APIs) != 2 {
		t.Fatalf("engine models/apis = %v / %v", reg.Engine.Models, reg.Engine.APIs)
	}
}

func TestByoWorkerEmitsEngine(t *testing.T) {
	var reg fleetRegistration
	if err := json.Unmarshal([]byte(cliRegistrationJSON), &reg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Mirror the mapping byoWorkers performs for the engine fields.
	w := byoWorker{
		ID:           "gb10",
		Hostname:     reg.Hostname,
		Provider:     "byo",
		Status:       "online",
		GPUs:         reg.GPUs,
		Capabilities: reg.Capabilities,
		Engine:       reg.Engine,
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal byoWorker: %v", err)
	}
	// GET /v1/fleet/workers must expose the endpoint so the gateway/CLI can route.
	for _, want := range []string{`"engine.serve"`, `"http://node.example:1234"`, `"anthropic"`, `"status":"ready"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("byoWorker JSON missing %s:\n%s", want, raw)
		}
	}
}

// A CPU-only / non-serving worker carries no engine field — omitempty keeps the wire
// clean and the console renders it exactly as before.
func TestByoWorkerNoEngineOmitted(t *testing.T) {
	raw, err := json.Marshal(byoWorker{ID: "cpu1", Hostname: "cpu1", Provider: "byo", Status: "online"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "engine") {
		t.Fatalf("expected no engine key for a non-serving worker:\n%s", raw)
	}
}

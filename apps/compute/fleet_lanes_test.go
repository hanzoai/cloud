package compute

// fleet_lanes_test.go — the Studio lanes a node advertises survive the round-trip
// from the CLI's presence Input (fleetRegistration) to GET /v1/compute/fleet/workers
// (byoWorker), which is where the studio engine reads them to decide which GPU cards
// a page may offer.

import (
	"encoding/json"
	"slices"
	"testing"
)

const laneRegistrationJSON = `{
  "hostname": "dgx",
  "os": "linux",
  "version": "1.50.0",
  "jobQueue": "gpu-jobs",
  "gpus": [{"name": "NVIDIA GB10"}],
  "capabilities": ["studio.render"],
  "lanes": ["image.zen", "video.wan", "mesh"],
  "models": [
    {"id": "z-image-turbo", "lane": "image.zen", "licence": "Apache-2.0"},
    {"id": "triposr", "lane": "mesh", "licence": "MIT"}
  ]
}`

func TestFleetLanesRoundTrip(t *testing.T) {
	var reg fleetRegistration
	if err := json.Unmarshal([]byte(laneRegistrationJSON), &reg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(reg.Lanes, []string{"image.zen", "video.wan", "mesh"}) {
		t.Fatalf("lanes = %v", reg.Lanes)
	}
	w := byoWorker{ID: "dgx", Hostname: reg.Hostname, Status: "online", GPUs: reg.GPUs, Lanes: reg.Lanes, Models: reg.Models}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Lanes  []string    `json:"lanes"`
		Models []laneModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Lanes, reg.Lanes) || len(back.Models) != 2 || back.Models[1] != (laneModel{ID: "triposr", Lane: "mesh", Licence: "MIT"}) {
		t.Fatalf("emitted %s", raw)
	}
}

// A node that advertises no lanes emits none: the field is absent, not an empty claim.
func TestFleetNoLanesOmitted(t *testing.T) {
	raw, err := json.Marshal(byoWorker{ID: "evo", Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["lanes"]; ok {
		t.Fatalf("lanes present on a node that advertised none: %s", raw)
	}
}

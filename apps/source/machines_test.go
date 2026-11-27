// Copyright © 2026 Hanzo AI. MIT License.

package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func intPtr(v int) *int { return &v }

func TestUnifiedMachinesEndpointIncludesLinkedAndCloudCompute(t *testing.T) {
	r := mount(t)

	// Stub compute plane to return a managed GPU VM, a cloud droplet, and a BYO box that shares a name with a linked machine.
	r.svc.State.compute = func(_ context.Context, org, _ string) ([]client.ComputeMachine, error) {
		if org != "acme" {
			return nil, nil
		}
		return []client.ComputeMachine{
			{
				ID:       "gpu-h100-1",
				Name:     "H100 Training Node",
				Provider: "digitalocean",
				Region:   "sfo3",
				Type:     "gpu-h100x8-640gb",
				Status:   "running",
				PublicIP: "203.0.113.10",
				OS:       "linux",
				GPU:      "8× H100",
				Mem:      "640 GB",
				Vcpu:     intPtr(96),
			},
			{
				ID:       "web-droplet-1",
				Name:     "API Box",
				Provider: "digitalocean",
				Region:   "nyc3",
				Type:     "s-4vcpu-8gb",
				Status:   "active",
				PublicIP: "203.0.113.20",
				OS:       "linux",
				Mem:      "8 GB",
				Vcpu:     intPtr(4),
			},
			{
				ID:       "dgx-byo",
				Name:     "dgx",
				Provider: "byo",
				Region:   "on-prem",
				Type:     "byo-gpu",
				Status:   "online",
				OS:       "linux",
				GPU:      "NVIDIA GB10",
			},
			{
				ID:       "lab-box",
				Name:     "lab-box",
				Provider: "byo",
				Region:   "on-prem",
				Type:     "byo-gpu",
				Status:   "offline",
				OS:       "linux",
				GPU:      "RTX 5090",
			},
		}, nil
	}

	// Link a personal machine via POST /v1/machines (canonical path).
	dev := newDevice(t)
	code, body := r.as(http.MethodPost, "/v1/machines", "alice", "acme", dev.link("dgx"))
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/machines: status=%d body=%s", code, body)
	}
	var linked SourceLinked
	if err := json.Unmarshal([]byte(body), &linked); err != nil {
		t.Fatalf("decode link: %v", err)
	}

	// GET /v1/machines returns linked personal machine + cloud compute + non-duplicate BYO machine (dgx BYO deduped against linked dgx).
	code, body = r.as(http.MethodGet, "/v1/machines", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/machines: status=%d body=%s", code, body)
	}
	var list MachineList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode MachineList: %v", err)
	}
	if len(list.Sources) != 1 || list.Sources[0].ID != linked.ID {
		t.Fatalf("expected 1 linked source, got %+v", list.Sources)
	}
	if len(list.Machines) != 4 {
		t.Fatalf("expected 4 unified machines (dgx linked + H100 + web droplet + lab-box BYO), got %d: %+v", len(list.Machines), list.Machines)
	}

	byID := map[string]Machine{}
	for _, m := range list.Machines {
		byID[m.ID] = m
	}
	if byID[linked.ID].Origin != "linked" || byID[linked.ID].Kind != "desktop" {
		t.Fatalf("unexpected linked machine: %+v", byID[linked.ID])
	}
	if byID["gpu-h100-1"].Origin != "cloud" || byID["gpu-h100-1"].Kind != "gpu" || byID["gpu-h100-1"].State != "online" {
		t.Fatalf("unexpected cloud GPU machine: %+v", byID["gpu-h100-1"])
	}
	if byID["web-droplet-1"].Origin != "cloud" || byID["web-droplet-1"].Kind != "cloud_compute" || byID["web-droplet-1"].State != "online" {
		t.Fatalf("unexpected cloud VM machine: %+v", byID["web-droplet-1"])
	}
	if _, dup := byID["dgx-byo"]; dup {
		t.Fatalf("expected dgx BYO duplicate to be deduped against linked dgx machine")
	}
	if byID["lab-box"].Origin != "byo" || byID["lab-box"].State != "offline" {
		t.Fatalf("unexpected BYO machine: %+v", byID["lab-box"])
	}

	// Filter ?kind=cloud returns only cloud compute machines.
	code, body = r.as(http.MethodGet, "/v1/machines?kind=cloud", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/machines?kind=cloud: status=%d body=%s", code, body)
	}
	var cloudOnly MachineList
	_ = json.Unmarshal([]byte(body), &cloudOnly)
	if len(cloudOnly.Machines) != 2 {
		t.Fatalf("expected 2 cloud machines, got %+v", cloudOnly.Machines)
	}

	// Filter ?kind=gpu returns GPU machines (cloud H100 + BYO RTX 5090).
	code, body = r.as(http.MethodGet, "/v1/machines?kind=gpu", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/machines?kind=gpu: status=%d body=%s", code, body)
	}
	var gpuOnly MachineList
	_ = json.Unmarshal([]byte(body), &gpuOnly)
	if len(gpuOnly.Machines) != 2 {
		t.Fatalf("expected 2 GPU machines, got %+v", gpuOnly.Machines)
	}

	// GET /v1/machines/:id resolves BOTH linked machines and cloud compute machines.
	code, body = r.as(http.MethodGet, "/v1/machines/gpu-h100-1", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/machines/gpu-h100-1: status=%d body=%s", code, body)
	}
	var gotCloud Machine
	_ = json.Unmarshal([]byte(body), &gotCloud)
	if gotCloud.ID != "gpu-h100-1" || gotCloud.GPU != "8× H100" {
		t.Fatalf("unexpected cloud machine get: %+v", gotCloud)
	}

	// Backward-compatible /v1/sources alias still returns linked sources cleanly.
	code, body = r.as(http.MethodGet, "/v1/sources", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/sources: status=%d body=%s", code, body)
	}
	var legacy SourceList
	_ = json.Unmarshal([]byte(body), &legacy)
	if len(legacy.Sources) != 1 || legacy.Sources[0].ID != linked.ID {
		t.Fatalf("unexpected legacy /v1/sources response: %+v", legacy)
	}
}

func TestUnifiedMachinesDegradesGracefullyWhenComputePlaneFails(t *testing.T) {
	r := mount(t)
	r.svc.State.compute = func(_ context.Context, _, _ string) ([]client.ComputeMachine, error) {
		return nil, errors.New("compute plane temporarily unreachable")
	}
	dev := newDevice(t)
	code, _ := r.as(http.MethodPost, "/v1/machines", "alice", "acme", dev.link("ra"))
	if code != http.StatusCreated {
		t.Fatalf("link failed: %d", code)
	}
	code, body := r.as(http.MethodGet, "/v1/machines", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 even when compute plane fails, got %d: %s", code, body)
	}
	var list MachineList
	_ = json.Unmarshal([]byte(body), &list)
	if len(list.Machines) != 1 || list.Machines[0].Name != "ra" {
		t.Fatalf("expected linked machine ra to still appear, got %+v", list.Machines)
	}
}

// Copyright © 2026 Hanzo AI. MIT License.

package compute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

func TestCollectComputeMachinesPlaneRPC(t *testing.T) {
	fv := &fakeVisor{
		machinesByOwner: map[string][]map[string]any{
			"acme": {
				{
					"owner":    "acme",
					"name":     "gpu-1",
					"id":       "gpu-1",
					"provider": "digitalocean",
					"region":   "sfo3",
					"type":     "gpu-h100x1-80gb",
					"state":    "active",
					"publicIp": "203.0.113.55",
					"os":       "linux",
				},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		switch r.URL.Path {
		case "/v1/machines":
			envelope200(w, fv.machinesByOwner[owner])
		case "/v1/k8s/nodes":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"nodes":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := &cloud.Service[state]{
		Base: cloud.Base{Log: luxlog.New("test")},
		State: state{
			cl: &computeClient{target: srv.URL, cc: srv.Client()},
		},
	}

	out := collectComputeMachines(context.Background(), s, "acme", "alice")
	if len(out.Machines) != 1 {
		t.Fatalf("expected 1 machine, got %+v", out.Machines)
	}
	m := out.Machines[0]
	if m.ID != "gpu-1" || m.Provider != "digitalocean" || m.Region != "sfo3" || m.GPU != "H100" {
		t.Fatalf("unexpected compute machine projection: %+v", m)
	}
}

package visor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeVisor is a stand-in for the real Visor Beego service. It speaks the same
// casibase {status,msg,data} envelope and, crucially, scopes every read by the
// ?owner query — so a test can prove cloud forwards the VALIDATED principal's org
// (not a client-forgeable field) as the tenant key. It records the last owner it
// saw for assertions.
type fakeVisor struct {
	lastOwner string
	// machinesByOwner is the per-tenant machine inventory the fake serves.
	machinesByOwner map[string][]map[string]any
	poolsByOwner    map[string][]map[string]any
}

func envelope200(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "msg": "", "data": data})
}

func (f *fakeVisor) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/get-machines", func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		f.lastOwner = owner
		envelope200(w, f.machinesByOwner[owner])
	})
	mux.HandleFunc("/v1/get-machine", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id") // owner/name
		for _, ms := range f.machinesByOwner {
			for _, m := range ms {
				if id == m["owner"].(string)+"/"+m["name"].(string) {
					envelope200(w, m)
					return
				}
			}
		}
		envelope200(w, map[string]any{}) // not found → empty machine
	})
	mux.HandleFunc("/v1/machines/launch", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		quote := map[string]any{"size": body["size"], "region": body["region"], "priceHourly": 1.57, "currency": "usd"}
		if dry, _ := body["dryRun"].(bool); dry {
			envelope200(w, quote)
			return
		}
		machine := map[string]any{
			"owner": f.lastOwner, "name": body["name"], "id": "drop-123",
			"size": body["size"], "region": body["region"], "state": "provisioning",
		}
		envelope200(w, map[string]any{"machine": machine, "quote": quote})
	})
	mux.HandleFunc("/v1/delete-machine", func(w http.ResponseWriter, r *http.Request) {
		envelope200(w, true)
	})
	mux.HandleFunc("/v1/get-node-pools", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		envelope200(w, f.poolsByOwner[f.lastOwner])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountApp mounts the visor surface against the fake upstream.
func mountApp(t *testing.T, f *fakeVisor) *zip.App {
	t.Helper()
	srv := f.server(t)
	t.Setenv("VISOR_URL", srv.URL)
	t.Setenv("VISOR_CLIENT_ID", "")     // force bearer-forward path (fake ignores auth)
	t.Setenv("VISOR_CLIENT_SECRET", "") //
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		// A validated principal: principal.Tenant requires a non-empty X-User-Id,
		// which the gateway sets ONLY from a verified credential. The test app has
		// no sanitizer, so inject it directly (empty org → no user → the 403 path).
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestMachinesListTenantScopedAndShape(t *testing.T) {
	f := &fakeVisor{machinesByOwner: map[string][]map[string]any{
		"acme": {{
			"owner": "acme", "name": "web-1", "id": "drop-1", "displayName": "Web 1",
			"size": "s-2vcpu-4gb", "region": "sfo3", "state": "running", "publicIp": "1.2.3.4",
			"cpuSize": "2",
		}},
		// "other" has no machines.
	}}
	app := mountApp(t, f)

	// No validated principal → 403, and the request never reaches Visor.
	if code, _ := do(t, app, http.MethodGet, "/v1/machines", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// acme sees its machine, mapped to the console shape.
	code, body := do(t, app, http.MethodGet, "/v1/machines", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if f.lastOwner != "acme" {
		t.Fatalf("cloud must forward owner=acme (the validated org), got %q", f.lastOwner)
	}
	var listed struct {
		Machines []machineView `json:"machines"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(listed.Machines) != 1 {
		t.Fatalf("acme want 1 machine, got %d", len(listed.Machines))
	}
	m := listed.Machines[0]
	if m.ID != "web-1" || m.Name != "Web 1" || m.Region != "sfo3" || m.Type != "s-2vcpu-4gb" ||
		m.Status != "running" || m.PublicIp != "1.2.3.4" || m.Vcpu == nil || *m.Vcpu != 2 {
		t.Fatalf("machine view mismatch: %+v", m)
	}

	// Cross-tenant isolation: "other" gets an honest empty list, and Visor was
	// scoped to owner=other (never acme's data).
	code, body = do(t, app, http.MethodGet, "/v1/machines", "other", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Machines) != 0 {
		t.Fatalf("other must see zero machines, got %d %+v", code, listed.Machines)
	}
	if f.lastOwner != "other" {
		t.Fatalf("cloud must forward owner=other, got %q", f.lastOwner)
	}
}

func TestGPUsDerivedFromMachines(t *testing.T) {
	f := &fakeVisor{machinesByOwner: map[string][]map[string]any{
		"acme": {
			{"owner": "acme", "name": "gpu-node", "size": "gpu-h100x8-640gb", "region": "sfo3", "state": "running"},
			{"owner": "acme", "name": "cpu-node", "size": "s-4vcpu-8gb", "region": "sfo3", "state": "running"},
		},
	}}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/gpus", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("gpus want 200, got %d (%s)", code, body)
	}
	var out struct {
		Gpus []gpuView `json:"gpus"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v", err)
	}
	// gpu-h100x8 → 8 real H100 accelerators; the CPU node contributes none.
	if len(out.Gpus) != 8 {
		t.Fatalf("want 8 derived GPUs, got %d", len(out.Gpus))
	}
	for _, g := range out.Gpus {
		if g.Model != "H100" || g.Machine != "gpu-node" || g.Status != "running" {
			t.Fatalf("gpu row mismatch: %+v", g)
		}
	}

	// Alerts is an honest empty (Visor has no alert inventory), still tenant-gated.
	if code, _ := do(t, app, http.MethodGet, "/v1/gpus/alerts", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org alerts want 403, got %d", code)
	}
	code, body = do(t, app, http.MethodGet, "/v1/gpus/alerts", "acme", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"alerts":[]`)) {
		t.Fatalf("alerts want 200 [], got %d %s", code, body)
	}
}

func TestClustersFromNodePools(t *testing.T) {
	f := &fakeVisor{poolsByOwner: map[string][]map[string]any{
		"acme": {
			{"owner": "acme", "name": "pool-a", "clusterId": "k8s-1", "poolId": "p1", "size": "s-4vcpu-8gb", "count": 3, "state": "Active"},
			{"owner": "acme", "name": "pool-b", "clusterId": "k8s-1", "poolId": "p2", "size": "gpu-l40sx1-48gb", "count": 2, "state": "Active"},
		},
	}}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/clusters", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("clusters want 200, got %d (%s)", code, body)
	}
	var out struct {
		Clusters []clusterView `json:"clusters"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(out.Clusters) != 1 {
		t.Fatalf("two pools of one clusterId → 1 cluster, got %d", len(out.Clusters))
	}
	cl := out.Clusters[0]
	if cl.DoksClusterID != "k8s-1" || cl.NodeCount != 5 || len(cl.NodePools) != 2 {
		t.Fatalf("cluster projection mismatch: %+v", cl)
	}
}

func TestLaunchQuoteAndRealAndDelete(t *testing.T) {
	f := &fakeVisor{machinesByOwner: map[string][]map[string]any{}}
	app := mountApp(t, f)

	// dryRun returns the quote verbatim (no machine created).
	code, body := do(t, app, http.MethodPost, "/v1/machines", "acme",
		map[string]any{"size": "gpu-l40sx1-48gb", "region": "sfo3", "name": "q", "dryRun": true})
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"priceHourly"`)) {
		t.Fatalf("dryRun want 200 quote, got %d %s", code, body)
	}

	// A real launch returns the launched machine as a clean view.
	code, body = do(t, app, http.MethodPost, "/v1/machines", "acme",
		map[string]any{"size": "gpu-l40sx1-48gb", "region": "sfo3", "name": "gpu-1"})
	if code != http.StatusCreated {
		t.Fatalf("launch want 201, got %d %s", code, body)
	}
	var mv machineView
	_ = json.Unmarshal(body, &mv)
	if mv.Name != "gpu-1" || mv.Status != "provisioning" || mv.GPU != "L40S" {
		t.Fatalf("launched machine view mismatch: %+v", mv)
	}

	// size is required.
	if code, _ := do(t, app, http.MethodPost, "/v1/machines", "acme", map[string]any{"region": "sfo3"}); code != http.StatusBadRequest {
		t.Fatalf("launch without size want 400, got %d", code)
	}

	// delete is tenant-gated and returns 204.
	if code, _ := do(t, app, http.MethodDelete, "/v1/machines/gpu-1", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org delete want 403, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/machines/gpu-1", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
}

func TestGPUSpecOf(t *testing.T) {
	cases := []struct {
		slug    string
		model   string
		perNode int
		ok      bool
	}{
		{"gpu-h100x8-640gb", "H100", 8, true},
		{"gpu-l40sx1-48gb", "L40S", 1, true},
		{"gpu-rtx4000x1-20gb", "RTX 4000", 1, true},
		{"s-4vcpu-8gb", "", 0, false},
		{"", "", 0, false},
	}
	for _, tc := range cases {
		spec, ok := gpuSpecOf(tc.slug)
		if ok != tc.ok || spec.model != tc.model || spec.perNode != tc.perNode {
			t.Fatalf("gpuSpecOf(%q) = %+v,%v want %s,%d,%v", tc.slug, spec, ok, tc.model, tc.perNode, tc.ok)
		}
	}
}

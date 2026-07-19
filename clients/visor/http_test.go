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
	// machinesByOwner is the per-tenant REGISTRY inventory (/v1/get-machines).
	machinesByOwner map[string][]map[string]any
	// liveByOwner is the per-tenant LIVE DigitalOcean reseller list
	// (/v1/machines → ListComputeMachines) — the live droplet inventory that
	// listMachines now unions with the registry.
	liveByOwner map[string][]map[string]any
	// nodesByOwner is the per-tenant DOKS worker NODES list (/v1/k8s/nodes →
	// ListComputeKubernetesNodes) — the THIRD machine source managedMachines unions.
	nodesByOwner map[string][]map[string]any
	poolsByOwner map[string][]map[string]any
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
	// Live DO reseller list (ListComputeMachines). Exact-match path — it does NOT
	// shadow /v1/machines/launch below (that is a distinct exact pattern).
	mux.HandleFunc("/v1/machines", func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		f.lastOwner = owner
		envelope200(w, f.liveByOwner[owner])
	})
	// DOKS worker nodes (ListComputeKubernetesNodes) — the third machine source.
	mux.HandleFunc("/v1/k8s/nodes", func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		f.lastOwner = owner
		envelope200(w, f.nodesByOwner[owner])
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
		// A validated principal: principal.Org requires a non-empty X-User-Id,
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

// TestMachinesMergeLiveDOAndRegistry proves listMachines unions Visor's registry
// (/v1/get-machines) with the LIVE DigitalOcean reseller list (/v1/machines): a
// droplet present ONLY in the live list surfaces (the bug this fixes), a machine
// in BOTH is deduped (by id AND by name — even when the registry row has no
// provider id yet), and the REGISTRY entry wins the collision so its enrichment
// (displayName) is preserved.
func TestMachinesMergeLiveDOAndRegistry(t *testing.T) {
	f := &fakeVisor{
		// Registry (DB-backed, enriched/masked): web-1 has a provider id; api-1 is a
		// fresh record with no provider id yet (id assigned only after sync).
		machinesByOwner: map[string][]map[string]any{
			"acme": {
				{"owner": "acme", "name": "web-1", "id": "drop-1", "displayName": "Web 1",
					"provider": "DigitalOcean", "size": "s-2vcpu-4gb", "region": "sfo3", "state": "running"},
				{"owner": "acme", "name": "api-1", "displayName": "API 1",
					"provider": "DigitalOcean", "size": "s-1vcpu-1gb", "region": "sfo3", "state": "running"},
			},
		},
		// Live DO reseller list: web-1 again (same droplet id, raw — no displayName),
		// api-1 again (same name, now WITH a droplet id), and do-only which was
		// provisioned but never landed in the registry.
		liveByOwner: map[string][]map[string]any{
			"acme": {
				{"owner": "acme", "name": "web-1", "id": "drop-1",
					"provider": "DigitalOcean", "size": "s-2vcpu-4gb", "region": "sfo3", "state": "running"},
				{"owner": "acme", "name": "api-1", "id": "drop-3",
					"provider": "DigitalOcean", "size": "s-1vcpu-1gb", "region": "sfo3", "state": "running"},
				{"owner": "acme", "name": "do-only", "id": "drop-2",
					"provider": "DigitalOcean", "size": "s-4vcpu-8gb", "region": "nyc3", "state": "running"},
			},
		},
	}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/machines", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Machines []machineView `json:"machines"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}

	byID := map[string]machineView{}
	for _, m := range listed.Machines {
		byID[m.ID] = m
	}
	// Expected deduped union: 3 machines, not 5. web-1 (deduped by id), api-1
	// (deduped by name — the registry row had no id), and the live-only do-only.
	// The two "won" rows keep the REGISTRY displayName; do-only keeps its live name.
	want := []struct{ id, name, region, typ string }{
		{"web-1", "Web 1", "sfo3", "s-2vcpu-4gb"},
		{"api-1", "API 1", "sfo3", "s-1vcpu-1gb"},
		{"do-only", "do-only", "nyc3", "s-4vcpu-8gb"},
	}
	if len(listed.Machines) != len(want) {
		t.Fatalf("registry+live union want %d deduped machines, got %d: %+v", len(want), len(listed.Machines), listed.Machines)
	}
	for _, w := range want {
		m, ok := byID[w.id]
		if !ok {
			t.Fatalf("machine %q missing from merged list: %+v", w.id, listed.Machines)
		}
		if m.Name != w.name || m.Region != w.region || m.Type != w.typ || m.Provider != "DigitalOcean" || m.Status != "running" {
			t.Fatalf("machine %q view mismatch: got %+v want name=%q region=%q type=%q", w.id, m, w.name, w.region, w.typ)
		}
	}
}

// TestMachinesMergeDOKSNodes proves the THIRD source: listMachines unions DOKS
// worker NODES (/v1/k8s/nodes) with the live droplet list so a cluster's
// nodes appear on the fleet — while a DOKS node whose droplet is ALSO in the live
// list is deduped by droplet id (never listed twice). This is the visor backport's
// payoff: cluster NODES show, not just standalone droplets.
func TestMachinesMergeDOKSNodes(t *testing.T) {
	f := &fakeVisor{
		// Live DO reseller list: one standalone droplet, plus a DOKS worker droplet
		// (drop-node-1) that DO also surfaced under the org tag — so its droplet id
		// appears in BOTH the live list and the k8s-nodes list.
		liveByOwner: map[string][]map[string]any{
			"acme": {
				{"owner": "acme", "name": "standalone-1", "id": "drop-1",
					"provider": "DigitalOcean", "size": "s-2vcpu-4gb", "region": "sfo3", "state": "running"},
				{"owner": "acme", "name": "prod-default-aaa", "id": "drop-node-1",
					"provider": "DigitalOcean", "size": "s-4vcpu-8gb", "region": "sfo3", "state": "running"},
			},
		},
		// DOKS nodes: drop-node-1 again but under a DIFFERENT name ("node-alias") so
		// only DROPLET-ID dedup can collapse it (name dedup could not) — this pins
		// that a node whose droplet is already listed dedupes BY ID. drop-node-2
		// lives ONLY in the cluster (never a standalone droplet) and must surface.
		nodesByOwner: map[string][]map[string]any{
			"acme": {
				{"owner": "acme", "name": "node-alias", "id": "drop-node-1",
					"provider": "DigitalOcean", "size": "s-4vcpu-8gb", "region": "sfo3", "state": "running", "tag": "doks-cluster:prod"},
				{"owner": "acme", "name": "prod-default-bbb", "id": "drop-node-2",
					"provider": "DigitalOcean", "size": "s-4vcpu-8gb", "region": "sfo3", "state": "running", "tag": "doks-cluster:prod"},
			},
		},
	}
	app := mountApp(t, f)

	code, body := do(t, app, http.MethodGet, "/v1/machines", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Machines []machineView `json:"machines"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}

	// machineView.ID is the machine NAME (see TestMachinesMergeLiveDOAndRegistry).
	byName := map[string]machineView{}
	for _, m := range listed.Machines {
		byName[m.ID] = m
	}
	// Deduped union: 3 machines, not 4. standalone-1, the shared droplet drop-node-1
	// (deduped BY ID — the "node-alias" row collapses into the live "prod-default-aaa"
	// which wins as the earlier source), and the DOKS-only node prod-default-bbb.
	if len(listed.Machines) != 3 {
		t.Fatalf("live+DOKS union want 3 deduped machines, got %d: %+v", len(listed.Machines), listed.Machines)
	}
	if _, ok := byName["standalone-1"]; !ok {
		t.Errorf("standalone droplet missing from merged list: %+v", listed.Machines)
	}
	if _, ok := byName["node-alias"]; ok {
		t.Errorf("node-alias must NOT appear — its droplet drop-node-1 is already live; dedup must be BY ID: %+v", listed.Machines)
	}
	shared, ok := byName["prod-default-aaa"]
	if !ok {
		t.Errorf("shared droplet prod-default-aaa missing (live row must win the id collision): %+v", listed.Machines)
	} else if shared.Provider != "DigitalOcean" || shared.Region != "sfo3" {
		t.Errorf("shared node view mismatch: got %+v", shared)
	}
	nodeOnly, ok := byName["prod-default-bbb"]
	if !ok {
		t.Fatalf("DOKS-only node prod-default-bbb missing — the cluster node must surface on the fleet: %+v", listed.Machines)
	}
	if nodeOnly.Provider != "DigitalOcean" || nodeOnly.Status != "running" || nodeOnly.Type != "s-4vcpu-8gb" {
		t.Errorf("DOKS-only node view mismatch: got %+v want provider=DigitalOcean status=running type=s-4vcpu-8gb", nodeOnly)
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

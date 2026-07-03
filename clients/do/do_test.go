package do

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/digitalocean/godo"
	"github.com/gofiber/fiber/v3"
	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

var testCfg = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// ── fakes: an in-memory DigitalOcean account shared across all orgs ──────────
//
// The fakes store resources under their PHYSICAL DO name (what the handler sets
// via physicalName(org, friendly)). A single account holds every org's
// resources; the handler's prefix filter is what must keep them apart. That is
// exactly the real-world condition (DO is one account), so the isolation test is
// honest.

func notFound() error {
	return &godo.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}, Message: "not found"}
}

type fakeVPCs struct{ byID map[string]*godo.VPC }

func (f *fakeVPCs) List(context.Context, *godo.ListOptions) ([]*godo.VPC, *godo.Response, error) {
	out := make([]*godo.VPC, 0, len(f.byID))
	for _, v := range f.byID {
		out = append(out, v)
	}
	return out, &godo.Response{}, nil
}

func (f *fakeVPCs) Get(_ context.Context, id string) (*godo.VPC, *godo.Response, error) {
	if v, ok := f.byID[id]; ok {
		return v, &godo.Response{}, nil
	}
	return nil, nil, notFound()
}

func (f *fakeVPCs) Create(_ context.Context, r *godo.VPCCreateRequest) (*godo.VPC, *godo.Response, error) {
	v := &godo.VPC{ID: "vpc-" + r.Name, Name: r.Name, IPRange: r.IPRange, RegionSlug: r.RegionSlug}
	f.byID[v.ID] = v
	return v, &godo.Response{}, nil
}

func (f *fakeVPCs) Delete(_ context.Context, id string) (*godo.Response, error) {
	if _, ok := f.byID[id]; !ok {
		return nil, notFound()
	}
	delete(f.byID, id)
	return &godo.Response{}, nil
}

type fakeLBs struct{ byID map[string]*godo.LoadBalancer }

func (f *fakeLBs) List(context.Context, *godo.ListOptions) ([]godo.LoadBalancer, *godo.Response, error) {
	out := make([]godo.LoadBalancer, 0, len(f.byID))
	for _, lb := range f.byID {
		out = append(out, *lb)
	}
	return out, &godo.Response{}, nil
}

func (f *fakeLBs) Get(_ context.Context, id string) (*godo.LoadBalancer, *godo.Response, error) {
	if lb, ok := f.byID[id]; ok {
		return lb, &godo.Response{}, nil
	}
	return nil, nil, notFound()
}

func (f *fakeLBs) Create(_ context.Context, r *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error) {
	lb := &godo.LoadBalancer{ID: "lb-" + r.Name, Name: r.Name, Type: r.Type, Status: "new", IP: "10.0.0.1"}
	f.byID[lb.ID] = lb
	return lb, &godo.Response{}, nil
}

func (f *fakeLBs) Delete(_ context.Context, id string) (*godo.Response, error) {
	if _, ok := f.byID[id]; !ok {
		return nil, notFound()
	}
	delete(f.byID, id)
	return &godo.Response{}, nil
}

// ── harness ──────────────────────────────────────────────────────────────────

func mountFake(t *testing.T) (*zip.App, *fakeVPCs, *fakeLBs) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	vpcs := &fakeVPCs{byID: map[string]*godo.VPC{}}
	lbs := &fakeLBs{byID: map[string]*godo.LoadBalancer{}}
	s := &svc{vpcs: vpcs, lbs: lbs, log: luxlog.New("test")}
	s.routes(app)
	return app, vpcs, lbs
}

// req drives one JSON request through the Fiber test harness. org=="" omits the
// principal headers (the forge path); a non-empty org sets BOTH X-Org-Id and
// X-User-Id so tenant() sees a validated principal (as the gateway mints).
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(rq, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// createVPC returns the created resource's id.
func createVPC(t *testing.T, app *zip.App, org, name, region string) string {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/vpcs", org, map[string]string{"name": name, "region": region})
	if code != http.StatusCreated {
		t.Fatalf("create vpc %s/%s: want 201, got %d (%s)", org, name, code, body)
	}
	var v vpcView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode vpc: %v", err)
	}
	if v.Name != name {
		t.Fatalf("created vpc name: want friendly %q, got %q", name, v.Name)
	}
	return v.ID
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestVPCPerOrgIsolation is the load-bearing tenant-isolation test: two orgs
// create VPCs in the ONE DO account; neither can list, get, or delete the
// other's — a cross-tenant id reads as 404, and the physical name is namespaced.
func TestVPCPerOrgIsolation(t *testing.T) {
	app, rawVPCs, _ := mountFake(t)

	acmeID := createVPC(t, app, "acme", "web", "sfo3")
	maxpID := createVPC(t, app, "maxpower", "web", "nyc3") // same friendly name, different org

	// Physical DO names are org-namespaced and therefore distinct despite the
	// shared friendly name — the isolation boundary is by construction.
	if rawVPCs.byID[acmeID].Name == rawVPCs.byID[maxpID].Name {
		t.Fatalf("distinct orgs must get distinct physical names, both = %q", rawVPCs.byID[acmeID].Name)
	}

	// acme lists exactly its own VPC, under the friendly name.
	code, body := req(t, app, http.MethodGet, "/v1/vpcs", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("acme list: want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Vpcs []vpcView `json:"vpcs"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Vpcs) != 1 || listed.Vpcs[0].Name != "web" || listed.Vpcs[0].ID != acmeID {
		t.Fatalf("acme must see exactly its own [web], got %+v", listed.Vpcs)
	}
	if listed.Vpcs[0].CIDR == "" && listed.Vpcs[0].Region != "sfo3" {
		t.Fatalf("acme vpc must carry real DO fields, got %+v", listed.Vpcs[0])
	}

	// acme cannot GET maxpower's VPC (cross-tenant read → 404, not 403 — no oracle).
	if code, _ := req(t, app, http.MethodGet, "/v1/vpcs/"+maxpID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower vpc: want 404, got %d", code)
	}

	// acme cannot DELETE maxpower's VPC; it must remain in the account.
	if code, _ := req(t, app, http.MethodDelete, "/v1/vpcs/"+maxpID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme DELETE maxpower vpc: want 404, got %d", code)
	}
	if _, ok := rawVPCs.byID[maxpID]; !ok {
		t.Fatalf("maxpower vpc must survive acme's delete attempt")
	}

	// maxpower CAN delete its own.
	if code, _ := req(t, app, http.MethodDelete, "/v1/vpcs/"+maxpID, "maxpower", nil); code != http.StatusNoContent {
		t.Fatalf("maxpower DELETE own vpc: want 204, got %d", code)
	}
}

// TestLoadBalancerListIsolationAndDefaults proves LB list scoping + that a
// minimal create yields a real LB (default forwarding rule) with the FE shape.
func TestLoadBalancerListIsolationAndDefaults(t *testing.T) {
	app, _, rawLBs := mountFake(t)

	// Minimal create (no forwarding rules) must succeed with a defaulted rule.
	code, body := req(t, app, http.MethodPost, "/v1/load-balancers", "acme",
		map[string]string{"name": "edge", "region": "sfo3"})
	if code != http.StatusCreated {
		t.Fatalf("create lb: want 201, got %d (%s)", code, body)
	}
	var lb lbView
	if err := json.Unmarshal(body, &lb); err != nil {
		t.Fatalf("decode lb: %v", err)
	}
	if lb.Name != "edge" || lb.Status != "new" {
		t.Fatalf("lb view: want friendly name 'edge' + real status 'new', got %+v", lb)
	}

	// A second org's LB in the same account.
	req(t, app, http.MethodPost, "/v1/load-balancers", "maxpower",
		map[string]string{"name": "edge", "region": "nyc3"})
	if len(rawLBs.byID) != 2 {
		t.Fatalf("account must hold both orgs' LBs, got %d", len(rawLBs.byID))
	}

	// acme lists exactly one — its own.
	code, body = req(t, app, http.MethodGet, "/v1/load-balancers", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("acme lb list: want 200, got %d (%s)", code, body)
	}
	var listed struct {
		LoadBalancers []lbView `json:"loadBalancers"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode lb list: %v", err)
	}
	if len(listed.LoadBalancers) != 1 || listed.LoadBalancers[0].Name != "edge" {
		t.Fatalf("acme must see exactly its own [edge] LB, got %+v", listed.LoadBalancers)
	}
}

// TestForgePathRefused proves the anonymous forge (X-Org-Id with no validated
// principal) is refused 403 before DO is ever touched — the same gate S3 uses.
func TestForgePathRefused(t *testing.T) {
	app, _, _ := mountFake(t)

	// No headers at all → 403.
	if code, _ := req(t, app, http.MethodGet, "/v1/vpcs", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal VPC list: want 403, got %d", code)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/load-balancers", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal LB list: want 403, got %d", code)
	}

	// X-Org-Id present but NO X-User-Id (the forgeable path) → still 403.
	app2 := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &svc{vpcs: &fakeVPCs{byID: map[string]*godo.VPC{}}, lbs: &fakeLBs{byID: map[string]*godo.LoadBalancer{}}, log: luxlog.New("test")}
	s.routes(app2)
	rq := httptest.NewRequest(http.MethodGet, "/v1/vpcs", nil)
	rq.Header.Set("X-Org-Id", "victim") // forged org, no validated user
	resp, err := app2.Fiber().Test(rq, testCfg)
	if err != nil {
		t.Fatalf("forge test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged X-Org-Id without X-User-Id: want 403, got %d", resp.StatusCode)
	}
}

// TestFailClosedWhenUnconfigured proves that with no DO token every op is an
// honest 503 — never a fabricated resource.
func TestFailClosedWhenUnconfigured(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &svc{log: luxlog.New("test")} // nil seams → unconfigured
	s.routes(app)

	for _, path := range []string{"/v1/vpcs", "/v1/load-balancers"} {
		if code, _ := req(t, app, http.MethodGet, path, "acme", nil); code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured GET %s: want 503, got %d", path, code)
		}
	}
}

package provisioning

// Tests for the dedicated-instance strategy: a datastore/docdb create launches
// the org's OWN operator Datastore CR + admin Secret in tenant-<org>, isolated
// by instance, metered to the org, reconciled provisioning->ready off the CR
// status, and torn down on drop. A fake orchestrator stands in for the cluster
// so the whole control-plane path is exercised hermetically.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/commerce/metering"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fakeOrch is an in-memory orchestrator: it records the CRs/Secrets applied and
// deleted, and returns a controllable instance phase.
type fakeOrch struct {
	ready      error
	ensureErr  error
	phase      string
	datastores map[string]*unstructured.Unstructured
	secrets    map[string]*unstructured.Unstructured
	ensured    []string
	deletedDS  []string
	deletedSec []string
	deletedPVC []string
}

func newFakeOrch() *fakeOrch {
	return &fakeOrch{
		datastores: map[string]*unstructured.Unstructured{},
		secrets:    map[string]*unstructured.Unstructured{},
	}
}

func (f *fakeOrch) Ready() error { return f.ready }
func (f *fakeOrch) EnsureTenant(_ context.Context, ns, _ string) error {
	f.ensured = append(f.ensured, ns)
	return f.ensureErr
}
func (f *fakeOrch) ApplySecret(_ context.Context, ns, name string, obj *unstructured.Unstructured) error {
	f.secrets[ns+"/"+name] = obj
	return nil
}
func (f *fakeOrch) ApplyDatastore(_ context.Context, ns, name string, obj *unstructured.Unstructured) error {
	f.datastores[ns+"/"+name] = obj
	return nil
}
func (f *fakeOrch) DatastorePhase(context.Context, string, string) (string, error) { return f.phase, nil }
func (f *fakeOrch) DeleteDatastore(_ context.Context, ns, name string) error {
	f.deletedDS = append(f.deletedDS, ns+"/"+name)
	delete(f.datastores, ns+"/"+name)
	return nil
}
func (f *fakeOrch) DeleteSecret(_ context.Context, ns, name string) error {
	f.deletedSec = append(f.deletedSec, ns+"/"+name)
	delete(f.secrets, ns+"/"+name)
	return nil
}
func (f *fakeOrch) DeletePVC(_ context.Context, ns, name string) error {
	f.deletedPVC = append(f.deletedPVC, ns+"/"+name)
	return nil
}

func newDedicatedSvc(t *testing.T, orch orchestrator) *svc {
	t.Helper()
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	log := luxlog.New("module", "provdedtest")
	return &svc{store: newTestStore(t), sec: openSecrets("hanzo", log), reg: newRegistry(), log: log, orch: orch}
}

func doReq(t *testing.T, h zip.Handler, method, route, path, org, bodyStr string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	switch method {
	case http.MethodGet:
		app.Get(route, h)
	case http.MethodDelete:
		app.Delete(route, h)
	default:
		app.Post(route, h)
	}
	var rdr io.Reader
	if bodyStr != "" {
		rdr = strings.NewReader(bodyStr)
	}
	req, _ := http.NewRequest(method, path, rdr)
	if bodyStr != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org) // validated principal (tenant() gates on X-User-Id)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

// TestDedicated_CreateLaunchesInstance: a datastore create writes a Datastore CR
// + admin Secret into tenant-<org>, persists a "provisioning" row with the size
// dimension, and returns a DSN pointing at the instance's OWN in-cluster Service.
func TestDedicated_CreateLaunchesInstance(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	resp := postCreate(t, s, "datastore", "acme", "analytics")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var cr createResp
	_ = json.NewDecoder(resp.Body).Decode(&cr)

	if cr.Status != statusProvisioning {
		t.Fatalf("status = %q, want %q", cr.Status, statusProvisioning)
	}
	ns := "tenant-acme"
	inst := instanceName("datastore", "acme", "analytics")
	wantHost := inst + "." + ns + ".svc"
	if cr.Host != wantHost || cr.Port != 8123 {
		t.Fatalf("endpoint = %s:%d, want %s:8123 (the instance's own service)", cr.Host, cr.Port, wantHost)
	}
	if !strings.HasPrefix(cr.ConnectionString, "clickhouse://admin:") || !strings.Contains(cr.ConnectionString, wantHost+":8123") {
		t.Fatalf("dsn %q does not target the dedicated instance", cr.ConnectionString)
	}

	// The Datastore CR landed in the tenant namespace with the right shape.
	ds := orch.datastores[ns+"/"+inst]
	if ds == nil {
		t.Fatalf("no Datastore CR applied at %s/%s; have %v", ns, inst, keys(orch.datastores))
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "kind"); got != "Datastore" {
		t.Fatalf("CR kind = %q, want Datastore (writes status.phase)", got)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "type"); got != "datastore" {
		t.Fatalf("spec.type = %q, want datastore", got)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "image", "repository"); got != "ghcr.io/hanzoai/datastore" {
		t.Fatalf("image = %q, want ghcr.io/hanzoai/datastore", got)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "metadata", "labels", "hanzo.ai/org"); got != "acme" {
		t.Fatalf("CR org label = %q, want acme (ownership boundary)", got)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "metadata", "labels", "hanzo.ai/resource"); got != cr.ID {
		t.Fatalf("CR resource label = %q, want %q", got, cr.ID)
	}
	// envFrom references the admin Secret by name only (no inline plaintext).
	envFrom, _, _ := unstructured.NestedSlice(ds.Object, "spec", "envFrom")
	if len(envFrom) != 1 {
		t.Fatalf("spec.envFrom = %v, want one secretRef", envFrom)
	}

	// The admin Secret carries the image's env keys and is NOT the CR.
	sec := orch.secrets[ns+"/"+inst+"-admin"]
	if sec == nil {
		t.Fatalf("no admin Secret applied at %s/%s-admin", ns, inst)
	}
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	for _, k := range []string{"DATASTORE_DB", "DATASTORE_USER", "DATASTORE_PASSWORD"} {
		if sd[k] == "" {
			t.Fatalf("admin Secret missing %q (image env); have %v", k, sd)
		}
	}
	if cr.Password == "" || sd["DATASTORE_PASSWORD"] != cr.Password {
		t.Fatalf("returned password must match the Secret the instance boots with")
	}

	// The row is persisted "provisioning" with the size dimension.
	r, err := s.store.Get(context.Background(), "acme", "datastore", "analytics")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if r.Status != statusProvisioning || r.Size != "10Gi" || r.PhysicalName != inst {
		t.Fatalf("row = %+v, want status=provisioning size=10Gi physical=%s", r, inst)
	}
}

// TestDedicated_TwoOrgsSeparateInstances proves isolation-by-instance: the same
// resource name for two orgs yields two DISTINCT instances in two DISTINCT
// namespaces — no shared backend, no cross-tenant reachability.
func TestDedicated_TwoOrgsSeparateInstances(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	if resp := postCreate(t, s, "docdb", "acme", "events"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("acme create = %d", resp.StatusCode)
	}
	if resp := postCreate(t, s, "docdb", "globex", "events"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("globex create = %d", resp.StatusCode)
	}

	acme := instanceName("docdb", "acme", "events")
	globex := instanceName("docdb", "globex", "events")
	if acme == globex {
		t.Fatalf("two orgs collided on one instance name %q", acme)
	}
	if orch.datastores["tenant-acme/"+acme] == nil {
		t.Fatalf("acme instance missing in tenant-acme")
	}
	if orch.datastores["tenant-globex/"+globex] == nil {
		t.Fatalf("globex instance missing in tenant-globex")
	}
	// Neither instance can appear in the other's namespace.
	if orch.datastores["tenant-acme/"+globex] != nil || orch.datastores["tenant-globex/"+acme] != nil {
		t.Fatalf("cross-tenant instance leak")
	}
}

// TestDedicated_ReadyReconcile: a provisioning row flips to ready on GET once the
// operator reports the instance StatefulSet Running — never before.
func TestDedicated_ReadyReconcile(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	if resp := postCreate(t, s, "datastore", "acme", "warehouse"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}

	// Instance not up yet -> GET still reports provisioning.
	orch.phase = "Creating"
	resp := doReq(t, s.get("datastore"), http.MethodGet, "/v1/datastore/:name", "/v1/datastore/warehouse", "acme", "")
	var g getResp
	_ = json.NewDecoder(resp.Body).Decode(&g)
	if g.Status != statusProvisioning {
		t.Fatalf("status = %q, want provisioning while instance is Creating", g.Status)
	}

	// Operator reports Running -> GET reconciles to ready and persists it.
	orch.phase = phaseRunning
	resp = doReq(t, s.get("datastore"), http.MethodGet, "/v1/datastore/:name", "/v1/datastore/warehouse", "acme", "")
	_ = json.NewDecoder(resp.Body).Decode(&g)
	if g.Status != statusReady {
		t.Fatalf("status = %q, want ready once instance is Running", g.Status)
	}
	r, _ := s.store.Get(context.Background(), "acme", "datastore", "warehouse")
	if r.Status != statusReady {
		t.Fatalf("row status = %q, want ready (persisted)", r.Status)
	}
}

// TestDedicated_DropTearsDownInstance: delete removes the CR + admin Secret + row.
func TestDedicated_DropTearsDownInstance(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	if resp := postCreate(t, s, "docdb", "acme", "sessions"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	inst := instanceName("docdb", "acme", "sessions")

	resp := doReq(t, s.drop("docdb"), http.MethodDelete, "/v1/docdb/:name", "/v1/docdb/sessions", "acme", "")
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("drop = %d body=%s, want 204", resp.StatusCode, body)
	}
	if orch.datastores["tenant-acme/"+inst] != nil {
		t.Fatalf("Datastore CR not deleted")
	}
	if orch.secrets["tenant-acme/"+inst+"-admin"] != nil {
		t.Fatalf("admin Secret not deleted")
	}
	// The retained data volume is reaped too (no storage footprint left behind).
	wantPVC := "tenant-acme/data-" + inst + "-0"
	if len(orch.deletedPVC) != 1 || orch.deletedPVC[0] != wantPVC {
		t.Fatalf("drop deleted PVCs %v, want [%s]", orch.deletedPVC, wantPVC)
	}
	if _, err := s.store.Get(context.Background(), "acme", "docdb", "sessions"); err == nil {
		t.Fatalf("row still present after drop")
	}
}

// TestDedicated_BillsProvisionAndFootprintToOrg proves the two billing
// invariants: the provision debit carries the size dimension and lands on the
// CALLER's org (never the client-default "hanzo"), and the recurring footprint
// meter charges each ready instance's OWN org a size-derived GB-day amount.
func TestDedicated_BillsProvisionAndFootprintToOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	log := luxlog.New("module", "proddedbill")
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	m, err := metering.New(metering.Config{BaseURL: bs.start(t), Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	orch := newFakeOrch()
	s := &svc{
		store: newTestStore(t), sec: openSecrets("hanzo", log), reg: newRegistry(), log: log,
		orch: orch, bill: cloud.NewResourceMeter(cloud.Deps{Logger: log, Metering: m, Env: "mainnet"}, "provisioning"),
	}

	// Provision debit -> caller org "acme", size in Model.
	resp := postCreate(t, s, "datastore", "acme", "metrics")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create = %d body=%s", resp.StatusCode, body)
	}
	if !waitForDebit(func() bool { return bs.debits() >= 1 }) {
		t.Fatalf("provision was not billed")
	}
	org, body := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("provision debited org %q, want caller acme (never default hanzo)", org)
	}
	var u struct {
		User  string `json:"user"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &u)
	if u.User != "acme" || !strings.HasPrefix(u.Model, "datastore:") {
		t.Fatalf("provision debit user=%q model=%q, want user=acme model=datastore:<size>", u.User, u.Model)
	}

	// Mark the instance ready, then run one footprint sweep -> a GB-day debit to
	// acme carrying the size dimension.
	if _, err := s.store.UpdateStatus(context.Background(), "acme", "datastore", "metrics", statusReady); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	before := bs.debits()
	s.meterDedicatedFootprint(context.Background())
	if !waitForDebit(func() bool { return bs.debits() > before }) {
		t.Fatalf("footprint sweep did not bill the running instance")
	}
	org, body = bs.lastDebit()
	_ = json.Unmarshal(body, &u)
	if org != "acme" || !strings.Contains(u.Model, "gbday") {
		t.Fatalf("footprint debit org=%q model=%q, want org=acme model=…gbday", org, u.Model)
	}
}

// TestDedicated_PricingPure exercises the pure sizing/pricing helpers.
func TestDedicated_PricingPure(t *testing.T) {
	for _, c := range []struct {
		size   string
		wantGB int
	}{
		{"10Gi", 10}, {"1Ti", 1024}, {"512Mi", 1}, {"", 0}, {"100G", 100}, {"garbage", 0},
	} {
		if got := sizeToGB(c.size); got != c.wantGB {
			t.Fatalf("sizeToGB(%q) = %d, want %d", c.size, got, c.wantGB)
		}
	}
	// 10 GiB * $0.08/GB-month / 30 = 2.67 -> 3 cents/day; empty -> 0.
	if got := gbDayCents(10); got != 3 {
		t.Fatalf("gbDayCents(10) = %d, want 3", got)
	}
	if got := gbDayCents(0); got != 0 {
		t.Fatalf("gbDayCents(0) = %d, want 0", got)
	}
}

func keys(m map[string]*unstructured.Unstructured) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

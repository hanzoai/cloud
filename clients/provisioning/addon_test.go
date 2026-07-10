package provisioning

// Tests for the instance-binding / <KIND>_URL injection add-on mechanism and the
// two engines it un-dedicated (sql, kv). A dedicated provision assembles the
// right operator Datastore CR + admin credential and — when instance-bound —
// projects the DSN as <KIND>_URL into the app instance's addons Secret; drop
// reverts it to Base BEFORE tearing the backend down. The fakeOrch stands in for
// the cluster so the whole control-plane path is hermetic.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// postCreateInstance is postCreate with an instance binding in the body.
func postCreateInstance(t *testing.T, s *cloud.Service[state], kind, org, name, instance string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/"+kind, create(s, kind))
	body := `{"name":"` + name + `","instance":"` + instance + `"}`
	req, _ := http.NewRequest("POST", "/v1/"+kind, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u-"+org) // validated principal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

// crHasEnv reports whether a Datastore CR carries a spec.env {name,value} pair.
func crHasEnv(ds *unstructured.Unstructured, name, value string) bool {
	env, _, _ := unstructured.NestedSlice(ds.Object, "spec", "env")
	for _, e := range env {
		if m, ok := e.(map[string]any); ok && m["name"] == name && m["value"] == value {
			return true
		}
	}
	return false
}

// TestStore_InstanceColumnRoundTrips proves the additive instance column
// persists and that ListByInstance scopes to (org, instance).
func TestStore_InstanceColumnRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	bound := sampleResource("cache")
	bound.Kind, bound.Instance = "kv", "commerce"
	bound.PhysicalName = "ds-abc-cache" // distinct physical to avoid the earlier sample's
	if err := s.Insert(ctx, bound); err != nil {
		t.Fatalf("Insert bound: %v", err)
	}
	unbound := sampleResource("legacy")
	unbound.PhysicalName = "ds-abc-legacy" // Instance stays "" (the default)
	if err := s.Insert(ctx, unbound); err != nil {
		t.Fatalf("Insert unbound: %v", err)
	}

	got, err := s.Get(ctx, "acme", "kv", "cache")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Instance != "commerce" {
		t.Fatalf("instance = %q, want commerce (column must round-trip)", got.Instance)
	}
	// The legacy row defaults to empty instance — additive column never disturbs
	// an existing (pre-binding) provision.
	leg, _ := s.Get(ctx, "acme", "sql", "legacy")
	if leg.Instance != "" {
		t.Fatalf("unbound instance = %q, want empty", leg.Instance)
	}

	byInst, err := s.ListByInstance(ctx, "acme", "commerce")
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(byInst) != 1 || byInst[0].Name != "cache" {
		t.Fatalf("ListByInstance(acme,commerce) = %+v, want exactly the cache row", byInst)
	}
	// Cross-tenant/instance isolation: another org sees nothing for that instance.
	if other, _ := s.ListByInstance(ctx, "globex", "commerce"); len(other) != 0 {
		t.Fatalf("cross-tenant leak: globex saw %d rows for instance commerce", len(other))
	}
}

// TestDedicated_SQLEngineAssemblesDSN proves the sql add-on materializes a
// postgresql Datastore CR with the POSTGRES_* admin env + the PGDATA subdirectory
// and returns a postgres:// DSN authenticated as the "admin" superuser.
func TestDedicated_SQLEngineAssemblesDSN(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	resp := postCreate(t, s, "sql", "acme", "orders")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var cr createResp
	_ = json.NewDecoder(resp.Body).Decode(&cr)

	inst := instanceName("sql", "acme", "orders")
	host := inst + ".tenant-acme.svc"
	if cr.Port != 5432 || cr.Host != host {
		t.Fatalf("endpoint = %s:%d, want %s:5432", cr.Host, cr.Port, host)
	}
	if !strings.HasPrefix(cr.ConnectionString, "postgres://admin:") ||
		!strings.Contains(cr.ConnectionString, host+":5432/") ||
		!strings.HasSuffix(cr.ConnectionString, "?sslmode=disable") {
		t.Fatalf("dsn %q is not a postgres admin DSN at the instance", cr.ConnectionString)
	}

	ds := orch.datastores["tenant-acme/"+inst]
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "type"); got != "postgresql" {
		t.Fatalf("spec.type = %q, want postgresql", got)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "image", "repository"); got != "ghcr.io/hanzoai/sql" {
		t.Fatalf("image = %q, want ghcr.io/hanzoai/sql", got)
	}
	// PGDATA must be the subdirectory (fresh PVC root's lost+found breaks initdb).
	if !crHasEnv(ds, "PGDATA", "/var/lib/postgresql/data/pgdata") {
		t.Fatalf("spec.env missing PGDATA=/var/lib/postgresql/data/pgdata")
	}
	// Admin credential via envFrom (image reads POSTGRES_*), never inlined.
	if envFrom, _, _ := unstructured.NestedSlice(ds.Object, "spec", "envFrom"); len(envFrom) != 1 {
		t.Fatalf("spec.envFrom = %v, want one secretRef", envFrom)
	}
	sec := orch.secrets["tenant-acme/"+inst+"-admin"]
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	if sd["POSTGRES_USER"] != "admin" || sd["POSTGRES_PASSWORD"] != cr.Password || sd["POSTGRES_DB"] == "" {
		t.Fatalf("admin Secret POSTGRES_* wrong: %v", sd)
	}
}

// TestDedicated_KVEngineAssemblesDSN proves the kv add-on materializes a valkey
// Datastore CR that loads a per-instance requirepass from a MOUNTED config
// Secret (the kv-server binary reads no password from env) and returns a
// redis://default:… DSN — never an "admin" user that would fail AUTH.
func TestDedicated_KVEngineAssemblesDSN(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	resp := postCreate(t, s, "kv", "acme", "sessions")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var cr createResp
	_ = json.NewDecoder(resp.Body).Decode(&cr)

	inst := instanceName("kv", "acme", "sessions")
	host := inst + ".tenant-acme.svc"
	if cr.Port != 6379 || cr.Username != "default" {
		t.Fatalf("kv endpoint port=%d user=%q, want 6379/default", cr.Port, cr.Username)
	}
	if cr.ConnectionString != "redis://default:"+cr.Password+"@"+host+":6379" {
		t.Fatalf("dsn %q not the expected redis default-user DSN", cr.ConnectionString)
	}

	ds := orch.datastores["tenant-acme/"+inst]
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "type"); got != "valkey" {
		t.Fatalf("spec.type = %q, want valkey", got)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "image", "repository"); got != "ghcr.io/hanzoai/kv" {
		t.Fatalf("image = %q, want ghcr.io/hanzoai/kv", got)
	}
	// The password is a MOUNTED config file, not envFrom — the kv binary reads no
	// password from env, so an envFrom engine would boot UNAUTHENTICATED.
	if envFrom, found, _ := unstructured.NestedSlice(ds.Object, "spec", "envFrom"); found && len(envFrom) != 0 {
		t.Fatalf("kv must NOT use envFrom (binary ignores env password), got %v", envFrom)
	}
	vols, _, _ := unstructured.NestedSlice(ds.Object, "spec", "volumes")
	if len(vols) != 1 {
		t.Fatalf("spec.volumes = %v, want one addon-secret volume", vols)
	}
	if v, ok := vols[0].(map[string]any); !ok || v["secret"] == nil {
		t.Fatalf("volume is not a secret source: %v", vols[0])
	}
	args, _, _ := unstructured.NestedStringSlice(ds.Object, "spec", "args")
	if len(args) == 0 || args[0] != "/etc/kvconf/kv.conf" {
		t.Fatalf("spec.args must load the requirepass config first, got %v", args)
	}
	// The mounted Secret is a valkey config snippet carrying the exact password.
	sec := orch.secrets["tenant-acme/"+inst+"-admin"]
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	if sd["kv.conf"] != "requirepass "+cr.Password+"\n" {
		t.Fatalf("admin Secret kv.conf = %q, want requirepass <pw>", sd["kv.conf"])
	}
}

// TestDedicated_InstanceBindingInjectsURL proves an instance-bound create
// projects the DSN as <KIND>_URL into <instance>-addons, and a SECOND add-on on
// the same instance MERGES (never clobbers the first).
func TestDedicated_InstanceBindingInjectsURL(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	r1 := postCreateInstance(t, s, "datastore", "acme", "warehouse", "commerce")
	if r1.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r1.Body)
		t.Fatalf("datastore create = %d body=%s", r1.StatusCode, body)
	}
	var cr1 createResp
	_ = json.NewDecoder(r1.Body).Decode(&cr1)

	addons := orch.addons["tenant-acme/commerce-addons"]
	if addons["DATASTORE_URL"] != cr1.ConnectionString {
		t.Fatalf("DATASTORE_URL = %q, want the assembled DSN %q", addons["DATASTORE_URL"], cr1.ConnectionString)
	}

	// A second add-on for the SAME instance merges a distinct key.
	r2 := postCreateInstance(t, s, "kv", "acme", "cache", "commerce")
	if r2.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r2.Body)
		t.Fatalf("kv create = %d body=%s", r2.StatusCode, body)
	}
	var cr2 createResp
	_ = json.NewDecoder(r2.Body).Decode(&cr2)

	addons = orch.addons["tenant-acme/commerce-addons"]
	if addons["KV_URL"] != cr2.ConnectionString {
		t.Fatalf("KV_URL = %q, want %q", addons["KV_URL"], cr2.ConnectionString)
	}
	if addons["DATASTORE_URL"] != cr1.ConnectionString {
		t.Fatalf("second add-on clobbered the first: DATASTORE_URL = %q", addons["DATASTORE_URL"])
	}
}

// TestDedicated_NotInstanceBoundSkipsInjection proves a create WITHOUT an
// instance touches no addons Secret — the pre-binding behavior is unchanged.
func TestDedicated_NotInstanceBoundSkipsInjection(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	if resp := postCreate(t, s, "datastore", "acme", "warehouse"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	if len(orch.addons) != 0 {
		t.Fatalf("un-bound create wrote addons %v, want none", orch.addons)
	}
}

// TestDedicated_DropRemovesURLBeforeTeardown proves drop reverts the instance to
// Base (removes the <KIND>_URL) BEFORE it tears the backend down, and that the
// key is gone afterward.
func TestDedicated_DropRemovesURLBeforeTeardown(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	if resp := postCreateInstance(t, s, "datastore", "acme", "warehouse", "commerce"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	if orch.addons["tenant-acme/commerce-addons"]["DATASTORE_URL"] == "" {
		t.Fatalf("precondition: DATASTORE_URL should be injected")
	}

	resp := doReq(t, drop(s, "datastore"), http.MethodDelete, "/v1/datastore/:name", "/v1/datastore/warehouse", "acme", "")
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("drop = %d body=%s, want 204", resp.StatusCode, body)
	}

	// The URL is gone (instance reverted to Base)...
	if v := orch.addons["tenant-acme/commerce-addons"]["DATASTORE_URL"]; v != "" {
		t.Fatalf("DATASTORE_URL still present after drop: %q", v)
	}
	// ...and the revert happened BEFORE the CR teardown (never leave a live
	// instance pointed at a deleted backend).
	inst := instanceName("datastore", "acme", "warehouse")
	removeIdx, teardownIdx := -1, -1
	for i, op := range orch.ops {
		if op == "remove:tenant-acme/commerce-addons:DATASTORE_URL" {
			removeIdx = i
		}
		if op == "teardown:tenant-acme/"+inst {
			teardownIdx = i
		}
	}
	if removeIdx < 0 || teardownIdx < 0 || removeIdx > teardownIdx {
		t.Fatalf("revert must precede teardown; ops=%v (remove=%d teardown=%d)", orch.ops, removeIdx, teardownIdx)
	}
}

// TestDedicated_InjectFailureRollsBack proves injection is part of the ATOMIC
// provision: if wiring the instance fails, the row, CR and admin Secret are all
// rolled back (nothing persisted, org never billed for a half-wired resource).
func TestDedicated_InjectFailureRollsBack(t *testing.T) {
	orch := newFakeOrch()
	orch.patchErr = context.DeadlineExceeded // make PatchAddonSecret fail
	s := newDedicatedSvc(t, orch)

	resp := postCreateInstance(t, s, "datastore", "acme", "warehouse", "commerce")
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 502 (inject failed)", resp.StatusCode, body)
	}
	// Nothing persisted.
	if _, err := s.State.store.Get(context.Background(), "acme", "datastore", "warehouse"); err == nil {
		t.Fatalf("row persisted despite inject failure — provision not atomic")
	}
	// Backend rolled back.
	inst := instanceName("datastore", "acme", "warehouse")
	if orch.datastores["tenant-acme/"+inst] != nil {
		t.Fatalf("Datastore CR not rolled back after inject failure")
	}
	if orch.secrets["tenant-acme/"+inst+"-admin"] != nil {
		t.Fatalf("admin Secret not rolled back after inject failure")
	}
}

// TestDedicated_InjectPartialWriteRollsBackOrphanKey proves the tightened
// rollback (Red low-1): when the inject PATCH LANDS server-side yet still
// returns an error (a dropped response / post-commit timeout), the rollback
// SCRUBS the already-written <KIND>_URL so the instance is never left pointing
// at the backend we then tear down — a dangling DSN is strictly worse than Base.
func TestDedicated_InjectPartialWriteRollsBackOrphanKey(t *testing.T) {
	orch := newFakeOrch()
	orch.patchErr = context.DeadlineExceeded
	orch.patchErrAfterWrite = true // the key lands, THEN the call reports failure
	s := newDedicatedSvc(t, orch)

	resp := postCreateInstance(t, s, "datastore", "acme", "warehouse", "commerce")
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 502 (inject failed)", resp.StatusCode, body)
	}

	// The orphan DATASTORE_URL that briefly landed must be gone — else a live
	// instance would point at a torn-down backend.
	if v := orch.addons["tenant-acme/commerce-addons"]["DATASTORE_URL"]; v != "" {
		t.Fatalf("orphan DATASTORE_URL survived a failed inject: %q", v)
	}
	// And the scrub must have actually RUN (a remove op AFTER the inject) — not
	// merely be absent because the write never happened.
	injectIdx, removeIdx := -1, -1
	for i, op := range orch.ops {
		switch op {
		case "inject:tenant-acme/commerce-addons:DATASTORE_URL":
			injectIdx = i
		case "remove:tenant-acme/commerce-addons:DATASTORE_URL":
			removeIdx = i
		}
	}
	if injectIdx < 0 || removeIdx < 0 || removeIdx < injectIdx {
		t.Fatalf("rollback must scrub the landed key (inject then remove); ops=%v", orch.ops)
	}
	// The rest of the provision is still fully rolled back.
	if _, err := s.State.store.Get(context.Background(), "acme", "datastore", "warehouse"); err == nil {
		t.Fatalf("row persisted despite inject failure")
	}
	inst := instanceName("datastore", "acme", "warehouse")
	if orch.datastores["tenant-acme/"+inst] != nil || orch.secrets["tenant-acme/"+inst+"-admin"] != nil {
		t.Fatalf("backend not rolled back after inject failure")
	}
}

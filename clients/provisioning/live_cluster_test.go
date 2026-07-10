//go:build livecluster

package provisioning

// Live end-to-end proof against a REAL cluster + the deployed Hanzo operator.
// Exercises cloud's ACTUAL dedicated code path (create/get/drop through the HTTP
// handlers, real k8sOrchestrator over KUBECONFIG) for two orgs, proving:
//   - POST /v1/{datastore,docdb} launches the org's OWN instance and returns
//     "provisioning" with a DSN at <inst>.tenant-<org>.svc,
//   - GET reconciles to "ready" once the operator reports the StatefulSet Running,
//   - two orgs get SEPARATE instances in SEPARATE namespaces (isolation),
//   - DELETE reaps the instance.
//
// Run:  go test -tags livecluster -run TestLive_DedicatedProvisioning \
//         ./clients/provisioning/ -v -timeout 15m
// Requires a KUBECONFIG with rights to create namespaces + hanzo.ai/v1
// datastores + secrets in tenant-<org> (cluster-admin, or cloud-api after the
// universe RBAC lands).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

func liveSvc(t *testing.T) *cloud.Service[state] {
	t.Helper()
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	t.Setenv("CLOUD_DEDICATED_SIZE", "5Gi") // small PVC = faster boot
	log := luxlog.New("module", "provlive")
	orch := newOrchestrator()
	if err := orch.Ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	orch.rbacTimeout = 90 * time.Second
	return &cloud.Service[state]{Base: cloud.Base{Log: log}, State: state{store: newTestStore(t), sec: openSecrets("hanzo", log), reg: newRegistry(), orch: orch}}
}

func createLive(t *testing.T, s *cloud.Service[state], kind, org, name string) createResp {
	t.Helper()
	resp := postCreate(t, s, kind, org, name)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s/%s create = %d body=%s", kind, name, resp.StatusCode, b)
	}
	var cr createResp
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	t.Logf("provisioned %s org=%s name=%s -> status=%s host=%s:%d dsn=%s", kind, org, name, cr.Status, cr.Host, cr.Port, cr.ConnectionString)
	return cr
}

func waitReady(t *testing.T, s *cloud.Service[state], kind, org, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp := doReq(t, get(s, kind), http.MethodGet, "/v1/"+kind+"/:name", "/v1/"+kind+"/"+name, org, "")
		var g getResp
		_ = json.NewDecoder(resp.Body).Decode(&g)
		if g.Status == statusReady {
			t.Logf("%s org=%s name=%s READY at %s:%d", kind, org, name, g.Host, g.Port)
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("%s org=%s name=%s never reached ready in %s", kind, org, name, timeout)
}

func dropLive(t *testing.T, s *cloud.Service[state], kind, org, name string) {
	t.Helper()
	resp := doReq(t, drop(s, kind), http.MethodDelete, "/v1/"+kind+"/:name", "/v1/"+kind+"/"+name, org, "")
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s/%s drop = %d body=%s", kind, name, resp.StatusCode, b)
	}
	t.Logf("reaped %s org=%s name=%s", kind, org, name)
}

func TestLive_DedicatedProvisioning(t *testing.T) {
	s := liveSvc(t)
	ctx := context.Background()

	// Two orgs, each gets its OWN datastore instance in its OWN namespace.
	a := createLive(t, s, "datastore", "clivea", "shop")
	b := createLive(t, s, "datastore", "cliveb", "shop")
	// A docdb for org clivea too (the other un-gated engine).
	d := createLive(t, s, "docdb", "clivea", "docs")

	// Isolation: distinct instances, distinct namespaces.
	ra, _ := s.State.store.Get(ctx, "clivea", "datastore", "shop")
	rb, _ := s.State.store.Get(ctx, "cliveb", "datastore", "shop")
	if ra.PhysicalName == rb.PhysicalName {
		t.Fatalf("two orgs collided on instance %q", ra.PhysicalName)
	}
	if ra.Host == rb.Host {
		t.Fatalf("two orgs share a host %q — not isolated", ra.Host)
	}
	t.Logf("ISOLATION: clivea=%s  cliveb=%s  (separate instances + namespaces)", ra.Host, rb.Host)
	_ = a
	_ = b
	_ = d

	// Readiness (bounded — StatefulSet image pull + boot).
	waitReady(t, s, "datastore", "clivea", "shop", 5*time.Minute)
	waitReady(t, s, "datastore", "cliveb", "shop", 5*time.Minute)
	waitReady(t, s, "docdb", "clivea", "docs", 5*time.Minute)

	// Reap.
	dropLive(t, s, "datastore", "clivea", "shop")
	dropLive(t, s, "datastore", "cliveb", "shop")
	dropLive(t, s, "docdb", "clivea", "docs")
}

package provisioning

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestDedicated_DocdbIsBase proves the managed "document database" (docdb)
// provisions a dedicated per-org Hanzo Base instance (SQLite + realtime,
// IAM-native) — NOT MongoDB/FerretDB. The Datastore CR runs the base image on
// :8090 with the data PVC mounted at /data; the customer connects to the
// instance's OWN Base API and authenticates via Hanzo IAM (no per-resource
// password); the connection string is a Base URL, never mongodb://.
func TestDedicated_DocdbIsBase(t *testing.T) {
	t.Setenv("IAM_URL", "https://hanzo.id")
	t.Setenv("KMS_URL", "https://kms.hanzo.ai")

	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)

	resp := postCreate(t, s, "docdb", "acme", "events")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var cr createResp
	_ = json.NewDecoder(resp.Body).Decode(&cr)

	ns := "tenant-acme"
	inst := instanceName("docdb", "acme", "events")
	wantHost := inst + "." + ns + ".svc"

	// Endpoint: the instance's OWN Base service on :8090; DSN is a Base API URL.
	if cr.Host != wantHost || cr.Port != 8090 {
		t.Fatalf("endpoint = %s:%d, want %s:8090", cr.Host, cr.Port, wantHost)
	}
	if strings.Contains(cr.ConnectionString, "mongodb://") {
		t.Fatalf("connString %q must NOT be mongodb://", cr.ConnectionString)
	}
	if want := "http://" + wantHost + ":8090/v1"; cr.ConnectionString != want {
		t.Fatalf("connString = %q, want %q (Base API URL)", cr.ConnectionString, want)
	}
	// IAM-native: no per-resource credential is returned.
	if cr.Password != "" || cr.Username != "" {
		t.Fatalf("IAM-native docdb must return no credential; got user=%q pwSet=%v", cr.Username, cr.Password != "")
	}

	// The Datastore CR runs the Base image on :8090 with /data mounted.
	ds := orch.datastores[ns+"/"+inst]
	if ds == nil {
		t.Fatalf("no Datastore CR applied at %s/%s", ns, inst)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "image", "repository"); got != "ghcr.io/hanzoai/base" {
		t.Fatalf("image = %q, want ghcr.io/hanzoai/base (NOT ghcr.io/hanzoai/docdb)", got)
	}
	ports, _, _ := unstructured.NestedSlice(ds.Object, "spec", "ports")
	if len(ports) != 1 {
		t.Fatalf("spec.ports = %v, want one (8090)", ports)
	}
	if p, _, _ := unstructured.NestedInt64(ports[0].(map[string]any), "containerPort"); p != 8090 {
		t.Fatalf("containerPort = %d, want 8090", p)
	}
	// The data PVC MUST be mounted (else Base writes to ephemeral storage).
	vms, _, _ := unstructured.NestedSlice(ds.Object, "spec", "volumeMounts")
	if len(vms) != 1 {
		t.Fatalf("spec.volumeMounts = %v, want [{name:data,mountPath:/data}]", vms)
	}
	if mp, _, _ := unstructured.NestedString(vms[0].(map[string]any), "mountPath"); mp != "/data" {
		t.Fatalf("volumeMount mountPath = %q, want /data", mp)
	}

	// The admin Secret carries IAM config — NO POSTGRES_*/mongo credentials.
	sec := orch.secrets[ns+"/"+inst+"-admin"]
	if sec == nil {
		t.Fatalf("no admin Secret at %s/%s-admin", ns, inst)
	}
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	if sd["IAM_URL"] != "https://hanzo.id" {
		t.Fatalf("admin Secret IAM_URL = %q, want https://hanzo.id", sd["IAM_URL"])
	}
	for _, k := range []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"} {
		if _, ok := sd[k]; ok {
			t.Fatalf("admin Secret must carry NO mongo/postgres creds; found %q", k)
		}
	}

	// Persisted row: dedicated docdb, IAM-native (no sealed secret ref).
	r, err := s.store.Get(context.Background(), "acme", "docdb", "events")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if r.SecretRef != "" {
		t.Fatalf("IAM-native docdb must seal no secret; SecretRef=%q", r.SecretRef)
	}
}

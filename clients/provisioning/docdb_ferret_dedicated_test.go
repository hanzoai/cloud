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

// TestDedicated_DocdbIsFerretOnSQL proves the managed "document database" (docdb)
// provisions a dedicated per-org FerretDB instance speaking the MongoDB wire
// protocol on :27017, backed by Hanzo SQL (SQLite) — NOT a raw mongod, NOT
// Postgres. Existing MongoDB drivers (mongodb://) connect with the returned
// per-instance SCRAM credential; data at rest is SQLite files under /state
// (verified end-to-end by the docker-based Mongo-wire + SQLite-at-rest check in
// the PR: a standard MongoDB Go driver did Insert/Find/Update/Count over the
// wire, and /state held per-database *.sqlite files with "SQLite format 3"
// magic — no WiredTiger mongod datadir, no Postgres PG_VERSION).
func TestDedicated_DocdbIsFerretOnSQL(t *testing.T) {
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

	// MongoDB wire endpoint on :27017 with a per-instance credential.
	if cr.Host != wantHost || cr.Port != 27017 {
		t.Fatalf("endpoint = %s:%d, want %s:27017", cr.Host, cr.Port, wantHost)
	}
	if !strings.HasPrefix(cr.ConnectionString, "mongodb://"+cr.Username+":") ||
		!strings.Contains(cr.ConnectionString, wantHost+":27017") {
		t.Fatalf("connString %q must be mongodb://<user>:<pw>@%s:27017/…", cr.ConnectionString, wantHost)
	}
	if cr.Username == "" || cr.Password == "" {
		t.Fatalf("FerretDB docdb must return a per-instance SCRAM credential; got user=%q pwSet=%v", cr.Username, cr.Password != "")
	}

	// The Datastore CR runs the FerretDB image on :27017 with /state mounted.
	ds := orch.datastores[ns+"/"+inst]
	if ds == nil {
		t.Fatalf("no Datastore CR applied at %s/%s", ns, inst)
	}
	if got, _, _ := unstructured.NestedString(ds.Object, "spec", "image", "repository"); got != "ghcr.io/hanzoai/docdb-sqlite" {
		t.Fatalf("image = %q, want ghcr.io/hanzoai/docdb-sqlite (FerretDB v1 + SQLite backend)", got)
	}
	ports, _, _ := unstructured.NestedSlice(ds.Object, "spec", "ports")
	if len(ports) != 1 {
		t.Fatalf("spec.ports = %v, want one (27017)", ports)
	}
	if p, _, _ := unstructured.NestedInt64(ports[0].(map[string]any), "containerPort"); p != 27017 {
		t.Fatalf("containerPort = %d, want 27017", p)
	}
	// The SQLite data PVC MUST be mounted at /state (else data is ephemeral).
	vms, _, _ := unstructured.NestedSlice(ds.Object, "spec", "volumeMounts")
	if len(vms) != 1 {
		t.Fatalf("spec.volumeMounts = %v, want [{name:data,mountPath:/state}]", vms)
	}
	if mp, _, _ := unstructured.NestedString(vms[0].(map[string]any), "mountPath"); mp != "/state" {
		t.Fatalf("volumeMount mountPath = %q, want /state", mp)
	}
	// FerretDB runs as UID 1000 (distroless): the pod MUST carry fsGroup so the
	// fresh block PVC is group-writable, else the instance CrashLoops on
	// "permission denied" writing /state. The operator maps spec.fsGroup →
	// PodSecurityContext.fsGroup.
	if fsg, ok, _ := unstructured.NestedInt64(ds.Object, "spec", "fsGroup"); !ok || fsg != 1000 {
		t.Fatalf("spec.fsGroup = %d (present=%v), want 1000 (non-root FerretDB needs a group-writable PVC)", fsg, ok)
	}

	// The admin Secret configures the FerretDB SQLite backend + SCRAM auth —
	// NO Postgres, NO IAM/Base env.
	sec := orch.secrets[ns+"/"+inst+"-admin"]
	if sec == nil {
		t.Fatalf("no admin Secret at %s/%s-admin", ns, inst)
	}
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	if sd["FERRETDB_HANDLER"] != "sqlite" {
		t.Fatalf("FERRETDB_HANDLER = %q, want sqlite (Hanzo SQL backend)", sd["FERRETDB_HANDLER"])
	}
	// SQLite files (and process state) MUST live on the mounted PVC.
	if sd["FERRETDB_SQLITE_URL"] != "file:/state/" {
		t.Fatalf("FERRETDB_SQLITE_URL = %q, want file:/state/ (SQLite files on the PVC)", sd["FERRETDB_SQLITE_URL"])
	}
	if sd["FERRETDB_STATE_DIR"] != "/state" {
		t.Fatalf("FERRETDB_STATE_DIR = %q, want /state (state persists on the PVC)", sd["FERRETDB_STATE_DIR"])
	}
	if sd["FERRETDB_TEST_ENABLE_NEW_AUTH"] != "true" {
		t.Fatalf("FERRETDB_TEST_ENABLE_NEW_AUTH = %q, want true (required to bootstrap the SCRAM setup user)", sd["FERRETDB_TEST_ENABLE_NEW_AUTH"])
	}
	for _, k := range []string{"FERRETDB_SETUP_USERNAME", "FERRETDB_SETUP_PASSWORD", "FERRETDB_SETUP_DATABASE"} {
		if sd[k] == "" {
			t.Fatalf("admin Secret missing %q (FerretDB SCRAM setup); have %v", k, sd)
		}
	}
	if sd["FERRETDB_SETUP_PASSWORD"] != cr.Password {
		t.Fatalf("returned password must match the FerretDB setup password the instance boots with")
	}
	for _, k := range []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "IAM_URL", "FERRETDB_POSTGRESQL_URL"} {
		if _, ok := sd[k]; ok {
			t.Fatalf("admin Secret must carry NO Postgres/IAM env (Hanzo SQL only); found %q", k)
		}
	}

	// Persisted as a dedicated docdb "provisioning" row.
	r, err := s.State.store.Get(context.Background(), "acme", "docdb", "events")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if r.Status != statusProvisioning {
		t.Fatalf("row status = %q, want provisioning", r.Status)
	}
}

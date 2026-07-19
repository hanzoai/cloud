// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// k8sFake is a stand-in for Visor's /v1/k8s surface. It speaks the casibase
// {status,msg,data} envelope, scopes every read by ?owner (proving cloud forwards
// the VALIDATED principal's org), and records the last owner + any mutation it saw
// so a test can assert an admin-gated call is REFUSED before it ever reaches Visor.
type k8sFake struct {
	lastOwner   string
	createdBody map[string]any
	deletedID   string
}

func (f *k8sFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// list (GET) + create (POST) on the bare /clusters literal.
	mux.HandleFunc("/v1/k8s/clusters", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		switch r.Method {
		case http.MethodGet:
			var out []map[string]any
			if f.lastOwner == "acme" {
				out = []map[string]any{{
					"id": "cl-1", "name": "prod", "regionSlug": "sfo3", "status": "running",
					"tags": []string{"managed-by:hanzo-visor", "hanzo-org:acme"},
				}}
			}
			envelope200(w, out)
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&f.createdBody)
			envelope200(w, map[string]any{
				"id": "cl-new", "name": f.createdBody["name"], "regionSlug": f.createdBody["region"],
				"status": "provisioning", "tags": []string{"hanzo-org:" + f.lastOwner},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	// detail (GET) + delete (DELETE) on /clusters/{id}.
	mux.HandleFunc("/v1/k8s/clusters/", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		id := strings.TrimPrefix(r.URL.Path, "/v1/k8s/clusters/")
		switch r.Method {
		case http.MethodGet:
			envelope200(w, map[string]any{
				"id": id, "name": "prod", "regionSlug": "sfo3", "status": "running",
				"tags": []string{"hanzo-org:acme"},
				"nodePools": []map[string]any{{
					"id": "p1", "name": "workers", "size": "s-4vcpu-8gb", "count": 3,
				}},
				"nodes": []map[string]any{{
					"owner": "acme", "name": "worker-1", "id": "555",
					"provider": "DigitalOcean", "size": "s-4vcpu-8gb", "region": "sfo3", "state": "running",
				}},
			})
		case http.MethodDelete:
			f.deletedID = id
			envelope200(w, true)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/k8s/nodes", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		var out []map[string]any
		if f.lastOwner == "acme" {
			out = []map[string]any{{
				"owner": "acme", "name": "worker-1", "id": "555",
				"provider": "DigitalOcean", "size": "s-4vcpu-8gb", "region": "sfo3",
				"state": "running", "tag": "doks-cluster:prod",
			}}
		}
		envelope200(w, out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mountK8s(t *testing.T, f *k8sFake) *zip.App {
	t.Helper()
	srv := f.server(t)
	t.Setenv("VISOR_URL", srv.URL)
	t.Setenv("VISOR_CLIENT_ID", "")     // force the bearer-forward path (fake ignores auth)
	t.Setenv("VISOR_CLIENT_SECRET", "") //
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// reqK8s issues a request as a validated principal (X-Org-Id + X-User-Id). admin=true
// adds the OrgAdmin bit the mutation gate checks — so the same helper drives both the
// allowed and the refused mutation paths.
func reqK8s(t *testing.T, app *zip.App, method, path, org string, admin bool, body any) (int, []byte) {
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
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// GET /v1/k8s/clusters is org-scoped: cloud forwards the validated org, and maps
// Visor clusters to the managed clusterView. No org → 403 (never reaches Visor).
func TestK8sClustersListTenantScoped(t *testing.T) {
	f := &k8sFake{}
	app := mountK8s(t, f)

	if code, _ := reqK8s(t, app, http.MethodGet, "/v1/k8s/clusters", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	code, body := reqK8s(t, app, http.MethodGet, "/v1/k8s/clusters", "acme", false, nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if f.lastOwner != "acme" {
		t.Fatalf("cloud must forward owner=acme, got %q", f.lastOwner)
	}
	var out struct {
		Clusters []clusterView `json:"clusters"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(out.Clusters) != 1 {
		t.Fatalf("acme want 1 cluster, got %d", len(out.Clusters))
	}
	cl := out.Clusters[0]
	if cl.DoksClusterID != "cl-1" || cl.Name != "prod" || cl.Region != "sfo3" || cl.Status != "running" || cl.Kind != "managed" {
		t.Fatalf("cluster view mismatch: %+v", cl)
	}
}

// GET /v1/k8s/clusters/:id returns detail: node pools (poolId mapped from Visor's
// id) with the derived node count, and the worker nodes as machineViews.
func TestK8sClusterDetail(t *testing.T) {
	f := &k8sFake{}
	app := mountK8s(t, f)

	code, body := reqK8s(t, app, http.MethodGet, "/v1/k8s/clusters/cl-1", "acme", false, nil)
	if code != http.StatusOK {
		t.Fatalf("detail want 200, got %d (%s)", code, body)
	}
	var d clusterDetailView
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if d.DoksClusterID != "cl-1" || len(d.NodePools) != 1 || d.NodePools[0].PoolID != "p1" || d.NodePools[0].Count != 3 {
		t.Fatalf("detail pools mismatch: %+v", d)
	}
	if d.NodeCount != 3 || d.NodeSize != "s-4vcpu-8gb" {
		t.Fatalf("derived node count/size wrong: count=%d size=%q", d.NodeCount, d.NodeSize)
	}
	if len(d.Nodes) != 1 || d.Nodes[0].ID != "worker-1" || d.Nodes[0].Status != "running" {
		t.Fatalf("detail nodes mismatch: %+v", d.Nodes)
	}
}

// GET /v1/k8s/nodes returns every worker node as a machineView, org-scoped.
func TestK8sNodesTenantScoped(t *testing.T) {
	f := &k8sFake{}
	app := mountK8s(t, f)

	if code, _ := reqK8s(t, app, http.MethodGet, "/v1/k8s/nodes", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("no-org nodes want 403, got %d", code)
	}
	code, body := reqK8s(t, app, http.MethodGet, "/v1/k8s/nodes", "acme", false, nil)
	if code != http.StatusOK {
		t.Fatalf("nodes want 200, got %d (%s)", code, body)
	}
	var out struct {
		Nodes []machineView `json:"nodes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(out.Nodes) != 1 || out.Nodes[0].ID != "worker-1" || out.Nodes[0].Type != "s-4vcpu-8gb" {
		t.Fatalf("node view mismatch: %+v", out.Nodes)
	}
}

// POST /v1/k8s/clusters is ADMIN-GATED: a non-admin is refused BEFORE the request
// reaches Visor; an admin with a valid body provisions and gets 201; a bad body is a
// 400 at the cloud boundary.
func TestK8sCreateClusterAdminGated(t *testing.T) {
	f := &k8sFake{}
	app := mountK8s(t, f)
	valid := map[string]any{"name": "prod", "region": "sfo3", "version": "latest",
		"nodePool": map[string]any{"size": "s-4vcpu-8gb", "count": 2}}

	// No validated principal → 403.
	if code, _ := reqK8s(t, app, http.MethodPost, "/v1/k8s/clusters", "", false, valid); code != http.StatusForbidden {
		t.Fatalf("no-org create want 403, got %d", code)
	}
	// Validated but NON-admin → 403, and the mutation never reached Visor.
	if code, _ := reqK8s(t, app, http.MethodPost, "/v1/k8s/clusters", "acme", false, valid); code != http.StatusForbidden {
		t.Fatalf("non-admin create want 403, got %d", code)
	}
	if f.createdBody != nil {
		t.Fatalf("non-admin create must NOT reach Visor, but body was received: %+v", f.createdBody)
	}
	// Admin + valid body → 201, forwarded to Visor with the spec intact.
	code, body := reqK8s(t, app, http.MethodPost, "/v1/k8s/clusters", "acme", true, valid)
	if code != http.StatusCreated {
		t.Fatalf("admin create want 201, got %d (%s)", code, body)
	}
	if f.createdBody == nil || f.createdBody["name"] != "prod" || f.createdBody["region"] != "sfo3" {
		t.Fatalf("create spec not forwarded to Visor: %+v", f.createdBody)
	}
	var mv clusterView
	if err := json.Unmarshal(body, &mv); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if mv.Name != "prod" || mv.Status != "provisioning" || mv.Kind != "managed" {
		t.Fatalf("created cluster view mismatch: %+v", mv)
	}
	// Admin + invalid body (no node pool size) → 400 at the boundary.
	if code, _ := reqK8s(t, app, http.MethodPost, "/v1/k8s/clusters", "acme", true,
		map[string]any{"name": "x", "region": "sfo3"}); code != http.StatusBadRequest {
		t.Fatalf("admin create without pool size want 400, got %d", code)
	}
}

// DELETE /v1/k8s/clusters/:id is ADMIN-GATED, like create.
func TestK8sDeleteClusterAdminGated(t *testing.T) {
	f := &k8sFake{}
	app := mountK8s(t, f)

	if code, _ := reqK8s(t, app, http.MethodDelete, "/v1/k8s/clusters/cl-1", "", false, nil); code != http.StatusForbidden {
		t.Fatalf("no-org delete want 403, got %d", code)
	}
	if code, _ := reqK8s(t, app, http.MethodDelete, "/v1/k8s/clusters/cl-1", "acme", false, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin delete want 403, got %d", code)
	}
	if f.deletedID != "" {
		t.Fatalf("non-admin delete must NOT reach Visor, but id %q was deleted", f.deletedID)
	}
	if code, _ := reqK8s(t, app, http.MethodDelete, "/v1/k8s/clusters/cl-1", "acme", true, nil); code != http.StatusNoContent {
		t.Fatalf("admin delete want 204, got %d", code)
	}
	if f.deletedID != "cl-1" {
		t.Fatalf("admin delete must forward id cl-1 to Visor, got %q", f.deletedID)
	}
}

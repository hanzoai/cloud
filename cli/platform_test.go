package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// platformStub spins an httptest server whose handler is provided by the test,
// plus a client pointed at it with the given token.
func platformStub(t *testing.T, token string, h http.HandlerFunc) (*Platform, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	return newPlatform(srv.URL, token), srv.Close
}

func TestPlatformAuthHeaderAndApps(t *testing.T) {
	p, done := platformStub(t, "svc-tok", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer svc-tok" {
			t.Errorf("auth header = %q", got)
		}
		if r.URL.Path != "/v1/apps" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("env") != "main" || r.URL.Query().Get("drift") != "1" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(AppsList{
			Apps: []AppView{{ID: "hanzoai/iam/main", Org: "hanzoai", App: "iam", Env: "main", Drift: json.RawMessage(`{"severity":"red"}`)}},
		})
	})
	defer done()

	res, err := p.Apps(context.Background(), AppsQuery{Env: "main", Drift: true})
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if len(res.Apps) != 1 || res.Apps[0].App != "iam" {
		t.Fatalf("apps wrong: %+v", res.Apps)
	}
	if driftSeverity(res.Apps[0].Drift) != "red" {
		t.Fatalf("drift severity = %q", driftSeverity(res.Apps[0].Drift))
	}
}

func TestPlatformApp(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/hanzoai/iam/main" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("org") != "hanzoai" {
			t.Errorf("org query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(AppView{ID: "hanzoai/iam/main", App: "iam"})
	})
	defer done()
	a, err := p.App(context.Background(), "hanzoai/iam/main", "hanzoai")
	if err != nil || a.App != "iam" {
		t.Fatalf("App: %v %+v", err, a)
	}
}

func TestPlatformSyncApps(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/apps/sync" {
			t.Errorf("sync = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(200)
	})
	defer done()
	if err := p.SyncApps(context.Background()); err != nil {
		t.Fatalf("SyncApps: %v", err)
	}
}

func TestPlatformClustersAndProvision(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/org/acme/cluster":
			_ = json.NewEncoder(w).Encode(map[string]any{"clusters": []Cluster{{DoksClusterID: "c1", Name: "hanzo-acme", Region: "sfo3", Status: "running", Phase: "ready", Active: true}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/org/acme/cluster":
			body, _ := io.ReadAll(r.Body)
			var req ProvisionReq
			_ = json.Unmarshal(body, &req)
			if req.Region != "sfo3" || !req.HA {
				t.Errorf("provision body = %+v", req)
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"cluster": Cluster{DoksClusterID: "c2", Name: "new", Phase: "requested"}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	defer done()

	cs, err := p.Clusters(context.Background(), "acme")
	if err != nil || len(cs) != 1 || cs[0].DoksClusterID != "c1" {
		t.Fatalf("Clusters: %v %+v", err, cs)
	}
	c, err := p.ProvisionCluster(context.Background(), "acme", ProvisionReq{Region: "sfo3", HA: true})
	if err != nil || c.DoksClusterID != "c2" {
		t.Fatalf("ProvisionCluster: %v %+v", err, c)
	}
}

func TestPlatformTargetAndSelect(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/org/acme/cluster/select" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["doksClusterId"] != "c1" {
				t.Errorf("select body = %v", m)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"target": Target{Cluster: "hanzo-acme", Dedicated: true, Namespaces: map[string]string{"acme": "main"}}})
	})
	defer done()

	tg, err := p.Target(context.Background(), "acme")
	if err != nil || tg.Cluster != "hanzo-acme" || !tg.Dedicated {
		t.Fatalf("Target: %v %+v", err, tg)
	}
	id := "c1"
	if _, err := p.SelectTarget(context.Background(), "acme", &id); err != nil {
		t.Fatalf("SelectTarget: %v", err)
	}
}

func TestPlatformInstallBaseline(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/org/acme/cluster/c1/install-baseline" {
			t.Errorf("install-baseline = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(200)
	})
	defer done()
	if err := p.InstallBaseline(context.Background(), "acme", "c1"); err != nil {
		t.Fatalf("InstallBaseline: %v", err)
	}
}

func TestPlatformRedeploy(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		want := "/v1/org/acme/project/p1/env/e1/container/app-x/redeploy"
		if r.Method != http.MethodPost || r.URL.Path != want {
			t.Errorf("redeploy path = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	defer done()
	if err := p.Redeploy(context.Background(), "acme", "p1", "e1", "app-x"); err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
}

func TestPlatformRedeployNotOK(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": false})
	})
	defer done()
	if err := p.Redeploy(context.Background(), "o", "p", "e", "c"); err == nil {
		t.Fatalf("expected error when ok=false")
	}
}

func TestPlatformEnqueueBuild(t *testing.T) {
	p, done := platformStub(t, "svc-tok", func(w http.ResponseWriter, r *http.Request) {
		// The build endpoint must use the BUILD token, not the service token.
		if got := r.Header.Get("Authorization"); got != "Bearer build-tok" {
			t.Errorf("build auth header = %q (must use build token)", got)
		}
		if r.URL.Path != "/v1/arcd/enqueue" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req BuildReq
		_ = json.Unmarshal(body, &req)
		if req.Repo != "hanzoai/pricing" || req.SHA != "abc123" || req.Image == "" {
			t.Errorf("build body = %+v", req)
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(BuildJob{BuildJobID: "bj-1", Status: "queued", RunnerPool: "runner-pool-32g", Image: req.Image})
	})
	defer done()

	job, err := p.EnqueueBuild(context.Background(), BuildReq{Repo: "hanzoai/pricing", SHA: "abc123", Image: "ghcr.io/hanzoai/pricing:t"}, "build-tok")
	if err != nil || job.BuildJobID != "bj-1" {
		t.Fatalf("EnqueueBuild: %v %+v", err, job)
	}
	if _, err := p.EnqueueBuild(context.Background(), BuildReq{Repo: "r", SHA: "s", Image: "i"}, ""); err == nil {
		t.Fatalf("expected error with empty build token")
	}
}

func TestPlatformError401Hint(t *testing.T) {
	p, done := platformStub(t, "bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Unauthorized"})
	})
	defer done()
	_, err := p.Apps(context.Background(), AppsQuery{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "platform service token") {
		t.Fatalf("401 error should carry a token hint, got %v", err)
	}
}

func TestPlatformNoTokenError(t *testing.T) {
	p := newPlatform("https://platform.hanzo.ai", "")
	if _, err := p.Apps(context.Background(), AppsQuery{}); err == nil || !strings.Contains(err.Error(), "no platform token") {
		t.Fatalf("expected no-token error, got %v", err)
	}
}

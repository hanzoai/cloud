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

// Apps hits the LIVE board /v1/paas/apps with the IAM bearer; it sends NO org
// filter (the board is org-confined server-side by the validated identity).
func TestPlatformAuthHeaderAndApps(t *testing.T) {
	p, done := platformStub(t, "svc-tok", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer svc-tok" {
			t.Errorf("auth header = %q", got)
		}
		if r.URL.Path != "/v1/paas/apps" {
			t.Errorf("path = %s, want /v1/paas/apps", r.URL.Path)
		}
		if r.URL.Query().Get("env") != "main" || r.URL.Query().Get("drift") != "1" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if r.URL.Query().Has("org") {
			t.Errorf("client must NOT send an org filter (identity confines the board): %s", r.URL.RawQuery)
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

// App hits /v1/paas/apps/{app}; no org query (identity scopes it).
func TestPlatformApp(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/paas/apps/iam" {
			t.Errorf("path = %s, want /v1/paas/apps/iam", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("app get must carry no query, got %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(AppView{ID: "hanzoai/iam/main", App: "iam", Phase: "Running"})
	})
	defer done()
	a, err := p.App(context.Background(), "iam")
	if err != nil || a.App != "iam" {
		t.Fatalf("App: %v %+v", err, a)
	}
}

// Clusters hits the LIVE /v1/clusters (org from identity, not the path).
func TestPlatformClusters(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/clusters" {
			t.Errorf("clusters = %s %s, want GET /v1/clusters", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"clusters": []Cluster{
			{DoksClusterID: "c1", Name: "hanzo-acme", Region: "sfo3", Status: "running", Kind: "managed", NodeCount: 3},
		}})
	})
	defer done()

	cs, err := p.Clusters(context.Background())
	if err != nil || len(cs) != 1 || cs[0].ID() != "c1" || cs[0].Kind != "managed" {
		t.Fatalf("Clusters: %v %+v", err, cs)
	}
}

// A BYO cluster with no DOKS id keys on its name via ID().
func TestClusterIDFallsBackToName(t *testing.T) {
	c := Cluster{Name: "byo-1", Kind: "byo"}
	if c.ID() != "byo-1" {
		t.Fatalf("ID() = %q, want byo-1", c.ID())
	}
}

// Redeploy hits /v1/paas/apps/{app}/deploy (rolling restart), org from identity.
func TestPlatformRedeploy(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/paas/apps/app-x/deploy" {
			t.Errorf("redeploy = %s %s, want POST /v1/paas/apps/app-x/deploy", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("env") != "test" {
			t.Errorf("env query = %s", r.URL.RawQuery)
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(DeployResult{OK: true, App: "app-x", Namespace: "hanzo-testnet", Env: "test", RestartedAt: "2026-07-18T00:00:00Z"})
	})
	defer done()
	res, err := p.Redeploy(context.Background(), "app-x", "test")
	if err != nil || res.Namespace != "hanzo-testnet" {
		t.Fatalf("Redeploy: %v %+v", err, res)
	}
}

func TestPlatformRedeployNotOK(t *testing.T) {
	p, done := platformStub(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(DeployResult{OK: false})
	})
	defer done()
	if _, err := p.Redeploy(context.Background(), "c", ""); err == nil {
		t.Fatalf("expected error when ok=false")
	}
}

func TestPlatformEnqueueBuild(t *testing.T) {
	p, done := platformStub(t, "svc-tok", func(w http.ResponseWriter, r *http.Request) {
		// The build endpoint must use the BUILD token, not the service token.
		if got := r.Header.Get("Authorization"); got != "Bearer build-tok" {
			t.Errorf("build auth header = %q (must use build token)", got)
		}
		if r.URL.Path != "/v1/runner" {
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
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "hanzo login") {
		t.Fatalf("401 error should point at `hanzo login`, got %v", err)
	}
}

func TestPlatformNoTokenError(t *testing.T) {
	p := newPlatform("https://platform.hanzo.ai", "")
	// After unify-infra, the "no credential" error points at `hanzo login` — the one
	// identity that authorizes the platform — not a separate platform token.
	if _, err := p.Apps(context.Background(), AppsQuery{}); err == nil || !strings.Contains(err.Error(), "hanzo login") {
		t.Fatalf("expected a `hanzo login` hint, got %v", err)
	}
}

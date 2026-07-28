package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRepoURL(t *testing.T) {
	cases := map[string]string{
		"luxfi/wallet":                    "https://github.com/luxfi/wallet",
		"hanzoai/cloud":                   "https://github.com/hanzoai/cloud",
		"https://github.com/luxfi/wallet": "https://github.com/luxfi/wallet", // full URL untouched
		"git@github.com:luxfi/wallet.git": "git@github.com:luxfi/wallet.git", // scp-style untouched
		"https://gitlab.com/org/repo":     "https://gitlab.com/org/repo",     // non-github URL untouched
		"owner/name/extra":                "owner/name/extra",                // not a bare owner/name
		"single":                          "single",                          // not two segments
		"":                                "",                                // empty
	}
	for in, want := range cases {
		if got := normalizeRepoURL(in); got != want {
			t.Errorf("normalizeRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// withPlatform points the CLI at an httptest platform via env (HANZO_PLATFORM_URL
// + HANZO_PLATFORM_TOKEN), the same resolution path the real binary uses.
func withPlatform(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	sandbox(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("HANZO_PLATFORM_URL", srv.URL)
	t.Setenv("HANZO_PLATFORM_TOKEN", "svc-tok")
	return srv.URL
}

// withCloud is withPlatform's sibling for routes the CLOUD binary serves.
// /v1/runner is one: the platform host does not implement it, so a build sent
// to PlatformURL answers 500 there and 401 here. Pointing this at CloudURL is
// what the test is asserting.
func withCloud(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	sandbox(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("HANZO_CLOUD_URL", srv.URL)
	t.Setenv("HANZO_PLATFORM_TOKEN", "svc-tok")
	return srv.URL
}

// apps list hits the LIVE board path /v1/paas/apps and renders the fleet table.
func TestAppsListCommandTable(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/paas/apps" {
			t.Errorf("apps path = %s, want /v1/paas/apps", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(AppsList{
			Apps: []AppView{
				{Org: "hanzoai", App: "iam", Env: "main", DeclaredTag: "v1.2.3", RunningTag: "v1.2.3", Health: "green", Drift: json.RawMessage(`{"severity":"ok"}`)},
			},
			Summary: struct {
				Total   int            `json:"total"`
				ByDrift map[string]int `json:"byDrift"`
			}{Total: 1, ByDrift: map[string]int{"ok": 1}},
		})
	})
	out, err := runRoot(t, "", "apps", "list")
	if err != nil {
		t.Fatalf("apps list: %v", err)
	}
	for _, want := range []string{"APP", "iam", "v1.2.3", "green", "ok", "1 apps"} {
		if !strings.Contains(out, want) {
			t.Fatalf("apps list table missing %q in:\n%s", want, out)
		}
	}
}

// apps list honors --env/--health/--drift as server query params (the board filters).
func TestAppsListCommandFilters(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("env") != "main" || q.Get("health") != "red" || q.Get("drift") != "1" {
			t.Errorf("filters not forwarded: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(AppsList{})
	})
	if _, err := runRoot(t, "", "apps", "list", "--env", "main", "--health", "red", "--drift"); err != nil {
		t.Fatalf("apps list filters: %v", err)
	}
}

func TestAppsListCommandJSON(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AppsList{Apps: []AppView{{Org: "hanzoai", App: "iam", Env: "main"}}})
	})
	out, err := runRoot(t, "", "apps", "list", "-o", "json")
	if err != nil {
		t.Fatalf("apps list json: %v", err)
	}
	var res AppsList
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(res.Apps) != 1 || res.Apps[0].App != "iam" {
		t.Fatalf("json decode wrong: %+v", res.Apps)
	}
}

// apps get hits /v1/paas/apps/{app}.
func TestAppsGetCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/paas/apps/iam" {
			t.Errorf("path = %s, want /v1/paas/apps/iam", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(AppView{ID: "hanzoai/iam/main", Org: "hanzoai", App: "iam", Env: "main", DeclaredTag: "v1.2.3", Health: "green", Phase: "Running"})
	})
	out, err := runRoot(t, "", "apps", "get", "iam")
	if err != nil {
		t.Fatalf("apps get: %v", err)
	}
	for _, want := range []string{"hanzoai/iam/main", "Running", "v1.2.3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("apps get missing %q in:\n%s", want, out)
		}
	}
}

// deploy hits /v1/paas/apps/{app}/deploy — a rolling restart, org from identity.
func TestDeployCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/paas/apps/app-x/deploy" || r.URL.Query().Get("env") != "main" {
			t.Errorf("redeploy request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(DeployResult{OK: true, App: "app-x", Namespace: "hanzo", Env: "main", RestartedAt: "2026-07-18T12:00:00Z"})
	})
	out, err := runRoot(t, "", "deploy", "app-x", "--env", "main")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(out, "restarted app-x") || !strings.Contains(out, "namespace=hanzo") {
		t.Fatalf("deploy output: %q", out)
	}
}

// deploy REQUIRES --env — a bare deploy errors CLI-side, never silently prod.
func TestDeployRequiresEnv(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("deploy without --env must not reach the server")
		w.WriteHeader(202)
	})
	if _, err := runRoot(t, "", "deploy", "app-x"); err == nil {
		t.Fatalf("deploy must require --env")
	}
}

// deploy --env selects the lifecycle namespace via the ?env query param.
func TestDeployCommandEnv(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/paas/apps/chat/deploy" || r.URL.Query().Get("env") != "test" {
			t.Errorf("deploy env request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(DeployResult{OK: true, App: "chat", Namespace: "hanzo-testnet", Env: "test", RestartedAt: "2026-07-18T12:00:00Z"})
	})
	if _, err := runRoot(t, "", "deploy", "chat", "--env", "test"); err != nil {
		t.Fatalf("deploy --env: %v", err)
	}
}

// A non-ok deploy response is surfaced as an error.
func TestDeployNotOK(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(DeployResult{OK: false})
	})
	if _, err := runRoot(t, "", "deploy", "app-x", "--env", "main"); err == nil {
		t.Fatalf("deploy must error when the server does not report ok")
	}
}

// clusters list hits the LIVE /v1/clusters (org from identity, not the path).
func TestClustersListCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/clusters" {
			t.Errorf("path = %s, want /v1/clusters", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"clusters": []Cluster{
			{DoksClusterID: "c1", Name: "hanzo-acme", Region: "sfo3", Status: "running", Kind: "managed", NodeCount: 3, NodeSize: "s-2vcpu-4gb", NvidiaGPU: 2},
		}})
	})
	out, err := runRoot(t, "", "clusters", "list")
	if err != nil {
		t.Fatalf("clusters list: %v", err)
	}
	for _, want := range []string{"NAME", "hanzo-acme", "c1", "managed", "2 nvidia"} {
		if !strings.Contains(out, want) {
			t.Fatalf("clusters list missing %q in:\n%s", want, out)
		}
	}
}

// clusters get filters the live list client-side by id or name.
func TestClustersGetCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"clusters": []Cluster{
			{DoksClusterID: "c1", Name: "hanzo-acme", Region: "sfo3", Status: "running", Kind: "byo", NodeCount: 1},
		}})
	})
	out, err := runRoot(t, "", "clusters", "get", "c1")
	if err != nil {
		t.Fatalf("clusters get: %v", err)
	}
	if !strings.Contains(out, "hanzo-acme") || !strings.Contains(out, "byo") {
		t.Fatalf("clusters get output: %q", out)
	}
}

func TestBuildCommandValidation(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) })
	// Missing --sha/--image → validation error before any HTTP call.
	if _, err := runRoot(t, "", "build", "hanzoai/pricing"); err == nil {
		t.Fatalf("build must require --sha and --image")
	}
}

func TestBuildCommand(t *testing.T) {
	withCloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runner" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer bt" {
			t.Errorf("build auth = %q", got)
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(BuildJob{BuildJobID: "bj-9", Status: "queued", Image: "ghcr.io/hanzoai/pricing:t"})
	})
	out, err := runRoot(t, "", "build", "hanzoai/pricing", "--sha", "abc", "--image", "ghcr.io/hanzoai/pricing:t", "--build-token", "bt")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(out, "bj-9") {
		t.Fatalf("build output: %q", out)
	}
}

func TestConfigSetGetCommand(t *testing.T) {
	sandbox(t)
	if _, err := runRoot(t, "", "config", "set", "org", "acme"); err != nil {
		t.Fatalf("config set: %v", err)
	}
	out, err := runRoot(t, "", "config", "get", "org")
	if err != nil {
		t.Fatalf("config get: %v", err)
	}
	if strings.TrimSpace(out) != "acme" {
		t.Fatalf("config get = %q", out)
	}
}

// `hanzo build` with no --image reads the repo's OWN hanzo.yml — the same file
// and the same two keys hanzoai/ci reads — so nothing about the recipe is
// restated on the command line and a project with no Dockerfile still builds.
func TestBuildReqLoadRecipe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hanzo.yml")
	if err := os.WriteFile(path, []byte(`binaries:
  - name: hanzo-demo
    main: ./cmd/demo
    platforms: [linux/amd64, darwin/arm64]
  - name: hanzo-demo-sdk
    image: node:22-bookworm
    run: npm install && npm run build && npm pack --pack-destination .
    out: "*.tgz"
bucket: plugins
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var br BuildReq
	if err := br.loadRecipe(path); err != nil {
		t.Fatalf("loadRecipe: %v", err)
	}
	if len(br.Binaries) != 2 || br.Bucket != "plugins" {
		t.Fatalf("got %d binaries bucket=%q", len(br.Binaries), br.Bucket)
	}
	if br.Binaries[0].Main != "./cmd/demo" || len(br.Binaries[0].Platforms) != 2 {
		t.Errorf("go lane: %+v", br.Binaries[0])
	}
	if br.Binaries[1].Image != "node:22-bookworm" || br.Binaries[1].Out != "*.tgz" || br.Binaries[1].Run == "" {
		t.Errorf("run lane: %+v", br.Binaries[1])
	}
	// A hanzo.yml with no binaries: is an error naming the alternative, not a
	// silent empty build.
	empty := filepath.Join(dir, "empty.yml")
	if err := os.WriteFile(empty, []byte("images:\n  - {name: api}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&BuildReq{}).loadRecipe(empty); err == nil {
		t.Error("a hanzo.yml declaring no binaries: must be refused")
	}
}

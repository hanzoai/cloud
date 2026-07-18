package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeRepoURL(t *testing.T) {
	cases := map[string]string{
		"luxfi/wallet":                        "https://github.com/luxfi/wallet",
		"hanzoai/cloud":                       "https://github.com/hanzoai/cloud",
		"https://github.com/luxfi/wallet":     "https://github.com/luxfi/wallet",   // full URL untouched
		"git@github.com:luxfi/wallet.git":     "git@github.com:luxfi/wallet.git",   // scp-style untouched
		"https://gitlab.com/org/repo":         "https://gitlab.com/org/repo",       // non-github URL untouched
		"owner/name/extra":                    "owner/name/extra",                  // not a bare owner/name
		"single":                              "single",                            // not two segments
		"":                                    "",                                  // empty
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

func TestAppsListCommandTable(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AppsList{
			Apps: []AppView{
				{Org: "hanzoai", App: "iam", Env: "main", DeclaredTag: strptr("v1.2.3"), RunningTag: strptr("v1.2.3"), Health: strptr("green"), Drift: json.RawMessage(`{"severity":"ok"}`)},
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

func TestDeployCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/org/acme/project/p1/env/e1/container/app-x/redeploy" {
			t.Errorf("redeploy path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	out, err := runRoot(t, "", "deploy", "app-x", "--org", "acme", "--project", "p1", "--env", "e1")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(out, "redeployed app-x") {
		t.Fatalf("deploy output: %q", out)
	}
}

func TestDeployRequiresProjectEnv(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	if _, err := runRoot(t, "", "deploy", "app-x", "--org", "acme"); err == nil {
		t.Fatalf("deploy must require --project/--env")
	}
}

func TestDeployRequiresOrg(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	if _, err := runRoot(t, "", "deploy", "app-x", "--project", "p1", "--env", "e1"); err == nil {
		t.Fatalf("deploy must require an org")
	}
}

func TestClustersListCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/org/acme/cluster" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"clusters": []Cluster{
			{DoksClusterID: "c1", Name: "hanzo-acme", Region: "sfo3", Status: "running", Phase: "ready", Active: true, OperatorInstalled: true, BaselineInstalled: true},
		}})
	})
	out, err := runRoot(t, "", "clusters", "list", "--org", "acme")
	if err != nil {
		t.Fatalf("clusters list: %v", err)
	}
	for _, want := range []string{"NAME", "hanzo-acme", "c1", "ready", "yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("clusters list missing %q in:\n%s", want, out)
		}
	}
}

func TestK8sTargetCommand(t *testing.T) {
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/org/acme/cluster/select" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"target": Target{Cluster: "hanzo-k8s", Dedicated: false, Namespaces: map[string]string{"hanzo": "main"}}})
	})
	out, err := runRoot(t, "", "k8s", "target", "--org", "acme")
	if err != nil {
		t.Fatalf("k8s target: %v", err)
	}
	if !strings.Contains(out, "hanzo-k8s") || !strings.Contains(out, "shared") {
		t.Fatalf("k8s target output: %q", out)
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
	withPlatform(t, func(w http.ResponseWriter, r *http.Request) {
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

func strptr(s string) *string { return &s }

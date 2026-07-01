package platform

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNamespaceIsDerivedFromOrgNotInput is the core cross-tenant invariant: the
// physical deploy namespace is a pure function of the validated org, so two
// different orgs ALWAYS map to different namespaces and a caller cannot steer a
// write into another org's namespace (the namespace is never a request input).
func TestNamespaceIsDerivedFromOrgNotInput(t *testing.T) {
	if ns := tenantNamespace("maxpower"); ns != "tenant-maxpower" {
		t.Fatalf("tenant-maxpower expected, got %q", ns)
	}
	if tenantNamespace("maxpower") == tenantNamespace("acme") {
		t.Fatal("distinct orgs must map to distinct namespaces")
	}
	// An empty org never yields a namespace that could collide with a real org.
	if ns := tenantNamespace(""); ns != "tenant-unknown" {
		t.Fatalf("empty org must be tenant-unknown, got %q", ns)
	}
}

// TestSanitizeOrgNeutralizesInjection proves org values that try path traversal,
// namespace escapes, or label injection collapse to a safe DNS-1123 segment, so
// a hostile owner claim cannot forge "tenant-hanzo/../kube-system" style targets.
func TestSanitizeOrgNeutralizesInjection(t *testing.T) {
	cases := map[string]string{
		"MaxPower":            "maxpower",
		"acme/../hanzo":       "acme----hanzo", // each of / . . / → '-', no collapse (matches projectsvc)
		"kube-system":         "kube-system",
		"a b c":               "a-b-c",
		"../../etc":           "etc",
		"org:with:colons":     "org-with-colons",
		"UPPER_SNAKE":         "upper-snake",
		"trailing-dashes---":  "trailing-dashes",
		"  spaced  ":          "spaced",
		"emoji😀here":          "emoji-here",
	}
	for in, want := range cases {
		if got := sanitizeOrg(in); got != want {
			t.Errorf("sanitizeOrg(%q)=%q want %q", in, got, want)
		}
		// The derived namespace must always be a clean tenant-<label>.
		ns := tenantNamespace(in)
		if !strings.HasPrefix(ns, "tenant-") || strings.ContainsAny(ns, "/:. ") {
			t.Errorf("tenantNamespace(%q)=%q not a clean label", in, ns)
		}
	}
}

// TestServiceCRAlwaysPinnedToTenantNamespace proves the rendered operator CR is
// ALWAYS placed in tenant-<org> and stamped with the org label — even if an app
// row somehow carried a stale namespace field, the render uses the passed org.
func TestServiceCRAlwaysPinnedToTenantNamespace(t *testing.T) {
	a := mkApp("maxpower", "proj_1", "api")
	a.Namespace = "tenant-hanzo" // hostile/stale value on the row
	cr := serviceCR(tenantNamespace("maxpower"), "maxpower", "web", a, "ghcr.io/hanzoai/nginx:1")
	meta := cr.Object["metadata"].(map[string]any)
	if meta["namespace"] != "tenant-maxpower" {
		t.Fatalf("CR namespace must be tenant-maxpower, got %v", meta["namespace"])
	}
	labels := meta["labels"].(map[string]any)
	if labels["hanzo.ai/org"] != "maxpower" {
		t.Fatalf("CR must be labeled with the org, got %v", labels["hanzo.ai/org"])
	}
	if meta["name"] != "api" {
		t.Fatalf("CR name must be the app slug, got %v", meta["name"])
	}
}

// TestBuildImageRefBindsToOrg proves the per-tenant build output image path
// includes the org segment, so two orgs building an app with the SAME slug push
// to DIFFERENT repositories (no cross-tenant image overwrite).
func TestBuildImageRefBindsToOrg(t *testing.T) {
	k := &k8sClient{imagePrefix: defaultBuildImagePrefix}
	a := k.buildImageRef("maxpower", "api", "main")
	b := k.buildImageRef("acme", "api", "main")
	if a == b {
		t.Fatalf("distinct orgs must map to distinct image refs: %q == %q", a, b)
	}
	if !strings.Contains(a, "tenant-maxpower-api") {
		t.Fatalf("image ref must bind org+app: %q", a)
	}
}

// TestSecretEnvNeverRenderedIntoCR proves that even if a secret env var reaches
// the store (it is rejected at the handler boundary), the CR renderer drops it —
// a secret value is never written into the operator CR's plaintext .spec.env.
func TestSecretEnvNeverRenderedIntoCR(t *testing.T) {
	env := []EnvVarJSON{
		{Key: "PUBLIC", Value: "ok", Secret: false},
		{Key: "DB_PASSWORD", Value: "hunter2", Secret: true},
	}
	b, _ := json.Marshal(env)
	rendered := envList(string(b))
	if len(rendered) != 1 {
		t.Fatalf("only the non-secret var should render, got %d", len(rendered))
	}
	m := rendered[0].(map[string]any)
	if m["name"] != "PUBLIC" {
		t.Fatalf("expected PUBLIC, got %v", m)
	}
	// The secret value must appear nowhere in the rendered spec.
	if strings.Contains(mustJSON(rendered), "hunter2") {
		t.Fatal("secret value leaked into rendered CR env")
	}
}

// TestAppViewMasksSecretValues proves the API response never echoes a secret
// env value back to a client.
func TestAppViewMasksSecretValues(t *testing.T) {
	a := mkApp("maxpower", "proj_1", "api")
	env := []EnvVarJSON{{Key: "DB_PASSWORD", Value: "hunter2", Secret: true}}
	b, _ := json.Marshal(env)
	a.EnvJSON = string(b)
	v := toAppView(a)
	if len(v.Env) != 1 || v.Env[0].Value != "" {
		t.Fatalf("secret value must be masked in the app view, got %+v", v.Env)
	}
	if strings.Contains(mustJSON(v), "hunter2") {
		t.Fatal("secret value leaked into app view")
	}
}

// TestSplitImageRef checks the repo/tag split used to build the CR image block.
func TestSplitImageRef(t *testing.T) {
	cases := []struct{ in, repo, tag string }{
		{"ghcr.io/hanzoai/nginx:1.2.3", "ghcr.io/hanzoai/nginx", "1.2.3"},
		{"ghcr.io/hanzoai/nginx", "ghcr.io/hanzoai/nginx", "latest"},
		{"registry:5000/app:v1", "registry:5000/app", "v1"},
	}
	for _, c := range cases {
		repo, tag := splitImageRef(c.in)
		if repo != c.repo || tag != c.tag {
			t.Errorf("splitImageRef(%q)=(%q,%q) want (%q,%q)", c.in, repo, tag, c.repo, c.tag)
		}
	}
}

// TestHealthFromStatus checks the honest health rollup (never a fabricated green).
func TestHealthFromStatus(t *testing.T) {
	if h := healthFromStatus(nil); h != "" {
		t.Fatalf("no status must be unknown, got %q", h)
	}
	if h := healthFromStatus(map[string]any{"replicas": int64(2), "readyReplicas": int64(2)}); h != "green" {
		t.Fatalf("ready==desired must be green, got %q", h)
	}
	if h := healthFromStatus(map[string]any{"replicas": int64(2), "readyReplicas": int64(1)}); h != "yellow" {
		t.Fatalf("partial must be yellow, got %q", h)
	}
	if h := healthFromStatus(map[string]any{"replicas": int64(2), "readyReplicas": int64(0)}); h != "red" {
		t.Fatalf("none ready must be red, got %q", h)
	}
	if h := healthFromStatus(map[string]any{"replicas": int64(0)}); h != "yellow" {
		t.Fatalf("scaled-to-zero must be yellow, got %q", h)
	}
}

// TestJobIDSuffixDeterministic proves the BuildKit Job name suffix is a stable
// function of the build ID (the 409-dedupe idempotency key).
func TestJobIDSuffixDeterministic(t *testing.T) {
	if jobIDSuffix("bld_ABCdef1234567890") != jobIDSuffix("bld_ABCdef1234567890") {
		t.Fatal("suffix must be deterministic")
	}
	s := jobIDSuffix("bld_ABCdef1234567890")
	if len(s) > 12 || strings.ContainsAny(s, "_/:. ") {
		t.Fatalf("suffix not a clean short label: %q", s)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

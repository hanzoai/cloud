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

// TestSanitizeOrgIsInjective is the CRIT-2 regression guard: the org normalizer
// (the ONE, shared provisioning.SanitizeOrg, reached through tenantNamespace)
// must be INJECTIVE — distinct raw owners can NEVER collapse onto the same
// tenant namespace. The prior lossy fold mapped "Acme"→"acme" and
// "acme/../hanzo"→"acme----hanzo", so a fold-sibling of a target org (registrable
// because IAM has no org-name shape validator) minted a valid token into the
// victim's namespace. Each pair below folds together under the OLD rule; here
// they MUST stay distinct.
func TestSanitizeOrgIsInjective(t *testing.T) {
	collidingPairs := [][2]string{
		{"Acme", "acme"},             // case fold
		{"team.a", "team-a"},         // '.'→'-' fold
		{"a b c", "a-b-c"},           // space→'-' fold
		{"org:x", "org-x"},           // ':'→'-' fold
		{"acme.hanzo", "acme-hanzo"}, // '.'→'-' fold
		{"UPPER_SNAKE", "upper-snake"},
	}
	for _, p := range collidingPairs {
		nsA, nsB := tenantNamespace(p[0]), tenantNamespace(p[1])
		if nsA == nsB {
			t.Errorf("INJECTIVITY BROKEN: distinct owners %q and %q both map to %q", p[0], p[1], nsA)
		}
	}
	// A clean, already-canonical DNS label is the identity (no gratuitous suffix)
	// so the common case stays human-readable.
	for _, clean := range []string{"maxpower", "acme", "kube-system", "team-a", "org-x"} {
		if got := tenantNamespace(clean); got != "tenant-"+clean {
			t.Errorf("clean label %q must be identity, got %q", clean, got)
		}
	}
	// Every derived namespace is always a clean, injection-free DNS label — a
	// hostile owner can never forge "tenant-hanzo/../kube-system"-style targets.
	for _, hostile := range []string{"acme/../hanzo", "org:with:colons", "../../etc", "emoji😀here", "  spaced  ", "trailing---"} {
		ns := tenantNamespace(hostile)
		if !strings.HasPrefix(ns, "tenant-") || strings.ContainsAny(ns, "/:. ") {
			t.Errorf("tenantNamespace(%q)=%q not a clean label", hostile, ns)
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
	// org + app are SEPARATE '/'-joined path components (injective), e.g.
	// ghcr.io/hanzoai/tenant-maxpower/api:main.
	if !strings.Contains(a, "tenant-maxpower/api") {
		t.Fatalf("image ref must bind org+app in separate path components: %q", a)
	}
}

// TestBuildImageRefIsInjective is the CRIT-2 image-ref regression guard: the
// (org,app)→image mapping must be injective. The prior single-component join
// "tenant-<org>-<app>" was AMBIGUOUS — (org="a-b",app="c") and (org="a",
// app="b-c") both rendered "tenant-a-b-c", letting one tenant push to another's
// image. With org+app in separate '/'-components (neither slug can contain '/'),
// the ambiguous pair MUST now map to distinct refs.
func TestBuildImageRefIsInjective(t *testing.T) {
	k := &k8sClient{imagePrefix: defaultBuildImagePrefix}
	x := k.buildImageRef("a-b", "c", "main")
	y := k.buildImageRef("a", "b-c", "main")
	if x == y {
		t.Fatalf("AMBIGUOUS image ref: (a-b,c) and (a,b-c) both map to %q", x)
	}
	// A case/shape fold-sibling of an org (registrable, no IAM shape check) must
	// not steer a build to the victim's repository.
	if k.buildImageRef("Acme", "api", "main") == k.buildImageRef("acme", "api", "main") {
		t.Fatal("fold-sibling orgs must map to distinct image refs")
	}
}

// TestSecretEnvNeverRenderedIntoCR proves the CR renderer emits a secret env var
// as a Kubernetes valueFrom.secretKeyRef (never an inline plaintext value): the
// secret's VALUE appears nowhere in the rendered spec, and the secret entry carries
// only valueFrom (no `value`), the shape the hanzo operator requires.
func TestSecretEnvNeverRenderedIntoCR(t *testing.T) {
	env := []EnvVarJSON{
		{Key: "PUBLIC", Value: "ok", Secret: false},
		{Key: "DB_PASSWORD", Value: "hunter2", Secret: true},
	}
	b, _ := json.Marshal(env)
	rendered := renderEnv(managedSecretName("api"), string(b))
	if len(rendered) != 2 {
		t.Fatalf("both vars should render (plain + secretKeyRef), got %d", len(rendered))
	}
	// The secret value must appear nowhere in the rendered spec.
	if strings.Contains(mustJSON(rendered), "hunter2") {
		t.Fatal("secret value leaked into rendered CR env")
	}
	// Locate the secret entry and assert its shape: valueFrom.secretKeyRef, no value.
	var secretEntry map[string]any
	for _, e := range rendered {
		m := e.(map[string]any)
		if m["name"] == "DB_PASSWORD" {
			secretEntry = m
		}
	}
	if secretEntry == nil {
		t.Fatal("DB_PASSWORD secret env not rendered")
	}
	if _, hasValue := secretEntry["value"]; hasValue {
		t.Fatal("secret env must NOT carry an inline value (operator rejects value+valueFrom)")
	}
	vf, ok := secretEntry["valueFrom"].(map[string]any)
	if !ok {
		t.Fatal("secret env must carry valueFrom")
	}
	skr, ok := vf["secretKeyRef"].(map[string]any)
	if !ok {
		t.Fatal("secret env valueFrom must carry secretKeyRef")
	}
	if skr["name"] != managedSecretName("api") || skr["key"] != "DB_PASSWORD" {
		t.Fatalf("secretKeyRef must point at the managed Secret/key, got %v", skr)
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

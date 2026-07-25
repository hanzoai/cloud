package paas

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestIsSemverTag is the exact semver policy from apps-drift.ts SEMVER_TAG:
// strictly vMAJOR.MINOR.PATCH; everything else is floating.
func TestIsSemverTag(t *testing.T) {
	valid := []string{"v1.0.0", "v1.28.16", "v0.3.0", "v10.20.30", "v4.4.4"}
	invalid := []string{
		"", "1.0.0", "v1.0", "v1", "latest", "main", "dev", "edge",
		"sha-08d2dea-amd64", "1.42.33-billing", "v1.0.0-rc1", "vX.Y.Z",
		"e19980422d342f40b8ba3142e6bbba54a076fc7f", "nolux-hanzo4",
	}
	for _, v := range valid {
		if !IsSemverTag(v) {
			t.Errorf("expected %q to be a semver tag", v)
		}
	}
	for _, v := range invalid {
		if IsSemverTag(v) {
			t.Errorf("expected %q to NOT be a semver tag", v)
		}
	}
}

// TestComputeDrift ports the apps-drift.ts contract cases 1:1 — the two
// implementations must agree exactly.
func TestComputeDrift(t *testing.T) {
	cases := []struct {
		name      string
		obs       Observed
		wantSev   DriftSeverity
		wantKinds []DriftKind
	}{
		{
			// Clean: declared==running==latest, released with assets.
			name:      "fully clean",
			obs:       Observed{DeclaredTag: "v1.2.0", RunningTag: "v1.2.0", LatestTag: "v1.2.0", ReleaseURL: "https://x/rel", ReleaseAssets: 3},
			wantSev:   SeverityOK,
			wantKinds: nil,
		},
		{
			// declared behind latest → stale (yellow), plus running rolled to declared.
			name:      "stale only",
			obs:       Observed{DeclaredTag: "v1.2.0", RunningTag: "v1.2.0", LatestTag: "v1.3.0", ReleaseURL: "https://x/rel", ReleaseAssets: 1},
			wantSev:   SeverityYellow,
			wantKinds: []DriftKind{DriftStale},
		},
		{
			// running != declared → un-rolled (yellow).
			name:      "un-rolled only",
			obs:       Observed{DeclaredTag: "v1.3.0", RunningTag: "v1.2.0", LatestTag: "v1.3.0", ReleaseURL: "https://x/rel", ReleaseAssets: 1},
			wantSev:   SeverityYellow,
			wantKinds: []DriftKind{DriftUnrolled},
		},
		{
			// floating declared → red; stale suppressed even though latest set.
			name:      "floating declared suppresses stale",
			obs:       Observed{DeclaredTag: "sha-08d2dea", RunningTag: "sha-08d2dea", LatestTag: "v1.3.0", ReleaseURL: "https://x/rel", ReleaseAssets: 1},
			wantSev:   SeverityRed,
			wantKinds: []DriftKind{DriftFloatingDeclared, DriftFloatingRunning},
		},
		{
			// floating running only (declared is clean semver, matches latest).
			name:      "floating running",
			obs:       Observed{DeclaredTag: "v1.3.0", RunningTag: "main", LatestTag: "v1.3.0", ReleaseURL: "https://x/rel", ReleaseAssets: 1},
			wantSev:   SeverityRed,
			wantKinds: []DriftKind{DriftFloatingRunning},
		},
		{
			// declared semver but no GH release → no-release (red). This is the
			// "all-red" state the live fleet shows today.
			name:      "no release",
			obs:       Observed{DeclaredTag: "v1.2.0", RunningTag: "v1.2.0"},
			wantSev:   SeverityRed,
			wantKinds: []DriftKind{DriftNoRelease},
		},
		{
			// release exists but 0 assets → zero-assets (red) — the iam class.
			name:      "zero assets",
			obs:       Observed{DeclaredTag: "v1.28.16", RunningTag: "v1.28.16", LatestTag: "v1.28.16", ReleaseURL: "https://x/rel", ReleaseAssets: 0},
			wantSev:   SeverityRed,
			wantKinds: []DriftKind{DriftZeroAssets},
		},
		{
			// Multiple at once: floating running + stale + no-release.
			name:      "compound",
			obs:       Observed{DeclaredTag: "v1.2.0", RunningTag: "sha-x", LatestTag: "v1.3.0"},
			wantSev:   SeverityRed,
			wantKinds: []DriftKind{DriftFloatingRunning, DriftStale, DriftNoRelease},
		},
		{
			// No declared tag at all → no flags (nothing to compare).
			name:      "no declared tag",
			obs:       Observed{RunningTag: "v1.2.0"},
			wantSev:   SeverityOK,
			wantKinds: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeDrift(tc.obs)
			if got.Severity != tc.wantSev {
				t.Errorf("severity = %q, want %q (flags=%v)", got.Severity, tc.wantSev, kinds(got.Flags))
			}
			if !reflect.DeepEqual(kinds(got.Flags), tc.wantKinds) {
				t.Errorf("kinds = %v, want %v", kinds(got.Flags), tc.wantKinds)
			}
			// Flags must never be nil in the verdict (JSON `[]`, never `null`).
			if got.Flags == nil {
				t.Errorf("Drift.Flags must be non-nil")
			}
		})
	}
}

func kinds(flags []DriftFlag) []DriftKind {
	if len(flags) == 0 {
		return nil
	}
	out := make([]DriftKind, 0, len(flags))
	for _, f := range flags {
		out = append(out, f.Kind)
	}
	return out
}

// TestOrgFromRepository ports inventory.ts orgFromRepository cases.
func TestOrgFromRepository(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/hanzoai/chat":                           "hanzoai",
		"ghcr.io/hanzoai/insights/capture":               "hanzoai",
		"docker.io/grafana/grafana":                      "grafana",
		"docker.io/otel/opentelemetry-collector-contrib": "otel",
		"hanzoai/iam":                                    "hanzoai", // no registry host
		"bareimage":                                      "bareimage",
	}
	for repo, want := range cases {
		if got := orgFromRepository(repo); got != want {
			t.Errorf("orgFromRepository(%q) = %q, want %q", repo, got, want)
		}
	}
}

// TestRepoFromRepository ports inventory.ts repoFromRepository cases.
func TestRepoFromRepository(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/hanzoai/chat":             "hanzoai/chat",
		"ghcr.io/hanzoai/insights/capture": "hanzoai/insights/capture",
		"docker.io/grafana/grafana":        "grafana/grafana",
		"hanzoai/iam":                      "hanzoai/iam",
		"bareimage":                        "bareimage",
	}
	for repo, want := range cases {
		if got := repoFromRepository(repo); got != want {
			t.Errorf("repoFromRepository(%q) = %q, want %q", repo, got, want)
		}
	}
}

// TestHealthFromStatus mirrors inventory.ts healthFromDeployment semantics but
// off the operator-reconciled Service status.
func TestHealthFromStatus(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]any
		want   string
	}{
		{"all ready", map[string]any{"replicas": int64(2), "readyReplicas": int64(2)}, "green"},
		{"partial", map[string]any{"replicas": int64(3), "readyReplicas": int64(1)}, "yellow"},
		{"none ready", map[string]any{"replicas": int64(2), "readyReplicas": int64(0)}, "red"},
		{"scaled to zero", map[string]any{"replicas": int64(0), "readyReplicas": int64(0)}, "yellow"},
		{"available fallback ok", map[string]any{"availableReplicas": int64(2)}, "green"},
		{"available fallback zero", map[string]any{"availableReplicas": int64(0)}, "red"},
		{"no signal", map[string]any{"phase": "Pending"}, ""},
		{"nil status", nil, ""},
		{"float decode", map[string]any{"replicas": float64(2), "readyReplicas": float64(2)}, "green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthFromStatus(tc.status); got != tc.want {
				t.Errorf("healthFromStatus(%v) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestObserveCR proves the CR→AppView mapping end to end (declared tag, org/repo
// derivation, health, endpoints, drift) on a real-shaped Service CR.
func TestObserveCR(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": "pricing"},
		"spec": map[string]any{
			"image": map[string]any{"repository": "ghcr.io/hanzoai/pricing", "tag": "v1.1.2"},
		},
		"status": map[string]any{
			"phase":         "Running",
			"replicas":      int64(2),
			"readyReplicas": int64(2),
			"endpoints":     []any{"https://pricing.hanzo.ai"},
		},
	}}
	v := observeCR(obj, "hanzo", "main", "v1.1.2")
	if v.ID != "hanzoai/pricing/main" {
		t.Errorf("ID = %q, want hanzoai/pricing/main", v.ID)
	}
	if v.Org != "hanzoai" || v.App != "pricing" || v.Env != "main" {
		t.Errorf("org/app/env = %q/%q/%q", v.Org, v.App, v.Env)
	}
	if v.Repo != "hanzoai/pricing" || v.Registry != "ghcr.io/hanzoai/pricing" {
		t.Errorf("repo/registry = %q/%q", v.Repo, v.Registry)
	}
	if v.DeclaredTag != "v1.1.2" {
		t.Errorf("declaredTag = %q, want v1.1.2", v.DeclaredTag)
	}
	if v.Health != "green" || v.Phase != "Running" {
		t.Errorf("health/phase = %q/%q, want green/Running", v.Health, v.Phase)
	}
	if v.RunningTag != "v1.1.2" {
		t.Errorf("runningTag = %q, want v1.1.2", v.RunningTag)
	}
	if !reflect.DeepEqual(v.Endpoints, []string{"https://pricing.hanzo.ai"}) {
		t.Errorf("endpoints = %v", v.Endpoints)
	}
	// pricing v1.1.2 declared==running, semver, but no GH release wired yet →
	// no-release (red). No un-rolled flag (running matches declared).
	if v.Drift.Severity != SeverityRed || len(v.Drift.Flags) != 1 || v.Drift.Flags[0].Kind != DriftNoRelease {
		t.Errorf("drift = %+v, want single no-release red", v.Drift)
	}
}

// TestObserveCRFloating proves a floating declared tag (the commerce/cloud class)
// is flagged red.
func TestObserveCRFloating(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "billing"},
		"spec":     map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/billing", "tag": "sha-08d2dea-amd64"}},
		"status":   map[string]any{"phase": "Running", "replicas": int64(1), "readyReplicas": int64(1)},
	}}
	v := observeCR(obj, "hanzo", "main", "sha-08d2dea-amd64")
	if v.Drift.Severity != SeverityRed {
		t.Fatalf("expected red for floating declared, got %q", v.Drift.Severity)
	}
	if kinds(v.Drift.Flags)[0] != DriftFloatingDeclared {
		t.Errorf("expected floating-declared first, got %v", kinds(v.Drift.Flags))
	}
}

// TestAppNameRE + imageRepoRE are the boundary injection guards for the CR name
// and deploy image repo.
func TestAppNameRE(t *testing.T) {
	valid := []string{"iam", "cloud", "commerce-admin", "insights-kv", "world-gw", "a"}
	invalid := []string{"", "-iam", "iam-", "IAM", "i am", "iam/x", "iam.x", "iam_x"}
	for _, v := range valid {
		if !appNameRE.MatchString(v) {
			t.Errorf("expected %q valid app name", v)
		}
	}
	for _, v := range invalid {
		if appNameRE.MatchString(v) {
			t.Errorf("expected %q invalid app name", v)
		}
	}
}

func TestImageRepoRE(t *testing.T) {
	valid := []string{"ghcr.io/hanzoai/iam", "docker.io/grafana/grafana", "ghcr.io/hanzoai/insights/capture"}
	invalid := []string{"", "ghcr.io/hanzoai/iam ", " ghcr.io/x", "GHCR.io/x", "ghcr.io/hanzoai/iam:tag"}
	for _, v := range valid {
		if !imageRepoRE.MatchString(v) {
			t.Errorf("expected %q valid repo", v)
		}
	}
	for _, v := range invalid {
		if imageRepoRE.MatchString(v) {
			t.Errorf("expected %q invalid repo", v)
		}
	}
}

// TestDeploymentsGVR pins the Deployment GVR (the running-tag source).
func TestDeploymentsGVR(t *testing.T) {
	want := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	if deploymentsGVR != want {
		t.Fatalf("deploymentsGVR = %v, want %v", deploymentsGVR, want)
	}
}

// TestImageRefSplit proves repo/tag extraction across tag, digest, host:port, and
// bare forms — the running-tag parse (inventory.ts parseImageRef).
func TestImageRefSplit(t *testing.T) {
	cases := []struct {
		ref      string
		wantRepo string
		wantTag  string
	}{
		{"ghcr.io/hanzoai/iam:v1.28.16", "ghcr.io/hanzoai/iam", "v1.28.16"},
		{"ghcr.io/hanzoai/cloud:1.785.32", "ghcr.io/hanzoai/cloud", "1.785.32"},
		{"docker.io/otel/opentelemetry-collector-contrib:0.154.0", "docker.io/otel/opentelemetry-collector-contrib", "0.154.0"},
		{"registry:5000/hanzoai/iam:v1", "registry:5000/hanzoai/iam", "v1"}, // host:port must not be read as tag
		{"ghcr.io/hanzoai/iam@sha256:abc123", "ghcr.io/hanzoai/iam", "sha256:abc123"},
		{"ghcr.io/hanzoai/iam", "ghcr.io/hanzoai/iam", ""}, // no tag
	}
	for _, tc := range cases {
		if got := repoFromImageRef(tc.ref); got != tc.wantRepo {
			t.Errorf("repoFromImageRef(%q) = %q, want %q", tc.ref, got, tc.wantRepo)
		}
		if got := tagFromImageRef(tc.ref); got != tc.wantTag {
			t.Errorf("tagFromImageRef(%q) = %q, want %q", tc.ref, got, tc.wantTag)
		}
	}
}

// TestRunningTagFromDeployment proves the container match ignores sidecars and
// falls back to the first container (inventory.ts runningTagFromDeployment).
func TestRunningTagFromDeployment(t *testing.T) {
	dep := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "replicate", "image": "ghcr.io/hanzoai/replicate:v9"}, // sidecar first
			map[string]any{"name": "app", "image": "ghcr.io/hanzoai/iam:v1.28.16"},
		}}}},
	}}
	// Exact repo match picks the app container, not the sidecar.
	if got := runningTagFromDeployment(dep, "ghcr.io/hanzoai/iam"); got != "v1.28.16" {
		t.Errorf("matched tag = %q, want v1.28.16 (must skip sidecar)", got)
	}
	// Unknown repo → first container fallback.
	if got := runningTagFromDeployment(dep, "ghcr.io/hanzoai/unknown"); got != "v9" {
		t.Errorf("fallback tag = %q, want v9 (first container)", got)
	}
	// No containers → empty.
	empty := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}
	if got := runningTagFromDeployment(empty, "x"); got != "" {
		t.Errorf("no-containers tag = %q, want empty", got)
	}
}

// TestObserveCRUnrolled proves the un-rolled flag fires when the running tag lags
// the declared tag — the core value of the Deployment join.
func TestObserveCRUnrolled(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "iam"},
		"spec":     map[string]any{"image": map[string]any{"repository": "ghcr.io/hanzoai/iam", "tag": "v1.28.16"}},
		"status":   map[string]any{"phase": "Running", "replicas": int64(2), "readyReplicas": int64(2)},
	}}
	// declared v1.28.16, running v1.28.15 → un-rolled (yellow) + no-release (red).
	v := observeCR(obj, "hanzo", "main", "v1.28.15")
	if v.Drift.Severity != SeverityRed {
		t.Fatalf("severity = %q, want red (no-release dominates)", v.Drift.Severity)
	}
	ks := kinds(v.Drift.Flags)
	hasUnrolled := false
	for _, k := range ks {
		if k == DriftUnrolled {
			hasUnrolled = true
		}
	}
	if !hasUnrolled {
		t.Errorf("expected un-rolled flag for running!=declared, got %v", ks)
	}
}

// TestScanOrder pins production-first namespace ordering (a bare deploy targets
// main).
func TestScanOrder(t *testing.T) {
	got := scanOrder()
	want := []string{"hanzo", "hanzo-mainnet", "hanzo-testnet", "hanzo-devnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanOrder = %v, want %v", got, want)
	}
	// The invariant that matters: every scanned namespace MUST classify, and must
	// classify to a real tenant. A scanned-but-unclassified namespace would render
	// rows with an empty tenant — rows no OrgAdmin could ever be confined to.
	for _, ns := range got {
		tenant, env, ok := nsClass(ns)
		if !ok || tenant == "" || env == "" {
			t.Errorf("scanOrder namespace %q does not classify: tenant=%q env=%q ok=%v", ns, tenant, env, ok)
		}
	}
}

// TestNsClassIsTotalAndConfining pins the classifier's two load-bearing properties:
// it is TOTAL (every input decided, never a panic) and it CONFINES (anything it does
// not recognise is classified out, so the reader can never reach beyond the platform
// tier, and a tenant namespace authorizes to its own org rather than to "hanzo").
func TestNsClassIsTotalAndConfining(t *testing.T) {
	for _, tc := range []struct {
		ns, tenant, env string
		ok              bool
	}{
		{"hanzo", "hanzo", "main", true},
		{"hanzo-mainnet", "hanzo", "main", true},
		{"hanzo-testnet", "hanzo", "test", true},
		{"hanzo-devnet", "hanzo", "dev", true},
		{"tenant-maxpower", "maxpower", "main", true}, // authorizes to maxpower, NOT hanzo
		{"tenant-hanzo", "hanzo", "main", true},
		{"tenant-", "", "", false},     // empty tenant is not a tenant
		{"kube-system", "", "", false}, // never ours
		{"default", "", "", false},
		{"", "", "", false},
		{"hanzo-evil", "", "", false}, // a look-alike suffix is not a lifecycle env
	} {
		tenant, env, ok := nsClass(tc.ns)
		if tenant != tc.tenant || env != tc.env || ok != tc.ok {
			t.Errorf("nsClass(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.ns, tenant, env, ok, tc.tenant, tc.env, tc.ok)
		}
	}
}

// TestDiscoverNamespacesAddsTenantsAndNeverBlanks pins the two properties that make
// discovery safe. It only ever ADDS to the first-party set — an empty or failed
// listing degrades to today's behavior rather than blanking the board (the bug this
// test was written for) — and a namespace nsClass does not recognise can never enter
// the scan set, so the reader still cannot reach beyond the platform tier.
func TestDiscoverNamespacesAddsTenantsAndNeverBlanks(t *testing.T) {
	ns := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": name},
		}}
	}
	// no namespaces visible at all → must still be the full first-party set
	if got := discoverNamespaces(fakeService(), t.Context()); !reflect.DeepEqual(got, scanOrder()) {
		t.Errorf("empty discovery = %v, want first-party %v (must never blank)", got, scanOrder())
	}
	got := discoverNamespaces(fakeService(
		ns("tenant-maxpower"), ns("tenant-hanzo"),
		ns("kube-system"), ns("default"), // never ours
	), t.Context())
	want := append(append([]string(nil), scanOrder()...), "tenant-hanzo", "tenant-maxpower")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverNamespaces = %v, want %v", got, want)
	}
	for _, n := range got {
		if _, _, ok := nsClass(n); !ok {
			t.Errorf("unclassified namespace %q entered the scan set", n)
		}
	}
}

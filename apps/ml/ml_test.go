package ml

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTenantNS(t *testing.T) {
	// A max-length org (42, orgRE) + a 20-char project composes to
	// len("ml-")+42+len("-")+20 = 66 > 63 — proving the composed-length guard fires
	// even though each component is individually valid.
	longOrg := strings.Repeat("a", 42)     // valid orgRE (max)
	longProject := strings.Repeat("p", 20) // valid projectRE; overflows only when combined
	cases := []struct {
		name        string
		org         string
		project     string
		admin       bool
		wantNS      string
		wantOrg     string
		wantProject string
		wantError   bool
	}{
		// Default project (empty or literal "default") keeps the legacy per-org namespace.
		{"plain org", "acme", "", false, "ml-acme", "acme", "default", false},
		{"uppercase is lowered", "ACME", "", false, "ml-acme", "acme", "default", false},
		{"whitespace trimmed", "  acme  ", "", false, "ml-acme", "acme", "default", false},
		{"hyphenated org", "acme-corp", "", false, "ml-acme-corp", "acme-corp", "default", false},
		{"literal default project", "acme", "default", false, "ml-acme", "acme", "default", false},
		{"empty + admin -> admin bucket", "", "", true, "ml-admin", "admin", "default", false},
		// Non-default project appends the "-"<project> suffix.
		{"non-default project suffixes ns", "acme", "research", false, "ml-acme-research", "acme", "research", false},
		{"project lowered + trimmed", "acme", "  Research  ", false, "ml-acme-research", "acme", "research", false},
		{"admin bucket + project", "", "ops", true, "ml-admin-ops", "admin", "ops", false},
		// Rejections.
		{"empty + non-admin -> 403", "", "", false, "", "", "", true},
		{"underscore org rejected", "acme_corp", "", false, "", "", "", true},
		{"bang org rejected", "acme!", "", false, "", "", "", true},
		{"leading hyphen org rejected", "-acme", "", false, "", "", "", true},
		{"trailing hyphen org rejected", "acme-", "", false, "", "", "", true},
		{"underscore project rejected", "acme", "my_proj", false, "", "", "", true},
		{"opaque id project rejected", "acme", "proj_ab12", false, "", "", "", true},
		{"trailing hyphen project rejected", "acme", "proj-", false, "", "", "", true},
		{"org+project over 63 chars rejected", longOrg, longProject, false, "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns, org, project, err := tenantNS(tc.org, tc.project, tc.admin)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error for org=%q project=%q admin=%v", tc.org, tc.project, tc.admin)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ns != tc.wantNS || org != tc.wantOrg || project != tc.wantProject {
				t.Fatalf("tenantNS(%q,%q,%v) = (%q,%q,%q), want (%q,%q,%q)",
					tc.org, tc.project, tc.admin, ns, org, project, tc.wantNS, tc.wantOrg, tc.wantProject)
			}
			if len(ns) > 63 {
				t.Fatalf("namespace %q exceeds the 63-char DNS-1123 label limit", ns)
			}
		})
	}
}

// TestTenantNSInjective is the cross-tenant fold guard: distinct org slugs must map
// to distinct namespaces, AND distinct projects within an org must too (no lossy
// sanitize collapsing two tenants/projects onto one namespace = cross-tenant access).
func TestTenantNSInjective(t *testing.T) {
	a, _, _, errA := tenantNS("a-b", "", false)
	b, _, _, errB := tenantNS("a--b", "", false)
	if errA != nil || errB != nil {
		t.Fatalf("both should be valid: %v %v", errA, errB)
	}
	if a == b {
		t.Fatalf("distinct orgs folded to one namespace: %q", a)
	}
	// Distinct projects under one org must not fold either.
	p1, _, _, err1 := tenantNS("acme", "a-b", false)
	p2, _, _, err2 := tenantNS("acme", "a--b", false)
	if err1 != nil || err2 != nil {
		t.Fatalf("both projects should be valid: %v %v", err1, err2)
	}
	if p1 == p2 {
		t.Fatalf("distinct projects folded to one namespace: %q", p1)
	}
	// A non-default project must never collide with the default per-org namespace.
	def, _, _, _ := tenantNS("acme", "", false)
	if p1 == def || p2 == def {
		t.Fatalf("project namespace collided with the default per-org namespace %q", def)
	}
}

func TestNameRE(t *testing.T) {
	valid := []string{"a", "foo", "foo-bar", "model-1", "abc123", "a-b-c-d"}
	invalid := []string{"", "-foo", "foo-", "Foo", "foo_bar", "a.b", "foo bar",
		"this-name-is-way-too-long-to-be-a-valid-dns-1123-label-because-it-exceeds-the-sixty-three-character-limit"}
	for _, v := range valid {
		if !nameRE.MatchString(v) {
			t.Errorf("expected %q valid", v)
		}
	}
	for _, v := range invalid {
		if nameRE.MatchString(v) {
			t.Errorf("expected %q invalid", v)
		}
	}
}

// TestLabelsForTenantBoundary proves a caller cannot override the tenant org
// label or the managed-by marker via user-supplied labels, and that the project
// label is stamped for a non-default project (non-overridable) but absent for the
// default one (backward-compatible single-project shape).
func TestLabelsForTenantBoundary(t *testing.T) {
	// Default project: org + managed-by non-overridable; NO project label stamped.
	got := labelsFor("acme", "default", map[string]string{
		"team":         "ml",
		orgLabel:       "evil-tenant", // attempted override
		managedByLabel: "attacker",    // attempted override
	})
	if got[orgLabel] != "acme" {
		t.Fatalf("org label override leaked: %v", got[orgLabel])
	}
	if got[managedByLabel] != managedByValue {
		t.Fatalf("managed-by override leaked: %v", got[managedByLabel])
	}
	if got["team"] != "ml" {
		t.Fatalf("benign user label dropped: %v", got["team"])
	}
	if _, ok := got[projectLabel]; ok {
		t.Fatalf("default project must stamp no project label, got %v", got[projectLabel])
	}

	// Non-default project: the project label is stamped and non-overridable.
	got = labelsFor("acme", "research", map[string]string{
		projectLabel: "evil-project", // attempted override
	})
	if got[projectLabel] != "research" {
		t.Fatalf("project label override leaked: %v", got[projectLabel])
	}
	if got[orgLabel] != "acme" {
		t.Fatalf("org label wrong for non-default project: %v", got[orgLabel])
	}
}

func TestView(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1beta1",
		"kind":       "InferenceService",
		"metadata":   map[string]any{"name": "m1", "namespace": "ml-acme"},
		"spec":       map[string]any{"predictor": map[string]any{"model": map[string]any{"runtime": "x"}}},
		"status":     map[string]any{"url": "http://m1.ml-acme.svc.cluster.local"},
	}}

	noSpec := view(obj, false)
	if noSpec["name"] != "m1" {
		t.Fatalf("name = %v", noSpec["name"])
	}
	if _, ok := noSpec["status"]; !ok {
		t.Fatal("status must be present in list view")
	}
	if _, ok := noSpec["spec"]; ok {
		t.Fatal("spec must NOT be present in list view")
	}
	if _, ok := noSpec["createdAt"]; !ok {
		t.Fatal("createdAt must be present")
	}
	// Namespace is an internal tenant detail and must never be echoed.
	if _, ok := noSpec["namespace"]; ok {
		t.Fatal("namespace must not be echoed")
	}

	withSpec := view(obj, true)
	if _, ok := withSpec["spec"]; !ok {
		t.Fatal("spec must be present in single-object view")
	}
}

func TestInternalURL(t *testing.T) {
	// address.url (internal) wins over url (external).
	both := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"url":     "http://external.example.com",
		"address": map[string]any{"url": "http://m1.ml-acme.svc.cluster.local"},
	}}}
	if got := internalURL(both); got != "http://m1.ml-acme.svc.cluster.local" {
		t.Fatalf("address.url should win, got %q", got)
	}
	// falls back to url.
	only := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"url": "http://external.example.com",
	}}}
	if got := internalURL(only); got != "http://external.example.com" {
		t.Fatalf("should fall back to url, got %q", got)
	}
	// empty when not ready.
	none := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{}}}
	if got := internalURL(none); got != "" {
		t.Fatalf("unready model should have no address, got %q", got)
	}
}

// TestGVRs pins the wire identity of every managed resource — a typo here
// silently breaks every call against that CRD.
func TestGVRs(t *testing.T) {
	cases := []struct {
		name            string
		group, ver, res string
		got             [3]string
	}{
		{"InferenceService", "serving.kserve.io", "v1beta1", "inferenceservices",
			[3]string{isvcGVR.Group, isvcGVR.Version, isvcGVR.Resource}},
		{"TrainJob", "trainer.kubeflow.org", "v1alpha1", "trainjobs",
			[3]string{trainjobGVR.Group, trainjobGVR.Version, trainjobGVR.Resource}},
		{"Experiment", "kubeflow.org", "v1beta1", "experiments",
			[3]string{experimentGVR.Group, experimentGVR.Version, experimentGVR.Resource}},
		{"Trial", "kubeflow.org", "v1beta1", "trials",
			[3]string{trialGVR.Group, trialGVR.Version, trialGVR.Resource}},
	}
	for _, tc := range cases {
		want := [3]string{tc.group, tc.ver, tc.res}
		if tc.got != want {
			t.Errorf("%s GVR = %v, want %v", tc.name, tc.got, want)
		}
	}
	// create kinds must carry apiVersion = group/version and the right kind.
	if modelKind.apiVersion != "serving.kserve.io/v1beta1" || modelKind.kind != "InferenceService" {
		t.Errorf("modelKind wrong: %+v", modelKind)
	}
	if jobKind.apiVersion != "trainer.kubeflow.org/v1alpha1" || jobKind.kind != "TrainJob" {
		t.Errorf("jobKind wrong: %+v", jobKind)
	}
	if expKind.apiVersion != "kubeflow.org/v1beta1" || expKind.kind != "Experiment" {
		t.Errorf("expKind wrong: %+v", expKind)
	}
}

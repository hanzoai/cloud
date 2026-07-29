package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// yamlMarshal renders an object back to canonical YAML for comparison. Map keys
// sort, so two renders of the same object produce identical bytes regardless of
// how each arrived.
func yamlMarshal(v any) ([]byte, error) { return sigsyaml.Marshal(v) }

// realChartDir is the hanzo/app chart this renderer exists to serve. The test
// skips when it is not checked out beside cloud, so CI without the universe repo
// is quiet rather than red.
const realChartDir = "../../../universe/charts/app"

// TestRenderRealChartMatchesHelm renders the actual fleet chart with an actual
// values file and asserts the result is IDENTICAL to what the helm binary
// produces from the same inputs.
//
// This is the only assertion that really matters. Rendering in-process is a
// bet that Helm's engine, driven directly, agrees with Helm's CLI — and the
// consequence of being subtly wrong is not a failed test but a fleet deployed
// from manifests nobody reviewed. Comparing against helm itself is what makes
// the bet checkable instead of assumed.
func TestRenderRealChartMatchesHelm(t *testing.T) {
	dir, err := filepath.Abs(realChartDir)
	if err != nil || !exists(filepath.Join(dir, "Chart.yaml")) {
		t.Skipf("hanzo/app chart not checked out at %s", realChartDir)
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm binary not installed; nothing to compare against")
	}

	valuesPath := filepath.Join(dir, "values", "hanzo", "www.yaml")
	if !exists(valuesPath) {
		t.Skipf("no values file at %s", valuesPath)
	}
	valuesRaw, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatal(err)
	}

	// --- ours: from bytes, no filesystem ---
	files, err := readTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := chartValues(valuesPath, valuesRaw)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := renderChart(files, "www", "hanzo", vals)
	if err != nil {
		t.Fatalf("renderChart: %v", err)
	}

	// --- helm's own answer ---
	out, err := exec.Command(helm, "template", "www", dir, "-n", "hanzo", "-f", valuesPath).Output()
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	theirs, err := parseManifest("helm-template", out)
	if err != nil {
		t.Fatal(err)
	}

	got, want := digest(t, mine), digest(t, filterEmpty(theirs))
	if len(got) != len(want) {
		t.Fatalf("object count: ours %d, helm %d\n ours: %v\n helm: %v", len(got), len(want), keys(got), keys(want))
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Fatalf("helm rendered %s and we did not", k)
		}
		if g != w {
			t.Fatalf("%s differs from helm's render:\n--- ours ---\n%s\n--- helm ---\n%s", k, g, w)
		}
	}
	if len(want) == 0 {
		t.Fatal("comparison rendered nothing; the test would pass on any renderer")
	}
	t.Logf("matched helm on %d objects", len(want))
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// readTree loads a chart directory as the bytes map the renderer takes, which is
// what a tree read from git hands over.
func readTree(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		// Everything under values/ is input to a release, not part of the chart.
		if strings.HasPrefix(rel, "values/") || strings.HasPrefix(rel, "values-review/") || strings.HasPrefix(rel, "hack/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out, err
}

func filterEmpty(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
	out := objs[:0]
	for _, o := range objs {
		if o != nil && o.GetKind() != "" {
			out = append(out, o)
		}
	}
	return out
}

// digest keys each object by kind/namespace/name and renders it back to sorted
// YAML, so the comparison is about content and not about ordering.
func digest(t *testing.T, objs []*unstructured.Unstructured) map[string]string {
	t.Helper()
	out := make(map[string]string, len(objs))
	for _, o := range objs {
		key := o.GetKind() + "/" + o.GetNamespace() + "/" + o.GetName()
		b, err := yamlMarshal(o.UnstructuredContent())
		if err != nil {
			t.Fatal(err)
		}
		out[key] = string(b)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

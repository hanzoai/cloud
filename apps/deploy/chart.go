package deploy

import (
	"fmt"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/yaml"
)

// chart.go — render a Helm chart to objects, from bytes.
//
// The fleet is ONE chart and one values file per service, so delivery has to
// render rather than just parse. It does that in-process with Helm's own engine
// rather than shelling `helm template`, for the same reason the inventory is a
// tree read: a subprocess would need the chart written to a writable POSIX
// directory on every reconcile, which is the disk dependency the whole native
// path exists to remove. loader.LoadFiles takes the bytes we already have.
//
// Using Helm's engine and not a template pass of our own is deliberate. A chart
// is not just Go templates: it is values coalescing, the .Chart/.Release/
// .Capabilities built-ins, `required`, `tpl`, subchart scoping, and the sprig
// set. Reimplementing a subset would render most charts correctly and a few
// silently differently, which is the worst outcome for something that decides
// what runs.

// renderChart renders chartFiles as one release and returns its objects.
//
// chartFiles is repo-relative and MUST be rooted at the chart directory —
// "Chart.yaml", "templates/deployment.yaml" — because that is what Helm's
// loader expects and what makes a chart relocatable.
//
// Values are the fully-resolved values for this release; the chart's own
// values.yaml is coalesced underneath by Helm, so a caller passes only the
// difference, exactly as `-f` does.
func renderChart(chartFiles map[string][]byte, release, namespace string, values map[string]any) ([]*unstructured.Unstructured, error) {
	if release == "" {
		return nil, fmt.Errorf("chart: release name is required")
	}
	files := make([]*loader.BufferedFile, 0, len(chartFiles))
	for _, name := range sortedKeys(chartFiles) {
		files = append(files, &loader.BufferedFile{Name: name, Data: chartFiles[name]})
	}
	chrt, err := loader.LoadFiles(files)
	if err != nil {
		return nil, fmt.Errorf("chart %s: load: %w", release, err)
	}

	vals, err := chartutil.ToRenderValues(chrt, values, chartutil.ReleaseOptions{
		Name:      release,
		Namespace: namespace,
		IsInstall: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("chart %s: values: %w", release, err)
	}

	rendered, err := engine.Render(chrt, vals)
	if err != nil {
		return nil, fmt.Errorf("chart %s: render: %w", release, err)
	}

	// Sorted so a render is byte-stable across calls; map iteration is not, and
	// an unstable desired set makes every diff unreadable.
	var objs []*unstructured.Unstructured
	for _, name := range sortedKeys(rendered) {
		body := rendered[name]
		// Helm renders partials (_helpers.tpl) and NOTES.txt too; neither is an
		// object, and a template that evaluates to nothing is a legitimate way
		// for a chart to say "not this time".
		if strings.HasSuffix(name, "NOTES.txt") || strings.Contains(name, "/_") || strings.TrimSpace(body) == "" {
			continue
		}
		items, err := parseManifest(name, []byte(body))
		if err != nil {
			return nil, err
		}
		for _, o := range items {
			// A chart may legally render a document that is only comments. It
			// arrives as a typed object with no kind, which the apiserver would
			// reject at apply time with a message naming nothing useful.
			if o == nil || o.GetKind() == "" {
				continue
			}
			// The namespace is deliberately NOT stamped here. `helm template`
			// leaves it unset and lets the installer decide, and the reconcile
			// already passes a default namespace of its own — stamping would
			// both duplicate that and make this render disagree with helm's,
			// which is the one thing the comparison test exists to prevent.
			objs = append(objs, o)
		}
	}
	return objs, nil
}

// chartValues parses one values file. Helm coalesces it over the chart's own
// values.yaml, so a file that is empty or all comments is a legal "take the
// defaults" and must not be an error.
func chartValues(path string, data []byte) (map[string]any, error) {
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("values %s: %w", path, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// chartRoot is the prefix every file of a chart shares, stripped so the loader
// sees a chart rooted at Chart.yaml. A chart read out of a monorepo arrives as
// "charts/app/Chart.yaml"; Helm needs "Chart.yaml".
func chartRoot(prefix string, files map[string][]byte) map[string][]byte {
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	out := make(map[string][]byte, len(files))
	for p, b := range files {
		if rel, ok := strings.CutPrefix(p, prefix); ok && rel != "" {
			out[rel] = b
		}
	}
	return out
}

// chartName reads a chart's declared name without loading the whole chart —
// enough to tell a chart directory from any other directory of YAML.
func chartName(chartYAML []byte) (string, error) {
	var m chart.Metadata
	if err := yaml.Unmarshal(chartYAML, &m); err != nil {
		return "", fmt.Errorf("chart: metadata: %w", err)
	}
	return m.Name, nil
}

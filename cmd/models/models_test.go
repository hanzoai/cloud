package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deployment is a values file wired the way charts/app/values/hanzo/cloud.yaml
// wires cloud: env → volumeMount (subPath) → configMap volume item → configMaps.
func deployment(models string) string {
	return `env:
- name: modelConfigPath
  value: /conf/models.yaml
volumeMounts:
- mountPath: /conf/models.yaml
  name: models
  readOnly: true
  subPath: models.yaml
volumes:
- configMap:
    items:
    - key: models.yaml
      path: models.yaml
    name: cloud-api-models
  name: models
configMaps:
- name: cloud-api-models
  data:
    models.yaml: |
` + indent(models, "      ")
}

func indent(s, pre string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pre + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// write lays a values file out at charts/app/values/<ns>/cloud.yaml under a
// temp root, so check resolves the chart directory as it does in universe.
func write(t *testing.T, body string, files map[string]string) string {
	t.Helper()
	chart := filepath.Join(t.TempDir(), "charts", "app")
	f := filepath.Join(chart, "values", "hanzo", "cloud.yaml")
	for p, b := range files {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(chart, p)), 0o755))
		must(t, os.WriteFile(filepath.Join(chart, p), []byte(b), 0o644))
	}
	must(t, os.MkdirAll(filepath.Dir(f), 0o755))
	must(t, os.WriteFile(f, []byte(body), 0o644))
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const base = `default_pricing:
  input_per_million: 1.0
  output_per_million: 4.0
`

// The router.depth shapes: a table keyed by model id, then by depth, is what the
// pinned loader reads; a table keyed by depth alone is the older shape.
const nested = base + `router:
  enabled: true
  depth:
    enso-auto:
      default: enso-flash
      high: enso-pro
`

const flat = base + `router:
  enabled: true
  depth:
    "off": enso-flash
    high: enso-pro
`

func TestTheMountedConfigLoads(t *testing.T) {
	for name, models := range map[string]string{"no depth": base, "depth by model": nested} {
		from, err := check(write(t, deployment(models), nil))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if from != "cloud-api-models/models.yaml" {
			t.Fatalf("%s: read %q, want cloud-api-models/models.yaml", name, from)
		}
	}
}

func TestADepthTableByDepthAloneIsRefused(t *testing.T) {
	_, err := check(write(t, deployment(flat), nil))
	if err == nil || !strings.Contains(err.Error(), "cloud-api-models/models.yaml does not load") {
		t.Fatalf("flat router.depth: got %v, want a load error naming the ConfigMap key", err)
	}
}

func TestAConfigInTheChartFilesIsReadFromThere(t *testing.T) {
	body := strings.Replace(deployment(base), "  data:\n    models.yaml: |\n"+indent(base, "      "),
		"  files:\n    models.yaml: files/hanzo/cloud/models.yaml\n", 1)
	from, err := check(write(t, body, map[string]string{"files/hanzo/cloud/models.yaml": flat}))
	if err == nil || from != "cloud-api-models/models.yaml" {
		t.Fatalf("files: entry with a flat depth: from=%q err=%v, want a load error", from, err)
	}
}

func TestABrokenLinkIsNamed(t *testing.T) {
	good := deployment(base)
	for want, body := range map[string]string{
		"no modelConfigPath in env":               strings.Replace(good, "modelConfigPath", "otherPath", 1),
		"no volumeMount serves":                   strings.Replace(good, "mountPath: /conf/models.yaml", "mountPath: /conf/other.yaml", 1),
		`no volume "models"`:                      strings.Replace(good, "  name: models\nconfigMaps:", "  name: other\nconfigMaps:", 1),
		`projects no item at "models.yaml"`:       strings.Replace(good, "      path: models.yaml", "      path: other.yaml", 1),
		`configMap "cloud-api-models" is not`:     strings.Replace(good, "- name: cloud-api-models", "- name: other", 1),
		`configMap "cloud-api-models" has no key`: strings.Replace(good, "    models.yaml: |", "    other.yaml: |", 1),
	} {
		if body == good {
			t.Fatalf("%s: fixture edit did not apply", want)
		}
		_, err := check(write(t, body, nil))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want an error containing %q", err, want)
		}
	}
}

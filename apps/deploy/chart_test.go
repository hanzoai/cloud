package deploy

import (
	"testing"
)

// miniChart is the shape hanzo/app has: one values contract, a workload whose
// selector is exactly two labels, and a Service. The selector matters more than
// it looks — spec.selector on a Deployment is IMMUTABLE, so a render that adds a
// third label there is not a diff, it is an apply the apiserver refuses.
func miniChart() map[string][]byte {
	return map[string][]byte{
		"Chart.yaml": []byte("apiVersion: v2\nname: app\nversion: 0.1.0\nappVersion: \"0.1.0\"\n"),
		"values.yaml": []byte(`replicas: 1
image:
  repository: ""
  tag: ""
  pullPolicy: IfNotPresent
ports: []
partOf: ""
`),
		"templates/_helpers.tpl": []byte(`{{- define "app.name" -}}
{{- .Release.Name -}}
{{- end -}}
{{- define "app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "app.name" . }}
app.kubernetes.io/instance: {{ include "app.name" . }}
{{- end -}}
`),
		"templates/workload.yaml": []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "app.name" . }}
  labels:
    {{- include "app.selectorLabels" . | nindent 4 }}
    app.kubernetes.io/managed-by: {{ .Release.Service }}
    {{- with .Values.partOf }}
    app.kubernetes.io/part-of: {{ . }}
    {{- end }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      {{- include "app.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "app.selectorLabels" . | nindent 8 }}
    spec:
      containers:
        - name: {{ include "app.name" . }}
          image: {{ .Values.image.repository }}:{{ .Values.image.tag }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
`),
		"templates/service.yaml": []byte(`{{- if .Values.ports }}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "app.name" . }}
spec:
  selector:
    {{- include "app.selectorLabels" . | nindent 4 }}
  ports:
    {{- range .Values.ports }}
    - name: {{ .name }}
      port: {{ .servicePort | default .containerPort }}
      targetPort: {{ .containerPort }}
    {{- end }}
{{- end }}
`),
		"templates/NOTES.txt": []byte("thanks for installing {{ .Release.Name }}\n"),
	}
}

// TestRenderChart proves the fleet's shape renders: the release name reaches
// every object, values override the chart's defaults, partials and NOTES never
// become objects, and the namespace is left for the installer to apply.
func TestRenderChart(t *testing.T) {
	vals, err := chartValues("www.yaml", []byte(`replicas: 2
partOf: www
image:
  repository: ghcr.io/hanzoai/cloud-www
  tag: 0.1.5
ports:
  - name: http
    containerPort: 3000
    servicePort: 80
`))
	if err != nil {
		t.Fatalf("values: %v", err)
	}

	objs, err := renderChart(miniChart(), "www", "hanzo", vals)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(objs) != 2 {
		got := []string{}
		for _, o := range objs {
			got = append(got, o.GetKind()+"/"+o.GetName())
		}
		t.Fatalf("objects = %v, want a Deployment and a Service", got)
	}

	byKind := map[string]int{}
	for _, o := range objs {
		byKind[o.GetKind()]++
		if o.GetName() != "www" {
			t.Fatalf("%s named %q, want the release name", o.GetKind(), o.GetName())
		}
		// The namespace is NOT stamped: helm template leaves it unset and the
		// reconcile applies its own default, so stamping here would diverge
		// from helm for no gain.
		if o.GetNamespace() != "" {
			t.Fatalf("%s namespace = %q, want it left to the installer", o.GetKind(), o.GetNamespace())
		}
	}
	if byKind["Deployment"] != 1 || byKind["Service"] != 1 {
		t.Fatalf("kinds = %v", byKind)
	}

	dep := objs[0]
	if dep.GetKind() != "Deployment" {
		dep = objs[1]
	}
	spec, _ := dep.UnstructuredContent()["spec"].(map[string]any)
	if r, ok := spec["replicas"].(int64); !ok || r != 2 {
		t.Fatalf("replicas = %v, want 2 from the values file", spec["replicas"])
	}
	// The selector is EXACTLY the two labels. A third here is an apply the
	// apiserver refuses, not a diff.
	sel, _ := spec["selector"].(map[string]any)
	ml, _ := sel["matchLabels"].(map[string]any)
	if len(ml) != 2 || ml["app.kubernetes.io/name"] != "www" || ml["app.kubernetes.io/instance"] != "www" {
		t.Fatalf("selector = %v, want exactly name+instance", ml)
	}
	// managed-by comes from .Release.Service, which Helm sets.
	lbl := dep.GetLabels()
	if lbl["app.kubernetes.io/managed-by"] != "Helm" || lbl["app.kubernetes.io/part-of"] != "www" {
		t.Fatalf("labels = %v", lbl)
	}
}

// TestRenderChartGuardedTemplateIsEmpty proves a template that renders to
// nothing contributes nothing. Charts guard whole files on a values key, and an
// empty document reaching the desired set would be an object with no kind.
func TestRenderChartGuardedTemplateIsEmpty(t *testing.T) {
	objs, err := renderChart(miniChart(), "worker", "hanzo", map[string]any{
		"replicas": 1,
		"image":    map[string]any{"repository": "ghcr.io/hanzoai/worker", "tag": "1.0.0"},
		// no ports -> the Service template guards itself out
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(objs) != 1 || objs[0].GetKind() != "Deployment" {
		t.Fatalf("objects = %d, want only the Deployment", len(objs))
	}
}

// TestRenderChartRequiresRelease pins that a release name is required. Helm
// names objects from it, so rendering without one produces objects named ""
// that the apiserver rejects one at a time.
func TestRenderChartRequiresRelease(t *testing.T) {
	if _, err := renderChart(miniChart(), "", "hanzo", nil); err == nil {
		t.Fatal("render accepted an empty release name")
	}
}

// TestChartRootStripsPrefix proves a chart read out of a monorepo is presented
// to the loader rooted at Chart.yaml.
func TestChartRootStripsPrefix(t *testing.T) {
	got := chartRoot("charts/app", map[string][]byte{
		"charts/app/Chart.yaml":              []byte("x"),
		"charts/app/templates/workload.yaml": []byte("y"),
		"charts/app/values/hanzo/www.yaml":   []byte("z"),
		"infra/k8s/other.yaml":               []byte("w"), // outside the chart
	})
	if len(got) != 3 {
		t.Fatalf("rooted files = %v", sortedKeys(got))
	}
	for _, want := range []string{"Chart.yaml", "templates/workload.yaml", "values/hanzo/www.yaml"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing %q, have %v", want, sortedKeys(got))
		}
	}
}

// TestChartValuesEmptyIsDefaults proves an empty or comment-only values file is
// "take the defaults", not an error — Helm coalesces it over the chart's own.
func TestChartValuesEmptyIsDefaults(t *testing.T) {
	for _, body := range []string{"", "# nothing but a comment\n", "---\n"} {
		v, err := chartValues("empty.yaml", []byte(body))
		if err != nil {
			t.Fatalf("values %q: %v", body, err)
		}
		if v == nil || len(v) != 0 {
			t.Fatalf("values %q = %v, want an empty map", body, v)
		}
	}
	if _, err := chartValues("bad.yaml", []byte("replicas: [1,\n")); err == nil {
		t.Fatal("malformed values accepted")
	}
}

// TestChartName reads the declared name, which is how a chart directory is told
// from any other directory of YAML.
func TestChartName(t *testing.T) {
	n, err := chartName([]byte("apiVersion: v2\nname: app\nversion: 0.1.0\n"))
	if err != nil || n != "app" {
		t.Fatalf("chartName = %q, %v", n, err)
	}
	if _, err := chartName([]byte("name: [oops\n")); err == nil {
		t.Fatal("malformed Chart.yaml accepted")
	}
}

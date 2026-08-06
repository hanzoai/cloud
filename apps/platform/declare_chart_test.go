package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"sigs.k8s.io/yaml"
)

// declare_chart_test.go — the declaration is proved against the REAL chart.
//
// Everything else in this package can agree with itself and still be wrong about
// the one thing that matters: whether cd.hanzo.ai can render what we wrote. The
// chart lives in another repository (hanzoai/universe `charts/app`), it carries a
// values.schema.json with `additionalProperties: false`, and Helm validates
// against that schema BEFORE rendering — so a single invented key does not
// degrade the deployment, it fails the sync outright and the app never appears.
//
// This test renders the file this package generates through the actual chart, in
// process, exactly as the Application does. It SKIPS when the chart is not on
// this machine (it is not vendored, and vendoring it would create the second copy
// this whole design exists to avoid): point UNIVERSE_CHART at a universe checkout's
// charts/app, or have one at a conventional path beside this repository.
func TestGeneratedDeclarationRendersThroughTheRealChart(t *testing.T) {
	dir := realChart(t)

	spec := testSpec()
	spec.Env = []declareEnv{{Name: "PORT", Value: "3000"}, {Name: "VERSION", Value: "1.10"}}
	values := map[string]any{}
	if err := yaml.Unmarshal(spec.render(), &values); err != nil {
		t.Fatalf("the generated declaration is not valid YAML: %v", err)
	}

	ch, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	// The Application renders with releaseName = the file's basename and
	// namespace = the directory, so the render below is the one CD performs.
	rv, err := chartutil.ToRenderValues(ch, values, chartutil.ReleaseOptions{
		Name: spec.Name, Namespace: spec.Org, IsInstall: true,
	}, nil)
	if err != nil {
		// ToRenderValues is where values.schema.json is enforced. A failure here
		// IS the failure CD would report, with the same message.
		t.Fatalf("the chart REFUSED the generated declaration (this is what cd.hanzo.ai would say):\n%v", err)
	}
	files, err := engine.Render(ch, rv)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	kinds := map[string]string{}
	for name, body := range files {
		if strings.TrimSpace(body) == "" || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		for _, doc := range strings.Split(body, "\n---") {
			var head struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(doc), &head); err != nil || head.Kind == "" {
				continue
			}
			kinds[head.Kind] = doc
		}
	}

	// The three objects a web app needs, and the facts the caller asked for.
	for _, kind := range []string{"Deployment", "Service", "Ingress"} {
		if kinds[kind] == "" {
			t.Fatalf("the chart rendered no %s from the generated declaration; it rendered %v", kind, keysOf(anyMap(kinds)))
		}
	}
	if !strings.Contains(kinds["Deployment"], spec.Repository+":"+spec.Tag) {
		t.Errorf("the Deployment does not pull the declared image %s:%s:\n%s", spec.Repository, spec.Tag, kinds["Deployment"])
	}
	if !strings.Contains(kinds["Ingress"], spec.Hosts[0]) {
		t.Errorf("the Ingress does not serve the declared host %s:\n%s", spec.Hosts[0], kinds["Ingress"])
	}
	// The env value must reach the container as a STRING. Unquoted, YAML reads
	// 1.10 as a float and Kubernetes rejects the pod spec.
	if !strings.Contains(kinds["Deployment"], `"1.10"`) {
		t.Errorf("an env value was not rendered as a string:\n%s", kinds["Deployment"])
	}
	// The pull secret, or a private image cannot be fetched at all.
	if !strings.Contains(kinds["Deployment"], declarePullSecret) {
		t.Errorf("the Deployment names no image pull secret:\n%s", kinds["Deployment"])
	}
}

// A declaration with an invented key must be REFUSED by the chart, not quietly
// ignored. This is the proof that the schema is load-bearing — if it ever stops
// being enforced, this test fails and the "only schema keys" rule above becomes
// advice rather than a guarantee.
func TestTheChartRefusesAKeyItDoesNotDeclare(t *testing.T) {
	dir := realChart(t)
	ch, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	values := map[string]any{}
	if err := yaml.Unmarshal(testSpec().render(), &values); err != nil {
		t.Fatal(err)
	}
	values["storageGb"] = 10 // a plausible key the chart does not have
	if _, err := chartutil.ToRenderValues(ch, values, chartutil.ReleaseOptions{
		Name: "web", Namespace: "acme", IsInstall: true,
	}, nil); err == nil {
		t.Fatal("the chart accepted a key it does not declare; values.schema.json is not being enforced")
	}
}

// realChart locates a universe checkout's charts/app, or skips.
func realChart(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("UNIVERSE_CHART")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "work", "hanzo", "universe", "charts", "app"),
			filepath.Join(home, "universe", "charts", "app"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "Chart.yaml")); err == nil {
			return c
		}
	}
	t.Skip("no universe charts/app checkout found; set UNIVERSE_CHART to run the real-chart proof")
	return ""
}

func anyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ── the fence lives in another repository, so the write path VERIFIES it ────

// checkFence is a runtime control, not a comment: it reads the live
// ApplicationSet out of the clone the write already makes and refuses to put a
// declaration on main while that template would fence it wider than this API
// reports. These hold it to that, against the REAL universe checkout.
func TestFenceRefusesMainWhileTheApplicationSetDisagrees(t *testing.T) {
	root := universeRoot(t)

	// A customer org: reported fence is its own name, template still says
	// hasPrefix "tenant-" ⇒ it would land in hanzo-platform ⇒ refuse.
	err := checkFence(root, "acme")
	body, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(fleetSet)))
	if readErr != nil {
		t.Skipf("no ApplicationSet in %s: %v", root, readErr)
	}
	stale := strings.Contains(string(body), `hasPrefix "tenant-"`)
	switch {
	case stale && err == nil:
		t.Fatal("the ApplicationSet still fences by hasPrefix \"tenant-\" and checkFence allowed a commit to main")
	case stale:
		if !strings.Contains(err.Error(), platformProject) {
			t.Errorf("the refusal must name the fence that would actually apply: %v", err)
		}
		t.Logf("BLOCKED AS DESIGNED: %v", err)
	case err != nil:
		t.Fatalf("the ApplicationSet carries a reservation rule but checkFence still refused: %v", err)
	}

	// A RESERVED directory is fenced identically under either template, so it is
	// never blocked — the platform must still be able to release itself.
	if err := checkFence(root, "hanzo"); err != nil {
		t.Errorf("a reserved directory was blocked: %v", err)
	}
}

// An unreadable template is a refusal, never a pass. "The rule could not be
// read" and "the rule agrees" are opposite facts.
func TestFenceRefusesWhenTheTemplateCannotBeRead(t *testing.T) {
	if err := checkFence(t.TempDir(), "acme"); err == nil {
		t.Fatal("a commit to main was allowed with no ApplicationSet to check against")
	}
	// ...and a reserved directory still passes, since its fence does not depend
	// on the branch of the template that is in question.
	if err := checkFence(t.TempDir(), "hanzo"); err != nil {
		t.Errorf("a reserved directory needs no template to be fenced: %v", err)
	}
}

// universeRoot is the universe checkout holding both the chart and the
// ApplicationSet, or a skip.
func universeRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(realChart(t)))
}

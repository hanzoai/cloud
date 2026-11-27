package platform

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/k8s"

	"github.com/hanzoai/cloud/sdk"
	"github.com/luxfi/log"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func jobService(t *testing.T) *cloud.Service[state] {
	t.Helper()
	t.Setenv(runnerImageEnv, "oci.hanzo.ai/hanzoai/git-runner:4")
	t.Setenv(runnerNamespaceEnv, "hanzo")
	return runnerService(t, "gvisor", "visor")
}

// runnerService is a platform whose cluster installs these runtime classes.
func runnerService(t *testing.T, classes ...string) *cloud.Service[state] {
	t.Helper()
	t.Setenv(runnerImageEnv, "oci.hanzo.ai/hanzoai/git-runner:4")
	t.Setenv(runnerNamespaceEnv, "hanzo")
	var objs []runtime.Object
	for _, c := range classes {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "node.k8s.io/v1", "kind": "RuntimeClass",
			"metadata": map[string]any{"name": c}, "handler": c,
		}})
	}
	return &cloud.Service[state]{Log: log.New("test"), Brand: "hanzo", State: state{k8s: fakeK8s(objs...)}}
}

// A runner runs in visor, and a cluster without visor gets no Job at all — a Job
// naming a missing class gets no pod and holds a runner slot until its deadline.
func TestARunnerRunsInVisorOrNotAtAll(t *testing.T) {
	for _, c := range []struct {
		have []string
		ok   bool
	}{
		{[]string{"gvisor", "visor"}, true},
		{[]string{"gvisor"}, false},
		{nil, false},
	} {
		s := runnerService(t, c.have...)
		name, err := launch(s, context.Background(), cloud.JobEvent{
			Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 7,
			Labels: []string{"linux-amd64"}, URL: "https://git.hanzo.ai", Token: "reg-token",
		})
		if !c.ok {
			if err == nil {
				t.Fatalf("%v: a cluster without visor must refuse the launch, got Job %q", c.have, name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v: launch: %v", c.have, err)
		}
		job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("job not created: %v", err)
		}
		spec := job.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		if spec["runtimeClassName"] != "visor" {
			t.Fatalf("runtimeClassName %v, want visor", spec["runtimeClassName"])
		}
	}
}

// A forge job becomes one Job in this namespace: the runner image, the guest
// runtime, the forge's URL and registration, and the labels the job asked for
// as host-executor labels.
func TestLaunchRendersAForgeRunner(t *testing.T) {
	s := jobService(t)
	name, err := launch(s, context.Background(), cloud.JobEvent{
		Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 42,
		Labels: []string{"hanzo-build-linux-amd64"}, URL: "https://git.hanzo.ai", Token: "reg-token",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if name != "runner-forge-gui-42" {
		t.Fatalf("name %q", name)
	}
	job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("job not created: %v", err)
	}
	spec := job.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if spec["runtimeClassName"] != "visor" {
		t.Fatalf("runtimeClassName %v", spec["runtimeClassName"])
	}
	c := spec["containers"].([]any)[0].(map[string]any)
	if c["image"] != "oci.hanzo.ai/hanzoai/git-runner:4" {
		t.Fatalf("image %v", c["image"])
	}
	env := map[string]string{}
	var ref map[string]any
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		if v, ok := m["value"].(string); ok {
			env[m["name"].(string)] = v
		}
		if m["name"] == "GIT_RUNNER_TOKEN" {
			ref, _ = m["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
		}
	}
	if env["GIT_RUNNER_URL"] != "https://git.hanzo.ai" {
		t.Fatalf("registration env %v", env)
	}
	// The registration is read from a Secret the Job owns; the Job itself
	// carries no credential anywhere.
	if ref["name"] != name || ref["key"] != "token" {
		t.Fatalf("GIT_RUNNER_TOKEN must be a secretKeyRef to %s/token, got %v", name, ref)
	}
	raw, _ := json.Marshal(job.Object)
	if strings.Contains(string(raw), "reg-token") {
		t.Fatal("the Job object carries the registration in the clear")
	}
	secret, err := s.State.k8s.dyn.Resource(secretsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("registration secret not created: %v", err)
	}
	if secret.Object["stringData"].(map[string]any)["token"] != "reg-token" {
		t.Fatalf("secret holds %v", secret.Object["stringData"])
	}
	owners := secret.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Job" || owners[0].Name != name || owners[0].UID != job.GetUID() {
		t.Fatalf("the secret must be owned by its Job, so it goes when the Job does: %+v", owners)
	}
	if env["RUNNER_LABELS"] != "hanzo-build-linux-amd64:host" {
		t.Fatalf("labels %q", env["RUNNER_LABELS"])
	}
	if env["RUNNER_FETCH_TIMEOUT"] != runnerFetchTimeout || env["RUNNER_FETCH_INTERVAL"] != runnerFetchInterval || env["RUNNER_FETCH_INTERVAL_MAX"] != runnerFetchIntervalMax {
		t.Fatalf("a forge runner polls with %q/%q/%q, want %s/%s/%s", env["RUNNER_FETCH_TIMEOUT"], env["RUNNER_FETCH_INTERVAL"], env["RUNNER_FETCH_INTERVAL_MAX"], runnerFetchTimeout, runnerFetchInterval, runnerFetchIntervalMax)
	}
	if env["RUNNER_JITCONFIG"] != "" {
		t.Fatalf("a forge runner carries no JIT configuration")
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT %q, want 0", env["GIT_TERMINAL_PROMPT"])
	}
	if b := job.Object["spec"].(map[string]any)["backoffLimit"]; b != int64(0) {
		t.Fatalf("backoffLimit %v, want 0: a retried runner holds a slot it cannot use", b)
	}
	// Six cloud gates on one node without a disk request: the scheduler saw room,
	// the kubelet evicted them mid-run.
	res := c["resources"].(map[string]any)
	if res["requests"].(map[string]any)["ephemeral-storage"] == nil {
		t.Fatal("no ephemeral-storage request: the runner is best-effort for disk and evicted first")
	}
	if res["limits"].(map[string]any)["ephemeral-storage"] == nil {
		t.Fatal("no ephemeral-storage limit: one runaway job evicts every neighbour")
	}
	// The request is what a job uses: CPU p50 1.12 cores, memory p90 1.4Gi.
	// Under it the scheduler packs runners past the node's cores and all starve;
	// over it memory fills a node's requests while most of it sits unused.
	req := res["requests"].(map[string]any)
	if cpu := resource.MustParse(req["cpu"].(string)); cpu.MilliValue() < 1500 {
		t.Fatalf("cpu request %s is below what a job uses", req["cpu"])
	}
	if mem := resource.MustParse(req["memory"].(string)); mem.Value() > 1536<<20 {
		t.Fatalf("memory request %s reserves memory a job does not use", req["memory"])
	}
}

// A GitHub job carries only its just-in-time configuration.
func TestLaunchPlacesAnUnnamedJobOnEitherNode(t *testing.T) {
	s := jobService(t)
	name, err := launch(s, context.Background(), cloud.JobEvent{
		Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 43,
		Labels: []string{"ubuntu-latest"}, URL: "https://git.hanzo.ai", Token: "reg-token",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("job not created: %v", err)
	}
	spec := job.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if _, pinned := spec["nodeSelector"]; pinned {
		t.Fatalf("nodeSelector %v: a job that names no platform runs on either node", spec["nodeSelector"])
	}
	spread := spec["topologySpreadConstraints"].([]any)[0].(map[string]any)
	if spread["topologyKey"] != "kubernetes.io/hostname" || spread["maxSkew"] != int64(1) {
		t.Fatalf("spread %v: runners spread one per node before doubling up", spread)
	}
	if !avoidsPressure(spec) {
		t.Fatalf("affinity %v: a runner lands on a node under disk pressure", spec["affinity"])
	}
}

// A deploy job outranks every other runner and stays below every workload: it is
// placed first when a node frees, and the class of the job decides its node as
// before.
func TestADeployJobOutranksTheOtherRunners(t *testing.T) {
	for i, c := range []struct {
		labels   []string
		priority string
		class    any
	}{
		{[]string{"xl-deploy-linux-amd64"}, "hanzo-deploy", "xl"},
		{[]string{"XL-Deploy-Linux-AMD64"}, "hanzo-deploy", "xl"},
		{[]string{"xl-linux-amd64"}, "hanzo-ci", "xl"},
		{[]string{"linux-amd64"}, "hanzo-ci", nil},
		{[]string{"deployer-linux-amd64"}, "hanzo-ci", nil},
	} {
		s := jobService(t)
		name, err := launch(s, context.Background(), cloud.JobEvent{
			Org: "hanzo", Provider: "github", Repo: "hanzo-inc/app", ID: int64(300 + i),
			Labels: c.labels, Token: "jit",
		})
		if err != nil {
			t.Fatalf("%v: launch: %v", c.labels, err)
		}
		job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%v: job not created: %v", c.labels, err)
		}
		spec, _, _ := unstructured.NestedMap(job.Object, "spec", "template", "spec")
		if got := spec["priorityClassName"]; got != c.priority {
			t.Errorf("%v: priority %v, want %s", c.labels, got, c.priority)
		}
		if got := runnerClass(c.labels); (c.class == nil && got != "") || (c.class != nil && got != c.class) {
			t.Errorf("%v: class %q, want %v", c.labels, got, c.class)
		}
	}
}

// A heavy job requests the cores it uses and joins no node that runs another
// while a node without one exists, so two are never packed where each starves.
// An xl job asks 7 cores and 14 GiB on the xl pool, may burst to the whole
// node, and goes nowhere else. Every other job keeps the ordinary request and a spread that is
// a preference.
func TestAHeavyJobGetsItsCoresAndANodeApart(t *testing.T) {
	for i, c := range []struct {
		provider   string
		labels     []string
		cpu, limit string
		mem, ceil  string
		spread     string
		among      map[string]any
		class      any
		arch       string
	}{
		{"forge", []string{"linux-amd64"}, "1500m", "6", "1536Mi", "24Gi", "ScheduleAnyway", map[string]any{"hanzo.ai/runner": "forge"}, nil, "amd64"},
		{"forge", []string{"heavy-linux-amd64"}, "6", "6", "1536Mi", "24Gi", "DoNotSchedule", map[string]any{"hanzo.ai/class": "heavy"}, "heavy", "amd64"},
		{"forge", []string{"heavy-linux-arm64"}, "6", "6", "1536Mi", "24Gi", "DoNotSchedule", map[string]any{"hanzo.ai/class": "heavy"}, "heavy", "arm64"},
		{"github", []string{"self-hosted", "heavy-linux-amd64"}, "6", "6", "1536Mi", "24Gi", "DoNotSchedule", map[string]any{"hanzo.ai/class": "heavy"}, "heavy", "amd64"},
		{"github", []string{"hanzo-build-linux-amd64"}, "1500m", "6", "1536Mi", "24Gi", "ScheduleAnyway", map[string]any{"hanzo.ai/runner": "github"}, nil, "amd64"},
		{"github", []string{"xl-linux-amd64"}, "7", "32", "14Gi", "56Gi", "DoNotSchedule", map[string]any{"hanzo.ai/class": "xl"}, "xl", "amd64"},
		{"forge", []string{"XL-Linux-AMD64"}, "7", "32", "14Gi", "56Gi", "DoNotSchedule", map[string]any{"hanzo.ai/class": "xl"}, "xl", "amd64"},
	} {
		s := jobService(t)
		name, err := launch(s, context.Background(), cloud.JobEvent{
			Org: "hanzo", Provider: c.provider, Repo: "hanzo-inc/cloud", ID: int64(100 + i),
			Labels: c.labels, URL: "https://git.hanzo.ai", Token: "reg-token",
		})
		if err != nil {
			t.Fatalf("%v: launch: %v", c.labels, err)
		}
		job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%v: job not created: %v", c.labels, err)
		}
		tmpl := job.Object["spec"].(map[string]any)["template"].(map[string]any)
		spec := tmpl["spec"].(map[string]any)
		res := spec["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		requests, limits := res["requests"].(map[string]any), res["limits"].(map[string]any)
		if requests["cpu"] != c.cpu || limits["cpu"] != c.limit {
			t.Errorf("%v: cpu %v up to %v, want %s up to %s", c.labels, requests["cpu"], limits["cpu"], c.cpu, c.limit)
		}
		if requests["memory"] != c.mem || limits["memory"] != c.ceil {
			t.Errorf("%v: memory %v up to %v, want %s up to %s", c.labels, requests["memory"], limits["memory"], c.mem, c.ceil)
		}
		if requests["ephemeral-storage"] != "10Gi" || limits["ephemeral-storage"] != "80Gi" {
			t.Errorf("%v: disk %v up to %v, want 10Gi up to 80Gi for every class", c.labels, requests["ephemeral-storage"], limits["ephemeral-storage"])
		}
		constraints := spec["topologySpreadConstraints"].([]any)
		if len(constraints) != 1 {
			t.Fatalf("%v: %d spread constraints, want 1", c.labels, len(constraints))
		}
		spread := constraints[0].(map[string]any)
		skew := int64(1)
		if c.class == "xl" {
			skew = 4
		}
		if spread["whenUnsatisfiable"] != c.spread || spread["topologyKey"] != "kubernetes.io/hostname" || spread["maxSkew"] != skew {
			t.Errorf("%v: spread %v, want %s with a skew of %d", c.labels, spread, c.spread, skew)
		}
		among := spread["labelSelector"].(map[string]any)["matchLabels"].(map[string]any)
		if len(among) != len(c.among) {
			t.Errorf("%v: spread counts %v, want %v", c.labels, among, c.among)
		}
		pod := tmpl["metadata"].(map[string]any)["labels"].(map[string]any)
		for k, v := range c.among {
			if among[k] != v {
				t.Errorf("%v: spread counts %v, want %v", c.labels, among, c.among)
			}
			// A selector that does not match the pod's own labels counts nothing.
			if pod[k] != v {
				t.Errorf("%v: the pod's labels %v do not carry %s=%v, so its spread would not count it", c.labels, pod, k, v)
			}
		}
		if pod["hanzo.ai/class"] != c.class {
			t.Errorf("%v: class label %v, want %v", c.labels, pod["hanzo.ai/class"], c.class)
		}
		// The slot count reads every runner by this label, whatever its class.
		if pod["hanzo.ai/runner"] != c.provider {
			t.Errorf("%v: runner label %v, want %s", c.labels, pod["hanzo.ai/runner"], c.provider)
		}
		selector := spec["nodeSelector"].(map[string]any)
		if got := selector["kubernetes.io/arch"]; got != c.arch {
			t.Errorf("%v: arch %v, want %s", c.labels, got, c.arch)
		}
		// Only an xl job is held to the xl pool and tolerates its taint; every
		// runner tolerates evo's.
		xl := c.class == "xl"
		if pinned := selector["hanzo.ai/class"] == "xl"; pinned != xl {
			t.Errorf("%v: nodeSelector %v, want the xl pool %v", c.labels, selector, xl)
		}
		var tolerates []string
		for _, tol := range spec["tolerations"].([]any) {
			tol := tol.(map[string]any)
			value, _ := tol["value"].(string)
			tolerates = append(tolerates, tol["key"].(string)+"="+value)
		}
		want := []string{"hanzo.ai/no-ci="}
		if xl {
			want = append(want, "hanzo.ai/class=xl")
		}
		if !slices.Equal(tolerates, want) {
			t.Errorf("%v: tolerations %v, want %v", c.labels, tolerates, want)
		}
		if spec["priorityClassName"] != "hanzo-ci" {
			t.Errorf("%v: priority class %v: a runner above hanzo-ci takes a node from a service", c.labels, spec["priorityClassName"])
		}
		if !avoidsPressure(spec) {
			t.Errorf("%v: affinity %v: a runner lands on a node under disk pressure", c.labels, spec["affinity"])
		}
	}
}

// The class is a part of a label between hyphens, as the architecture is.
func TestRunnerClass(t *testing.T) {
	for _, c := range []struct {
		labels []string
		want   string
	}{
		{[]string{"heavy-linux-amd64"}, "heavy"},
		{[]string{"heavy-linux-arm64"}, "heavy"},
		{[]string{"Heavy-Linux-AMD64"}, "heavy"},
		{[]string{"self-hosted", "heavy-linux-amd64"}, "heavy"},
		{[]string{"xl-linux-amd64"}, "xl"},
		{[]string{"XL-Linux-AMD64"}, "xl"},
		{[]string{"self-hosted", "xl-linux-amd64"}, "xl"},
		{[]string{"heavy-linux-amd64", "xl-linux-amd64"}, "xl"},
		{[]string{"linux-amd64"}, ""},
		{[]string{"hanzo-build-linux-amd64"}, ""},
		{[]string{"ubuntu-latest"}, ""},
		{[]string{"heavyweight-linux-amd64"}, ""},
		{[]string{"linux-amd64-superheavy"}, ""},
		{[]string{"xlarge-linux-amd64"}, ""},
		{[]string{"c7a.8xl-linux-amd64"}, ""},
		{nil, ""},
	} {
		if got := runnerClass(c.labels); got != c.want {
			t.Errorf("%v: class %q, want %q", c.labels, got, c.want)
		}
	}
}

// The xl pool is amd64 alone, so an xl job that asks arm64 is refused at launch
// rather than left Pending for a node no group makes.
func TestAnXLJobOnArm64IsRefused(t *testing.T) {
	s := jobService(t)
	_, err := launch(s, context.Background(), cloud.JobEvent{
		Org: "hanzo", Provider: "github", Repo: "hanzo-inc/app", ID: 11,
		Labels: []string{"xl-linux-arm64"}, Token: "jit",
	})
	if err == nil || !strings.Contains(err.Error(), "no arm64 node") {
		t.Fatalf("launch: %v, want the xl class refused on arm64", err)
	}
	jobs, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("%d Jobs created for a refused job", len(jobs.Items))
	}
}

// A job has one limit, three hours, held in three places: the runner
// (act_runner's default), the forge ([actions] ENDLESS_TASK_TIMEOUT in
// universe's hanzo-git values) and the pod's deadline.
func TestARunnerPodLivesAsLongAsAJobMay(t *testing.T) {
	s := jobService(t)
	name, err := launch(s, context.Background(), cloud.JobEvent{
		Org: "hanzo", Provider: "forge", Repo: "hanzo-inc/cloud", ID: 9,
		Labels: []string{"heavy-linux-amd64"}, URL: "https://git.hanzo.ai", Token: "reg-token",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("job not created: %v", err)
	}
	if d := job.Object["spec"].(map[string]any)["activeDeadlineSeconds"]; d != int64(3*60*60) {
		t.Fatalf("activeDeadlineSeconds %v, want 10800: the runner and the forge end a job at three hours", d)
	}
}

// The autoscaler does not take a runner's node from under it: a runner evicted
// mid-job strands its task on the forge for three hours.
func TestTheAutoscalerLeavesARunnerItsNode(t *testing.T) {
	s := jobService(t)
	for _, ev := range []cloud.JobEvent{
		{Org: "hanzo", Provider: "forge", Repo: "hanzo-inc/cloud", ID: 12, Labels: []string{"linux-amd64"}, URL: "https://git.hanzo.ai", Token: "reg-token"},
		{Org: "hanzo", Provider: "github", Repo: "hanzoai/gui", ID: 13, Labels: []string{"linux-amd64"}, Token: "jit"},
	} {
		name, err := launch(s, context.Background(), ev)
		if err != nil {
			t.Fatalf("launch %s: %v", ev.Provider, err)
		}
		job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("job not created: %v", err)
		}
		meta := job.Object["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		if ann["cluster-autoscaler.kubernetes.io/safe-to-evict"] != "false" {
			t.Fatalf("%s runner pod annotations %v: the autoscaler may evict it mid-job", ev.Provider, ann)
		}
		if meta["labels"].(map[string]any)["hanzo.ai/runner"] != ev.Provider {
			t.Fatalf("%s runner pod lost its labels: %v", ev.Provider, meta["labels"])
		}
	}
}

func TestLaunchRendersAGitHubRunner(t *testing.T) {
	s := jobService(t)
	name, err := launch(s, context.Background(), cloud.JobEvent{
		Org: "hanzo", Provider: "github", Repo: "hanzoai/gui", ID: 7, Labels: []string{"hanzo-build-linux-amd64"}, Token: "jit",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("job not created: %v", err)
	}
	c := job.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	var names []string
	for _, e := range c["env"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "RUNNER_JITCONFIG,GIT_TERMINAL_PROMPT,RUNNER_NAME" {
		t.Fatalf("env %v", names)
	}
	if b := job.Object["spec"].(map[string]any)["backoffLimit"]; b != int64(0) {
		t.Fatalf("backoffLimit %v, want 0: a just-in-time registration is single-use", b)
	}
	// The queue loop's second runner for the same job is a second name, the one
	// its registration was minted under.
	name, err = launch(s, context.Background(), cloud.JobEvent{
		Org: "hanzo", Provider: "github", Repo: "hanzoai/gui", ID: 7, Labels: []string{"hanzo-build-linux-amd64"}, Token: "jit", Try: 2,
	})
	if err != nil || name != "runner-github-gui-7-r2" {
		t.Fatalf("try 2: %q, %v; want runner-github-gui-7-r2", name, err)
	}
}

// What a provider sends reaches a Job name and an env value, so every field is
// held to a hostname's alphabet before anything is created.
func TestLaunchRefusesWhatItCannotName(t *testing.T) {
	s := jobService(t)
	good := cloud.JobEvent{Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 1, Labels: []string{"ok"}, URL: "https://git.hanzo.ai", Token: "t"}
	for name, mutate := range map[string]func(*cloud.JobEvent){
		"no slash in repo":  func(e *cloud.JobEvent) { e.Repo = "gui" },
		"shell in repo":     func(e *cloud.JobEvent) { e.Repo = "hanzoai/gui;rm" },
		"space in label":    func(e *cloud.JobEvent) { e.Labels = []string{"a b"} },
		"no token":          func(e *cloud.JobEvent) { e.Token = "" },
		"no labels (forge)": func(e *cloud.JobEvent) { e.Labels = nil },
		"plain http forge":  func(e *cloud.JobEvent) { e.URL = "http://git" },
		"unknown provider":  func(e *cloud.JobEvent) { e.Provider = "gitlab" },
		"zero id":           func(e *cloud.JobEvent) { e.ID = 0 },
		"negative try":      func(e *cloud.JobEvent) { e.Try = -1 },
	} {
		ev := good
		mutate(&ev)
		if _, err := launch(s, context.Background(), ev); err == nil {
			t.Errorf("%s: launched", name)
		}
	}
}

// A runner is refused once the cluster is at its ceiling.
//
// The failure this prevents: one delivery became one kata microVM with no bound,
// so a busy repository put seven runners up at once and twelve across the
// cluster, 9-10 GB of guest RAM apiece, until the kernel started killing
// processes. The build path already counts its Jobs before starting another;
// this one did not.
func TestLaunchRefusesOverTheRunnerCeiling(t *testing.T) {
	s := jobService(t)
	ev := cloud.JobEvent{
		Provider: "github",
		Org:      "hanzo",
		Repo:     "hanzoai/gui",
		ID:       42,
		Token:    "jit",
		Labels:   []string{"hanzo-build-linux-amd64"},
	}

	// Fill the cluster's nodes to the ceiling, then ask for one more.
	max := s.State.k8s.limits.maxConcurrentRunners()
	for i := range max {
		ev.ID = int64(100 + i)
		name, err := launch(s, context.Background(), ev)
		if err != nil {
			t.Fatalf("runner %d of %d refused early: %v", i+1, max, err)
		}
		bind(t, s, name, "node-a")
	}

	ev.ID = 999
	_, err := launch(s, context.Background(), ev)
	if !errors.Is(err, errTooManyRunners) {
		t.Fatalf("over the ceiling must refuse with errTooManyRunners, got %v", err)
	}
	if !errors.Is(err, cloud.ErrRunnerCapacity) {
		t.Fatalf("the ceiling's refusal must be cloud.ErrRunnerCapacity, which receivers answer 202, got %v", err)
	}
}

// The forge holds at most half the runner slots, and GitHub may take the rest.
//
// A refused forge job stays in the forge's queue and starts on a later tick. A
// refused GitHub job is delivered again only a few times and only while a slot
// is free, and GitHub fails it after a day. cloud's own train keeps forge jobs
// queued most of the day, so a forge drained first held every slot and no GitHub
// workflow ran at all.
func TestTheForgeCannotHoldEveryRunnerSlot(t *testing.T) {
	s := jobService(t)
	max := s.State.k8s.limits.maxConcurrentRunners()
	forge := cloud.JobEvent{
		Provider: "forge", Org: "hanzo", Repo: "hanzo/cloud",
		URL: "https://git.hanzo.ai", Token: "tok", Labels: []string{"hanzo-build-linux-amd64"},
	}
	share := max - max/2
	for i := range share {
		forge.ID = int64(100 + i)
		if _, err := launch(s, context.Background(), forge); err != nil {
			t.Fatalf("forge runner %d of its %d refused early: %v", i+1, share, err)
		}
	}
	forge.ID = 999
	if _, err := launch(s, context.Background(), forge); !errors.Is(err, errTooManyRunners) {
		t.Fatalf("a forge runner past half of %d must be refused, got %v", max, err)
	}
	gh := cloud.JobEvent{
		Provider: "github", Org: "hanzo", Repo: "hanzoai/gui",
		Token: "jit", Labels: []string{"hanzo-build-linux-amd64"},
	}
	for i := range max - share {
		gh.ID = int64(200 + i)
		if _, err := launch(s, context.Background(), gh); err != nil {
			t.Fatalf("github runner %d of the %d the forge left refused: %v", i+1, max-share, err)
		}
	}
}

// Forge runners waiting for an arm64 node do not hold the forge's amd64 share.
//
// Five arm64 legs of cloud's train sat Pending on a full arm64 node and held
// the forge's whole half, so every new amd64 forge job was refused while the
// amd64 nodes were idle. Waiting pods of one architecture still cannot pile up
// past the half.
func TestWaitingArm64RunnersDoNotBlockAnAmd64ForgeJob(t *testing.T) {
	s := jobService(t)
	s.State.k8s.limits.maxRunners = 10 // the forge's share is 10 - 10/2 = 5
	ctx := context.Background()
	arm := cloud.JobEvent{
		Provider: "forge", Org: "hanzo", Repo: "hanzo-inc/cloud",
		URL: "https://git.hanzo.ai", Token: "tok", Labels: []string{"linux-arm64"},
	}
	for i := range 5 {
		arm.ID = int64(100 + i)
		if _, err := launch(s, ctx, arm); err != nil {
			t.Fatalf("waiting arm64 runner %d of 5 refused: %v", i+1, err)
		}
	}
	arm.ID = 105
	if _, err := launch(s, ctx, arm); !errors.Is(err, errTooManyRunners) {
		t.Fatalf("a sixth waiting arm64 forge runner must be refused, got %v", err)
	}
	amd := arm
	amd.ID, amd.Labels = 200, []string{"linux-amd64"}
	if _, err := launch(s, ctx, amd); err != nil {
		t.Fatalf("an amd64 forge job behind 5 waiting arm64 runners must be admitted, got %v", err)
	}
}

// Forge runners waiting on the xl pool do not hold the forge's ordinary share.
//
// 76 xl runners (cloud's gate and app's) sat Pending on a full xl pool and, counted with
// every other runner of their architecture, held the forge's whole half: no ordinary
// amd64 forge job was started, so a deploy that needs only an ordinary node waited over
// an hour. Waiting pods of one class still cannot pile up past the half.
func TestWaitingXLRunnersDoNotBlockAnOrdinaryForgeJob(t *testing.T) {
	s := jobService(t)
	s.State.k8s.limits.maxRunners = 10 // the forge's share is 10 - 10/2 = 5
	ctx := context.Background()
	xl := cloud.JobEvent{
		Provider: "forge", Org: "hanzo", Repo: "hanzo-inc/cloud",
		URL: "https://git.hanzo.ai", Token: "tok", Labels: []string{"xl-linux-amd64"},
	}
	for i := range 5 {
		xl.ID = int64(100 + i)
		if _, err := launch(s, ctx, xl); err != nil {
			t.Fatalf("waiting xl runner %d of 5 refused: %v", i+1, err)
		}
	}
	xl.ID = 105
	if _, err := launch(s, ctx, xl); !errors.Is(err, errTooManyRunners) {
		t.Fatalf("a sixth waiting xl forge runner must be refused, got %v", err)
	}
	plain := xl
	plain.ID, plain.Labels = 200, []string{"linux-amd64"}
	if _, err := launch(s, ctx, plain); err != nil {
		t.Fatalf("an ordinary amd64 forge job behind 5 waiting xl runners must be admitted, got %v", err)
	}
}

func TestRunnerNode(t *testing.T) {
	for _, c := range []struct {
		labels []string
		arch   string
		refuse bool
	}{
		{labels: []string{"linux-amd64"}, arch: "amd64"},
		{labels: []string{"linux-arm64"}, arch: "arm64"},
		{labels: []string{"hanzo-build-linux-amd64"}, arch: "amd64"},
		{labels: []string{"ubuntu-latest"}, arch: ""},
		{labels: []string{"depot-ubuntu-24.04"}, arch: ""},
		{labels: []string{}, arch: ""},
		{labels: []string{"self-hosted", "linux-arm64"}, arch: "arm64"},
		{labels: []string{"macos-arm64"}, refuse: true},
		{labels: []string{"win-amd64"}, refuse: true},
		{labels: []string{"windows-latest"}, refuse: true},
		// GitHub's own spellings, in its own case.
		{labels: []string{"self-hosted", "Linux", "ARM64"}, arch: "arm64"},
		{labels: []string{"self-hosted", "Linux", "X64"}, arch: "amd64"},
		{labels: []string{"self-hosted", "macOS", "ARM64"}, refuse: true},
		{labels: []string{"self-hosted", "Windows", "X64"}, refuse: true},
		{labels: []string{"macos14"}, refuse: true},
		{labels: []string{"osx-arm64"}, refuse: true},
		{labels: []string{"self_hosted_mac"}, refuse: true},
		{labels: []string{"self-hosted", "M1"}, refuse: true},
		{labels: []string{"win32"}, refuse: true},
		{labels: []string{"self-hosted", "windmill", "linux-arm64"}, arch: "arm64"},
		{labels: []string{"linux-win-cross"}, arch: ""},
	} {
		arch, err := runnerNode(c.labels)
		if c.refuse {
			if err == nil {
				t.Errorf("%v: wanted a refusal, got %q", c.labels, arch)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", c.labels, err)
		} else if arch != c.arch {
			t.Errorf("%v: got %q, want %q", c.labels, arch, c.arch)
		}
	}
}

// TestRunnerNameIsAValidObjectName: a repository name is not an object name.
//
// `safeLabel` admits `_` on purpose — a repository may carry one — so the two
// alphabets differ, and the Job name has to be composed in Kubernetes's. It was
// composed in the repository's instead, which the API server refuses:
//
//	Job.batch "runner-forge-hanzoai_extension-3903" is invalid: metadata.name:
//	a lowercase RFC 1123 subdomain must consist of lowercase alphanumeric
//	characters, '-' or '.'
//
// The create failed and the webhook answered 500, so every repository with `_`
// in its name could never run CI at all. Three of them were stuck when this was
// found.
func TestRunnerNameIsAValidObjectName(t *testing.T) {
	// The API server's own rule for a Job name.
	rfc1123 := regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

	for _, tc := range []struct{ in, want string }{
		{"hanzoai_extension", "hanzoai-extension"},
		{"hanzoai_universe", "hanzoai-universe"},
		{"Console", "console"},
		{"hanzo.ai", "hanzo.ai"},
		{"weird__name__", "weird--name"},
		{"_leading", "leading"},
	} {
		if got, want := cloud.RunnerName("forge", tc.in, 3903, 0), "runner-forge-"+tc.want+"-3903"; got != want {
			t.Errorf("RunnerName(%q) = %q, want %q", tc.in, got, want)
		}
	}

	// The composed name, which is what actually reaches the API server.
	for _, repo := range []string{"hanzoai_extension", "console", "hanzo.ai", "A_B_C", strings.Repeat("long_", 20)} {
		for try := range 10 {
			name := cloud.RunnerName("github", repo, 52011983745, try)
			if !rfc1123.MatchString(name) || len(name) > 63 {
				t.Errorf("Job name %q (repo %q) is not a valid object name", name, repo)
			}
		}
	}

	// A long repository is shortened and the id kept, so two of its jobs are two
	// names; a later try ends in -r<n>.
	long := strings.Repeat("a", 80)
	a, b := cloud.RunnerName("github", long, 52011983745, 0), cloud.RunnerName("github", long, 52011983746, 0)
	if a == b || !strings.HasSuffix(a, "-52011983745") || !strings.HasSuffix(b, "-52011983746") {
		t.Errorf("long repository: %q and %q, want each to end in its whole job id", a, b)
	}
	if got := cloud.RunnerName("github", long, 52011983745, 3); len(got) > 63 || !strings.HasSuffix(got, "-52011983745-r3") {
		t.Errorf("try 3 of a long repository: %q", got)
	}
	if got := cloud.RunnerName("github", "dev", 71, 1); got != "runner-github-dev-71-r1" {
		t.Errorf("try 1: %q, want runner-github-dev-71-r1", got)
	}
}

// A runner still waiting for a node holds no slot. The ceiling bounds what the
// nodes run; a Pending pod runs nothing, and counting it let amd64 runners queued
// on the one amd64 visor node fill the ceiling while the arm64 node sat idle.
// For GitHub the waiting pod is the queue itself — a refused job is delivered
// again only a few times — so a queue deeper than the ceiling is admitted.
func TestAWaitingRunnerHoldsNoSlot(t *testing.T) {
	s := jobService(t)
	max := s.State.k8s.limits.maxConcurrentRunners()
	ev := cloud.JobEvent{Provider: "github", Org: "hanzo", Repo: "hanzoai/gui", Token: "jit", Labels: []string{"linux-amd64"}}
	for i := range 2 * max {
		ev.ID = int64(300 + i)
		if _, err := launch(s, context.Background(), ev); err != nil {
			t.Fatalf("waiting runner %d refused, with no runner on any node: %v", i+1, err)
		}
	}
	ev.ID, ev.Labels = 999, []string{"linux-arm64"}
	if _, err := launch(s, context.Background(), ev); err != nil {
		t.Fatalf("an arm64 runner was refused behind %d amd64 runners that no node has taken: %v", 2*max, err)
	}
}

// bind stands in for the scheduler: the pod of runner Job name is placed on node.
func bind(t *testing.T, s *cloud.Service[state], name, node string) {
	t.Helper()
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name + "-pod", "namespace": "hanzo",
			"labels": map[string]any{"hanzo.ai/runner": "github", "job-name": name, "batch.kubernetes.io/job-name": name},
		},
		"spec": map[string]any{"nodeName": node},
	}}
	if _, err := s.State.k8s.dyn.Resource(k8s.Pods).Namespace("hanzo").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("bind %s: %v", name, err)
	}
}

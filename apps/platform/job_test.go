package platform

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func jobService(t *testing.T) *cloud.Service[state] {
	t.Helper()
	t.Setenv(runnerImageEnv, "oci.hanzo.ai/hanzoai/git-runner:4")
	t.Setenv(runnerNamespaceEnv, "hanzo")
	return &cloud.Service[state]{Base: cloud.Base{Log: log.New("test"), Brand: "hanzo"}, State: state{k8s: fakeK8s()}}
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
	if spec["runtimeClassName"] != runnerRuntimeClass {
		t.Fatalf("runtimeClassName %v", spec["runtimeClassName"])
	}
	c := spec["containers"].([]any)[0].(map[string]any)
	if c["image"] != "oci.hanzo.ai/hanzoai/git-runner:4" {
		t.Fatalf("image %v", c["image"])
	}
	env := map[string]string{}
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		env[m["name"].(string)] = m["value"].(string)
	}
	if env["GITEA_INSTANCE_URL"] != "https://git.hanzo.ai" || env["GITEA_RUNNER_REGISTRATION_TOKEN"] != "reg-token" {
		t.Fatalf("registration env %v", env)
	}
	if env["RUNNER_LABELS"] != "hanzo-build-linux-amd64:host" {
		t.Fatalf("labels %q", env["RUNNER_LABELS"])
	}
	if env["RUNNER_JITCONFIG"] != "" {
		t.Fatalf("a forge runner carries no JIT configuration")
	}
}

// A GitHub job carries only its just-in-time configuration.
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
	if strings.Join(names, ",") != "RUNNER_JITCONFIG,RUNNER_NAME" {
		t.Fatalf("env %v", names)
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
		Provider: "forge",
		Org:      "hanzo",
		Repo:     "hanzo/gui",
		ID:       42,
		URL:      "https://git.hanzo.ai",
		Token:    "tok",
		Labels:   []string{"hanzo-build-linux-amd64"},
	}

	// Fill the cluster to the ceiling, then ask for one more.
	max := s.State.k8s.limits.maxConcurrentRunners()
	for i := range max {
		ev.ID = int64(100 + i)
		if _, err := launch(s, context.Background(), ev); err != nil {
			t.Fatalf("runner %d of %d refused early: %v", i+1, max, err)
		}
	}

	ev.ID = 999
	_, err := launch(s, context.Background(), ev)
	if !errors.Is(err, errTooManyRunners) {
		t.Fatalf("over the ceiling must refuse with errTooManyRunners, got %v", err)
	}
}

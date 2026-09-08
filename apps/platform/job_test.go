package platform

import (
	"context"
	"errors"
	"regexp"
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
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT %q, want 0", env["GIT_TERMINAL_PROMPT"])
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
	if strings.Join(names, ",") != "RUNNER_JITCONFIG,GIT_TERMINAL_PROMPT,RUNNER_NAME" {
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

func TestRunnerNode(t *testing.T) {
	for _, c := range []struct {
		labels []string
		arch   string
		refuse bool
	}{
		{labels: []string{"linux-amd64"}, arch: "amd64"},
		{labels: []string{"linux-arm64"}, arch: "arm64"},
		{labels: []string{"hanzo-build-linux-amd64"}, arch: "amd64"},
		{labels: []string{"ubuntu-latest"}, arch: "amd64"},
		{labels: []string{"depot-ubuntu-24.04"}, arch: "amd64"},
		{labels: []string{}, arch: "amd64"},
		{labels: []string{"self-hosted", "linux-arm64"}, arch: "arm64"},
		{labels: []string{"macos-arm64"}, refuse: true},
		{labels: []string{"win-amd64"}, refuse: true},
		{labels: []string{"windows-latest"}, refuse: true},
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
// The create failed, the webhook answered 500, and the forge redelivered the
// same queued job forever — so every repository with `_` in its name could never
// run CI at all. Three of them were stuck when this was found.
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
		if got := dns1123(tc.in); got != tc.want {
			t.Errorf("dns1123(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// The composed name, which is what actually reaches the API server.
	for _, repo := range []string{"hanzoai_extension", "console", "hanzo.ai", "A_B_C"} {
		name := truncate("runner-forge-"+dns1123(repo)+"-3903", 63)
		if !rfc1123.MatchString(name) {
			t.Errorf("Job name %q (repo %q) is not a valid object name", name, repo)
		}
	}
}

package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The artifact lane accepts a hanzo.yml `binaries:` recipe verbatim and answers
// with the index URL a host will read — the ci lane's layout, byte for byte.
func TestRunnerArtifact_LaunchesAndIndexes(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, body := postRunner(t, app, testBuildTok, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "sha": "0abcdef1234567",
		"bucket": "plugins",
		"binaries": []any{
			map[string]any{"name": "cloud", "main": "./cmd/cloud", "platforms": []string{"linux/amd64", "linux/arm64"}},
			map[string]any{"name": "sdk", "run": "npm pack", "out": "*.tgz", "image": "node:22-bookworm"},
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("artifact build: want 202, got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "https://s3.hanzo.ai/plugins/hanzoai/cloud/0abcdef1234567/binaries.json"
	if resp.Index != want {
		t.Fatalf("index = %q, want %q", resp.Index, want)
	}
	if resp.Image != "" {
		t.Fatalf("artifact build must claim no image, got %q", resp.Image)
	}
}

// One initContainer per recipe entry, each in ITS OWN toolchain image, and the
// publisher — which is the only container that sees the object-store credential.
func TestArtifactJobSpec_ToolchainPerEntryAndCredentialOnlyInPublisher(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{
		{Name: "cloud", Main: "./cmd/cloud", Platforms: []string{"linux/amd64"}},
		{Name: "sdk", Run: "npm pack", Out: "*.tgz", Image: "node:22-bookworm"},
	}
	for i := range bins {
		if err := bins[i].validate(); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}
	job := k.artifactJobSpec("pf-artifact-x", "https://github.com/hanzoai/cloud", "v1.2.3", "v1.2.3", "https://s3.hanzo.ai/plugins/hanzoai/cloud/v1.2.3", "http://s3.hanzo.svc:9000/plugins/hanzoai/cloud/v1.2.3", bins)
	pod, _, err := unstructured.NestedMap(job.Object, "spec", "template", "spec")
	if err != nil {
		t.Fatalf("pod spec: %v", err)
	}
	inits, _ := pod["initContainers"].([]any)
	if len(inits) != 2 {
		t.Fatalf("initContainers = %d, want 2", len(inits))
	}
	for i, want := range []string{defaultToolchainImage, "docker.io/library/node:22-bookworm"} {
		c := inits[i].(map[string]any)
		if c["image"] != want {
			t.Errorf("initContainer[%d] image = %v, want %s", i, c["image"], want)
		}
		for _, e := range c["env"].([]any) {
			if _, secret := e.(map[string]any)["valueFrom"]; secret {
				t.Errorf("initContainer[%d] (runs the recipe) must carry NO secret env: %v", i, e)
			}
		}
	}
	pub := pod["containers"].([]any)[0].(map[string]any)
	if pub["image"] != publishImage {
		t.Errorf("publisher image = %v, want %s", pub["image"], publishImage)
	}
	if got := pub["command"].([]any)[2]; got != artifactPublishScript {
		t.Error("publisher must run the constant publish script, nothing recipe-supplied")
	}
	if pod["automountServiceAccountToken"] != false {
		t.Error("artifact build must mount no service-account token")
	}
}

// Bad recipes and lane confusion are 400s, not builds.
func TestRunnerArtifact_Rejects(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"image and binaries", map[string]any{"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1",
			"binaries": []any{map[string]any{"name": "x"}}}, http.StatusBadRequest},
		{"run without out", map[string]any{"repo": "https://github.com/hanzoai/cloud",
			"binaries": []any{map[string]any{"name": "x", "run": "make"}}}, http.StatusBadRequest},
		{"main and run", map[string]any{"repo": "https://github.com/hanzoai/cloud",
			"binaries": []any{map[string]any{"name": "x", "main": "./cmd/x", "run": "make", "out": "x"}}}, http.StatusBadRequest},
		{"branch ref needs an explicit tag", map[string]any{"repo": "https://github.com/hanzoai/cloud", "branch": "feat/x",
			"binaries": []any{map[string]any{"name": "x"}}}, http.StatusBadRequest},
		{"unallowlisted forge", map[string]any{"repo": "https://evil.example.com/hanzoai/cloud",
			"binaries": []any{map[string]any{"name": "x"}}}, http.StatusBadRequest},
	} {
		if code, body := postRunner(t, app, testBuildTok, tc.body); code != tc.want {
			t.Errorf("%s: want %d, got %d (%s)", tc.name, tc.want, code, body)
		}
	}
}

// An IAM org-admin publishes artifacts only for a forge owner its own org owns —
// the artifact-lane twin of the registry-namespace binding (H1).
func TestRunnerArtifact_IAMAdminBoundToItsOwnOwner(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	recipe := []any{map[string]any{"name": "x", "main": "./cmd/x"}}
	if code, body := postRunnerAs(t, app, "u", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "sha": "0abcdef", "binaries": recipe}); code != http.StatusAccepted {
		t.Fatalf("own-owner build: want 202, got %d (%s)", code, body)
	}
	if code, _ := postRunnerAs(t, app, "u", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/luxfi/node", "sha": "0abcdef", "binaries": recipe}); code != http.StatusForbidden {
		t.Fatalf("cross-owner build: want 403, got %d", code)
	}
}

// The recipe INTERPRETER is a shell script, so it is tested by running it — both
// lanes, against a real git repo, producing real files. This is what proves the
// declaration actually builds something rather than merely parsing.
func TestArtifactBuildScript_BuildsBothLanes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "go.mod"), "module demo\n\ngo 1.21\n")
	write(t, filepath.Join(src, "cmd", "demo", "main.go"), "package main\n\nfunc main() { println(\"demo\") }\n")
	write(t, filepath.Join(src, "pack.sh"), "#!/bin/sh\necho payload > demo-pkg.tgz\n")
	git(t, src, "init", "-q")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x", "--allow-empty")
	git(t, src, "add", "-A")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "src")

	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"go lane", []string{"NAME=demo", "MAIN=./cmd/demo", "RUN=", "OUT=", "LDFLAGS=-s -w", "PLATFORMS=linux/amd64"}, "demo-linux-amd64"},
		{"run lane", []string{"NAME=pkg", "MAIN=", "RUN=sh pack.sh", "OUT=*.tgz", "LDFLAGS=", "PLATFORMS="}, "demo-pkg.tgz"},
	} {
		w := t.TempDir()
		cmd := exec.Command("/bin/sh", "-c", strings.ReplaceAll(artifactBuildScript, "/w/", w+"/"))
		cmd.Env = append(append(os.Environ(), tc.env...),
			"REPO_URL="+src, "REF=HEAD", "HOME="+w, "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, out)
		}
		if _, err := os.Stat(filepath.Join(w, "dist", tc.want)); err != nil {
			t.Fatalf("%s: artifact %s not produced\n%s", tc.name, tc.want, out)
		}
		meta, _ := os.ReadFile(filepath.Join(w, "meta.txt"))
		if !strings.Contains(string(meta), tc.want) {
			t.Fatalf("%s: meta.txt does not name %s: %q", tc.name, tc.want, meta)
		}
		t.Logf("%s produced %s\n%s", tc.name, tc.want, out)
	}
}

// launchArtifactBuild is bounded by the SAME per-org build ceiling as the image
// lane — an artifact build cannot outrun a container build.
func TestLaunchArtifactBuild_SharesTheBuildCeiling(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{{Name: "x", Main: ".", Platforms: []string{"linux/amd64"}, Image: defaultToolchainImage}}
	for i := 0; i < k.limits.maxConcurrentBuilds(); i++ {
		if _, err := k.launchArtifactBuild(context.Background(), "https://github.com/hanzoai/cloud", "v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_"+string(rune('a'+i))); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
	if _, err := k.launchArtifactBuild(context.Background(), "https://github.com/hanzoai/cloud", "v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_over"); err != errTooManyBuilds {
		t.Fatalf("over the ceiling: want errTooManyBuilds, got %v", err)
	}
	list, _ := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != k.limits.maxConcurrentBuilds() {
		t.Fatalf("launched %d Jobs, want %d", len(list.Items), k.limits.maxConcurrentBuilds())
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

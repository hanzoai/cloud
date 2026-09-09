package platform

// The architectures a build publishes.
//
// These exist because ghcr.io/hanzoai/sql:18 and ghcr.io/hanzoai/git:1.26.50 are
// single-architecture amd64 manifests, so the forge's database and the forge
// itself cannot be scheduled on the arm64 node at all — one machine carries the
// database, the forge and every build while the other sits idle, and nothing in
// either image says why.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// argvHas reports the value of the first `--opt <prefix>…` element, and whether
// there was one. Matching the pair rather than a substring is what stops a test
// from passing on a flag that reads similarly.
func argvOpt(cmd []any, prefix string) (string, bool) {
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] != "--opt" {
			continue
		}
		if v, ok := cmd[i+1].(string); ok && strings.HasPrefix(v, prefix) {
			return strings.TrimPrefix(v, prefix), true
		}
	}
	return "", false
}

func TestNoPlatformBuildsExactlyWhatItBuildsToday(t *testing.T) {
	// Rule of the change: a caller that says nothing gets what it got before —
	// one image, on the default architecture, with no platform in the argv to
	// double the cost of every build in the fleet.
	cmd := buildFrontendCmdRev("https://git.example/x.git#main", "Dockerfile", "ghcr.io/hanzoai/x:v1", "ghcr.io/hanzoai/x:v1", "", nil)
	if v, ok := argvOpt(cmd, "platform="); ok {
		t.Fatalf("a build naming no platform emitted platform=%q", v)
	}
}

func TestOnePlatformIsNamedInTheArgv(t *testing.T) {
	cmd := buildFrontendCmdRev("https://git.example/x.git#main", "Dockerfile", "ghcr.io/hanzoai/x:v1", "ghcr.io/hanzoai/x:v1-arm64", "", []string{"linux/arm64"})
	v, ok := argvOpt(cmd, "platform=")
	if !ok || v != "linux/arm64" {
		t.Fatalf("platform opt = %q (present %v), want linux/arm64", v, ok)
	}
}

func TestTwoPlatformsAreOneOptSoBuildkitWritesOneIndex(t *testing.T) {
	// buildkit reads a comma-separated list as one solve over several platforms
	// and exports it as a manifest index. Two separate `--opt platform=` elements
	// would leave the last one standing and publish a single architecture under a
	// name that claims both.
	cmd := indexCmd("ghcr.io/hanzoai/x:v1", []string{"linux/amd64", "linux/arm64"})
	v, ok := argvOpt(cmd, "platform=")
	if !ok || v != "linux/amd64,linux/arm64" {
		t.Fatalf("index platform opt = %q (present %v), want both", v, ok)
	}
	if n := slices.IndexFunc(cmd, func(a any) bool { return a == "--output" }); n < 0 {
		t.Fatal("the index build exports nothing")
	}
}

func TestIndexBuildPassesNothingCallerWroteThroughAShell(t *testing.T) {
	// The script is a constant and the image ref is argv. If a ref could reach the
	// script text, an image name would be a command.
	cmd := indexCmd("ghcr.io/hanzoai/x:v1", []string{"linux/amd64", "linux/arm64"})
	script, ok := cmd[2].(string)
	if !ok {
		t.Fatalf("cmd[2] = %#v, want the script", cmd[2])
	}
	if strings.Contains(script, "hanzoai") || strings.Contains(script, "linux/") {
		t.Fatalf("the index script interpolates a caller value:\n%s", script)
	}
	if src, ok := argvOpt(cmd, "build-arg:SRC="); !ok || src != "ghcr.io/hanzoai/x:v1" {
		t.Fatalf("SRC = %q (present %v), want the image ref as its own argv element", src, ok)
	}
}

func TestFanOutPublishesEachArchitectureAtItsOwnTag(t *testing.T) {
	for image, want := range map[string]string{
		"ghcr.io/hanzoai/x:v1.2.3": "ghcr.io/hanzoai/x:v1.2.3-arm64",
		"ghcr.io/hanzoai/x":        "ghcr.io/hanzoai/x:latest-arm64",
		"oci.hanzo.ai:5000/h/x:v1": "oci.hanzo.ai:5000/h/x:v1-arm64",
	} {
		if got := archTag(image, "arm64"); got != want {
			t.Errorf("archTag(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestBuildPlatformsRefusesWhatWeHaveNoNodeFor(t *testing.T) {
	// Both nodes register qemu-binfmt with the F flag, so an unlisted platform
	// would build — correctly, ten times slow, with nothing in the output to say
	// it was emulated. Refusing is the only outcome a caller can see.
	for _, p := range []string{"linux/riscv64", "linux/arm/v7", "windows/amd64", "amd64", "", "linux/amd64,linux/arm64"} {
		if _, err := buildPlatforms([]string{p}); err == nil {
			t.Errorf("buildPlatforms(%q) was accepted", p)
		}
	}
}

func TestBuildPlatformsIsOneListPerRequestedSet(t *testing.T) {
	// Two requests naming the same platforms in two orders are one build, so the
	// list they produce has to be one list.
	a, err := buildPlatforms([]string{"linux/arm64", "linux/amd64"})
	if err != nil {
		t.Fatalf("buildPlatforms: %v", err)
	}
	b, err := buildPlatforms([]string{"linux/amd64", "linux/arm64", "linux/amd64"})
	if err != nil {
		t.Fatalf("buildPlatforms: %v", err)
	}
	if !slices.Equal(a, b) {
		t.Fatalf("%v and %v are the same request and produced two lists", a, b)
	}
	if got, err := buildPlatforms(nil); err != nil || got != nil {
		t.Fatalf("buildPlatforms(nil) = %v, %v — an unasked question has no answer", got, err)
	}
}

func TestEveryPlatformNamesTheNodeThatBuildsItNatively(t *testing.T) {
	for p, arch := range nativeArch {
		if got := strings.TrimPrefix(p, "linux/"); got != arch {
			t.Errorf("platform %q builds on arch %q", p, arch)
		}
	}
}

func TestBuildJobNamesAreDistinctPerArchitecture(t *testing.T) {
	seen := map[string]string{}
	for _, suffix := range []string{"", "amd64", "arm64", indexJobSuffix} {
		n := buildJobName("bld_0123456789abcdef", suffix)
		if len(n) > 63 {
			t.Errorf("job name %q is longer than a k8s name may be", n)
		}
		if prev, dup := seen[n]; dup {
			t.Fatalf("suffix %q and %q both name Job %q — one build would collide with itself", prev, suffix, n)
		}
		seen[n] = suffix
	}
}

func TestAFanOutHalfStampsTheReleaseVersionNotItsOwnTag(t *testing.T) {
	// Each half publishes at the release tag plus its architecture. The version a
	// binary reports is read by people asking which release they are looking at,
	// and `v1.0.95-amd64` answers a question nobody asked while hiding the one
	// they did.
	cmd, err := buildFrontendCmdArgs("https://git.example/x.git#main", "Dockerfile",
		"ghcr.io/hanzoai/x:v1.0.95", "ghcr.io/hanzoai/x:v1.0.95-amd64", "", nil, []string{"linux/amd64"})
	if err != nil {
		t.Fatalf("buildFrontendCmdArgs: %v", err)
	}
	if v, _ := argvOpt(cmd, "build-arg:VERSION="); v != "v1.0.95" {
		t.Errorf("VERSION = %q, want v1.0.95", v)
	}
	if v, _ := argvOpt(cmd, "build-arg:GIT_VERSION="); v != "1.0.95" {
		t.Errorf("GIT_VERSION = %q, want 1.0.95", v)
	}
	out := cmd[len(cmd)-1].(string)
	if !strings.Contains(out, "name=ghcr.io/hanzoai/x:v1.0.95-amd64,") {
		t.Errorf("the half pushed %q, want the architecture's own tag", out)
	}
}

func TestEachArchitectureKeepsItsOwnLayerCache(t *testing.T) {
	// Two halves build the same repository at the same tag. One cache ref has each
	// overwriting the other's index every build, so neither ever reads its own and
	// the cache costs upload bandwidth to do nothing.
	amd := strings.Join(cacheArgs("ghcr.io/hanzoai/x", cacheArch([]string{"linux/amd64"})), " ")
	arm := strings.Join(cacheArgs("ghcr.io/hanzoai/x", cacheArch([]string{"linux/arm64"})), " ")
	if amd == arm {
		t.Fatalf("both architectures cache at the same ref: %s", amd)
	}
	// A build that named no platform keeps the ref every cache in the registry is
	// already under.
	if plain := strings.Join(cacheArgs("ghcr.io/hanzoai/x", cacheArch(nil)), " "); !strings.Contains(plain, "ghcr.io/hanzoai/x:buildcache,") && !strings.HasSuffix(plain, "ghcr.io/hanzoai/x:buildcache") {
		t.Fatalf("a platformless build moved its cache: %s", plain)
	}
}

// finishedJob is a Job the apiserver would report as terminal.
func finishedJob(ns, name string, ok bool) *unstructured.Unstructured {
	field := "failed"
	if ok {
		field = "succeeded"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"status":   map[string]any{field: int64(1)},
	}}
}

func fanOutService(t *testing.T, objs ...runtime.Object) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{k8s: fakeK8s(objs...)},
	}
}

func TestTheIndexIsNotWrittenUntilEveryArchitectureHasLanded(t *testing.T) {
	// An index naming an image that is not published is a broken tag. Half a
	// fan-out is exactly that, and it is the state the reconciler sees on most of
	// its ticks — the two halves run on different machines and do not finish
	// together.
	b := Build{ID: "bld_fan1", Org: "hanzo", Image: "ghcr.io/hanzoai/x:v1",
		JobName: buildJobName("bld_fan1", indexJobSuffix), Platforms: []string{"linux/amd64", "linux/arm64"}}
	s := fanOutService(t, finishedJob("hanzo", buildJobName(b.ID, "amd64"), true))

	done, ok, err := directBuildResult(s, context.Background(), b)
	if done || ok {
		t.Fatalf("one architecture done reported the build finished (done=%v ok=%v err=%v)", done, ok, err)
	}
	if _, gErr := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").
		Get(context.Background(), buildJobName(b.ID, indexJobSuffix), metav1.GetOptions{}); gErr == nil {
		t.Fatal("the index was written over an architecture that has not been published")
	}
}

func TestEveryArchitectureLandingWritesTheIndex(t *testing.T) {
	b := Build{ID: "bld_fan2", Org: "hanzo", Image: "ghcr.io/hanzoai/x:v1",
		JobName: buildJobName("bld_fan2", indexJobSuffix), Platforms: []string{"linux/amd64", "linux/arm64"}}
	s := fanOutService(t,
		finishedJob("hanzo", buildJobName(b.ID, "amd64"), true),
		finishedJob("hanzo", buildJobName(b.ID, "arm64"), true))

	if done, ok, err := directBuildResult(s, context.Background(), b); done || ok || err != nil {
		t.Fatalf("launching the index reported the build finished (done=%v ok=%v err=%v)", done, ok, err)
	}
	job, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").
		Get(context.Background(), buildJobName(b.ID, indexJobSuffix), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no index Job after every architecture landed: %v", err)
	}
	if !strings.Contains(fmt.Sprint(job.Object), "platform=linux/amd64,linux/arm64") {
		t.Fatal("the index Job does not name both platforms")
	}

	// Its own result is the build's, once it exists.
	s2 := fanOutService(t,
		finishedJob("hanzo", buildJobName(b.ID, "amd64"), true),
		finishedJob("hanzo", buildJobName(b.ID, "arm64"), true),
		finishedJob("hanzo", buildJobName(b.ID, indexJobSuffix), true))
	if done, ok, err := directBuildResult(s2, context.Background(), b); !done || !ok || err != nil {
		t.Fatalf("a written index left the build unfinished (done=%v ok=%v err=%v)", done, ok, err)
	}
}

func TestOneArchitectureFailingFailsTheBuildWithNoIndex(t *testing.T) {
	// There is nothing honest to join. The half that did build stays at its own
	// tag, where it can be looked at.
	b := Build{ID: "bld_fan3", Org: "hanzo", Image: "ghcr.io/hanzoai/x:v1",
		JobName: buildJobName("bld_fan3", indexJobSuffix), Platforms: []string{"linux/amd64", "linux/arm64"}}
	s := fanOutService(t,
		finishedJob("hanzo", buildJobName(b.ID, "amd64"), true),
		finishedJob("hanzo", buildJobName(b.ID, "arm64"), false))

	done, ok, err := directBuildResult(s, context.Background(), b)
	if !done || ok || err != nil {
		t.Fatalf("a failed architecture did not fail the build (done=%v ok=%v err=%v)", done, ok, err)
	}
	if _, gErr := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").
		Get(context.Background(), buildJobName(b.ID, indexJobSuffix), metav1.GetOptions{}); gErr == nil {
		t.Fatal("an index was written over a failed architecture")
	}
}

func TestASingleArchitectureBuildIsStillJustItsJob(t *testing.T) {
	b := Build{ID: "bld_one", Org: "hanzo", Image: "ghcr.io/hanzoai/x:v1", JobName: buildJobName("bld_one", "")}
	s := fanOutService(t, finishedJob("hanzo", b.JobName, true))
	if done, ok, err := directBuildResult(s, context.Background(), b); !done || !ok || err != nil {
		t.Fatalf("done=%v ok=%v err=%v", done, ok, err)
	}
}

func TestAFanOutIsGivenTimeForBothOfItsPhases(t *testing.T) {
	// One deadline for two phases calls a healthy index build a failure — in the
	// record only, while the image it is writing publishes anyway.
	one := stuckAfter(Build{Platforms: []string{"linux/amd64"}})
	two := stuckAfter(Build{Platforms: []string{"linux/amd64", "linux/arm64"}})
	if two <= one {
		t.Fatalf("a two-phase build is allowed %s and a one-phase build %s", two, one)
	}
}

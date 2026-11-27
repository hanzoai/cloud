package platform

// Where a build runs. Every Job the build door creates requires the CI pool — the
// nodes labelled hanzo.ai/visor=true — and tolerates the pool's taint. These tests
// render each kind of build Job through its real launcher and read the pod spec
// the apiserver would receive.

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// assertOnThePool fails unless the Job's pod selects exactly want and tolerates
// the pool's taint. A selector without the toleration is a pod that never
// schedules; a toleration without the selector permits the pool without
// requiring it.
func assertOnThePool(t *testing.T, job *unstructured.Unstructured, want map[string]string) {
	t.Helper()
	sel, found, err := unstructured.NestedStringMap(job.Object, "spec", "template", "spec", "nodeSelector")
	if err != nil || !found {
		t.Fatalf("%s: no nodeSelector (found=%v err=%v)", job.GetName(), found, err)
	}
	if !maps.Equal(sel, want) {
		t.Fatalf("%s: nodeSelector %v, want %v", job.GetName(), sel, want)
	}
	if !avoidsPressure(job.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)) {
		t.Fatalf("%s: a build lands on a node under disk pressure", job.GetName())
	}
	ts, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "tolerations")
	for _, x := range ts {
		m, _ := x.(map[string]any)
		if m["key"] == ciPool && m["operator"] == "Exists" && m["effect"] == "NoSchedule" {
			return
		}
	}
	t.Fatalf("%s: tolerations %v do not admit the %s taint, so the pod cannot reach the pool it selects", job.GetName(), ts, ciPool)
}

// avoidsPressure reports whether every required node-affinity term of a pod spec
// refuses a node the disk guard labelled under pressure.
func avoidsPressure(spec map[string]any) bool {
	terms, _, _ := unstructured.NestedSlice(spec, "affinity", "nodeAffinity",
		"requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
	for _, x := range terms {
		exprs, _, _ := unstructured.NestedSlice(x.(map[string]any), "matchExpressions")
		ok := false
		for _, e := range exprs {
			m := e.(map[string]any)
			ok = ok || m["key"] == k8s.DiskLabel && m["operator"] == "NotIn" && slices.Contains(m["values"].([]any), any(k8s.DiskPressure))
		}
		if !ok {
			return false
		}
	}
	return len(terms) > 0
}

func getJob(t *testing.T, k *k8sClient, name string) *unstructured.Unstructured {
	t.Helper()
	job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("job %s not created: %v", name, err)
	}
	return job
}

// An architecture build requires the pool AND its architecture: the default
// single-platform build (amd64) and each half of a fan-out.
func TestAnArchitectureBuildRequiresTheCIPoolAndItsArchitecture(t *testing.T) {
	k := fakeK8s()
	ctx := context.Background()
	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", "main",
		"ghcr.io/hanzoai/app:v1", "Dockerfile", "", "", "bld_one", nil, nil); err != nil {
		t.Fatalf("single-platform build: %v", err)
	}
	assertOnThePool(t, getJob(t, k, buildJobName("bld_one", "")),
		map[string]string{ciPool: "true", "kubernetes.io/arch": defaultArch})

	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", "main",
		"ghcr.io/hanzoai/app:v2", "Dockerfile", "", "", "bld_fan", nil,
		[]string{"linux/amd64", "linux/arm64"}); err != nil {
		t.Fatalf("fan-out: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		assertOnThePool(t, getJob(t, k, buildJobName("bld_fan", arch)),
			map[string]string{ciPool: "true", "kubernetes.io/arch": arch})
	}
}

// The tenant build lane renders through the same spec and lands on the same pool.
func TestATenantBuildRequiresTheCIPool(t *testing.T) {
	k := fakeK8s()
	a := mkApp("lux", "proj_x", "api")
	a.Source = "git"
	a.RepoURL = "https://github.com/hanzoai/app"
	a.RepoBranch = "main"
	name, err := k.launchBuildJob(context.Background(), estate, "lux", a, k.buildImageRef("lux", "api", "main"), "main", "bld_tenant")
	if err != nil {
		t.Fatalf("tenant build: %v", err)
	}
	assertOnThePool(t, getJob(t, k, name), map[string]string{ciPool: "true", "kubernetes.io/arch": defaultArch})
}

// The index solve names no architecture — it executes nothing and only joins two
// published images — and still requires the pool.
func TestTheArchlessIndexSolveRequiresTheCIPool(t *testing.T) {
	b := Build{ID: "bld_idx", Org: "hanzo", Image: "ghcr.io/hanzoai/x:v1",
		JobName: buildJobName("bld_idx", indexJobSuffix), Platforms: []string{"linux/amd64", "linux/arm64"}}
	s := fanOutService(t,
		finishedJob("hanzo", buildJobName(b.ID, "amd64"), true),
		finishedJob("hanzo", buildJobName(b.ID, "arm64"), true))
	if _, _, _, err := directBuildResult(s, context.Background(), b); err != nil {
		t.Fatalf("launching the index: %v", err)
	}
	assertOnThePool(t, getJob(t, s.State.k8s, buildJobName(b.ID, indexJobSuffix)), map[string]string{ciPool: "true"})
}

// The artifact lane cross-compiles, names no architecture, and still requires
// the pool.
func TestAnArtifactBuildRequiresTheCIPool(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{{Name: "x", Main: ".", Platforms: []string{"linux/amd64"}, Image: defaultToolchainImage}}
	name, err := k.launchArtifactBuild(context.Background(), "hanzo", "https://github.com/hanzoai/cloud",
		"v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_art")
	if err != nil {
		t.Fatalf("artifact build: %v", err)
	}
	assertOnThePool(t, getJob(t, k, name), map[string]string{ciPool: "true"})
}

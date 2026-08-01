package platform

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A build's working set is disk — clone, module cache, 112 plugin binaries, the
// exported layer cache. Without a declared request the pod is best-effort for
// ephemeral-storage: the scheduler will place it on a node with no room and the
// kubelet evicts it first, which killed a release ten minutes into its build with
// "The node was low on resource: ephemeral-storage … request is 0".
func TestBuildDeclaresItsDiskNeed(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil || len(cs) == 0 {
		t.Fatalf("no containers: %v", err)
	}
	res, ok := cs[0].(map[string]any)["resources"].(map[string]any)
	if !ok {
		t.Fatal("the build container declares no resources; it is best-effort for disk and evicted first")
	}
	req, _ := res["requests"].(map[string]any)
	if req["ephemeral-storage"] == nil {
		t.Error("no ephemeral-storage REQUEST: the scheduler cannot avoid a full node")
	}
	lim, _ := res["limits"].(map[string]any)
	if lim["ephemeral-storage"] == nil {
		t.Error("no ephemeral-storage LIMIT: one runaway build can fill the node for everything else")
	}
}

// The request has to reflect what a build actually uses. At 20Gi it was declared
// but still short: the kubelet evicted a build that had exceeded its request off
// a node already at its threshold, ten minutes in.
func TestTheDiskRequestMatchesARealBuild(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	res := cs[0].(map[string]any)["resources"].(map[string]any)
	req, _ := res["requests"].(map[string]any)
	if req["ephemeral-storage"] == "20Gi" {
		t.Error("20Gi was measured too small — a build evicted after exceeding it")
	}
}

// A finished build holds its buildkitd emptyDir until the POD is deleted, so the
// TTL is disk time. An hour of it, with a build every few minutes, is what filled
// the nodes.
func TestAFinishedBuildReleasesItsDiskPromptly(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	ttl, found, err := unstructured.NestedInt64(job.Object, "spec", "ttlSecondsAfterFinished")
	if err != nil || !found {
		t.Fatal("the build job must bound how long it holds a node's disk")
	}
	if ttl > 900 {
		t.Errorf("ttl %ds holds the build's disk long after the work is done", ttl)
	}
	// Still far longer than the terminal-state read, which polls every 5s.
	if ttl < 60 {
		t.Errorf("ttl %ds could delete the job before its result is read", ttl)
	}
}

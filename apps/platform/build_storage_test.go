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

package platform

import (
	"strings"
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

// The layer cache lives in the object store because it has to scale independently
// of a build node. A registry cache is pulled WHOLE onto the node before any of it
// can be read, so its size lands on the same 105GB disk that holds the images, the
// snapshots and the build's own working set — 79GB retained with 26GB left, and a
// build evicted fifteen minutes in.
func TestTheCacheDoesNotLandOnTheNodesDisk(t *testing.T) {
	var joined []string
	for _, a := range buildFrontendCmd("ctx", "Dockerfile", "ghcr.io/hanzoai/cloud:v1") {
		joined = append(joined, a.(string))
	}
	s := strings.Join(joined, " ")
	if strings.Contains(s, "type=registry") {
		t.Error("a registry cache is pulled whole onto the node; that is what filled the runner pool")
	}
	if !strings.Contains(s, "type=s3") {
		t.Error("the cache must live in the object store")
	}
}

// The credential reaches buildkit through the ENVIRONMENT, never argv — a build
// command is logged and inspectable, and a key on it is a key in the logs.
func TestTheCacheCredentialIsNeverOnArgv(t *testing.T) {
	var joined []string
	for _, a := range buildFrontendCmd("ctx", "Dockerfile", "ghcr.io/hanzoai/cloud:v1") {
		joined = append(joined, a.(string))
	}
	s := strings.Join(joined, " ")
	for _, leak := range []string{"access_key_id=", "secret_access_key=", "AWS_SECRET"} {
		if strings.Contains(s, leak) {
			t.Errorf("the build command carries %q; it belongs in the env", leak)
		}
	}
	// And it IS supplied, from the same optional Secret the artifact publisher uses.
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	env, _ := cs[0].(map[string]any)["env"].([]any)
	found := map[string]bool{}
	for _, e := range env {
		em := e.(map[string]any)
		name, _ := em["name"].(string)
		found[name] = true
		if strings.HasPrefix(name, "AWS_") {
			ref := em["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
			// Optional, so a cluster without the Secret still SCHEDULES the build:
			// a cache accelerates, it never gates.
			if ref["optional"] != true {
				t.Errorf("%s is required; an absent cache credential would make the Job unschedulable", name)
			}
		}
	}
	for _, n := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		if !found[n] {
			t.Errorf("%s is not supplied, so every build is a cache miss", n)
		}
	}
}

// The write goes to the INTERNAL endpoint. The public host is a CDN edge that
// takes no writes — the same split artifactPutBase makes.
func TestTheCacheWritesToTheInternalEndpoint(t *testing.T) {
	ep := s3CacheEndpoint()
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		t.Errorf("endpoint %q has no scheme; buildkit needs a URL", ep)
	}
	if strings.Contains(ep, "s3.hanzo.ai") {
		t.Error("that is the public edge; writes go to the in-cluster address")
	}
}

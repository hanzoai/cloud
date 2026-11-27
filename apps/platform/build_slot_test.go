package platform

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// jobSucceeded marks one Job terminal the way the Job controller would.
func jobSucceeded(t *testing.T, k *k8sClient, name string) {
	t.Helper()
	jobs := k.dyn.Resource(jobsGVR).Namespace(k.buildNS)
	job, err := jobs.Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	_ = unstructured.SetNestedField(job.Object, int64(1), "status", "succeeded")
	if _, err := jobs.Update(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update %s: %v", name, err)
	}
}

// jobPod gives a Job the pod the Job controller would create, bound to node, or
// Pending when node is empty.
func jobPod(t *testing.T, k *k8sClient, job, node string) {
	t.Helper()
	spec := map[string]any{}
	if node != "" {
		spec["nodeName"] = node
	}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": job + "-x", "namespace": k.buildNS,
			"labels": map[string]any{"batch.kubernetes.io/job-name": job, "job-name": job},
		},
		"spec": spec,
	}}
	if _, err := k.dyn.Resource(k8s.Pods).Namespace(k.buildNS).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod for %s: %v", job, err)
	}
}

// placesHeld is how many unfinished builds an org has: running, or waiting their turn.
func placesHeld(ctx context.Context, k *k8sClient, org string) (int, error) {
	lines, err := k.buildLines(ctx, org)
	return len(lines), err
}

func jobExists(k *k8sClient, name string) bool {
	_, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

// A fan-out whose arm64 half never reaches a node holds its org's build slot for
// as long as that half is Pending. When the reconciler gives up on it, the half
// that never started is deleted and the slot comes back; the half that finished
// is left for its TTL.
func TestAStuckBuildGivesBackItsSlot(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	both := []string{"linux/amd64", "linux/arm64"}
	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", "main",
		"ghcr.io/hanzoai/app:v1", "Dockerfile", "", "", "bld_stuck", nil, both); err != nil {
		t.Fatalf("launch: %v", err)
	}
	b := Build{ID: "bld_stuck", Org: "hanzo", JobName: buildJobName("bld_stuck", indexJobSuffix), Platforms: both}
	amd, arm := buildJobName(b.ID, "amd64"), buildJobName(b.ID, "arm64")
	jobSucceeded(t, k, amd)
	jobPod(t, k, amd, "node-amd64")
	jobPod(t, k, arm, "")

	if n, err := placesHeld(ctx, k, "hanzo"); err != nil || n != 1 {
		t.Fatalf("a Pending half should hold one slot, got %d (%v)", n, err)
	}
	released, err := k.releaseUnscheduled(ctx, b)
	if err != nil || released != 1 {
		t.Fatalf("released %d Jobs (%v), want the one Pending half", released, err)
	}
	if n, err := placesHeld(ctx, k, "hanzo"); err != nil || n != 0 {
		t.Fatalf("the slot is still held: %d active (%v)", n, err)
	}
	if jobExists(k, arm) {
		t.Error("the Job that never reached a node is still there")
	}
	if !jobExists(k, amd) {
		t.Error("the finished half was deleted; it is its TTL's to reclaim")
	}
}

// A Job whose pod reached a node is doing work, and a build that is slow is not
// stuck: an artifact build runs past buildDeadline and still publishes. It keeps
// its slot.
func TestAStartedBuildKeepsItsSlot(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", "main",
		"ghcr.io/hanzoai/app:v1", "Dockerfile", "", "", "bld_slow", nil, []string{"linux/amd64"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	b := Build{ID: "bld_slow", Org: "hanzo", JobName: buildJobName("bld_slow", "")}
	jobPod(t, k, b.JobName, "node-amd64")

	released, err := k.releaseUnscheduled(ctx, b)
	if err != nil || released != 0 {
		t.Fatalf("released %d Jobs (%v) from a build that is running", released, err)
	}
	if !jobExists(k, b.JobName) {
		t.Fatal("a running build's Job was deleted")
	}
	if n, _ := placesHeld(ctx, k, "hanzo"); n != 1 {
		t.Fatalf("a running build holds %d slots, want 1", n)
	}
}

// A Job the controller never gave a pod (a quota refusal, a pod that could not be
// created) did no work either, and is released the same way.
func TestABuildWithNoPodGivesBackItsSlot(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", "main",
		"ghcr.io/hanzoai/app:v1", "Dockerfile", "", "", "bld_nopod", nil, []string{"linux/arm64"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	b := Build{ID: "bld_nopod", Org: "hanzo", JobName: buildJobName("bld_nopod", "")}
	if released, err := k.releaseUnscheduled(ctx, b); err != nil || released != 1 {
		t.Fatalf("released %d Jobs (%v), want 1", released, err)
	}
	if n, _ := placesHeld(ctx, k, "hanzo"); n != 0 {
		t.Fatalf("%d slots still held", n)
	}
}

// A builder lost with its node (a spot reclaim, a scale-down) is started again
// rather than failing the build: every build Job, image, index and artifact,
// ignores a pod failure the cluster marked DisruptionTarget. backoffLimit stays
// 0, so a build that fails on its own is still one attempt.
func TestALostNodeDoesNotFailTheBuild(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{{Name: "cloud", Main: "./cmd/cloud", Platforms: []string{"linux/amd64"}}}
	if err := bins[0].validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for lane, job := range map[string]*unstructured.Unstructured{
		"image":    k.buildJobSpec(estate, "pf-runner-t", "bld_t", "hanzo", "runner", "push-hanzoai", "", []any{"buildctl-daemonless.sh"}, defaultArch, nil),
		"index":    k.buildJobSpec(estate, "pf-runner-t-index", "bld_t", "hanzo", "runner", "push-hanzoai", "", indexCmd("ghcr.io/hanzoai/x:v1", []string{"linux/amd64", "linux/arm64"}), "", nil),
		"artifact": k.artifactJobSpec("pf-artifact-t", "hanzo", "https://github.com/hanzoai/cloud", "v1.2.3", "v1.2.3", "https://s3.hanzo.ai/plugins/hanzoai/cloud/v1.2.3", "http://s3.hanzo.svc:9000/plugins/hanzoai/cloud/v1.2.3", bins),
	} {
		rules, _, _ := unstructured.NestedSlice(job.Object, "spec", "podFailurePolicy", "rules")
		ignored := false
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			conds, _ := rule["onPodConditions"].([]any)
			for _, c := range conds {
				if cm, _ := c.(map[string]any); rule["action"] == "Ignore" && cm["type"] == "DisruptionTarget" {
					ignored = true
				}
			}
		}
		if !ignored {
			t.Errorf("%s build Job counts a lost node as a failed build", lane)
		}
		if limit, _, _ := unstructured.NestedInt64(job.Object, "spec", "backoffLimit"); limit != 0 {
			t.Errorf("%s build Job retries its own failures (backoffLimit %d)", lane, limit)
		}
		if policy, _, _ := unstructured.NestedString(job.Object, "spec", "template", "spec", "restartPolicy"); policy != "Never" {
			t.Errorf("%s build Job has restartPolicy %q; a pod failure policy needs Never", lane, policy)
		}
	}
}

// A TENANT BUILD'S JOB NAME ENDS IN ITS BUILD ID, whatever the org and the app are called. An org
// with a long name (a wallet org's is 49 characters) pushed the id past the 63-character limit,
// and the cut name was the same for qaq1 and qaq10 and for two builds of one app.
func TestATenantBuildJobNameKeepsTheBuildID(t *testing.T) {
	const org = "wallet-evm-db4d55cb76473850-a5659f7dc386ec41"
	seen := map[string]string{}
	for _, c := range []struct{ slug, build string }{
		{"qaq1", "bld_aaaaaaaaaaaa1"}, {"qaq10", "bld_bbbbbbbbbbbb2"}, {"qaq1", "bld_cccccccccccc3"}, {"a-very-long-application-name-indeed", "bld_dddddddddddd4"},
	} {
		name := tenantBuildJobName(org, c.slug, c.build)
		if len(name) > 63 {
			t.Fatalf("%q is %d characters", name, len(name))
		}
		if !strings.HasSuffix(name, "-"+jobIDSuffix(c.build)) {
			t.Fatalf("%q does not end in its build id %q", name, jobIDSuffix(c.build))
		}
		if other, dup := seen[name]; dup {
			t.Fatalf("%q names both %s and %s/%s", name, other, c.slug, c.build)
		}
		seen[name] = c.slug + "/" + c.build
	}
}

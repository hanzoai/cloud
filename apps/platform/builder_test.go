package platform

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const cloudRepo = "https://git.hanzo.ai/hanzo-inc/cloud"

const mainSHA = "0123456789abcdef0123456789abcdef01234567"

// withBuilder is a cluster where cloud's builder is declared with replicas and,
// when ready is non-empty, a pod whose Ready condition is ready; and a GitHub
// that trusts mainSHA in hanzo-inc/cloud and nothing else.
func withBuilder(t *testing.T, replicas int64, ready string) *k8sClient {
	t.Helper()
	self, fh := selfGitHost, forgeHost
	selfGitHost, forgeHost = "hanzo.ai", "git.hanzo.ai"
	t.Cleanup(func() { selfGitHost, forgeHost = self, fh })
	t.Setenv("CLOUD_PLATFORM_BUILDERS", cloudRepo)
	serveGitHubPeer(t, &githubPeer{trusted: func(owner, repo, ref string) string {
		if owner == "hanzo-inc" && repo == "cloud" && ref == mainSHA {
			return "branch"
		}
		return ""
	}})
	dep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "builder-hanzo-inc-cloud", "namespace": "hanzo"},
		"spec":     map[string]any{"replicas": replicas},
	}}
	k := fakeK8s(dep)
	if ready != "" {
		pod := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{
				"name": "builder-hanzo-inc-cloud-0", "namespace": k.buildNS,
				"labels":            map[string]any{"app.kubernetes.io/name": "builder-hanzo-inc-cloud"},
				"creationTimestamp": time.Now().Add(-2 * builderWake).UTC().Format(time.RFC3339),
			},
			"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready}}},
		}}
		if _, err := k.dyn.Resource(k8s.Pods).Namespace(k.buildNS).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return k
}

func replicas(t *testing.T, k *k8sClient) int64 {
	t.Helper()
	d, err := k.dyn.Resource(k8s.Deployments).Namespace(k.buildNS).Get(context.Background(), "builder-hanzo-inc-cloud", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n, _, _ := unstructured.NestedInt64(d.Object, "spec", "replicas")
	return n
}

// cloudBuild builds cloud's release shape, or the shape the arguments change it to,
// and returns the Job's pod template labels, pod spec and container.
func cloudBuild(t *testing.T, k *k8sClient, l lane, repo, ref, target string, args map[string]string, platforms ...string) (map[string]string, map[string]any, map[string]any) {
	t.Helper()
	id := genID("bld")
	name, err := k.launchDirectBuild(context.Background(), l, "hanzo", "", repo, ref,
		"ghcr.io/hanzo-inc/cloud:v8.5.1", "Dockerfile", target, "", id, args, platforms)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(platforms) > 1 {
		name = buildJobName(id, nativeArch[platforms[0]])
	}
	j := job(t, k, name)
	labels, _, _ := unstructured.NestedStringMap(j.Object, "spec", "template", "metadata", "labels")
	pod := podSpec(j.Object)
	return labels, pod, pod["containers"].([]any)[0].(map[string]any)
}

func argv(c map[string]any) string {
	var s []string
	for _, a := range c["command"].([]any) {
		s = append(s, a.(string))
	}
	return strings.Join(s, " ")
}

// The release of a repository with a Ready builder is solved there, and the Job
// is only its client: it dials the builder over mTLS with the client identity,
// asks for no layer cache, holds no object-store key and no daemon state, and
// takes a fraction of a core.
func TestTheReleaseIsSolvedOnItsBuilder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := withBuilder(t, 1, "True")
		labels, pod, c := cloudBuild(t, k, estate, cloudRepo, mainSHA, "", nil, "linux/amd64")
		cmd := argv(c)
		if !strings.HasPrefix(cmd, "/bin/sh -c ") || !strings.Contains(cmd, " builder "+builderAddr("builder-hanzo-inc-cloud", k.buildNS)+" build ") {
			t.Fatalf("not a client of the builder: %s", cmd)
		}
		for _, gone := range []string{"buildctl-daemonless.sh", "--import-cache", "--export-cache", "type=s3", "buildcache"} {
			if strings.Contains(cmd, gone) {
				t.Errorf("a client argv carries %q: %s", gone, cmd)
			}
		}
		for _, kept := range []string{"--frontend=dockerfile.v0", "context=https://git.hanzo.ai/hanzo-inc/cloud.git#0123456789abcdef0123456789abcdef01234567",
			"build-arg:REVISION=0123456789abcdef0123456789abcdef01234567", "push=true", "--secret id=GIT_AUTH_TOKEN,env=GIT_AUTH_TOKEN"} {
			if !strings.Contains(cmd, kept) {
				t.Errorf("the solve lost %q: %s", kept, cmd)
			}
		}
		for _, e := range c["env"].([]any) {
			switch n := e.(map[string]any)["name"]; n {
			case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "BUILDKITD_FLAGS":
				t.Errorf("a client holds %s", n)
			}
		}
		if slices.ContainsFunc(pod["volumes"].([]any), named("buildkitd")) {
			t.Error("a client carries the daemon's state volume")
		}
		i := slices.IndexFunc(pod["volumes"].([]any), named("certs"))
		if i < 0 || pod["volumes"].([]any)[i].(map[string]any)["secret"].(map[string]any)["secretName"] != "builder-hanzo-inc-cloud-client-tls" {
			t.Errorf("no client identity mounted: %v", pod["volumes"])
		}
		if labels[builderLabel] != "builder-hanzo-inc-cloud" || labels["hanzo.ai/publish"] != "" {
			t.Errorf("pod labels %v: want the builder's admission label and no object-store egress", labels)
		}
		if sel := pod["nodeSelector"].(map[string]any); sel["kubernetes.io/arch"] != nil {
			t.Errorf("a client is pinned to an architecture: %v", sel)
		}
		req := c["resources"].(map[string]any)["requests"].(map[string]any)
		if req["cpu"] != "100m" {
			t.Errorf("a client reserves %v CPU; the compile runs in the builder", req["cpu"])
		}
		if c["securityContext"].(map[string]any)["readOnlyRootFilesystem"] != true {
			t.Error("a client runs on a writable root")
		}
	})
}

// Every other build keeps a daemon of its own: a commit GitHub does not say a
// published branch contains, a ref that is not a commit, a target or a declared
// build-arg (the dev image, laid on a caller-named BASE), another architecture,
// a fan-out, a repository with no builder, the same path on another host, and
// a builder that is up and broken.
func TestOnlyTheReleaseShapeReachesABuilder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		base := "ghcr.io/x/y@sha256:" + strings.Repeat("a", 64)
		other := "fedcba9876543210fedcba9876543210fedcba98"
		for _, c := range []struct {
			what, ready, repo, ref, target string
			args                           map[string]string
			platforms                      []string
		}{
			{"a commit GitHub does not trust", "True", cloudRepo, other, "", nil, []string{"linux/amd64"}},
			{"a branch name", "True", cloudRepo, "main", "", nil, []string{"linux/amd64"}},
			{"a target", "True", cloudRepo, mainSHA, "dev", nil, []string{"linux/amd64"}},
			{"a build-arg", "True", cloudRepo, mainSHA, "", map[string]string{"BASE": base}, []string{"linux/amd64"}},
			{"arm64", "True", cloudRepo, mainSHA, "", nil, []string{"linux/arm64"}},
			{"a fan-out", "True", cloudRepo, mainSHA, "", nil, []string{"linux/amd64", "linux/arm64"}},
			{"a repository with no builder", "True", "https://git.hanzo.ai/hanzo-inc/iam", mainSHA, "", nil, []string{"linux/amd64"}},
			{"the same path on another host", "True", "https://github.com/hanzoai/cloud", mainSHA, "", nil, []string{"linux/amd64"}},
			{"a builder whose pod has been up and not Ready", "False", cloudRepo, mainSHA, "", nil, []string{"linux/amd64"}},
		} {
			_, _, ctr := cloudBuild(t, withBuilder(t, 1, c.ready), estate, c.repo, c.ref, c.target, c.args, c.platforms...)
			if cmd := argv(ctr); !strings.HasPrefix(cmd, "buildctl-daemonless.sh build") {
				t.Errorf("%s reached the builder: %s", c.what, cmd)
			}
		}
	})
}

// A sleeping builder is woken by the build that needs it, and the build waits
// for it; one unused for builderIdle with no client left goes back to sleep.
func TestABuilderWakesForABuildAndSleepsWhenIdle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := withBuilder(t, 0, "")
		_, _, c := cloudBuild(t, k, estate, cloudRepo, mainSHA, "", nil, "linux/amd64")
		if !strings.HasPrefix(argv(c), "/bin/sh -c ") || replicas(t, k) != 1 {
			t.Fatalf("a sleeping builder was not woken for the release: replicas %d, %s", replicas(t, k), argv(c))
		}
		k.sleepBuilders(context.Background())
		if replicas(t, k) != 1 {
			t.Fatal("a builder just used was put to sleep")
		}
		deps := k.dyn.Resource(k8s.Deployments).Namespace(k.buildNS)
		d, _ := deps.Get(context.Background(), "builder-hanzo-inc-cloud", metav1.GetOptions{})
		d.SetAnnotations(map[string]string{builderUsed: time.Now().Add(-2 * builderIdle).UTC().Format(time.RFC3339)})
		if _, err := deps.Update(context.Background(), d, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		k.sleepBuilders(context.Background())
		if replicas(t, k) != 1 {
			t.Fatal("a builder with a client still running was put to sleep")
		}
		jobs, _ := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
		for _, j := range jobs.Items {
			jobSucceeded(t, k, j.GetName())
		}
		k.sleepBuilders(context.Background())
		if replicas(t, k) != 0 {
			t.Fatal("an idle builder kept its node")
		}
	})
}

// The client's wait ends in a reason the door can read: the builder's address
// and the dial error, on the line solveError keeps.
func TestABuilderThatNeverAnswersIsNamed(t *testing.T) {
	if !strings.Contains(builderScript, `echo "error: builder $addr did not answer within 600s: $why"`) {
		t.Fatal("the wait's last word does not name the builder")
	}
	log := "error: builder tcp://b:1234 did not answer within 600s: error: failed to list workers: connection refused"
	if got := solveError(log); !strings.Contains(got, "tcp://b:1234") || !strings.Contains(got, "connection refused") {
		t.Fatalf("reason %q", got)
	}
}

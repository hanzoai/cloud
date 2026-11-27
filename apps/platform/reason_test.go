package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// cloneFailure is the log tail buildctl leaves when the git context cannot be read:
// a private github.com repository fetched with no credential.
const cloneFailure = `#1 [internal] load git source https://github.com/hanzoai/decision.git#646b93cca94cc5a4453e1f7948ffc33241530327
#1 0.211 fatal: could not read Username for 'https://github.com': terminal prompts disabled
#1 ERROR: failed to fetch remote https://github.com/hanzoai/decision.git: exit status 128
------
 > [internal] load git source https://github.com/hanzoai/decision.git#646b93cca94cc5a4453e1f7948ffc33241530327:
------
error: failed to solve: failed to read dockerfile: failed to fetch remote https://github.com/hanzoai/decision.git: git stderr:
fatal: could not read Username for 'https://github.com': terminal prompts disabled
`

func TestAFailedSolveIsReadFromItsLastWords(t *testing.T) {
	got := solveError(cloneFailure)
	want := "error: failed to solve: failed to read dockerfile: failed to fetch remote https://github.com/hanzoai/decision.git: git stderr: fatal: could not read Username for 'https://github.com': terminal prompts disabled"
	if got != want {
		t.Fatalf("reason %q is not buildctl's error with the stderr it quotes", got)
	}
	step := "#9 [build 3/7] RUN cargo build\n#9 ERROR: process \"/bin/sh -c cargo build\" did not complete successfully: exit code: 101\n"
	if got := solveError(step); !strings.Contains(got, "exit code: 101") {
		t.Fatalf("with no final error line the failing step is the reason, got %q", got)
	}
	if got := solveError("\nbuildkitd: permission denied\n\n"); got != "buildkitd: permission denied" {
		t.Fatalf("with neither the last line is the reason, got %q", got)
	}
	if got := solveError("error: " + strings.Repeat("x", 4*reasonMax)); len(got) != reasonMax {
		t.Fatalf("a reason is bounded at %d bytes, got %d", reasonMax, len(got))
	}
}

func TestAPodSaysWhyItDidNotFinish(t *testing.T) {
	exited := corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
		Name: "buildkit", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: cloneFailure}},
	}}}}
	if got := podReason(exited); !strings.HasPrefix(got, "error: failed to solve:") {
		t.Fatalf("a customer build's solve runs as an init container and its error is the reason, got %q", got)
	}
	silent := corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "buildkit", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}},
	}}}}
	if got := podReason(silent); got != "buildkit: OOMKilled, exit 137" {
		t.Fatalf("a container that left no message is named with its exit, got %q", got)
	}
	waiting := corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "buildkit", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: `secret "gh-read" not found`}},
	}}}}
	if got := podReason(waiting); !strings.Contains(got, `secret "gh-read" not found`) {
		t.Fatalf("a pod waiting on a Secret names it, got %q", got)
	}
	evicted := corev1.Pod{Status: corev1.PodStatus{Reason: "Evicted", Message: "The node was low on resource: ephemeral-storage."}}
	if got := podReason(evicted); !strings.HasPrefix(got, "Evicted: ") {
		t.Fatalf("an eviction is the reason, got %q", got)
	}
	if got := podReason(corev1.Pod{}); got != "" {
		t.Fatalf("a pod with nothing to say says nothing, got %q", got)
	}
}

// The build that motivated this: a fan-out of a private GitHub repository whose
// arm64 half failed on the clone. The row read `failed` and nothing else, the Job
// was gone ten minutes later, and the door had no read for one build. Now the
// reconciler records the half's own error and GET /v1/build/:id answers it.
func TestAFailedBuildAnswersItsReasonAtTheDoor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b := Build{ID: "bld_why", Org: "hanzo", Status: "queued", Image: "ghcr.io/hanzoai/decision:sha-646b93c",
			JobName: buildJobName("bld_why", indexJobSuffix), Platforms: []string{"linux/amd64", "linux/arm64"}, CreatedAt: time.Now().Unix()}
		arm := buildJobName(b.ID, "arm64")
		k := fakeK8s(
			finishedJob("hanzo", buildJobName(b.ID, "amd64"), true),
			finishedJob("hanzo", arm, false))
		k.clientset = k8sfake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: arm + "-x", Namespace: "hanzo", Labels: map[string]string{"job-name": arm}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "buildkit", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: cloneFailure}},
			}}},
		})
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		ctx := context.Background()
		if err := store.InsertBuild(ctx, b); err != nil {
			t.Fatalf("insert: %v", err)
		}
		s := &cloud.Service[state]{Log: luxlog.New("test"), Brand: "hanzo", State: state{store: store, k8s: k, sitesHost: "hanzo.app"}}
		reconcileDirectBuilds(s, ctx)

		got, err := store.GetBuild(ctx, "hanzo", b.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status != "failed" || !strings.Contains(got.Reason, "could not read Username for 'https://github.com'") {
			t.Fatalf("the row must say why: status %q reason %q", got.Status, got.Reason)
		}

		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		routes(app, s)
		get := func(org, id string) (int, runnerBuildResp) {
			req := httptest.NewRequest(http.MethodGet, "/v1/build/"+id, nil)
			for h, v := range appIdentity("ci", org) {
				req.Header.Set(h, v)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			var r runnerBuildResp
			_ = json.Unmarshal(body, &r)
			return resp.StatusCode, r
		}
		code, r := get("hanzo", b.ID)
		if code != http.StatusOK || r.Status != "failed" || r.Reason != got.Reason || r.BuildJobID != b.ID {
			t.Fatalf("GET /v1/build/%s = %d %+v", b.ID, code, r)
		}
		if code, _ := get("lux", b.ID); code != http.StatusNotFound {
			t.Fatalf("another org's build is not found, got %d", code)
		}
	})
}

func TestADeadlineNamesWhatTheBuildWaitedOn(t *testing.T) {
	b := Build{ID: "bld_wait", Org: "hanzo", JobName: buildJobName("bld_wait", "")}
	k := fakeK8s()
	k.clientset = k8sfake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: b.JobName + "-x", Namespace: "hanzo", Labels: map[string]string{"job-name": b.JobName}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "buildkit", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: `secret "gh-read" not found`}},
		}}},
	})
	s := &cloud.Service[state]{Log: luxlog.New("test"), State: state{k8s: k}}
	if got := stuckReason(s, context.Background(), b); !strings.Contains(got, `secret "gh-read" not found`) {
		t.Fatalf("got %q", got)
	}
	s.State.k8s = fakeK8s()
	if got := stuckReason(s, context.Background(), b); got != fmt.Sprintf("no result within %s", buildDeadline) {
		t.Fatalf("with nothing to read the deadline is the reason, got %q", got)
	}
}

// A private repository on github.com is cloned by the frontend before any step
// runs, so the only credential that can read it is one presented to the git
// context. It rides the read credential's own admission and goes to github.com
// alone, never to the forge.
func TestAnAdmittedGitHubContextIsCheckedOutWithTheReadCredential(t *testing.T) {
	const repo = "https://github.com/hanzo-inc/kai.git"
	if _, _, ok := githubReadRepo("hanzo", repo); !ok {
		t.Fatalf("hanzo may not read %s", repo)
	}
	if _, _, ok := githubReadRepo("zoo", repo); ok {
		t.Fatalf("zoo may read %s", repo)
	}
	secrets := solveSecrets(estate, repo+"#646b93cca94cc5a4453e1f7948ffc33241530327", true)
	job := fakeK8s().buildJobSpec(estate, "pf-runner-t", "bld_t", "hanzo", "runner", "push-hanzoai",
		"ghcr.io/hanzoai/decision:v1", []any{"buildctl-daemonless.sh", "build"}, "amd64", secrets)
	c := podSpec(job.Object)["containers"].([]any)[0].(map[string]any)
	argv := fmt.Sprint(c["command"])
	if !strings.Contains(argv, "id=GIT_AUTH_TOKEN.github.com,env=GIT_AUTH_TOKEN_github_com") {
		t.Fatalf("the context credential is not declared: %s", argv)
	}
	if strings.Contains(argv, "id=GIT_AUTH_TOKEN,") {
		t.Fatalf("the forge credential is declared for a github.com context: %s", argv)
	}
	env := fmt.Sprint(c["env"])
	if !strings.Contains(env, "name:GIT_AUTH_TOKEN_github_com") || !strings.Contains(env, "name:"+githubReadSecret) {
		t.Fatalf("the context credential is not sourced from %s: %s", githubReadSecret, env)
	}
	if c["terminationMessagePolicy"] != "FallbackToLogsOnError" {
		t.Fatal("a failed solve leaves no message to read its reason from")
	}
	for _, s := range solveSecrets(estate, repo+"#main", false) {
		if strings.HasPrefix(s.id, "GIT_AUTH_TOKEN") {
			t.Fatalf("an unadmitted github.com build holds %s", s.id)
		}
	}
}

// A build at its deadline with nothing failed says whether it never got a node
// (and the scheduler's reason) or was running, where and at which step.
func TestADeadlineSaysWhetherTheBuildQueuedOrRan(t *testing.T) {
	b := Build{ID: "bld_slow", Org: "hanzo", JobName: buildJobName("bld_slow", "")}
	created := metav1.NewTime(time.Now().Add(-46 * time.Minute))
	meta := metav1.ObjectMeta{Name: b.JobName + "-x", Namespace: "hanzo", Labels: map[string]string{"job-name": b.JobName}, CreationTimestamp: created}
	k := fakeK8s()
	k.clientset = k8sfake.NewSimpleClientset(&corev1.Pod{ObjectMeta: meta, Status: corev1.PodStatus{
		Phase:      corev1.PodPending,
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/33 nodes are available: 24 Insufficient cpu."}},
	}})
	s := &cloud.Service[state]{Log: luxlog.New("test"), State: state{k8s: k}}
	if got := stuckReason(s, context.Background(), b); !strings.Contains(got, "pending 46m") || !strings.Contains(got, "24 Insufficient cpu") {
		t.Fatalf("a build that never got a node: %q", got)
	}

	started := metav1.NewTime(created.Add(2 * time.Minute))
	k.clientset = k8sfake.NewSimpleClientset(&corev1.Pod{ObjectMeta: meta, Spec: corev1.PodSpec{NodeName: "i-0abc"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &started}})
	if got := stuckReason(s, context.Background(), b); !strings.Contains(got, "waited 2m0s for a node, running 44m") || strings.Contains(got, "i-0abc") {
		t.Fatalf("a build that ran out of time: %q", got)
	}
}

func TestLastStepIsTheLastVertexTheLogNames(t *testing.T) {
	log := "#20 DONE 2.8s\n\n#21 [build  7/11] RUN --mount=type=cache,id=x   go build ./...\n#21 0.326 building 134 of 135 plugins\n"
	if got := lastStep(log); got != "#21 [build 7/11] RUN --mount=type=cache,id=x go build ./..." {
		t.Fatalf("got %q", got)
	}
	if lastStep("no vertices here") != "" {
		t.Fatal("a log naming no step has no last step")
	}
}

func TestParseBuildDigestAndReturnAtTheDoor(t *testing.T) {
	const wantDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	// BuildKit --metadata-file JSON.
	if got := parseBuildDigest(`{"containerimage.digest":"` + wantDigest + `","image.name":"ghcr.io/hanzo-inc/zrok:0.1.7"}`); got != wantDigest {
		t.Fatalf("metadata JSON: got %q, want %q", got, wantDigest)
	}
	// BuildKit log line fallback.
	if got := parseBuildDigest("#12 exporting manifest list " + wantDigest + " 0.1s done\n"); got != wantDigest {
		t.Fatalf("buildkit log: got %q, want %q", got, wantDigest)
	}
	// crane push output fallback.
	if got := parseBuildDigest("ghcr.io/hanzo-inc/zrok:0.1.7@" + wantDigest + "\n"); got != wantDigest {
		t.Fatalf("crane output: got %q, want %q", got, wantDigest)
	}
	if got := parseBuildDigest("not a digest"); got != "" {
		t.Fatalf("junk must return empty, got %q", got)
	}

	// Real BuildKit push log line fallback (single-arch export + push).
	if got := parseBuildDigest("#48 pushing manifest for ghcr.io/hanzoai/flow:1.8.12@" + wantDigest + " 1.7s done\n"); got != wantDigest {
		t.Fatalf("buildkit push log: got %q, want %q", got, wantDigest)
	}

	// Regression guard: neither buildFrontendCmdRev nor indexCmd may pass
	// --metadata-file /dev/termination-log because buildctl atomic-writes a
	// sibling /dev/.tmp-* file, which fails under rootless/readOnlyRootFilesystem.
	estateCmd := fmt.Sprint(buildFrontendCmdRev(estate, "https://github.com/hanzoai/zrok.git#main", "Dockerfile",
		"ghcr.io/hanzo-inc/zrok:0.1.7", "ghcr.io/hanzo-inc/zrok:0.1.7", "", nil))
	if strings.Contains(estateCmd, "/dev/termination-log") {
		t.Fatalf("estate build cmd must not write /dev/termination-log: %s", estateCmd)
	}
	idxCmd := fmt.Sprint(indexCmd("ghcr.io/hanzo-inc/zrok:0.1.7", []string{"linux/amd64", "linux/arm64"}))
	if strings.Contains(idxCmd, "/dev/termination-log") {
		t.Fatalf("index cmd must not write /dev/termination-log: %s", idxCmd)
	}

	// End-to-end: reconciler extracts digest from pod termination message and GET /v1/build/:id returns it.
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b := Build{ID: "bld_dig", Org: "hanzo", Status: "queued", Image: "ghcr.io/hanzo-inc/zrok:0.1.7",
			JobName: buildJobName("bld_dig", ""), Tags: []string{"0.1.7"}, CreatedAt: time.Now().Unix()}
		k := fakeK8s(finishedJob("hanzo", b.JobName, true))
		k.clientset = k8sfake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: b.JobName + "-p", Namespace: "hanzo", Labels: map[string]string{"job-name": b.JobName}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "buildkit",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 0,
					Message:  `{"containerimage.digest":"` + wantDigest + `"}`,
				}},
			}}},
		})
		store, err := openStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		ctx := context.Background()
		if err := store.InsertBuild(ctx, b); err != nil {
			t.Fatalf("insert: %v", err)
		}
		s := &cloud.Service[state]{Log: luxlog.New("test"), Brand: "hanzo", State: state{store: store, k8s: k, sitesHost: "hanzo.app"}}
		reconcileDirectBuilds(s, ctx)

		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		routes(app, s)
		req := httptest.NewRequest(http.MethodGet, "/v1/build/"+b.ID, nil)
		for h, v := range appIdentity("ci", "hanzo") {
			req.Header.Set(h, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		var r runnerBuildResp
		_ = json.Unmarshal(body, &r)
		if resp.StatusCode != http.StatusOK || r.Status != "succeeded" || r.Digest != wantDigest {
			t.Fatalf("GET /v1/build/%s = %d %+v (want digest %s)", b.ID, resp.StatusCode, r, wantDigest)
		}
	})
}

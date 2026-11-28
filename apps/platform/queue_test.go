package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A job the forge still holds queued gets a runner, oldest first, on one
// registration token for the pass. A job that has its runner, a namespace no org
// owns and a platform no node serves are passed over, and the forge's half of the
// ceiling for an architecture refuses the rest of it — six slots, three of them
// the forge's per architecture.
func TestQueuedStartsWhatTheDeliveriesDidNot(t *testing.T) {
	s := jobService(t)
	s.State.k8s.limits.maxRunners = 6
	ctx := context.Background()
	if _, err := launch(s, ctx, cloud.JobEvent{Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 1,
		Labels: []string{"linux-arm64"}, URL: "https://git.hanzo.ai", Token: "reg"}); err != nil {
		t.Fatalf("the runner job 1's delivery started: %v", err)
	}
	queue := []struct {
		id                 int
		owner, repo, label string
	}{
		{1, "hanzoai", "gui", "linux-arm64"},     // has its runner
		{2, "hanzo-apps", "chat", "linux-amd64"}, // started
		{3, "lux", "node", "linux-amd64"},        // non-existent brand alias is not a namespace here
		{4, "hanzoai", "app", "macos-arm64"},     // a host's job, not a pod's
		{5, "charts", "app", "linux-amd64"},      // started
		{6, "hanzoai", "web", "linux-amd64"},     // started, and the forge's amd64 half is full
		{7, "hanzoai", "site", "linux-amd64"},    // refused: the rest is GitHub's
	}
	tokens := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/admin/actions/jobs":
			jobs := []map[string]any{}
			for _, j := range queue {
				jobs = append(jobs, map[string]any{"id": j.id, "labels": []string{j.label},
					"url": fmt.Sprintf("https://git.hanzo.ai/v1/repos/%s/%s/actions/jobs/%d", j.owner, j.repo, j.id)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs, "total_count": len(jobs)})
		case "POST /v1/admin/actions/runners/registration-token":
			tokens++
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "reg"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	fc, err := forge.New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}

	if n := queued(s, ctx, fc.Machine()); n != 3 {
		t.Fatalf("started %d, want 3", n)
	}
	list, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, j := range list.Items {
		names = append(names, j.GetName())
	}
	slices.Sort(names)
	if want := []string{"runner-forge-app-5", "runner-forge-chat-2", "runner-forge-gui-1", "runner-forge-web-6"}; !slices.Equal(names, want) {
		t.Fatalf("runners %v, want %v", names, want)
	}
	if tokens != 1 {
		t.Fatalf("%d registration tokens, want one for the pass", tokens)
	}
}

// A full arm64 share does not end the pass. The older arm64 job waits, and the
// newer amd64 job behind it starts: the queue is oldest first, so ending the pass
// at the first refusal left every amd64 job behind arm64 legs that could not run.
func TestAFullArm64ShareDoesNotHoldBackAnAmd64Job(t *testing.T) {
	s := jobService(t)
	s.State.k8s.limits.maxRunners = 6 // the forge's share is 3 per architecture
	ctx := context.Background()
	for id := int64(1); id <= 3; id++ {
		if _, err := launch(s, ctx, cloud.JobEvent{Org: "hanzo", Provider: "forge", Repo: "hanzo-inc/cloud", ID: id,
			Labels: []string{"linux-arm64"}, URL: "https://git.hanzo.ai", Token: "reg"}); err != nil {
			t.Fatalf("waiting arm64 runner %d: %v", id, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/admin/actions/jobs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "jobs": []map[string]any{
				{"id": 4, "labels": []string{"linux-arm64"}, "url": "https://git.hanzo.ai/v1/repos/hanzo-inc/cloud/actions/jobs/4"},
				{"id": 5, "labels": []string{"linux-amd64"}, "url": "https://git.hanzo.ai/v1/repos/hanzo-inc/cloud/actions/jobs/5"},
			}})
		case "POST /v1/admin/actions/runners/registration-token":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "reg"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	fc, err := forge.New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if n := queued(s, ctx, fc.Machine()); n != 1 {
		t.Fatalf("started %d, want 1 (the amd64 job)", n)
	}
	if _, err := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo").Get(ctx, "runner-forge-cloud-5", metav1.GetOptions{}); err != nil {
		t.Fatalf("the amd64 job got no runner: %v", err)
	}
}

// GitHub is asked to deliver again only a queued job whose every attempt was
// refused for capacity or a fault: oldest first, and no more than the free slots.
func TestRedeliverAsksAgainForJobsNeverHandedOver(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		now := time.Now()
		ago := func(d time.Duration) time.Time { return now.Add(-d) }
		job := func(id int64, guid string, when time.Time, code int, again bool) hook {
			return hook{ID: id, GUID: guid, DeliveredAt: when, StatusCode: code, Redelivery: again, Event: "workflow_job", Action: "queued"}
		}
		failed := [][]hook{{ // newest first, as GitHub lists them, over two pages
			job(90, "fresh", ago(20*time.Second), 500, false), // not waited out
			job(80, "handed", ago(3*time.Minute), 500, false), // a later attempt succeeded
			{ID: 75, GUID: "push", DeliveredAt: ago(4 * time.Minute), StatusCode: 502, Event: "push"},
			job(70, "young", ago(5*time.Minute), 429, false),  // due
			job(60, "denied", ago(6*time.Minute), 401, false), // neither capacity nor a fault
		}, {
			job(46, "spent", ago(10*time.Minute), 500, true), // five failed redeliveries
			job(45, "spent", ago(20*time.Minute), 500, true),
			job(44, "spent", ago(30*time.Minute), 500, true),
			job(43, "spent", ago(40*time.Minute), 500, true),
			job(42, "spent", ago(50*time.Minute), 500, true),
			job(41, "spent", ago(55*time.Minute), 500, false),
			job(30, "old", ago(2*time.Hour), 503, false),   // due, and the oldest
			job(10, "gone", ago(25*time.Hour), 500, false), // past the horizon
		}}
		succeeded := []hook{job(85, "handed", ago(2*time.Minute), 200, true)}

		var asked []int64
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/app/hook/deliveries":
				q := r.URL.Query()
				switch {
				case q.Get("status") == "success":
					_ = json.NewEncoder(w).Encode(succeeded)
				case q.Get("cursor") == "":
					w.Header().Set("Link", `<`+srv.URL+`/app/hook/deliveries?per_page=100&status=failure&cursor=2>; rel="next"`)
					_ = json.NewEncoder(w).Encode(failed[0])
				default:
					_ = json.NewEncoder(w).Encode(failed[1])
				}
			case r.Method == http.MethodPost:
				var id int64
				if _, err := fmt.Sscanf(r.URL.Path, "/app/hook/deliveries/%d/attempts", &id); err != nil {
					http.NotFound(w, r)
					return
				}
				asked = append(asked, id)
				w.WriteHeader(http.StatusAccepted)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		n, err := redeliver(context.Background(), srv.Client(), srv.URL, 1)
		if err != nil || n != 1 || !slices.Equal(asked, []int64{30}) {
			t.Fatalf("one free slot: asked again for %v (%d, %v), want [30]", asked, n, err)
		}
		asked = nil
		n, err = redeliver(context.Background(), srv.Client(), srv.URL, 5)
		if err != nil || n != 2 || !slices.Equal(asked, []int64{30, 70}) {
			t.Fatalf("five free slots: asked again for %v (%d, %v), want [30 70]", asked, n, err)
		}
	})
}

// A SPENT RUNNER IS RECLAIMED, NOT READ AS COVERAGE. An ephemeral runner takes
// whatever task the forge hands it, so the Job named for one job routinely runs
// another repo's and exits while this job is still queued. The pass that finds a
// FINISHED Job under that name deletes it and the next one launches a fresh
// runner — without this, nothing relaunches until the Job's TTL expires ten
// minutes later, and a release waits that long for a runner that is already gone.
func TestQueuedReclaimsASpentRunner(t *testing.T) {
	s := jobService(t)
	s.State.k8s.limits.maxRunners = 3
	ctx := context.Background()
	name, err := launch(s, ctx, cloud.JobEvent{Org: "hanzo", Provider: "forge", Repo: "hanzoai/gui", ID: 7,
		Labels: []string{"linux-amd64"}, URL: "https://git.hanzo.ai", Token: "reg"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo")
	spent, err := jobs.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The runner ran something and exited; the forge still holds job 7 queued.
	if err := unstructured.SetNestedField(spent.Object, int64(1), "status", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Update(ctx, spent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/admin/actions/jobs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "jobs": []map[string]any{{
				"id": 7, "labels": []string{"linux-amd64"},
				"url": "https://git.hanzo.ai/v1/repos/hanzoai/gui/actions/jobs/7",
			}}})
		case "POST /v1/admin/actions/runners/registration-token":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "reg"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	fc, err := forge.New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}

	if n := queued(s, ctx, fc.Machine()); n != 0 {
		t.Fatalf("started %d on the reclaiming pass, want 0", n)
	}
	if _, err := jobs.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the spent runner is still there: %v", err)
	}
	if n := queued(s, ctx, fc.Machine()); n != 1 {
		t.Fatalf("started %d on the next pass, want the relaunch", n)
	}
	if _, err := jobs.Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Fatalf("no fresh runner: %v", err)
	}
}

// serveRunners stands provider's reconcile up on a real socket, answering the
// nth call (from 0) with answer, and returns what each call stated.
func serveRunners(t *testing.T, answer func(ctx context.Context, n int, in *client.GitHubRunnersIn) *client.GitHubRunnersOut) func() []client.GitHubRunnersIn {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	var mu sync.Mutex
	var asked []client.GitHubRunnersIn
	cloud.ResetPlane()
	cloud.Plane().Post("/integrations/github/runners",
		func(ctx context.Context, in *client.GitHubRunnersIn) (*client.GitHubRunnersOut, error) {
			mu.Lock()
			n := len(asked)
			asked = append(asked, *in)
			mu.Unlock()
			return answer(ctx, n, in), nil
		},
		zip.WithOperationID(client.IntegrationsGitHubRunners))
	stop, err := cloud.ServePlane(providerpeer.App, nil)
	if err != nil {
		t.Fatalf("ServePlane(provider): %v", err)
	}
	t.Cleanup(func() { _ = stop(); cloud.ResetPlane() })
	for range 200 {
		if c, derr := net.Dial("unix", zip.SocketPath(providerpeer.App)); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() []client.GitHubRunnersIn {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// A RUNNER THAT TOOK ANOTHER JOB HOLDS THE NAME ITS OWN JOB'S RUNNER NEEDS. The
// reconcile states every GitHub runner Job and which of them finished, and
// provider answers the finished ones holding a queued job's name; platform
// deletes those, and the next reconcile starts that job's runner under the same
// name. A name platform did not state finished — a live runner's, a forge
// runner's — is never deleted, whatever the answer says.
func TestReconcileGitHubReclaimsASpentRunner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := jobService(t)
		ctx := context.Background()
		jobs := s.State.k8s.dyn.Resource(jobsGVR).Namespace("hanzo")
		start := func(ctx context.Context, provider string, id int64) string {
			ev := cloud.JobEvent{Org: "hanzo", Provider: provider, Repo: "hanzoai/dev", ID: id, Labels: []string{"linux-amd64"}, Token: "jit"}
			if provider == "forge" {
				ev.URL = "https://git.hanzo.ai"
			}
			name, err := launch(s, ctx, ev)
			if err != nil {
				t.Fatalf("launch %s %d: %v", provider, id, err)
			}
			return name
		}
		finish := func(name string) {
			j, err := jobs.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedField(j.Object, int64(1), "status", "succeeded"); err != nil {
				t.Fatal(err)
			}
			if _, err := jobs.Update(ctx, j, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		spent, live, other := start(ctx, "github", 71), start(ctx, "github", 72), start(ctx, "forge", 5)
		finish(spent)
		finish(other)

		asked := serveRunners(t, func(ctx context.Context, n int, in *client.GitHubRunnersIn) *client.GitHubRunnersOut {
			if n == 0 {
				return &client.GitHubRunnersOut{Reclaim: []string{spent, live, other}}
			}
			start(ctx, "github", 71) // what provider's launch reaches through cloud.OnWorkflowJob
			return &client.GitHubRunnersOut{Started: 1}
		})

		reconcileGitHub(s, ctx, "hanzo", 3)
		stated := asked()
		if len(stated) != 1 || stated[0].Free != 3 {
			t.Fatalf("asked %+v, want one reconcile stating 3 free slots", stated)
		}
		done := map[string]bool{}
		for _, j := range stated[0].Jobs {
			done[j.Name] = j.Done
		}
		if len(done) != 2 || !done[spent] || done[live] {
			t.Fatalf("stated %+v, want both GitHub runners, %s finished and %s not, and no forge runner", stated[0].Jobs, spent, live)
		}
		if _, err := jobs.Get(ctx, spent, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("the spent runner is still there: %v", err)
		}
		for _, name := range []string{live, other} {
			if _, err := jobs.Get(ctx, name, metav1.GetOptions{}); err != nil {
				t.Fatalf("%s was deleted, though this pass never stated it finished: %v", name, err)
			}
		}

		reconcileGitHub(s, ctx, "hanzo", 3)
		if _, err := jobs.Get(ctx, spent, metav1.GetOptions{}); err != nil {
			t.Fatalf("the next reconcile started no runner under %s: %v", spent, err)
		}
	})
}

// A REPOSITORY WITH A HUNDRED JOBS WAITING DOES NOT HOLD EVERY SLOT AHEAD OF ONE WITH A SINGLE JOB.
// The queue arrives oldest first, and cloud's pipeline queues seventeen jobs a push: platform's one
// deploy job, newest of all, waited behind every one of them.
func TestTheForgeQueueIsServedARepositoryAtATime(t *testing.T) {
	var jobs []forge.Job
	for id := int64(1); id <= 4; id++ {
		jobs = append(jobs, forge.Job{ID: id, Owner: "hanzo-inc", Repo: "cloud"})
	}
	jobs = append(jobs, forge.Job{ID: 5, Owner: "hanzo-inc", Repo: "platform"}, forge.Job{ID: 6, Owner: "hanzoai", Repo: "cli"}, forge.Job{ID: 7, Owner: "hanzoai", Repo: "cli"})

	var got []int64
	for _, j := range fairOrder(jobs) {
		got = append(got, j.ID)
	}
	if want := []int64{1, 5, 6, 2, 7, 3, 4}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v: each repository's oldest job, then each one's next", got, want)
	}
	if len(fairOrder(nil)) != 0 {
		t.Fatal("an empty queue is not empty")
	}
}

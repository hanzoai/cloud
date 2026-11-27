package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

func TestOurs(t *testing.T) {
	for _, c := range []struct {
		labels []string
		ours   bool
	}{
		{labels: []string{"linux-amd64"}, ours: true},
		{labels: []string{"linux-arm64"}, ours: true},
		{labels: []string{"hanzo-build-linux-amd64"}, ours: true},
		{labels: []string{"self-hosted"}, ours: true},
		{labels: []string{"Linux-AMD64"}, ours: true},
		// The classes: heavy, and xl on the xl pool.
		{labels: []string{"heavy-linux-amd64"}, ours: true},
		{labels: []string{"xl-linux-amd64"}, ours: true},
		// Spellings the retired org runners answered.
		{labels: []string{"lux-build-amd64"}, ours: true},
		{labels: []string{"lux-build-arm64"}, ours: true},
		{labels: []string{"zoo-build-linux-amd64"}, ours: true},
		{labels: []string{"build"}},
		{labels: []string{"zoo-build"}},
		{labels: []string{"ubuntu-latest"}},
		{labels: []string{"ubuntu-24.04"}},
		{labels: []string{"windows-latest"}},
		{labels: []string{"macos-14"}},
		{labels: nil},
		// macOS and Windows hosts are real machines on the forge, not pods this
		// path can mint.
		{labels: []string{"macos-arm64"}},
		{labels: []string{"win-amd64"}},
		{labels: []string{"windows-amd64"}},
		{labels: []string{"darwin-arm64"}},
		// self-hosted does not say Linux: GitHub's self-hosted Macs and Windows
		// boxes answer these, in this case.
		{labels: []string{"self-hosted", "macOS", "ARM64"}},
		{labels: []string{"self-hosted", "Windows", "X64"}},
		{labels: []string{"self-hosted", "osx"}},
		{labels: []string{"self-hosted", "macos14"}},
		{labels: []string{"Windows-ARM64"}},
		{labels: []string{"self-hosted", "Linux", "ARM64"}, ours: true},
		{labels: []string{"self-hosted", "M2"}},
		{labels: []string{"self-hosted", "win64"}},
		{labels: []string{"self_hosted_mac", "linux-arm64"}},
		{labels: []string{"self-hosted", "windmill"}, ours: true},
		{labels: []string{"self-hosted", "machine-learning"}, ours: true},
		{labels: []string{"self-hosted", "linux-win-cross"}, ours: true},
	} {
		if got := ours(c.labels); got != c.ours {
			t.Errorf("ours(%v) = %v, want %v", c.labels, got, c.ours)
		}
	}
}

// A launch the cluster refuses leaves the registration it minted, named for the
// job; the redelivery mints the same name. jitConfig must answer the id that
// withdraws it, and withdraw must delete exactly that registration.
func TestJitConfigThenWithdraw(t *testing.T) {
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/luxfi/actions/runners/generate-jitconfig":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"runner":{"id":4242,"name":"runner-github-bank-7"},"encoded_jit_config":"abc"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/orgs/luxfi/actions/runners/4242":
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	jit, id, err := jitConfig(context.Background(), "tok", "luxfi", "runner-github-bank-7", []string{"linux-amd64"})
	if err != nil || jit != "abc" || id != 4242 {
		t.Fatalf("jitConfig = %q, %d, %v; want abc, 4242, nil", jit, id, err)
	}
	if err := withdraw(context.Background(), "tok", "luxfi", id); err != nil || deleted == "" {
		t.Fatalf("withdraw: err %v, deleted %q", err, deleted)
	}
	// Already gone is done, not an error: GitHub removes a registration itself.
	if err := withdraw(context.Background(), "tok", "luxfi", 1); err != nil {
		t.Fatalf("withdraw of a gone registration: %v", err)
	}
}

// A redelivered job that has since finished must start no runner: GitHub never
// hands such a runner work, and it holds a slot until its deadline. jobStatus is
// what the handler asks before it launches anything.
func TestJobStatusReadsWhereTheJobIsNow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/hanzoai/ci/actions/jobs/1":
			_, _ = w.Write([]byte(`{"id":1,"status":"queued"}`))
		case "/repos/hanzoai/ci/actions/jobs/2":
			_, _ = w.Write([]byte(`{"id":2,"status":"completed","conclusion":"cancelled"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	for id, want := range map[int64]string{1: "queued", 2: "completed"} {
		if got, err := jobStatus(context.Background(), "tok", "hanzoai", "ci", id); err != nil || got != want {
			t.Fatalf("jobStatus(%d) = %q, %v; want %q", id, got, err, want)
		}
	}
	if _, err := jobStatus(context.Background(), "tok", "hanzoai", "ci", 3); err == nil {
		t.Fatal("an unknown job answered a status; the handler would trust it")
	}
}

// A job refused and then served by another job's runner leaves that other job
// short, so a runner is still owed while the org holds a queued job the runner
// could take — and only then.
func TestQueuedForFindsTheJobLeftWithoutARunner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("status") == "in_progress" {
			t.Errorf("a delivery read in-progress runs: %s", r.URL)
		}
		switch r.URL.Path {
		case "/installation/repositories":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"total_count":3,"repositories":[{"name":"console","owner":{"login":"hanzo-apps"}},` +
				`{"name":"hanzo.ai","owner":{"login":"hanzo-apps"}},{"name":"elsewhere","owner":{"login":"other"}}]}`))
		case "/repos/hanzo-apps/console/actions/runs":
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		case "/repos/hanzo-apps/hanzo.ai/actions/runs":
			_, _ = w.Write([]byte(`{"workflow_runs":[{"id":7}]}`))
		case "/repos/hanzo-apps/hanzo.ai/actions/runs/7/jobs":
			_, _ = w.Write([]byte(`{"jobs":[{"id":1,"status":"in_progress","labels":["linux-amd64"]},{"id":2,"status":"queued","labels":["linux-amd64"]},{"id":3,"status":"queued","labels":["linux-arm64"]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	for labels, want := range map[string]bool{"linux-amd64": true, "LINUX-ARM64": true, "linux-riscv64": false} {
		got, err := queuedFor(context.Background(), "tok", "hanzo-apps", []string{labels})
		if err != nil || got != want {
			t.Fatalf("queuedFor(%s) = %v, %v; want %v", labels, got, err, want)
		}
	}
	if _, err := queuedFor(context.Background(), "revoked", "hanzo-apps", []string{"linux-amd64"}); err == nil {
		t.Fatal("an installation GitHub cannot list answered; the handler would launch on a guess")
	}
}

// shortCase is one organization's queue and runners, and the runners owed.
type shortCase struct {
	queued []queuedJob
	regs   []ghRunner
	owed   []string // runner names, oldest job first
}

// A queued job is owed a runner unless an idle runner whose labels fit takes it.
// Idle is not busy, and online or starting; a finished runner never counts. The
// runner owed takes the first of the job's names nothing holds.
func TestShortCountsTheRunnersThatWillTakeAJob(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	job := func(id int64, age time.Duration, labels ...string) queuedJob {
		return queuedJob{ID: id, Status: "queued", Labels: labels, Created: now.Add(-age), Repo: "dev"}
	}
	reg := func(id int64, name, status string, busy bool, labels ...string) ghRunner {
		r := ghRunner{ID: id, Name: name, Status: status, Busy: busy}
		for _, l := range labels {
			r.Labels = append(r.Labels, struct {
				Name string `json:"name"`
			}{l})
		}
		return r
	}
	jobs := map[string]client.RunnerJob{
		"runner-github-dev-1": {Name: "runner-github-dev-1", Created: now.Add(-time.Minute).Unix()},      // starting
		"runner-github-dev-2": {Name: "runner-github-dev-2", Created: now.Add(-10 * time.Minute).Unix()}, // stuck, or busy
		"runner-github-dev-3": {Name: "runner-github-dev-3", Done: true},                                 // finished
	}
	amd := "linux-amd64"
	for name, c := range map[string]shortCase{
		"no runners: every job is owed, oldest first": {
			queued: []queuedJob{job(10, 5*time.Minute, amd), job(11, 9*time.Minute, amd)},
			owed:   []string{"runner-github-dev-11", "runner-github-dev-10"},
		},
		"an idle runner of any name takes the oldest": {
			queued: []queuedJob{job(10, 5*time.Minute, amd), job(11, 9*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "evo", "online", false, "self-hosted", amd)},
			owed:   []string{"runner-github-dev-10"},
		},
		"a runner whose Job is starting takes one": {
			queued: []queuedJob{job(10, 5*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "runner-github-dev-1", "offline", false, amd)},
		},
		"a runner a delivery is launching now takes one": {
			queued: []queuedJob{job(12, 30*time.Second, amd)},
			regs:   []ghRunner{reg(1, "runner-github-dev-12", "offline", false, amd)},
		},
		"a stuck runner takes none": {
			queued: []queuedJob{job(10, 5*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "runner-github-dev-2", "offline", false, amd)},
			owed:   []string{"runner-github-dev-10"},
		},
		"a busy runner takes none": {
			queued: []queuedJob{job(10, 5*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "evo", "online", true, amd), reg(2, "runner-github-dev-1", "online", true, amd)},
			owed:   []string{"runner-github-dev-10"},
		},
		"a finished runner takes none, though GitHub still says online": {
			queued: []queuedJob{job(10, 5*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "runner-github-dev-3", "online", false, amd)},
			owed:   []string{"runner-github-dev-10"},
		},
		"a runner takes only a job whose labels it has": {
			queued: []queuedJob{job(10, 5*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "evo", "online", false, "linux-arm64")},
			owed:   []string{"runner-github-dev-10"},
		},
		"the narrowest runner that fits is spent first": {
			queued: []queuedJob{job(10, 9*time.Minute, amd), job(11, 5*time.Minute, amd, "gpu")},
			regs:   []ghRunner{reg(1, "wide", "online", false, amd, "gpu"), reg(2, "narrow", "online", false, amd)},
		},
		"a job whose runner took another job gets a runner under its next name": {
			queued: []queuedJob{job(2, 20*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "runner-github-dev-2", "online", true, amd)},
			owed:   []string{"runner-github-dev-2-r1"},
		},
		"a job whose runner is stuck on the cluster gets no second one": {
			queued: []queuedJob{job(2, 20*time.Minute, amd)},
			regs:   []ghRunner{reg(1, "runner-github-dev-2", "offline", false, amd)},
		},
		"a job whose runner finished gets one under its next name at once": {
			queued: []queuedJob{job(3, 20*time.Minute, amd)},
			owed:   []string{"runner-github-dev-3-r1"},
		},
	} {
		var got []string
		for _, o := range short(c.queued, c.regs, jobs, nil, now).owed {
			got = append(got, cloud.RunnerName("github", o.job.Repo, o.job.ID, o.try))
		}
		if !slices.Equal(got, c.owed) {
			t.Errorf("%s: owed %v, want %v", name, got, c.owed)
		}
	}

	// The finished Job is answered for platform to reclaim.
	if p := short([]queuedJob{job(3, 20*time.Minute, amd)}, nil, jobs, nil, now); !slices.Equal(p.reclaim, []string{"runner-github-dev-3"}) {
		t.Fatalf("reclaim %v, want runner-github-dev-3", p.reclaim)
	}

	// A registration with no Job on an old job's name may be a delivery's whose
	// Job is not listed yet: the first sighting leaves the job alone, and only a
	// second sighting of the same registration withdraws it and reuses the name.
	queued, regs := []queuedJob{job(13, 20*time.Minute, amd)}, []ghRunner{reg(77, "runner-github-dev-13", "offline", false, amd)}
	first := short(queued, regs, jobs, nil, now)
	if len(first.owed) != 0 || !first.sighted[77] {
		t.Fatalf("first sighting: owed %+v, sighted %v; want nothing owed and 77 sighted", first.owed, first.sighted)
	}
	second := short(queued, regs, jobs, first.sighted, now)
	if len(second.owed) != 1 || second.owed[0].stale != 77 || second.owed[0].try != 0 {
		t.Fatalf("second sighting: owed %+v; want job 13 under its own name behind registration 77", second.owed)
	}
	if other := short(queued, regs, jobs, map[int64]bool{78: true}, now); len(other.owed) != 0 {
		t.Fatalf("a different registration seen before withdrew 77: %+v", other.owed)
	}
}

// fakeActions is GitHub as a reconcile reads it: the App's installations, one
// organization's granted repositories, runs and jobs, and the runners
// registered with it, which a mint adds and a withdraw removes.
type fakeActions struct {
	mu       sync.Mutex
	runs     map[string]map[string][]int64 // repository → run status → run ids
	jobs     map[int64][]map[string]any    // run id → its jobs
	runners  []map[string]any
	next     int64
	minted   []string
	gone     []int64
	strayOrg []string      // organizations read that no org connected
	fail     string        // a path answered 500
	hold     chan struct{} // when set, the first installations listing waits on it
	held     chan struct{} // closed once that listing is waiting
	once     sync.Once
}

func (f *fakeActions) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations" && f.hold != nil {
			first := false
			f.once.Do(func() { first = true; close(f.held) })
			if first {
				<-f.hold
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case path == f.fail:
			w.WriteHeader(http.StatusInternalServerError)
		case path == "/app/installations":
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[
				{"id":111,"account":{"login":"hanzoai","type":"Organization"}},
				{"id":222,"account":{"login":"luxfi","type":"Organization"}},
				{"id":333,"account":{"login":"zeekay","type":"User"}}]`))
		case strings.HasSuffix(path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_x"})
		case path == "/installation/repositories":
			repos := []map[string]any{}
			for name := range f.runs {
				repos = append(repos, map[string]any{"name": name, "owner": map[string]any{"login": "hanzoai"}, "pushed_at": "2026-10-08T11:00:00Z"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": repos})
		case !strings.HasPrefix(path, "/orgs/hanzoai/") && !strings.HasPrefix(path, "/repos/hanzoai/"):
			f.strayOrg = append(f.strayOrg, path)
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && path == "/orgs/hanzoai/actions/runners":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.runners), "runners": f.runners})
		case r.Method == http.MethodPost && path == "/orgs/hanzoai/actions/runners/generate-jitconfig":
			var in struct {
				Name   string   `json:"name"`
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			for _, x := range f.runners {
				if x["name"] == in.Name {
					w.WriteHeader(http.StatusConflict)
					return
				}
			}
			f.next++
			labels := []map[string]any{}
			for _, l := range in.Labels {
				labels = append(labels, map[string]any{"name": l, "type": "custom"})
			}
			f.runners = append(f.runners, map[string]any{"id": f.next, "name": in.Name, "status": "offline", "busy": false, "labels": labels})
			f.minted = append(f.minted, in.Name)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"runner": map[string]any{"id": f.next}, "encoded_jit_config": "jit"})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/orgs/hanzoai/actions/runners/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/orgs/hanzoai/actions/runners/"), 10, 64)
			f.gone = append(f.gone, id)
			f.runners = slices.DeleteFunc(f.runners, func(x map[string]any) bool { return fmt.Sprint(x["id"]) == fmt.Sprint(id) })
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(path, "/jobs"):
			var id int64
			_, _ = fmt.Sscanf(path[strings.LastIndex(path, "/runs/")+len("/runs/"):], "%d", &id)
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": f.jobs[id]})
		case strings.HasSuffix(path, "/actions/runs"):
			repo := strings.Split(path, "/")[3]
			var runs []map[string]any
			for _, id := range f.runs[repo][r.URL.Query().Get("status")] {
				runs = append(runs, map[string]any{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// launches is the launcher a reconcile reaches through cloud.OnWorkflowJob: it
// records each job it is asked for and answers refuse(n) for the nth (from 0).
func launches(t *testing.T, refuse func(n int) error) *[]cloud.JobEvent {
	t.Helper()
	var got []cloud.JobEvent
	cloud.RegisterRunner(func(_ context.Context, ev cloud.JobEvent) (string, error) {
		n := len(got)
		got = append(got, ev)
		if err := refuse(n); err != nil {
			return "", err
		}
		_, repo, _ := strings.Cut(ev.Repo, "/")
		return cloud.RunnerName("github", repo, ev.ID, ev.Try), nil
	})
	t.Cleanup(func() { cloud.RegisterRunner(nil) })
	return &got
}

// fitting refuses every launch past the first n with the cluster's ceiling.
func fitting(n int) func(int) error {
	return func(i int) error {
		if i >= n {
			return fmt.Errorf("platform: %w", cloud.ErrRunnerCapacity)
		}
		return nil
	}
}

// reconcileFixture is provider serving hanzoai — connected by hanzo — beside
// luxfi, installed but connected by no org, and a user account. hanzoai/dev has
// a queued run and one in progress, which the queued listing also answers, as a
// run that starts between the two listings is; hanzoai/cli has a queued run.
func reconcileFixture(t *testing.T) *fakeActions {
	t.Helper()
	ago := func(m int) string { return time.Now().Add(-time.Duration(m) * time.Minute).UTC().Format(time.RFC3339) }
	f := &fakeActions{
		runs: map[string]map[string][]int64{
			"dev": {"queued": {7, 8}, "in_progress": {8}},
			"cli": {"queued": {9}},
		},
		jobs: map[int64][]map[string]any{
			7: {
				{"id": 71, "status": "queued", "labels": []string{"linux-amd64"}, "created_at": ago(30)},
				{"id": 72, "status": "queued", "labels": []string{"ubuntu-latest"}, "created_at": ago(29)},
				{"id": 73, "status": "queued", "labels": []string{"self-hosted", "macOS", "ARM64"}, "created_at": ago(28)},
			},
			8: {
				{"id": 81, "status": "in_progress", "labels": []string{"linux-amd64"}, "created_at": ago(25)},
				{"id": 82, "status": "queued", "labels": []string{"linux-arm64"}, "created_at": ago(20)},
			},
			9: {{"id": 91, "status": "queued", "labels": []string{"linux-amd64"}, "created_at": ago(10)}},
		},
		next: 500,
	}
	withGithubApp(t, f.serve(t))
	newApp(t, newKMS(t))
	for _, c := range []Connection{
		{Org: "hanzo", Provider: "github", Label: "hanzoai", ExternalID: "111"},
		{Org: "hanzo", Provider: "github", Label: "zeekay", ExternalID: "333"},
	} {
		if err := mounted.State.store.Upsert(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	forget := func() {
		sightings.Lock()
		sightings.by = map[int64]map[int64]bool{}
		sightings.Unlock()
		failed.Lock()
		failed.at = map[int64]time.Time{}
		failed.Unlock()
	}
	forget()
	t.Cleanup(forget)
	return f
}

// Every queued job the fleet serves gets a runner, oldest first and no more than
// the free slots, minted under the name its Job takes and launched for the org
// that connected the installation. A job in a run already in progress counts,
// once however many listings answer it; a hosted runner's job, a self-hosted
// Mac's, and an installation no org connected do not.
func TestGitHubRunnersStartsWhatNoRunnerTakes(t *testing.T) {
	f := reconcileFixture(t)
	got := launches(t, fitting(10))
	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 2})
	if err != nil || out.Started != 2 {
		t.Fatalf("started %v, %v; want 2", out, err)
	}
	if want := []string{"runner-github-dev-71", "runner-github-dev-82"}; !slices.Equal(f.minted, want) {
		t.Fatalf("minted %v, want %v", f.minted, want)
	}
	for _, ev := range *got {
		if ev.Org != "hanzo" || ev.Provider != "github" || ev.Repo != "hanzoai/dev" || ev.Token != "jit" || ev.Try != 0 {
			t.Fatalf("launched %+v, want hanzo's runner for hanzoai/dev on its JIT configuration", ev)
		}
	}
	if f.strayOrg != nil {
		t.Fatalf("read %v, an account no org connected or no organization", f.strayOrg)
	}

	// The two runners are starting, so they take two jobs; the third job, and
	// only it, gets a runner of its own. Job 82, read in two listings, is one job:
	// read twice it would spend a second runner here.
	now := time.Now().Unix()
	out, err = planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5, Jobs: []client.RunnerJob{
		{Name: "runner-github-dev-71", Created: now}, {Name: "runner-github-dev-82", Created: now},
	}})
	if err != nil || out.Started != 1 || len(f.minted) != 3 || f.minted[2] != "runner-github-cli-91" {
		t.Fatalf("second pass started %v, %v, minted %v; want cli's job alone", out, err, f.minted)
	}

	// Covered, every job: nothing starts.
	out, err = planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5, Jobs: []client.RunnerJob{
		{Name: "runner-github-dev-71", Created: now}, {Name: "runner-github-dev-82", Created: now}, {Name: "runner-github-cli-91", Created: now},
	}})
	if err != nil || out.Started != 0 || len(f.minted) != 3 {
		t.Fatalf("covered pass started %v, %v, minted %v; want nothing", out, err, f.minted)
	}
}

// A full cluster ends the pass, and the registration minted for the refused
// launch is withdrawn, so the next pass can mint the same name.
func TestGitHubRunnersStopsAtTheCeiling(t *testing.T) {
	f := reconcileFixture(t)
	got := launches(t, fitting(1))
	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5})
	if err != nil || out.Started != 1 || len(*got) != 2 {
		t.Fatalf("started %v, %v, asked %d; want 1 started before the ceiling", out, err, len(*got))
	}
	if len(f.minted) != 2 || !slices.Equal(f.gone, []int64{502}) {
		t.Fatalf("minted %v, withdrew %v; want the refused runner's registration withdrawn and nothing minted after", f.minted, f.gone)
	}
}

// A launch that fails for any reason spends a slot of the pass. Counting only
// launches, a fault that failed every one minted and withdrew a registration for
// every owed job, every pass.
func TestGitHubRunnersCountsAFailedLaunch(t *testing.T) {
	f := reconcileFixture(t)
	launches(t, func(int) error { return errors.New("platform: runner: label names a platform with no node") })
	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 1})
	if err != nil || out.Started != 0 {
		t.Fatalf("started %v, %v; want none", out, err)
	}
	if len(f.minted) != 1 || len(f.gone) != 1 {
		t.Fatalf("minted %v, withdrew %v; want one attempt for one free slot", f.minted, f.gone)
	}
}

// A runner that took another job holds its own job's name, so the reconcile
// starts that job's runner under the next name, and launches it as that try.
func TestGitHubRunnersRenamesARunnerWhoseNameIsTaken(t *testing.T) {
	f := reconcileFixture(t)
	f.runs = map[string]map[string][]int64{"dev": {"queued": {7}}}
	f.jobs[7] = f.jobs[7][:1]
	f.runners = []map[string]any{{"id": 400, "name": "runner-github-dev-71", "status": "online", "busy": true,
		"labels": []map[string]any{{"name": "linux-amd64"}}}}
	got := launches(t, fitting(10))
	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5, Jobs: []client.RunnerJob{
		{Name: "runner-github-dev-71", Created: time.Now().Add(-20 * time.Minute).Unix()},
	}})
	if err != nil || out.Started != 1 || !slices.Equal(f.minted, []string{"runner-github-dev-71-r1"}) || (*got)[0].Try != 1 || f.gone != nil {
		t.Fatalf("started %+v, %v, minted %v, launched %+v, withdrew %v; want runner-github-dev-71-r1 as try 1",
			out, err, f.minted, *got, f.gone)
	}
}

// A finished Job under a job's runner name is answered for platform to reclaim,
// and the job's runner starts under its next name at once.
func TestGitHubRunnersReclaimsAFinishedRunner(t *testing.T) {
	f := reconcileFixture(t)
	f.runs = map[string]map[string][]int64{"dev": {"queued": {7}}}
	f.jobs[7] = f.jobs[7][:1]
	launches(t, fitting(10))
	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5, Jobs: []client.RunnerJob{
		{Name: "runner-github-dev-71", Done: true},
	}})
	if err != nil || out.Started != 1 || !slices.Equal(out.Reclaim, []string{"runner-github-dev-71"}) ||
		!slices.Equal(f.minted, []string{"runner-github-dev-71-r1"}) {
		t.Fatalf("%+v, %v, minted %v; want runner-github-dev-71 reclaimed and runner-github-dev-71-r1 started", out, err, f.minted)
	}
}

// A registration with no Job under an old job's name is not withdrawn when first
// seen — a delivery mints before its Job exists — and is withdrawn, and its name
// reused, when the next reconcile sees the same registration so.
func TestGitHubRunnersWithdrawsADeadRegistrationSeenTwice(t *testing.T) {
	f := reconcileFixture(t)
	f.runs = map[string]map[string][]int64{"dev": {"queued": {7}}}
	f.jobs[7] = f.jobs[7][:1]
	f.runners = []map[string]any{{"id": 400, "name": "runner-github-dev-71", "status": "offline", "busy": false,
		"labels": []map[string]any{{"name": "linux-amd64"}}}}
	launches(t, fitting(10))

	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5})
	if err != nil || out.Started != 0 || f.gone != nil || f.minted != nil {
		t.Fatalf("first sighting: %+v, %v, withdrew %v, minted %v; want nothing touched", out, err, f.gone, f.minted)
	}
	out, err = planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5})
	if err != nil || out.Started != 1 || !slices.Equal(f.gone, []int64{400}) || !slices.Equal(f.minted, []string{"runner-github-dev-71"}) {
		t.Fatalf("second sighting: %+v, %v, withdrew %v, minted %v; want 400 withdrawn and runner-github-dev-71 minted", out, err, f.gone, f.minted)
	}
}

// An installation GitHub fails to answer mid-read gets nothing that pass: no
// launch and no withdraw, even of a registration seen dead before.
func TestGitHubRunnersActsOnNoHalfRead(t *testing.T) {
	f := reconcileFixture(t)
	f.runners = []map[string]any{{"id": 400, "name": "runner-github-dev-71", "status": "offline", "busy": false,
		"labels": []map[string]any{{"name": "linux-amd64"}}}}
	sightings.Lock()
	sightings.by[111] = map[int64]bool{400: true}
	sightings.Unlock()
	f.fail = "/repos/hanzoai/cli/actions/runs/9/jobs"
	got := launches(t, fitting(10))
	out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 5})
	if err != nil || out.Started != 0 || len(*got) != 0 || f.minted != nil || f.gone != nil {
		t.Fatalf("%+v, %v, launched %d, minted %v, withdrew %v; want nothing", out, err, len(*got), f.minted, f.gone)
	}
}

// One reconcile runs at a time: a second asked while the first reads is
// refused, and the first goes on.
func TestGitHubRunnersRunsOneAtATime(t *testing.T) {
	f := reconcileFixture(t)
	launches(t, fitting(10))
	f.hold, f.held = make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(f.hold) }) }) // before the server closes, which waits on it
	done := make(chan error, 1)
	go func() {
		_, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 1})
		done <- err
	}()
	<-f.held
	_, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 1})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("a second reconcile while one runs: %v, want 409", err)
	}
	release.Do(func() { close(f.hold) })
	if err := <-done; err != nil {
		t.Fatalf("the first reconcile: %v", err)
	}
}

// A job whose runner fails to start the same way every pass goes behind the
// others, so it no longer spends each pass's attempts while the jobs behind it,
// in every org, wait.
func TestGitHubRunnersPutsAFailingJobLast(t *testing.T) {
	reconcileFixture(t)
	var tried []int64
	cloud.RegisterRunner(func(_ context.Context, ev cloud.JobEvent) (string, error) {
		tried = append(tried, ev.ID)
		if ev.ID == 71 {
			return "", errors.New("platform: runner: malformed job coordinate")
		}
		_, repo, _ := strings.Cut(ev.Repo, "/")
		return cloud.RunnerName("github", repo, ev.ID, ev.Try), nil
	})
	t.Cleanup(func() { cloud.RegisterRunner(nil) })
	var live []client.RunnerJob
	for range 3 {
		out, err := planeGitHubRunners(context.Background(), &client.GitHubRunnersIn{Free: 1, Jobs: live})
		if err != nil {
			t.Fatal(err)
		}
		if out.Started == 1 {
			id := tried[len(tried)-1]
			repo := map[int64]string{82: "dev", 91: "cli"}[id]
			live = append(live, client.RunnerJob{Name: cloud.RunnerName("github", repo, id, 0), Created: time.Now().Unix()})
		}
	}
	if !slices.Equal(tried, []int64{71, 82, 91}) {
		t.Fatalf("tried %v, want 71 once, then 82 and 91 ahead of it", tried)
	}
}

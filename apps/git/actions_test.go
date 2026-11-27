package git

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// The commits the fake forge holds for hanzo-inc/cloud: the main tip, and a
// release tag whose run failed in its tests.
const (
	tipSHA = "0c66629ea5123914b0ab9e605bd4bba320fcd473"
	tagSHA = "883e9cd244be1c39f38229fcb6216a55653b7289"
)

// cicd is the workflow the fake forge holds at the tip.
const cicd = `name: CI/CD
jobs:
  review:
    runs-on: [linux-amd64]
  unit:
    uses: hanzoai/ci/.github/workflows/build.yml@v2.1.21
  image:
    needs: [review]
    runs-on: [linux-amd64]
  notarize:
    runs-on: macos-14
`

// unitLog is what the failed unit job wrote.
const unitLog = "2026-11-11T11:44:09Z === RUN   TestA\n" +
	"2026-11-11T11:50:01Z --- FAIL: TestA (0.01s)\n" +
	"2026-11-11T11:50:01Z     a_test.go:12: got 1, want 2\n" +
	"2026-11-11T11:50:02Z FAIL\tgithub.com/hanzoai/cloud/apps/git\t0.31s\n" +
	"2026-11-11T12:11:06Z ##[error]Process completed with exit code 1."

// fakeForge is git.hanzo.ai as the actions reads see it. Every repository read
// must arrive SUDOED as the caller; the runner registry and queue as the
// machine. sudo records who each repository read acted as.
type fakeForge struct {
	t    *testing.T
	mu   sync.Mutex
	sudo []string
}

func (f *fakeForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	enc := json.NewEncoder(w)
	p := strings.TrimPrefix(r.URL.Path, forge.API)
	q := r.URL.Query()
	who := r.Header.Get("Sudo")
	if strings.HasPrefix(p, "/admin/") {
		if who != "" {
			http.Error(w, "the registry is the machine's", http.StatusBadRequest)
			return
		}
		switch p {
		case "/admin/actions/runners":
			_ = enc.Encode(map[string]any{"total_count": 2, "runners": []any{
				map[string]any{"id": 1, "name": "runner-forge-cloud-74301", "status": "online", "busy": true,
					"labels": []any{map[string]any{"name": "linux-amd64"}}, "last_online": "2026-11-12T06:50:00Z"},
				map[string]any{"id": 2, "name": "spark-1", "status": "offline",
					"labels":      []any{map[string]any{"name": "spark"}, map[string]any{"name": "linux-arm64"}},
					"last_online": "2026-11-01T00:00:00Z"},
			}})
		case "/admin/actions/jobs":
			_ = enc.Encode(map[string]any{"total_count": 2, "jobs": []any{
				map[string]any{"id": 74303, "url": "https://git.hanzo.test/v1/repos/hanzo-inc/cloud/actions/jobs/74303",
					"labels": []string{"spark", "linux-arm64"}},
				map[string]any{"id": 9, "url": "https://git.hanzo.test/v1/repos/someone/else/actions/jobs/9",
					"labels": []string{"linux-amd64"}},
			}})
		default:
			http.NotFound(w, r)
		}
		return
	}
	if who == "" {
		http.Error(w, "a repository is read as the caller", http.StatusBadRequest)
		return
	}
	if who == "u_stranger" {
		http.NotFound(w, r) // the forge gives this person nothing
		return
	}
	f.mu.Lock()
	f.sudo = append(f.sudo, who)
	f.mu.Unlock()
	run := func(id, n int, sha, ref, status, conclusion string) map[string]any {
		return map[string]any{"id": id, "run_number": n, "run_attempt": 1, "path": "cicd.yml@refs/heads/main",
			"display_title": "release", "event": "push", "head_branch": ref, "head_sha": sha,
			"status": status, "conclusion": conclusion, "actor": map[string]any{"login": "z"},
			"started_at": "2026-11-11T11:41:24Z", "completed_at": "2026-11-11T12:20:39Z"}
	}
	base := "/repos/hanzo-inc/cloud"
	switch {
	case p == "/orgs/hanzo-inc/repos":
		w.Header().Set("X-Total-Count", "1")
		_ = enc.Encode([]any{map[string]any{"name": "cloud", "full_name": "hanzo-inc/cloud", "private": true,
			"default_branch": "main", "updated_at": "2026-11-12T06:49:00Z", "ssh_url": "git@git.hanzo.test:hanzo-inc/cloud.git"}})
	case p == base:
		_ = enc.Encode(map[string]any{"name": "cloud", "full_name": "hanzo-inc/cloud", "private": true, "default_branch": "main"})
	case p == base+"/branches":
		_ = enc.Encode([]any{map[string]any{"name": "main"}})
	case p == base+"/git/commits/main" || p == base+"/git/commits/HEAD":
		_ = enc.Encode(map[string]any{"sha": tipSHA})
	case p == base+"/git/commits/v8.5.1131":
		_ = enc.Encode(map[string]any{"sha": tagSHA})
	case p == base+"/actions/runs" && q.Get("head_sha") == tagSHA:
		_ = enc.Encode(map[string]any{"total_count": 1, "workflow_runs": []any{run(17654, 1425, tagSHA, "main", "completed", "failure")}})
	case p == base+"/actions/runs" && q.Get("head_sha") == "":
		_ = enc.Encode(map[string]any{"total_count": 2, "workflow_runs": []any{
			run(17677, 1426, tipSHA, "main", "in_progress", ""), run(17654, 1425, tagSHA, "main", "completed", "failure")}})
	case p == base+"/actions/runs/17654":
		_ = enc.Encode(run(17654, 1425, tagSHA, "main", "completed", "failure"))
	case p == base+"/actions/runs/17654/jobs":
		_ = enc.Encode(map[string]any{"total_count": 3, "jobs": []any{
			map[string]any{"id": 74207, "run_id": 17654, "name": "gate", "status": "completed", "conclusion": "success"},
			map[string]any{"id": 74238, "run_id": 17654, "name": "test-amd64", "status": "completed", "conclusion": "failure",
				"labels": []string{"linux-amd64"}, "runner_name": "runner-forge-cloud-74238",
				"steps": []any{map[string]any{"number": 1, "name": "go test", "status": "completed", "conclusion": "failure"}}},
			map[string]any{"id": 74243, "run_id": 17654, "name": "release", "status": "completed", "conclusion": "skipped"},
		}})
	case p == base+"/actions/jobs/74238/logs":
		_, _ = w.Write([]byte(unitLog))
	case p == base+"/actions/runs/17700":
		_ = enc.Encode(run(17700, 1430, tagSHA, "main", "completed", "failure"))
	case p == base+"/actions/runs/17700/jobs":
		jobs := []any{}
		for i := range 6 {
			jobs = append(jobs, map[string]any{"id": 80000 + i, "run_id": 17700, "name": "shard", "status": "completed", "conclusion": "failure"})
		}
		_ = enc.Encode(map[string]any{"total_count": len(jobs), "jobs": jobs})
	case strings.HasPrefix(p, base+"/actions/jobs/8000") && strings.HasSuffix(p, "/logs"):
		_, _ = w.Write([]byte("2026-11-11T12:00:00Z --- FAIL: TestShard (0.01s)\n"))
	case p == base+"/contents-ext/.hanzo/workflows" && q.Get("ref") == tipSHA:
		_ = enc.Encode(map[string]any{"dir_contents": []any{
			map[string]any{"name": "cicd.yml", "path": ".hanzo/workflows/cicd.yml", "type": "file", "size": len(cicd)},
			map[string]any{"name": "README.md", "path": ".hanzo/workflows/README.md", "type": "file", "size": 3},
		}})
	case p == base+"/contents-ext/.hanzo/workflows/cicd.yml" && q.Get("ref") == tipSHA:
		f := map[string]any{"name": "cicd.yml", "path": ".hanzo/workflows/cicd.yml", "type": "file", "size": len(cicd)}
		if q.Get("includes") == "file_content" {
			f["content"] = base64.StdEncoding.EncodeToString([]byte(cicd))
		}
		_ = enc.Encode(map[string]any{"file_contents": f})
	default:
		http.NotFound(w, r)
	}
}

// actionsApp mounts git with the fake forge behind it, read as each caller's
// own login (the caller zip carries), the way forge.Client.Caller resolves it.
func actionsApp(t *testing.T) (*zip.App, *fakeForge) {
	t.Helper()
	f := &fakeForge{t: t}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	wasAs, wasSrc, wasWs, wasFleet := forgeAs, madeSource, madeWorkspace, fleetCache
	fleetCache = &fleet{}
	t.Cleanup(func() { forgeAs, madeSource, madeWorkspace, fleetCache = wasAs, wasSrc, wasWs, wasFleet })
	forgeAs = func(ctx context.Context) (*forge.Client, error) {
		c, err := forge.New(srv.URL, "machine")
		if err != nil {
			return nil, err
		}
		return c.As(zip.CallerOf(ctx).User), nil
	}
	madeSource = func(context.Context) (*forge.Client, error) { return forge.New(srv.URL, "machine") }
	madeWorkspace = func(org string, _ bool) (forge.Workspace, error) { return forge.Workspace{}, nil }
	return mountApp(t), f
}

// get reads path as org and decodes the 200 it must answer.
func get[T any](t *testing.T, app *zip.App, path, org string) T {
	t.Helper()
	code, body := do(t, app, http.MethodGet, path, org, nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return v
}

// A tag's runs come from the forge: the commit it names, every run on it with
// its jobs in one vocabulary, and the verdict that says the release is red —
// read as the caller, in a namespace their org owns that is not its home.
func TestRunsOfAForgeRefSayWhyItIsRed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, f := actionsApp(t)
		got := get[workflowRuns](t, app, "/v1/git/runs?repo=hanzo-inc/cloud&ref=v8.5.1131", "hanzo")
		if got.Commit != tagSHA || got.State != forge.Failed || len(got.Data) != 1 {
			t.Fatalf("runs = %+v", got)
		}
		r := got.Data[0]
		if r.ID != "17654" || r.Number != 1425 || r.Repo != "hanzo-inc/cloud" || r.Status != Failure || r.Actor != "z" || r.EndedAt.IsZero() || len(r.Jobs) != 3 {
			t.Fatalf("run = %+v", r)
		}
		if j := r.Jobs[1]; j.Name != "test-amd64" || j.Status != Failure || j.Runner != "runner-forge-cloud-74238" || j.Steps[0].Status != Failure {
			t.Fatalf("job = %+v", j)
		}
		if r.Jobs[2].Status != Skipped {
			t.Fatalf("a skipped job reads %q", r.Jobs[2].Status)
		}
		newest := get[workflowRuns](t, app, "/v1/git/runs?repo=hanzo-inc/cloud&limit=5", "hanzo")
		if len(newest.Data) != 2 || newest.Data[0].Status != Running || newest.State != "" {
			t.Fatalf("newest = %+v", newest)
		}
		for _, who := range f.sudo {
			if who != "u_hanzo" {
				t.Fatalf("a repository was read as %q, not as the caller", who)
			}
		}
	})
}

// A namespace the caller's org does not own is refused before the forge is
// asked — another tenant's, and the estate's own for an org that is not hanzo.
func TestAnotherOrgsNamespaceIsRefusedBeforeTheForge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, f := actionsApp(t)
		for _, c := range []struct{ org, path string }{
			{"acme", "/v1/git/runs?repo=hanzo-inc/cloud"},
			{"acme", "/v1/git/runs/17654?repo=hanzo-inc/cloud"},
			{"acme", "/v1/git/runs/17654/logs?repo=hanzo-inc/cloud"},
			{"acme", "/v1/git/workflows?repo=hanzo-inc/cloud"},
			{"acme", "/v1/git/repos/" + url.PathEscape("hanzo-inc/cloud")},
			{"acme", "/v1/git?owner=hanzo-inc"},
			{"hanzo", "/v1/git/runs?repo=someone/else"},
		} {
			if code, body := do(t, app, http.MethodGet, c.path, c.org, nil); code != http.StatusForbidden {
				t.Errorf("%s as %s: %d %s, want 403", c.path, c.org, code, body)
			}
		}
		if len(f.sudo) != 0 {
			t.Fatalf("the forge was asked %d times for namespaces the caller may not read", len(f.sudo))
		}
	})
}

// One run reads with its jobs, and its log answers the jobs that failed with
// the lines that say what failed — or one job, by name or id.
func TestARunsLogsSayWhatFailed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := actionsApp(t)
		run := get[workflowRun](t, app, "/v1/git/runs/17654?repo=hanzo-inc/cloud", "hanzo")
		if run.Status != Failure || len(run.Jobs) != 3 {
			t.Fatalf("run = %+v", run)
		}
		logs := get[runLogs](t, app, "/v1/git/runs/17654/logs?repo=hanzo-inc/cloud", "hanzo")
		if len(logs.Jobs) != 1 || logs.Jobs[0].Name != "test-amd64" || logs.Run.ID != "17654" || logs.Run.Jobs != nil {
			t.Fatalf("logs = %+v", logs)
		}
		want := "--- FAIL: TestA (0.01s)|a_test.go:12: got 1, want 2|FAIL\tgithub.com/hanzoai/cloud/apps/git\t0.31s|##[error]Process completed with exit code 1."
		if got := strings.Join(logs.Jobs[0].Failing, "|"); got != want {
			t.Fatalf("failing = %q", got)
		}
		if !strings.HasSuffix(logs.Jobs[0].Log, "exit code 1.") {
			t.Fatalf("log = %q", logs.Jobs[0].Log)
		}
		byID := get[runLogs](t, app, "/v1/git/runs/17654/logs?repo=hanzo-inc/cloud&job=74238", "hanzo")
		if len(byID.Jobs) != 1 || byID.Jobs[0].ID != 74238 {
			t.Fatalf("by id = %+v", byID)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/git/runs/17654/logs?repo=hanzo-inc/cloud&job=nope", "hanzo", nil); code != http.StatusNotFound {
			t.Fatalf("a job the run does not have answered %d, want 404", code)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/git/runs/main/logs?repo=hanzo-inc/cloud", "hanzo", nil); code != http.StatusBadRequest {
			t.Fatalf("a run that is not a number answered %d, want 400", code)
		}
		// Six failed jobs: four logs are read, and the rest are counted.
		many := get[runLogs](t, app, "/v1/git/runs/17700/logs?repo=hanzo-inc/cloud", "hanzo")
		if len(many.Jobs) != maxJobLogs || many.More != 2 || many.Jobs[0].Failing[0] != "--- FAIL: TestShard (0.01s)" {
			t.Fatalf("many = %d jobs, %d more", len(many.Jobs), many.More)
		}
		byName := get[runLogs](t, app, "/v1/git/runs/17700/logs?repo=hanzo-inc/cloud&job=shard", "hanzo")
		if len(byName.Jobs) != maxJobLogs || byName.More != 2 {
			t.Fatalf("a name six jobs share read %d, %d more", len(byName.Jobs), byName.More)
		}
	})
}

// A daemon's task log is read from its end: a log past the bound answers its
// last lines and the failures in them, and says it is the end of a longer one.
func TestATaskLogIsReadFromItsEnd(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		s := mounted.Load()
		was := taskLogRead
		taskLogRead = 4 << 10
		t.Cleanup(func() { taskLogRead = was })

		var b strings.Builder
		line := func(c string) {
			j, _ := json.Marshal(map[string]any{"time": 1, "content": c})
			b.Write(j)
			b.WriteByte('\n')
		}
		line("--- FAIL: TestEarly (0.00s)") // before the part read
		for b.Len() < 16<<10 {
			line(strings.Repeat("x", 80))
		}
		line("--- FAIL: TestLate (0.00s)")
		line("the last line")
		// The log arrives as the runner sends it: in appends, each its own object.
		var all []string
		for _, l := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
			var c struct{ Content string }
			_ = json.Unmarshal([]byte(l), &c)
			all = append(all, c.Content)
		}
		for i := 0; i < len(all); i += 50 {
			var lines []LogLine
			for _, c := range all[i:min(i+50, len(all))] {
				lines = append(lines, LogLine{Content: c})
			}
			if _, err := putLog(context.Background(), s.State.logs, "acme", 7, int64(i), lines); err != nil {
				t.Fatal(err)
			}
		}
		l, err := taskLog(s, "acme", 7)
		if err != nil {
			t.Fatalf("taskLog: %v", err)
		}
		if !l.Truncated || !strings.HasSuffix(l.Text, "\nthe last line") || len(l.Text) > 4<<10 || strings.Contains(l.Text, "{") {
			t.Fatalf("tail: truncated %v, %d bytes, ends %q", l.Truncated, len(l.Text), l.Text[max(0, len(l.Text)-30):])
		}
		if len(l.Failing) != 1 || l.Failing[0] != "--- FAIL: TestLate (0.00s)" {
			t.Fatalf("failing = %q", l.Failing)
		}
		if l, err := taskLog(s, "acme", 8); err != nil || l.Text != "" || l.Failing == nil {
			t.Fatalf("a task that wrote nothing = %+v, %v", l, err)
		}
	})
}

// The runners are the forge's, by pool, and a pool with work queued and no
// runner online is listed first. A SuperAdmin sees every runner by name and the
// whole queue; anyone else sees no runner by name and only the pools their own
// readable jobs wait on — the brand orgs are where sign-ups land, so holding the
// org is not holding its repositories.
func TestRunnersShowADeadPool(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := actionsApp(t)
		get := func(org string, head map[string]string) runnerList {
			t.Helper()
			code, body := doAs(t, app, http.MethodGet, "/v1/git/runners", org, head)
			if code != http.StatusOK {
				t.Fatalf("runners as %s %v: %d %s", org, head, code, body)
			}
			var out runnerList
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}

		super := get("admin", map[string]string{"X-User-IsAdmin": "true"})
		if len(super.Data) != 2 || super.Data[0].Name != "runner-forge-cloud-74301" || !super.Data[0].Forge || !super.Data[0].Busy || super.Data[0].LastOnline == 0 {
			t.Fatalf("a SuperAdmin's runners = %+v", super.Data)
		}
		if len(super.Pools) != 2 {
			t.Fatalf("a SuperAdmin's pools = %+v", super.Pools)
		}
		if p := super.Pools[0]; p.Pool != "linux-arm64+spark" || p.Queued != 1 || p.Online != 0 || p.Runners != 1 || !dead(p) {
			t.Fatalf("the dead pool = %+v", p)
		}
		if p := super.Pools[1]; p.Pool != "linux-amd64" || p.Queued != 1 || p.Online != 1 || p.Busy != 1 {
			t.Fatalf("the live pool = %+v", p)
		}

		member := get("hanzo", nil)
		if len(member.Data) != 0 {
			t.Fatalf("an org member saw runners by name: %+v", member.Data)
		}
		if len(member.Pools) != 1 || member.Pools[0].Pool != "linux-arm64+spark" || member.Pools[0].Queued != 1 || !dead(member.Pools[0]) {
			t.Fatalf("an org member's pools = %+v; want only the dead pool its own job waits on", member.Pools)
		}

		// The same org, as someone the forge does not give the repository to.
		stranger := get("hanzo", map[string]string{"X-User-Id": "u_stranger"})
		if len(stranger.Data) != 0 || len(stranger.Pools) != 0 {
			t.Fatalf("a sign-up in the org read %+v", stranger)
		}
	})
}

// A forge that cannot be read is said, and the daemons here still answer.
func TestRunnersSayAForgeThatCannotBeRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := actionsApp(t)
		madeSource = func(context.Context) (*forge.Client, error) { return nil, errors.New("forge down") }
		got := get[runnerList](t, app, "/v1/git/runners", "hanzo")
		if got.Fault != "the forge's runners could not be read" || got.Data == nil || got.Pools == nil {
			t.Fatalf("an org member's runners = %+v; want one sentence and the daemons", got)
		}
		code, body := doAs(t, app, http.MethodGet, "/v1/git/runners", "admin", map[string]string{"X-User-IsAdmin": "true"})
		if code != http.StatusOK || !strings.Contains(string(body), "forge down") {
			t.Fatalf("a SuperAdmin's runners = %d %s; want the forge's own words", code, body)
		}
	})
}

// One walk of the registry and queue answers every caller at once, on its own
// deadline: concurrent callers against a slow forge cost the forge one walk,
// and a caller that leaves does not take the walk with it.
func TestTheFleetIsWalkedOnceForEveryCaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		actionsApp(t)
		var walks atomic.Int64
		release := make(chan struct{})
		madeSource = func(context.Context) (*forge.Client, error) {
			walks.Add(1)
			<-release
			return nil, errors.New("forge down")
		}
		gone, leave := context.WithCancel(context.Background())
		leave()
		if _, _, err := fleetCache.read(gone); err == nil {
			t.Fatal("a caller that left was answered")
		}
		var wg sync.WaitGroup
		errs := make(chan error, 6)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, err := fleetCache.read(context.Background())
				errs <- err
			}()
		}
		time.Sleep(50 * time.Millisecond)
		close(release)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err == nil || !strings.Contains(err.Error(), "forge down") {
				t.Fatalf("a waiter got %v", err)
			}
		}
		if n := walks.Load(); n != 1 {
			t.Fatalf("%d walks for seven callers, want one", n)
		}
		// The failure is the answer for the window: the forge is not asked again.
		if _, _, err := fleetCache.read(context.Background()); err == nil || walks.Load() != 1 {
			t.Fatalf("a held failure was walked again: %v, %d walks", err, walks.Load())
		}
	})
}

// A forge repository's workflows are read at the commit the ref names, from
// the forge, each job placed where the platform would run it.
func TestWorkflowsOfAForgeRepository(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := actionsApp(t)
		got := get[workflowList](t, app, "/v1/git/workflows?repo=hanzo-inc/cloud&ref=main", "hanzo")
		if got.Commit != tipSHA || len(got.Data) != 1 || got.Data[0].Name != ".hanzo/workflows/cicd.yml" || got.Data[0].Fault != "" {
			t.Fatalf("workflows = %+v", got)
		}
		jobs := map[string]jobView{}
		for _, j := range got.Data[0].Jobs {
			jobs[j.Name] = j
		}
		if j := jobs["image"]; j.Pool != "linux-amd64" || len(j.Needs) != 1 || j.Needs[0] != "review" {
			t.Fatalf("image = %+v", j)
		}
		if j := jobs["unit"]; j.Uses != "hanzoai/ci/.github/workflows/build.yml@v2.1.21" || j.Pool != "" {
			t.Fatalf("unit = %+v", j)
		}
		if j := jobs["notarize"]; j.Pool != "" {
			t.Fatalf("a macOS job was placed in %q; no node here runs it", j.Pool)
		}
	})
}

// The repositories on the forge are listed by the coordinate the other ops
// take, and one is read by it — owner/name, percent-encoded as one segment.
func TestForgeRepositoriesByCoordinate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := actionsApp(t)
		list := get[repoList](t, app, "/v1/git?owner=hanzo-inc", "hanzo")
		if len(list.Data) != 1 || list.Data[0].Name != "hanzo-inc/cloud" || list.Data[0].Public || list.Data[0].Org != "hanzo" {
			t.Fatalf("list = %+v", list.Data)
		}
		one := get[repoView](t, app, "/v1/git/repos/"+url.PathEscape("hanzo-inc/cloud"), "hanzo")
		if one.Name != "hanzo-inc/cloud" || one.DefaultBranch != "main" || len(one.Branches) != 1 {
			t.Fatalf("repo = %+v", one)
		}
		if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/"+url.PathEscape("hanzo-inc/cloud"), "hanzo", nil); code != http.StatusNotFound {
			t.Fatalf("a forge repository was deletable here: %d", code)
		}
	})
}

// TestForgeAsRefusedCredentialIsBadGateway pins that a forge the platform
// credential cannot be minted for answers 502 with the reason, not a bare 500.
func TestForgeAsRefusedCredentialIsBadGateway(t *testing.T) {
	was := madeSource
	t.Cleanup(func() { madeSource = was })
	madeSource = func(context.Context) (*forge.Client, error) {
		return nil, errors.New("forge: native machine authority is not ready or does not match IAM")
	}
	_, err := forgeAs(context.Background())
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadGateway || !strings.Contains(he.Msg, "machine authority") {
		t.Fatalf("forgeAs = %v, want 502 naming the cause", err)
	}
}

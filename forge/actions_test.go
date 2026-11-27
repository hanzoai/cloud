package forge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	runSHA = "3af69f9d85a1b2c3d4e5f60718293a4b5c6d7e8f"
	tagSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// actionsForge answers a commit's runs, paged the way the forge pages them, and
// each run's jobs. n is how many runs the commit has.
func actionsForge(t *testing.T, n int) (*Client, *int) {
	t.Helper()
	asks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks++
		if r.Header.Get("Authorization") != "token tok" || r.Header.Get("Sudo") != "" {
			http.Error(w, "not the machine", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/actions/runs/7":
			writeJSON(w, map[string]any{
				"id": 7, "run_number": 41, "run_attempt": 2, "path": "cicd.yml@refs/heads/main",
				"display_title": "agents: one run", "event": "push", "head_branch": "main", "head_sha": runSHA,
				"status": "in_progress", "conclusion": "", "actor": map[string]any{"login": "z"},
				"started_at": "2026-11-12T06:49:10Z", "completed_at": "1970-01-01T00:00:00Z",
			})
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/git/refs/tags/smoked/":
			writeJSON(w, []any{
				map[string]any{"ref": "refs/tags/smoked/sha256-aa", "object": map[string]any{"type": "commit", "sha": runSHA}},
				map[string]any{"ref": "refs/tags/smoked/sha256-bb", "object": map[string]any{"type": "tag", "sha": tagSHA}},
			})
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/git/refs/tags/v1.2.3":
			writeJSON(w, map[string]any{"ref": "refs/tags/v1.2.3", "object": map[string]any{"type": "commit", "sha": runSHA}})
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/git/tags/"+tagSHA:
			writeJSON(w, map[string]any{"object": map[string]any{"type": "commit", "sha": runSHA}})
		case r.URL.Path == API+"/repos/hanzo-inc/cloud":
			writeJSON(w, map[string]any{"name": "cloud", "full_name": "hanzo-inc/cloud"})
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/actions/jobs/9/logs":
			_, _ = w.Write([]byte(jobLog))
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/actions/runs" && q.Get("head_sha") == "":
			// The newest runs of the repository, on the branch asked for, at the page
			// size asked for: one run per page position, numbered down from 100.
			p, _ := strconv.Atoi(q.Get("page"))
			limit, _ := strconv.Atoi(q.Get("limit"))
			runs := []map[string]any{}
			for i := 0; i < limit; i++ {
				id := 100 - (p-1)*limit - i
				runs = append(runs, map[string]any{"id": id, "run_number": id, "head_branch": q.Get("branch"), "head_sha": runSHA, "path": "cicd.yml"})
			}
			writeJSON(w, map[string]any{"workflow_runs": runs, "total_count": 100})
		case r.URL.Path == API+"/repos/hanzo-inc/cloud/actions/runs":
			if q.Get("head_sha") != runSHA {
				writeJSON(w, map[string]any{"workflow_runs": []any{}, "total_count": 0})
				return
			}
			p, _ := strconv.Atoi(q.Get("page"))
			limit, _ := strconv.Atoi(q.Get("limit"))
			runs := []map[string]any{}
			for id := (p-1)*limit + 1; id <= min(p*limit, n); id++ {
				runs = append(runs, map[string]any{
					"id": n - id + 1, "run_number": n - id + 1, "run_attempt": 1,
					"path": "cicd.yml@refs/tags/v1.2.3", "display_title": "release", "event": "push",
					"head_branch": "v1.2.3", "head_sha": runSHA, "status": "completed", "conclusion": "success",
					"html_url":   "https://git.hanzo.ai/hanzo-inc/cloud/actions/runs/1",
					"started_at": "2026-11-07T10:00:00Z", "completed_at": "1970-01-01T00:00:00Z",
				})
			}
			writeJSON(w, map[string]any{"workflow_runs": runs, "total_count": n})
		case strings.HasPrefix(r.URL.Path, API+"/repos/hanzo-inc/cloud/actions/runs/") && strings.HasSuffix(r.URL.Path, "/jobs"):
			writeJSON(w, map[string]any{"total_count": 1, "jobs": []any{map[string]any{
				"id": 9, "run_id": 1, "name": "image", "status": "completed", "conclusion": "failure",
				"labels": []string{"linux-amd64"}, "runner_name": "kata-amd64-7", "html_url": "https://git.hanzo.ai/hanzo-inc/cloud/actions/runs/1/jobs/0",
				"started_at": "2026-11-07T10:00:05Z", "completed_at": "",
				"steps": []any{map[string]any{"number": 1, "name": "Build & push images", "status": "completed", "conclusion": "failure"}},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Machine(), &asks
}

// Every run on the commit comes back, across the forge's pages, with the
// workflow named by its file and the epoch the forge writes for "not yet" read
// as no time at all.
func TestRunsReadsEveryRunOfACommit(t *testing.T) {
	const n = page + 2
	c, _ := actionsForge(t, n)
	runs, err := c.Runs(context.Background(), "hanzo-inc", "cloud", RunQuery{SHA: runSHA})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != n {
		t.Fatalf("%d runs, want %d", len(runs), n)
	}
	r := runs[0]
	if r.Workflow != "cicd.yml" || r.Ref != "v1.2.3" || r.SHA != runSHA || r.Conclusion != "success" {
		t.Fatalf("run = %+v", r)
	}
	if !r.StartedAt.Equal(time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)) || !r.EndedAt.IsZero() {
		t.Fatalf("times = %v / %v, want the start and no end", r.StartedAt, r.EndedAt)
	}
}

// Without a commit, the newest runs come back, as many as asked for and no
// more, on the branch asked for — across pages of one size, so none is skipped.
func TestRunsListsTheNewestOfARepository(t *testing.T) {
	c, _ := actionsForge(t, 0)
	runs, err := c.Runs(context.Background(), "hanzo-inc", "cloud", RunQuery{Branch: "main", Limit: 60})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 60 {
		t.Fatalf("%d runs, want 60", len(runs))
	}
	for i, r := range runs {
		if r.ID != int64(100-i) || r.Ref != "main" {
			t.Fatalf("run %d = %+v, want id %d on main", i, r, 100-i)
		}
	}
	if runs, _ := c.Runs(context.Background(), "hanzo-inc", "cloud", RunQuery{Limit: 1000}); len(runs) != maxRuns {
		t.Fatalf("a limit past the ceiling answered %d runs, want %d", len(runs), maxRuns)
	}
}

// One run is read by its id, with who started it, and the epoch the forge
// writes for "not yet" read as no time at all.
func TestRunReadsOneRun(t *testing.T) {
	c, _ := actionsForge(t, 0)
	r, err := c.Run(context.Background(), "hanzo-inc", "cloud", 7)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.ID != 7 || r.Number != 41 || r.Attempt != 2 || r.Workflow != "cicd.yml" || r.Actor != "z" || r.Status != "in_progress" || !r.EndedAt.IsZero() {
		t.Fatalf("run = %+v", r)
	}
}

// jobLog is a failed Go test job as the forge records it: every line behind
// the time it was written.
const jobLog = "2026-11-11T08:20:01.1234567Z === RUN   TestA\n" +
	"2026-11-11T08:20:01.2Z --- FAIL: TestA (0.01s)\n" +
	"2026-11-11T08:20:01.3Z     a_test.go:12: got 1, want 2\n" +
	"2026-11-11T08:20:02Z FAIL\tgithub.com/hanzoai/cloud/apps/git\t0.31s\n" +
	"2026-11-11T08:20:03Z ok  \tgithub.com/hanzoai/cloud/forge\t1.2s\n" +
	"2026-11-11T08:20:04Z apps/ci/ref.go:12:2: undefined: forgeOf\n" +
	"2026-11-11T08:20:05Z panic: runtime error: index out of range\n" +
	"2026-11-11T08:20:06Z ##[error]Process completed with exit code 1.\n" +
	"2026-11-11T08:20:07Z FAIL"

// A job's log is read whole, and the lines that say what failed are picked out
// of it without the time in front of them.
func TestLogPicksOutWhatFailed(t *testing.T) {
	c, _ := actionsForge(t, 0)
	l, err := c.Log(context.Background(), "hanzo-inc", "cloud", 9)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	want := []string{
		"--- FAIL: TestA (0.01s)",
		"a_test.go:12: got 1, want 2",
		"FAIL\tgithub.com/hanzoai/cloud/apps/git\t0.31s",
		"apps/ci/ref.go:12:2: undefined: forgeOf",
		"panic: runtime error: index out of range",
		"##[error]Process completed with exit code 1.",
		"FAIL",
	}
	if strings.Join(l.Failing, "|") != strings.Join(want, "|") {
		t.Fatalf("failing =\n%q\nwant\n%q", l.Failing, want)
	}
	if l.Truncated || !strings.HasPrefix(l.Text, "2026-11-11T08:20:01.1234567Z === RUN") || !strings.HasSuffix(l.Text, "Z FAIL") {
		t.Fatalf("text = %q (truncated %v)", l.Text, l.Truncated)
	}
}

// A log longer than the bound keeps its END, in whole lines, and still finds a
// failure written before the part it kept.
func TestALongLogKeepsItsEnd(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- FAIL: TestEarly (0.00s)\n")
	for b.Len() < 2*maxLog {
		b.WriteString(strings.Repeat("x", 99) + "\n")
	}
	b.WriteString("the last line")
	l, err := ReadLog(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if !l.Truncated || len(l.Text) > maxLog || !strings.HasSuffix(l.Text, "\nthe last line") || !strings.HasPrefix(l.Text, "x") {
		t.Fatalf("tail: truncated %v, %d bytes, ends %q", l.Truncated, len(l.Text), l.Text[len(l.Text)-20:])
	}
	if len(l.Failing) != 1 || l.Failing[0] != "--- FAIL: TestEarly (0.00s)" {
		t.Fatalf("failing = %q", l.Failing)
	}
}

// A commit nothing ran on is an empty list, not an error.
func TestRunsOfAQuietCommitIsEmpty(t *testing.T) {
	c, _ := actionsForge(t, 0)
	runs, err := c.Runs(context.Background(), "hanzo-inc", "cloud", RunQuery{SHA: runSHA})
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs = %v, %v; want none and no error", runs, err)
	}
}

// head_sha is an exact match, so anything but a full commit name would answer
// "nothing ran". It is refused as the caller's error before the forge is asked.
func TestRunsRefusesAnythingButAFullSHA(t *testing.T) {
	c, asks := actionsForge(t, 1)
	for _, sha := range []string{"main", "3af69f9", strings.ToUpper(runSHA)} {
		if _, err := c.Runs(context.Background(), "hanzo-inc", "cloud", RunQuery{SHA: sha}); !errors.Is(err, ErrBadName) {
			t.Errorf("Runs(%q) err = %v, want ErrBadName", sha, err)
		}
	}
	if _, err := c.Runs(context.Background(), "hanzo-inc/x", "cloud", RunQuery{SHA: runSHA}); !errors.Is(err, ErrBadName) {
		t.Errorf("a slashed owner was not refused: %v", err)
	}
	if *asks != 0 {
		t.Fatalf("the forge was asked %d times for requests that should never leave", *asks)
	}
}

// A run's jobs come back with their steps, which is where "which part failed" is.
func TestJobsReadsARunsJobsAndSteps(t *testing.T) {
	c, _ := actionsForge(t, 1)
	jobs, err := c.Jobs(context.Background(), "hanzo-inc", "cloud", 1)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if j.Name != "image" || j.Run != 1 || j.Conclusion != "failure" || j.Owner != "hanzo-inc" || j.Repo != "cloud" || j.Runner != "kata-amd64-7" || !j.EndedAt.IsZero() {
		t.Fatalf("job = %+v", j)
	}
	if len(j.Steps) != 1 || j.Steps[0].Name != "Build & push images" || j.Steps[0].Conclusion != "failure" {
		t.Fatalf("steps = %+v", j.Steps)
	}
}

// A ref the forge cannot be asked for is the caller's error, so a surface can
// answer 400 rather than blame the forge.
func TestABadRefIsTheCallersError(t *testing.T) {
	c, asks := actionsForge(t, 1)
	for _, ref := range []string{"a..b", "v1 2", "-x", "/main"} {
		if _, err := c.Resolve(context.Background(), "hanzo-inc", "cloud", ref); !errors.Is(err, ErrBadName) {
			t.Errorf("Resolve(%q) err = %v, want ErrBadName", ref, err)
		}
	}
	if *asks != 0 {
		t.Fatalf("the forge was asked %d times for refs that should never leave", *asks)
	}
}

// References under a prefix come back in one read: a lightweight tag with its
// commit, an annotated one as its tag object until it is peeled, the forge's
// one-object answer for an exact match read as a list of one, and a prefix
// nothing matches as an empty list.
func TestRefsReadsWhatAPrefixNames(t *testing.T) {
	c, asks := actionsForge(t, 0)
	refs, err := c.Refs(context.Background(), "hanzo-inc", "cloud", "tags/smoked/")
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if *asks != 1 {
		t.Fatalf("listing refs asked the forge %d times, want once", *asks)
	}
	if len(refs) != 2 || refs[0] != (Ref{"refs/tags/smoked/sha256-aa", runSHA, false}) || refs[1] != (Ref{"refs/tags/smoked/sha256-bb", tagSHA, true}) {
		t.Fatalf("refs = %+v", refs)
	}
	if sha, err := c.Peel(context.Background(), "hanzo-inc", "cloud", refs[1]); err != nil || sha != runSHA {
		t.Fatalf("Peel = %q, %v; want the commit the tag names", sha, err)
	}
	if sha, err := c.Peel(context.Background(), "hanzo-inc", "cloud", refs[0]); err != nil || sha != runSHA {
		t.Fatalf("Peel of a lightweight tag = %q, %v", sha, err)
	}
	one, err := c.Refs(context.Background(), "hanzo-inc", "cloud", "refs/tags/v1.2.3")
	if err != nil || len(one) != 1 || one[0].SHA != runSHA {
		t.Fatalf("exact ref = %+v, %v", one, err)
	}
	none, err := c.Refs(context.Background(), "hanzo-inc", "cloud", "tags/nothing")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("unmatched prefix = %+v, %v; want an empty list", none, err)
	}
	for _, bad := range []string{"", "tags/../heads", "tags/a b"} {
		if _, err := c.Refs(context.Background(), "hanzo-inc", "cloud", bad); !errors.Is(err, ErrBadName) {
			t.Errorf("Refs(%q) err = %v, want ErrBadName", bad, err)
		}
	}
}

// A tag that ends at a tree, or a chain of tags past the bound, is no commit.
func TestPeelRefusesWhatIsNotACommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, API+"/repos/o/r/git/tags/")
		switch name {
		case "1111111111111111111111111111111111111111":
			writeJSON(w, map[string]any{"object": map[string]any{"type": "tree", "sha": runSHA}})
		default: // every tag names another tag
			writeJSON(w, map[string]any{"object": map[string]any{"type": "tag", "sha": tagSHA}})
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	m := c.Machine()
	for _, r := range []Ref{
		{Name: "refs/tags/tree", SHA: "1111111111111111111111111111111111111111", Annotated: true},
		{Name: "refs/tags/loop", SHA: tagSHA, Annotated: true},
	} {
		if _, err := m.Peel(context.Background(), "o", "r", r); !errors.Is(err, ErrBadName) {
			t.Errorf("Peel(%s) err = %v, want ErrBadName", r.Name, err)
		}
	}
}

// A line longer than the bound is kept to the bound and the rest passed over,
// and a failing line is kept shorter still, so no log's memory is a line's size.
func TestALongLineIsBounded(t *testing.T) {
	long := "--- FAIL: TestHuge " + strings.Repeat("y", 3*maxLine)
	l, err := ReadLog(strings.NewReader("before\n" + long + "\nafter"))
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	lines := strings.Split(l.Text, "\n")
	if len(lines) != 3 || lines[0] != "before" || len(lines[1]) != maxLine || lines[2] != "after" {
		t.Fatalf("lines = %d, middle %d bytes", len(lines), len(lines[1]))
	}
	if len(l.Failing) != 1 || len(l.Failing[0]) != maxFailingLine {
		t.Fatalf("failing = %d lines, first %d bytes", len(l.Failing), len(l.Failing[0]))
	}
}

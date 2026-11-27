package forge

// actions.go reads what the forge RAN: a repository's Actions runs, the jobs and
// steps of each, and a job's log. The forge schedules every run and holds every
// result, so this is a read of the one record, never a copy of it.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Run is one Actions workflow run.
type Run struct {
	// ID is the run's id on the forge, the {run} of its API address.
	ID int64
	// Number is the run's number within its repository, as the web UI shows it.
	Number int
	// Attempt counts re-runs of the same run, starting at 1.
	Attempt int
	// Workflow is the workflow file that ran, without the ref the forge appends
	// to it ("cicd.yml", not "cicd.yml@refs/heads/main").
	Workflow string
	// Title is the run's display title, usually the commit's subject.
	Title string
	// Event is what started it: push, pull_request, workflow_dispatch, schedule.
	Event string
	// Ref is the branch or tag the run was started on, in its short form.
	Ref string
	// SHA is the commit the run checked out.
	SHA string
	// Status is whether the run is over, in the forge's words: queued, waiting,
	// in_progress or completed.
	Status string
	// Conclusion is how it ended — success, failure, cancelled, skipped — and is
	// empty until Status is completed.
	Conclusion string
	// URL is the run's page on the forge.
	URL string
	// Actor is the forge login that started the run, or the pusher of its commit.
	Actor string
	// StartedAt and EndedAt are zero while unknown; the forge reports an unset
	// time as the Unix epoch, which is read as unset.
	StartedAt time.Time
	EndedAt   time.Time
}

// Step is one step of a job.
type Step struct {
	Number     int
	Name       string
	Status     string
	Conclusion string
}

// runsWire and jobsWire are the forge's answers, decoded field by field. Times
// stay strings until parsed, so one malformed timestamp cannot fail the read.
type runsWire struct {
	Total int64     `json:"total_count"`
	Runs  []runWire `json:"workflow_runs"`
}

type runWire struct {
	ID         int64  `json:"id"`
	Number     int    `json:"run_number"`
	Attempt    int    `json:"run_attempt"`
	Path       string `json:"path"`
	Title      string `json:"display_title"`
	Event      string `json:"event"`
	Branch     string `json:"head_branch"`
	SHA        string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
	Started    string `json:"started_at"`
	Completed  string `json:"completed_at"`
	Actor      *struct {
		Login string `json:"login"`
	} `json:"actor"`
}

// run reads the forge's answer: the workflow without the ref the forge appends
// to its path, and the times as [when] reads them.
func (r runWire) run() Run {
	workflow, _, _ := strings.Cut(r.Path, "@")
	out := Run{
		ID: r.ID, Number: r.Number, Attempt: r.Attempt, Workflow: workflow,
		Title: r.Title, Event: r.Event, Ref: r.Branch, SHA: r.SHA,
		Status: r.Status, Conclusion: r.Conclusion, URL: r.URL,
		StartedAt: when(r.Started), EndedAt: when(r.Completed),
	}
	if r.Actor != nil {
		out.Actor = r.Actor.Login
	}
	return out
}

type jobsWire struct {
	Total int64 `json:"total_count"`
	Jobs  []struct {
		ID         int64    `json:"id"`
		Run        int64    `json:"run_id"`
		Name       string   `json:"name"`
		Labels     []string `json:"labels"`
		Runner     string   `json:"runner_name"`
		Status     string   `json:"status"`
		Conclusion string   `json:"conclusion"`
		URL        string   `json:"html_url"`
		Started    string   `json:"started_at"`
		Completed  string   `json:"completed_at"`
		Steps      []struct {
			Number     int    `json:"number"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	} `json:"jobs"`
}

// RunQuery narrows the runs [Client.Runs] lists.
type RunQuery struct {
	// SHA keeps the runs on one commit and lists EVERY one of them, across the
	// forge's pages. It must be a full commit name, which [Client.Resolve]
	// produces: the forge matches head_sha exactly, so a short sha or a branch
	// name would answer an empty list that reads as "nothing ran" rather than as
	// "asked the wrong question". It is refused instead.
	SHA string
	// Branch keeps the runs started on one branch. Ignored with SHA, which
	// already names one commit.
	Branch string
	// Limit caps a listing without SHA: 0 is [defaultRuns], and it is held to
	// [maxRuns].
	Limit int
}

// defaultRuns and maxRuns bound a listing of a repository's runs that names no
// commit: the newest are what such a listing is for, and the forge computes each
// run it returns.
const (
	defaultRuns = 20
	maxRuns     = 100
)

// Runs lists owner/repo's Actions runs, newest first: every run on q.SHA when it
// names a commit, otherwise the newest q.Limit, on q.Branch when it names one.
func (c *Client) Runs(ctx context.Context, owner, repo string, q RunQuery) ([]Run, error) {
	if err := validSegment(owner); err != nil {
		return nil, err
	}
	if err := validSegment(repo); err != nil {
		return nil, fmt.Errorf("forge: repo: %w", err)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/actions/runs"
	filter := url.Values{}
	want := maxPages * page
	switch {
	case q.SHA != "":
		if !hex(q.SHA) {
			return nil, fmt.Errorf("%w: %q is not a full commit sha", ErrBadName, q.SHA)
		}
		filter.Set("head_sha", q.SHA)
	default:
		if b := strings.TrimSpace(q.Branch); b != "" {
			if err := validRef(b); err != nil {
				return nil, err
			}
			filter.Set("branch", b)
		}
		want = q.Limit
		if want <= 0 {
			want = defaultRuns
		}
		want = min(want, maxRuns)
	}
	// One page size for the whole walk: the forge pages by offset, so a size that
	// changed between pages would skip or repeat runs.
	size := strconv.Itoa(min(page, want))
	var out []Run
	for p := 1; p <= maxPages; p++ {
		var w runsWire
		pq := url.Values{"page": {strconv.Itoa(p)}, "limit": {size}}
		for k, v := range filter {
			pq[k] = v
		}
		if err := c.do(ctx, path, pq, &w); err != nil {
			return nil, err
		}
		for _, r := range w.Runs {
			out = append(out, r.run())
		}
		if len(w.Runs) == 0 || len(out) >= want || int64(len(out)) >= w.Total {
			return out[:min(len(out), want)], nil
		}
	}
	if q.SHA == "" {
		return out, nil
	}
	return nil, fmt.Errorf("forge: %s/%s@%s has more than %d runs", owner, repo, q.SHA, maxPages*page)
}

// Run reads one run of owner/repo by its id.
func (c *Client) Run(ctx context.Context, owner, repo string, id int64) (Run, error) {
	if err := validSegment(owner); err != nil {
		return Run{}, err
	}
	if err := validSegment(repo); err != nil {
		return Run{}, fmt.Errorf("forge: repo: %w", err)
	}
	if id <= 0 {
		return Run{}, fmt.Errorf("%w: run %d", ErrBadName, id)
	}
	var w runWire
	err := c.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/actions/runs/"+strconv.FormatInt(id, 10), nil, &w)
	if err != nil {
		return Run{}, err
	}
	return w.run(), nil
}

// Jobs lists the jobs of one run in the order the forge created them, each with
// its steps.
func (c *Client) Jobs(ctx context.Context, owner, repo string, run int64) ([]Job, error) {
	if err := validSegment(owner); err != nil {
		return nil, err
	}
	if err := validSegment(repo); err != nil {
		return nil, fmt.Errorf("forge: repo: %w", err)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/actions/runs/" + strconv.FormatInt(run, 10) + "/jobs"
	var out []Job
	for p := 1; p <= maxPages; p++ {
		var w jobsWire
		q := url.Values{"sort": {"id"}, "order": {"asc"}, "page": {strconv.Itoa(p)}, "limit": {strconv.Itoa(page)}}
		if err := c.do(ctx, path, q, &w); err != nil {
			return nil, err
		}
		for _, j := range w.Jobs {
			steps := make([]Step, 0, len(j.Steps))
			for _, s := range j.Steps {
				steps = append(steps, Step{Number: s.Number, Name: s.Name, Status: s.Status, Conclusion: s.Conclusion})
			}
			out = append(out, Job{
				ID: j.ID, Owner: owner, Repo: repo, Labels: j.Labels, Runner: j.Runner,
				Run: j.Run, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.URL,
				StartedAt: when(j.Started), EndedAt: when(j.Completed), Steps: steps,
			})
		}
		if len(w.Jobs) < page || int64(len(out)) >= w.Total {
			return out, nil
		}
	}
	return nil, fmt.Errorf("forge: run %d of %s/%s has more than %d jobs", run, owner, repo, maxPages*page)
}

// when reads one of the forge's timestamps. Empty, malformed and the epoch the
// forge writes for "not yet" are all the zero time.
func when(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t.UTC()
}

// The states [Verdict] answers, worst last.
const (
	None    = "none"
	Passed  = "passed"
	Queued  = "queued"
	Running = "running"
	Failed  = "failed"
)

// severity orders the states so that the worst of many is the answer.
var severity = map[string]int{None: 0, Passed: 1, Queued: 2, Running: 3, Failed: 4}

// Counted are the runs a commit is judged by, as indices into runs: the latest
// attempt of each workflow, per ref and per event that started it. The one the
// forge numbered last stands, a later attempt of the same run beating an
// earlier one. A tag and the branch it was cut from are different refs, and a
// push and a pull request are different events, so each is judged on its own
// and a green one never hides a red one.
func Counted(runs []Run) []int {
	type key struct{ workflow, ref, event string }
	latest := map[key]int{}
	var order []key
	for i, r := range runs {
		k := key{r.Workflow, r.Ref, r.Event}
		p, ok := latest[k]
		if !ok {
			order = append(order, k)
		}
		if !ok || r.Number > runs[p].Number || r.Number == runs[p].Number && r.Attempt > runs[p].Attempt {
			latest[k] = i
		}
	}
	out := make([]int, 0, len(order))
	for _, k := range order {
		out = append(out, latest[k])
	}
	return out
}

// Verdict answers "is this commit green?" from the runs on it and each run's
// jobs (jobs[i] are runs[i]'s, and only the [Counted] runs' are read): passed,
// failed, running, queued or none.
func Verdict(runs []Run, jobs [][]Job) string {
	out := None
	see := func(status, conclusion string) {
		if s := stateOf(status, conclusion); severity[s] > severity[out] {
			out = s
		}
	}
	for _, i := range Counted(runs) {
		see(runs[i].Status, runs[i].Conclusion)
		for _, j := range jobs[i] {
			see(j.Status, j.Conclusion)
		}
	}
	return out
}

// stateOf reads one run's or one job's status and conclusion as a state.
//
// The words are the forge's (ToActionsStatus in hanzoai/git): status is queued
// (waiting for a runner), waiting (blocked on a dependency or an approval),
// in_progress or completed, and conclusion, set only once completed, is success,
// failure, cancelled or skipped. Green has to be proven, so a conclusion that is
// not success or skipped, and a completed one with no conclusion, is failed.
func stateOf(status, conclusion string) string {
	switch {
	case conclusion != "" && conclusion != "success" && conclusion != "skipped":
		return Failed
	case status == "in_progress":
		return Running
	case status == "queued" || status == "waiting":
		return Queued
	case conclusion == "":
		return Failed
	}
	return Passed
}

// What one log read keeps, and how far it reads. maxLog is the END of the log
// kept, which is where a job says why it stopped; maxFailing and maxFailingLine
// bound the failing lines; maxLine is the most of one line read, the rest of it
// passed over; maxLogRead is how much of a log is read at all.
const (
	maxLog         = 256 << 10
	maxFailing     = 100
	maxFailingLine = 2 << 10
	maxLine        = 64 << 10
	maxLogRead     = 32 << 20
)

// Log is one job's output, as the forge recorded it.
type Log struct {
	// Text is the last [maxLog] bytes of the log, whole lines only, a line
	// longer than [maxLine] cut there.
	Text string
	// Truncated says Text is the end of a longer log.
	Truncated bool
	// Cut says the log is longer than [maxLogRead] and was read no further, so
	// Text ends where the read stopped rather than where the log does.
	Cut bool
	// Failing are the lines that say what failed, read over the whole log (to
	// [maxLogRead]), in the order they were written: a test that failed, a
	// package that failed, a panic, a compiler or vet diagnostic, and an error
	// the runner annotated. At most [maxFailing], each at most [maxFailingLine].
	Failing []string
}

// Log reads the log of one job of owner/repo.
//
// The forge writes each line behind the time it was written; Text keeps that,
// and Failing drops it, because a failing line is quoted to say what failed and
// not when.
func (c *Client) Log(ctx context.Context, owner, repo string, job int64) (Log, error) {
	if err := validSegment(owner); err != nil {
		return Log{}, err
	}
	if err := validSegment(repo); err != nil {
		return Log{}, fmt.Errorf("forge: repo: %w", err)
	}
	if job <= 0 {
		return Log{}, fmt.Errorf("%w: job %d", ErrBadName, job)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/actions/jobs/" + strconv.FormatInt(job, 10) + "/logs"
	resp, err := c.open(ctx, path, nil, "text/plain")
	if err != nil {
		return Log{}, err
	}
	defer resp.Body.Close()
	return ReadLog(resp.Body)
}

// ReadLog reads a log the way [Client.Log] does, in memory bounded whatever its
// size: its last [maxLog] bytes in whole lines, and every failing line in it,
// to [maxLogRead]. Any runner's log reads the same.
func ReadLog(r io.Reader) (Log, error) {
	var (
		out  Log
		tail []string
		size int
		read int64
	)
	br := bufio.NewReaderSize(io.LimitReader(r, maxLogRead+1), maxLine)
	for {
		line, n, err := readLine(br)
		read += int64(n)
		if read > maxLogRead {
			out.Cut = true
			break
		}
		if n > 0 {
			if f, ok := failing(line); ok && len(out.Failing) < maxFailing {
				out.Failing = append(out.Failing, f[:min(len(f), maxFailingLine)])
			}
			tail = append(tail, line)
			size += len(line) + 1
			for size > maxLog && len(tail) > 1 {
				size -= len(tail[0]) + 1
				tail = tail[1:]
				out.Truncated = true
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Log{}, fmt.Errorf("forge: read log: %w", err)
		}
	}
	out.Text = strings.Join(tail, "\n")
	if out.Failing == nil {
		out.Failing = []string{}
	}
	return out, nil
}

// readLine is the next line of br without its terminator, at most [maxLine]
// bytes of it; the rest of a longer line is read and passed over. n counts every
// byte consumed, so the caller's read bound sees the whole line.
func readLine(br *bufio.Reader) (line string, n int, err error) {
	b, err := br.ReadSlice('\n')
	n = len(b)
	line = strings.TrimRight(string(b), "\r\n")
	for errors.Is(err, bufio.ErrBufferFull) {
		var more []byte
		more, err = br.ReadSlice('\n')
		n += len(more)
	}
	return line, n, err
}

// stamp is the time the forge writes in front of every log line.
var stamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z\s?`)

// diagnostic is a report about one line of a Go file: a compiler's or vet's,
// and what a failing test logged (t.Errorf writes file:line: before it).
var diagnostic = regexp.MustCompile(`^\S+\.go:\d+(:\d+)?: `)

// failing reports whether a log line says what failed, and the line without its
// time. What it reads as a failure, and nothing else:
//
//	--- FAIL: TestX (0.01s)       a test, or a subtest
//	x_test.go:12: …               what that test said when it failed
//	FAIL / FAIL <pkg> <time>      a package, or the whole run
//	panic: …                      a panic
//	x.go:12:3: …                  a compiler or vet diagnostic
//	##[error]… / ::error…         an error the runner or a step annotated
func failing(line string) (string, bool) {
	s := stamp.ReplaceAllString(line, "")
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "--- FAIL:"),
		t == "FAIL", strings.HasPrefix(t, "FAIL\t"), strings.HasPrefix(t, "FAIL "),
		strings.HasPrefix(t, "panic: "),
		strings.HasPrefix(t, "##[error]"), strings.HasPrefix(t, "::error"),
		diagnostic.MatchString(t):
		return t, true
	}
	return "", false
}

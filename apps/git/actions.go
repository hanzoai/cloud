package git

// actions.go — what the forge ran for a repository whose home is there, what it
// logged, and the runners it ran on.
//
// The forge schedules every run of a repository it holds and keeps every result
// and log, so each answer here is a read of that one record, never a copy. The
// ops are the same ones a repository this app holds answers through (run.go);
// [place] decides which record a repository argument names, and [forgeFor]
// decides whether the caller may read it.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
	"golang.org/x/sync/errgroup"
)

// jobReads bounds the job lists one request reads at once: a read of the server
// that schedules every build is never worth degrading it.
const jobReads = 4

// word is a forge status and conclusion in this surface's one vocabulary:
// waiting, running, or how it ended. A run the forge calls completed without a
// conclusion did not prove it passed, so it is a failure.
func word(status, conclusion string) string {
	switch {
	case conclusion != "":
		return conclusion
	case status == "in_progress":
		return Running
	case status == "queued" || status == "waiting" || status == "blocked":
		return Waiting
	case status == "completed":
		return Failure
	}
	return status
}

func forgeRunView(owner, name string, r forge.Run) workflowRun {
	v := workflowRun{
		ID: strconv.FormatInt(r.ID, 10), Number: int64(r.Number), Repo: owner + "/" + name,
		Ref: r.Ref, Commit: r.SHA, Event: r.Event, Workflow: r.Workflow, Actor: r.Actor,
		Status: word(r.Status, r.Conclusion), Title: r.Title, Attempt: r.Attempt, URL: r.URL,
		StartedAt: r.StartedAt, EndedAt: r.EndedAt,
	}
	if !r.StartedAt.IsZero() {
		v.CreatedAt, v.UpdatedAt = r.StartedAt.Unix(), r.StartedAt.Unix()
	}
	if !r.EndedAt.IsZero() {
		v.UpdatedAt = r.EndedAt.Unix()
	}
	return v
}

func forgeJobView(j forge.Job) runJob {
	v := runJob{ID: j.ID, Name: j.Name, Status: word(j.Status, j.Conclusion), Runner: j.Runner,
		Labels: j.Labels, URL: j.URL, StartedAt: j.StartedAt, EndedAt: j.EndedAt}
	for _, s := range j.Steps {
		v.Steps = append(v.Steps, runStep{Number: s.Number, Name: s.Name, Status: word(s.Status, s.Conclusion)})
	}
	return v
}

// forgeRuns lists a forge repository's runs: the newest, or every run on the
// commit ref resolves to, with jobs and the ref's verdict.
func forgeRuns(ctx context.Context, t tenant, owner, name, ref string, limit int) (*workflowRuns, error) {
	m, err := forgeFor(ctx, t, owner)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		runs, err := m.Runs(ctx, owner, name, forge.RunQuery{Limit: limit})
		if err != nil {
			return nil, madeRefusal(err, "no such repository on the forge")
		}
		out := &workflowRuns{Data: make([]workflowRun, 0, len(runs))}
		for _, r := range runs {
			out.Data = append(out.Data, forgeRunView(owner, name, r))
		}
		return out, nil
	}
	sha, err := m.Resolve(ctx, owner, name, ref)
	if err != nil {
		return nil, madeRefusal(err, "no such repository or ref on the forge")
	}
	runs, err := m.Runs(ctx, owner, name, forge.RunQuery{SHA: sha})
	if err != nil {
		return nil, madeRefusal(err, "no such repository on the forge")
	}
	out := &workflowRuns{Data: make([]workflowRun, len(runs)), Commit: sha}
	for i, r := range runs {
		out.Data[i] = forgeRunView(owner, name, r)
	}
	// Jobs of the runs the commit is judged by, and of no other: a superseded
	// attempt is history, and a commit with thousands of runs costs as many job
	// reads as it has workflows.
	jobs := make([][]forge.Job, len(runs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(jobReads)
	for _, i := range forge.Counted(runs) {
		g.Go(func() error {
			js, err := m.Jobs(gctx, owner, name, runs[i].ID)
			jobs[i] = js
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, madeRefusal(err, "no such run on the forge")
	}
	for i := range runs {
		for _, j := range jobs[i] {
			out.Data[i].Jobs = append(out.Data[i].Jobs, forgeJobView(j))
		}
	}
	out.State = forge.Verdict(runs, jobs)
	return out, nil
}

// forgeRun reads one run of a forge repository with its jobs.
func forgeRun(ctx context.Context, t tenant, owner, name, id string) (*workflowRun, error) {
	m, err := forgeFor(ctx, t, owner)
	if err != nil {
		return nil, err
	}
	return readRun(ctx, m, owner, name, id)
}

// readRun reads one run with its jobs, as the client m acts.
func readRun(ctx context.Context, m *forge.Client, owner, name, id string) (*workflowRun, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil || n <= 0 {
		return nil, zip.ErrBadRequest("a forge run is addressed by its numeric id")
	}
	r, err := m.Run(ctx, owner, name, n)
	if err != nil {
		return nil, madeRefusal(err, "no such run on the forge")
	}
	jobs, err := m.Jobs(ctx, owner, name, n)
	if err != nil {
		return nil, madeRefusal(err, "no such run on the forge")
	}
	v := forgeRunView(owner, name, r)
	for _, j := range jobs {
		v.Jobs = append(v.Jobs, forgeJobView(j))
	}
	return &v, nil
}

// runLogRef addresses one run's log.
type runLogRef struct {
	// ID is the run, from the :id path segment: its id on the forge for a forge
	// repository, or the id this app gave it.
	ID string `json:"id"`
	// Repo is the forge repository the run belongs to, owner/name; empty for a
	// run this app scheduled.
	Repo string `json:"repo,omitempty"`
	// Job is one job of the run, by its id or its name. Empty reads every job
	// that failed.
	Job string `json:"job,omitempty"`
}

// jobLog is one job's output.
type jobLog struct {
	// ID is the job's id.
	ID int64 `json:"id"`
	// Name is the job's name in the workflow document.
	Name string `json:"name"`
	// Status is waiting, running, or how the job ended.
	Status string `json:"status"`
	// Failing are the lines of the whole log that say what failed, in order: a
	// failing test and what it reported, a failing package, a panic, a compiler
	// or vet diagnostic, and an error the runner annotated. At most 100.
	Failing []string `json:"failing"`
	// Log is the end of the job's output, at most 256 KiB, in whole lines.
	Log string `json:"log"`
	// Truncated says Log is the end of a longer log.
	Truncated bool `json:"truncated"`
	// Cut says the log is longer than 32 MiB and was read no further, so Log
	// ends where the read stopped rather than where the job's output did.
	Cut bool `json:"cut,omitempty"`
	// Fault is why this job's log could not be read; the other jobs still answer.
	Fault string `json:"fault,omitempty"`
}

// runLogs is a run and the logs of the jobs asked for.
type runLogs struct {
	// Run is the run the logs belong to.
	Run workflowRun `json:"run"`
	// Jobs are the jobs read: the ones the job filter names, or by default those
	// that failed, at most 4. Empty when nothing failed and no job was named.
	Jobs []jobLog `json:"jobs"`
	// More is how many more jobs matched than were read; read each with job=.
	More int `json:"more"`
}

// maxJobLogs bounds the logs one request reads.
const maxJobLogs = 4

// readRunLogs returns what a run's jobs wrote: one job's log, named by its id
// or its name, or by default the log of every job that failed, with the lines
// in each that say what failed picked out.
//
// A run of a forge repository is addressed by its id on the forge and the
// repository, owner/name; one this app scheduled by its id alone.
//
// Example: {"id": "17647", "repo": "hanzo-inc/cloud", "job": "unit"}
func (o ops) readRunLogs(ctx context.Context, in *runLogRef) (*runLogs, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var (
		run  *workflowRun
		read func(context.Context, runJob) (forge.Log, error)
	)
	if owner, name, ok := place(in.Repo); ok {
		m, err := forgeFor(ctx, t, owner)
		if err != nil {
			return nil, err
		}
		if run, err = readRun(ctx, m, owner, name, in.ID); err != nil {
			return nil, err
		}
		read = func(ctx context.Context, j runJob) (forge.Log, error) {
			l, err := m.Log(ctx, owner, name, j.ID)
			if err != nil {
				return forge.Log{}, madeRefusal(err, "no log for job "+j.Name)
			}
			return l, nil
		}
	} else {
		if run, err = o.getRun(ctx, &runRef{ID: in.ID}); err != nil {
			return nil, err
		}
		read = func(_ context.Context, j runJob) (forge.Log, error) {
			l, err := taskLog(o.s, t.org, j.ID)
			if err != nil {
				o.s.Log.Warn("git: read a task log", "org", t.org, "task", j.ID, "err", err)
				return forge.Log{}, errors.New("this job's log could not be read")
			}
			return l, nil
		}
	}
	out := &runLogs{Jobs: []jobLog{}}
	want := strings.TrimSpace(in.Job)
	for _, j := range run.Jobs {
		if want == "" && j.Status != Failure || want != "" && want != j.Name && want != strconv.FormatInt(j.ID, 10) {
			continue
		}
		if len(out.Jobs) == maxJobLogs {
			out.More++
			continue
		}
		jl := jobLog{ID: j.ID, Name: j.Name, Status: j.Status, Failing: []string{}}
		if l, err := read(ctx, j); err != nil {
			jl.Fault = err.Error()
		} else {
			jl.Failing, jl.Log, jl.Truncated, jl.Cut = append(jl.Failing, l.Failing...), l.Text, l.Truncated, l.Cut
		}
		out.Jobs = append(out.Jobs, jl)
	}
	if want != "" && len(out.Jobs) == 0 {
		return nil, zip.ErrNotFound("the run has no job " + want)
	}
	out.Run = *run
	out.Run.Jobs = nil
	return out, nil
}

// taskLog is the log a daemon here wrote for one task: one JSON line per line
// of output (runner.go append), read through the same reader the forge's is.
//
// The file is the tenant's own daemon's to grow, so it is read from its END: at
// most the last [taskLogRead] bytes, streamed line by line into forge.ReadLog,
// whose memory is bounded whatever it is fed.
func taskLog(s *cloud.Service[state], org string, task int64) (forge.Log, error) {
	blobs := s.State.logs
	if blobs == nil {
		return forge.Log{}, internalErr(objects.ErrNoBlobs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// A log this replica's volume still holds is carried before it is read.
	if err := carryOne(ctx, blobs, legacyLog(s.State.dataDir, org, task), org, task); err != nil {
		return forge.Log{}, internalErr(err)
	}
	f, truncated, err := logTail(ctx, blobs, org, task, taskLogRead)
	if err != nil {
		return forge.Log{}, internalErr(err)
	}
	defer f.Close()
	from := int64(0)
	if truncated {
		from = 1
	}
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(f, 64<<10)
		if from > 0 { // the line the seek landed inside, passed over a buffer at a time
			_, err := br.ReadSlice('\n')
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
		}
		for {
			line, err := br.ReadSlice('\n')
			full := !errors.Is(err, bufio.ErrBufferFull)
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n') // a line past the buffer is no line this wrote
			}
			var l struct {
				Content string `json:"content"`
			}
			if full && len(line) > 0 && json.Unmarshal(line, &l) == nil {
				if _, werr := io.WriteString(pw, l.Content+"\n"); werr != nil {
					return
				}
			}
			if err != nil {
				pw.Close()
				return
			}
		}
	}()
	l, err := forge.ReadLog(pr)
	pr.Close()
	if err != nil {
		return forge.Log{}, internalErr(err)
	}
	l.Truncated = l.Truncated || from > 0
	return l, nil
}

// taskLogRead is how much of the end of a daemon's task log is read. A test
// shrinks it to read a log past it without writing 32 MiB.
var taskLogRead int64 = 32 << 20

// taskView is one task of a run this app scheduled, as a job.
func taskView(k Task) runJob {
	v := runJob{ID: k.ID, Name: k.Job, Status: k.Status, Runner: k.Runner}
	if k.Started > 0 {
		v.StartedAt = time.Unix(0, k.Started).UTC()
	}
	if k.Stopped > 0 {
		v.EndedAt = time.Unix(0, k.Stopped).UTC()
	}
	var steps []struct {
		ID     int64  `json:"id"`
		Result string `json:"result"`
	}
	if json.Unmarshal([]byte(k.Steps), &steps) == nil {
		for _, s := range steps {
			st := Running
			if s.Result != "" {
				st = strings.ToLower(strings.TrimPrefix(s.Result, "RESULT_"))
			}
			v.Steps = append(v.Steps, runStep{Number: int(s.ID) + 1, Status: st})
		}
	}
	return v
}

// poolHealth is one label set's capacity on the forge and the work waiting on it.
type poolHealth struct {
	// Pool is the label set, sorted and joined by "+".
	Pool string `json:"pool"`
	// Labels are the labels in it.
	Labels []string `json:"labels"`
	// Runners is how many registered forge runners carry every one of them.
	Runners int `json:"runners"`
	// Online is how many of those the forge heard from lately.
	Online int `json:"online"`
	// Busy is how many of those are executing a job now.
	Busy int `json:"busy"`
	// LastOnline is when any of them was last heard from, in unix seconds; 0 when
	// none ever was, or the forge does not say.
	LastOnline int64 `json:"lastOnline"`
	// Queued is how many of the caller's jobs are waiting for a runner with this
	// label set. Queued with nothing Online is a dead pool.
	Queued int `json:"queued"`
}

// poolOf is a label set's name: sorted, joined by "+".
func poolOf(labels []string) string {
	l := slices.Clone(labels)
	sort.Strings(l)
	return strings.Join(slices.Compact(l), "+")
}

// launchPool is the pool a forge job's labels are run in: the platform starts a
// runner for any job a Linux node can run, labelled as the job asked, so the
// pool is the label set — and none for a job that asks for nothing or for a
// platform no node here is (macOS, Windows).
func launchPool(want []string) string {
	if len(want) == 0 {
		return ""
	}
	for _, l := range want {
		if cloud.Podless(l) {
			return ""
		}
	}
	return poolOf(want)
}

// forgeRunners reads the forge's runners and queue as the machine, through
// [fleetCache], and answers what the caller may see of them.
//
// The runners serve every tenant and are named for the repository whose job
// they took, and the queue names repositories too, so a SuperAdmin sees all of
// both. Anyone else sees the queued jobs of repositories the FORGE lets them
// read — sudoed as their own login, each answer remembered for [fleetFresh] —
// and the pools those jobs wait on, counted but never named: an org's
// namespace set is not enough, because the brand orgs are where sign-ups land.
//
// A forge that cannot be read is said in fault — its own words to a
// SuperAdmin, one sentence to anyone else — and the daemons here still answer.
var forgeRunners = func(ctx context.Context, t tenant) (runners []runnerView, pools []poolHealth, fault string) {
	super := principal.SuperFrom(ctx)
	owned := namespaces(t.org)
	if !super && len(owned) == 0 {
		return nil, []poolHealth{}, ""
	}
	fleet, jobs, err := fleetCache.read(ctx)
	if err != nil {
		if super {
			return nil, []poolHealth{}, "the forge's runners could not be read: " + err.Error()
		}
		return nil, []poolHealth{}, "the forge's runners could not be read"
	}
	if super {
		return fleetViews(fleet), health(fleet, jobs), ""
	}
	jobs = slices.DeleteFunc(slices.Clone(jobs), func(j forge.Job) bool {
		return !slices.ContainsFunc(owned, func(ns string) bool { return strings.EqualFold(ns, j.Owner) })
	})
	if len(jobs) == 0 {
		return nil, []poolHealth{}, ""
	}
	m, err := forgeAs(ctx)
	if err != nil {
		return nil, []poolHealth{}, "your forge login could not be read"
	}
	var mine []forge.Job
	asked := 0
	for _, j := range jobs {
		ok, known := fleetCache.readable(m.Actor(), j.Owner, j.Repo)
		if !known {
			if asked == queueRepos {
				continue
			}
			asked++
			_, rerr := m.Repo(ctx, j.Owner, j.Repo)
			ok = rerr == nil
			// Only the forge's answer is remembered: a timeout or a 5xx says nothing
			// about whether this person may read the repository.
			if ok || errors.Is(rerr, forge.ErrUnknownActor) || errors.Is(rerr, forge.ErrNotFound) || errors.Is(rerr, forge.ErrRefused) {
				fleetCache.remember(m.Actor(), j.Owner, j.Repo, ok)
			}
		}
		if ok {
			mine = append(mine, j)
		}
	}
	return nil, slices.DeleteFunc(health(fleet, mine), func(h poolHealth) bool { return h.Queued == 0 }), ""
}

// queueRepos bounds the repositories one request asks the forge the caller may
// read; what was asked is remembered, so the next request asks past them.
const queueRepos = 32

// fleetCache holds the forge's runner registry and queue for [fleetFresh]: both
// are whole walks as the site administrator, the same for every caller, and a
// dashboard asking every few seconds must not cost the forge two walks a time.
var fleetCache = &fleet{}

// fleetFresh is how long one read of the registry and queue answers — a
// failure included, so a forge that is down is asked once a window, not once a
// caller. fleetWalk bounds one read, whoever is waiting on it.
const (
	fleetFresh = 15 * time.Second
	fleetWalk  = 20 * time.Second
)

type fleet struct {
	mu      sync.Mutex
	at      time.Time
	runners []forge.Runner
	jobs    []forge.Job
	err     error
	// walk is the read in flight, closed when it lands; nil when none is.
	walk chan struct{}
	// seen is whether a forge login may read a repository, as the forge said
	// within fleetFresh, keyed login + "\x00" + owner/repo; swept is when it was
	// last cleared of answers past that.
	seen  map[string]seenRepo
	swept time.Time
}

type seenRepo struct {
	ok bool
	at time.Time
}

// read answers the registry and queue: the held copy while it is fresh, else
// ONE walk for every caller, made on its own deadline rather than the first
// caller's, so a caller that leaves takes nothing with it and every caller
// waits only as long as its own request lives.
func (f *fleet) read(ctx context.Context) ([]forge.Runner, []forge.Job, error) {
	f.mu.Lock()
	if !f.at.IsZero() && time.Since(f.at) < fleetFresh {
		defer f.mu.Unlock()
		return f.runners, f.jobs, f.err
	}
	if f.walk == nil {
		f.walk = make(chan struct{})
		go f.fetch(f.walk)
	}
	walk := f.walk
	f.mu.Unlock()
	select {
	case <-walk:
	case <-ctx.Done():
		return nil, nil, context.Cause(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runners, f.jobs, f.err
}

// fetch makes the one walk and files what it found, failure or not. It runs
// detached, outside any handler's recover, so a panic in it is filed as the
// walk's failure rather than ending the process with every waiter stranded.
func (f *fleet) fetch(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), fleetWalk)
	defer cancel()
	var (
		runners []forge.Runner
		jobs    []forge.Job
		err     error
	)
	defer func() {
		if p := recover(); p != nil {
			runners, jobs, err = nil, nil, fmt.Errorf("the walk panicked: %v", p)
			slog.Error("git: the forge's runners could not be read", "err", err)
		}
		f.mu.Lock()
		f.at, f.runners, f.jobs, f.err, f.walk = time.Now(), runners, jobs, err, nil
		f.mu.Unlock()
		close(done)
	}()
	var c *forge.Client
	c, err = madeSource(ctx)
	if err == nil {
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { runners, err = c.Runners(gctx); return err })
		g.Go(func() (err error) { jobs, err = c.Machine().Queued(gctx); return err })
		err = g.Wait()
	}
	if err != nil {
		slog.Warn("git: the forge's runners could not be read", "err", err)
		runners, jobs = nil, nil
	}
}

// readable is what the forge last said about login reading owner/repo, and
// whether it said so within fleetFresh.
func (f *fleet) readable(login, owner, repo string) (ok, known bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, found := f.seen[seenKey(login, owner, repo)]
	if !found || time.Since(s.at) >= fleetFresh {
		return false, false
	}
	return s.ok, true
}

// remember files what the forge said, dropping answers past fleetFresh so the
// map holds one window's worth.
func (f *fleet) remember(login, owner, repo string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[string]seenRepo{}
	}
	now := time.Now()
	if now.Sub(f.swept) >= fleetFresh {
		for k, s := range f.seen {
			if now.Sub(s.at) >= fleetFresh {
				delete(f.seen, k)
			}
		}
		f.swept = now
	}
	f.seen[seenKey(login, owner, repo)] = seenRepo{ok: ok, at: now}
}

func seenKey(login, owner, repo string) string {
	return login + "\x00" + strings.ToLower(owner+"/"+repo)
}

// fleetViews are the forge's runners in this surface's shape, busy and online
// first, then the most recently heard from.
func fleetViews(runners []forge.Runner) []runnerView {
	out := make([]runnerView, 0, len(runners))
	for _, r := range runners {
		out = append(out, runnerView{Name: r.Name, Pool: poolOf(r.Labels), Forge: true, Labels: r.Labels,
			Online: r.Online, Busy: r.Busy, Ephemeral: r.Ephemeral,
			LastOnline: unix(r.LastOnline), LastActive: unix(r.LastActive)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Busy != out[j].Busy {
			return out[i].Busy
		}
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		return out[i].LastOnline > out[j].LastOnline
	})
	return out
}

// health groups runners and queued jobs into pools: every label set a runner
// answers to or a queued job asks for. A runner counts toward a pool when it
// carries every label in it, which is how the forge hands it a job.
func health(runners []forge.Runner, jobs []forge.Job) []poolHealth {
	sets := map[string][]string{}
	for _, r := range runners {
		sets[poolOf(r.Labels)] = r.Labels
	}
	queued := map[string]int{}
	for _, j := range jobs {
		p := poolOf(j.Labels)
		sets[p] = j.Labels
		queued[p]++
	}
	out := make([]poolHealth, 0, len(sets))
	for name, labels := range sets {
		h := poolHealth{Pool: name, Labels: append([]string{}, slices.Compact(slices.Sorted(slices.Values(labels)))...), Queued: queued[name]}
		for _, r := range runners {
			if !holds(r.Labels, labels) {
				continue
			}
			h.Runners++
			if r.Online {
				h.Online++
			}
			if r.Busy {
				h.Busy++
			}
			h.LastOnline = max(h.LastOnline, unix(r.LastOnline))
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if dead(out[i]) != dead(out[j]) {
			return dead(out[i])
		}
		return out[i].Pool < out[j].Pool
	})
	return out
}

// dead is a pool with work waiting and no runner online to take it.
func dead(h poolHealth) bool { return h.Queued > 0 && h.Online == 0 }

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// workflowDirs are where a repository's workflows live, in the order the forge
// reads them: the first that holds any decides (setting.Actions.WorkflowDirs in
// hanzoai/git; journal.go reads the same two here).
var workflowDirs = []string{".hanzo/workflows", ".github/workflows"}

// forgeWorkflows reads a forge repository's workflow documents at the commit
// ref resolves to, each job placed in the pool the platform would run it in.
func forgeWorkflows(ctx context.Context, t tenant, owner, name, ref string) (*workflowList, error) {
	m, err := forgeFor(ctx, t, owner)
	if err != nil {
		return nil, err
	}
	sha, err := m.Resolve(ctx, owner, name, ref)
	if err != nil {
		return nil, madeRefusal(err, "no such repository or ref on the forge")
	}
	out := &workflowList{Data: []workflowView{}, Commit: sha}
	for _, dir := range workflowDirs {
		entries, err := m.List(ctx, owner, name, sha, dir)
		if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrUnknownActor) || errors.Is(err, forge.ErrNotDir) {
			continue // a repository need not carry either directory
		}
		if err != nil {
			return nil, madeRefusal(err, "no such repository on the forge")
		}
		for _, e := range entries {
			low := strings.ToLower(e.Name)
			if e.Dir || !strings.HasSuffix(low, ".yml") && !strings.HasSuffix(low, ".yaml") {
				continue
			}
			f, err := m.Read(ctx, owner, name, sha, e.Path)
			if err != nil {
				out.Data = append(out.Data, workflowView{Name: e.Path, Fault: "unreadable: " + err.Error()})
				continue
			}
			if f.Truncated {
				out.Data = append(out.Data, workflowView{Name: e.Path, Fault: "larger than a workflow is read"})
				continue
			}
			out.Data = append(out.Data, inspectWith(document{name: e.Path, body: f.Data}, launchPool))
		}
		if len(out.Data) > 0 {
			return out, nil
		}
	}
	return out, nil
}

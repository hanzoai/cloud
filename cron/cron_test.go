// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/kit"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
)

// fakeKube is an in-memory cluster: entries + job lifecycle.
type fakeKube struct {
	mu      sync.Mutex
	entries map[string]entry
	created []string
	// jobs marks created jobs done+succeeded immediately.
	failJobs    bool
	lostReplies int
	// why is what a failed Job says about its failure.
	why string
	// status is cron-status as recordRun leaves it; statusErr is what every
	// write of it answers instead.
	status    map[string]string
	statusErr error
}

func (f *fakeKube) listEnabled(context.Context) ([]entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]entry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeKube) getEntry(_ context.Context, name string) (entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[name]
	if !ok {
		return entry{}, fmt.Errorf("configmap %q not found", name)
	}
	return e, nil
}

func (f *fakeKube) hasActiveJob(context.Context, string, string) (bool, error) { return false, nil }

// createJob makes jobName once; asked again for the same name it adopts the Job
// already there. lostReplies makes that many creates land and report an error, as a
// create whose reply was lost.
func (f *fakeKube) createJob(_ context.Context, _, jobName string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.Contains(f.created, jobName) {
		return nil
	}
	f.created = append(f.created, jobName)
	if f.lostReplies > 0 {
		f.lostReplies--
		return fmt.Errorf("create %s: the reply was lost", jobName)
	}
	return nil
}

func (f *fakeKube) jobDone(context.Context, string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return true, !f.failJobs, nil
}

func (f *fakeKube) jobFailure(context.Context, string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.why
}

func (f *fakeKube) recordRun(_ context.Context, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return f.statusErr
	}
	if f.status == nil {
		f.status = map[string]string{}
	}
	for k, v := range data {
		f.status[k] = v
	}
	return nil
}

// quick is the pace these tests run cron at: a Job is looked at, and a firing
// retried, in a fraction of a second rather than after ten.
var quick = pace{watch: 50 * time.Millisecond, retry: 200 * time.Millisecond}

// newTestEngine embeds a real tasks engine on an ephemeral port with a
// worker registered for this package's workflows, wired to k.
func newTestEngine(t *testing.T, k kube) (*tasksengine.Embedded, tasksengine.View) {
	t.Helper()
	setWiring(k, luxlog.NewNoOpLogger(), quick)
	// A socket in this test's own dir: no port to pick, and nothing for a parallel
	// test to collide with.
	dir := kit.Sockets(t)
	eng, err := tasksengine.Embed(context.Background(), tasksengine.EmbedConfig{
		Address: filepath.Join(dir, "tasks.sock"),
		DataDir: dir,
		NodeID:  "cron-test",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	old := currentEngine
	currentEngine = func() *tasksengine.Embedded { return eng }
	t.Cleanup(func() { currentEngine = old })

	view := eng.View(tasksengine.Org(org()))
	if err := view.RegisterNamespace(tasksengine.Namespace{
		NamespaceInfo: tasksengine.NamespaceInfo{Name: namespace},
	}); err != nil {
		t.Fatalf("RegisterNamespace: %v", err)
	}

	cli, err := tasksclient.Dial(tasksclient.Options{
		Address:   eng.Address(),
		Namespace: namespace,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(cli.Close)
	w := tasksworker.New(cli, taskQueue, tasksworker.Options{})
	w.RegisterWorkflow(PokeWorkflow)
	w.RegisterWorkflow(JobWorkflow)
	w.RegisterWorkflow(ReconcileWorkflow)
	w.RegisterActivity(PokeActivity)
	w.RegisterActivity(RunJobActivity)
	w.RegisterActivity(ReconcileActivity)
	if err := w.Start(); err != nil {
		t.Fatalf("worker: %v", err)
	}
	t.Cleanup(w.Stop)
	return eng, view
}

// waitStatus polls the org-shard execution list until pred matches, for up to a
// minute: a shared CI runner stretches a run that retries.
func waitStatus(t *testing.T, view tasksengine.View, wfType, wantStatus string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if s, ok := findExec(t, view, wfType); ok && s == wantStatus {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	s, _ := findExec(t, view, wfType)
	t.Fatalf("workflow %s: status %q, want %q", wfType, s, wantStatus)
}

func findExec(t *testing.T, view tasksengine.View, wfType string) (string, bool) {
	t.Helper()
	execs, err := view.ListWorkflows(namespace)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	for _, e := range execs {
		if e.Type.Name == wfType {
			return e.Status, true
		}
	}
	return "", false
}

// TestPokeEndToEnd proves the whole durable path for a poke entry: schedule
// registered in the org shard → TriggerSchedule → engine dispatches to the
// loopback worker → PokeActivity re-reads the entry and hits the endpoint
// with the bearer from env → run completes IN THE ORG SHARD (what the
// console shows).
func TestPokeEndToEnd(t *testing.T) {
	var gotAuth string
	hits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		hits <- struct{}{}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv("CRON_TEST_BEARER", "sekret")

	fk := &fakeKube{entries: map[string]entry{
		"poke-probe": {
			Name: "poke-probe", Schedule: "*/15 * * * *", Kind: kindPoke,
			Poke: pokeSpec{URL: srv.URL, BearerEnv: "CRON_TEST_BEARER"},
		},
	}}
	_, view := newTestEngine(t, fk)

	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"poke-probe", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}

	select {
	case <-hits:
	case <-time.After(30 * time.Second):
		t.Fatal("poke endpoint never hit")
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("Authorization = %q, want bearer from env", gotAuth)
	}
	waitStatus(t, view, "PokeWorkflow", "WORKFLOW_EXECUTION_STATUS_COMPLETED")
}

// TestJobEndToEnd proves a job entry runs the k8s Job through the durable
// path and the run completes; and a FAILING Job fails the run visibly.
func TestJobEndToEnd(t *testing.T) {
	fk := &fakeKube{entries: map[string]entry{
		"backup-probe": {
			Name: "backup-probe", Schedule: "0 1 * * *", Kind: kindJob,
			JobYAML: []byte("apiVersion: batch/v1\nkind: Job\n"),
		},
	}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"backup-probe", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_COMPLETED")

	fk.mu.Lock()
	created := len(fk.created)
	fk.mu.Unlock()
	if created != 1 {
		t.Fatalf("jobs created = %d, want 1", created)
	}
}

// TestReconcileConverges pins reconcile semantics: registers new entries,
// deletes entries whose ConfigMap vanished, never touches foreign schedules
// or the reconcile self-schedule, and does NOT rewrite an unchanged entry
// (anchor preservation — rewriting resets CreateTime and pushes the next
// fire forever forward).
func TestReconcileConverges(t *testing.T) {
	fk := &fakeKube{entries: map[string]entry{
		"a": {Name: "a", Schedule: "0 4 * * *", Kind: kindPoke, Poke: pokeSpec{URL: "http://x/v1/sync"}},
	}}
	_, view := newTestEngine(t, fk)
	log := luxlog.NewNoOpLogger()

	// A foreign schedule reconcile must never touch.
	if err := view.CreateSchedule(tasksengine.Schedule{
		ScheduleId: "user-owned", Namespace: namespace,
		Spec:   tasksengine.ScheduleSpec{CronString: []string{"0 0 * * *"}},
		Action: tasksengine.ScheduleAction{WorkflowType: tasksengine.TypeRef{Name: "X"}, TaskQueue: "q"},
	}); err != nil {
		t.Fatalf("foreign schedule: %v", err)
	}

	if err := reconcile(context.Background(), view, fk, log); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	s1, ok, _ := view.DescribeSchedule(namespace, idPrefix+"a")
	if !ok {
		t.Fatal("entry a not registered")
	}

	// Unchanged second reconcile: anchor (CreateTime) must be preserved.
	time.Sleep(1100 * time.Millisecond) // RFC3339 second granularity
	if err := reconcile(context.Background(), view, fk, log); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	s2, _, _ := view.DescribeSchedule(namespace, idPrefix+"a")
	if s1.Info.CreateTime != s2.Info.CreateTime {
		t.Fatalf("unchanged entry was rewritten: CreateTime %s → %s", s1.Info.CreateTime, s2.Info.CreateTime)
	}

	// Spec change → rewritten; CM removal → deleted; foreign survives.
	fk.mu.Lock()
	fk.entries["a"] = entry{Name: "a", Schedule: "30 4 * * *", Kind: kindPoke, Poke: pokeSpec{URL: "http://x/v1/sync"}}
	fk.entries["b"] = entry{Name: "b", Schedule: "*/10 * * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")}
	fk.mu.Unlock()
	if err := reconcile(context.Background(), view, fk, log); err != nil {
		t.Fatalf("reconcile 3: %v", err)
	}
	s3, _, _ := view.DescribeSchedule(namespace, idPrefix+"a")
	if s3.Spec.CronString[0] != "30 4 * * *" {
		t.Fatalf("drifted entry not rewritten: %v", s3.Spec.CronString)
	}
	if _, ok, _ := view.DescribeSchedule(namespace, idPrefix+"b"); !ok {
		t.Fatal("new entry b not registered")
	}

	fk.mu.Lock()
	delete(fk.entries, "b")
	fk.mu.Unlock()
	if err := reconcile(context.Background(), view, fk, log); err != nil {
		t.Fatalf("reconcile 4: %v", err)
	}
	if _, ok, _ := view.DescribeSchedule(namespace, idPrefix+"b"); ok {
		t.Fatal("stale entry b not deleted")
	}
	if _, ok, _ := view.DescribeSchedule(namespace, "user-owned"); !ok {
		t.Fatal("foreign schedule was deleted — prefix ownership violated")
	}
}

// TestParseEntry pins the ConfigMap contract.
func TestParseEntry(t *testing.T) {
	if _, err := parseEntry("x", map[string]string{}); err == nil {
		t.Fatal("want error: missing schedule")
	}
	if _, err := parseEntry("x", map[string]string{"schedule": "0 1 * * *"}); err == nil {
		t.Fatal("want error: neither job nor poke")
	}
	if _, err := parseEntry("x", map[string]string{
		"schedule": "0 1 * * *", "job.yaml": "kind: Job", "poke.json": `{"url":"http://a"}`,
	}); err == nil {
		t.Fatal("want error: both set")
	}
	e, err := parseEntry("x", map[string]string{"schedule": "0 1 * * *", "poke.json": `{"url":"http://a","bearerEnv":"T"}`})
	if err != nil || e.Kind != kindPoke || e.Poke.URL != "http://a" {
		t.Fatalf("poke parse: %+v err=%v", e, err)
	}
	op, err := parseEntry("x", map[string]string{"schedule": "0 * * * *", "poke.json": `{"app":"commerce","op":"billing_renew"}`})
	if err != nil || op.Kind != kindPoke || op.Poke.App != "commerce" || op.Poke.Op != "billing_renew" {
		t.Fatalf("op poke parse: %+v err=%v", op, err)
	}
	for _, bad := range []string{`{"app":"commerce"}`, `{"op":"billing_renew"}`, `{"app":"commerce","op":"billing_renew","url":"http://a"}`, `{}`} {
		if _, err := parseEntry("x", map[string]string{"schedule": "0 * * * *", "poke.json": bad}); err == nil {
			t.Fatalf("want error: poke %s", bad)
		}
	}
	j, err := parseEntry("x", map[string]string{"schedule": "0 1 * * *", "job.yaml": "kind: Job"})
	if err != nil || j.Kind != kindJob {
		t.Fatalf("job parse: %+v err=%v", j, err)
	}
}

// A FIRING THAT REACHES A PROCESS NOT HOLDING CRON IS AN ERROR THE ENGINE RETRIES,
// NEVER A SUCCESS. It used to answer an empty success, so the console showed a run
// that nobody made; a run says it ran only where it ran.
func TestAFiringWhereCronIsNotHeldIsRetried(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()
	fk := &fakeKube{entries: map[string]entry{
		"poke-probe": {Name: "poke-probe", Kind: kindPoke, Poke: pokeSpec{URL: srv.URL}},
		"backup":     {Name: "backup", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	setWiring(fk, luxlog.NewNoOpLogger(), quick)
	elsewhere, err := coord.New(coord.Config{Self: coord.Member{ID: "cloud-1"}, Declared: 2})
	if err != nil {
		t.Fatal(err)
	}
	was := lead
	lead = elsewhere.Lead("cron")
	t.Cleanup(func() { lead = was })

	if _, err := PokeActivity(context.Background(), EntryInput{Name: "poke-probe"}); !errors.Is(err, coord.ErrNotHeld) || final(err) {
		t.Fatalf("a poke where cron is not held: %v (final=%v), want a retryable refusal", err, final(err))
	}
	if _, err := RunJobActivity(context.Background(), EntryInput{Name: "backup"}); !errors.Is(err, coord.ErrNotHeld) || final(err) {
		t.Fatalf("a job where cron is not held: %v (final=%v), want a retryable refusal", err, final(err))
	}
	if hit || len(fk.created) != 0 {
		t.Fatalf("a process not holding cron acted: poked=%v jobs=%d", hit, len(fk.created))
	}
}

// A JOB THAT RAN AND FAILED IS NOT RUN AGAIN, now that the activity is retried for
// the firings that never made one: the failure is final and the run fails once.
func TestAJobThatRanAndFailedIsNotRetried(t *testing.T) {
	fk := &fakeKube{failJobs: true, entries: map[string]entry{
		"backup-probe": {Name: "backup-probe", Schedule: "0 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"backup-probe", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_FAILED")
	fk.mu.Lock()
	created := len(fk.created)
	fk.mu.Unlock()
	if created != 1 {
		t.Fatalf("a Job that failed was created %d times; want once", created)
	}
}

// final reports whether err ends its activity without a retry.
func final(err error) bool {
	var e *temporal.Error
	return errors.As(err, &e) && e.NonRetryable
}

// ONE FIRING MAKES ONE JOB. A create that landed and reported an error is retried,
// and the retry finds the Job it made instead of making a second; another firing
// names another Job.
func TestOneFiringMakesOneJob(t *testing.T) {
	fk := &fakeKube{lostReplies: 1, entries: map[string]entry{
		"backup-probe": {Name: "backup-probe", Schedule: "0 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"backup-probe", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_COMPLETED")
	fk.mu.Lock()
	created := append([]string(nil), fk.created...)
	fk.mu.Unlock()
	if len(created) != 1 {
		t.Fatalf("one firing whose create reply was lost made %d Jobs: %v", len(created), created)
	}
	if jobNameFor("backup-probe", "wf/run-1") == jobNameFor("backup-probe", "wf/run-2") {
		t.Fatal("two firings name one Job")
	}
	if n := jobNameFor(strings.Repeat("e", 80), "wf/run-1"); len(n) > 63 {
		t.Fatalf("a Job name of %d characters is not a DNS label", len(n))
	}
}

// THE TICK NAMES THE JOB. A schedule's own fire carries the tick the engine
// stamped on it: two engines (one per pod) firing one tick, one of them a minute
// late after a restart, and a retry hours later all name one Job. A run with no
// tick — an operator's trigger — names its own, and the next tick another.
func TestOneTickNamesOneJob(t *testing.T) {
	tick := func(at string) *tasksengine.WorkflowExecution {
		return &tasksengine.WorkflowExecution{SearchAttrs: map[string]any{"TemporalScheduledStartTime": at}}
	}
	onTime := firingOf(tick("2026-10-03T01:00:00Z"), "wf-a/run-a", "", time.Time{})
	late := firingOf(tick("2026-10-03T01:00:00Z"), "wf-b/run-b", "", time.Time{}) // the other engine, catching up
	if onTime != late || onTime != "2026-10-03T01:00:00Z" {
		t.Fatalf("one tick fired by two engines named %q and %q", onTime, late)
	}
	if next := firingOf(tick("2026-10-04T01:00:00Z"), "wf-c/run-c", "", time.Time{}); next == onTime {
		t.Fatal("two nights name one Job")
	}
	manual := firingOf(&tasksengine.WorkflowExecution{}, "wf-d/run-d", "", time.Time{})
	again := firingOf(&tasksengine.WorkflowExecution{}, "wf-e/run-e", "", time.Time{})
	if manual == onTime || manual == again {
		t.Fatalf("operator triggers named %q and %q (the tick is %q)", manual, again, onTime)
	}
}

// A RUN BEGUN BEFORE THE DEPLOY FINDS THE JOB IT MADE. The previous release named a
// fire's Job by the tick it began within a minute of, from the start its workflow
// recorded, and stamped no tick. Its retry, run by this release, names that Job
// again; a trigger it began more than a minute after a tick keeps its own name, as
// it had; and a run this release begins records no start, so an operator's
// trigger, with no tick either, is its own.
func TestARunBegunBeforeTheDeployFindsItsJob(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	old := &tasksengine.WorkflowExecution{SearchAttrs: map[string]any{"ScheduleId": "nightly"}}
	if got := firingOf(old, "wf-a/run-a", "0 1 * * *", at("2026-10-03T01:00:04Z")); got != "2026-10-03T01:00:00Z" {
		t.Fatalf("a fire the previous release began names %q, not its tick", got)
	}
	if got := firingOf(old, "wf-b/run-b", "0 1 * * *", at("2026-10-03T01:07:00Z")); got != "wf-b/run-b" {
		t.Fatalf("a trigger the previous release began names %q, not its run", got)
	}
	if got := firingOf(old, "wf-c/run-c", "0 1 * * *", time.Time{}); got != "wf-c/run-c" {
		t.Fatalf("a trigger this release began names %q, not its run", got)
	}
	stamped := &tasksengine.WorkflowExecution{SearchAttrs: map[string]any{"ScheduleId": "nightly", "TemporalScheduledStartTime": "2026-10-04T01:00:00Z"}}
	if got := firingOf(stamped, "wf-d/run-d", "0 1 * * *", at("2026-10-03T01:00:04Z")); got != "2026-10-04T01:00:00Z" {
		t.Fatalf("a stamped tick yields to a recorded start: %q", got)
	}
}

// A SCHEDULE'S OWN FIRE, THROUGH THE ENGINE, NAMES ITS JOB BY THE TICK. The entry
// fires every second; the run the engine starts at the next second carries that
// second, and the Job is named for it.
func TestAScheduledFireNamesItsJobByTheTick(t *testing.T) {
	fk := &fakeKube{entries: map[string]entry{
		"secondly": {Name: "secondly", Schedule: "* * * * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	_, view := newTestEngine(t, fk)
	began := time.Now().UTC().Truncate(time.Second)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	deadline := time.Now().Add(80 * time.Second)
	for time.Now().Before(deadline) {
		fk.mu.Lock()
		created := append([]string(nil), fk.created...)
		fk.mu.Unlock()
		if len(created) > 0 {
			for at := time.Now().UTC().Truncate(time.Second); !at.Before(began); at = at.Add(-time.Second) {
				if created[0] == jobNameFor("secondly", at.Format(time.RFC3339)) {
					return
				}
			}
			t.Fatalf("a scheduled fire named its Job %q, not by its second", created[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the schedule never fired")
}

// A release-lag credential rejection must be observable, not retried as though
// the Job had never started. Reconciliation owns the desired schedule; a string
// in container logs is not authority to disable production monitoring.
func TestJobFailsClosedOn401(t *testing.T) {
	const why = "container lag: urllib.error.HTTPError: HTTP Error 401: Unauthorized"
	fk := &fakeKube{failJobs: true, why: why, entries: map[string]entry{
		"release-lag": {Name: "release-lag", Schedule: "0 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"release-lag", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_FAILED")
	run := statusOf(t, fk, "cron-release-lag", func(s runStatus) bool { return s.Result == "failed" })
	if !strings.Contains(run.Error, why) {
		t.Fatalf("release-lag must report the credential rejection: %+v", run)
	}
	fk.mu.Lock()
	defer fk.mu.Unlock()
	if len(fk.created) != 1 {
		t.Fatalf("failed Job must not be retried: %v", fk.created)
	}
}

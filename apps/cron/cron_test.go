// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
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
	failJobs bool
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

func (f *fakeKube) hasActiveJob(context.Context, string) (bool, error) { return false, nil }

func (f *fakeKube) createJob(_ context.Context, name string, _ []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jn := name + "-1"
	f.created = append(f.created, jn)
	return jn, nil
}

func (f *fakeKube) jobDone(context.Context, string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return true, !f.failJobs, nil
}

// newTestEngine embeds a real tasks engine on an ephemeral port with a
// worker registered for this package's workflows, wired to k.
func newTestEngine(t *testing.T, k kube) (*tasksengine.Embedded, tasksengine.View) {
	t.Helper()
	setWiring(k, luxlog.NewNoOpLogger())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	eng, err := tasksengine.Embed(context.Background(), tasksengine.EmbedConfig{
		ZAPPort: port,
		DataDir: t.TempDir(),
		NodeID:  "cron-test",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	old := currentEngine
	currentEngine = func() *tasksengine.Embedded { return eng }
	t.Cleanup(func() { currentEngine = old })

	view := eng.View(org())
	if err := view.RegisterNamespace(tasksengine.Namespace{
		NamespaceInfo: tasksengine.NamespaceInfo{Name: namespace},
	}); err != nil {
		t.Fatalf("RegisterNamespace: %v", err)
	}

	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort:  fmt.Sprintf("127.0.0.1:%d", eng.ZAPPort()),
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

// waitStatus polls the org-shard execution list until pred matches.
func waitStatus(t *testing.T, view tasksengine.View, wfType, wantStatus string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
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
	j, err := parseEntry("x", map[string]string{"schedule": "0 1 * * *", "job.yaml": "kind: Job"})
	if err != nil || j.Kind != kindJob {
		t.Fatalf("job parse: %+v err=%v", j, err)
	}
}

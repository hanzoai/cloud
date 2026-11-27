package cron

import (
	"context"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// M1: the nightly backup's Job failed; an operator triggers the schedule to run it
// again. Named by the tick, the manual run adopts the failed Job and reports its
// failure without running anything — until the Job's 8h TTL reaps it.
func TestRed4ManualRerunOfFailedTickRunsNothing(t *testing.T) {
	fk := &fakeKube{failJobs: true, entries: map[string]entry{
		"sql-backup": {Name: "sql-backup", Schedule: "30 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, req := range []string{"nightly", "operator-rerun"} {
		if _, err := view.TriggerSchedule(namespace, idPrefix+"sql-backup", req); err != nil {
			t.Fatalf("TriggerSchedule: %v", err)
		}
	}
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		execs, _ := view.ListWorkflows(namespace)
		n := 0
		for _, e := range execs {
			if e.Type.Name == "JobWorkflow" && e.Status == "WORKFLOW_EXECUTION_STATUS_FAILED" {
				n++
			}
		}
		if n == 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	fk.mu.Lock()
	created := append([]string(nil), fk.created...)
	fk.mu.Unlock()
	t.Logf("two runs (nightly + operator re-run) created %d Jobs: %v", len(created), created)
	if len(created) < 2 {
		t.Errorf("M1: the operator's re-run of a failed Job ran nothing and reported the old failure")
	}
}

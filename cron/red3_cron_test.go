package cron

import (
	"context"
	"testing"

	luxlog "github.com/luxfi/log"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// reaping is a cluster whose Jobs carry ttlSecondsAfterFinished: 0 (or whose TTL
// passed before the watcher looked): a Job that finished is deleted, so the watch
// reads NotFound.
type reaping struct {
	*fakeKube
	live map[string]bool
}

func (r *reaping) createJob(ctx context.Context, name, jobName string, m []byte) error {
	r.mu.Lock()
	if r.live[jobName] {
		r.mu.Unlock()
		return nil
	}
	r.live[jobName] = true
	r.created = append(r.created, jobName)
	r.mu.Unlock()
	return nil
}

func (r *reaping) jobDone(_ context.Context, jobName string) (bool, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, jobName) // it ran, succeeded, and the TTL controller deleted it
	return false, false, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, jobName)
}

// C1: one firing of a Job whose Job is reaped on completion runs the Job again on
// every retry (the watch's NotFound is now retryable; at 39237a9359 it was final).
func TestRed3ReapedJobRunsAgainPerRetry(t *testing.T) {
	fk := &reaping{fakeKube: &fakeKube{entries: map[string]entry{
		"backup": {Name: "backup", Schedule: "0 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}, live: map[string]bool{}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"backup", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	// The run has ended when the count is read, so every retry it would make is made.
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_FAILED")
	fk.mu.Lock()
	created := append([]string(nil), fk.created...)
	fk.mu.Unlock()
	t.Logf("one firing created %d Jobs: %v", len(created), created)
	if len(created) > 1 {
		t.Errorf("C1: one firing ran its Job %d times", len(created))
	}
}

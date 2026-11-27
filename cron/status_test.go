// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	luxlog "github.com/luxfi/log"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

// statusOf reads one key of the fake's cron-status, waiting up to ten seconds for
// it to satisfy ok.
func statusOf(t *testing.T, fk *fakeKube, key string, ok func(runStatus) bool) runStatus {
	t.Helper()
	var last string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		fk.mu.Lock()
		last = fk.status[key]
		fk.mu.Unlock()
		var s runStatus
		if last != "" && json.Unmarshal([]byte(last), &s) == nil && ok(s) {
			return s
		}
	}
	t.Fatalf("cron-status %s is %q", key, last)
	return runStatus{}
}

// A run's outcome, and why it failed, is in cron-status, where Hanzo CD shows it to
// anyone who may read the namespace's ConfigMaps. Before this, the reason was only
// in places a platform administrator alone could read (hanzo-inc/cloud#828).
func TestARunAndWhyItFailedAreInCronStatus(t *testing.T) {
	const why = "BackoffLimitExceeded: Job has reached the specified backoff limit; " +
		"container upload: Error, exit 1; its last lines: ERROR: upload of x HTTP 403"
	fk := &fakeKube{failJobs: true, why: why, entries: map[string]entry{
		"backup-probe": {Name: "backup-probe", Schedule: "0 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
	}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"backup-probe", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	failed := func(s runStatus) bool { return s.Result == "failed" }
	run := statusOf(t, fk, "cron-backup-probe", failed)
	if !strings.Contains(run.Error, why) || run.Kind != "job" || !strings.HasPrefix(run.Made, "backup-probe-") {
		t.Fatalf("the failed run reads %+v; want kind job, its Job, and %q", run, why)
	}
	if _, err := time.Parse(time.RFC3339, run.At); err != nil {
		t.Fatalf("at %q: %v", run.At, err)
	}
	firstFailure := statusOf(t, fk, "cron-backup-probe.failed", failed)

	// The next run succeeds: the last run says so, and the last failure stays readable.
	fk.mu.Lock()
	fk.failJobs = false
	fk.mu.Unlock()
	if _, err := view.TriggerSchedule(namespace, idPrefix+"backup-probe", "req-2"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	ok := statusOf(t, fk, "cron-backup-probe", func(s runStatus) bool { return s.Result == "ok" })
	if ok.Error != "" {
		t.Fatalf("a run that succeeded carries an error: %+v", ok)
	}
	if kept := statusOf(t, fk, "cron-backup-probe.failed", failed); kept != firstFailure {
		t.Fatalf("a success changed the last failure: %+v, was %+v", kept, firstFailure)
	}
}

// cron-status is a reading, never a reason: a run whose status cannot be written
// (the ConfigMap absent, the write refused) ends as it would have.
func TestAStatusThatCannotBeWrittenChangesNoRun(t *testing.T) {
	fk := &fakeKube{statusErr: apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, statusMap, errors.New("no patch")),
		entries: map[string]entry{
			"probe": {Name: "probe", Schedule: "0 1 * * *", Kind: kindJob, JobYAML: []byte("kind: Job\n")},
		}}
	_, view := newTestEngine(t, fk)
	if err := reconcile(context.Background(), view, fk, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := view.TriggerSchedule(namespace, idPrefix+"probe", "req-1"); err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_COMPLETED")
	fk.mu.Lock()
	defer fk.mu.Unlock()
	if len(fk.created) != 1 {
		t.Fatalf("the run made %d Jobs, want 1: %v", len(fk.created), fk.created)
	}
}

// A process that does not hold cron writes nothing: the one that holds it runs the
// entry and writes its outcome, and "not here" is not an outcome of the entry.
func TestARunElsewhereIsNotRecorded(t *testing.T) {
	fk := &fakeKube{}
	setWiring(fk, luxlog.NewNoOpLogger(), quick)
	t.Cleanup(func() { setWiring(nil, nil, steady) })
	record(context.Background(), "job", "probe", "", fmt.Errorf("cron %q: %w", "probe", coord.ErrNotHeld))
	if len(fk.status) != 0 {
		t.Fatalf("a run elsewhere was recorded: %v", fk.status)
	}
	long := strings.Repeat("é", statusErrBytes) // two bytes a rune: the cut lands inside one
	record(context.Background(), "poke", "probe", "plane:commerce/billing_renew", errors.New(long))
	var s runStatus
	if err := json.Unmarshal([]byte(fk.status["cron-probe"]), &s); err != nil {
		t.Fatalf("cron-probe: %v", err)
	}
	if len(s.Error) > statusErrBytes+len(" …") || !strings.HasSuffix(s.Error, " …") || strings.ContainsRune(s.Error, '�') {
		t.Fatalf("a long error reads %d bytes, ending %q", len(s.Error), s.Error[max(0, len(s.Error)-8):])
	}
}

// The write is a merge patch of one entry's keys: another entry's keys, and keys
// nothing in cron wrote, are left as they are.
func TestRecordRunSetsOnlyItsOwnKeys(t *testing.T) {
	cs := fake.NewClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: statusMap, Namespace: "hanzo"},
		Data:       map[string]string{"cron-sql-backup": `{"result":"failed"}`, "note": "kept"},
	})
	k := &clusterKube{cs: cs, ns: "hanzo"}
	if err := k.recordRun(context.Background(), map[string]string{"cron-hanzo-git-backup": `{"result":"ok"}`}); err != nil {
		t.Fatalf("recordRun: %v", err)
	}
	cm, err := cs.CoreV1().ConfigMaps("hanzo").Get(context.Background(), statusMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"cron-sql-backup": `{"result":"failed"}`, "note": "kept", "cron-hanzo-git-backup": `{"result":"ok"}`}
	if fmt.Sprint(cm.Data) != fmt.Sprint(want) {
		t.Fatalf("cron-status holds %v, want %v", cm.Data, want)
	}

	// Absent, the ConfigMap is not made here: universe declares it, and cron may only patch it.
	gone := &clusterKube{cs: fake.NewClientset(), ns: "hanzo"}
	if err := gone.recordRun(context.Background(), map[string]string{"cron-x": "{}"}); !apierrors.IsNotFound(err) {
		t.Fatalf("recordRun into no ConfigMap: %v, want NotFound", err)
	}
}

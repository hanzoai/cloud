// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A failed backup's run says why, in the engine's own record of the failure. It
// said "job X failed" and nothing more, and the Job and its pods are reaped, so
// two backups failed every night for over a week with no reason anyone could read
// without a shell in the cluster (hanzo-inc/cloud#828).
func TestAFailedJobsRunSaysWhy(t *testing.T) {
	const why = "BackoffLimitExceeded: Job has reached the specified backoff limit; " +
		"container dump: Error, exit 1; its last lines: ERROR: sql-read answered as the primary"
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
	waitStatus(t, view, "JobWorkflow", "WORKFLOW_EXECUTION_STATUS_FAILED")

	var last []string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		streaks, err := view.FailureStreaks(namespace)
		if err != nil {
			t.Fatalf("FailureStreaks: %v", err)
		}
		last = last[:0]
		for _, s := range streaks {
			last = append(last, s.LastError)
			if strings.Contains(s.LastError, why) {
				return
			}
		}
	}
	t.Fatalf("no failure the engine recorded says why the Job failed: %q", last)
}

// failureOf reads the Job's verdict, then the newest pod with failure evidence
// and the first failed container, including its last lines where available.
func TestAFailureNamesTheVerdictThePodAndItsLastLines(t *testing.T) {
	failed := func(reason, msg string) *batchv1.Job {
		return &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: msg},
		}}}
	}
	at := func(min int) metav1.Time { return metav1.NewTime(time.Date(2026, 11, 17, 1, 30+min, 0, 0, time.UTC)) }
	exited := func(name, reason string, code int32) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code}}}
	}
	ok := exited("ok", "Completed", 0)

	for _, c := range []struct {
		name string
		job  *batchv1.Job
		pods []corev1.Pod
		want string
	}{{
		name: "a script that exited on its error, the newest of three tries",
		job:  failed("BackoffLimitExceeded", "Job has reached the specified backoff limit"),
		pods: []corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "try-1", CreationTimestamp: at(0)},
				Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{exited("dump", "OOMKilled", 137)}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "try-3", CreationTimestamp: at(2)},
				Status: corev1.PodStatus{
					InitContainerStatuses: []corev1.ContainerStatus{ok},
					ContainerStatuses:     []corev1.ContainerStatus{exited("upload", "Error", 1)}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "try-2", CreationTimestamp: at(1)}},
		},
		want: "BackoffLimitExceeded: Job has reached the specified backoff limit; container upload: Error, exit 1; " +
			"its last lines: try-3/upload said | ERROR: upload of x HTTP 403",
	}, {
		name: "a pod the node evicted",
		job:  failed("BackoffLimitExceeded", ""),
		pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{
			Reason: "Evicted", Message: "Pod ephemeral local storage usage exceeds the total limit of containers 20Gi."}}},
		want: "BackoffLimitExceeded; pod p Evicted: Pod ephemeral local storage usage exceeds the total limit of containers 20Gi.",
	}, {
		name: "a pod that never started",
		job:  failed("DeadlineExceeded", "Job was active longer than specified deadline"),
		pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "dump", State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: `secret "s3-backup" not found`}}}},
		}}},
		want: `DeadlineExceeded: Job was active longer than specified deadline; container dump never started: CreateContainerConfigError: secret "s3-backup" not found`,
	}, {
		name: "a pod no node would take",
		job:  failed("DeadlineExceeded", "Job was active longer than specified deadline"),
		pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: "Unschedulable", Message: "0/6 nodes are available: 6 Insufficient cpu."}},
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "dump", State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}},
		}}},
		want: "DeadlineExceeded: Job was active longer than specified deadline; pod p not scheduled: Unschedulable: 0/6 nodes are available: 6 Insufficient cpu.",
	}, {
		name: "a Job whose pods are gone",
		job:  failed("DeadlineExceeded", "Job was active longer than specified deadline"),
		want: "DeadlineExceeded: Job was active longer than specified deadline; no Job pods found",
	}, {
		name: "newest pod has no status but older retry failed",
		job: failed("BackoffLimitExceeded", ""),
		pods: []corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "try-2", CreationTimestamp: at(2)}},
			{ObjectMeta: metav1.ObjectMeta{Name: "try-1", CreationTimestamp: at(1)}, Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{exited("backup", "OOMKilled", 137)}}},
		},
		want: "BackoffLimitExceeded; container backup: OOMKilled, exit 137; its last lines: try-1/backup said | ERROR: upload of x HTTP 403",
	}, {
		name: "a restarted container retains its previous exit",
		job: failed("BackoffLimitExceeded", ""),
		pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{Name: "backup", LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}},
		}}},
		want: "BackoffLimitExceeded; container backup (previous attempt): OOMKilled, exit 137",
	}, {
		name: "pods exist without failure evidence",
		job: failed("BackoffLimitExceeded", ""),
		pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p"}}},
		want: "BackoffLimitExceeded; Job pods have no recorded failure details",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got := failureOf(c.job, c.pods, func(pod, container string) string {
				return "\n" + pod + "/" + container + " said\n  ERROR: upload of x HTTP 403\n"
			})
			if got != c.want {
				t.Fatalf("failureOf =\n  %q\nwant\n  %q", got, c.want)
			}
		})
	}
}

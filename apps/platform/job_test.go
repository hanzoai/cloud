package platform

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// waitForJob is how every caller learns a Job finished, so the three outcomes it can
// report are the three a caller has to handle: succeeded, failed, and still running
// when the deadline passed. A missing Job is the third — nothing to read is not
// success.
func TestWaitForJob(t *testing.T) {
	k := fakeK8s()
	seed := func(name, field string) {
		job := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": name, "namespace": k.buildNS},
			"status":   map[string]any{field: int64(1)},
		}}
		if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	seed("ok", "succeeded")
	seed("bad", "failed")

	if err := k.waitForJob(context.Background(), "ok", time.Minute); err != nil {
		t.Fatalf("succeeded job: want nil, got %v", err)
	}
	if err := k.waitForJob(context.Background(), "bad", time.Minute); err == nil {
		t.Fatalf("failed job: want error, got nil")
	}
	if err := k.waitForJob(context.Background(), "ghost", 0); err == nil {
		t.Fatalf("missing job past deadline: want timeout error, got nil")
	}
}

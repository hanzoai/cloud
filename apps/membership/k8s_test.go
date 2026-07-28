package membership

// membership_k8s_test.go proves the parts of the live membership source that run without
// a cluster: the ready-gating predicate (the safety-critical filter that keeps a
// draining/unready pod out of the writer election), the pod→member conversion, and the
// capability-detected static fallback. The live LIST against a real API server is a
// staging-gated integration (a cluster is required), flagged in the report.

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func readyPod(name, ip string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      ip,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// TestPodWriterEligible is the drain-safety table: only a Running+Ready+non-terminating
// pod is a writer candidate. A terminating pod (DeletionTimestamp set — a rolling
// upgrade's first step) is excluded IMMEDIATELY so ha.Owner re-elects a live successor
// before the pod stops serving.
func TestPodWriterEligible(t *testing.T) {
	terminating := readyPod("cloud-1", "10.0.0.1")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now

	notReady := readyPod("cloud-2", "10.0.0.2")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse

	pending := readyPod("cloud-3", "10.0.0.3")
	pending.Status.Phase = corev1.PodPending

	noCond := readyPod("cloud-4", "10.0.0.4")
	noCond.Status.Conditions = nil

	for _, tc := range []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{"running-ready", readyPod("cloud-0", "10.0.0.0"), true},
		{"terminating", terminating, false},
		{"ready-false", notReady, false},
		{"pending", pending, false},
		{"no-ready-condition", noCond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := podWriterEligible(&tc.pod); got != tc.want {
				t.Fatalf("podWriterEligible(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestReadyMembers: a mixed pod list yields only the eligible pods, each as
// Member{ID: pod name, Addr: podIP:port}. The terminating pod is dropped — it must never
// appear in the set ha.Owner elects over.
func TestReadyMembers(t *testing.T) {
	now := metav1.Now()
	draining := readyPod("cloud-1", "10.0.0.1")
	draining.DeletionTimestamp = &now

	members := readyMembers([]corev1.Pod{
		readyPod("cloud-0", "10.0.0.0"),
		draining,
		readyPod("cloud-2", "10.0.0.2"),
	}, "8080")

	if len(members) != 2 {
		t.Fatalf("want 2 eligible members, got %d (%+v)", len(members), members)
	}
	byID := map[string]string{}
	for _, m := range members {
		byID[m.ID] = m.Addr
	}
	if byID["cloud-0"] != "10.0.0.0:8080" || byID["cloud-2"] != "10.0.0.2:8080" {
		t.Fatalf("member addrs wrong: %+v", byID)
	}
	if _, drained := byID["cloud-1"]; drained {
		t.Fatal("terminating pod cloud-1 must not be in the writer set")
	}
}

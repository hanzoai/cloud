package sandbox

// lost_test.go — a lease whose pod the cluster ended on its own (evicted for
// filling its working directory, deleted, exited) answers again under the same
// id, and the call that finds it gone is answered 503, never 502.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kubelet answers an exec the way the real one does: through the pod the fake
// apiserver holds, so a pod that is not there is the apiserver's NotFound and a
// pod whose container is not running is "container not found".
type kubelet struct {
	*files
	down error // a channel failure on a pod that is alive
}

func (k *kubelet) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	obj, err := k.rt.pods().Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); phase != "Running" {
		return errors.New(`unable to upgrade connection: container not found ("sandbox")`)
	}
	if k.down != nil {
		return k.down
	}
	return k.files.stream(ctx, ns, pod, argv, stdin, stdout, stderr)
}

// lostService is parking's fake cluster with the kubelet in front of its disks.
func lostService(t *testing.T) (*Service, *kubelet) {
	t.Helper()
	s, fs, _ := parking(t)
	k := &kubelet{files: fs}
	s.State.rt.str = k
	return s, k
}

// evict ends m's pod the way the kubelet does when its working directory is over
// its limit: the object stays, Failed, with the reason on it.
func evict(t *testing.T, s *Service, m Sandbox) {
	t.Helper()
	if err := evicted(s, m); err != nil {
		t.Fatalf("evict: %v", err)
	}
}

func evicted(s *Service, m Sandbox) error {
	ctx := context.Background()
	pod, err := s.State.rt.pods().Get(ctx, m.Pod, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(pod.Object, "Failed", "status", "phase")
	_ = unstructured.SetNestedField(pod.Object, "Evicted", "status", "reason")
	_ = unstructured.SetNestedField(pod.Object, `Usage of EmptyDir volume "work" exceeds the limit "2Gi". `, "status", "message")
	_, err = s.State.rt.pods().Update(ctx, pod, metav1.UpdateOptions{})
	return err
}

func httpStatus(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func row(t *testing.T, s *Service, org, id string) Sandbox {
	t.Helper()
	st, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	m, err := st.Get(context.Background(), org, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return m
}

// An evicted pod under a running lease: the next call is 503 naming why, the same
// id gets a new pod, and the call after that runs.
func TestAnEvictedSandboxComesBackUnderItsID(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _ := lostService(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		evict(t, s, m)

		_, err = Run(s, ctx, "acme", m.ID, Cmd{Command: "true"})
		if httpStatus(err) != http.StatusServiceUnavailable {
			t.Fatalf("a run on an evicted pod answered %d %v, want 503", httpStatus(err), err)
		}
		for _, want := range []string{m.ID, "Evicted", `exceeds the limit "2Gi"`, "run it again"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the answer %q does not say %q", err, want)
			}
		}
		back := row(t, s, "acme", m.ID)
		if back.Status != statusRunning || back.Pod == m.Pod || back.Pod == "" {
			t.Fatalf("the lease was not given a new pod: %+v (was %s)", back, m.Pod)
		}
		if back.ExpiresAt != m.ExpiresAt || back.Payer != m.Payer {
			t.Fatalf("a restart is the same lease: expires %d->%d payer %q->%q", m.ExpiresAt, back.ExpiresAt, m.Payer, back.Payer)
		}
		if _, err := s.State.rt.pods().Get(ctx, m.Pod, metav1.GetOptions{}); err == nil {
			t.Fatalf("the evicted pod %s was left in the cluster", m.Pod)
		}
		r, err := Run(s, ctx, "acme", m.ID, Cmd{Command: "true"})
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("the run after the restart: %+v %v", r, err)
		}
	})
}

// A file write and read meet the same pod the same way. A sandbox with a disk of
// its own keeps its files across the restart, and the answer says so.
func TestARestartKeepsTheDisk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _ := lostService(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		if _, _, err := Write(s, ctx, "acme", m.ID, "notes.md", []byte("half done")); err != nil {
			t.Fatalf("write: %v", err)
		}
		evict(t, s, m)

		_, _, err = Write(s, ctx, "acme", m.ID, "more.md", []byte("x"))
		if httpStatus(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "disk") {
			t.Fatalf("a write on an evicted pod answered %d %v, want 503 naming the disk", httpStatus(err), err)
		}
		e, err := Read(s, ctx, "acme", m.ID, "notes.md")
		if err != nil || string(e.Data) != "half done" {
			t.Fatalf("the file on the sandbox's disk did not survive: %q %v", e.Data, err)
		}
	})
}

// A lease that names a sandbox whose pod is gone hands back one that answers —
// the same id with a new pod — rather than the row as it reads.
func TestAResumeOfALostPodAnswersALiveOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _ := lostService(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		if err := s.State.rt.pods().Delete(ctx, m.Pod, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("delete pod: %v", err)
		}

		back, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID, Class: "exec"})
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if back.ID != m.ID || back.Pod == m.Pod || back.Status != statusRunning {
			t.Fatalf("the resume answered the lost pod: %+v (was %s)", back, m.Pod)
		}
		if r, err := Run(s, ctx, "acme", m.ID, Cmd{Command: "true"}); err != nil || r.ExitCode != 0 {
			t.Fatalf("the run after the resume: %+v %v", r, err)
		}
	})
}

// An exec that races the eviction fails while the pod still reads Running; the
// call waits for the pod to read ended and answers as for any lost pod.
func TestAnExecThatRacesTheEvictionWaitsForIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, k := lostService(t)
		s.State.rt.settle = 2 * time.Second
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		k.down = errors.New(`failed to create exec: task 49248cb8 not found`)
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := evicted(s, m); err != nil {
				t.Errorf("evict: %v", err)
			}
		}()

		_, err = Run(s, ctx, "acme", m.ID, Cmd{Command: "true"})
		if httpStatus(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "Evicted") {
			t.Fatalf("an exec that raced the eviction answered %d %v, want 503 naming it", httpStatus(err), err)
		}
		k.down = nil
		if r, err := Run(s, ctx, "acme", m.ID, Cmd{Command: "true"}); err != nil || r.ExitCode != 0 {
			t.Fatalf("the run after the restart: %+v %v", r, err)
		}
	})
}

// Two calls that read the row before either replaced the lost pod start one new
// pod between them: the second finds the row already moved and starts nothing.
func TestOneRestartWins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _ := lostService(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		evict(t, s, m)
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		lost := errors.New(`unable to upgrade connection: container not found ("sandbox")`)

		if err := fault(s, ctx, st, m, "exec", lost); httpStatus(err) != http.StatusServiceUnavailable {
			t.Fatalf("first: %d %v", httpStatus(err), err)
		}
		first := row(t, s, "acme", m.ID)
		err = fault(s, ctx, st, m, "exec", lost) // the same stale read
		if httpStatus(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "in a moment") {
			t.Fatalf("second: %d %v, want 503 saying a new pod is on its way", httpStatus(err), err)
		}
		if back := row(t, s, "acme", m.ID); back.Pod != first.Pod || back.Status != statusRunning {
			t.Fatalf("the stale call moved the row: %+v, want pod %s", back, first.Pod)
		}
		pods, err := s.State.rt.pods().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list pods: %v", err)
		}
		if len(pods.Items) != 1 || pods.Items[0].GetName() != first.Pod {
			var names []string
			for _, p := range pods.Items {
				names = append(names, p.GetName())
			}
			t.Fatalf("pods %v, want only %s", names, first.Pod)
		}
	})
}

// A channel failure on a pod that is alive is still the gateway's, and it touches
// nothing: no pod is replaced for a call the cluster merely dropped.
func TestAChannelFailureOnALivePodIsStillABadGateway(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, k := lostService(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		k.down = errors.New("error dialing backend: dial tcp 10.0.0.1:10250: i/o timeout")

		if _, err := Run(s, ctx, "acme", m.ID, Cmd{Command: "true"}); httpStatus(err) != http.StatusBadGateway {
			t.Fatalf("a dropped channel answered %d %v, want 502", httpStatus(err), err)
		}
		if back := row(t, s, "acme", m.ID); back.Pod != m.Pod || back.Status != statusRunning {
			t.Fatalf("a live pod was replaced: %+v (was %s)", back, m.Pod)
		}
	})
}

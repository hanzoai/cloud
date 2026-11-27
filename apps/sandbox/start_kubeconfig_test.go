package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

// recorder is a streamer that answers every exec successfully and remembers what
// it was asked to run and what it was handed on stdin. It is the whole cluster
// these tests need: the question is not what the pod does with the kubeconfig, it
// is whether the write is ATTEMPTED, and with which bytes.
type recorder struct {
	argv  [][]string
	stdin [][]byte
}

func (s *recorder) stream(_ context.Context, _, _ string, argv []string, stdin io.Reader, _, _ io.Writer) error {
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	s.argv = append(s.argv, argv)
	s.stdin = append(s.stdin, in)
	return nil
}

func (s *recorder) tty(context.Context, string, string, []string, io.Reader, io.Writer, remotecommand.TerminalSizeQueue) error {
	return nil
}

// runningPod is a pod object the fake apiserver will hand back as Running, so
// waitRunning returns immediately and the test is about what happens AFTER it.
func runningPod(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": "hanzo-sandboxes"},
		"status":   map[string]any{"phase": "Running"},
	}}
}

// A SANDBOX THAT HAS COME UP MUST BE ABLE TO USE ITS OWN EXEC CHANNEL.
//
// The row says `pending` until the API layer flips it, and start() takes its
// Sandbox BY VALUE — so for the whole of start(), the copy in hand describes the
// sandbox as it was before the pod existed. exec refuses anything that is not
// running, so the kubeconfig write at the end of start() refused ITSELF, on every
// SuperAdmin lease, every time: `write kubeconfig: sandbox is pending`, answered
// to the caller as a 503.
//
// waitRunning had already proven the pod was up. The only thing that disagreed
// was a stale field, which is why this is a test about a value and not a cluster.
func TestStartWritesTheKubeconfigOnceThePodIsRunning(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pod := podName("m_admin")
		rec := &recorder{}
		r := fakeRuntime(runningPod(pod))
		r.str = rec
		r.startTimeout = 2 * time.Second
		r.execTimeout = 2 * time.Second

		m := Sandbox{ID: "m_admin", Org: "admin", Class: "admin", Pod: pod, Status: "pending"}
		if err := r.start(context.Background(), m, cred{kube: []byte("apiVersion: v1\nkind: Config\n")}); err != nil {
			t.Fatalf("start: %v", err)
		}

		var wrote bool
		for _, a := range rec.argv {
			if strings.Contains(strings.Join(a, " "), kubePath) {
				wrote = true
			}
		}
		if !wrote {
			t.Fatalf("start() never wrote the kubeconfig; exec saw %v", rec.argv)
		}
	})
}

// CONTROL: a lease with no kubeconfig must not touch the exec channel at all.
// Without this, the test above would still pass if start() started writing an
// empty file for everybody — which would put a credential path in every pod.
func TestStartWritesNothingWhenThereIsNoKubeconfig(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pod := podName("m_plain")
		rec := &recorder{}
		r := fakeRuntime(runningPod(pod))
		r.str = rec
		r.startTimeout = 2 * time.Second

		m := Sandbox{ID: "m_plain", Org: "acme", Class: "exec", Pod: pod, Status: "pending"}
		if err := r.start(context.Background(), m, cred{}); err != nil {
			t.Fatalf("start: %v", err)
		}
		if len(rec.argv) != 0 {
			t.Fatalf("a sandbox with no kubeconfig used the exec channel: %v", rec.argv)
		}
	})
}

// A SUPERADMIN'S DEV LEASE IS HANDED THE ORG'S FLEET. With no DigitalOcean token
// anywhere, the lease still reaches its pod with a kubeconfig built from the
// cluster the org attached: one context, under the cluster's fleet name, pointing
// where the attached kubeconfig points.
func TestAnAdminDevLeaseTakesItsKubeconfigFromTheFleet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		away(t)
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		t.Setenv("DO_API_TOKEN", "")
		s, err := New(cloud.Deps{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		rec := &recorder{}
		r := fakeRuntime()
		r.str = rec
		r.startTimeout, r.execTimeout = 2*time.Second, 2*time.Second
		r.attached = &fakeAttached{org: "admin", name: "lab", kube: []byte(`apiVersion: v1
kind: Config
clusters:
- name: default
  cluster: {server: "https://8.8.8.8:6443"}
contexts:
- name: default
  context: {cluster: default, user: default}
current-context: default
users:
- name: default
  user: {token: LABTOKEN}
`)}
		// Every pod the fake apiserver is asked to create comes up Running.
		r.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "pods",
			func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
				pod := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
				_ = unstructured.SetNestedField(pod.Object, "Running", "status", "phase")
				return false, nil, nil
			})
		s.State.rt = r

		if _, err := Lease(s, context.Background(), "admin", account.Org("admin"), true, "", Spec{Class: "dev", Project: "ops"}); err != nil {
			t.Fatalf("Lease: %v", err)
		}
		var kube []byte
		for i, a := range rec.argv {
			if strings.Contains(strings.Join(a, " "), kubePath) {
				kube = rec.stdin[i]
			}
		}
		cfg, err := clientcmd.Load(kube)
		if err != nil || len(cfg.Contexts) != 1 {
			t.Fatalf("the pod was not handed a one-cluster kubeconfig (%v): %q", err, kube)
		}
		lab, user := cfg.Clusters["lab"], cfg.AuthInfos["lab"]
		if cfg.CurrentContext != "lab" || lab == nil || lab.Server != "https://8.8.8.8:6443" || user == nil || user.Token != "LABTOKEN" {
			t.Fatalf("the kubeconfig is not the fleet's lab cluster:\n%s", kube)
		}
	})
}

// adminLease leases a SuperAdmin's dev sandbox against the fleet given, on a fake
// cluster whose pods come up Running, and reports what the exec channel carried.
func adminLease(t *testing.T, fleet attached) (*recorder, error) {
	t.Helper()
	away(t)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	s, err := New(cloud.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := &recorder{}
	r := fakeRuntime()
	r.str = rec
	r.startTimeout, r.execTimeout = 2*time.Second, 2*time.Second
	r.attached = fleet
	r.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "pods",
		func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
			pod := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
			_ = unstructured.SetNestedField(pod.Object, "Running", "status", "phase")
			return false, nil, nil
		})
	s.State.rt = r
	_, err = Lease(s, context.Background(), "admin", account.Org("admin"), true, "", Spec{Class: "dev", Project: "ops"})
	return rec, err
}

func wroteKube(rec *recorder) bool {
	for _, a := range rec.argv {
		if strings.Contains(strings.Join(a, " "), kubePath) {
			return true
		}
	}
	return false
}

// AN ORG WITH NOTHING ATTACHED STILL GETS ITS OPERATOR A SHELL. Zero clusters is
// an org with no fleet, not a failure, and the lease holds no kubeconfig.
func TestAnAdminLeaseWithNoFleetIsAShell(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, err := adminLease(t, &fakeAttached{org: "admin"})
		if err != nil {
			t.Fatalf("a SuperAdmin with no attached cluster was refused a dev sandbox: %v", err)
		}
		if wroteKube(rec) {
			t.Fatalf("a lease with no fleet wrote a kubeconfig: %v", rec.argv)
		}
	})
}

// AN UNREADABLE FLEET IS LOUD AND COSTS NOTHING ELSE. A cluster is attached but
// its kubeconfig cannot be had: the lease still starts, holding no credential.
func TestAnAdminLeaseWithAnUnreadableFleetIsAShell(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, err := adminLease(t, &fakeAttached{org: "admin", name: "lab"})
		if err != nil {
			t.Fatalf("an unreadable fleet refused the operator's shell: %v", err)
		}
		if wroteKube(rec) {
			t.Fatalf("an unreadable fleet wrote a kubeconfig: %v", rec.argv)
		}
	})
}

var _ = k8s.Pods

// pullingPod is a pod on a node whose container kubelet is still creating, which
// is what it reports for the length of an image pull.
func pullingPod(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": "hanzo-sandboxes"},
		"spec":     map[string]any{"nodeName": "visor-1"},
		"status": map[string]any{"phase": "Pending", "containerStatuses": []any{
			map[string]any{"name": "sandbox", "state": map[string]any{"waiting": map[string]any{"reason": "ContainerCreating"}}},
		}},
	}}
}

// A PULL IS NOT A START. The first lease on a node pulled the 1.9 GB image for
// 4m59s and was refused at the 5-minute start budget with the image one second
// from landing. A pod whose container is being created waits the pull budget;
// the start budget still bounds a pod that is not pulling.
func TestAPullInProgressWaitsThePullBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pod := podName("m_pull")
		r := fakeRuntime(pullingPod(pod))
		r.startTimeout, r.pullTimeout = 200*time.Millisecond, 5*time.Second
		go func() {
			time.Sleep(600 * time.Millisecond)
			_, _ = r.pods().Update(context.Background(), runningPod(pod), metav1.UpdateOptions{})
		}()
		if err := r.waitRunning(context.Background(), Sandbox{ID: "m_pull", Pod: pod}); err != nil {
			t.Fatalf("a pod that finished pulling after the start budget was refused: %v", err)
		}
	})
}

// CONTROL: a pod that is not pulling — here, one with no node — still fails at
// the start budget, so a cluster that cannot place a sandbox answers in
// minutes, not in the pull budget.
func TestAPodNotPullingStillFailsAtTheStartBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pod := podName("m_stuck")
		stuck := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": pod, "namespace": "hanzo-sandboxes"},
			"status":   map[string]any{"phase": "Pending", "message": "0/9 nodes are available"},
		}}
		r := fakeRuntime(stuck)
		r.startTimeout, r.pullTimeout = 200*time.Millisecond, time.Minute
		began := time.Now()
		err := r.waitRunning(context.Background(), Sandbox{ID: "m_stuck", Pod: pod})
		if err == nil || !strings.Contains(err.Error(), "not running after 200ms") {
			t.Fatalf("want the start budget's refusal, got %v", err)
		}
		if time.Since(began) > 5*time.Second {
			t.Fatalf("an unplaced pod waited %v, the pull budget", time.Since(began))
		}
	})
}

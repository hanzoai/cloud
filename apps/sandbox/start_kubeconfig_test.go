package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/k8s"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/remotecommand"
)

// recorder is a streamer that answers every exec successfully and remembers what
// it was asked to run. It is the whole cluster this test needs: the question is
// not what the pod does with the kubeconfig, it is whether the write is ATTEMPTED.
type recorder struct{ argv [][]string }

func (s *recorder) stream(_ context.Context, _, _ string, argv []string, stdin io.Reader, _, _ io.Writer) error {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	s.argv = append(s.argv, argv)
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
}

// CONTROL: a lease with no kubeconfig must not touch the exec channel at all.
// Without this, the test above would still pass if start() started writing an
// empty file for everybody — which would put a credential path in every pod.
func TestStartWritesNothingWhenThereIsNoKubeconfig(t *testing.T) {
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
}

var _ = k8s.Pods

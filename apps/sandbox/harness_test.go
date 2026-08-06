package sandbox

// The harness every test in this package shares: a real router, a real per-org
// store on a temp dir, a FAKE cluster and a FAKE box.
//
// It builds the Service the same way Mount does and calls the same routes()
// — so the routing table under test is the production one — and substitutes
// only the two things that would otherwise leave the process: the Kubernetes
// clientset and the HTTP call to a box. Nothing here is a second router or a
// second handler set.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	fake "k8s.io/client-go/kubernetes/fake"
)

// fakeCluster is a clientset whose kubelet is a reactor: a Pod that is created
// comes back Running with an address, which is what the real one eventually
// does and what waitReady is written against.
func fakeCluster(objs ...runtime.Object) *fake.Clientset {
	cs := fake.NewSimpleClientset(objs...)
	var mu sync.Mutex
	n := 0
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		mu.Lock()
		n++
		pod.Status.PodIP = "10.0.0." + itoa(n)
		mu.Unlock()
		pod.Status.Phase = corev1.PodRunning
		return false, pod, nil // false: let the tracker store the mutated object
	})
	return cs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// warmPod is one member of the warm pool as the Deployment would have left it.
func warmPod(name, class, ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "hanzo-boxes",
			Labels:    map[string]string{labClass: class, labState: stateWarm, "app": execAppLabel},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
	}
}

// recorder is the fake box: it remembers what the proxy sent and answers with
// whatever the test told it to.
type recorder struct {
	mu       sync.Mutex
	calls    []call
	status   int
	body     string
	ctype    string
	err      error
	blockCtx bool
}

type call struct {
	Box                              Box
	Method, Path, Query, Body, CType string
}

func (r *recorder) Do(ctx context.Context, b Box, method, path, q string, body io.Reader, ct string) (*http.Response, error) {
	raw := ""
	if body != nil {
		v, _ := io.ReadAll(body)
		raw = string(v)
	}
	r.mu.Lock()
	r.calls = append(r.calls, call{Box: b, Method: method, Path: path, Query: q, Body: raw, CType: ct})
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	st, bd, ctype := r.status, r.body, r.ctype
	if st == 0 {
		st = http.StatusOK
	}
	if ctype == "" {
		ctype = "application/json"
	}
	return &http.Response{
		StatusCode: st,
		Header:     http.Header{"Content-Type": []string{ctype}},
		Body:       io.NopCloser(strings.NewReader(bd)),
	}, nil
}

func (r *recorder) last() call {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return call{}
	}
	return r.calls[len(r.calls)-1]
}

type rig struct {
	app *zip.App
	svc *cloud.Service[state]
	cs  *fake.Clientset
	box *recorder
}

func newRig(t *testing.T, objs ...runtime.Object) *rig {
	t.Helper()
	cs := fakeCluster(objs...)
	rec := &recorder{}
	log := luxlog.New("test")
	deps := cloud.Deps{Logger: log, DataDir: t.TempDir()}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "sandbox"),
		State: state{
			stores: cloud.NewOrgStore(cloud.NewBase(deps, "sandbox"), "sandbox", openStore),
			pool: &pool{
				cs: cs, ns: "hanzo-boxes", storage: "do-block-storage", volSize: "20Gi",
				images:    map[string]string{classExec: "img:exec", classDev: "img:dev", classDesktop: "img:desktop"},
				nodeTaint: "hanzo.ai/box",
				readyWait: 2 * time.Second, readyPoll: time.Millisecond,
			},
			boxes: rec,
			now:   time.Now,
		},
	}
	app := zip.New(zip.Config{Logger: log})
	routes(app, s)
	return &rig{app: app, svc: s, cs: cs, box: rec}
}

// do fires an in-process request. org is stamped as X-Org-Id AND X-User-Id —
// the pair the gateway's SanitizeIdentity mints from a validated principal.
// Sending X-Org-Id alone is the FORGE, and tests below send exactly that.
func (r *rig) do(t *testing.T, method, path, org string, body any) (int, []byte) {
	t.Helper()
	if org == "" {
		return r.raw(t, method, path, nil, body)
	}
	return r.raw(t, method, path, map[string]string{"X-Org-Id": org, "X-User-Id": "u_" + org}, body)
}

// forge fires a request carrying an org claim with NO validated user behind it.
func (r *rig) forge(t *testing.T, method, path, org string, body any) (int, []byte) {
	t.Helper()
	return r.raw(t, method, path, map[string]string{"X-Org-Id": org}, body)
}

func (r *rig) raw(t *testing.T, method, path string, hdr map[string]string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// mkBox creates a box over HTTP and returns it, failing the test if it did not.
func (r *rig) mkBox(t *testing.T, org, project, class string) Box {
	t.Helper()
	code, body := r.do(t, http.MethodPost, "/v1/sandbox/boxes", org,
		map[string]any{"project": project, "class": class})
	if code != http.StatusCreated {
		t.Fatalf("create box: want 201, got %d: %s", code, body)
	}
	var b Box
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("create box json: %v (%s)", err, body)
	}
	return b
}

func (r *rig) store(t *testing.T, org string) *Store {
	t.Helper()
	st, err := storeFor(r.svc, org)
	if err != nil {
		t.Fatalf("storeFor(%s): %v", org, err)
	}
	return st
}

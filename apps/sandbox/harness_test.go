package sandbox

// The harness every test in this package shares: the REAL router, the REAL
// per-org stores on a temp dir, a FAKE cluster and a FAKE box.
//
// It builds the Service the way Mount does and registers the same routes(), so
// the routing table under test is the production one. Exactly two things are
// substituted — the two that would otherwise leave the process:
//
//	the pool's dynamic Kubernetes client  -> client-go's fake dynamic client
//	the proxy's HTTP transport            -> boxRecorder, in-process
//
// Nothing here is a second router or a second handler set. If a route moves,
// these tests move with it or fail; they never quietly test a copy.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/k8s"

	// devmaster keys this test binary: cek opens nothing without a master, and
	// every box row lives in an encrypted per-org file.
	_ "github.com/hanzoai/cloud/internal/devmaster"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const (
	testNS  = "hanzo-boxes"
	testKey = "test-service-key" // stands in for the KMS-sourced CODE_EXEC_API_KEY

	// warmLabel is the value of labState a claimable pod carries. It is spelled
	// as a literal here because it is spelled as a literal THERE: pool.claim
	// builds "…,box-state=warm" with fmt.Sprintf and patches "bound" inline, so
	// the package names neither value. The warm-pool Deployment and claim()
	// agree by convention, not by a shared constant.
	warmLabel = "warm"
)

// listAll is the unfiltered list — what an operator's `kubectl get` would see,
// as opposed to the label-scoped list claim() and release() issue.
var listAll = metav1.ListOptions{}

// warmPod is one member of the warm pool as the Deployment would have left it:
// running, networked, labelled with its class and free to claim.
func warmPod(name, class, ip string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": testNS,
			"labels":    map[string]any{labClass: class, labState: warmLabel},
		},
		"status": map[string]any{"phase": "Running", "podIP": ip},
	}}
}

// warmPool is a pool with room for every class, which is the state a test that
// is not ABOUT scheduling wants: a claim succeeds and the test gets on with it.
func warmPool() []runtime.Object {
	var objs []runtime.Object
	n := 0
	for _, class := range []string{"exec", "dev", "desktop"} {
		for i := 0; i < 4; i++ {
			n++
			objs = append(objs, warmPod(class+"-"+strconv.Itoa(i), class, "10.0.0."+strconv.Itoa(n)))
		}
	}
	return objs
}

// ── the fake box ─────────────────────────────────────────────────────────────

// boxRecorder is the box: it remembers exactly what the proxy put on the wire
// and answers with whatever the test told it to. It is a RoundTripper rather
// than an interface the Service holds, because the proxy dials through a
// package-level http.Client — so this substitutes the transport and leaves
// forward() completely untouched, including its header and query handling.
type boxRecorder struct {
	mu     sync.Mutex
	calls  []boxCall
	status int
	body   string
	ctype  string
	err    error
}

type boxCall struct {
	Method, URL, Path, Query, Body string
	Header                         http.Header
}

func (r *boxRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	raw := ""
	if req.Body != nil {
		v, _ := io.ReadAll(req.Body)
		raw = string(v)
	}
	r.mu.Lock()
	r.calls = append(r.calls, boxCall{
		Method: req.Method, URL: req.URL.String(), Path: req.URL.Path,
		Query: req.URL.RawQuery, Body: raw, Header: req.Header.Clone(),
	})
	st, bd, ct, err := r.status, r.body, r.ctype, r.err
	r.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if st == 0 {
		st = http.StatusOK
	}
	if ct == "" {
		ct = "application/json"
	}
	return &http.Response{
		StatusCode:    st,
		Header:        http.Header{"Content-Type": []string{ct}},
		Body:          io.NopCloser(strings.NewReader(bd)),
		ContentLength: int64(len(bd)),
		Request:       req,
	}, nil
}

func (r *boxRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *boxRecorder) last() boxCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return boxCall{}
	}
	return r.calls[len(r.calls)-1]
}

// ── the rig ──────────────────────────────────────────────────────────────────

type rig struct {
	app *zip.App
	svc *cloud.Service[state]
	dyn *dynamicfake.FakeDynamicClient
	box *boxRecorder
}

// newRig builds the subsystem exactly as Mount does — same Base, same OrgStore,
// same routes — over a fake cluster. Passing no objects seeds a full warm pool;
// pass objects to control what is claimable.
func newRig(t *testing.T, objs ...runtime.Object) *rig {
	t.Helper()
	if len(objs) == 0 {
		objs = warmPool()
	}
	// An explicit gvr->listKind map, not a scheme: the pool only ever touches
	// these two resources, and naming them keeps the fake from panicking on a
	// List when a test seeds nothing of that kind.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			k8s.Pods:    "PodList",
			k8s.Volumes: "PersistentVolumeClaimList",
		}, objs...)

	rec := &boxRecorder{}
	prev := boxClient
	boxClient = &http.Client{Transport: rec}
	t.Cleanup(func() { boxClient = prev })

	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	b := cloud.NewBase(deps, "sandbox")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "sandbox", openStore),
		pool: &pool{
			ns:    testNS,
			image: "registry.hanzo.ai/hanzoai/box",
			port:  "8000",
			dyn:   dyn,
		},
		key: testKey,
	}}
	t.Cleanup(func() { _ = s.State.stores.CloseAll() })

	app := zip.New(zip.Config{Logger: deps.Logger})
	routes(app, s)
	return &rig{app: app, svc: s, dyn: dyn, box: rec}
}

// do fires an in-process request. org is stamped as X-Org-Id AND X-User-Id —
// the pair SanitizeIdentity mints from a validated principal. Sending X-Org-Id
// alone is the FORGE, and forge() below sends exactly that.
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

// store reaches the org's physical store through the package's own one door.
func (r *rig) store(t *testing.T, org string) *Store {
	t.Helper()
	st, err := storeFor(r.svc, org)
	if err != nil {
		t.Fatalf("storeFor(%s): %v", org, err)
	}
	return st
}

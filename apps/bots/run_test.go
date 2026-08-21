package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The control plane against a fake runtime. The fake is GENUINELY org-scoped (a
// run lives under exactly one org key), so a test that reads another tenant's run
// has to get past the same boundary the real runtime enforces by tenant path — a
// handler that forgot to pass the validated org, or passed a client-supplied one,
// fails here.

type runKey struct{ org, id string }

type listCall struct{ org string }

type fakeRuntime struct {
	mu    sync.Mutex
	rows  map[runKey]Run
	lists []listCall
	stops []runKey
	// Injected outcomes for the honest-failure paths.
	listErr, stopErr error
}

func newFake() *fakeRuntime { return &fakeRuntime{rows: map[runKey]Run{}} }

func (f *fakeRuntime) seed(org string, r Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[runKey{org, r.ID}] = r
}

func (f *fakeRuntime) List(_ context.Context, org string) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists = append(f.lists, listCall{org})
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []Run
	for k, r := range f.rows {
		if k.org == org {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRuntime) Stop(_ context.Context, org, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, runKey{org, runID})
	if f.stopErr != nil {
		return f.stopErr
	}
	k := runKey{org, runID}
	if _, ok := f.rows[k]; !ok {
		// The real runtime resolves under tenants/{org}/ and ANSWERS absent.
		return ErrNotFound
	}
	delete(f.rows, k)
	return nil
}

func (f *fakeRuntime) stopCalls() []runKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runKey(nil), f.stops...)
}

func (f *fakeRuntime) listCalls() []listCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]listCall(nil), f.lists...)
}

func (f *fakeRuntime) has(org, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[runKey{org, id}]
	return ok
}

// mountWith builds the run plane over an injected runtime, exactly as Mount does
// over the real one — mountRuns is the shared registration path, so what a test
// drives is the code that ships. The relay rides along because it is the same
// capability's ops face and the gate must read the WHOLE surface.
func mountWith(t *testing.T, rt Runtime) *zip.App {
	t.Helper()
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	compose(app)
	runsOn(t, app, rt)
	return app
}

// runsOn registers the run plane and the relay on an app the caller composed. It
// exists so mountAll can put all three families on ONE router without a second
// copy of this construction.
func runsOn(t *testing.T, app *zip.App, rt Runtime) {
	t.Helper()
	s := &cloud.Service[executor]{
		Base:  cloud.NewBase(cloud.Deps{}, "bot"),
		State: executor{gateway: gatewayBase(), runtime: rt},
	}
	mountRuns(app, s)
	if err := mountRelay(app, cloud.Deps{}); err != nil {
		t.Fatalf("mountRelay: %v", err)
	}
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (mountRuns says why): the program's composer installs it once at the root,
// after the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing — and a test that skips it does not
// test a stricter program, it tests one where every org-scoped op answers 403 for
// a reason that would never exist in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountAll builds the WHOLE capability on one router — the run plane and the
// relay — which is what a projection gate has to read. A gate that saw one of the
// two could not see a route that landed in the gap between them.
//
// It is what Mount does, minus the deps a test cannot supply, which is why the
// gate reads this and not a reconstruction: the routes are registered by the same
// two functions the binary calls.
func mountAll(t *testing.T) *zip.App {
	t.Helper()
	return mountWith(t, newFake())
}

// call sends a request with the gateway-minted identity headers. A non-empty org
// also sets X-User-Id (the validated principal); an empty org sends neither.
func call(t *testing.T, app *zip.App, method, path, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func listRunIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var v BotRuns
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(v.Bots))
	for _, b := range v.Bots {
		ids = append(ids, b.RunID)
	}
	return ids
}

// ---- run ----

// Launching is not implemented and must say so rather than charge for a bot that
// never boots. The runtime has no launch operation, so a 200 here would be a lie
// with a price on it.
func TestRunIsNotImplementedAndStartsNothing(t *testing.T) {
	rt := newFake()
	app := mountWith(t, rt)

	if code, _ := call(t, app, http.MethodPost, "/v1/bots/runs", "acme"); code != http.StatusNotImplemented {
		t.Fatalf("launch want 501, got %d", code)
	}
	// Nothing was started, so nothing may be listed as started.
	_, body := call(t, app, http.MethodGet, "/v1/bots/runs", "acme")
	if ids := listRunIDs(t, body); len(ids) != 0 {
		t.Fatalf("a refused launch must not produce a run, got %v", ids)
	}
}

// ---- list ----

// A list returns the CALLER's runs and only those, scoped by the validated org the
// runtime is asked with — never a client-supplied one.
func TestListIsScopedToTheCallerOrg(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_acme", Task: "acme work", Surface: "desktop", Status: "running", StartedAt: "2023-11-14T22:13:20Z"})
	rt.seed("globex", Run{ID: "run_globex", Task: "globex secret", Surface: "terminal", Status: "running", StartedAt: "2023-11-14T22:13:20Z"})
	app := mountWith(t, rt)

	code, body := call(t, app, http.MethodGet, "/v1/bots/runs", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if ids := listRunIDs(t, body); len(ids) != 1 || ids[0] != "run_acme" {
		t.Fatalf("acme must see exactly its own run, got %v (%s)", ids, body)
	}
	// The runtime was asked with the caller's validated org, nothing else.
	if got := rt.listCalls(); len(got) != 1 || got[0].org != "acme" {
		t.Fatalf("runtime must be asked with the validated org only, got %+v", got)
	}
}

// The row carries the run's real attributes as the runtime reported them, plus a
// sessionUrl derived here from the runtime's own id.
func TestListRowShape(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_1", Task: "ship it", Surface: "terminal", Status: "running", StartedAt: "2023-11-14T22:13:20Z"})
	app := mountWith(t, rt)

	_, body := call(t, app, http.MethodGet, "/v1/bots/runs", "acme")
	var v BotRuns
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(v.Bots) != 1 {
		t.Fatalf("want 1 row, got %d", len(v.Bots))
	}
	want := BotRun{
		RunID: "run_1", Task: "ship it", Surface: "terminal", Status: "running",
		SessionURL: "https://bot.example.test/vnc?nodeId=run_1",
		StartedAt:  "2023-11-14T22:13:20Z",
	}
	if v.Bots[0] != want {
		t.Fatalf("row\n got %+v\nwant %+v", v.Bots[0], want)
	}
}

// No tenant, no read — and the runtime is never even asked.
func TestListFailsClosedWithoutTenant(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_acme", Status: "running"})
	app := mountWith(t, rt)

	if code, _ := call(t, app, http.MethodGet, "/v1/bots/runs", ""); code != http.StatusForbidden {
		t.Fatalf("no-tenant list want 403, got %d", code)
	}
	if got := rt.listCalls(); len(got) != 0 {
		t.Fatalf("runtime asked without a tenant: %+v", got)
	}
}

// A forged X-Org-Id with no validated principal (the direct-to-pod path) must not
// enumerate the victim tenant: principal.Org requires a validated X-User-Id.
func TestListRefusesForgedOrgWithoutValidatedPrincipal(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_acme", Task: "secret", Status: "running"})
	app := mountWith(t, rt)

	req := httptest.NewRequest(http.MethodGet, "/v1/bots/runs", nil)
	req.Header.Set("X-Org-Id", "acme") // forged: no X-User-Id
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged-org list want 403, got %d", resp.StatusCode)
	}
	if got := rt.listCalls(); len(got) != 0 {
		t.Fatalf("runtime asked on a forged org: %+v", got)
	}
}

// A runtime that cannot answer is an error, never an empty list: [] would claim
// "your org has no runs", which is not what we learned.
func TestListReportsRuntimeFailureInsteadOfEmpty(t *testing.T) {
	rt := newFake()
	rt.listErr = fmt.Errorf("runtime is down")
	app := mountWith(t, rt)

	code, body := call(t, app, http.MethodGet, "/v1/bots/runs", "acme")
	if code != http.StatusBadGateway {
		t.Fatalf("runtime failure want 502, got %d (%s)", code, body)
	}
}

// ---- stop ----

// One tenant may not stop another's run. The id is real and the caller is
// validated — and it is still a 404, because the runtime is asked under the
// CALLER's org, where that run does not exist.
func TestStopCannotReachAnotherOrgsRun(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_victim", Task: "acme work", Status: "running"})
	app := mountWith(t, rt)

	code, body := call(t, app, http.MethodPost, "/v1/bots/runs/run_victim/stop", "globex")
	if code != http.StatusNotFound {
		t.Fatalf("cross-org stop want 404, got %d (%s)", code, body)
	}
	// The runtime was asked under globex — never under the victim's org.
	for _, s := range rt.stopCalls() {
		if s.org != "globex" {
			t.Fatalf("stop escaped the caller's org: %+v", s)
		}
	}
	if !rt.has("acme", "run_victim") {
		t.Fatal("victim run must survive a foreign stop")
	}
}

// An unknown id and another tenant's id are indistinguishable: both 404, so the
// endpoint is not an oracle for which run ids exist.
func TestStopUnknownRunIsNotFound(t *testing.T) {
	app := mountWith(t, newFake())
	if code, _ := call(t, app, http.MethodPost, "/v1/bots/runs/run_nope/stop", "acme"); code != http.StatusNotFound {
		t.Fatalf("unknown stop want 404, got %d", code)
	}
}

// The happy path: the runtime is driven with the caller's own org and run id, and
// the run is gone from its list.
func TestStopHaltsTheRun(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_1", Task: "work", Status: "running"})
	app := mountWith(t, rt)

	code, body := call(t, app, http.MethodPost, "/v1/bots/runs/run_1/stop", "acme")
	if code != http.StatusOK {
		t.Fatalf("stop want 200, got %d (%s)", code, body)
	}
	var v BotStopped
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.RunID != "run_1" || v.Status != statusStopped {
		t.Fatalf("stop view %+v", v)
	}
	if got := rt.stopCalls(); len(got) != 1 || got[0] != (runKey{"acme", "run_1"}) {
		t.Fatalf("runtime must be driven once with the caller's org+run, got %v", got)
	}
	if _, body := call(t, app, http.MethodGet, "/v1/bots/runs", "acme"); len(listRunIDs(t, body)) != 0 {
		t.Fatal("a stopped run must leave the list")
	}
}

// THE correctness lie this endpoint must never tell: a runtime that does not serve
// stop reports nothing about the run, so claiming "stopped" would make a stop that
// cannot fail. It is a 502, and it says the run was NOT stopped.
func TestStopFailsClosedWhenTheRuntimeDoesNotServeStop(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_1", Status: "running"})
	rt.stopErr = ErrNotServed
	app := mountWith(t, rt)

	code, body := call(t, app, http.MethodPost, "/v1/bots/runs/run_1/stop", "acme")
	if code != http.StatusBadGateway {
		t.Fatalf("unserved stop want 502, got %d (%s)", code, body)
	}
	if !rt.has("acme", "run_1") {
		t.Fatal("the run must not be treated as gone when the runtime never answered")
	}
}

// A stop that could not reach the executor must not claim the run was stopped.
func TestStopWithUnreachableRuntimeIs502(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_1", Status: "running"})
	rt.stopErr = fmt.Errorf("connection refused")
	app := mountWith(t, rt)

	if code, _ := call(t, app, http.MethodPost, "/v1/bots/runs/run_1/stop", "acme"); code != http.StatusBadGateway {
		t.Fatalf("unreachable runtime want 502, got %d", code)
	}
	if !rt.has("acme", "run_1") {
		t.Fatal("the run must stay live when the halt failed")
	}
}

// No tenant, no stop — and the runtime is never driven.
func TestStopFailsClosedWithoutTenant(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "run_1", Status: "running"})
	app := mountWith(t, rt)

	if code, _ := call(t, app, http.MethodPost, "/v1/bots/runs/run_1/stop", ""); code != http.StatusForbidden {
		t.Fatalf("no-tenant stop want 403, got %d", code)
	}
	if len(rt.stopCalls()) != 0 {
		t.Fatalf("runtime driven without a tenant: %v", rt.stopCalls())
	}
	if !rt.has("acme", "run_1") {
		t.Fatal("run stopped without a tenant")
	}
}

// An oversize id is a miss like any other — it never reaches the runtime.
func TestStopOversizeRunIDIsNotFound(t *testing.T) {
	rt := newFake()
	app := mountWith(t, rt)

	long := make([]byte, maxRunID+1)
	for i := range long {
		long[i] = 'a'
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/bots/runs/"+string(long)+"/stop", "acme"); code != http.StatusNotFound {
		t.Fatalf("oversize runId want 404, got %d", code)
	}
	if len(rt.stopCalls()) != 0 {
		t.Fatalf("runtime driven for an oversize id: %v", rt.stopCalls())
	}
}

// /v1/bots/runs is the launch literal, never a run id: the router resolves the
// static segment over the :runId param regardless of registration order.
func TestRunLiteralDoesNotBindAsARunID(t *testing.T) {
	rt := newFake()
	app := mountWith(t, rt)

	// POST /v1/bots/runs reaches the launch handler (501), NOT the stop handler
	// (which would 404 a run named "run" and would have driven the runtime).
	if code, _ := call(t, app, http.MethodPost, "/v1/bots/runs", "acme"); code != http.StatusNotImplemented {
		t.Fatalf("POST /v1/bots/runs must hit launch (501), got %d", code)
	}
	if len(rt.stopCalls()) != 0 {
		t.Fatalf("/v1/bots/runs bound as a run id and drove a stop: %v", rt.stopCalls())
	}
}

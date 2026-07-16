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

// The list/stop half of the control plane, against fake seams. The fakes are
// GENUINELY org-scoped (a run lives under exactly one org key), so a test that
// reads another tenant's run has to get past the same boundary the real registry
// enforces — a handler that forgot to pass the validated org, or passed a
// client-supplied one, fails here rather than silently passing.

type runKey struct{ org, id string }

type openCall struct{ org, actor, task, surface string }

type fakeRuns struct {
	mu    sync.Mutex
	rows  map[runKey]Run
	opens []openCall
	n     int
	// Injected failures for the honest-error paths.
	openErr, listErr, getErr, stopErr error
}

func newFakeRuns() *fakeRuns { return &fakeRuns{rows: map[runKey]Run{}} }

// seed places a run in ONE org's registry.
func (f *fakeRuns) seed(org string, r Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[runKey{org, r.ID}] = r
}

func (f *fakeRuns) Open(_ context.Context, org, actor, task, surface string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return "", f.openErr
	}
	f.opens = append(f.opens, openCall{org, actor, task, surface})
	f.n++
	id := fmt.Sprintf("sess_%d", f.n)
	f.rows[runKey{org, id}] = Run{ID: id, Task: task, Surface: surface, Status: "running", StartedAt: 1700000000}
	return id, nil
}

func (f *fakeRuns) List(_ context.Context, org string) ([]Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []Run
	for k, r := range f.rows {
		if k.org == org && r.Status == "running" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRuns) Get(_ context.Context, org, runID string) (Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return Run{}, false, f.getErr
	}
	r, ok := f.rows[runKey{org, runID}]
	return r, ok, nil
}

func (f *fakeRuns) Stop(_ context.Context, org, runID, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopErr != nil {
		return false, f.stopErr
	}
	k := runKey{org, runID}
	r, ok := f.rows[k]
	if !ok {
		return false, nil
	}
	r.Status = "done"
	f.rows[k] = r
	return true, nil
}

func (f *fakeRuns) statusOf(org, id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[runKey{org, id}].Status
}

type stopCall struct{ org, runID string }

type fakeRuntime struct {
	mu    sync.Mutex
	stops []stopCall
	err   error
}

func (f *fakeRuntime) Stop(_ context.Context, org, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, stopCall{org, runID})
	return f.err
}

func (f *fakeRuntime) calls() []stopCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stopCall(nil), f.stops...)
}

// mountWith builds the surface over injected seams, exactly as Mount does over
// the real ones — routes() is the shared registration path, so what a test drives
// is the code that ships.
func mountWith(t *testing.T, runs Runs, rt Runtime) *zip.App {
	t.Helper()
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	deps := cloud.Deps{Logger: luxlog.New("test")}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "bots"),
		State: state{bill: cloud.NewResourceMeter(deps, meterKind), gateway: gatewayBase(), runs: runs, runtime: rt},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return app
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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func listRunIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var v botsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(v.Bots))
	for _, b := range v.Bots {
		ids = append(ids, b.RunID)
	}
	return ids
}

// ---- list ----

// A list returns the CALLER's runs and only those: the other tenant's live run
// is in the same registry under its own org and must never appear.
func TestListIsScopedToTheCallerOrg(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_acme", Task: "acme work", Surface: "desktop", Status: "running", StartedAt: 1700000000})
	runs.seed("globex", Run{ID: "sess_globex", Task: "globex secret", Surface: "terminal", Status: "running", StartedAt: 1700000000})
	app := mountWith(t, runs, &fakeRuntime{})

	code, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	ids := listRunIDs(t, body)
	if len(ids) != 1 || ids[0] != "sess_acme" {
		t.Fatalf("acme must see exactly its own run, got %v (%s)", ids, body)
	}
}

// The list row carries the run's real attributes and a sessionUrl derived from
// its id — the surface is the one recorded, never a fabricated default.
func TestListRowShape(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_1", Task: "ship it", Surface: "terminal", Status: "running", StartedAt: 1700000000})
	app := mountWith(t, runs, &fakeRuntime{})

	_, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	var v botsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(v.Bots) != 1 {
		t.Fatalf("want 1 row, got %d", len(v.Bots))
	}
	got := v.Bots[0]
	want := botView{
		RunID: "sess_1", Task: "ship it", Surface: "terminal", Status: "running",
		SessionURL: "https://bot.example.test/vnc?nodeId=sess_1",
		StartedAt:  "2023-11-14T22:13:20Z",
	}
	if got != want {
		t.Fatalf("row\n got %+v\nwant %+v", got, want)
	}
}

// No tenant, no read — and the registry is never even consulted.
func TestListFailsClosedWithoutTenant(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_acme", Status: "running"})
	app := mountWith(t, runs, &fakeRuntime{})

	if code, _ := call(t, app, http.MethodGet, "/v1/bots", ""); code != http.StatusForbidden {
		t.Fatalf("no-tenant list want 403, got %d", code)
	}
}

// A forged X-Org-Id with no validated principal (the direct-to-pod path) must not
// enumerate the victim tenant: principal.Org requires a validated X-User-Id.
func TestListRefusesForgedOrgWithoutValidatedPrincipal(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_acme", Task: "secret", Status: "running"})
	app := mountWith(t, runs, &fakeRuntime{})

	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.Header.Set("X-Org-Id", "acme") // forged: no X-User-Id
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged-org list want 403, got %d (%s)", resp.StatusCode, b)
	}
}

// The registry is ours and authoritative, so a read failure is an error — never
// an empty list, which the caller would read as "my org has no runs".
func TestListReportsRegistryFailureInsteadOfEmpty(t *testing.T) {
	runs := newFakeRuns()
	runs.listErr = fmt.Errorf("store is down")
	app := mountWith(t, runs, &fakeRuntime{})

	code, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	if code != http.StatusInternalServerError {
		t.Fatalf("registry failure want 500, got %d (%s)", code, body)
	}
}

// ---- stop ----

// THE crown jewel: one tenant may not stop another's run. The run exists, the id
// is correct, the caller is validated — and it is still a 404, because it is not
// THEIRS. The runtime must never be driven for a run the caller does not own.
func TestStopCannotReachAnotherOrgsRun(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_victim", Task: "acme work", Status: "running"})
	rt := &fakeRuntime{}
	app := mountWith(t, runs, rt)

	code, body := call(t, app, http.MethodPost, "/v1/bots/sess_victim/stop", "globex")
	if code != http.StatusNotFound {
		t.Fatalf("cross-org stop want 404, got %d (%s)", code, body)
	}
	if len(rt.calls()) != 0 {
		t.Fatalf("runtime must not be driven for a run the caller does not own, got %v", rt.calls())
	}
	if got := runs.statusOf("acme", "sess_victim"); got != "running" {
		t.Fatalf("victim run must stay running, got %q", got)
	}
}

// An unknown id and another tenant's id are indistinguishable: both 404, so the
// endpoint is not an oracle for which run ids exist.
func TestStopUnknownRunIsNotFound(t *testing.T) {
	app := mountWith(t, newFakeRuns(), &fakeRuntime{})
	if code, _ := call(t, app, http.MethodPost, "/v1/bots/sess_nope/stop", "acme"); code != http.StatusNotFound {
		t.Fatalf("unknown stop want 404, got %d", code)
	}
}

// The happy path: ownership proven, runtime halted with the caller's own org and
// run id, and only then is the record closed.
func TestStopHaltsRuntimeThenClosesRecord(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_1", Task: "work", Status: "running"})
	rt := &fakeRuntime{}
	app := mountWith(t, runs, rt)

	code, body := call(t, app, http.MethodPost, "/v1/bots/sess_1/stop", "acme")
	if code != http.StatusOK {
		t.Fatalf("stop want 200, got %d (%s)", code, body)
	}
	var v stopView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.RunID != "sess_1" || v.Status != statusStopped {
		t.Fatalf("stop view %+v", v)
	}
	if got := rt.calls(); len(got) != 1 || got[0] != (stopCall{"acme", "sess_1"}) {
		t.Fatalf("runtime must be driven once with the caller's org+run, got %v", got)
	}
	if got := runs.statusOf("acme", "sess_1"); got != "done" {
		t.Fatalf("record must be closed, got %q", got)
	}
}

// A stop that could not reach the executor must not claim the run was stopped —
// and must leave the record live, because it still is.
func TestStopWithUnreachableRuntimeDoesNotCloseTheRecord(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_1", Status: "running"})
	rt := &fakeRuntime{err: fmt.Errorf("connection refused")}
	app := mountWith(t, runs, rt)

	code, body := call(t, app, http.MethodPost, "/v1/bots/sess_1/stop", "acme")
	if code != http.StatusBadGateway {
		t.Fatalf("unreachable runtime want 502, got %d (%s)", code, body)
	}
	if got := runs.statusOf("acme", "sess_1"); got != "running" {
		t.Fatalf("record must stay live when the halt failed, got %q", got)
	}
}

// No tenant, no stop — and neither seam is touched.
func TestStopFailsClosedWithoutTenant(t *testing.T) {
	runs := newFakeRuns()
	runs.seed("acme", Run{ID: "sess_1", Status: "running"})
	rt := &fakeRuntime{}
	app := mountWith(t, runs, rt)

	if code, _ := call(t, app, http.MethodPost, "/v1/bots/sess_1/stop", ""); code != http.StatusForbidden {
		t.Fatalf("no-tenant stop want 403, got %d", code)
	}
	if len(rt.calls()) != 0 {
		t.Fatalf("runtime driven without a tenant: %v", rt.calls())
	}
	if got := runs.statusOf("acme", "sess_1"); got != "running" {
		t.Fatalf("run stopped without a tenant, status %q", got)
	}
}

// An oversize id is a miss like any other — it never reaches the registry.
func TestStopOversizeRunIDIsNotFound(t *testing.T) {
	runs := newFakeRuns()
	runs.getErr = fmt.Errorf("registry must not be consulted for an oversize id")
	app := mountWith(t, runs, &fakeRuntime{})

	long := make([]byte, maxRunID+1)
	for i := range long {
		long[i] = 'a'
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/bots/"+string(long)+"/stop", "acme"); code != http.StatusNotFound {
		t.Fatalf("oversize runId want 404, got %d", code)
	}
}

// /v1/bots/run is the launch literal, never a run id: the router resolves the
// static segment over the :runId param regardless of registration order.
func TestRunLiteralDoesNotBindAsARunID(t *testing.T) {
	runs := newFakeRuns()
	app := mountWith(t, runs, &fakeRuntime{})

	// POST /v1/bots/run with no body reaches the launch handler (400 task required),
	// NOT the stop handler (which would 404 on a run named "run").
	code, body := call(t, app, http.MethodPost, "/v1/bots/run", "acme")
	if code != http.StatusBadRequest {
		t.Fatalf("POST /v1/bots/run must hit launch (400 no task), got %d (%s)", code, body)
	}
}

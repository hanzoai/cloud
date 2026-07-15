package agents

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// ---- store: target CRUD + tenant isolation ----

func TestTargetStoreCRUDAndTenantIsolation(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	now := int64(1000)
	acme := Target{ID: "t-acme", Org: "acme", Label: "laptop", Kind: TargetLaptop, Status: TargetOnline, Host: "mac", CreatedAt: now, UpdatedAt: now}
	evil := Target{ID: "t-evil", Org: "evil", Label: "box", Kind: TargetGPU, Status: TargetOnline, Host: "gpu0", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTarget(ctx, acme); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.CreateTarget(ctx, evil); err != nil {
		t.Fatalf("create evil: %v", err)
	}

	// Get is org-keyed: an org can only resolve its own target id.
	if got, err := s.GetTarget(ctx, "acme", "t-acme"); err != nil || got.Label != "laptop" {
		t.Fatalf("acme get own: %+v %v", got, err)
	}
	if _, err := s.GetTarget(ctx, "acme", "t-evil"); err != errTargetNotFound {
		t.Fatalf("acme resolving evil's target must fail-closed, got %v", err)
	}
	if _, err := s.GetTarget(ctx, "evil", "t-acme"); err != errTargetNotFound {
		t.Fatalf("evil resolving acme's target must fail-closed, got %v", err)
	}

	// List is org-scoped.
	al, _ := s.ListTargets(ctx, "acme")
	if len(al) != 1 || al[0].ID != "t-acme" {
		t.Fatalf("acme list want [t-acme], got %+v", al)
	}
	el, _ := s.ListTargets(ctx, "evil")
	if len(el) != 1 || el[0].ID != "t-evil" {
		t.Fatalf("evil list want [t-evil], got %+v", el)
	}

	// Update under the WRONG org matches no row (fail-closed) — can't mutate cross-tenant.
	cross := acme
	cross.Org = "evil"
	cross.Label = "pwned"
	if err := s.UpdateTarget(ctx, cross); err != errTargetNotFound {
		t.Fatalf("cross-tenant update must fail-closed, got %v", err)
	}
	if got, _ := s.GetTarget(ctx, "acme", "t-acme"); got.Label != "laptop" {
		t.Fatalf("acme target must be untouched by cross-tenant update, got %q", got.Label)
	}

	// Delete is org-scoped: evil deleting acme's id removes nothing.
	if ok, _ := s.DeleteTarget(ctx, "evil", "t-acme"); ok {
		t.Fatalf("evil deleting acme's target must be a no-op")
	}
	if _, err := s.GetTarget(ctx, "acme", "t-acme"); err != nil {
		t.Fatalf("acme target must survive evil's delete: %v", err)
	}
	if ok, _ := s.DeleteTarget(ctx, "acme", "t-acme"); !ok {
		t.Fatalf("acme deleting own target should succeed")
	}
	if _, err := s.GetTarget(ctx, "acme", "t-acme"); err != errTargetNotFound {
		t.Fatalf("deleted target must be gone, got %v", err)
	}
}

// ---- store: session load (by explicit target id OR host), org-scoped ----

func TestTargetSessionLoad(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()

	mk := func(org, id, host, target, status string) {
		x := mkSession(org, id, "", id)
		x.Host, x.Target, x.Status = host, target, status
		if err := s.CreateSession(ctx, x); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Target T maps host "spark".
	mk("acme", "s1", "", "T", StatusRunning) // dispatched to T, no host
	mk("acme", "s2", "spark", "", StatusRunning) // on T's host, running
	mk("acme", "s3", "spark", "", StatusDone) // on T's host, finished
	mk("acme", "s4", "other", "", StatusRunning) // unrelated host, no target
	mk("evil", "s5", "spark", "", StatusRunning) // FOREIGN org, same host

	load, err := s.SessionLoad(ctx, "acme", "T", "spark")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if load.Sessions != 3 { // s1 (target) + s2,s3 (host) — never s4, never foreign s5
		t.Fatalf("load.Sessions want 3, got %d", load.Sessions)
	}
	if load.Running != 2 { // s1, s2 (s3 is done)
		t.Fatalf("load.Running want 2, got %d", load.Running)
	}

	// A target with NO host counts ONLY explicit dispatch (host clause disabled),
	// so a foreign or same-org host session is never miscredited to it.
	load2, _ := s.SessionLoad(ctx, "acme", "T", "")
	if load2.Sessions != 1 || load2.Running != 1 {
		t.Fatalf("hostless target load want {1,1}, got %+v", load2)
	}
}

// ---- store: session execution-context round-trips ----

func TestSessionContextRoundTrip(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	x := mkSession("acme", "sx", "", "sx")
	x.Host, x.Cwd, x.Repo, x.Target = "spark", "/home/z/work", "hanzoai/cloud", "tgt-1"
	if err := s.CreateSession(ctx, x); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetSession(ctx, "acme", "sx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Host != "spark" || got.Cwd != "/home/z/work" || got.Repo != "hanzoai/cloud" || got.Target != "tgt-1" {
		t.Fatalf("context did not round-trip: %+v", got)
	}
}

// ---- HTTP: target isolation + CRUD + validation ----

type targetsResp struct {
	Targets []targetView `json:"targets"`
}

func registerTargetHTTP(t *testing.T, app *zip.App, org string, body map[string]any) targetView {
	t.Helper()
	code, b := do(t, app, http.MethodPost, "/v1/agents/targets", org, body)
	if code != http.StatusCreated {
		t.Fatalf("register target want 201, got %d (%s)", code, b)
	}
	var v targetView
	mustJSON(t, b, &v)
	return v
}

func TestHTTPTargetsIsolationAndCRUD(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})

	// Defaults: no kind/status supplied → machine/online.
	tv := registerTargetHTTP(t, app, "acme", map[string]any{"label": "my laptop"})
	if tv.Kind != TargetMachine || tv.Status != TargetOnline || tv.Label != "my laptop" {
		t.Fatalf("target defaults wrong: %+v", tv)
	}

	// acme sees it; evil does not.
	_, b := do(t, app, http.MethodGet, "/v1/agents/targets", "acme", nil)
	var al targetsResp
	mustJSON(t, b, &al)
	if len(al.Targets) != 1 || al.Targets[0].ID != tv.ID {
		t.Fatalf("acme list want its target, got %+v", al.Targets)
	}
	_, b = do(t, app, http.MethodGet, "/v1/agents/targets", "evil", nil)
	var el targetsResp
	mustJSON(t, b, &el)
	if len(el.Targets) != 0 {
		t.Fatalf("evil must see no targets, got %+v", el.Targets)
	}

	// evil cannot read/mutate acme's target id (fail-closed 404, never 200).
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/targets/"+tv.ID, "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil GET acme target want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPatch, "/v1/agents/targets/"+tv.ID, "evil", map[string]any{"status": TargetOffline}); code != http.StatusNotFound {
		t.Fatalf("evil PATCH acme target want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/agents/targets/"+tv.ID, "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil DELETE acme target want 404, got %d", code)
	}
	// The target survived evil's attempts.
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/targets/"+tv.ID, "acme", nil); code != http.StatusOK {
		t.Fatalf("acme target must survive, got %d", code)
	}

	// Anonymous (X-Org-Id without a validated principal) is refused everywhere.
	if code, _ := doNoUser(t, app, http.MethodGet, "/v1/agents/targets", "acme", nil); code != http.StatusForbidden {
		t.Fatalf("anon list want 403, got %d", code)
	}
	if code, _ := doNoUser(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{"label": "x"}); code != http.StatusForbidden {
		t.Fatalf("anon register want 403, got %d", code)
	}

	// Validation: bad kind / bad status / missing label → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{"label": "x", "kind": "toaster"}); code != http.StatusBadRequest {
		t.Fatalf("bad kind want 400, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{"label": "x", "status": "melting"}); code != http.StatusBadRequest {
		t.Fatalf("bad status want 400, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/targets", "acme", map[string]any{"label": "  "}); code != http.StatusBadRequest {
		t.Fatalf("blank label want 400, got %d", code)
	}

	// Patch mutates; delete removes.
	code, b := do(t, app, http.MethodPatch, "/v1/agents/targets/"+tv.ID, "acme", map[string]any{"status": TargetOffline, "capacity": "8 vCPU / 32G"})
	if code != http.StatusOK {
		t.Fatalf("patch want 200, got %d (%s)", code, b)
	}
	var patched targetView
	mustJSON(t, b, &patched)
	if patched.Status != TargetOffline || patched.Capacity != "8 vCPU / 32G" {
		t.Fatalf("patch did not apply: %+v", patched)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/agents/targets/"+tv.ID, "acme", nil); code != http.StatusOK {
		t.Fatalf("delete want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/targets/"+tv.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("deleted target GET want 404, got %d", code)
	}
}

// ---- HTTP: session <-> target association (#48), fail-closed cross-tenant ----

func TestHTTPSessionTargetAssociation(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})

	tgt := registerTargetHTTP(t, app, "acme", map[string]any{"label": "spark", "kind": TargetGPU, "host": "spark"})

	// Register a session dispatched to the target → 201, target echoed.
	sv := register(t, app, "acme", map[string]any{"agent": "dev", "target": tgt.ID, "host": "spark", "repo": "hanzoai/cloud"})
	if sv.Target != tgt.ID || sv.Host != "spark" || sv.Repo != "hanzoai/cloud" {
		t.Fatalf("session context/target not set: %+v", sv)
	}

	// A bogus target → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions", "acme", map[string]any{"agent": "dev", "target": "does-not-exist"}); code != http.StatusBadRequest {
		t.Fatalf("register with bogus target want 400, got %d", code)
	}

	// Cross-tenant: evil's target id can never be referenced from acme.
	evilTgt := registerTargetHTTP(t, app, "evil", map[string]any{"label": "evilbox"})
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions", "acme", map[string]any{"agent": "dev", "target": evilTgt.ID}); code != http.StatusBadRequest {
		t.Fatalf("acme referencing evil's target want 400, got %d", code)
	}

	// PATCH association: attach, then detach, then reject a bogus/foreign target.
	plain := register(t, app, "acme", map[string]any{"agent": "dev"})
	code, b := do(t, app, http.MethodPatch, "/v1/agents/sessions/"+plain.ID, "acme", map[string]any{"target": tgt.ID})
	if code != http.StatusOK {
		t.Fatalf("patch attach want 200, got %d (%s)", code, b)
	}
	var attached sessionView
	mustJSON(t, b, &attached)
	if attached.Target != tgt.ID {
		t.Fatalf("patch did not attach target: %+v", attached)
	}
	code, b = do(t, app, http.MethodPatch, "/v1/agents/sessions/"+plain.ID, "acme", map[string]any{"target": ""})
	if code != http.StatusOK {
		t.Fatalf("patch detach want 200, got %d (%s)", code, b)
	}
	var detached sessionView
	mustJSON(t, b, &detached)
	if detached.Target != "" {
		t.Fatalf("patch did not detach target: %+v", detached)
	}
	if code, _ := do(t, app, http.MethodPatch, "/v1/agents/sessions/"+plain.ID, "acme", map[string]any{"target": evilTgt.ID}); code != http.StatusBadRequest {
		t.Fatalf("patch to foreign target want 400, got %d", code)
	}

	// The target detail credits the dispatched + host-matched sessions (sv is both).
	_, b = do(t, app, http.MethodGet, "/v1/agents/targets/"+tgt.ID, "acme", nil)
	var td targetView
	mustJSON(t, b, &td)
	if td.Sessions < 1 || td.Running < 1 {
		t.Fatalf("target should credit its running session, got sessions=%d running=%d", td.Sessions, td.Running)
	}
}

// ---- HTTP: compact list carries context + last-event ----

func TestHTTPSessionContextAndLastEvent(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	sv := register(t, app, "acme", map[string]any{"agent": "dev", "host": "spark", "cwd": "/work", "repo": "hanzoai/cloud"})

	// Append a log event so the list row carries a last-event preview.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions/"+sv.ID+"/events", "acme",
		map[string]any{"kind": KindLog, "payload": map[string]any{"line": "hello world"}}); code != http.StatusCreated {
		t.Fatalf("append event want 201, got %d", code)
	}

	_, b := do(t, app, http.MethodGet, "/v1/agents/sessions", "acme", nil)
	var lst struct {
		Sessions []sessionView `json:"sessions"`
	}
	mustJSON(t, b, &lst)
	if len(lst.Sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(lst.Sessions))
	}
	row := lst.Sessions[0]
	if row.Host != "spark" || row.Cwd != "/work" || row.Repo != "hanzoai/cloud" {
		t.Fatalf("list row lost context: %+v", row)
	}
	if row.LastEvent == nil || row.LastEvent.Kind != KindLog {
		t.Fatalf("list row missing last-event: %+v", row.LastEvent)
	}
	if !strings.Contains(row.LastEvent.Preview, "hello world") {
		t.Fatalf("last-event preview missing payload: %q", row.LastEvent.Preview)
	}
}

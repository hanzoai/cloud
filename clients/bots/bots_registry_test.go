package bots

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/agents"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// These drive the REAL registry binding (adapters.go -> the agents session plane
// and its org-scoped SQL), not a fake. The fakes in bots_list_stop_test.go prove
// the handlers pass the validated org; these prove the thing they pass it to
// actually scopes by it — so the isolation argument does not rest on a test
// double agreeing with the code under test.

// mountReal builds the bots surface over the real seams, with a real agents
// session plane mounted behind it. The runtime is still a fake: it is an
// executor, and a unit test must not need one to prove authorization.
func mountReal(t *testing.T) (*zip.App, *fakeRuntime) {
	t.Helper()
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// The session plane is the registry of record; mounting it wires the
	// in-process seam the bots adapter reads through.
	if err := agents.Mount(app, deps); err != nil {
		t.Fatalf("agents.Mount: %v", err)
	}
	rt := &fakeRuntime{}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "bots"),
		State: state{
			bill: cloud.NewResourceMeter(deps, meterKind), gateway: gatewayBase(),
			runs: sessionRuns{}, runtime: rt,
		},
	}
	routes(app, s)
	return app, rt
}

// launch runs one bot for org through the real surface and returns its run id.
func launch(t *testing.T, app *zip.App, org, task, surface string) string {
	t.Helper()
	code, body := do(t, app, org, map[string]any{"task": task, "surface": surface})
	if code != http.StatusOK {
		t.Fatalf("launch for %s want 200, got %d (%s)", org, code, body)
	}
	var rv runView
	if err := json.Unmarshal(body, &rv); err != nil {
		t.Fatalf("decode launch: %v", err)
	}
	if rv.RunID == "" {
		t.Fatal("launch returned no runId")
	}
	return rv.RunID
}

// A launch is durable: it lands in the registry and comes straight back out of
// the org's own list, with the task and surface it was launched with.
func TestRealRegistryLaunchIsListable(t *testing.T) {
	app, _ := mountReal(t)
	id := launch(t, app, "acme", "summarize my inbox", "terminal")

	code, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var v botsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(v.Bots) != 1 {
		t.Fatalf("want exactly the one launched run, got %d (%s)", len(v.Bots), body)
	}
	got := v.Bots[0]
	if got.RunID != id || got.Task != "summarize my inbox" || got.Surface != "terminal" || got.Status != "running" {
		t.Fatalf("listed run does not match the launch: %+v (id %s)", got, id)
	}
	if got.SessionURL != "https://bot.example.test/vnc?nodeId="+id {
		t.Fatalf("sessionUrl %q", got.SessionURL)
	}
	if got.StartedAt == "" {
		t.Fatal("startedAt must be set")
	}
}

// Two tenants, one store: each sees only its own run. This is the real SQL
// boundary, not a fake's map lookup.
func TestRealRegistryListIsTenantIsolated(t *testing.T) {
	app, _ := mountReal(t)
	acme := launch(t, app, "acme", "acme work", "desktop")
	globex := launch(t, app, "globex", "globex secret", "desktop")

	_, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	ids := listRunIDs(t, body)
	if len(ids) != 1 || ids[0] != acme {
		t.Fatalf("acme must see only %s, got %v (%s)", acme, ids, body)
	}
	_, body = call(t, app, http.MethodGet, "/v1/bots", "globex")
	ids = listRunIDs(t, body)
	if len(ids) != 1 || ids[0] != globex {
		t.Fatalf("globex must see only %s, got %v (%s)", globex, ids, body)
	}
}

// The crown jewel against the real store: knowing another tenant's run id buys
// nothing. It is a 404 and the executor is never driven.
func TestRealRegistryStopCannotCrossTenant(t *testing.T) {
	app, rt := mountReal(t)
	victim := launch(t, app, "acme", "acme work", "desktop")

	code, body := call(t, app, http.MethodPost, "/v1/bots/"+victim+"/stop", "globex")
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant stop want 404, got %d (%s)", code, body)
	}
	if len(rt.calls()) != 0 {
		t.Fatalf("runtime driven for another tenant's run: %v", rt.calls())
	}
	// The victim's run is untouched: still running, still listed.
	_, body = call(t, app, http.MethodGet, "/v1/bots", "acme")
	if ids := listRunIDs(t, body); len(ids) != 1 || ids[0] != victim {
		t.Fatalf("victim run must survive a foreign stop, got %v", ids)
	}
}

// Stopping your own run through the real registry: the executor is driven, the
// record goes terminal, and the run leaves the live list.
func TestRealRegistryStopClosesOwnRun(t *testing.T) {
	app, rt := mountReal(t)
	id := launch(t, app, "acme", "acme work", "desktop")

	code, body := call(t, app, http.MethodPost, "/v1/bots/"+id+"/stop", "acme")
	if code != http.StatusOK {
		t.Fatalf("stop want 200, got %d (%s)", code, body)
	}
	if got := rt.calls(); len(got) != 1 || got[0] != (stopCall{"acme", id}) {
		t.Fatalf("runtime stop calls %v", got)
	}
	_, body = call(t, app, http.MethodGet, "/v1/bots", "acme")
	if ids := listRunIDs(t, body); len(ids) != 0 {
		t.Fatalf("stopped run must leave the live list, got %v", ids)
	}
	// The session plane kept the record and moved it to a terminal state — the
	// history is not deleted, it is finished.
	x, found, err := agents.GetSession(context.Background(), "acme", id)
	if err != nil || !found {
		t.Fatalf("stopped run must still exist on the session plane (found=%v err=%v)", found, err)
	}
	if x.Status != agents.StatusDone {
		t.Fatalf("a requested stop ends the run done, got %q", x.Status)
	}
}

// The own-key guard: a session of the caller's OWN org that is not a bot run
// (a coding run) is not reachable through the bots face. The id is real and the
// org matches — it is still a 404, because /v1/bots owns bot runs only.
func TestRealRegistryCannotStopANonBotSession(t *testing.T) {
	app, rt := mountReal(t)
	coding, err := agents.OpenSession(context.Background(), agents.SessionOpen{
		Org: "acme", Actor: "acme/u1", Agent: "hanzo", Title: "code: api — fix bug",
	})
	if err != nil {
		t.Fatalf("open coding session: %v", err)
	}

	code, body := call(t, app, http.MethodPost, "/v1/bots/"+coding+"/stop", "acme")
	if code != http.StatusNotFound {
		t.Fatalf("stopping a coding session via /v1/bots want 404, got %d (%s)", code, body)
	}
	if len(rt.calls()) != 0 {
		t.Fatalf("runtime driven for a non-bot session: %v", rt.calls())
	}
	// It is untouched, and it never appears in the bots list.
	x, found, err := agents.GetSession(context.Background(), "acme", coding)
	if err != nil || !found || x.Status != agents.StatusRunning {
		t.Fatalf("coding session must be untouched (found=%v status=%q err=%v)", found, x.Status, err)
	}
	_, body = call(t, app, http.MethodGet, "/v1/bots", "acme")
	if ids := listRunIDs(t, body); len(ids) != 0 {
		t.Fatalf("a coding session must not list as a bot run, got %v", ids)
	}
}

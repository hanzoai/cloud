package compute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeAgents is a faithful stand-in for the cloud /v1/agent surface the bot
// composition depends on. It implements exactly the two behaviors a bot relies
// on, so a test proves the launch→message path without a real gateway:
//
//	POST /v1/agent            create-if-absent — records the agent and 201s;
//	                           409 on a repeat (idempotency); 400 when a
//	                           non-empty model is outside its catalog (so a test
//	                           proves launchBot propagates the model-validation
//	                           400 before provisioning any machine).
//	POST /v1/agent/:name/run  runs ONLY an agent that was actually created
//	                           (200 pong), else 404 "agent not found" — the exact
//	                           Resolve semantics messageBot depends on, so a test
//	                           proves resolve-now-succeeds because launch created it.
type fakeAgents struct {
	mu           sync.Mutex
	created      map[string]map[string]string // name -> {model, instructions}
	catalog      map[string]bool              // served models, for validation
	lastRunAgent string
	lastRunInput string
}

func newFakeAgents() *fakeAgents {
	return &fakeAgents{
		created: map[string]map[string]string{},
		catalog: map[string]bool{"zen-flash": true, "deepseek-v4-flash": true},
	}
}

func (a *fakeAgents) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /v1/agent — create-if-absent (with model validation).
	mux.HandleFunc("/v1/agent", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Name, Model, Instructions string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.mu.Lock()
		defer a.mu.Unlock()
		if m := strings.TrimSpace(b.Model); m != "" && !a.catalog[m] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"model not in catalog"}`))
			return
		}
		if _, exists := a.created[b.Name]; exists {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"agent already exists in this org"}`))
			return
		}
		a.created[b.Name] = map[string]string{"model": b.Model, "instructions": b.Instructions}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"agent_x","name":"` + b.Name + `"}`))
	})

	// POST /v1/agent/{name}/run — runs only a created agent, else 404 (Resolve).
	mux.HandleFunc("/v1/agent/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/agent/"), "/run")
		var b struct{ Input string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.lastRunAgent, a.lastRunInput = name, b.Input
		if _, ok := a.created[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"agent not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","output":"pong"}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// wasCreated reports whether an agent of that name was created via POST /v1/agent.
func (a *fakeAgents) wasCreated(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.created[name]
	return ok
}

// botVM is a stand-in for the vm (Visor) resell compute + binding surface, and
// it speaks BOTH of vm's wires because vm does — the same split fakeVisor makes
// with envelope200 and op200 (http_test.go):
//
//   - the machines + launch routes still use that envelope, so those answer HTTP 200
//     with the {status,msg,data} envelope;
//   - a machine's AGENT is a typed op, so those answer the value itself, 204 for
//     the unbind, and 404 for a read of a machine that runs no bot.
//
// That second set is not invented here. It is written to match what visor's own
// controllers/agent_wire_test.go asserts against the real handlers — a fake that
// agreed only with this client would prove the two agree with each other and
// nothing about the service. Change one and the other is where to look.
//
// Every read is scoped by the ?owner query, so a test proves cloud forwards the
// VALIDATED principal's org; the last bind/unbind is recorded so a test can
// assert the composition.
type botVM struct {
	bots         map[string]map[string]any // id -> machine (kind=bot)
	bindings     map[string]agentBinding   // id -> binding
	lastOwner    string
	lastBindOrg  string
	lastBindName string // agentName last bound
	lastUnbind   string // id last unbound
}

func newBotVM() *botVM {
	return &botVM{bots: map[string]map[string]any{}, bindings: map[string]agentBinding{}}
}

func (f *botVM) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// GET /v1/machines?owner=&kind=bot — the collection, kind-filtered.
	mux.HandleFunc("GET /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		out := []map[string]any{}
		if r.URL.Query().Get("kind") == "bot" || r.URL.Query().Get("kind") == "" {
			for _, m := range f.bots {
				out = append(out, m)
			}
		}
		envelope200(w, out)
	})

	// POST /v1/machines — quote (dryRun) or launch a machine. Adding to the
	// collection is what POST means, so it needs no address of its own.
	mux.HandleFunc("POST /v1/machines", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		quote := map[string]any{"size": body["size"], "region": body["region"], "priceHourly": 1.57, "currency": "usd"}
		if dry, _ := body["dryRun"].(bool); dry {
			envelope200(w, quote)
			return
		}
		name, _ := body["name"].(string)
		id := "drop-" + name
		machine := map[string]any{
			"owner": f.lastOwner, "name": name, "id": id,
			"size": body["size"], "region": body["region"], "state": "provisioning",
			"tag": "hanzo-kind:bot",
		}
		f.bots[id] = machine
		envelope200(w, map[string]any{"machine": machine, "quote": quote})
	})

	// GET /v1/machines/agents?owner= — the org's bindings. A TYPED op: the object
	// itself, keyed agentBindings, no envelope. Registered ahead of the
	// /v1/machines/ prefix pattern below because net/http prefers the longer
	// pattern anyway — stating it here keeps the two readable in match order.
	mux.HandleFunc("/v1/machines/agents", func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.URL.Query().Get("owner")
		out := []agentBinding{}
		for _, b := range f.bindings {
			out = append(out, b)
		}
		op200(w, map[string]any{"agentBindings": out})
	})

	// /v1/machines/{owner}/{name}[/agent] — one machine and its sub-resource. The
	// tenant is in the ADDRESS, so the fake reads it where the server does; there
	// is no ?owner= on an item call to disagree with the path.
	item := func(w http.ResponseWriter, r *http.Request) {
		f.lastOwner = r.PathValue("owner")
		id := r.PathValue("name")
		parts := []string{id}
		if strings.HasSuffix(r.URL.Path, "/agent") {
			parts = append(parts, "agent")
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet: // read one machine
			if m, ok := f.bots[id]; ok {
				envelope200(w, m)
				return
			}
			envelope200(w, map[string]any{}) // not found -> empty machine
		case len(parts) == 1 && r.Method == http.MethodDelete: // DeleteComputeMachine
			delete(f.bots, id)
			envelope200(w, "deleted")
		case len(parts) == 2 && parts[1] == "agent" && r.Method == http.MethodPut:
			var b struct {
				AgentName, BotVersion string
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			// vm takes the owning org from the ?owner it resolved, never from the
			// body — which is why the body no longer carries one.
			f.lastBindOrg, f.lastBindName = f.lastOwner, b.AgentName
			binding := agentBinding{
				Owner: f.lastOwner, Name: id, MachineId: f.lastOwner + "/" + id, Org: f.lastOwner,
				AgentName: b.AgentName, BotVersion: b.BotVersion, Status: "Pending",
				Message: "machine provisioning; @hanzo/bot runtime not yet confirmed",
			}
			f.bindings[id] = binding
			op200(w, binding)
		case len(parts) == 2 && parts[1] == "agent" && r.Method == http.MethodGet:
			b, ok := f.bindings[id]
			if !ok {
				// A machine that runs no bot is a 404, not a 200 carrying an empty
				// object — see visor controllers/agent_wire_test.go.
				http.Error(w, `{"status":404,"error":"no agent binding for machine"}`, http.StatusNotFound)
				return
			}
			op200(w, b)
		case len(parts) == 2 && parts[1] == "agent" && r.Method == http.MethodDelete:
			f.lastUnbind = id
			delete(f.bindings, id)
			w.WriteHeader(http.StatusNoContent) // idempotent, and nothing to say
		default:
			http.Error(w, "unhandled "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}

	// Two EXACT addresses, not one subtree. A subtree pattern redirects the item
	// call to a trailing slash, and Go's client answers a 301 by reissuing as GET
	// — so a DELETE would arrive as a read and the fake would report success.
	mux.HandleFunc("/v1/machines/{owner}/{name}", item)
	mux.HandleFunc("/v1/machines/{owner}/{name}/agent", item)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountBots wires the visor surface against the fake vm (and, when given, a fake
// agents server for the message path).
func mountBots(t *testing.T, f *botVM, agentsURL string) *zip.App {
	t.Helper()
	srv := f.server(t)
	t.Setenv("COMPUTE_URL", srv.URL)
	t.Setenv("COMPUTE_CLIENT_ID", "")
	t.Setenv("COMPUTE_CLIENT_SECRET", "")
	if agentsURL != "" {
		t.Setenv("CLOUD_AGENTS_URL", agentsURL)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge()) // typed ops read tenant + request off the context (see mountApp)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

func TestBotsGatedNoPrincipal(t *testing.T) {
	app := mountBots(t, newBotVM(), "")
	// Every bot + machine-agent route must 403 (not 404) without a validated
	// principal — routed and org-gated exactly like /v1/compute/machines.
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/compute/bots"},
		{http.MethodPost, "/v1/compute/bots/launch"},
		{http.MethodGet, "/v1/compute/bots/launch"}, // matches /v1/compute/bots/:id — still gated
		{http.MethodGet, "/v1/compute/bots/drop-x"},
		{http.MethodDelete, "/v1/compute/bots/drop-x"},
		{http.MethodPost, "/v1/compute/bots/drop-x/stop"},
		{http.MethodPost, "/v1/compute/bots/drop-x/message"},
		{http.MethodPut, "/v1/compute/machines/drop-x/agent"},
		{http.MethodGet, "/v1/compute/machines/drop-x/agent"},
		{http.MethodDelete, "/v1/compute/machines/drop-x/agent"},
		{http.MethodGet, "/v1/compute/machines/agents"},
	}
	for _, tc := range cases {
		if code, _ := do(t, app, tc.method, tc.path, "", nil); code != http.StatusForbidden {
			t.Errorf("%s %s no-principal = %d, want 403", tc.method, tc.path, code)
		}
	}
}

func TestBotLaunchQuoteAndReal(t *testing.T) {
	f := newBotVM()
	fa := newFakeAgents()
	app := mountBots(t, f, fa.server(t).URL)

	// dryRun → the price quote verbatim, no machine, no bind, NO agent created.
	code, body := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "helper", "dryRun": true})
	if code != http.StatusOK || !strings.Contains(string(body), `"priceHourly"`) {
		t.Fatalf("dryRun want 200 quote, got %d %s", code, body)
	}
	if len(f.bots) != 0 || f.lastBindName != "" || fa.wasCreated("helper") {
		t.Fatalf("dryRun must not launch, bind or create an agent (bots=%d bind=%q created=%v)",
			len(f.bots), f.lastBindName, fa.wasCreated("helper"))
	}

	// A real launch → 201 botView with the agent bound (agent defaults to name).
	code, body = do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "helper"})
	if code != http.StatusCreated {
		t.Fatalf("launch want 201, got %d %s", code, body)
	}
	// The bound agent was auto-created (so the bot is immediately messageable).
	if !fa.wasCreated("helper") {
		t.Fatalf("launch must auto-create the bound agent")
	}
	var bv botView
	if err := json.Unmarshal(body, &bv); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if bv.Name != "helper" || bv.Status != "provisioning" || bv.Agent != "helper" || bv.Binding == nil {
		t.Fatalf("bot view mismatch: %+v", bv)
	}
	if f.lastOwner != "acme" || f.lastBindOrg != "acme" || f.lastBindName != "helper" {
		t.Fatalf("cloud must forward validated org+agent: owner=%q bindOrg=%q agent=%q", f.lastOwner, f.lastBindOrg, f.lastBindName)
	}

	// An explicit agent overrides the name default.
	_, body = do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "sup", "agent": "support"})
	_ = json.Unmarshal(body, &bv)
	if f.lastBindName != "support" || bv.Agent != "support" {
		t.Fatalf("explicit agent want support, got bind=%q view=%q", f.lastBindName, bv.Agent)
	}

	// size required; real launch requires a name.
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme", map[string]any{"region": "sfo3"}); code != http.StatusBadRequest {
		t.Fatalf("launch without size want 400, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme", map[string]any{"size": "s-2vcpu-4gb"}); code != http.StatusBadRequest {
		t.Fatalf("real launch without name want 400, got %d", code)
	}
}

func TestBotListGetDelete(t *testing.T) {
	f := newBotVM()
	app := mountBots(t, f, newFakeAgents().server(t).URL)
	// Launch two bots for acme.
	for _, n := range []string{"a", "b"} {
		if code, body := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
			map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": n}); code != http.StatusCreated {
			t.Fatalf("seed launch %s: %d %s", n, code, body)
		}
	}

	// list → both bots, kind=bot, each joined with its binding.
	code, body := do(t, app, http.MethodGet, "/v1/compute/bots", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d %s", code, body)
	}
	var listed struct {
		Bots []botView `json:"bots"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(listed.Bots) != 2 {
		t.Fatalf("want 2 bots, got %d", len(listed.Bots))
	}
	for _, b := range listed.Bots {
		if b.Agent == "" || b.Binding == nil {
			t.Fatalf("listed bot missing joined binding: %+v", b)
		}
	}

	// get one → botView.
	code, body = do(t, app, http.MethodGet, "/v1/compute/bots/drop-a", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get want 200, got %d %s", code, body)
	}
	var bv botView
	_ = json.Unmarshal(body, &bv)
	if bv.Agent != "a" {
		t.Fatalf("get bot agent want a, got %q", bv.Agent)
	}

	// a non-bot machine (no kind tag, no binding) is not a bot → 404.
	f.bots["plain"] = map[string]any{"owner": "acme", "name": "plain", "id": "plain", "state": "running"}
	if code, _ := do(t, app, http.MethodGet, "/v1/compute/bots/plain", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get non-bot want 404, got %d", code)
	}

	// delete → 204, and the bot unbinds AND the machine is gone.
	if code, _ := do(t, app, http.MethodDelete, "/v1/compute/bots/drop-a", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	if f.lastUnbind != "drop-a" {
		t.Fatalf("delete must unbind the agent, lastUnbind=%q", f.lastUnbind)
	}
	if _, ok := f.bots["drop-a"]; ok {
		t.Fatalf("delete must terminate the machine")
	}
}

func TestBotStopPause(t *testing.T) {
	f := newBotVM()
	app := mountBots(t, f, newFakeAgents().server(t).URL)
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "c"}); code != http.StatusCreated {
		t.Fatal("seed launch")
	}
	// stop → unbind (halt the agent), 200 {status:stopped}. Machine stays.
	code, body := do(t, app, http.MethodPost, "/v1/compute/bots/drop-c/stop", "acme", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"stopped"`) {
		t.Fatalf("stop want 200 stopped, got %d %s", code, body)
	}
	if f.lastUnbind != "drop-c" {
		t.Fatalf("stop must unbind, lastUnbind=%q", f.lastUnbind)
	}
	if _, ok := f.bots["drop-c"]; !ok {
		t.Fatalf("stop must NOT delete the machine")
	}
	// pause routes to the same halt.
	f.lastUnbind = ""
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/drop-c/pause", "acme", nil); code != http.StatusOK {
		t.Fatalf("pause want 200, got %d", code)
	}
	if f.lastUnbind != "drop-c" {
		t.Fatalf("pause must unbind, lastUnbind=%q", f.lastUnbind)
	}
	// an unknown action is a clean 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/drop-c/frobnicate", "acme", nil); code != http.StatusBadRequest {
		t.Fatalf("unknown action want 400, got %d", code)
	}
}

func TestBotMessageRunsAgent(t *testing.T) {
	f := newBotVM()
	fa := newFakeAgents()
	app := mountBots(t, f, fa.server(t).URL)

	// Launch a bot with an explicit agent name — launch auto-creates that agent.
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "chat", "agent": "concierge"}); code != http.StatusCreated {
		t.Fatal("seed launch")
	}
	if !fa.wasCreated("concierge") {
		t.Fatalf("launch must auto-create the bound agent 'concierge'")
	}

	// message → runs the BOUND agent with the caller's input; because launch
	// created it, Resolve succeeds and the response passes through (200 pong).
	code, body := do(t, app, http.MethodPost, "/v1/compute/bots/drop-chat/message", "acme",
		map[string]any{"input": "ping"})
	if code != http.StatusOK || !strings.Contains(string(body), `"pong"`) {
		t.Fatalf("message want 200 pong, got %d %s", code, body)
	}
	if fa.lastRunAgent != "concierge" || fa.lastRunInput != "ping" {
		t.Fatalf("message must run the bound agent: agent=%q input=%q", fa.lastRunAgent, fa.lastRunInput)
	}

	// A machine that runs NO agent is a 400 that says what the caller can do
	// about it — not the upstream 404 that says only that a lookup missed.
	//
	// This is the one place the difference is visible in the STATUS: vm answers
	// 404 to a read of an unbound machine, and that is a fact about the machine
	// rather than a fault, so messageBot has to recognise it and answer in its
	// own terms. Without that recognition the caller is told "not found" about a
	// bot that exists and is running.
	f.bots["drop-mute"] = map[string]any{"owner": "acme", "name": "mute", "id": "drop-mute", "state": "running", "tag": "hanzo-kind:bot"}
	code, body = do(t, app, http.MethodPost, "/v1/compute/bots/drop-mute/message", "acme",
		map[string]any{"input": "ping"})
	if code != http.StatusBadRequest {
		t.Fatalf("message an unbound bot want 400, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "no bound agent") {
		t.Fatalf("message an unbound bot must say so, got %s", body)
	}
}

// TestBotLaunchAutoCreateClosesTheGap is the regression for the launch→message
// gap: a launched bot must be immediately messageable. It proves both halves —
// the OLD broken path and the NEW fixed one — against the SAME faithful agents
// fake whose /run 404s "agent not found" for any agent that was never created
// (exactly what messageBot's in-process Resolve does).
func TestBotLaunchAutoCreateClosesTheGap(t *testing.T) {
	f := newBotVM()
	fa := newFakeAgents()
	app := mountBots(t, f, fa.server(t).URL)

	// OLD path (the bug): a machine bound to an agent that was NEVER created is
	// un-messageable — run Resolves nothing → 404. We reproduce it by binding an
	// agent directly (the thin proxy does NOT auto-create), then messaging it.
	f.bots["drop-ghost"] = map[string]any{"owner": "acme", "name": "ghost", "id": "drop-ghost", "state": "running", "tag": "hanzo-kind:bot"}
	if code, _ := do(t, app, http.MethodPut, "/v1/compute/machines/drop-ghost/agent", "acme",
		map[string]any{"agentName": "ghost"}); code != http.StatusOK {
		t.Fatal("seed direct bind")
	}
	if fa.wasCreated("ghost") {
		t.Fatalf("precondition: a direct bind must NOT create the agent")
	}
	if code, body := do(t, app, http.MethodPost, "/v1/compute/bots/drop-ghost/message", "acme",
		map[string]any{"input": "hi"}); code != http.StatusNotFound {
		t.Fatalf("bug repro: messaging an uncreated agent must 404, got %d %s", code, body)
	}

	// NEW path (the fix): launchBot auto-creates the bound agent, so the very same
	// message now Resolves and runs → 200. launch → message works.
	if code, _ := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "helper"}); code != http.StatusCreated {
		t.Fatal("launch")
	}
	if code, body := do(t, app, http.MethodPost, "/v1/compute/bots/drop-helper/message", "acme",
		map[string]any{"input": "hi"}); code != http.StatusOK || !strings.Contains(string(body), `"pong"`) {
		t.Fatalf("launched bot must be messageable, got %d %s", code, body)
	}

	// Idempotent: relaunching the same bot (agent already exists → 409 create) is
	// NOT an error — launch still 201s and the bot stays messageable.
	if code, body := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "helper"}); code != http.StatusCreated {
		t.Fatalf("relaunch (idempotent create) want 201, got %d %s", code, body)
	}

	// Model validation propagates: a launch naming a non-catalog model fails fast
	// with the agent surface's 400 — and provisions NO machine (fail-fast order).
	botsBefore := len(f.bots)
	if code, body := do(t, app, http.MethodPost, "/v1/compute/bots/launch", "acme",
		map[string]any{"size": "s-2vcpu-4gb", "region": "sfo3", "name": "badmodel", "model": "claude-sonnet-4-5"}); code != http.StatusBadRequest {
		t.Fatalf("launch with non-catalog model want 400, got %d %s", code, body)
	}
	if len(f.bots) != botsBefore {
		t.Fatalf("a bad-model launch must not provision a machine (bots %d→%d)", botsBefore, len(f.bots))
	}
}

func TestMachineAgentBindingProxies(t *testing.T) {
	f := newBotVM()
	app := mountBots(t, f, "")
	// Seed a resell machine to bind against.
	f.bots["drop-m"] = map[string]any{"owner": "acme", "name": "m", "id": "drop-m", "state": "running"}

	// PUT agent → 200 binding, org forwarded from the validated principal.
	code, body := do(t, app, http.MethodPut, "/v1/compute/machines/drop-m/agent", "acme",
		map[string]any{"agentName": "worker", "botVersion": "1.2.3"})
	if code != http.StatusOK {
		t.Fatalf("bind want 200, got %d %s", code, body)
	}
	var b agentBinding
	_ = json.Unmarshal(body, &b)
	if b.AgentName != "worker" || f.lastBindOrg != "acme" || f.lastBindName != "worker" {
		t.Fatalf("bind mismatch: view=%+v bindOrg=%q", b, f.lastBindOrg)
	}
	if code, _ := do(t, app, http.MethodPut, "/v1/compute/machines/drop-m/agent", "acme", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("bind without agentName want 400, got %d", code)
	}

	// GET the binding → 200; a machine with none → 404.
	if code, _ := do(t, app, http.MethodGet, "/v1/compute/machines/drop-m/agent", "acme", nil); code != http.StatusOK {
		t.Fatalf("get binding want 200, got %d", code)
	}
	// A machine with none is 404 — and the 404 is THIS route's, not vm's passed
	// through. Both paths answer 404, so only the message tells them apart, and
	// only the message proves the upstream miss was recognised as a fact about
	// the machine rather than relayed as an upstream fault.
	code, body = do(t, app, http.MethodGet, "/v1/compute/machines/nope/agent", "acme", nil)
	if code != http.StatusNotFound {
		t.Fatalf("get missing binding want 404, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "no agent binding for machine") {
		t.Fatalf("get missing binding must answer in its own words, got %s", body)
	}
	if strings.Contains(string(body), "upstream") {
		t.Fatalf("get missing binding relayed vm's prose: %s", body)
	}

	// list machine agents → 200 {agentBindings:[...]} with the one we bound.
	code, body = do(t, app, http.MethodGet, "/v1/compute/machines/agents", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list bindings want 200, got %d", code)
	}
	var out struct {
		AgentBindings []agentBinding `json:"agentBindings"`
	}
	_ = json.Unmarshal(body, &out)
	if len(out.AgentBindings) != 1 || out.AgentBindings[0].AgentName != "worker" {
		t.Fatalf("list bindings mismatch: %+v", out.AgentBindings)
	}

	// DELETE the binding → 204, and it is gone.
	if code, _ := do(t, app, http.MethodDelete, "/v1/compute/machines/drop-m/agent", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("unbind want 204, got %d", code)
	}
	if f.lastUnbind != "drop-m" || len(f.bindings) != 0 {
		t.Fatalf("unbind must remove the binding, lastUnbind=%q left=%d", f.lastUnbind, len(f.bindings))
	}
}

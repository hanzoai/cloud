package git

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	wire "github.com/hanzoai/git/modules/actions/runner"
	"github.com/zap-proto/zip"
)

// runner_wire_test.go is the ORACLE for the port.
//
// The runner protocol has a reference implementation — the forge at
// github.com/hanzoai/git, routers/api/actions/runner.go — and a client that
// speaks it today, github.com/hanzoai/git-runner. This file holds that reference
// to the implementation here: same addresses, same operation names, same typed
// payloads, same validation, same timestamp precision, same failures. Where the
// two disagree, this is the file that says so.
//
// The addresses are stated as literals and the names DERIVED from them with
// zip.ID, which is exactly what the runner client's own test does — so the
// derivation under test is the one both ends actually use, not a copy of it.

// forgeRoute is the reference implementation's route base
// (routers/api/actions/runner.go: const RunnerRouteBase). Cloud must serve the
// same address, because the deployed runner client dials a host and asks for the
// op by the name derived from it.
const forgeRoute = "/v1/runner"

// forgeOps are the five operations the reference implementation registers, in
// the order it registers them.
var forgeOps = []string{"register", "declare", "task", "state", "log"}

// TestRunnerServesExactlyTheFiveOperations is P1. The plugin answers the five
// runner operations at /v1/runner and NOTHING else lives there — in particular
// nothing that means "build this for me", which is a different verb served by a
// different app at a different address (platform's POST /v1/platform/runner).
func TestRunnerServesExactlyTheFiveOperations(t *testing.T) {
	app := mountApp(t)

	var got []string
	for _, r := range app.Routes() {
		if strings.HasPrefix(r.Pattern, forgeRoute) {
			got = append(got, r.Method+" "+r.Pattern+" -> "+r.Op)
		}
	}
	sort.Strings(got)

	var want []string
	for _, name := range forgeOps {
		path := forgeRoute + "/" + name
		want = append(want, "POST "+path+" -> "+zip.ID("POST", path))
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("routes under %s:\n got %v\nwant %v", forgeRoute, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route %d:\n got %s\nwant %s", i, got[i], want[i])
		}
	}
	t.Logf("served under %s:\n\t%s", forgeRoute, strings.Join(got, "\n\t"))

	// The address constant is the ONE place the base is written on this side.
	if RunnerRoute != forgeRoute {
		t.Fatalf("RunnerRoute = %q, reference implementation serves %q", RunnerRoute, forgeRoute)
	}

	// The op names are what the deployed runner client asks for by name over the
	// call plane (git-runner internal/pkg/client/forge.go).
	for _, name := range forgeOps {
		if id := zip.ID("POST", forgeRoute+"/"+name); id != "post_runner_"+name {
			t.Fatalf("op id for %s = %q, client calls %q", name, id, "post_runner_"+name)
		}
	}

	// Nothing here answers a build request. The build door's own body reaches
	// no route at this root, and the root itself is not a route.
	code, _ := do(t, app, http.MethodPost, forgeRoute, "", map[string]string{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:x",
	})
	if code != http.StatusNotFound {
		t.Fatalf("POST %s (a build-door body) = %d, want 404: this root serves runners, not builds", forgeRoute, code)
	}
}

// runnerCall posts one runner operation, carrying the credential the way the
// wire says: as headers, never in the body (wire.Credential's json:"-").
func runnerCall(t *testing.T, app *zip.App, name string, cred wire.Credential, in, out any) int {
	t.Helper()
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, forgeRoute+"/"+name, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if cred.UUID != "" {
		req.Header.Set("x-runner-uuid", cred.UUID)
		req.Header.Set("x-runner-token", cred.Token)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, body, err)
		}
	}
	if resp.StatusCode >= 400 {
		t.Logf("%s -> %d %s", name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.StatusCode
}

// declarePoolFor declares capacity and returns the secret a daemon joins with.
func declarePoolFor(t *testing.T, app *zip.App, org, name string, labels []string) string {
	t.Helper()
	code, body := do(t, app, http.MethodPost, "/v1/git/pools", org,
		map[string]any{"name": name, "labels": labels})
	if code != http.StatusCreated {
		t.Fatalf("declare pool %s: %d %s", name, code, body)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("declare pool: %v", err)
	}
	if out.Secret == "" {
		t.Fatal("declare pool answered no join secret")
	}
	return out.Secret
}

// TestRunnerRefusesCapacityNobodyDeclared is P3's negative half: there is no
// path on which a daemon that is merely running becomes usable capacity.
func TestRunnerRefusesCapacityNobodyDeclared(t *testing.T) {
	app := mountApp(t)

	// A pool nobody declared. There is no row, so there is no secret, so there is
	// nothing to present — and presenting a plausible one is refused.
	code := runnerCall(t, app, "register", wire.Credential{},
		&wire.RegisterIn{Name: "evo-1", Token: "acme/evo.deadbeef"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("register into an undeclared pool = %d, want 401", code)
	}

	// A declared pool, wrong secret.
	declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
	code = runnerCall(t, app, "register", wire.Credential{},
		&wire.RegisterIn{Name: "evo-1", Token: "acme/evo.wrong"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("register with a wrong secret = %d, want 401", code)
	}

	// A token that is not a join secret at all.
	code = runnerCall(t, app, "register", wire.Credential{},
		&wire.RegisterIn{Name: "evo-1", Token: "just-a-string"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("register with a malformed secret = %d, want 401", code)
	}

	// The reference implementation's validation, verbatim: a register with no
	// name or no token is a bad request, before any lookup.
	if code := runnerCall(t, app, "register", wire.Credential{},
		&wire.RegisterIn{Token: "acme/evo.x"}, nil); code != http.StatusBadRequest {
		t.Fatalf("register with no name = %d, want 400", code)
	}
	if code := runnerCall(t, app, "register", wire.Credential{},
		&wire.RegisterIn{Name: "evo-1"}, nil); code != http.StatusBadRequest {
		t.Fatalf("register with no token = %d, want 400", code)
	}

	// Every credentialled operation refuses a caller it cannot place, and the
	// refusal does not distinguish an unknown handle from a wrong token.
	for _, name := range []string{"declare", "task", "state", "log"} {
		if code := runnerCall(t, app, name, wire.Credential{}, map[string]any{}, nil); code != http.StatusUnauthorized {
			t.Fatalf("%s with no credential = %d, want 401", name, code)
		}
		bad := wire.Credential{UUID: "acme/runner_0000", Token: "nope"}
		if code := runnerCall(t, app, name, bad, map[string]any{}, nil); code != http.StatusUnauthorized {
			t.Fatalf("%s with an unknown handle = %d, want 401", name, code)
		}
	}
}

// workflowFor is a one-job workflow asking for the labels given.
func workflowFor(labels ...string) string {
	return fmt.Sprintf("name: ci\non: push\njobs:\n  build:\n    runs-on: [%s]\n    steps:\n      - run: echo hi\n",
		strings.Join(labels, ", "))
}

// pushWorkflow lands a repo carrying one workflow and returns the commit.
func pushWorkflow(t *testing.T, app *zip.App, org, repo, body string) string {
	t.Helper()
	code, out := do(t, app, http.MethodPost, "/v1/git/repos/"+repo+"/push", org, map[string]any{
		"name": repo, "branch": "main", "message": "ci",
		"files": []map[string]string{{"path": ".hanzo/workflows/ci.yml", "content": body}},
	})
	if code != http.StatusOK {
		t.Fatalf("push: %d %s", code, out)
	}
	var r struct {
		Commit string `json:"commit"`
	}
	_ = json.Unmarshal(out, &r)
	return r.Commit
}

// TestRunnerLeasesTaskThroughTheWholeProtocol is P3's positive half and the
// wire's round trip: register, declare, task, state and log, end to end, against
// a pool an operator declared and a run a push created.
func TestRunnerLeasesTaskThroughTheWholeProtocol(t *testing.T) {
	app := mountApp(t)
	secret := declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64", "ubuntu-latest"})

	var reg wire.RegisterOut
	if code := runnerCall(t, app, "register", wire.Credential{}, &wire.RegisterIn{
		Name: "evo-1", Token: secret, Version: "1.2.3",
		Labels: []string{"linux/amd64"}, Capabilities: []string{"cancelling"},
	}, &reg); code != http.StatusOK {
		t.Fatalf("register = %d", code)
	}
	if reg.Runner.UUID == "" || reg.Runner.Token == "" {
		t.Fatalf("register answered no credential: %+v", reg.Runner)
	}
	if reg.Runner.Name != "evo-1" || reg.Runner.Ephemeral {
		t.Fatalf("register echoed %+v", reg.Runner)
	}
	cred := wire.Credential{UUID: reg.Runner.UUID, Token: reg.Runner.Token}

	// declare: the two sides learn about each other from one exchange.
	var dec wire.DeclareOut
	if code := runnerCall(t, app, "declare", cred, &wire.DeclareIn{
		Version: "1.2.4", Labels: []string{"linux/amd64"}, Capabilities: []string{"cancelling"},
	}, &dec); code != http.StatusOK {
		t.Fatalf("declare = %d", code)
	}
	if len(dec.Capabilities) == 0 || dec.Capabilities[0] != "job-summary" {
		t.Fatalf("declare capabilities = %v, want the forge's [job-summary]", dec.Capabilities)
	}
	if dec.Runner.Version != "1.2.4" {
		t.Fatalf("declare did not store the version: %+v", dec.Runner)
	}

	// No work yet: an empty queue answers a version and no task.
	var idle wire.TaskOut
	if code := runnerCall(t, app, "task", cred, &wire.TaskIn{Queue: 0}, &idle); code != http.StatusOK {
		t.Fatalf("task = %d", code)
	}
	if idle.Task != nil {
		t.Fatalf("task answered work before any push: %+v", idle.Task)
	}
	if idle.Queue == 0 {
		t.Fatal("task answered queue 0, which tells a runner the forge cannot version its queue")
	}

	// A push creates the run; the drain turns the journal note into it.
	commit := pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))
	drain(mounted.Load(), t.Context(), "acme")

	var got wire.TaskOut
	if code := runnerCall(t, app, "task", cred, &wire.TaskIn{Queue: idle.Queue}, &got); code != http.StatusOK {
		t.Fatalf("task = %d", code)
	}
	if got.Task == nil {
		t.Fatal("no task leased after a push into a declared pool")
	}
	if got.Task.ID == 0 {
		t.Fatal("a leased task must carry a nonzero id")
	}
	if !strings.Contains(string(got.Task.Workflow), "runs-on") {
		t.Fatalf("task carried no workflow document: %q", got.Task.Workflow)
	}
	c := got.Task.Context
	if c.Job != "build" || c.EventName != "push" || c.Sha != commit ||
		c.Repository != "acme/widgets" || c.RepositoryOwner != "acme" ||
		c.Ref != "refs/heads/main" || c.RefName != "main" {
		t.Fatalf("task context is wrong: %+v", c)
	}
	if c.ServerURL == "" || c.APIURL == "" || c.ActionsURL == "" {
		t.Fatalf("task context carries no addresses: %+v", c)
	}
	if len(c.Event) == 0 || !json.Valid(c.Event) {
		t.Fatalf("task context event is not json: %q", c.Event)
	}

	// A second runner in the same pool finds nothing: a task is leased once.
	var second wire.RegisterOut
	if code := runnerCall(t, app, "register", wire.Credential{},
		&wire.RegisterIn{Name: "evo-2", Token: secret}, &second); code != http.StatusOK {
		t.Fatalf("register second = %d", code)
	}
	var none wire.TaskOut
	runnerCall(t, app, "task", wire.Credential{UUID: second.Runner.UUID, Token: second.Runner.Token},
		&wire.TaskIn{Queue: 0}, &none)
	if none.Task != nil {
		t.Fatalf("a leased task was handed to a second runner: %d", none.Task.ID)
	}

	// log: nanosecond instants, and the ack is index + lines accepted.
	const nano = int64(1_757_000_000_123_456_789)
	var logged wire.LogOut
	if code := runnerCall(t, app, "log", cred, &wire.LogIn{
		Task: got.Task.ID, Index: 0,
		Lines: []wire.Line{{Time: nano, Content: "hello"}, {Time: nano + 1, Content: "world"}},
	}, &logged); code != http.StatusOK {
		t.Fatalf("log = %d", code)
	}
	if logged.Ack != 2 {
		t.Fatalf("log ack = %d, want 2", logged.Ack)
	}
	// A resent batch overlaps rather than duplicating: the ack does not move.
	if code := runnerCall(t, app, "log", cred, &wire.LogIn{
		Task: got.Task.ID, Index: 0,
		Lines: []wire.Line{{Time: nano, Content: "hello"}, {Time: nano + 1, Content: "world"}},
	}, &logged); code != http.StatusOK || logged.Ack != 2 {
		t.Fatalf("resent log = %d ack %d, want 200 ack 2", code, logged.Ack)
	}

	// A runner that does not hold the task cannot write its log.
	if code := runnerCall(t, app, "log",
		wire.Credential{UUID: second.Runner.UUID, Token: second.Runner.Token},
		&wire.LogIn{Task: got.Task.ID, Index: 2, Lines: []wire.Line{{Time: nano, Content: "x"}}},
		nil); code != http.StatusForbidden {
		t.Fatalf("log from the wrong runner = %d, want 403", code)
	}

	// state: the result comes back as the forge now holds it, and the instants
	// survive at nanosecond precision.
	var st wire.StateOut
	if code := runnerCall(t, app, "state", cred, &wire.StateIn{
		State: wire.State{ID: got.Task.ID, Result: wire.Success, Started: nano, Stopped: nano + 5,
			Steps: []wire.Step{{ID: 1, Result: wire.Success, Started: nano, Stopped: nano + 5, LogLength: 2}}},
		Outputs: []wire.Pair{{Name: "digest", Value: "sha256:abc"}},
	}, &st); code != http.StatusOK {
		t.Fatalf("state = %d", code)
	}
	if st.State.Result != wire.Success || st.State.ID != got.Task.ID {
		t.Fatalf("state answered %+v", st.State)
	}
	if len(st.Stored) != 1 || st.Stored[0] != "digest" {
		t.Fatalf("state stored = %v, want [digest]", st.Stored)
	}

	store, err := storeFor(mounted.Load(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Task(t.Context(), got.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Stopped != nano+5 {
		t.Fatalf("stopped = %d, want %d: the wire is unix NANOSECONDS and must not be rounded", task.Stopped, nano+5)
	}
	if task.Status != Success {
		t.Fatalf("task status = %q, want %q", task.Status, Success)
	}

	// The run the push opened is finished, and it is visible on the control
	// surface a person reads.
	code, body := do(t, app, http.MethodGet, "/v1/git/runs?repo=widgets", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list runs = %d %s", code, body)
	}
	var runs struct {
		Data []workflowRun `json:"data"`
	}
	if err := json.Unmarshal(body, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Data) != 1 || runs.Data[0].Status != Success || runs.Data[0].Commit != commit {
		t.Fatalf("runs = %+v", runs.Data)
	}

	// And the runners are visible, without any credential appearing.
	code, body = do(t, app, http.MethodGet, "/v1/git/runners", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list runners = %d %s", code, body)
	}
	if strings.Contains(string(body), reg.Runner.Token) || strings.Contains(string(body), secret) {
		t.Fatal("a credential appeared in the runner listing")
	}
	if !strings.Contains(string(body), "evo-1") {
		t.Fatalf("runner listing = %s", body)
	}
}

// TestSealedLogRefusesMoreLines holds the reference implementation's log
// semantics: a resent seal is acknowledged, and appending past one is a conflict.
func TestSealedLogRefusesMoreLines(t *testing.T) {
	app := mountApp(t)
	secret := declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
	var reg wire.RegisterOut
	runnerCall(t, app, "register", wire.Credential{}, &wire.RegisterIn{Name: "evo-1", Token: secret}, &reg)
	cred := wire.Credential{UUID: reg.Runner.UUID, Token: reg.Runner.Token}

	pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))
	drain(mounted.Load(), t.Context(), "acme")
	var got wire.TaskOut
	runnerCall(t, app, "task", cred, &wire.TaskIn{Queue: 0}, &got)
	if got.Task == nil {
		t.Fatal("no task leased")
	}

	var out wire.LogOut
	if code := runnerCall(t, app, "log", cred, &wire.LogIn{
		Task: got.Task.ID, Index: 0, Last: true,
		Lines: []wire.Line{{Time: 1, Content: "done"}},
	}, &out); code != http.StatusOK || out.Ack != 1 {
		t.Fatalf("seal = %d ack %d", code, out.Ack)
	}
	// Resending the seal is idempotent.
	if code := runnerCall(t, app, "log", cred, &wire.LogIn{
		Task: got.Task.ID, Index: 1, Last: true,
	}, &out); code != http.StatusOK || out.Ack != 1 {
		t.Fatalf("resent seal = %d ack %d", code, out.Ack)
	}
	// Appending past it is a conflict.
	if code := runnerCall(t, app, "log", cred, &wire.LogIn{
		Task: got.Task.ID, Index: 1, Lines: []wire.Line{{Time: 2, Content: "more"}},
	}, nil); code != http.StatusConflict {
		t.Fatalf("append past a seal = %d, want 409", code)
	}
}

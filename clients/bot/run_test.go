package bot

import "testing"

// The run family reports on machinery this cloud does not have, so the tests
// are of three kinds: that each answer is shaped the way the client reads it,
// that the parameters are as open or as closed as the protocol declares them,
// and that each method costs what the protocol prices it at — including that
// the methods with nothing behind them are not on the surface at all.

// runMethods is every method this family serves and what each costs. A client
// reads the names out of the handshake to decide which controls to offer; the
// cost decides who may use them.
var runMethods = map[string]Scope{
	"worktrees.list": Read,
	"worktrees.gc":   Admin,
}

// runGone is every method of this family's protocol surface that this gateway
// does not serve. Each names a worktree, a branch, a terminal session or a
// grant, and there is none, so each could only refuse.
var runGone = []string{
	"worktrees.restore", "worktrees.branches", "worktrees.create",
	"terminal.list", "terminal.close", "terminal.open",
	"exec.approval.grants.revoke",
}

// runMember is any member of the org, and runOperator is an admin of it.
// Reading worktrees is a member's; changing the set of them deletes and
// restores directories, so it is not.
var (
	runMember   = who{org: "acme"}
	runOperator = who{org: "acme", admin: true}
)

// runEmptyList reads one named field of a payload object and requires it to be
// an empty JSON array — present, and not a null the client would map over.
func runEmptyList(t *testing.T, frame map[string]any, field string) {
	t.Helper()
	got, ok := payload(t, frame)[field]
	if !ok {
		t.Fatalf("the answer carries no %s, and the client reads that field: %v", field, frame)
	}
	list, ok := got.([]any)
	if !ok {
		t.Fatalf("%s is %v, want a list", field, got)
	}
	if len(list) != 0 {
		t.Errorf("%s claims %v, and this gateway manages none", field, list)
	}
}

// ── worktrees ────────────────────────────────────────────────────────────────

// The page sorts what it gets straight off the answer and the session menu
// searches it for one id. Both need a list; neither survives a null or a
// missing field.
func TestRunWorktreeListIsAList(t *testing.T) {
	app := mount(t)
	for _, params := range []string{"", "{}"} {
		_, frame := ask(t, app, runMember, "1:a", "worktrees.list", params)
		if frame["ok"] != true {
			t.Fatalf("worktrees.list with %q refused: %v", params, frame)
		}
		runEmptyList(t, frame, "worktrees")
	}
}

// The parameters are closed upstream, so a field this gateway does not declare
// is a caller that has misunderstood the method.
func TestRunWorktreeListRefusesAParameterItDoesNotDeclare(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, runMember, "1:a", "worktrees.list", `{"includeRemoved":true}`)
	if frame["ok"] != false {
		t.Fatalf("a field the method does not declare was accepted: %v", frame)
	}
	if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
		t.Errorf("the refusal is %v, want INVALID_REQUEST", code)
	}
}

// A collection over an empty set removes nothing, and says so with three
// counts rather than with a refusal. The CLI prints all three.
func TestRunWorktreeSweepCollectsNothing(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, runOperator, "1:a", "worktrees.gc", `{}`)
	if frame["ok"] != true {
		t.Fatalf("worktrees.gc refused: %v", frame)
	}
	out := payload(t, frame)
	runEmptyList(t, frame, "removed")
	for _, field := range []string{"orphansDeleted", "snapshotsPruned"} {
		got, ok := out[field]
		if !ok {
			t.Errorf("the collection reports no %s", field)
			continue
		}
		if got != float64(0) {
			t.Errorf("%s is %v, and nothing was collected", field, got)
		}
	}
}

// The collection's parameters are closed too.
func TestRunWorktreeSweepRefusesAParameterItDoesNotDeclare(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, runOperator, "1:a", "worktrees.gc", `{"dryRun":true}`)
	if frame["ok"] != false {
		t.Fatalf("a field the collection does not declare was accepted: %v", frame)
	}
	if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
		t.Errorf("the refusal is %v, want INVALID_REQUEST", code)
	}
}

// ── who may call what ────────────────────────────────────────────────────────

// Collecting deletes directories, so it belongs to an admin of the org even
// though reading the same set does not. That difference is the only thing one
// request can see, and it is what pins the price of the pair.
func TestRunSweepIsAdminAndListIsNot(t *testing.T) {
	app := mount(t)

	_, frame := ask(t, app, runMember, "1:a", "worktrees.gc", `{}`)
	if frame["ok"] != false {
		t.Fatalf("a member collected worktrees: %v", frame)
	}
	details, _ := wrong(t, frame)["details"].(map[string]any)
	if details["missingScope"] != string(Admin) {
		t.Errorf("the refusal named %v as the missing capability, want %s", details["missingScope"], Admin)
	}

	if _, frame := ask(t, app, runMember, "2:a", "worktrees.list", `{}`); frame["ok"] != true {
		t.Errorf("a member could not read the worktrees: %v", frame)
	}
	if _, frame := ask(t, app, runOperator, "3:a", "worktrees.gc", `{}`); frame["ok"] != true {
		t.Errorf("an admin could not collect: %v", frame)
	}
}

// ── the advertised surface ───────────────────────────────────────────────────

// The handshake carries exactly what this family answers. A name that is
// present and can only refuse renders a control that fails when it is pressed;
// a name that is absent renders nothing, which is the honest state of a
// gateway with no working copy and no shell.
func TestRunFamilyIsAdvertisedExactly(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, runOperator, "1:a", "connect", "")
	features, _ := payload(t, frame)["features"].(map[string]any)
	names, ok := features["methods"].([]any)
	if !ok {
		t.Fatalf("the handshake advertises no method list: %v", features)
	}
	listed := map[string]bool{}
	for _, m := range names {
		listed[m.(string)] = true
	}
	for method := range runMethods {
		if !listed[method] {
			t.Errorf("%s is served but not advertised, so a client will not offer it", method)
		}
	}
	for _, method := range runGone {
		if listed[method] {
			t.Errorf("%s is advertised and cannot be answered", method)
		}
	}
	// terminal.data and terminal.exit carry one session's bytes. With no
	// session there are none, so neither is announced.
	for _, e := range features["events"].([]any) {
		if e == "terminal.data" || e == "terminal.exit" {
			t.Errorf("%v is announced, and nothing here emits it", e)
		}
	}
}

// What each method costs, and that the rest are not on the surface at all.
func TestRunCosts(t *testing.T) {
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	for method, need := range runMethods {
		m, ok := surface.methods[method]
		if !ok {
			t.Errorf("%s is not registered", method)
			continue
		}
		if m.need != need {
			t.Errorf("%s costs %s, want %s", method, m.need, need)
		}
	}
	for _, method := range runGone {
		if _, ok := surface.methods[method]; ok {
			t.Errorf("%s is registered, and nothing here can answer it", method)
		}
	}
}

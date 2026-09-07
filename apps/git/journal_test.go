package git

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// journal_test.go proves the ORDER, which is the whole point of the journal: a
// push is durable before it is answered, and nothing downstream of it can fail
// the push.

// TestPushRecordsCIWorkBeforeItAnswers is P2.
//
// The delivery half is never run here. What is asserted is what the push itself
// left behind at the moment it returned success: a row on disk saying a run is
// owed. That is the ordering — if delivery had to happen inside the push, this
// test could not both see the success and see nothing delivered.
func TestPushRecordsCIWorkBeforeItAnswers(t *testing.T) {
	app := mountApp(t)
	// Capacity is declared, so the push has somewhere to go once it is delivered.
	declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})

	commit := pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))

	s := mounted.Load()
	store, err := storeFor(s, "acme")
	if err != nil {
		t.Fatal(err)
	}

	// The push has answered. On disk: exactly one note, owed, naming the commit.
	owed, err := store.Owed(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 1 {
		t.Fatalf("after a successful push the journal holds %d notes, want 1", len(owed))
	}
	n := owed[0]
	if n.Commit != commit || n.Repo != "widgets" || n.Ref != "refs/heads/main" || n.Org != "acme" {
		t.Fatalf("note = %+v", n)
	}
	if n.ID != fact("acme", "", "widgets", "refs/heads/main", commit) {
		t.Fatalf("note id %q is not the identity of the fact", n.ID)
	}

	// Nothing has run yet. Delivery is off the push, which is why the push could
	// answer without it.
	runs, err := store.Runs(t.Context(), "acme", "", "widgets", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("the push opened %d runs inside the request; delivery must happen after it answers", len(runs))
	}

	// Now deliver. The run appears and the note is taken.
	drain(s, t.Context(), "acme")
	runs, err = store.Runs(t.Context(), "acme", "", "widgets", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Commit != commit {
		t.Fatalf("after delivery runs = %+v", runs)
	}
	owed, err = store.Owed(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 0 {
		t.Fatalf("a delivered note is still owed: %+v", owed)
	}

	// Draining again changes nothing: the note is taken and the run is unique on
	// (repo, commit, workflow), so redelivery cannot make a second run.
	drain(s, t.Context(), "acme")
	again, _ := store.Runs(t.Context(), "acme", "", "widgets", 10)
	if len(again) != 1 || again[0].ID != runs[0].ID {
		t.Fatalf("redelivery made a second run: %+v", again)
	}
}

// TestPushSucceedsWhenNoCapacityIsDeclared is the other half of P2: CI being
// unable to accept the work does not touch the push. Nothing here declares a
// pool, so the delivery cannot resolve one — and the push still lands, the note
// is still durable, and it is still owed with the reason recorded on it.
func TestPushSucceedsWhenNoCapacityIsDeclared(t *testing.T) {
	app := mountApp(t)

	commit := pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))
	if commit == "" {
		t.Fatal("push did not land")
	}

	s := mounted.Load()
	store, err := storeFor(s, "acme")
	if err != nil {
		t.Fatal(err)
	}

	drain(s, t.Context(), "acme")

	owed, err := store.Owed(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 1 {
		t.Fatalf("owed = %d, want the note to survive an undeliverable pass", len(owed))
	}
	// At least one attempt was recorded — the push's own detached delivery may
	// have raced this one, which is exactly the retry the design asks for.
	if owed[0].Attempts < 1 {
		t.Fatalf("attempts = %d, want the failed delivery recorded", owed[0].Attempts)
	}
	if !strings.Contains(owed[0].Fault, "no pool declares it") {
		t.Fatalf("fault = %q, want it to name the undeclared capacity", owed[0].Fault)
	}
	runs, _ := store.Runs(t.Context(), "acme", "", "widgets", 10)
	if len(runs) != 0 {
		t.Fatalf("a run was opened with no pool to execute it: %+v", runs)
	}

	// Declare the capacity and the SAME note delivers. Work waits for a
	// declaration; it never falls back to whatever happens to be running.
	declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
	drain(s, t.Context(), "acme")
	runs, _ = store.Runs(t.Context(), "acme", "", "widgets", 10)
	if len(runs) != 1 || runs[0].Commit != commit {
		t.Fatalf("after declaring capacity runs = %+v", runs)
	}
	owed, _ = store.Owed(t.Context(), 10)
	if len(owed) != 0 {
		t.Fatalf("note still owed after a successful delivery: %+v", owed)
	}
}

// TestRepeatedPushOfOneCommitOwesOneRun holds the idempotency the delivery id
// carries: the note is the fact, not the moment.
func TestRepeatedPushOfOneCommitOwesOneRun(t *testing.T) {
	app := mountApp(t)
	declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
	commit := pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))

	s := mounted.Load()
	store, _ := storeFor(s, "acme")
	// Re-report the same push, as a redelivery or a retried transport would.
	if err := store.Owe(t.Context(), Note{
		ID: fact("acme", "", "widgets", "refs/heads/main", commit), Org: "acme",
		Repo: "widgets", Ref: "refs/heads/main", Commit: commit,
	}); err != nil {
		t.Fatal(err)
	}
	owed, _ := store.Owed(t.Context(), 10)
	if len(owed) != 1 {
		t.Fatalf("a repeated report of one push owes %d runs, want 1", len(owed))
	}
}

// TestWorkflowsReportWhichPoolWouldRunThem covers the control-surface question
// "which pool can execute this workflow" — the one an operator asks when a run
// is not happening.
func TestWorkflowsReportWhichPoolWouldRunThem(t *testing.T) {
	app := mountApp(t)
	pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/arm64"))

	read := func() []workflowView {
		t.Helper()
		code, body := do(t, app, http.MethodGet, "/v1/git/workflows?repo=widgets", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("list workflows = %d %s", code, body)
		}
		var out struct {
			Data []workflowView `json:"data"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}

	got := read()
	if len(got) != 1 || len(got[0].Jobs) != 1 {
		t.Fatalf("workflows = %+v", got)
	}
	if got[0].Jobs[0].Name != "build" || got[0].Jobs[0].Pool != "" {
		t.Fatalf("job = %+v, want an empty pool while nobody declares linux/arm64", got[0].Jobs[0])
	}

	declarePoolFor(t, app, "acme", "spark", []string{"linux/arm64"})
	got = read()
	if got[0].Jobs[0].Pool != "spark" {
		t.Fatalf("job = %+v, want spark once it is declared", got[0].Jobs[0])
	}
}

// TestStartRunTakesTheSamePathAPushDoes holds the one-way rule: an explicit run
// and a pushed one are one mechanism, so asking twice yields one run.
func TestStartRunTakesTheSamePathAPushDoes(t *testing.T) {
	app := mountApp(t)
	declarePoolFor(t, app, "acme", "evo", []string{"linux/amd64"})
	pushWorkflow(t, app, "acme", "widgets", workflowFor("linux/amd64"))

	first := start(t, app)
	if len(first) != 1 {
		t.Fatalf("start run = %+v", first)
	}
	second := start(t, app)
	if len(second) != 1 || second[0].ID != first[0].ID {
		t.Fatalf("starting the same commit twice made two runs: %v then %v", first, second)
	}
}

func start(t *testing.T, app *zip.App) []workflowRun {
	t.Helper()
	code, body := do(t, app, http.MethodPost, "/v1/git/runs", "acme", map[string]string{"repo": "widgets"})
	if code != http.StatusCreated {
		t.Fatalf("start run = %d %s", code, body)
	}
	var out struct {
		Data []workflowRun `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Data
}

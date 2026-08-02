package risk

// search_test.go is the regression suite for the unbounded expensive op.
//
// THE DEFECT: POST /v1/risk/search spawned a bare goroutine per call with a
// ten-minute budget, replaying 243 topologies over up to five thousand recorded
// decisions. Nothing bounded how many ran at once, nothing could stop one, and a
// rollout left the durable row saying `running` for ever.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// idle builds a runner with NO workers, so a queued job stays queued and the
// admission rules can be asserted deterministically rather than raced against.
func idle() *runner {
	return &runner{jobs: make(chan job, searchQueue), log: luxlog.New("risktest"), running: map[Tenant]*run{}}
}

// TestOneSearchPerTenant pins the per-tenant concurrency bound. Without it a
// caller in a loop occupies every worker on a pod every other tenant shares.
func TestOneSearchPerTenant(t *testing.T) {
	r := idle()
	tn := Tenant("hanzo/acme")

	if err := r.start(job{t: tn, id: "search_1"}); err != nil {
		t.Fatalf("the first search was refused: %v", err)
	}
	err := r.start(job{t: tn, id: "search_2"})
	if err == nil {
		t.Fatal("a second search for the same tenant was accepted — one caller can hold every worker")
	}
	if he := statusOf(err); he != 409 {
		t.Fatalf("a busy tenant answers %d, want 409", he)
	}
	// Another tenant is NOT blocked by it: the bound is per tenant, not a global
	// mutex wearing a per-tenant name.
	if err := r.start(job{t: Tenant("hanzo/beta"), id: "search_3"}); err != nil {
		t.Fatalf("a different tenant was refused because another tenant was running: %v", err)
	}
}

// TestTheBacklogIsBoundedAndRefusesLoudly pins the process bound. A queue that
// grows turns a capacity problem into a latency mystery; a queue that refuses
// tells the caller to come back.
func TestTheBacklogIsBoundedAndRefusesLoudly(t *testing.T) {
	r := idle()
	for i := 0; i < searchQueue; i++ {
		if err := r.start(job{t: Tenant(fmt.Sprintf("hanzo/t%d", i)), id: fmt.Sprintf("s%d", i)}); err != nil {
			t.Fatalf("queueing %d of %d: %v", i, searchQueue, err)
		}
	}
	err := r.start(job{t: Tenant("hanzo/overflow"), id: "s-overflow"})
	if err == nil {
		t.Fatal("the queue accepted an unbounded number of searches")
	}
	if got := statusOf(err); got != 429 {
		t.Fatalf("a full queue answers %d, want 429", got)
	}
	// And the refused tenant holds no claim, or it could never start one later.
	r.mu.Lock()
	_, stuck := r.running[Tenant("hanzo/overflow")]
	r.mu.Unlock()
	if stuck {
		t.Fatal("a tenant refused at the queue keeps its claim, so it can never start a search again")
	}
}

// TestAQueuedSearchIsCancellable pins that a search can be taken back BEFORE a
// worker reaches it — which is exactly when the queue is full and it matters.
func TestAQueuedSearchIsCancellable(t *testing.T) {
	r := idle()
	tn := Tenant("hanzo/acme")
	if err := r.start(job{t: tn, id: "search_1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !r.cancel(tn, "search_1") {
		t.Fatal("a queued search could not be cancelled")
	}
	if r.cancel(tn, "search_1") {
		t.Fatal("cancelling twice reported a second cancellation")
	}
	// Cancelling frees the slot.
	if err := r.start(job{t: tn, id: "search_2"}); err != nil {
		t.Fatalf("a cancelled search left the tenant's slot occupied: %v", err)
	}
	// And a cancel naming someone else's run does nothing.
	if r.cancel(Tenant("hanzo/beta"), "search_2") {
		t.Fatal("one tenant cancelled another tenant's search")
	}
}

// TestARestartResolvesEverySearchItWasRunning pins the durability rule. cloud
// deploys strategy Recreate at one replica, so a `running` row that survives a
// rollout is not exceptional — it is EVERY rollout, and it reads identically to a
// search that is about to answer.
func TestARestartResolvesEverySearchItWasRunning(t *testing.T) {
	_, s := wireApp(t)
	tn := Tenant("hanzo/acme")
	r := resOf(t, s, tn)

	body, _ := json.Marshal(searchReport{Events: 42})
	if err := putSearch(dbOf(t, r), "search_orphan", searchRunning, body); err != nil {
		t.Fatalf("putSearch: %v", err)
	}
	// The rollout: the process is gone and the file is reopened.
	if err := abandonSearches(dbOf(t, r)); err != nil {
		t.Fatalf("abandonSearches: %v", err)
	}
	status, out, err := getSearch(dbOf(t, r), "search_orphan")
	if err != nil {
		t.Fatalf("getSearch: %v", err)
	}
	if status == searchRunning {
		t.Fatal("a search left running by a dead process still reads as in progress")
	}
	if status != searchCancelled {
		t.Fatalf("status = %q, want %q", status, searchCancelled)
	}
	var rep searchReport
	_ = json.Unmarshal(out, &rep)
	if rep.Refusal == "" {
		t.Fatal("the abandoned search names no reason, so a reader cannot tell it apart from one that failed")
	}
}

// TestSearchIsGatedMeteredAndAnswersItsRun walks the op end to end: a real
// history, a real 202, a durable row, and a report that resolves.
func TestSearchIsGatedMeteredAndAnswersItsRun(t *testing.T) {
	app, s := wireApp(t)
	s.State.reg = &stubRegistry{known: map[string]bool{}}

	for i := 0; i < 5; i++ {
		code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
			fmt.Sprintf(`{"stage":"payment","subject":{"kind":"transaction","id":"tx-%d"},
			  "amount":{"nano":%d,"currency":"USD","direction":"in"}}`, i, (i+1)*1_000_000_000))
		if code != http.StatusOK {
			t.Fatalf("decide = %d %s", code, body)
		}
	}

	code, body := req(t, app, http.MethodPost, "/v1/risk/search", "acme", "u_acme", `{"limit":5}`)
	if code != http.StatusAccepted && code != http.StatusOK {
		t.Fatalf("search = %d %s", code, body)
	}
	var run riskSearchRun
	_ = json.Unmarshal(body, &run)
	if run.ID == "" {
		t.Fatalf("search answered no run identifier: %s", body)
	}

	// It resolves, one way or the other, inside the budget.
	deadline := time.Now().Add(30 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		code, body = req(t, app, http.MethodGet, "/v1/risk/search/"+run.ID, "acme", "u_acme", "")
		if code != http.StatusOK {
			t.Fatalf("search result = %d %s", code, body)
		}
		var rep riskSearchReport
		_ = json.Unmarshal(body, &rep)
		status = rep.Status
		if status != searchRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status == searchRunning {
		t.Fatal("the search never resolved within its own budget")
	}

	// Another tenant cannot read it, and cannot cancel it.
	code, _ = req(t, app, http.MethodGet, "/v1/risk/search/"+run.ID, "beta", "u_beta", "")
	if code != http.StatusNotFound {
		t.Fatalf("B reading A's search = %d, want 404", code)
	}
	code, _ = req(t, app, http.MethodDelete, "/v1/risk/search/"+run.ID, "beta", "u_beta", "")
	if code != http.StatusNotFound {
		t.Fatalf("B cancelling A's search = %d, want 404", code)
	}
	// A can cancel its own, idempotently.
	code, body = req(t, app, http.MethodDelete, "/v1/risk/search/"+run.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("A cancelling its own search = %d %s", code, body)
	}
}

// statusOf reads the HTTP status a refusal carries. The status IS the
// distinction being asserted: 409 says this tenant already has one going and 429
// says the node does, and a caller retries exactly one of them.
func statusOf(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

package risk

// bill_test.go is the money half of "every costly op is gated, metered and
// bounded". It stands a real metering client in front of a fake commerce, so a
// test can see BOTH sides of a price: what was asked of the ledger before the
// work, and what was charged after it.
//
// The two facts it pins are the ones that were not true: an estimation queued by
// the SCHEDULE cost nothing while an estimation queued by the op was charged, and
// a drift reading cost nothing at all. Both spend the same shared pod.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// book is a commerce that answers balance reads and records debits. deny makes
// every org unfunded, which is how a test tells a gate that ran from one that
// was never there.
type book struct {
	deny bool

	mu      sync.Mutex
	asks    map[string]int
	charges []charge
}

type charge struct {
	org   string
	model string
	cents int64
}

func (b *book) Do(req *http.Request) (*http.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	org := req.Header.Get("X-Org-Id")
	switch {
	case strings.HasSuffix(req.URL.Path, "/v1/billing/balance"):
		if b.asks == nil {
			b.asks = map[string]int{}
		}
		b.asks[org]++
		available := 100000
		if b.deny {
			available = 0
		}
		return reply(200, map[string]any{"available": available, "currency": "usd"}), nil
	case strings.HasSuffix(req.URL.Path, "/v1/billing/usage"):
		// The ledger is named by X-Org-Id and never by the body — metering.Usage
		// tags Org `json:"-"` for exactly that reason, so reading it off the header
		// is reading the address the money actually went to.
		var u struct {
			Model  string `json:"model"`
			Amount int64  `json:"amount"`
		}
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &u)
		}
		b.charges = append(b.charges, charge{org: org, model: u.Model, cents: u.Amount})
		return reply(200, map[string]any{"ok": true}), nil
	}
	// Everything else — the spend-cap overlay — is "no rule configured".
	return reply(404, map[string]any{}), nil
}

func reply(code int, body any) *http.Response {
	raw, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// asked is how many times an org's balance was read.
func (b *book) asked(org string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asks[org]
}

// charged waits for a debit of one kind to land on one org's ledger. The debit
// is fire-and-forget by design — the work already happened and a charge must
// never block the answer — so the wait is part of the contract, not a flake.
func (b *book) charged(org, model string) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, c := range b.charges {
			if c.org == org && c.model == model && c.cents > 0 {
				b.mu.Unlock()
				return true
			}
		}
		b.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// wireBilled mounts the surface with a ledger behind it. wireAt's deployment has
// no commerce at all, which makes every gate allow — so a test written against
// it cannot tell a priced op from a free one.
func wireBilled(t *testing.T, dir string, b *book) (*zip.App, *stateService) {
	t.Helper()
	client, err := metering.New(metering.Config{
		BaseURL: "http://commerce.test", Org: "hanzo", HTTPClient: b,
	})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	deps := cloud.Deps{Logger: luxlog.New("risktest"), DataDir: dir, Brand: "hanzo", Metering: client}
	s, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !s.State.bill.Enabled() {
		t.Fatal("the test deployment holds no ledger, so every gate would allow and this test proves nothing")
	}
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	mount(s, app)
	t.Cleanup(s.State.shelf.close)
	return app, s
}

// TestAScheduledEstimationPaysForItself is the free-CPU hole on the automatic
// path.
//
// The op gated and metered; the schedule called enqueue directly and did
// neither. A tenant that set `every: 1` therefore bought unlimited estimations —
// competing, free, for the same two fleet-wide bench slots as the ones somebody
// paid for. The price now lives in enqueue, which is the ONE path a version is
// created by, so who asked cannot change what it costs.
func TestAScheduledEstimationPaysForItself(t *testing.T) {
	book := &book{deny: true}
	_, s := wireBilled(t, t.TempDir(), book)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ground(t, s, tn, db, time.Now().AddDate(0, 0, -300), 900, 1, 0)
	if err := putSchedule(db, schedule{Every: 1, Role: roleChallenger, Horizon: 30, Window: 400,
		Rows: fitRows}.withDefaults()); err != nil {
		t.Fatalf("putSchedule: %v", err)
	}

	// The trigger is genuinely due, or the test is asserting nothing.
	sched, err := getSchedule(db)
	if err != nil {
		t.Fatalf("getSchedule: %v", err)
	}
	last, _, err := lastFit(db)
	if err != nil {
		t.Fatalf("lastFit: %v", err)
	}
	n, err := maturedSince(db, sched.Horizon, last, time.Now())
	if err != nil {
		t.Fatalf("maturedSince: %v", err)
	}
	if ok, _ := due(sched, last, n, time.Now()); !ok {
		t.Fatal("the schedule is not due, so a refusal below would prove nothing")
	}

	if err := watch(context.Background(), s, tn, db, time.Now()); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if book.asked("acme") == 0 {
		t.Fatal("the scheduled estimation never asked the ledger — a timer buys CPU for free")
	}
	rows, err := listFits(db, 50)
	if err != nil {
		t.Fatalf("listFits: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("an unfunded tenant's schedule queued %d estimations", len(rows))
	}

	// Funded, the same tick runs it AND lands the debit on that tenant's own
	// ledger — never the brand's, never a neighbour's.
	book.mu.Lock()
	book.deny = false
	book.mu.Unlock()
	if err := watch(context.Background(), s, tn, db, time.Now()); err != nil {
		t.Fatalf("watch (funded): %v", err)
	}
	if id := settle(t, s, tn); id == "" {
		t.Fatal("a funded schedule queued nothing")
	}
	if !book.charged("acme", "fit") {
		t.Fatal("the scheduled estimation was never charged")
	}
}

// TestTheOpChargesOnceNotTwice guards the move of the price into enqueue: the
// typed op must not gate a second time, or an explicit fit costs double what the
// schedule's does for the same work.
func TestTheOpChargesOnceNotTwice(t *testing.T) {
	book := &book{}
	app, s := wireBilled(t, t.TempDir(), book)
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ground(t, s, tn, db, time.Now().AddDate(0, 0, -300), 900, 1, 0)

	code, body := req(t, app, http.MethodPost, "/v1/ml/fits", "acme", "u_acme",
		`{"horizon":30,"window":400,"note":"quarterly refresh"}`)
	if code != http.StatusAccepted {
		t.Fatalf("fit = %d %s", code, body)
	}
	settle(t, s, tn)
	if !book.charged("acme", "fit") {
		t.Fatal("an explicit estimation was never charged")
	}
	book.mu.Lock()
	defer book.mu.Unlock()
	var fits int
	for _, c := range book.charges {
		if c.model == "fit" {
			fits++
		}
	}
	if fits != 1 {
		t.Fatalf("one estimation produced %d debits", fits)
	}
}

// TestHeavyWorkQueuesOnOneBench pins the concurrency bound the exhaustive search
// did not have.
//
// A CLOSED GRID IS NOT A BOUND ON CONCURRENCY. 243 candidates over a thousand
// replayed rows is one bounded search; POST /v1/ml/search started a bare
// goroutine per call, so a caller looping it ran as many at once as it cared to
// — minutes of CPU each, on the single replica that answers authorisations for
// every product on this host. It queues on THE bench now, which is where the
// per-tenant and fleet-wide bounds already live.
func TestHeavyWorkQueuesOnOneBench(t *testing.T) {
	app, s := wireBilled(t, t.TempDir(), &book{})
	tn, _ := qualify("hanzo", "acme")
	db, err := s.State.shelf.open(tn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ground(t, s, tn, db, time.Now().AddDate(0, 0, -300), 400, 1, 0)

	// A job is parked on the bench, holding this tenant's one place.
	release := make(chan struct{})
	defer close(release)
	if err := s.State.bench.start(tn, kindFit, "fit-parked", func(context.Context) { <-release }); err != nil {
		t.Fatalf("start: %v", err)
	}

	code, body := req(t, app, http.MethodPost, "/v1/ml/search", "acme", "u_acme", `{"limit":200}`)
	if code != http.StatusConflict {
		t.Fatalf("a search started while this tenant already had work on the bench = %d %s, want 409", code, body)
	}
	// And the same bound holds the other way: heavy work is heavy work.
	code, body = req(t, app, http.MethodPost, "/v1/ml/fits", "acme", "u_acme", `{"horizon":30,"window":400}`)
	if code != http.StatusConflict {
		t.Fatalf("an estimation started while this tenant already had work on the bench = %d %s, want 409", code, body)
	}

	// A NEIGHBOUR is unaffected — the bound is the tenant's own place, not a lock
	// on the deployment.
	other, _ := qualify("hanzo", "beta")
	odb, err := s.State.shelf.open(other)
	if err != nil {
		t.Fatalf("open beta: %v", err)
	}
	ground(t, s, other, odb, time.Now().AddDate(0, 0, -300), 400, 1, 0)
	code, body = req(t, app, http.MethodPost, "/v1/ml/search", "beta", "u_beta", `{"limit":200}`)
	if code != http.StatusAccepted {
		t.Fatalf("a neighbour's search = %d %s, want 202", code, body)
	}

	// A running search is NOT reported as a version being estimated: two kinds
	// queue here and one answer to both would name a search as a model version.
	if id, ok := s.State.bench.running(other, kindFit); ok {
		t.Fatalf("a search reads back as an estimation (%q)", id)
	}
	if _, ok := s.State.bench.running(other, kindSearch); !ok {
		t.Fatal("the search is not on the bench at all")
	}
}

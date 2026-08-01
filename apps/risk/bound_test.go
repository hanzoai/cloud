package risk

// bound_test.go — every way this surface can be made to do work, and what stops
// it being free.
//
// Two defects live here and both are the same shape: an authenticated caller
// reaching expensive per-tenant state with nothing between them and it.
//
//   - AN UNBOUNDED TIMESTAMP. `at` was parsed and believed. One event in the
//     future moves the aggregates' leading edge there and the subject's real
//     behaviour reads as nothing from then on.
//   - AN UNGATED, UNMETERED OP. score and learn were free: no balance check, no
//     debit, no concurrency bound — per-tenant model work and a per-tenant disk
//     write, in a loop, for nothing. Free unbounded compute is a denial of
//     service and lost revenue at once.

import (
	"context"
	"encoding/json"
	"fmt"
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

// ── the timestamp ────────────────────────────────────────────────────────────

// TestEvent_RefusesAnUnbelievableTimestamp is the wire door's half of the bound:
// a stamp outside the window is a 400 with a reason, never a silent adjustment.
//
// Mutation proof: delete the `within` call in mlEvent.observation and the future
// and ancient rows below stop failing.
func TestEvent_RefusesAnUnbelievableTimestamp(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   string
		ok   bool
	}{
		{"empty means now", "", true},
		{"a minute ago", now.Add(-time.Minute).Format(time.RFC3339), true},
		{"inside the clock skew", now.Add(time.Minute).Format(time.RFC3339), true},
		{"the far edge of the window", now.Add(-ringWindow + time.Hour).Format(time.RFC3339), true},
		{"an hour ahead", now.Add(time.Hour).Format(time.RFC3339), false},
		{"a century ahead", now.AddDate(100, 0, 0).Format(time.RFC3339), false},
		{"just past the window", now.Add(-ringWindow - time.Hour).Format(time.RFC3339), false},
		{"the unix epoch", time.Unix(0, 0).UTC().Format(time.RFC3339), false},
		{"not a timestamp at all", "yesterday", false},
	} {
		_, err := mlEvent{Kind: kindAccount, Subject: "u_1", At: tc.at}.observation(now)
		if tc.ok && err != nil {
			t.Errorf("%s (%q): %v, want accepted", tc.name, tc.at, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s (%q) was accepted — an unbelievable stamp must be refused, not adjusted", tc.name, tc.at)
		}
	}
}

// TestLearn_RefusesAFutureStampOnTheWire is the same bound where a caller meets
// it, and it checks the STATUS as well as the refusal: this is a client error and
// must not read as a server one.
func TestLearn_RefusesAFutureStampOnTheWire(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	ahead := time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339)
	body := `{"events":[{"id":"e1","kind":"account","subject":"u_1","at":"` + ahead + `"}]}`
	code, out := req(t, app, http.MethodPost, "/v1/ml/learn", orgA, "u_"+orgA, body)
	if code != http.StatusBadRequest {
		t.Fatalf("POST /v1/ml/learn with a stamp a year ahead = %d %s, want 400", code, out)
	}
	if !strings.Contains(string(out), "future") {
		t.Errorf("the refusal does not say what was wrong: %s", out)
	}
}

// TestObservation_TruncatesToTheSecond: the aggregates are durable at one-second
// resolution and the finest ring bucket is a minute, so truncating at the door is
// what makes a rebuild from the record IDENTICAL to the live rings rather than
// merely close.
func TestObservation_TruncatesToTheSecond(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 123456789, time.UTC)
	o, err := mlEvent{Kind: kindAccount, Subject: "u_1"}.observation(now)
	if err != nil {
		t.Fatalf("observation: %v", err)
	}
	if o.At.Nanosecond() != 0 {
		t.Fatalf("the observation kept sub-second precision (%s) the durable record cannot carry", o.At)
	}
}

// ── the bounds ───────────────────────────────────────────────────────────────

// TestPlane_TheBoundsAreDerivedAndPerTenant holds the arithmetic the memory
// argument rests on. A future change that "fixes" pressure by shrinking the
// per-tenant bound to uselessness, or by letting the process ceiling grow without
// saying so, fails here rather than in production.
func TestPlane_TheBoundsAreDerivedAndPerTenant(t *testing.T) {
	if residentKeys != residentRingBudget/perKeyBytes {
		t.Fatalf("the per-tenant key bound (%d) is no longer derived from the per-tenant byte budget", residentKeys)
	}
	if residentKeys < 256 {
		t.Fatalf("the per-tenant bound is %d subjects — too few to hold an ordinary organisation's own traffic, "+
			"which turns a memory budget into a detector that is off", residentKeys)
	}
	if planeRingCeiling != maxResident*residentRingBudget {
		t.Fatal("the process ceiling is no longer the product of the per-tenant budget and the resident bound")
	}
	if planeRingCeiling > 1<<30 {
		t.Fatalf("the aggregates may hold %d MiB, which is more of the deployment's 9 GiB than this app should take",
			planeRingCeiling>>20)
	}
}

// TestLearn_RefusesAnOversizeBatch: a batch is a loop over one tenant's model
// lock and one transaction on its own store, so it is bounded and the refusal
// names the bound.
func TestLearn_RefusesAnOversizeBatch(t *testing.T) {
	probe.reset(true)
	app := mountApp(t)
	var b strings.Builder
	b.WriteString(`{"events":[`)
	for i := 0; i <= maxBatch; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"e%d","kind":"account","subject":"u_%d"}`, i, i)
	}
	b.WriteString(`]}`)
	code, out := req(t, app, http.MethodPost, "/v1/ml/learn", orgA, "u_"+orgA, b.String())
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /v1/ml/learn with %d events = %d %s, want 413", maxBatch+1, code, out)
	}
}

// TestPlane_BoundsOneTenantsConcurrency: every call for one organisation
// serialises on that organisation's model lock, so an unbounded number of them is
// its own concurrency turned into a queue of goroutines. The bound is per tenant,
// so a tenant at it slows only itself.
func TestPlane_BoundsOneTenantsConcurrency(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	for i := 0; i < maxInFlight; i++ {
		if err := p.enter(a); err != nil {
			t.Fatalf("slot %d of %d refused: %v", i, maxInFlight, err)
		}
	}
	if err := p.enter(a); err == nil {
		t.Fatalf("a %d+1st concurrent call was admitted — the bound is not applied", maxInFlight)
	}
	// ...and the OTHER organisation is untouched, which is the whole point.
	if err := p.enter(b); err != nil {
		t.Fatalf("one organisation at its own bound refused another organisation's call: %v", err)
	}
	p.leave(b)
	for i := 0; i < maxInFlight; i++ {
		p.leave(a)
	}
	if err := p.enter(a); err != nil {
		t.Fatalf("the slots were not released: %v", err)
	}
	p.leave(a)
	p.mu.Lock()
	held := len(p.busy)
	p.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d tenant(s) left in the in-flight map — it leaks a key per tenant that ever called", held)
	}
}

// ── the money ────────────────────────────────────────────────────────────────

// TestScoreAndLearn_AreGatedOnTheCallersOwnBalance is the defect stated as an
// experiment: with the caller's ledger empty, the two ops an abuser would call in
// a loop must refuse — in the fleet's own money contract, and BEFORE any model
// work happens.
//
// Mutation proof: delete the o.gate call from either op and that op answers 200
// against a zero balance.
func TestScoreAndLearn_AreGatedOnTheCallersOwnBalance(t *testing.T) {
	probe.reset(true)
	books := &ledger{available: 0}
	app := mountBilled(t, books)
	for _, tc := range []struct{ path, body string }{
		{"/v1/ml/score", `{"event":{"id":"e1","kind":"account","subject":"u_1"}}`},
		{"/v1/ml/learn", `{"events":[{"id":"e1","kind":"account","subject":"u_1"}]}`},
	} {
		code, out := req(t, app, http.MethodPost, tc.path, orgA, "u_"+orgA, tc.body)
		if code != http.StatusPaymentRequired {
			t.Fatalf("POST %s on an empty balance = %d %s, want 402", tc.path, code, out)
		}
		// The fleet's nested money contract, not a second vocabulary for it.
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(out, &body); err != nil || body.Error.Code != "insufficient_balance" {
			t.Fatalf("POST %s refused with %s, want the fleet's {\"error\":{\"code\":\"insufficient_balance\"}}", tc.path, out)
		}
	}
	if books.debits() != 0 {
		t.Fatalf("a refused request still debited %d time(s)", books.debits())
	}
}

// TestScoreAndLearn_MeterOneScreenPerEvent: the billable unit is a screen — one
// event judged against an organisation's own model — so a batch of N is N, on the
// CALLER's ledger and no other.
//
// Mutation proof: drop the pay(...) call in either op, or meter a flat fee
// instead of per event, and the amounts below stop matching.
func TestScoreAndLearn_MeterOneScreenPerEvent(t *testing.T) {
	probe.reset(true)
	books := &ledger{available: 1_000_000}
	app := mountBilled(t, books)

	code, out := req(t, app, http.MethodPost, "/v1/ml/score", orgA, "u_"+orgA,
		`{"event":{"id":"e1","kind":"account","subject":"u_1"}}`)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/ml/score = %d %s", code, out)
	}
	const batch = 7
	var b strings.Builder
	b.WriteString(`{"events":[`)
	for i := 0; i < batch; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"e%d","kind":"account","subject":"u_%d"}`, i, i)
	}
	b.WriteString(`]}`)
	if code, out := req(t, app, http.MethodPost, "/v1/ml/learn", orgA, "u_"+orgA, b.String()); code != http.StatusOK {
		t.Fatalf("POST /v1/ml/learn = %d %s", code, out)
	}

	posted := books.await(t, 2)
	var screens int64
	for _, u := range posted {
		if u.Org != orgA || u.User != orgA {
			t.Fatalf("a debit landed on %q/%q rather than the caller's own ledger", u.Org, u.User)
		}
		screens += u.Micros / defaultScreenUUSD
	}
	if screens != 1+batch {
		t.Fatalf("the two calls metered %d screens, want %d (one score + %d learned events)", screens, 1+batch, batch)
	}
}

// TestSearch_IsPricedFromItsMeasuredSize: a search is every candidate over every
// event of the replayed history, so pricing it as one flat unit prices the
// largest operation on this surface as the cheapest. The gate therefore runs
// where the size is known — after the history is measured, before the first tree
// is planted.
//
// It is proved as a BRACKET rather than by watching the call: a balance one cent
// under the measured cost refuses, and exactly the measured cost admits. Nothing
// but the real number satisfies both.
func TestSearch_IsPricedFromItsMeasuredSize(t *testing.T) {
	const events = 20
	want := cloud.MicrosToGateCents(screenMicros(events * len(candidates())))
	if want < 2 {
		t.Fatalf("a %d-event search costs %d cents, which is too coarse for this bracket to mean anything", events, want)
	}
	hold := func(k tenant) {
		now := time.Now().UTC()
		for i := 0; i < events; i++ {
			probe.hold(string(k), map[string]any{
				"subject_kind": kindAccount, "subject": "u_" + itoa(i%4),
				"bucket": now.Add(-time.Duration(i) * time.Minute),
				"events": uint32(2), "spend_nano": int64(150_000_000),
			})
		}
	}

	probe.reset(true)
	hold(key(t, brandA, orgA))
	poor := mountBilled(t, &ledger{available: want - 1})
	if code, out := req(t, poor, http.MethodPost, "/v1/ml/search", orgA, "u_"+orgA, `{"days":1}`); code != http.StatusPaymentRequired {
		t.Fatalf("a %d-event search on a balance of %d cents = %d %s, want 402 — it costs %d",
			events, want-1, code, out, want)
	}

	probe.reset(true)
	hold(key(t, brandA, orgA))
	rich := mountBilled(t, &ledger{available: want})
	code, out := req(t, rich, http.MethodPost, "/v1/ml/search", orgA, "u_"+orgA, `{"days":1}`)
	if code != http.StatusAccepted {
		t.Fatalf("a %d-event search on a balance of exactly %d cents = %d %s, want 202 — the gate is asking for more than the work costs",
			events, want, code, out)
	}
	var run mlSearchRun
	if err := json.Unmarshal(out, &run); err != nil || run.Events != events {
		t.Fatalf("the accepted run replays %d events, want %d (%s)", run.Events, events, out)
	}
}

// TestGate_FailsClosed: a ledger that cannot be reached refuses. Free work on a
// billing outage is the same defect as free work with no gate, discovered later.
func TestGate_FailsClosed(t *testing.T) {
	probe.reset(true)
	books := &ledger{down: true}
	app := mountBilled(t, books)
	code, out := req(t, app, http.MethodPost, "/v1/ml/score", orgA, "u_"+orgA,
		`{"event":{"id":"e1","kind":"account","subject":"u_1"}}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/ml/score with the ledger unreachable = %d %s, want 503", code, out)
	}
}

// TestScreenPrice_IsAConfigurablePolicyDefault: the price is a named operator
// knob with a stated default, and a typo can never silently zero out billing.
func TestScreenPrice_IsAConfigurablePolicyDefault(t *testing.T) {
	if got := screenRate(); got != defaultScreenUUSD {
		t.Fatalf("the unset price is %d, want the stated default %d", got, defaultScreenUUSD)
	}
	for _, bad := range []string{"-1", "not a number"} {
		t.Setenv("CLOUD_RISK_PRICE_UUSD_PER_SCREEN", bad)
		if got := screenRate(); got != defaultScreenUUSD {
			t.Fatalf("%q resolved to %d — an unusable override must fall through to the default, never to free", bad, got)
		}
	}
	t.Setenv("CLOUD_RISK_PRICE_UUSD_PER_SCREEN", "250")
	if got := screenMicros(4); got != 1000 {
		t.Fatalf("4 screens at 250 uUSD = %d, want 1000", got)
	}
	t.Setenv("CLOUD_RISK_PRICE_UUSD_PER_SCREEN", "0")
	if got := screenMicros(4); got != 0 {
		t.Fatalf("an explicit zero price charged %d — zero must mean free, and therefore un-gated", got)
	}
}

// ── a ledger to bill against ─────────────────────────────────────────────────

// ledger is commerce, as much of it as the money contract touches: a balance to
// authorize against and a place the debits land. It is an HTTP doer rather than a
// stubbed interface so the REAL metering client runs — the thing being tested is
// that this surface asks and pays, and a fake client would answer for it.
type ledger struct {
	mu        sync.Mutex
	available int64 // cents
	down      bool
	posted    []debit
}

// debit is one recorded charge: who paid and how much.
type debit struct {
	Org, User string
	Micros    int64
}

func (l *ledger) Do(r *http.Request) (*http.Response, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return nil, fmt.Errorf("commerce unreachable")
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/billing/balance"):
		return reply(http.StatusOK, fmt.Sprintf(`{"available":%d,"balance":%d,"currency":"usd"}`, l.available, l.available)), nil
	case strings.HasSuffix(r.URL.Path, "/v1/billing/usage"):
		// The field names are commerce's own wire: `amount` is cents, `amountMicros`
		// the sub-cent form a per-screen price needs.
		var u struct {
			User         string `json:"user"`
			AmountMicros int64  `json:"amountMicros"`
			AmountCents  int64  `json:"amount"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &u)
		micros := u.AmountMicros
		if micros == 0 {
			micros = u.AmountCents * 10_000
		}
		l.posted = append(l.posted, debit{Org: r.Header.Get("X-Org-Id"), User: u.User, Micros: micros})
		return reply(http.StatusOK, `{}`), nil
	default:
		// Spend caps and anything else this surface does not use: absent, which the
		// client reads as "no cap configured".
		return reply(http.StatusNotFound, `{}`), nil
	}
}

func (l *ledger) debits() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.posted)
}

// await waits for n debits. They are posted on a background context by design —
// a debit must not add latency to a decision — so a test that read immediately
// would be measuring the scheduler.
func (l *ledger) await(t *testing.T, n int) []debit {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		got := append([]debit(nil), l.posted...)
		l.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d of %d debits reached the ledger", l.debits(), n)
	return nil
}

func reply(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// mountBilled mounts risk with a REAL metering client pointed at books.
func mountBilled(t *testing.T, books *ledger) *zip.App {
	t.Helper()
	client, err := metering.New(metering.Config{BaseURL: "http://commerce.test", Token: "t", Org: brandA, HTTPClient: books})
	if err != nil {
		t.Fatalf("metering client: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.New("risktest"), Brand: brandA, DataDir: t.TempDir(), Metering: client}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// TestFold_IsBoundedAndRetriedRatherThanForgotten: a fold rolls up to four source
// planes and reads a window of the warehouse, so one goroutine per residency is
// unbounded fan-out at the datastore — a thousand tenants arriving after a
// rollout is a thousand of those at once, which is this app doing to the
// warehouse what a shared ring store used to do to tenants.
//
// The bound is on CONCURRENCY, never on state, so a tenant that finds no ticket
// loses nothing: no sentinel is written, the state says so, and the NEXT touch
// folds. Deferred and reported, never dropped and silent.
//
// Mutation proof: start the goroutine unconditionally and the first assertion
// fails; write the sentinel on a deferral and the last one does.
func TestFold_IsBoundedAndRetriedRatherThanForgotten(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	k := key(t, brandA, orgA)

	for i := 0; i < maxFolds; i++ { // every ticket taken, as other tenants would
		p.folds <- struct{}{}
	}
	if _, err := p.resident(k); err != nil {
		t.Fatalf("resident: %v", err)
	}
	p.mu.Lock()
	_, attempted := p.folded[k]
	p.mu.Unlock()
	if attempted {
		t.Fatal("a fold started with every ticket taken — the fan-out at the warehouse is unbounded")
	}
	if gap := p.surface(k).Gap; gap == "" {
		t.Fatal("a tenant whose fold was deferred reports no gap — a zero fold with no reason is the silence this app refuses")
	}

	for i := 0; i < maxFolds; i++ { // the other tenants finish
		<-p.folds
	}
	if _, err := p.resident(k); err != nil {
		t.Fatalf("resident: %v", err)
	}
	p.mu.Lock()
	_, attempted = p.folded[k]
	p.mu.Unlock()
	if !attempted {
		t.Fatal("the deferred fold was never retried — the tenant stays unfolded for as long as it is resident, " +
			"which is the moat quietly not applying to it")
	}
	// And the ticket comes back, or the bound closes over the process one fold at
	// a time until nothing folds at all.
	if err := p.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if held := len(p.folds); held != 0 {
		t.Fatalf("%d fold ticket(s) never returned", held)
	}
}

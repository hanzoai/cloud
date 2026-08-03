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
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── the timestamp ────────────────────────────────────────────────────────────

// TestEvent_RefusesAnUnbelievableTimestamp is the wire door's half of the bound:
// a stamp outside the window is a 400 with a reason, never a silent adjustment.
//
// Mutation proof: delete the `within` call in riskEvent.observation and the future
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
		_, err := riskEvent{Kind: kindAccount, Subject: "u_1", At: tc.at}.observation(now)
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
	code, out := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA, body)
	if code != http.StatusBadRequest {
		t.Fatalf("POST /v1/risk/learn with a stamp a year ahead = %d %s, want 400", code, out)
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
	o, err := riskEvent{Kind: kindAccount, Subject: "u_1"}.observation(now)
	if err != nil {
		t.Fatalf("observation: %v", err)
	}
	if o.at.Nanosecond() != 0 {
		t.Fatalf("the observation kept sub-second precision (%s) the durable record cannot carry", o.at)
	}
}

// ── the bounds ───────────────────────────────────────────────────────────────

// TestPlane_TheBoundsAreDerivedAndPerTenant holds the arithmetic the memory
// argument rests on. A future change that "fixes" pressure by shrinking the
// per-tenant bound to uselessness, or by letting the process ceiling grow without
// saying so, fails here rather than in production.
func TestPlane_TheBoundsAreDerivedAndPerTenant(t *testing.T) {
	if ringKeys != residentRingBudget/perSubjectBytes-shards {
		t.Fatalf("the per-tenant key bound (%d) is no longer derived from the per-tenant byte budget", ringKeys)
	}
	if ringKeyCeiling*perSubjectBytes > residentRingBudget {
		t.Fatalf("a tenant's aggregates may hold %d subjects at %d bytes = %d, over the %d-byte budget they are "+
			"supposed to cost. The ceiling is what the budget buys, not the number passed to the store.",
			ringKeyCeiling, perSubjectBytes, ringKeyCeiling*perSubjectBytes, residentRingBudget)
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
	code, out := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA, b.String())
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /v1/risk/learn with %d events = %d %s, want 413", maxBatch+1, code, out)
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
		{"/v1/risk/score", `{"event":{"id":"e1","kind":"account","subject":"u_1"}}`},
		{"/v1/risk/learn", `{"events":[{"id":"e1","kind":"account","subject":"u_1"}]}`},
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

	code, out := req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA,
		`{"event":{"id":"e1","kind":"account","subject":"u_1"}}`)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/risk/score = %d %s", code, out)
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
	if code, out := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA, b.String()); code != http.StatusOK {
		t.Fatalf("POST /v1/risk/learn = %d %s", code, out)
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
		for i := 0; i < events; i++ {
			probe.hold(string(k), map[string]any{
				"subject_kind": kindAccount, "subject": "u_" + itoa(i%4),
				"bucket": surfaceAt(i + 1),
				"events": uint32(2), "spend_nano": int64(150_000_000),
			})
		}
	}

	probe.reset(true)
	hold(key(t, brandA, orgA))
	poor := mountBilled(t, &ledger{available: want - 1})
	if code, out := req(t, poor, http.MethodPost, "/v1/risk/search", orgA, "u_"+orgA, `{"days":1}`); code != http.StatusPaymentRequired {
		t.Fatalf("a %d-event search on a balance of %d cents = %d %s, want 402 — it costs %d",
			events, want-1, code, out, want)
	}

	probe.reset(true)
	hold(key(t, brandA, orgA))
	rich := mountBilled(t, &ledger{available: want})
	code, out := req(t, rich, http.MethodPost, "/v1/risk/search", orgA, "u_"+orgA, `{"days":1}`)
	if code != http.StatusAccepted {
		t.Fatalf("a %d-event search on a balance of exactly %d cents = %d %s, want 202 — the gate is asking for more than the work costs",
			events, want, code, out)
	}
	var run riskSearchRun
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
	code, out := req(t, app, http.MethodPost, "/v1/risk/score", orgA, "u_"+orgA,
		`{"event":{"id":"e1","kind":"account","subject":"u_1"}}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/risk/score with the ledger unreachable = %d %s, want 503", code, out)
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

// screens is everything booked so far, in screens. It does not wait: a caller
// uses it where the work is already known to have finished, and "nothing was
// booked" is a real answer there rather than a timeout.
func (l *ledger) screens() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var micros int64
	for _, d := range l.posted {
		micros += d.Micros
	}
	return micros / defaultScreenUUSD
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

// ── the bound every other bound is made of ───────────────────────────────────

// TestBounds_ArePublishedInTheDimensionThatBinds is the CLASS test, and it
// MEASURES rather than restating the formula.
//
// A cap on a COUNT of caller-sized values is not a bound. Every ceiling this
// package publishes — 8 MiB of aggregates per tenant, 32 MiB of record per
// tenant, 512 MiB across the process — is a count multiplied by the size of
// something a caller chose, so while that size is unbounded so is the product. It
// was: a caller picking 4 KiB identifiers made the "8 MiB" of rings and the
// record's row cap understate reality by more than an order of magnitude.
//
// So this test builds the WORST CASE the door will actually accept and compares
// what it really costs against what is published. Recomputing the formula would
// prove nothing; the point is that the formula is true of a real value.
//
// Mutation proof: raise maxField (or delete the length check in observe) and the
// key-text and row-byte measurements below exceed the published terms.
func TestBounds_ArePublishedInTheDimensionThatBinds(t *testing.T) {
	probe.reset(true)
	big := strings.Repeat("z", maxField)
	k := key(t, brandA, orgA)

	// 1. THE KEY TEXT. Every per-subject memory figure is a multiple of it.
	worst, err := observe(big, actor{Kind: kindAccount, Subject: big, Peer: big, Device: big}, 9_999, time.Now().UTC())
	if err != nil {
		t.Fatalf("the widest legal observation was refused: %v", err)
	}
	for _, key := range anomaly.Keys(worst.tx(k)) {
		if n := len(key.OrgID) + 1 + len(key.Kind) + 1 + len(key.Value); n > maxKeyText {
			t.Fatalf("a legal event produces a %d-byte aggregate key against a published %d — "+
				"every per-tenant memory figure is understated by that ratio", n, maxKeyText)
		}
	}

	// 2. THE ROW ON DISK. The record's ceiling is recordRows × maxRowBytes, and
	//    that is a byte bound only if a real worst-case row fits maxRowBytes.
	p := newTestPlane(t)
	holdFolds(t, p)
	const sample = 200
	sh, err := p.for_(k)
	if err != nil {
		t.Fatalf("shelf: %v", err)
	}
	before := shelfBytes(t, sh)
	batch := make([]observation, 0, sample)
	at := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < sample; i++ {
		// Distinct ids and subjects at the bound: the widest row the door admits.
		batch = append(batch, ob(t, pad(strconv.Itoa(i), big), kindAccount, pad("s"+strconv.Itoa(i), big), 9_999,
			at.Add(time.Duration(i)*time.Second), pad("p"+strconv.Itoa(i), big), pad("d"+strconv.Itoa(i), big)))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	grew := shelfBytes(t, sh) - before
	per := grew / sample
	if per > maxRowBytes {
		t.Fatalf("a worst-case observation costs %d bytes on the shelf against a published %d — "+
			"the record's %d MiB per-tenant ceiling is understated by %.1fx",
			per, maxRowBytes, recordBudget>>20, float64(per)/float64(maxRowBytes))
	}
	// The row cap IS the byte budget divided by the row bound, so the product is
	// the budget (to within the one row integer division drops).
	if recordRows*maxRowBytes > recordBudget || (recordRows+1)*maxRowBytes <= recordBudget {
		t.Fatalf("the record's row cap (%d) is no longer the byte budget (%d) divided by the row bound (%d)",
			recordRows, recordBudget, maxRowBytes)
	}
	t.Logf("measured: worst-case row %d bytes (published %d); record ceiling %d rows x %d = %d MiB",
		per, maxRowBytes, recordRows, maxRowBytes, recordBudget>>20)
}

// pad grows s to exactly len(big) bytes, keeping s's own prefix so the values
// stay distinct.
func pad(s, big string) string { return s + big[len(s):] }

// shelfBytes is the real size of one organisation's SQLite file, pages and all.
func shelfBytes(t *testing.T, sh *shelf) int {
	t.Helper()
	var pages, size int
	if err := sh.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := sh.db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	return pages * size
}

// TestField_IsRefusedAtTheDoorAndNotTruncated: the bound is applied where an
// observation is MINTED, so the live wire, the replay and the fold all get it —
// and it REFUSES, because two subjects differing only past a truncation would
// silently become one set of aggregates.
//
// Mutation proof: delete the length loop in observe and the 413s below become
// 200s.
func TestField_IsRefusedAtTheDoorAndNotTruncated(t *testing.T) {
	probe.reset(true)
	app := mountBilled(t, &ledger{available: 1_000_000})
	over := strings.Repeat("z", maxField+1)
	for _, tc := range []struct{ name, body string }{
		{"subject", `{"events":[{"id":"e1","kind":"account","subject":"` + over + `"}]}`},
		{"id", `{"events":[{"id":"` + over + `","kind":"account","subject":"u_1"}]}`},
		{"peer", `{"events":[{"id":"e1","kind":"account","subject":"u_1","peer":"` + over + `"}]}`},
		{"device", `{"events":[{"id":"e1","kind":"account","subject":"u_1","device":"` + over + `"}]}`},
	} {
		code, out := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA, tc.body)
		if code != http.StatusRequestEntityTooLarge {
			t.Fatalf("a %d-byte %q was accepted with %d %s — every ceiling this plane publishes is a count of these",
				len(over), tc.name, code, out)
		}
	}
	// And exactly at the bound is legal, so the refusal is a bound and not a ban.
	at := strings.Repeat("z", maxField)
	code, out := req(t, app, http.MethodPost, "/v1/risk/learn", orgA, "u_"+orgA,
		`{"events":[{"id":"e1","kind":"account","subject":"`+at+`"}]}`)
	if code != http.StatusOK {
		t.Fatalf("a subject of exactly %d bytes was refused: %d %s", maxField, code, out)
	}
}

// TestObservation_HasOneConstructor is the STRUCTURAL half of the field bound:
// the bound lives in [observe], so it is a bound only while observe is the only
// way to make an observation. A second composite literal anywhere in the package
// — production or test — is a second door with no lock on it.
//
// Mutation proof: write `observation{}` with any field set anywhere in this
// package and this fails.
func TestObservation_HasOneConstructor(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || len(lit.Elts) == 0 {
					return true // observation{} carries nothing and bounds nothing
				}
				id, ok := lit.Type.(*ast.Ident)
				if !ok || id.Name != "observation" {
					return true
				}
				if strings.HasSuffix(fset.Position(lit.Pos()).Filename, "learn.go") {
					return true // the one constructor lives there
				}
				t.Errorf("%s builds an observation directly — the field bound lives in observe(), "+
					"so a second constructor is a second unbounded door",
					fset.Position(lit.Pos()))
				return true
			})
			_ = name
		}
	}
}

// TestOps_EveryOpIsAdmittedAndPriced is the STRUCTURAL half of the money seam and
// of the concurrency bound.
//
// Half this surface used to be free: state, features, appetite, snapshot, restore
// and the search read reached the plane, the tenant's own shelf and — in
// features' case — up to 120 warehouse statements, with no balance check, no
// debit and no in-flight slot. Gating the two ops a reviewer looks at is not
// gating; the ops an abuser calls in a loop are the cheap ones nobody thought of.
// So the rule is checked by reading the source rather than by remembering it.
//
// Mutation proof: delete the o.gate or the o.admit call from any op and this
// names it.
func TestOps_EveryOpIsAdmittedAndPriced(t *testing.T) {
	fset := token.NewFileSet()
	// THE WHOLE PACKAGE, not typed.go. An op declared in any other file would
	// otherwise be admitted and priced by nobody's assertion — the gate would read
	// green while the surface it guards grew past it, which is the one way a
	// structural test stops being one.
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	// The ops are exactly the methods on `ops` that a typed registration names.
	registered := map[string]bool{}
	risk, err := parser.ParseFile(fset, "risk.go", nil, 0)
	if err != nil {
		t.Fatalf("parse risk.go: %v", err)
	}
	ast.Inspect(risk, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "o" {
			registered[sel.Sel.Name] = true
		}
		return true
	})
	if len(registered) < 9 {
		t.Fatalf("found %d registered ops, want every one of them — the scan is not reading the mount", len(registered))
	}
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				// The RECEIVER TYPE is part of the identity, not just the name. The plane
				// carries methods called score, learn, state and appetite too, and matching
				// on the name alone reads those instead — which is a scan that fails every
				// op for the wrong reason and would be "fixed" by narrowing it back.
				if !ok || recvType(fn) != "ops" || !registered[fn.Name.Name] {
					continue
				}
				seen[fn.Name.Name] = true
				checkOpIsAdmittedAndPriced(t, fn)
			}
		}
	}
	// Every registered op was FOUND. A name the mount registers and no file
	// declares means the scan stopped reading the surface, which is how this test
	// passes by looking at nothing.
	for name := range registered {
		if !seen[name] {
			t.Errorf("the mount registers op %q and no file in this package declares it — the scan is "+
				"not reading the surface it claims to check", name)
		}
	}
}

// recvType is the receiver's type name, without its pointer, or "" for a
// function with no receiver.
func recvType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// checkOpIsAdmittedAndPriced is the per-op half of
// [TestOps_EveryOpIsAdmittedAndPriced].
func checkOpIsAdmittedAndPriced(t *testing.T, fn *ast.FuncDecl) {
	t.Helper()
	{
		var admits, gates bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "o" {
				admits = admits || sel.Sel.Name == "admit"
				gates = gates || sel.Sel.Name == "gate"
			}
			return true
		})
		if !admits {
			t.Errorf("op %q reaches the plane without o.admit — no tenant in-flight slot, so that "+
				"organisation's own concurrency is an unbounded queue of goroutines", fn.Name.Name)
		}
		if !gates {
			t.Errorf("op %q is not priced — free unbounded compute against a per-tenant model, a "+
				"per-tenant disk and a shared warehouse", fn.Name.Name)
		}
	}
}

// TestEveryOp_IsGatedOnTheCallersOwnBalance is the BEHAVIOURAL half of
// [TestOps_EveryOpIsAdmittedAndPriced]: every op on this surface refuses on an
// empty balance, and refuses without doing the work.
//
// features is the one that mattered most and was free: it rolls up to four
// source planes into the tenant's own surface — up to 120 bounded
// INSERT..SELECT statements against the single warehouse pod the file's own
// header calls "a single stateful pod that has taken the API down once already"
// — and then reads a caller-chosen window of up to 400 days back.
//
// Mutation proof: remove the o.gate call from any op below and it answers 200 on
// a zero balance.
func TestEveryOp_IsGatedOnTheCallersOwnBalance(t *testing.T) {
	probe.reset(true)
	books := &ledger{available: 0}
	app := mountBilled(t, books)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/risk/score", `{"event":{"id":"e1","kind":"account","subject":"u_1"}}`},
		{http.MethodPost, "/v1/risk/learn", `{"events":[{"id":"e1","kind":"account","subject":"u_1"}]}`},
		{http.MethodGet, "/v1/risk/state", ""},
		{http.MethodPut, "/v1/risk/state/appetite", `{"review":0.01,"sample":0.001}`},
		{http.MethodPost, "/v1/risk/state/snapshot", ""},
		{http.MethodPost, "/v1/risk/state/restore", `{"body":{"version":1}}`},
		{http.MethodGet, "/v1/risk/policy", ""},
		{http.MethodGet, "/v1/risk/features?days=400", ""},
		{http.MethodGet, "/v1/risk/search/srch_none", ""},
	} {
		code, out := req(t, app, tc.method, tc.path, orgA, "u_"+orgA, tc.body)
		if code != http.StatusPaymentRequired {
			t.Errorf("%s %s on an empty balance = %d %s, want 402 — free unbounded compute against a "+
				"per-tenant model, a per-tenant disk and a shared warehouse", tc.method, tc.path, code, out)
		}
	}
	if books.debits() != 0 {
		t.Fatalf("a refused request still debited %d time(s)", books.debits())
	}
	// And the warehouse was never touched: the gate runs BEFORE the work.
	probe.mu.Lock()
	sent := len(probe.sent)
	probe.mu.Unlock()
	if sent != 0 {
		t.Fatalf("%d warehouse statements were issued for requests that were refused on the balance", sent)
	}
}

// TestFeatures_IsPricedFromItsWindow: the most expensive read on this surface is
// not also the cheapest. A 400-day catalogue costs 400 screens, and it is charged
// at BOTH seams — the gate before the work and the meter after it.
//
// Both, because they fail differently and only one of them is a refusal. A meter
// that reports the right number while the gate asks for nothing is an op that
// bills a caller who has no balance and does the work anyway; the amount looks
// perfect in the books and the control is off. So the gate is proved as a
// BRACKET — one cent under the measured cost refuses, exactly the cost admits —
// which nothing but the real number satisfies.
//
// Mutation proof: price the GATE at zero screens and the 402 below becomes a 200;
// price the METER at a flat 1 and the amount stops tracking the window.
func TestFeatures_IsPricedFromItsWindow(t *testing.T) {
	const days = 400
	want := cloud.MicrosToGateCents(screenMicros(days))
	if want < 2 {
		t.Fatalf("a %d-day catalogue costs %d cents, too coarse for this bracket to mean anything", days, want)
	}

	// A cent under the cost is REFUSED. The gate is asking for the window.
	probe.reset(true)
	poor := mountBilled(t, &ledger{available: want - 1})
	if code, out := req(t, poor, http.MethodGet, "/v1/risk/features?days=400", orgA, "u_"+orgA, ""); code != http.StatusPaymentRequired {
		t.Fatalf("a %d-day catalogue on a balance of %d cents = %d %s, want 402 — it costs %d, and an "+
			"op gated at zero is an op with no gate wearing one", days, want-1, code, out, want)
	}

	// Exactly the cost admits, and the meter books exactly the window.
	probe.reset(true)
	books := &ledger{available: want}
	app := mountBilled(t, books)
	if code, out := req(t, app, http.MethodGet, "/v1/risk/features?days=400", orgA, "u_"+orgA, ""); code != http.StatusOK {
		t.Fatalf("GET /v1/risk/features?days=400 on a balance of exactly %d cents = %d %s — the gate "+
			"is asking for more than the work costs", want, code, out)
	}
	posted := books.await(t, 1)
	if got := posted[0].Micros / defaultScreenUUSD; got != days {
		t.Fatalf("a %d-day catalogue metered %d screens — the op that issues up to 120 warehouse "+
			"statements must be priced from the window it reads", days, got)
	}
}

// TestSearch_IsMeteredOnWhatTheRunDid, not on what it was admitted for.
//
// The op answers 202 and the grid runs behind it. Metering at ACCEPT charged for
// every candidate over every event the moment the run was admitted — and this
// binary deploys at ONE replica with the old pod stopped first, so a rollout
// cancels the run partway with the debit already taken and, if the process dies
// before the report is written, with no result to show for it either.
//
// It is proved on a run that is CUT SHORT, because that is the only place the two
// rules differ: a run that finishes tries every candidate, so "what it was
// admitted for" and "what it did" are the same number and an assertion on a
// completed run cannot tell them apart. The rollout is the case that matters
// anyway — one replica, old pod stopped first — and it is the case where metering
// at accept charges in full for work that never happened.
//
// Mutation proof: move the pay() call back beside the 202 and the amount below
// becomes the full candidates × events even though the grid was cancelled.
func TestSearch_IsMeteredOnWhatTheRunDid(t *testing.T) {
	probe.reset(true)
	books := &ledger{available: 1_000_000_000}
	app := mountBilled(t, books)
	k := key(t, brandA, orgA)
	for i := 0; i < 40; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + strconv.Itoa(i%5),
			"bucket": surfaceAt(i + 1),
			"events": uint32(1), "spend_nano": int64(1_000_000),
		})
	}
	code, out := req(t, app, http.MethodPost, "/v1/risk/search", orgA, "u_"+orgA, `{"days":7}`)
	if code != http.StatusAccepted {
		t.Fatalf("POST /v1/risk/search = %d %s, want 202", code, out)
	}
	var run struct {
		ID                 string `json:"id"`
		Events, Candidates int
	}
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("decode the run: %v", err)
	}
	accepted := int64(run.Events * run.Candidates)
	if accepted == 0 {
		t.Fatal("the accepted run is empty — this test proves nothing")
	}
	// THE ROLLOUT, MID-GRID. Shutdown cancels the plane's context and waits for the
	// run goroutine, which is exactly what the old pod does while the new one is
	// starting. Nothing is charged until that goroutine ends, so by the time this
	// returns the books are final and there is no interval to poll.
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if did := books.screens(); did >= accepted {
		t.Fatalf("a run cancelled by a rollout metered %d screens, the full %d it was ADMITTED for — "+
			"the meter is running on the accepted size and not on the work, so every deploy bills "+
			"every caller in flight for a grid that never ran", did, accepted)
	}
}

// TestSearch_TheDebitLandsOnTheCallerThatAskedForIt: the meter fires from a
// background goroutine long after the 202, so it must carry the identity it was
// given rather than read a request arena that has since been recycled — on a
// connection two tenants took turns on, that arena is the OTHER tenant.
func TestSearch_TheDebitLandsOnTheCallerThatAskedForIt(t *testing.T) {
	probe.reset(true)
	books := &ledger{available: 1_000_000_000}
	app := mountBilled(t, books)
	k := key(t, brandA, orgA)
	for i := 0; i < 40; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + strconv.Itoa(i%5),
			"bucket": surfaceAt(i + 1),
			"events": uint32(1), "spend_nano": int64(1_000_000),
		})
	}
	if code, out := req(t, app, http.MethodPost, "/v1/risk/search", orgA, "u_"+orgA, `{"days":7}`); code != http.StatusAccepted {
		t.Fatalf("POST /v1/risk/search = %d %s, want 202", code, out)
	}
	posted := books.await(t, 1)
	if posted[0].Micros == 0 {
		t.Fatal("a completed run metered nothing")
	}
	if posted[0].Org != orgA || posted[0].User != orgA {
		t.Fatalf("the run's debit landed on %q/%q rather than the caller's own ledger — the meter is "+
			"reading a request that has already been recycled", posted[0].Org, posted[0].User)
	}
}

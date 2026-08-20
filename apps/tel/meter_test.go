package tel

// Three acts on this surface buy from a carrier, and the property that matters
// most is the one the surface never had: an org that cannot pay is stopped BEFORE
// the carrier is asked. Uncollected revenue is a bill; an ungated carrier is an
// unbounded spend surface reachable by any authenticated tenant.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// carrierSpy is the live carrier, counting what was actually bought. It stands in
// for the REST carrier rather than the stub, because the stub is deliberately
// unbilled and would prove nothing about the gate.
type carrierSpy struct {
	Carrier
	bought int
}

func (c *carrierSpy) Buy(ctx context.Context, e164 string) (Number, error) {
	c.bought++
	return Number{ID: "n1", E164: e164}, nil
}

func (c *carrierSpy) Send(ctx context.Context, r SMSRequest) (SMS, error) {
	c.bought++
	return SMS{ID: "m1", From: r.From, To: r.To}, nil
}

// telAt builds the surface with a LIVE carrier billed against l, and returns the
// spy so a test can ask whether the carrier was reached at all.
func telAt(t *testing.T, l *planetest.Ledger) (*cloud.Service[state], *carrierSpy) {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	spy := &carrierSpy{Carrier: newStub()}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Metering: nil, Env: "mainnet"}, "tel"),
		State: state{store: store, carrier: spy, live: true},
	}
	s.Bill = cloud.NewResourceMeter(cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, "tel")
	return s, spy
}

// buyAs orders one number as org through a REAL request, so the payer is resolved
// the way production resolves it.
func buyAs(t *testing.T, s *cloud.Service[state], org string) error {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var out error
	app.Post("/probe", func(c *zip.Ctx) error {
		_, out = ops{s: s}.buyNumber(c.Context(), &buyInput{E164: "+15550001111"})
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return out
}

// Ordering a number bills the caller's own org at the declared fee.
func TestBuyingANumberBillsTheCaller(t *testing.T) {
	l := planetest.Money(t, 100000)
	s, spy := telAt(t, l)

	if err := buyAs(t, s, "acme"); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if spy.bought != 1 {
		t.Fatalf("carrier asked %d times, want 1", spy.bought)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a number order must bill", l.Count())
	}
	org, cents, model, _ := l.Charged()
	if org != "acme" {
		t.Errorf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	if cents != price[number] {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, price[number])
	}
	if model != number {
		t.Errorf("debit unit = %q, want %q", model, number)
	}
}

// An org that cannot pay never reaches the carrier. This is the property the
// surface was missing entirely: the refusal has to land BEFORE the number is
// ordered, or the platform has already bought it.
func TestUnfundedOrgNeverReachesTheCarrier(t *testing.T) {
	l := planetest.Money(t, 0)
	s, spy := telAt(t, l)

	if err := buyAs(t, s, "acme"); err == nil {
		t.Fatal("an unfunded org ordered a number; the gate must refuse it")
	}
	if spy.bought != 0 {
		t.Fatalf("carrier asked %d times for an unfunded org, want 0 — the gate must precede the order", spy.bought)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused order, want 0", n)
	}
}

// A deployment with no carrier credential runs the in-process stub, which buys
// nothing — so it is neither gated nor billed, and the suite and the sandbox keep
// working with an empty balance.
func TestTheStubBuysNothingAndBillsNobody(t *testing.T) {
	l := planetest.Money(t, 0)
	s, spy := telAt(t, l)
	s.State.live = false

	if err := buyAs(t, s, "acme"); err != nil {
		t.Fatalf("the stub refused an order: %v", err)
	}
	if spy.bought != 1 {
		t.Fatalf("stub asked %d times, want 1", spy.bought)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d against the stub, want 0 — nothing was bought", n)
	}
}

// ── the fleet-agent door ────────────────────────────────────────────────────
//
// An agent reaches an op with NO HTTP request behind it: the identity boundary
// states the caller on the context and zip dispatches straight to the handler.
// Every check the REST door runs still runs — principal.Acting resolves the org
// off the same stated caller — so this is not a way in, it is a different
// transport for the same way in.
//
// It was, however, a way to spend for free. The payer used to be resolved from
// the request alone, so on this door the wallet came back empty; empty means
// "nobody to bill", which the meter correctly treats as no gate and no debit. An
// identified tenant could therefore order carrier numbers on a zero balance and
// leave no ledger row. See cloud.PayerOf.

// agent is the context a fleet agent arrives on: a stated caller, no request.
func agent(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{
		Org: org, User: "u_" + org, Name: org, RequestID: "req-agent",
	})
}

// A zero-balance agent is refused, and the carrier is never asked. This is the
// exact order red placed.
func TestFleetAgentCannotOrderOnAnEmptyBalance(t *testing.T) {
	l := planetest.Money(t, 0)
	s, spy := telAt(t, l)

	if _, err := (ops{s: s}).buyNumber(agent("acme"), &buyInput{E164: "+15550001111"}); err == nil {
		t.Fatal("an agent on a zero balance ordered a number; the gate must refuse it on every door")
	}
	if spy.bought != 0 {
		t.Fatalf("carrier asked %d times for an unfunded agent, want 0 — the gate must precede the order", spy.bought)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused order, want 0", n)
	}
}

// And a funded agent is billed, on its own org. Without this the test above would
// pass for the wrong reason — a door that refuses everyone is not a gate.
func TestFleetAgentOrderBillsItsOwnOrg(t *testing.T) {
	l := planetest.Money(t, 100000)
	s, spy := telAt(t, l)

	if _, err := (ops{s: s}).buyNumber(agent("acme"), &buyInput{E164: "+15550001111"}); err != nil {
		t.Fatalf("a funded agent was refused: %v", err)
	}
	if spy.bought != 1 {
		t.Fatalf("carrier asked %d times, want 1", spy.bought)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — an agent's order must bill like anyone else's", l.Count())
	}
	if org, _, _, _ := l.Charged(); org != "acme" {
		t.Fatalf("debited org %q, want the agent's own %q", org, "acme")
	}
}

// ── the bound ───────────────────────────────────────────────────────────────

// A balance is not a per-call limit, and this is the test that says so.
//
// The gate reads the ledger, and the ledger does not know about calls this pod
// already has in flight — so N simultaneous callers each saw the same untouched
// balance and each cleared it. Red ordered four numbers against a balance
// covering one. A narrower window does not fix that; nothing commits until the
// debit lands, and the debit lands after the carrier has already been paid.
//
// cloud.ResourceMeter.Allow commits the cost BEFORE it weighs it, so the figure
// the balance must cover already includes every other call outstanding for the
// same wallet, and the second caller must clear both.
func TestConcurrentOrdersCannotOutrunTheBalance(t *testing.T) {
	// Exactly one number's worth of money.
	l := planetest.Money(t, price[number])
	s, spy := telAt(t, l)

	const callers = 4
	var wg sync.WaitGroup
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, so the reads genuinely overlap
			_, errs[i] = (ops{s: s}).buyNumber(agent("acme"), &buyInput{E164: "+1555000111" + strconv.Itoa(i)})
		}(i)
	}
	close(start)
	wg.Wait()

	var ok int
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d concurrent orders succeeded against a balance covering ONE; "+
			"the gate is not weighing what this pod already has in flight", ok, callers)
	}
	if spy.bought != 1 {
		t.Fatalf("carrier asked %d times, want 1 — every admitted order is money already spent", spy.bought)
	}
}

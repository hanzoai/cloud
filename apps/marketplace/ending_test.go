package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

// hand is the ledger as the rail reaches it, with a hand on it: a return can be
// held until let go, or refused as a ledger that is down, and a hold can be
// written and its answer lost.
type hand struct {
	money.FinanceClient
	mu      sync.Mutex
	gate    chan struct{} // a return waits for it to close, when set
	entered chan struct{} // a return reached the ledger
	down    bool          // a return is refused
	lose    bool          // a hold is written and answered as an error
}

func grab(t *testing.T) *hand {
	t.Helper()
	real := finance.Current()
	h := &hand{FinanceClient: real, entered: make(chan struct{}, 64)}
	finance.Publish(h)
	t.Cleanup(func() { finance.Publish(real) })
	return h
}

func (h *hand) set(fn func(*hand)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *hand) Hold(ctx context.Context, in money.UsageInput) (string, bool, error) {
	id, posted, err := h.FinanceClient.(finance.Holder).Hold(ctx, in)
	h.mu.Lock()
	lose := h.lose
	h.mu.Unlock()
	if err == nil && lose {
		return "", false, errors.New("finance: the hold was written and not acknowledged")
	}
	return id, posted, err
}

func (h *hand) Return(ctx context.Context, in money.UsageInput) (money.Amount, error) {
	h.mu.Lock()
	gate, down := h.gate, h.down
	h.mu.Unlock()
	if down {
		return money.Zero(), errors.New("finance: the ledger is down")
	}
	if gate != nil {
		h.entered <- struct{}{}
		<-gate
	}
	return h.FinanceClient.(finance.Holder).Return(ctx, in)
}

func (h *hand) Draw(ctx context.Context, in money.UsageInput) (string, bool, error) {
	return h.FinanceClient.(finance.Drawer).Draw(ctx, in)
}

// job reads one job as w.
func (e *env) job(id string, w who) Job {
	e.t.Helper()
	var j Job
	e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+id, w, nil, &j)
	return j
}

// An ending that pays nothing is decided before the money returns. While the rail
// returns a cancelled job's money, the seller's accept is refused, not landed on a
// job whose money went back; and a rail that does not answer leaves the job ending,
// taking no step, until the clock finishes it.
func TestAnEndingIsDecidedBeforeTheMoneyReturns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(2_000))
		h := grab(t)
		in := func(brief string) map[string]any {
			return map[string]any{"listing": l.ID, "brief": brief, "amount": "10.00"}
		}
		j := e.hire(buyer, in("Cancelled"), 30*24*time.Hour)

		gate := make(chan struct{})
		h.set(func(h *hand) { h.gate = gate })
		done := make(chan int)
		go func() {
			code, _, _ := e.send(http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/cancel", member(buyer))
			done <- code
		}()
		<-h.entered
		now := e.job(j.ID, member(seller))
		if now.Status != StatusOpen || now.Ending != StatusCancelled {
			t.Fatalf("a job whose money is being returned reads %s ending %q, want open, ending cancelled", now.Status, now.Ending)
		}
		for _, act := range []string{"accept", "decline"} {
			code, b, _ := e.send(http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/"+act, member(seller))
			if code != http.StatusConflict || !strings.Contains(string(b), "cancelled") {
				t.Fatalf("%s while the job is being cancelled = %d %s, want 409", act, code, b)
			}
		}
		close(gate)
		if code := <-done; code != http.StatusOK {
			t.Fatalf("cancel = %d", code)
		}
		h.set(func(h *hand) { h.gate = nil })
		now = e.job(j.ID, member(buyer))
		if now.Status != StatusCancelled || now.Ending != "" || strings.Join(statuses(now), ",") != "quoted,funding,open,cancelled" {
			t.Fatalf("the cancelled job reads %s ending %q through %v", now.Status, now.Ending, statuses(now))
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(2_000)) != 0 {
			t.Fatalf("the buyer holds %s, want its 20.00 back", got)
		}
		// The decision is on both parties' trails, and then the ending it reached.
		for _, org := range []string{buyer, seller} {
			acts := e.actions(org, "marketplace.job.")
			if i := slices.Index(acts, "marketplace.job.ending"); i < 0 || slices.Index(acts, "marketplace.job.cancelled") < i {
				t.Fatalf("%s's trail %v, want the ending decided and then cancelled", org, acts)
			}
		}

		// The rail is down when the seller refunds an accepted job: the refund is
		// decided, the job takes no step, and the clock finishes it once the rail is back.
		k := e.hire(buyer, in("Refunded"), 30*24*time.Hour)
		e.act(k.ID, "accept", member(seller), nil, http.StatusOK)
		h.set(func(h *hand) { h.down = true })
		if code, b, _ := e.send(http.MethodPost, "/v1/marketplace/jobs/"+k.ID+"/refund", member(seller)); code != http.StatusServiceUnavailable {
			t.Fatalf("a refund the rail cannot return = %d %s, want 503", code, b)
		}
		now = e.job(k.ID, member(buyer))
		if now.Status != StatusAccepted || now.Ending != StatusRefunded {
			t.Fatalf("a refund the rail did not return reads %s ending %q, want accepted, ending refunded", now.Status, now.Ending)
		}
		for _, a := range []struct {
			act string
			as  who
		}{{"deliver", member(seller)}, {"release", member(buyer)}, {"dispute", member(buyer)}, {"refund", member(seller)}} {
			body := map[string]any{"note": "done", "reason": "why"}
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs/"+k.ID+"/"+a.act, a.as, "", body); code != http.StatusConflict {
				t.Fatalf("%s of a job being refunded = %d %s, want 409", a.act, code, b)
			}
		}
		e.tick()
		now = e.job(k.ID, member(buyer))
		if now.Ending != StatusRefunded {
			t.Fatalf("the clock took a job ending while the rail was down to %s ending %q", now.Status, now.Ending)
		}
		h.set(func(h *hand) { h.down = false })
		if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET retry_at = 0 WHERE id = $1`, k.ID); err != nil {
			t.Fatal(err)
		}
		e.tick()
		now = e.job(k.ID, member(buyer))
		if now.Status != StatusRefunded || now.Ending != "" {
			t.Fatalf("the clock left the refund %s ending %q, want refunded", now.Status, now.Ending)
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(2_000)) != 0 {
			t.Fatalf("the buyer holds %s, want its 20.00 back", got)
		}
		if got := e.balance(seller, wallet); !got.IsZero() {
			t.Fatalf("the seller was paid %s by jobs that ended unpaid", got)
		}
	})
}

// A cancel raced by the seller's accept ends one way, with the money where the job
// says it is: every accepted job still holds its amount, and every cancelled one
// gave it back.
func TestACancelRacedByAnAcceptEndsOneWay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		const n = 24
		e.fund(buyer, money.FromCents(100*n))
		h := grab(t)
		var jobs []Job
		for i := range n {
			jobs = append(jobs, e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work " + string(rune('a'+i)), "amount": "1.00"}, 30*24*time.Hour))
		}
		// Returns answer as a reply over two plane hops does: late.
		gate := make(chan struct{})
		h.set(func(h *hand) { h.gate = gate })
		go func() {
			for range h.entered {
				time.Sleep(5 * time.Millisecond)
				gate <- struct{}{}
			}
		}()
		var wg sync.WaitGroup
		for _, j := range jobs {
			for _, a := range []struct {
				act string
				as  who
			}{{"cancel", member(buyer)}, {"accept", member(seller)}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _, _ = e.send(http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/"+a.act, a.as)
				}()
			}
		}
		wg.Wait()
		h.set(func(h *hand) { h.gate = nil })
		close(h.entered)
		cancelled := 0
		for _, j := range jobs {
			now := e.job(j.ID, member(buyer))
			switch {
			case now.Status == StatusCancelled:
				cancelled++
			case now.Status != StatusAccepted || now.Ending != "":
				t.Fatalf("job %s ended %s ending %q", j.ID, now.Status, now.Ending)
			}
		}
		if got, want := e.balance(buyer, buyer), money.FromCents(int64(100*cancelled)); got.Cmp(want) != 0 {
			t.Fatalf("the buyer holds %s after %d of %d jobs were cancelled, want %s: an accepted job's money went back", got, cancelled, n, want)
		}
	})
}

// A hold whose answer is lost is given back. The buyer's funding committed its
// hold and heard an error; the same payment sent again opens the job holding
// once; a new signature for the same quote gives the first payment back before it
// holds; and a quote nobody opens gives back what its payment set aside when it
// lapses.
func TestAHoldWhoseAnswerIsLostIsGivenBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		h := grab(t)
		lost := func(in map[string]any) (string, string) {
			t.Helper()
			code, b, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
			if code != http.StatusPaymentRequired {
				t.Fatalf("quote = %d %s", code, b)
			}
			payment := e.pay(e.challengeOf(hd), 30*24*time.Hour)
			quote, _ := client.JobOf(e.resource)
			h.set(func(h *hand) { h.lose = true })
			code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in)
			h.set(func(h *hand) { h.lose = false })
			if code != http.StatusServiceUnavailable {
				t.Fatalf("a funding whose hold's answer was lost = %d %s, want 503", code, b)
			}
			return quote, payment
		}
		open := func(in map[string]any, payment string) Job {
			t.Helper()
			code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in)
			if code != http.StatusCreated {
				t.Fatalf("funding = %d %s, want 201", code, b)
			}
			var j Job
			if err := json.Unmarshal(b, &j); err != nil {
				t.Fatal(err)
			}
			return j
		}
		held := func(want int64, why string) {
			t.Helper()
			if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(10_000-want)) != 0 {
				t.Fatalf("%s: the buyer holds %s, want %s set aside", why, got, money.FromCents(want))
			}
		}

		// The same payment again: one job, one hold.
		a := map[string]any{"listing": l.ID, "brief": "Same payment", "amount": "30.00"}
		_, payment := lost(a)
		held(3_000, "after a hold whose answer was lost")
		open(a, payment)
		held(3_000, "after the same payment opened the job")

		// Signed again for the same quote: the first payment is given back first.
		b := map[string]any{"listing": l.ID, "brief": "New signature", "amount": "30.00"}
		quote, _ := lost(b)
		held(6_000, "after a second lost answer")
		code, body, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", b)
		if code != http.StatusPaymentRequired {
			t.Fatalf("the same terms asked again = %d %s", code, body)
		}
		req := e.challengeOf(hd)
		if again, _ := client.JobOf(e.resource); again != quote {
			t.Fatalf("the same terms asked again quoted %s, want the quote %s", again, quote)
		}
		open(b, e.pay(req, 30*24*time.Hour))
		held(6_000, "after a new signature opened the job")

		// Nobody sends it again: the quote lapses, and its payment's hold comes back.
		c := map[string]any{"listing": l.ID, "brief": "Never sent again", "amount": "30.00"}
		quote, _ = lost(c)
		held(9_000, "after a third lost answer")
		e.age(quote, "created_at", quoteTTL+time.Minute)
		e.tick()
		if _, found, _ := mounted.State.store.job(context.Background(), quote); found {
			t.Fatal("a lapsed quote was kept")
		}
		held(6_000, "after the quote lapsed")
	})
}

// A job the clock sent to the arbiter because its payment stopped clearing is
// paid by the clock the moment it clears again: a seller that furnishes its tax
// form after the review window closed is paid for the work it delivered, with
// nobody ruling. One that never clears in the arbiter's time is refunded, and so
// is one whose authorization expired before it could be paid.
func TestAContestedJobIsPaidOnceItClears(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		cured := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Cured", "amount": "40.00"}, 30*24*time.Hour)
		never := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Never", "amount": "10.00"}, 30*24*time.Hour)
		expired := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Expired", "amount": "5.00"}, 30*24*time.Hour)
		for _, j := range []Job{cured, never, expired} {
			e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
			e.act(j.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
			e.age(j.ID, "delivered_at", defaultReview+time.Hour)
		}
		e.peers.set(func(s *stand) { s.verdict[seller] = client.ClearNeedsForm })
		e.tick()
		var now Job
		for _, j := range []Job{cured, never, expired} {
			now = e.job(j.ID, member(seller))
			if now.Status != StatusDisputed || now.Dispute == nil || now.Dispute.By != bySweep || !now.Dispute.Contested {
				t.Fatalf("a delivered job owed a form when review closed is %s (%+v), want disputed by the clock", now.Status, now.Dispute)
			}
		}
		// Still owed: the clock tries again after its backoff, and leaves it disputed.
		if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET retry_at = 0`); err != nil {
			t.Fatal(err)
		}
		e.tick()
		now = e.job(cured.ID, member(seller))
		if now.Status != StatusDisputed {
			t.Fatalf("a contested job that still does not clear is %s", now.Status)
		}
		// The form is furnished for one payment: the clock pays it, nobody ruling.
		e.peers.set(func(s *stand) { s.verdict[seller] = "" })
		if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET retry_at = 0 WHERE id = $1`, cured.ID); err != nil {
			t.Fatal(err)
		}
		e.tick()
		now = e.job(cured.ID, member(seller))
		if now.Status != StatusReleased || now.History[len(now.History)-1].By != bySweep {
			t.Fatalf("a contested job whose payment clears again is %s, want released by the clock", now.Status)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(4_000)) != 0 {
			t.Fatalf("seller holds %s, want the 40.00 it delivered for", got)
		}
		// The other never clears: when the arbiter's time runs out, it is refunded.
		e.peers.set(func(s *stand) { s.verdict[seller] = client.ClearBlocked })
		e.age(never.ID, "disputed_at", disputeTimeout+time.Hour)
		if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET retry_at = 0 WHERE id = $1`, never.ID); err != nil {
			t.Fatal(err)
		}
		e.tick()
		now = e.job(never.ID, member(buyer))
		if now.Status != StatusRefunded {
			t.Fatalf("a contested job that never cleared in the arbiter's time is %s, want refunded", now.Status)
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(5_500)) != 0 {
			t.Fatalf("buyer holds %s, want 100.00 less the 40.00 paid and the 5.00 still held", got)
		}
		// The last cannot be cleared at all when the arbiter's time has run out: while
		// its authorization stands the clock tries again, and once it has expired the
		// payment can never be made, and the job is refunded.
		e.peers.set(func(s *stand) { s.verdict[seller] = down })
		e.age(expired.ID, "disputed_at", disputeTimeout+time.Hour)
		for _, stale := range []bool{false, true} {
			if stale {
				e.age(expired.ID, "valid_before", 31*24*time.Hour)
			}
			if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET retry_at = 0 WHERE id = $1`, expired.ID); err != nil {
				t.Fatal(err)
			}
			e.tick()
			now = e.job(expired.ID, member(buyer))
			if want := map[bool]string{false: StatusDisputed, true: StatusRefunded}[stale]; now.Status != want {
				t.Fatalf("a lapsed contest that cannot be cleared, its authorization expired=%v, is %s, want %s", stale, now.Status, want)
			}
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(6_000)) != 0 {
			t.Fatalf("buyer holds %s, want 100.00 less the 40.00 paid", got)
		}
	})
}

// A job spends one of its buyer's hourly clearances, however often its quote is
// shown: the quote is cleared once, and funding and release ask again of that
// clearance, which principal counts against the payment and not the buyer. A
// payment the rail refuses is refused before anything is asked for it.
func TestAJobSpendsOneOfTheBuyersClearances(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		before := len(e.peers.asked())
		in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "1.00"}
		for range 3 {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in); code != http.StatusPaymentRequired {
				t.Fatalf("quote = %d %s", code, b)
			}
		}
		// A payment the rail refuses — its signature not its payer's — costs none.
		_, _, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		p, err := protocol.ParsePayment(e.pay(e.challengeOf(h), 30*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		sig := []byte(p.Payload.Signature)
		if sig[10] == '0' {
			sig[10] = '1'
		} else {
			sig[10] = '0'
		}
		p.Payload.Signature = string(sig)
		forged, err := protocol.EncodeHeader(p)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), forged, in); code != http.StatusPaymentRequired {
				t.Fatalf("a forged payment = %d %s, want 402", code, b)
			}
		}
		j := e.hire(buyer, in, 30*24*time.Hour)
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
		r := e.act(j.ID, "release", member(buyer), nil, http.StatusOK)
		asked := e.peers.asked()[before:]
		if len(asked) != 3 || asked[0].in.Again != "" || asked[1].in.Again != asked[0].id || asked[2].in.Again != asked[1].id {
			t.Fatalf("a job shown three times, funded and released asked %+v, want the quote's, then funding's and release's each asked again of the last", asked)
		}
		if r.Clearance != asked[2].id {
			t.Fatalf("released on clearance %s, want the release's %s", r.Clearance, asked[2].id)
		}
	})
}

// The clock's own dispute is known by the job's mark, never by a name: a seller
// org named as the clock signs its steps, disputing a job it only accepted, waits
// for the arbiter like any party, and the clock pays it nothing.
func TestASellerNamedLikeTheClockIsNotPaidByIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		wallet := e.createWallet(bySweep)
		e.agent(bySweep, "analyst")
		l := e.list(bySweep, publishReq{Kind: KindAgent, Tool: "analyst", Title: "Research analyst", Price: "250",
			Recipient: wallet, Public: true, Category: "research"})
		e.fund(buyer, money.FromCents(10_000))
		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "100.00"}, 30*24*time.Hour)
		e.act(j.ID, "accept", member(bySweep), nil, http.StatusOK)
		e.act(j.ID, "dispute", member(bySweep), map[string]any{"reason": "pay me"}, http.StatusOK)
		for range 3 {
			e.tick()
			if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET retry_at = 0`); err != nil {
				t.Fatal(err)
			}
		}
		if now := e.job(j.ID, member(buyer)); now.Status != StatusDisputed || now.Dispute == nil || now.Dispute.Contested {
			t.Fatalf("a seller named %q disputing its own job is %s (%+v) after the clock ran, want disputed and not the clock's", bySweep, now.Status, now.Dispute)
		}
		if got := e.balance(bySweep, wallet); !got.IsZero() {
			t.Fatalf("the clock paid a seller named %q %s for a job it never delivered", bySweep, got)
		}
	})
}

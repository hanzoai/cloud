package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/client"
	x402peer "github.com/hanzoai/cloud/client/x402"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

const (
	seller = "globex"
	buyer  = "acme"
)

// agent defines an agent in org and answers its id.
func (e *env) agent(org, name string) string {
	e.t.Helper()
	var a struct {
		ID string `json:"id"`
	}
	e.must(201, http.MethodPost, "/v1/agent", member(org), map[string]any{"name": name, "cap_micro_usd": 1_000_000,
		"max_task_micro_usd": 100_000, "period": "month"}, &a)
	return a.ID
}

// shopfront has seller list an agent for hire at 250, paid into a wallet of its
// own, and answers the listing and the wallet.
func (e *env) shopfront() (Listing, string) {
	e.t.Helper()
	wallet := e.createWallet(seller)
	e.agent(seller, "analyst")
	l := e.list(seller, publishReq{Kind: KindAgent, Tool: "analyst", Title: "Research analyst", Price: "250",
		Recipient: wallet, Public: true, Category: "research"})
	return l, wallet
}

// hire runs the whole of a hire as buyer: the quote's 402, the signature over
// its terms, and the job it opens.
func (e *env) hire(buyer string, in map[string]any, window time.Duration) Job {
	e.t.Helper()
	code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
	if code != http.StatusPaymentRequired {
		e.t.Fatalf("hire with no payment = %d (%s), want the 402 naming what to sign", code, b)
	}
	req := e.challengeOf(h)
	if _, ok := client.JobOf(e.resource); !ok {
		e.t.Fatalf("the challenge is for %q, want a job", e.resource)
	}
	var j Job
	code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), e.pay(req, window), in)
	if code != http.StatusCreated {
		e.t.Fatalf("hire with payment = %d (%s), want 201", code, b)
	}
	if err := json.Unmarshal(b, &j); err != nil {
		e.t.Fatal(err)
	}
	return j
}

// act takes one act on a job as w, and answers the job.
func (e *env) act(id, act string, w who, body any, want int) Job {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	var j Job
	code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs/"+id+"/"+act, w, "", body)
	if code != want {
		e.t.Fatalf("%s %s as %+v = %d (%s), want %d", act, id, w, code, b, want)
	}
	if want < 300 {
		_ = json.Unmarshal(b, &j)
	}
	return j
}

// age moves one of a job's clocks back by d, as time passing would.
func (e *env) age(id, column string, d time.Duration) {
	e.t.Helper()
	if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET `+column+` = `+column+` - $1 WHERE id = $2`, int64(d.Seconds()), id); err != nil {
		e.t.Fatal(err)
	}
}

// tick runs the clocks once, as the sweep does every minute.
func (e *env) tick() { marketOps{s: mounted}.sweepOnce(context.Background()) }

// tickAt is one sweep whose clock reads at, however long the sweep itself takes.
func (e *env) tickAt(at time.Time) {
	marketOps{s: mounted, sweepAt: func() time.Time { return at }}.sweepOnce(context.Background())
}

func statuses(j Job) []string {
	var out []string
	for _, s := range j.History {
		out = append(out, s.Status)
	}
	return out
}

// The whole of a job, as two orgs run it: cleared and quoted, funded by a held
// x402 authorization, accepted, delivered, released — the seller paid exactly
// once, from the buyer's balance, and exactly one economic event stated by the
// rail with the category the parties agreed — then rated by each party once.
func TestJobLifecycle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(100_000))
		before := e.balance(buyer, buyer)

		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Summarize our Q3 filings", "amount": "250.00"}, 30*24*time.Hour)
		if j.Status != StatusOpen || j.BuyerOrg != buyer || j.SellerOrg != seller || j.Amount.String() != "250" ||
			j.Category != client.CategoryService || j.Clearance == "" || j.Escrow.Rail != RailX402 || j.Escrow.PayTo == "" {
			t.Fatalf("opened %+v", j)
		}
		if j.Deadline == 0 || j.Review != int64(defaultReview.Seconds()) {
			t.Fatalf("a job with no stated deadline must be delivered while its payment can settle: deadline %d review %d", j.Deadline, j.Review)
		}
		// Clearance came first — at the quote, and again when the payment was agreed —
		// and it was the buyer's question about paying the seller.
		cleared := func(n int) {
			t.Helper()
			got := e.peers.asked()
			if len(got) != n {
				t.Fatalf("clearances asked = %+v, want %d", got, n)
			}
			for _, c := range got {
				if c.payer != buyer || c.in.Payee != seller || c.in.Amount != "250.00" || c.in.Category != client.CategoryService || c.in.Rail != client.RailX402 {
					t.Fatalf("clearance asked %+v, want acme paying globex 250.00 for a service over x402", c)
				}
			}
		}
		cleared(2)
		// Opening paid nothing: the amount is set aside in the buyer's wallet.
		price, _ := money.ParseUSD("250")
		if got := before.Sub(e.balance(buyer, buyer)); got.Cmp(price) != 0 {
			t.Fatalf("opening a job set %s aside, want the 250 it pays", got)
		}
		if got := e.balance(seller, wallet); !got.IsZero() {
			t.Fatalf("opening a job paid the seller %s", got)
		}

		// Only the seller takes it on.
		e.act(j.ID, "accept", member(buyer), nil, http.StatusForbidden)
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "accept", member(seller), nil, http.StatusConflict)
		d := e.act(j.ID, "deliver", member(seller), map[string]any{"note": "Summary attached", "url": "https://files.example.com/q3.pdf"}, http.StatusOK)
		if d.Status != StatusDelivered || d.Delivery == nil || !strings.HasPrefix(d.Delivery.Hash, "0x") || len(d.Delivery.Hash) != 66 {
			t.Fatalf("delivered %+v", d.Delivery)
		}
		e.act(j.ID, "release", member(seller), nil, http.StatusForbidden)
		r := e.act(j.ID, "release", member(buyer), nil, http.StatusOK)
		if r.Status != StatusReleased || r.Escrow.TxHash == "" {
			t.Fatalf("released %+v", r)
		}
		if want := []string{StatusQuoted, StatusFunding, StatusOpen, StatusAccepted, StatusDelivered, StatusReleased}; !slices.Equal(statuses(r), want) {
			t.Fatalf("history %v, want %v", statuses(r), want)
		}
		// And again before the money moved; the job keeps the one it was paid on.
		cleared(3)
		if r.Clearance == j.Clearance || r.Clearance == "" {
			t.Fatalf("released on clearance %q, want the one asked at release, not the funding's %q", r.Clearance, j.Clearance)
		}
		// Paid exactly once, into the listing's wallet.
		if got := before.Sub(e.balance(buyer, buyer)); got.Cmp(price) != 0 {
			t.Fatalf("buyer debited %s, want exactly 250", got)
		}
		if got := e.balance(seller, wallet); got.Cmp(price) != 0 {
			t.Fatalf("seller credited %s, want exactly 250", got)
		}
		e.act(j.ID, "release", member(buyer), nil, http.StatusConflict)
		if got := e.balance(seller, wallet); got.Cmp(price) != 0 {
			t.Fatalf("a second release moved money: seller holds %s", got)
		}
		// One economic event, stated by the rail, for the service.
		if _, err := x402.Announce(context.Background()); err != nil {
			t.Fatalf("state the settlement: %v", err)
		}
		evs := e.events.All()
		if len(evs) != 1 || evs[0].Buyer != buyer || evs[0].Seller != seller || evs[0].Rail != client.RailX402 ||
			evs[0].Gross != "250" || evs[0].Category != client.CategoryService || evs[0].Service != client.JobResource(j.ID) {
			t.Fatalf("stated %+v, want one x402 service payment of 250 for the job", evs)
		}
		// Both parties see the settlement as a receipt, the seller as payee.
		var got struct {
			Settlements []x402.Receipt `json:"settlements"`
		}
		e.must(200, http.MethodGet, "/v1/x402/settlements?role=payee", member(seller), nil, &got)
		if len(got.Settlements) != 1 || got.Settlements[0].ID != r.Escrow.TxHash || got.Settlements[0].Category != client.CategoryService {
			t.Fatalf("seller's receipts = %+v", got.Settlements)
		}

		// Feedback: each party once, and only a party.
		var f Feedback
		e.must(201, http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/feedback", member(buyer), map[string]any{"rating": 5, "comment": "Thorough"}, &f)
		if f.From != buyer || f.About != seller || f.Listing != l.ID {
			t.Fatalf("feedback %+v", f)
		}
		// What was said and sent rests as ciphertext, whichever route wrote it.
		st := mounted.State.store
		sealedNotPlain(t, "feedback.comment", raw(t, st, `SELECT comment FROM feedback WHERE job = $1 AND from_org = $2`, j.ID, buyer), "Thorough")
		sealedNotPlain(t, "jobs.brief", raw(t, st, `SELECT brief FROM jobs WHERE id = $1`, j.ID), j.Brief)
		held, _, err := st.job(context.Background(), j.ID)
		if err != nil || held.payment == "" {
			t.Fatalf("the job's payment reads %q (%v)", held.payment, err)
		}
		if p := raw(t, st, `SELECT payment FROM jobs WHERE id = $1`, j.ID); p != "kms:orgs/"+buyer+"/marketplace/"+j.ID+"/payment@default" || strings.Contains(p, held.payment) {
			t.Errorf("jobs.payment holds %.80q, want its KMS path in the buyer's org", p)
		}
		sealedNotPlain(t, "jobs.delivery_note", raw(t, st, `SELECT delivery_note FROM jobs WHERE id = $1`, j.ID), "Summary")
		if u := raw(t, st, `SELECT delivery_url FROM jobs WHERE id = $1`, j.ID); u != "kms:orgs/"+seller+"/marketplace/"+j.ID+"/delivery_url@default" {
			t.Errorf("jobs.delivery_url holds %.80q, want its KMS path in the seller's org", u)
		}
		e.act(j.ID, "feedback", member(buyer), map[string]any{"rating": 1}, http.StatusConflict)
		e.act(j.ID, "feedback", member("initech"), map[string]any{"rating": 1}, http.StatusNotFound)
		e.act(j.ID, "feedback", member(seller), map[string]any{"rating": 0}, http.StatusBadRequest)
		e.must(201, http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/feedback", member(seller), map[string]any{"rating": 4}, nil)

		// And the shop shows what the parties made of it.
		var item ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if item.Reputation.Rating == nil || *item.Reputation.Rating != 5 || item.Reputation.Reviews != 1 || item.Reputation.Jobs.Completed != 1 {
			t.Fatalf("listing reputation = %+v, want one 5-star review of one completed job", item.Reputation)
		}
		if item.Seller.Reputation.Reviews != 1 || item.Seller.Reputation.Jobs.Completed != 1 {
			t.Fatalf("seller reputation = %+v", item.Seller.Reputation)
		}
		// And the shop finds it by its rating: the seller's own rating of the buyer is
		// not the listing's.
		for q, n := range map[string]int{"?rating=5": 1, "?rating=4": 1, "?rating=1": 1} {
			var shop Shop
			e.must(200, http.MethodGet, "/v1/marketplace/shop"+q, anyone, nil, &shop)
			if shop.Total != n || shop.Facets.Rating["5"] != 1 || shop.Facets.Rating["4"] != 0 {
				t.Fatalf("shop%s = total %d ratings %v, want the five-star listing", q, shop.Total, shop.Facets.Rating)
			}
		}
		// Every step is on both parties' trails.
		for _, org := range []string{buyer, seller} {
			acts := e.actions(org, "marketplace.job.")
			for _, want := range []string{"marketplace.job.open", "marketplace.job.accepted", "marketplace.job.delivered", "marketplace.job.released"} {
				if !slices.Contains(acts, want) {
					t.Fatalf("%s's trail %v lacks %s", org, acts, want)
				}
			}
		}
	})
}

// A dispute stops a job; the seller giving way refunds it with nothing moved; the
// arbiter — a platform SuperAdmin, and nobody else — rules on another; and a
// dispute nobody rules on lapses to a refund.
func TestJobDisputes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(100_000))
		before := e.balance(buyer, buyer)
		hire := func() Job {
			t.Helper()
			j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "A deck", "amount": "100.00"}, 30*24*time.Hour)
			e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
			return j
		}

		a := hire()
		e.act(a.ID, "dispute", member(buyer), map[string]any{}, http.StatusBadRequest)
		dp := e.act(a.ID, "dispute", member(buyer), map[string]any{"reason": "Wrong quarter"}, http.StatusOK)
		if dp.Status != StatusDisputed || dp.Dispute == nil || dp.Dispute.By != buyer {
			t.Fatalf("disputed %+v", dp)
		}
		e.act(a.ID, "deliver", member(seller), map[string]any{"note": "late"}, http.StatusConflict)
		e.act(a.ID, "refund", member(seller), nil, http.StatusOK)

		b := hire()
		e.act(b.ID, "dispute", member(seller), map[string]any{"reason": "The brief changed"}, http.StatusOK)
		e.act(b.ID, "resolve", admin(buyer), map[string]any{"outcome": "release"}, http.StatusForbidden)
		e.act(b.ID, "resolve", arbiter, map[string]any{"outcome": "split"}, http.StatusBadRequest)
		if r := e.act(b.ID, "resolve", arbiter, map[string]any{"outcome": "release"}, http.StatusOK); r.Status != StatusReleased {
			t.Fatalf("ruled for release = %+v", r)
		}

		c := hire()
		e.act(c.ID, "dispute", member(buyer), map[string]any{"reason": "Silence"}, http.StatusOK)
		e.age(c.ID, "disputed_at", disputeTimeout+time.Minute)
		e.tick()
		var lapsed Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+c.ID, member(buyer), nil, &lapsed)
		if lapsed.Status != StatusRefunded || lapsed.History[len(lapsed.History)-1].By != bySweep {
			t.Fatalf("a dispute nobody ruled on is %s, want refunded by the clock", lapsed.Status)
		}

		// One payment moved: the one the arbiter released.
		hundred := money.FromCents(10_000)
		if got := before.Sub(e.balance(buyer, buyer)); got.Cmp(hundred) != 0 {
			t.Fatalf("buyer paid %s, want exactly the one ruled job's 100", got)
		}
		if got := e.balance(seller, wallet); got.Cmp(hundred) != 0 {
			t.Fatalf("seller received %s, want 100", got)
		}
		// A refunded job may be rated; nothing moved for it, and no event says it did.
		e.must(201, http.MethodPost, "/v1/marketplace/jobs/"+a.ID+"/feedback", member(buyer), map[string]any{"rating": 2}, nil)
		if _, err := x402.Announce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if evs := e.events.All(); len(evs) != 1 || evs[0].Service != client.JobResource(b.ID) {
			t.Fatalf("stated %+v, want the one released job's payment alone", evs)
		}
		var item ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if item.Reputation.Jobs.Disputed != 3 || item.Reputation.Jobs.Completed != 1 {
			t.Fatalf("reputation = %+v, want three disputed and one completed", item.Reputation.Jobs)
		}
	})
}

// The clocks: a delivered job releases itself when its review window closes,
// once; an accepted one past its deadline is refunded; an open one past it is
// cancelled; and a quote nobody paid is dropped.
func TestJobClocks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(100_000))
		deadline := time.Now().Add(48 * time.Hour).Unix()
		in := map[string]any{"listing": l.ID, "brief": "Audit", "amount": "40.00", "deadline": deadline, "review": 3600}

		done := e.hire(buyer, in, 30*24*time.Hour)
		if done.Deadline != deadline || done.Review != 3600 {
			t.Fatalf("stated deadline and review were not kept: %+v", done)
		}
		e.act(done.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(done.ID, "deliver", member(seller), map[string]any{"note": "Report", "hash": "0x" + strings.Repeat("ab", 32)}, http.StatusOK)
		e.tick()
		var j Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+done.ID, member(seller), nil, &j)
		if j.Status != StatusDelivered {
			t.Fatalf("released before its review window closed: %s", j.Status)
		}
		e.age(done.ID, "delivered_at", 2*time.Hour)
		e.tick()
		e.tick()
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+done.ID, member(seller), nil, &j)
		if j.Status != StatusReleased || j.History[len(j.History)-1].By != bySweep {
			t.Fatalf("after the review window: %s by %s, want released by the clock", j.Status, j.History[len(j.History)-1].By)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(4000)) != 0 {
			t.Fatalf("seller holds %s after the clock released, want exactly 40", got)
		}
		// A dispute is too late now.
		e.act(done.ID, "dispute", member(buyer), map[string]any{"reason": "late"}, http.StatusConflict)

		late := e.hire(buyer, in, 30*24*time.Hour)
		e.act(late.ID, "accept", member(seller), nil, http.StatusOK)
		e.age(late.ID, "deadline", 49*time.Hour)
		e.act(late.ID, "deliver", member(seller), map[string]any{"note": "too late"}, http.StatusConflict)
		e.tick()
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+late.ID, member(buyer), nil, &j)
		if j.Status != StatusRefunded {
			t.Fatalf("an accepted job past its deadline is %s, want refunded", j.Status)
		}

		idle := e.hire(buyer, in, 30*24*time.Hour)
		e.age(idle.ID, "deadline", 49*time.Hour)
		e.act(idle.ID, "accept", member(seller), nil, http.StatusConflict)
		e.tick()
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+idle.ID, member(buyer), nil, &j)
		if j.Status != StatusCancelled {
			t.Fatalf("an open job past its deadline is %s, want cancelled", j.Status)
		}

		// A quote nobody paid lapses.
		code, _, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d", code)
		}
		e.challengeOf(h)
		quote, _ := client.JobOf(e.resource)
		e.age(quote, "created_at", quoteTTL+time.Minute)
		e.tick()
		if _, found, _ := mounted.State.store.job(context.Background(), quote); found {
			t.Fatal("a lapsed quote was kept")
		}
		// Only the released job paid.
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(4000)) != 0 {
			t.Fatalf("seller holds %s, want the one released job's 40", got)
		}
	})
}

// Clearance comes first and its answer is obeyed: a payment that waits on the
// seller's tax form, or cannot clear, opens nothing and asks for no signature.
func TestClearanceIsObeyed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "10.00"}
		for verdict, want := range map[string]int{client.ClearNeedsForm: http.StatusConflict, client.ClearBlocked: http.StatusForbidden} {
			e.peers.set(func(s *stand) { s.verdict[seller] = verdict })
			code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
			if code != want || !strings.Contains(string(b), "clr_") {
				t.Fatalf("%s clearance = %d (%s), want %d naming the clearance", verdict, code, b, want)
			}
			if h.Get(protocol.HeaderPaymentRequired) != "" {
				t.Fatalf("a %s clearance still asked for a signature", verdict)
			}
		}
		var page struct {
			Jobs []Job `json:"jobs"`
		}
		e.must(200, http.MethodGet, "/v1/marketplace/jobs", member(buyer), nil, &page)
		if len(page.Jobs) != 0 {
			t.Fatalf("a job opened without clearance: %+v", page.Jobs)
		}
		// Refused terms never reach clearance at all.
		for _, bad := range []map[string]any{
			{"listing": l.ID, "brief": "Work", "amount": "0.001"},
			{"listing": l.ID, "brief": "", "amount": "10.00"},
			{"listing": l.ID, "brief": "Work", "amount": "10.00", "category": "refund"},
			{"listing": l.ID, "seller": seller, "brief": "Work", "amount": "10.00"},
			{"listing": l.ID, "brief": "Work", "amount": "10.00", "performed": "Germany"},
		} {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", bad); code != http.StatusBadRequest {
				t.Fatalf("hire %v = %d (%s), want 400", bad, code, b)
			}
		}
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(seller), "", in); code != http.StatusBadRequest {
			t.Fatalf("an org hired itself: %d", code)
		}
		if n := len(e.peers.asked()); n != 2 {
			t.Fatalf("%d clearances asked, want the two refused", n)
		}
		// The chain rail waits on its deployment, and says so.
		e.peers.set(func(s *stand) { delete(s.verdict, seller) })
		code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", map[string]any{"listing": l.ID, "brief": "Work",
			"amount": "10.00", "rail": "chain"})
		if code != http.StatusServiceUnavailable || !strings.Contains(string(b), "luxfi/standard") {
			t.Fatalf("a chain job = %d (%s), want 503 naming the missing escrow", code, b)
		}
	})
}

// A payment that is not the quote's, or does not stay valid long enough, opens
// nothing.
func TestAPaymentMustFitItsJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "10.00", "review": 86400}
		code, _, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d", code)
		}
		req := e.challengeOf(h)
		// Valid for an hour: not through a review window of a day. Valid for ten days:
		// through the review window, not through the arbiter's ruling on a dispute
		// raised at its end, which the buyer could otherwise wait out unpaid.
		for _, short := range []time.Duration{time.Hour, 10 * 24 * time.Hour} {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), e.pay(req, short), in); code != http.StatusPaymentRequired {
				t.Fatalf("a payment valid for %s = %d (%s), want 402", short, code, b)
			}
		}
		good := e.pay(req, 20*24*time.Hour)
		// Another amount than the quote's.
		other := map[string]any{"listing": l.ID, "brief": "Work", "amount": "11.00", "review": 86400}
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), good, other); code != http.StatusConflict {
			t.Fatalf("a payment sent with other terms = %d (%s), want 409", code, b)
		}
		// Another org presenting the buyer's payment finds no job.
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member("initech"), good, in); code != http.StatusNotFound {
			t.Fatalf("another org's payment = %d (%s), want 404", code, b)
		}
		// A wallet named that did not sign it.
		w := e.createWallet(buyer)
		named := map[string]any{"listing": l.ID, "brief": "Work", "amount": "10.00", "review": 86400, "wallet": w}
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), good, named); code != http.StatusForbidden {
			t.Fatalf("a payment not signed by the named wallet = %d (%s), want 403", code, b)
		}
		var j Job
		code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), good, in)
		if code != http.StatusCreated || json.Unmarshal(b, &j) != nil || j.Status != StatusOpen {
			t.Fatalf("the quote's own payment = %d (%s)", code, b)
		}
		// Sent again, it is the same job, not a second one.
		var again Job
		code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), good, in)
		if code != http.StatusCreated || json.Unmarshal(b, &again) != nil || again.ID != j.ID {
			t.Fatalf("the same payment again = %d (%s), want job %s", code, b, j.ID)
		}
	})
}

// A job is its two parties' alone: a third org reads none of it and does nothing
// to it, and each party sees it on its own side.
func TestJobsAreThePartiesAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "10.00"}, 30*24*time.Hour)
		for _, act := range []string{"accept", "decline", "cancel", "release", "refund"} {
			e.act(j.ID, act, member("initech"), nil, http.StatusNotFound)
		}
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/jobs/"+j.ID, member("initech"), "", nil); code != http.StatusNotFound {
			t.Fatalf("a third org read the job: %d", code)
		}
		var page struct {
			Jobs []Job `json:"jobs"`
		}
		for _, tc := range []struct {
			as   who
			role string
			n    int
		}{{member("initech"), "buyer", 0}, {member("initech"), "seller", 0}, {member(buyer), "buyer", 1},
			{member(buyer), "seller", 0}, {member(seller), "seller", 1}, {member(seller), "buyer", 0}} {
			e.must(200, http.MethodGet, "/v1/marketplace/jobs?role="+tc.role, tc.as, nil, &page)
			if len(page.Jobs) != tc.n {
				t.Fatalf("%s as %s sees %d jobs, want %d", tc.as.org, tc.role, len(page.Jobs), tc.n)
			}
		}
		// The seller may decline; the buyer may not.
		e.act(j.ID, "decline", member(buyer), nil, http.StatusForbidden)
		if d := e.act(j.ID, "decline", member(seller), map[string]any{"reason": "Booked"}, http.StatusOK); d.Status != StatusDeclined {
			t.Fatalf("declined %+v", d)
		}
		e.act(j.ID, "feedback", member(buyer), map[string]any{"rating": 3}, http.StatusConflict)
	})
}

// A release whose money moved but whose job was never marked — the process died
// between the two — is finished as the release it was: neither the seller nor the
// clock can refund a job the buyer already paid.
func TestAPaidJobIsNeverRefunded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "20.00"}, 30*24*time.Hour)
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		row, _, err := mounted.State.store.job(context.Background(), j.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The rail settles; the marketplace never hears back.
		out, err := x402peer.X402Settle(cloud.For(context.Background(), buyer), &client.SettleIn{Resource: client.JobResource(j.ID), Payment: row.payment})
		if err != nil || !out.OK {
			t.Fatalf("settle behind the job's back = %+v %v", out, err)
		}
		e.act(j.ID, "refund", member(seller), nil, http.StatusConflict)
		var now Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+j.ID, member(buyer), nil, &now)
		if now.Status != StatusReleased || now.Escrow.TxHash == "" {
			t.Fatalf("a paid job reads %s, want released with its receipt", now.Status)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(2000)) != 0 {
			t.Fatalf("seller holds %s, want the 20 paid once", got)
		}
	})
}

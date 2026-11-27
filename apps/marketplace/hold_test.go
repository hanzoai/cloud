package marketplace

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
)

// A funded job holds its amount. One balance opens one job's worth of work, not
// three; what the buyer spends afterwards comes out of what is left, never out of
// the job; and a job whose clock runs out is paid by the sweep however often it
// failed before, so a seller who delivered is never left unpaid.
func TestAFundedJobHoldsItsAmount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		hundred := map[string]any{"listing": l.ID, "brief": "Work", "amount": "100.00"}
		j := e.hire(buyer, hundred, 30*24*time.Hour)
		if got := e.balance(buyer, buyer); !got.IsZero() {
			t.Fatalf("a funded job left %s to spend, want its 100 set aside", got)
		}
		// The same balance promised to a second job is refused, and opens nothing.
		for range 2 {
			code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", map[string]any{"listing": l.ID, "brief": "More", "amount": "100.00"})
			if code != http.StatusPaymentRequired {
				t.Fatalf("quote = %d (%s)", code, b)
			}
			code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), e.pay(e.challengeOf(h), 30*24*time.Hour),
				map[string]any{"listing": l.ID, "brief": "More", "amount": "100.00"})
			if code != http.StatusPaymentRequired || !strings.Contains(string(b), "insufficient_funds") {
				t.Fatalf("a second job on a balance already promised = %d (%s), want 402 insufficient_funds", code, b)
			}
		}
		var page jobPage
		e.must(200, http.MethodGet, "/v1/marketplace/jobs?role=seller", member(seller), nil, &page)
		if len(page.Jobs) != 1 || page.Jobs[0].ID != j.ID {
			t.Fatalf("the seller sees %d jobs, want the one that is paid for", len(page.Jobs))
		}
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
		// The buyer spends on metered calls after the job opened: the wallet goes
		// below zero, and the job's money is untouched.
		if err := e.fin.RecordUsage(context.Background(), money.UsageInput{Org: buyer, Subject: buyer, Amount: money.FromCents(5_000),
			Currency: "usd", Ref: "msg_after"}); err != nil {
			t.Fatal(err)
		}
		// The sweep had failed this job forty times before; it is tried all the same.
		e.age(j.ID, "delivered_at", defaultReview+time.Hour)
		if _, err := mounted.State.store.db.Exec(`UPDATE jobs SET tries = 40, retry_at = 0 WHERE id = $1`, j.ID); err != nil {
			t.Fatal(err)
		}
		e.tick()
		var now Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+j.ID, member(seller), nil, &now)
		if now.Status != StatusReleased {
			t.Fatalf("a delivered job past its review window is %s, want released by the clock", now.Status)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(10_000)) != 0 {
			t.Fatalf("seller holds %s, want the 100 set aside for it", got)
		}
	})
}

// Every way a job ends unpaid returns what it set aside to the buyer: the seller
// declining, the buyer cancelling, the deadline passing before acceptance or
// before delivery, the seller refunding, the arbiter refunding, and a dispute
// lapsing.
func TestAJobEndedUnpaidReturnsItsAmount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(700))
		open := func(deadline bool) Job {
			t.Helper()
			in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "1.00"}
			if deadline {
				in["deadline"] = time.Now().Add(2 * time.Hour).Unix()
			}
			return e.hire(buyer, in, 30*24*time.Hour)
		}
		declined, cancelled, lapsedOpen, lapsedAccepted := open(false), open(false), open(true), open(true)
		refunded, ruled, unruled := open(false), open(false), open(false)
		if got := e.balance(buyer, buyer); !got.IsZero() {
			t.Fatalf("seven funded jobs of 1.00 left %s to spend", got)
		}
		e.act(declined.ID, "decline", member(seller), map[string]any{"reason": "booked"}, http.StatusOK)
		e.act(cancelled.ID, "cancel", member(buyer), nil, http.StatusOK)
		for _, j := range []Job{lapsedAccepted, refunded, ruled, unruled} {
			e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		}
		e.act(refunded.ID, "refund", member(seller), nil, http.StatusOK)
		for _, j := range []Job{ruled, unruled} {
			e.act(j.ID, "dispute", member(buyer), map[string]any{"reason": "late"}, http.StatusOK)
		}
		e.act(ruled.ID, "resolve", arbiter, map[string]any{"outcome": "refund"}, http.StatusOK)
		e.age(lapsedOpen.ID, "deadline", 3*time.Hour)
		e.age(lapsedAccepted.ID, "deadline", 3*time.Hour)
		e.age(unruled.ID, "disputed_at", disputeTimeout+time.Hour)
		e.tick()
		for j, want := range map[string]string{declined.ID: StatusDeclined, cancelled.ID: StatusCancelled, lapsedOpen.ID: StatusCancelled,
			lapsedAccepted.ID: StatusRefunded, refunded.ID: StatusRefunded, ruled.ID: StatusRefunded, unruled.ID: StatusRefunded} {
			var now Job
			e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+j, member(buyer), nil, &now)
			if now.Status != want {
				t.Fatalf("job %s is %s, want %s", j, now.Status, want)
			}
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(700)) != 0 {
			t.Fatalf("the buyer holds %s after seven jobs ended unpaid, want its 7.00 back", got)
		}
		if got := e.balance(seller, wallet); !got.IsZero() {
			t.Fatalf("the seller was paid %s by jobs that ended unpaid", got)
		}
	})
}

// Clearance is asked at every step the money takes, not only at the quote: a
// seller blocked after the quote is not hired by it, one blocked after the job
// opened is not paid by its release, and the clock puts such a job before the
// arbiter — who pays it once it clears again, or lets it lapse to a refund.
func TestAClearanceIsAskedAtEveryStep(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(1_000))
		block := func(verdict string) {
			e.peers.set(func(s *stand) {
				if verdict == "" {
					delete(s.verdict, seller)
					return
				}
				s.verdict[seller] = verdict
			})
		}
		in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "5.00"}
		code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d (%s)", code, b)
		}
		payment := e.pay(e.challengeOf(h), 30*24*time.Hour)
		block(client.ClearBlocked)
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in); code != http.StatusForbidden {
			t.Fatalf("funding a quote whose seller was blocked since = %d (%s), want 403", code, b)
		}
		// The same terms asked again answer the quote they have, and funding it is
		// refused all the same.
		code, b, h = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("the same terms asked again = %d (%s), want the quote they have", code, b)
		}
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), e.pay(e.challengeOf(h), 30*24*time.Hour), in); code != http.StatusForbidden {
			t.Fatalf("funding the quote asked again of a blocked seller = %d (%s), want 403", code, b)
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(1_000)) != 0 {
			t.Fatalf("a refused funding set %s aside", money.FromCents(1_000).Sub(got))
		}
		block("")
		j := e.hire(buyer, in, 30*24*time.Hour)
		stuck := e.hire(buyer, in, 30*24*time.Hour)
		for _, x := range []Job{j, stuck} {
			e.act(x.ID, "accept", member(seller), nil, http.StatusOK)
			e.act(x.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
		}
		block(client.ClearBlocked)
		e.act(j.ID, "release", member(buyer), nil, http.StatusForbidden)
		block(client.ClearNeedsForm)
		e.act(j.ID, "release", member(buyer), nil, http.StatusConflict)
		block(client.ClearBlocked)
		if got := e.balance(seller, wallet); !got.IsZero() {
			t.Fatalf("a seller blocked since the job opened was paid %s", got)
		}
		// The review window closes on a payment that does not clear: the clock hands
		// both jobs to the arbiter instead of paying them.
		for _, x := range []Job{j, stuck} {
			e.age(x.ID, "delivered_at", defaultReview+time.Hour)
		}
		e.tick()
		for _, x := range []Job{j, stuck} {
			var now Job
			e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+x.ID, member(seller), nil, &now)
			if now.Status != StatusDisputed || now.Dispute == nil || now.Dispute.By != bySweep || !strings.Contains(now.Dispute.Reason, "no longer clears") {
				t.Fatalf("a delivered job whose payment no longer clears is %s (%+v), want disputed by the clock", now.Status, now.Dispute)
			}
		}
		e.act(j.ID, "resolve", arbiter, map[string]any{"outcome": "release"}, http.StatusForbidden)
		block("")
		r := e.act(j.ID, "resolve", arbiter, map[string]any{"outcome": "release"}, http.StatusOK)
		if r.Status != StatusReleased || r.Clearance == j.Clearance {
			t.Fatalf("released %s on clearance %q, want the one asked at release", r.Status, r.Clearance)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(500)) != 0 {
			t.Fatalf("seller holds %s, want the one job that cleared", got)
		}
		// Nobody rules on the other, and it still does not clear when the arbiter's
		// time runs out: it lapses, and the buyer's money comes back.
		block(client.ClearBlocked)
		e.age(stuck.ID, "disputed_at", disputeTimeout+time.Hour)
		e.tick()
		var now Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+stuck.ID, member(buyer), nil, &now)
		if now.Status != StatusRefunded {
			t.Fatalf("an unruled dispute is %s, want refunded", now.Status)
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(500)) != 0 {
			t.Fatalf("buyer holds %s, want 10.00 less the one job paid", got)
		}
	})
}

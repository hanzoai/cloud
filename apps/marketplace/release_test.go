package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

// A buyer whose wallet cannot cover a job opens none: the payment is refused when
// it is agreed, nothing is set aside and nothing paid, and the same signed payment
// opens the job once the wallet covers it — which then pays its seller whatever
// the buyer spends in between.
func TestAnUnfundedBuyerOpensNoJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "1000000.00"}
		code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d (%s)", code, b)
		}
		payment := e.pay(e.challengeOf(h), 30*24*time.Hour)
		code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in)
		if code != http.StatusPaymentRequired || !strings.Contains(string(b), "insufficient_funds") {
			t.Fatalf("a job its wallet cannot cover = %d (%s), want 402 insufficient_funds", code, b)
		}
		var page jobPage
		e.must(200, http.MethodGet, "/v1/marketplace/jobs?role=seller", member(seller), nil, &page)
		if len(page.Jobs) != 0 {
			t.Fatalf("the seller was offered %+v by a buyer with nothing", page.Jobs)
		}
		million := money.FromCents(100_000_000)
		e.fund(buyer, million)
		var j Job
		code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in)
		if code != http.StatusCreated || json.Unmarshal(b, &j) != nil || j.Status != StatusOpen {
			t.Fatalf("the same payment, covered = %d (%s), want the open job", code, b)
		}
		if got := e.balance(buyer, buyer); !got.IsZero() {
			t.Fatalf("the buyer can still spend %s of a million set aside", got)
		}
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
		if r := e.act(j.ID, "release", member(buyer), nil, http.StatusOK); r.Status != StatusReleased {
			t.Fatalf("released %s", r.Status)
		}
		if got := e.balance(seller, wallet); got.Cmp(million) != 0 {
			t.Fatalf("seller holds %s, want the million paid once", got)
		}
	})
}

// A buyer's release and a seller's refund raced on the same jobs end every job
// one way, with the money where the job says: no job reads refunded while its
// payment settled, and the buyer paid exactly the jobs that read released.
func TestARefundRacedAgainstAReleaseEndsOneWay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(1_000_000))
		before := e.balance(buyer, buyer)
		const n = 24
		var jobs []Job
		for range n {
			j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "1.00"}, 30*24*time.Hour)
			e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
			jobs = append(jobs, j)
		}
		var wg sync.WaitGroup
		for _, j := range jobs {
			start := make(chan struct{})
			for _, a := range []struct {
				act string
				as  who
			}{{"release", member(buyer)}, {"refund", member(seller)}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, _, _ = e.send(http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/"+a.act, a.as)
				}()
			}
			close(start)
		}
		wg.Wait()
		var receipts struct {
			Settlements []x402.Receipt `json:"settlements"`
		}
		e.must(200, http.MethodGet, "/v1/x402/settlements?role=payer", member(buyer), nil, &receipts)
		paid := map[string]bool{}
		for _, r := range receipts.Settlements {
			if id, ok := client.JobOf(r.Resource); ok {
				paid[id] = true
			}
		}
		released := 0
		for _, j := range jobs {
			var now Job
			e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+j.ID, member(buyer), nil, &now)
			switch now.Status {
			case StatusReleased:
				released++
				if !paid[j.ID] {
					t.Errorf("job %s reads released with no settlement", j.ID)
				}
			case StatusRefunded:
				if paid[j.ID] {
					t.Errorf("job %s reads refunded while its payment settled", j.ID)
				}
			default:
				t.Errorf("job %s ended %s", j.ID, now.Status)
			}
		}
		// What a refunded job set aside came back; what a released one set aside paid.
		want := money.FromCents(int64(released) * 100)
		if got := before.Sub(e.balance(buyer, buyer)); got.Cmp(want) != 0 {
			t.Fatalf("buyer paid %s for %d released jobs", got, released)
		}
		if got := e.balance(seller, wallet); got.Cmp(want) != 0 {
			t.Fatalf("seller holds %s for %d released jobs", got, released)
		}
	})
}

// send drives one request as w with an empty body, safe from goroutines.
func (e *env) send(method, path string, w who) (int, []byte, error) {
	hr := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{}`)))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Org-Id", w.org)
	hr.Header.Set("X-User-Id", "u_"+w.org)
	hr.Header.Set("X-User-Owner", w.org)
	resp, err := e.app.Test(hr, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// Jobs that cannot move never stand in front of one that can. A step the sweep
// fails waits out a backoff, behind every job that has not failed, so a flood of
// unsettleable jobs costs the sweep one try each and no other job's clock stops.
func TestStuckJobsStarveNoClock(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		// sweepBatch and then some delivered jobs whose payment can never settle: the
		// chain rail is not bound here, so every release of one fails.
		stuck := sweepBatch + 100
		past := time.Now().Add(-maxReview - time.Hour).Unix()
		st := mounted.State.store
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for i := range stuck {
			if _, err := tx.Exec(`INSERT INTO jobs (id, title, buyer_org, seller_org, amount, category, rail, status, review, recipient,
			clearance, delivered_at, created_at, updated_at) VALUES ($1, 'x', 'sybil', 'shill', $2, 'service', $3, $4, 1, 'w', 'c', $5, $6, $7)`,
				fmt.Sprintf("job_stuck%04d", i), money.FromCents(1).AttoString(), RailChain, StatusDelivered, past, past, past); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		e.fund(buyer, money.FromCents(10_000))
		good := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "5.00"}, 30*24*time.Hour)
		e.act(good.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(good.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
		e.age(good.ID, "delivered_at", defaultReview+time.Hour)

		// Both sweeps run at one instant. A sweep reads the time once and defers each
		// failure to a minute after it, and one sweep of sweepBatch failing jobs on a
		// loaded runner has taken longer than that minute (CI run 17680: 94s), after
		// which those jobs are due again for the right reason and the second sweep
		// retries them. What this test is about is the order a sweep asks in, not the
		// wall clock, so the clock is fixed.
		began := time.Now()
		e.tickAt(began) // the oldest sweepBatch of the stuck ones, each failing once
		failed := map[string]bool{}
		rows, err := st.db.Query(`SELECT id FROM jobs WHERE tries > 0`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			failed[id] = true
		}
		_ = rows.Close()
		if len(failed) != sweepBatch {
			t.Fatalf("one sweep failed %d jobs, want a batch of %d", len(failed), sweepBatch)
		}
		due, err := st.due(context.Background(), began.Unix())
		if err != nil {
			t.Fatal(err)
		}
		in := false
		for _, d := range due {
			in = in || d.ID == good.ID
			if failed[d.ID] {
				t.Fatalf("a job that just failed, %s, is due again at once", d.ID)
			}
		}
		if !in {
			t.Fatalf("the honest job is not among the %d due", len(due))
		}
		e.tickAt(began)
		var j Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+good.ID, member(seller), nil, &j)
		if j.Status != StatusReleased {
			t.Fatalf("an honest job past its review window is %s after two sweeps behind %d stuck ones", j.Status, stuck)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(500)) != 0 {
			t.Fatalf("seller holds %s", got)
		}
		var tries int
		var retry int64
		if err := st.db.QueryRow(`SELECT tries, retry_at FROM jobs WHERE id = 'job_stuck0000'`).Scan(&tries, &retry); err != nil {
			t.Fatal(err)
		}
		if want := began.Unix() + int64(time.Minute.Seconds()); tries != 1 || retry != want {
			t.Fatalf("a failed job: tries %d, retry at %d, want one try and a wait of a minute (%d)", tries, retry, want)
		}
	})
}

// A seller rotating its payout wallet's keys strands none of its open jobs: the
// buyer's payment names the address the wallet had when the job was funded, and
// it settles to that same wallet after the rotation, by the buyer or the clock.
func TestAKeyRotationStrandsNoJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		in := map[string]any{"listing": l.ID, "brief": "Work", "amount": "10.00"}
		byBuyer, byClock := e.hire(buyer, in, 30*24*time.Hour), e.hire(buyer, in, 30*24*time.Hour)
		for _, j := range []Job{byBuyer, byClock} {
			e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
			e.act(j.ID, "deliver", member(seller), map[string]any{"note": "done"}, http.StatusOK)
		}
		var w struct {
			Address string `json:"address"`
		}
		e.must(200, http.MethodPost, "/v1/wallet/"+wallet+"/keys", admin(seller), map[string]any{}, &w)
		if strings.EqualFold(w.Address, byBuyer.Escrow.PayTo) {
			t.Fatal("the rotation kept the address")
		}
		if r := e.act(byBuyer.ID, "release", member(buyer), nil, http.StatusOK); r.Status != StatusReleased {
			t.Fatalf("released %s", r.Status)
		}
		e.age(byClock.ID, "delivered_at", defaultReview+time.Hour)
		e.tick()
		var j Job
		e.must(200, http.MethodGet, "/v1/marketplace/jobs/"+byClock.ID, member(seller), nil, &j)
		if j.Status != StatusReleased {
			t.Fatalf("the clock's release after a rotation left the job %s", j.Status)
		}
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(2000)) != 0 {
			t.Fatalf("seller holds %s, want both jobs' 20", got)
		}
	})
}

// A SuperAdmin inspecting an org reads its jobs and takes none of its parties'
// steps — least of all a release, which spends the buyer's wallet. The platform's
// act on a job is the arbiter's ruling.
func TestASuperAdminTakesNoPartysStep(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "30.00"}, 30*24*time.Hour)
		for _, org := range []string{buyer, seller} {
			sudo := who{org: org, admin: true, super: true}
			if code, b, _ := e.req(http.MethodGet, "/v1/marketplace/jobs/"+j.ID, sudo, "", nil); code != http.StatusOK {
				t.Fatalf("a SuperAdmin inspecting %s reads its job = %d (%s)", org, code, b)
			}
		}
		e.act(j.ID, "accept", who{org: seller, admin: true, super: true}, nil, http.StatusForbidden)
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		sudo := who{org: buyer, admin: true, super: true}
		for _, act := range []string{"release", "cancel", "feedback"} {
			e.act(j.ID, act, sudo, map[string]any{"rating": 5}, http.StatusForbidden)
		}
		e.act(j.ID, "dispute", sudo, map[string]any{"reason": "mine now"}, http.StatusForbidden)
		if got := e.balance(seller, wallet); !got.IsZero() {
			t.Fatalf("a SuperAdmin's step paid the seller %s", got)
		}
		e.act(j.ID, "dispute", member(buyer), map[string]any{"reason": "late"}, http.StatusOK)
		if r := e.act(j.ID, "resolve", arbiter, map[string]any{"outcome": "release"}, http.StatusOK); r.Status != StatusReleased ||
			!strings.HasPrefix(r.History[len(r.History)-1].By, "arbiter:") {
			t.Fatalf("the arbiter's ruling = %+v", r.History)
		}
	})
}

// A hire can be paid over MCP, where no header of the caller's own is sent: the
// 402's message carries the terms to sign, and the signed payment rides the hire's
// own payment field. The same terms asked again answer the same quote, so a
// caller that asks twice clears and quotes the work once, and its payment is
// cleared again of that clearance when it is funded.
func TestAHireOverMCPIsPaid(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		args := map[string]any{"listing": l.ID, "brief": "Work", "amount": "10.00"}
		text, isError := e.mcp(buyer, args)
		if !isError || !strings.Contains(text, client.HeaderPaymentRequired+"): ") {
			t.Fatalf("an unpaid hire over MCP = %q, want the terms in the message", text)
		}
		terms := text[strings.LastIndex(text, "): ")+3:]
		var required protocol.PaymentRequired
		if err := protocol.DecodeHeader(terms, &required); err != nil || len(required.Accepts) != 1 {
			t.Fatalf("the terms in the message do not decode: %v (%q)", err, terms)
		}
		again, _ := e.mcp(buyer, args)
		if again[strings.LastIndex(again, "): ")+3:] != terms {
			t.Fatal("the same terms asked again quoted the work again")
		}
		// The same quote, cleared once.
		if n := len(e.peers.asked()); n != 1 {
			t.Fatalf("%d clearances asked for two asks, want 1", n)
		}
		e.resource = required.Resource.URL
		args["payment"] = e.pay(required.Accepts[0], 30*24*time.Hour)
		text, isError = e.mcp(buyer, args)
		var j Job
		if isError || json.Unmarshal([]byte(text), &j) != nil || j.Status != StatusOpen {
			t.Fatalf("a paid hire over MCP = %q, want the open job", text)
		}
		if id, _ := client.JobOf(required.Resource.URL); j.ID != id {
			t.Fatalf("opened %s, the quote was %s", j.ID, id)
		}
		if asked := e.peers.asked(); len(asked) != 2 || asked[0].in.Again != "" || asked[1].in.Again == "" || asked[1].in.Again != asked[0].id {
			t.Fatalf("clearances asked %+v, want the quote's and the funding's asked again of it", asked)
		}
		// Over REST the same refusal carries the terms in the body, beside the header.
		code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", map[string]any{"listing": l.ID, "brief": "More", "amount": "10.00"})
		var body struct {
			Accepts []protocol.PaymentRequirements `json:"accepts"`
			Terms   string                         `json:"paymentRequired"`
		}
		if code != http.StatusPaymentRequired || json.Unmarshal(b, &body) != nil || len(body.Accepts) != 1 || body.Terms != h.Get(protocol.HeaderPaymentRequired) {
			t.Fatalf("the REST 402 = %d (%s)", code, b)
		}
	})
}

// mcp calls post_marketplace_jobs over the MCP server as a member of org, and
// answers the tool result's text and whether it is an error.
func (e *env) mcp(org string, args map[string]any) (string, bool) {
	e.t.Helper()
	rpc, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": opHire, "arguments": args}})
	hr := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(rpc))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org)
	hr.Header.Set("X-User-Owner", org)
	resp, err := e.app.Test(hr, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil || len(out.Result.Content) != 1 {
		e.t.Fatalf("tools/call = %d %s", resp.StatusCode, b)
	}
	return out.Result.Content[0].Text, out.Result.IsError
}

// A payment cleared only net of withholding is one x402 cannot make — it pays one
// amount to one payee, and nothing on the rail withholds — so the hire is refused
// and nothing opens, rather than paying the seller the gross.
func TestAWithheldPaymentIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, wallet := e.shopfront()
		e.fund(buyer, money.FromCents(100_000))
		e.peers.set(func(s *stand) { s.withheld[seller] = "24.00" })
		code, b, h := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", map[string]any{"listing": l.ID, "brief": "Work", "amount": "100.00"})
		if code != http.StatusConflict || !strings.Contains(string(b), "withheld") || h.Get(protocol.HeaderPaymentRequired) != "" {
			t.Fatalf("a hire cleared net of 24.00 withheld = %d (%s), want 409 and nothing to sign", code, b)
		}
		var page struct {
			Jobs []Job `json:"jobs"`
		}
		e.must(200, http.MethodGet, "/v1/marketplace/jobs", member(buyer), nil, &page)
		if len(page.Jobs) != 0 {
			t.Fatalf("a job opened: %+v", page.Jobs)
		}
		if got := e.balance(seller, wallet); !got.IsZero() {
			t.Fatalf("seller holds %s", got)
		}
		e.peers.set(func(s *stand) { delete(s.withheld, seller) })
		e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Work", "amount": "100.00"}, 30*24*time.Hour)
	})
}

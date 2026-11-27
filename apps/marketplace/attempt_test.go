package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// jobsAs lists org's jobs as role.
func (e *env) jobsAs(org, role string) []Job {
	e.t.Helper()
	var page jobPage
	e.must(200, http.MethodGet, "/v1/marketplace/jobs?role="+role, member(org), nil, &page)
	return page.Jobs
}

// find is the job with id in jobs, if it is there.
func find(jobs []Job, id string) (Job, bool) {
	i := slices.IndexFunc(jobs, func(j Job) bool { return j.ID == id })
	if i < 0 {
		return Job{}, false
	}
	return jobs[i], true
}

// One attempt is one job, however often it is sent. The buyer's funding commits
// its hold and the answer is lost, as a gateway timing out while the platform is
// still funding; the buyer reloads, sees the attempt in flight on its own list,
// and sends the same request again: the same quote, then the same job, with its
// amount set aside once. A new signature for the job it opened opens nothing more,
// and another request under the attempt is refused.
func TestAnAttemptIsOneJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		h := grab(t)
		in := map[string]any{"listing": l.ID, "brief": "One attempt", "amount": "30.00", "attempt": "att-8f14e45f"}
		quote := func(body map[string]any) (int, string) {
			t.Helper()
			code, b, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", body)
			if code != http.StatusPaymentRequired {
				return code, string(b)
			}
			e.challengeOf(hd)
			id, _ := client.JobOf(e.resource)
			return code, id
		}
		held := func(want int64, why string) {
			t.Helper()
			if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(10_000-want)) != 0 {
				t.Fatalf("%s: the buyer holds %s, want %s set aside", why, got, money.FromCents(want))
			}
		}

		code, x := quote(in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d %s", code, x)
		}
		cleared := len(e.peers.asked())
		if code, again := quote(in); code != http.StatusPaymentRequired || again != x {
			t.Fatalf("the attempt asked again = %d %s, want the quote %s", code, again, x)
		}
		if n := len(e.peers.asked()); n != cleared {
			t.Fatalf("the attempt asked again was cleared again (%d clearances, want %d)", n, cleared)
		}
		j, ok := find(e.jobsAs(buyer, "buyer"), x)
		if !ok || j.Status != StatusQuoted || j.Attempt != "att-8f14e45f" {
			t.Fatalf("the buyer's list shows the quote as %+v (listed %v), want quoted under its attempt", j, ok)
		}
		if _, ok := find(e.jobsAs(seller, "seller"), x); ok {
			t.Fatal("the seller's list shows the buyer's quote")
		}
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/jobs/"+x, member(seller), "", nil); code != http.StatusNotFound {
			t.Fatalf("the seller reads the buyer's quote = %d, want 404", code)
		}

		// The funding commits its hold, and its answer is lost.
		payment := e.pay(e.challengeOf(func() http.Header {
			_, _, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
			return hd
		}()), 30*24*time.Hour)
		h.set(func(h *hand) { h.lose = true })
		code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in)
		h.set(func(h *hand) { h.lose = false })
		if code != http.StatusServiceUnavailable {
			t.Fatalf("a funding whose answer was lost = %d %s, want 503", code, b)
		}
		held(3_000, "after a funding whose answer was lost")
		j, ok = find(e.jobsAs(buyer, "buyer"), x)
		if !ok || j.Status != StatusFunding {
			t.Fatalf("the buyer's list shows the attempt in flight as %+v (listed %v), want funding", j, ok)
		}
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/jobs/"+x, member(seller), "", nil); code != http.StatusNotFound {
			t.Fatalf("the seller reads a job being funded = %d, want 404", code)
		}

		// The buyer reloads: the attempt answers its quote, and its payment sent again
		// opens the job it was for, holding once.
		if code, again := quote(in); code != http.StatusPaymentRequired || again != x {
			t.Fatalf("the attempt asked again while funding = %d %s, want the quote %s", code, again, x)
		}
		code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in)
		if code != http.StatusCreated {
			t.Fatalf("the payment sent again = %d %s, want 201", code, b)
		}
		var opened Job
		if err := json.Unmarshal(b, &opened); err != nil || opened.ID != x || opened.Status != StatusOpen {
			t.Fatalf("the payment sent again opened %+v (%v), want %s open", opened, err, x)
		}
		held(3_000, "after the attempt opened its job")
		if got := strings.Join(statuses(opened), ","); got != "quoted,funding,open" {
			t.Fatalf("the job's history is %s", got)
		}

		// Sent again once it opened — with no payment, the same one, or a new
		// signature — the attempt answers its job, and nothing more is held.
		for _, proof := range []string{"", payment, e.pay(e.challengeOf(func() http.Header {
			_, _, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", map[string]any{
				"listing": l.ID, "brief": "Another hire", "amount": "30.00"})
			return hd
		}()), 30*24*time.Hour)} {
			code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), proof, in)
			var again Job
			if code != http.StatusCreated || json.Unmarshal(b, &again) != nil || again.ID != x {
				t.Fatalf("the attempt sent again after it opened (payment %t) = %d %s, want job %s", proof != "", code, b, x)
			}
		}
		held(3_000, "after the attempt was sent again three times")
		opens := 0
		for _, j := range e.jobsAs(buyer, "buyer") {
			if j.Status == StatusOpen {
				opens++
			}
		}
		if opens != 1 {
			t.Fatalf("the buyer has %d open jobs, want the attempt's one", opens)
		}

		// Another request under the attempt is refused; another attempt is another job.
		other := map[string]any{"listing": l.ID, "brief": "Something else", "amount": "30.00", "attempt": "att-8f14e45f"}
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", other); code != http.StatusUnprocessableEntity {
			t.Fatalf("another request under a used attempt = %d %s, want 422", code, b)
		}
		second := map[string]any{"listing": l.ID, "brief": "One attempt", "amount": "30.00", "attempt": "att-c9f0f895"}
		code, y := quote(second)
		if code != http.StatusPaymentRequired || y == x {
			t.Fatalf("another attempt = %d %s, want a quote of its own", code, y)
		}
		// A payment for one attempt's job, sent under another or none, is refused.
		for _, body := range []map[string]any{second, {"listing": l.ID, "brief": "One attempt", "amount": "30.00"}} {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, body); code != http.StatusConflict {
				t.Fatalf("the first attempt's payment under %v = %d %s, want 409", body["attempt"], code, b)
			}
		}
		for _, bad := range []string{strings.Repeat("a", 129), "has space", "ümlaut"} {
			body := map[string]any{"listing": l.ID, "brief": "Bad", "amount": "1.00", "attempt": bad}
			if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", body); code != http.StatusBadRequest {
				t.Fatalf("attempt %q = %d, want 400", bad, code)
			}
		}
	})
}

// An attempt whose quote is past its hour is quoted anew, and whatever the old
// quote's payment set aside is given back first, so the attempt still holds its
// amount once.
func TestAStaleAttemptIsQuotedAnew(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		h := grab(t)
		in := map[string]any{"listing": l.ID, "brief": "Stale", "amount": "40.00", "attempt": "att-stale"}
		code, _, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d", code)
		}
		payment := e.pay(e.challengeOf(hd), 30*24*time.Hour)
		x, _ := client.JobOf(e.resource)
		h.set(func(h *hand) { h.lose = true })
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), payment, in); code != http.StatusServiceUnavailable {
			t.Fatalf("a funding whose answer was lost = %d %s", code, b)
		}
		h.set(func(h *hand) { h.lose = false })
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(6_000)) != 0 {
			t.Fatalf("the buyer holds %s, want 40.00 set aside", got)
		}
		e.age(x, "created_at", quoteTTL+time.Minute)
		code, b, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("the attempt asked again past its hour = %d %s", code, b)
		}
		req := e.challengeOf(hd)
		y, _ := client.JobOf(e.resource)
		if y == x {
			t.Fatalf("the attempt past its hour answered its old quote %s", x)
		}
		if _, found, _ := mounted.State.store.job(context.Background(), x); found {
			t.Fatal("the old quote was kept")
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(10_000)) != 0 {
			t.Fatalf("the buyer holds %s after the old quote was given up, want all of it back", got)
		}
		code, b, _ = e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), e.pay(req, 30*24*time.Hour), in)
		if code != http.StatusCreated {
			t.Fatalf("funding the new quote = %d %s", code, b)
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(6_000)) != 0 {
			t.Fatalf("the buyer holds %s, want 40.00 set aside once", got)
		}
	})
}

// A store whose jobs were written before attempts were kept opens, and its jobs
// read as made under none.
func TestAJobsStoreFromBeforeAttemptsOpens(t *testing.T) {
	// A file written by an older release exists on SQLite alone.
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	db, err := cek.Open(namespace.System(), "marketplace", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE jobs (
  id TEXT PRIMARY KEY, listing TEXT NOT NULL DEFAULT '', title TEXT NOT NULL, buyer_org TEXT NOT NULL,
  seller_org TEXT NOT NULL, amount TEXT NOT NULL, currency TEXT NOT NULL DEFAULT 'USD', category TEXT NOT NULL,
  rail TEXT NOT NULL, status TEXT NOT NULL, brief TEXT NOT NULL DEFAULT '', performed TEXT NOT NULL DEFAULT '',
  deadline INTEGER NOT NULL DEFAULT 0, review INTEGER NOT NULL, recipient TEXT NOT NULL, clearance TEXT NOT NULL,
  payment TEXT NOT NULL DEFAULT '', payer TEXT NOT NULL DEFAULT '', valid_before INTEGER NOT NULL DEFAULT 0,
  network TEXT NOT NULL DEFAULT '', asset TEXT NOT NULL DEFAULT '', pay_to TEXT NOT NULL DEFAULT '',
  settlement TEXT NOT NULL DEFAULT '', delivery_note TEXT NOT NULL DEFAULT '', delivery_url TEXT NOT NULL DEFAULT '',
  delivery_hash TEXT NOT NULL DEFAULT '', delivered_at INTEGER NOT NULL DEFAULT 0, dispute_reason TEXT NOT NULL DEFAULT '',
  disputed_by TEXT NOT NULL DEFAULT '', disputed_at INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL, tries INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0,
  ending TEXT NOT NULL DEFAULT '', contested INTEGER NOT NULL DEFAULT 0);
INSERT INTO jobs (id, title, buyer_org, seller_org, amount, category, rail, status, review, recipient, clearance, created_at, updated_at)
  VALUES ('job_old', 'Old', 'acme', 'globex', '1000000000000000000', 'service', 'x402', 'open', 259200, 'wal_1', 'clr_1', 1, 1);`); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s, err := Open(dir, testMaster, testKMS)
	if err != nil {
		t.Fatalf("Open a store from before attempts: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	j, found, err := s.job(context.Background(), "job_old")
	if err != nil || !found || j.Attempt != "" || j.Status != StatusOpen {
		t.Fatalf("the old job reads %+v found=%v err=%v", j.Job, found, err)
	}
	if _, found, err := s.attempted(context.Background(), "acme", ""); err != nil || found {
		t.Fatalf("an old job is found by an empty attempt: found=%v err=%v", found, err)
	}
}

package finance

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
)

// TestFundingClass locks the provider-level funding classification the
// /v1/admin/usage/funding endpoint derives from the credit ledger.
func TestFundingClass(t *testing.T) {
	cases := []struct {
		name string
		pc   ProviderCredit
		want string
	}{
		{"grant with remaining -> credit", ProviderCredit{HasCredit: true, RemainingCents: 2_500_000}, "credit"},
		{"grant exhausted -> paid", ProviderCredit{HasCredit: true, RemainingCents: 0}, "paid"},
		{"no grant -> paid_only", ProviderCredit{HasCredit: false, RemainingCents: 0}, "paid_only"},
		{"no grant, stray remaining -> paid_only", ProviderCredit{HasCredit: false, RemainingCents: 100}, "paid_only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fundingClass(tc.pc); got != tc.want {
				t.Errorf("fundingClass(%+v) = %q, want %q", tc.pc, got, tc.want)
			}
		})
	}
}

// TestAI64 covers the datastore-cell coercion across the driver/JSON transports
// (sum()/count() come back as uint64, float64, or decimal-string depending on path).
func TestAI64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(42), 42},
		{int(42), 42},
		{uint64(42), 42},
		{float64(42), 42},
		{"42", 42},
		{"2600000", 2_600_000},
		{nil, 0},
		{"not-a-number", 0},
	}
	for _, tc := range cases {
		if got := aI64(tc.in); got != tc.want {
			t.Errorf("aI64(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestDOGrantIsDiscoveredNotSeeded pins the inversion: DO's grant must NOT be a
// constant in this map. It is read from DO's own invoices (Client.CreditIssued),
// because the hand-entered value was wrong and unverifiable — this map said
// $26,000 while DO's ledger showed $21,263.65 ever applied and the operator
// believed $50,000. A seeded number here would silently win again.
func TestDOGrantIsDiscoveredNotSeeded(t *testing.T) {
	if v, ok := providerGrantsCents["do-ai"]; ok {
		t.Fatalf("do-ai must not carry a seeded grant (got %d cents); it is discovered from DO invoices", v)
	}
}

// TestExhaustedCreditClassifiesAsPaid is the whole point of the funding class: a
// provider whose promo credit is SPENT is cash from that moment on, even though a
// grant certainly existed. Classifying on "was a grant ever issued" instead of
// "is credit left" is what let $1,824 of real DO spend read as credit-funded.
func TestExhaustedCreditClassifiesAsPaid(t *testing.T) {
	live := ProviderCredit{GrantCents: 2_126_712, RemainingCents: 347, HasCredit: true}
	if got := fundingClass(live); got != "credit" {
		t.Errorf("credit remaining => credit-funded, got %q", got)
	}
	spent := ProviderCredit{GrantCents: 2_126_712, RemainingCents: 0, HasCredit: false, IsPaidOnly: true}
	if got := fundingClass(spent); got == "credit" {
		t.Error("an EXHAUSTED grant must never classify as credit — every later call is cash")
	}
}

// TestWindowReadsTheDatesItWasGiven: the funding board is a money read, and it
// used to answer the last 24 hours whatever dates were asked for — the bounds
// were passed to a parser that only reads them for a custom window, so a
// question about July was answered with yesterday and labelled July.
func TestWindowReadsTheDatesItWasGiven(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	jul1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	jul27 := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

	t.Run("both bounds", func(t *testing.T) {
		start, end := window("2026-07-01T00:00:00Z", "2026-07-27T00:00:00Z", now)
		if !start.Equal(jul1) || !end.Equal(jul27) {
			t.Fatalf("window = [%v, %v), want [%v, %v)", start, end, jul1, jul27)
		}
	})

	t.Run("open end runs to now", func(t *testing.T) {
		start, end := window("2026-07-01T00:00:00Z", "", now)
		if !start.Equal(jul1) || !end.Equal(now) {
			t.Fatalf("window = [%v, %v), want [%v, %v)", start, end, jul1, now)
		}
	})

	// The documented fallback, and the reason this is not a 400: a typo in a date
	// must not blank the board.
	t.Run("nothing readable falls back to thirty days", func(t *testing.T) {
		for _, tc := range [][2]string{{"", ""}, {"nonsense", ""}, {"2026-07-27T00:00:00Z", "2026-07-01T00:00:00Z"}} {
			start, end := window(tc[0], tc[1], now)
			if !end.Equal(now) || !start.Equal(now.AddDate(0, 0, -30)) {
				t.Errorf("window(%q, %q) = [%v, %v), want the last 30 days", tc[0], tc[1], start, end)
			}
		}
	})
}

// doStub serves the two reads the do-ai ledger row makes (balance, then invoices for
// the discovered grant) at a chosen status, so a test can revoke the credential
// without one existing.
func doStub(t *testing.T, status int, balance string) *digitalocean.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"id":"unauthorized","message":"Unable to authenticate you"}`)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/customers/my/balance"):
			_, _ = io.WriteString(w, balance)
		default:
			_, _ = io.WriteString(w, `{"invoices":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return digitalocean.NewWithBase(srv.URL, "dop_v1_a_revoked_token_is_still_a_string")
}

// TestARevokedTokenDoesNotClassifyAProviderAsPaid is the mechanism finding on this
// board. Ready() is a PRESENCE check and a revoked token is still a non-empty
// string, so the do-ai row is built, the live read 401s, and the row used to fall
// through to the no-grant branch — publishing IsPaidOnly on an account whose promo
// credit had simply not been read. Every usage line then inherited "paid".
func TestARevokedTokenDoesNotClassifyAProviderAsPaid(t *testing.T) {
	s := &cloud.Service[core.State]{State: core.State{DO: doStub(t, http.StatusUnauthorized, "")}}

	var row ProviderCredit
	for _, pc := range computeProviderCredits(context.Background(), s) {
		if pc.Provider == "do-ai" {
			row = pc
		}
	}
	if row.Provider == "" {
		t.Fatal("a configured provider must stay on the board when its read fails — vanishing is its own lie")
	}
	if row.Error == "" {
		t.Fatal("the row carries no error, so a refused read is indistinguishable from a read that found no grant")
	}
	if row.IsPaidOnly {
		t.Error("a provider whose credit could not be READ was reported as established paid-only — " +
			"that is promo spend labelled as cash")
	}
	if row.HasCredit {
		t.Error("nothing was read, so credit must not be asserted either")
	}
	if got := fundingClass(row); got != "unknown" {
		t.Errorf("fundingClass on an unread row = %q, want %q — the usage board tags every line with this", got, "unknown")
	}
}

// TestAReadThatAnsweredStillClassifies keeps the fix from becoming a blanket
// abstention: when DO answers, the verdict is real. A balance of -$500 is $500 of
// credit we hold.
func TestAReadThatAnsweredStillClassifies(t *testing.T) {
	bal := `{"account_balance":"-500.00","month_to_date_balance":"0.00","month_to_date_usage":"10.00","generated_at":"2026-08-19T00:00:00Z"}`
	s := &cloud.Service[core.State]{State: core.State{DO: doStub(t, http.StatusOK, bal)}}

	var row ProviderCredit
	for _, pc := range computeProviderCredits(context.Background(), s) {
		if pc.Provider == "do-ai" {
			row = pc
		}
	}
	if row.Error != "" {
		t.Fatalf("DO answered, so no error belongs on the row: %q", row.Error)
	}
	if row.RemainingCents != 50_000 {
		t.Errorf("remaining = %d cents, want 50000", row.RemainingCents)
	}
	if !row.HasCredit || row.IsPaidOnly {
		t.Errorf("credit remaining must classify as credit-funded; got hasCredit=%v isPaidOnly=%v", row.HasCredit, row.IsPaidOnly)
	}
	if got := fundingClass(row); got != "credit" {
		t.Errorf("fundingClass = %q, want credit", got)
	}
}

// TestUnknownIsNotOneOfTheThreeVerdicts pins the wire invariant the row relies on:
// on the healthy path exactly one of HasCredit/IsPaidOnly is true, so both-false is
// unambiguously "not classified" and needs no sentinel amount to say so.
func TestUnknownIsNotOneOfTheThreeVerdicts(t *testing.T) {
	for _, pc := range []ProviderCredit{
		{HasCredit: true, RemainingCents: 1},
		{HasCredit: true, RemainingCents: 0},
		{IsPaidOnly: true},
	} {
		if got := fundingClass(pc); got == "unknown" {
			t.Errorf("a row that WAS read must reach a real verdict; %+v classified unknown", pc)
		}
	}
	if got := fundingClass(ProviderCredit{Error: "401 unauthorized"}); got != "unknown" {
		t.Errorf("an unread row = %q, want unknown", got)
	}
}

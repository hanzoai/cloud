package metering_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// TestRecord_DebitsFinanceInProcess pins the money-critical seam: when a finance ledger is
// co-resident (finance.Current() != nil), Record posts the usage debit DIRECTLY to the
// native ledger and NEVER touches HTTP. It also locks the anti-leak invariant — a micros-only
// debit (the AI meter prices sub-cent) lands EXACTLY in the 18-decimal ledger (not dropped to
// zero); the RecordResult reports the ceiled whole cents while the ledger stays exact — and
// RequestID idempotency.
func TestRecord_DebitsFinanceInProcess(t *testing.T) {
	ctx := context.Background()
	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	defer finance.Publish(nil)

	// Seed acme's pooled wallet with $1.00 (100¢).
	if _, err := fin.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100)}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}

	// A commerce fake that MUST NOT be hit: any HTTP call records fc.method, which we assert
	// stays empty. The client is Enabled (BaseURL set) so the finance seam — not "not
	// configured" — is what intercepts.
	fc := &fakeCommerce{status: 500, reply: `boom`}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()
	c := newClient(t, srv, metering.Config{})

	// Micros-only debit: 15_000 micro-USD = 1.5¢. The RESULT reports the ceiled 2¢
	// (Cents() rounds sub-cent up for whole-cent contexts); the LEDGER debits the
	// exact 1.5¢ (NOT dropped to 0, NOT ceiled).
	res, err := c.Record(ctx, metering.Usage{User: "acme", Org: "acme", AmountMicros: 15000, RequestID: "m1"})
	if err != nil {
		t.Fatalf("record micros: %v", err)
	}
	if res == nil || res.Amount != 2 {
		t.Fatalf("record micros result = %+v; want reported Amount 2 (Cents() ceils 1.5¢)", res)
	}
	// Exact 18-decimal ledger: 100¢ − 1.5¢ = 98.5¢ ($0.985), not a ceiled 98¢.
	wantBal, _ := money.ParseUSD("0.985")
	if bal, _ := fin.Balance(ctx, "acme", "acme", "usd", false); bal.Cmp(wantBal) != 0 {
		t.Fatalf("balance after micros debit = %s; want 0.985 (exact 1.5¢ debit, not ceiled)", bal)
	}

	// Whole-cent debit: 50¢ → 98.5¢ − 50¢ = 48.5¢.
	if _, err := c.Record(ctx, metering.Usage{User: "acme", Org: "acme", AmountCents: 50, RequestID: "m2"}); err != nil {
		t.Fatalf("record cents: %v", err)
	}
	wantBal, _ = money.ParseUSD("0.485")
	if bal, _ := fin.Balance(ctx, "acme", "acme", "usd", false); bal.Cmp(wantBal) != 0 {
		t.Fatalf("balance after cents debit = %s; want 0.485 (98.5-50)", bal)
	}

	// Idempotent replay on RequestID m2 → no second debit.
	if _, err := c.Record(ctx, metering.Usage{User: "acme", Org: "acme", AmountCents: 50, RequestID: "m2"}); err != nil {
		t.Fatalf("record replay: %v", err)
	}
	if bal, _ := fin.Balance(ctx, "acme", "acme", "usd", false); bal.Cmp(wantBal) != 0 {
		t.Fatalf("balance after replay = %s; want 0.485 (idempotent)", bal)
	}

	// Not one byte of HTTP: the finance seam intercepted every debit.
	if fc.method != "" {
		t.Fatalf("commerce HTTP was called (%s %s); finance co-resident must intercept", fc.method, fc.path)
	}
}

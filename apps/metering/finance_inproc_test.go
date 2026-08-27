package metering_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// TestRecord_DebitsFinanceInProcess pins the money-critical client: when a finance ledger is
// co-resident (finance.Current() != nil), Record posts the usage debit DIRECTLY to the
// native ledger and NEVER touches HTTP. It also locks the anti-leak invariant — a micros-only
// debit (the AI meter prices sub-cent) lands EXACTLY in the 18-decimal ledger (not dropped to
// zero); the RecordResult reports the ceiled whole cents while the ledger stays exact — and
// that a repeated RequestID is NOT a replay.
func TestRecord_DebitsFinanceInProcess(t *testing.T) {
	ctx := context.Background()
	fin := finance.New(finance.Local(t.TempDir()))
	finance.Publish(fin)
	defer finance.Publish(nil)

	// Seed acme's pooled wallet with $1.00 (100¢).
	if _, err := fin.Deposit(ctx, types.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100)}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}

	// A commerce fake that MUST NOT be hit: any HTTP call records fc.method, which we assert
	// stays empty. The client is Enabled (BaseURL set) so the finance client — not "not
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

	// A REPEATED RequestID IS NOT A REPLAY. RequestID is the call's correlation header —
	// the caller's to choose — so two calls carrying one are two acts and both bill:
	// 48.5¢ − 20¢ = 28.5¢. This line used to assert the opposite, and that assertion WAS
	// the leak: pin the header once and every call after the first was free.
	if _, err := c.Record(ctx, metering.Usage{User: "acme", Org: "acme", AmountCents: 20, RequestID: "m2"}); err != nil {
		t.Fatalf("record with a repeated correlation id: %v", err)
	}
	wantBal, _ = money.ParseUSD("0.285")
	if bal, _ := fin.Balance(ctx, "acme", "acme", "usd", false); bal.Cmp(wantBal) != 0 {
		t.Fatalf("balance after a repeated correlation id = %s; want 0.285 (it billed)", bal)
	}

	// Not one byte of HTTP: the finance client intercepted every debit.
	if fc.method != "" {
		t.Fatalf("commerce HTTP was called (%s %s); finance co-resident must intercept", fc.method, fc.path)
	}
}

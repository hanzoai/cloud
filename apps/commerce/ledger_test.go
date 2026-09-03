// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// ledger_test.go pins what the creditledger ADAPTER translates: commerce names an
// address, and this is the one place that address becomes a finance call.
//
// The address has three parts — (Org, Subject, Test) — and the adapter used to drop
// the third on the floor. `Test` was never passed to Deposit and both balance reads
// hardcoded `false`, so every credit commerce minted through this client landed in the
// LIVE books whatever the caller meant, and a sandbox tenant's grant became money the
// AI spend gate buys real inference with. finance keeps sandbox money in a separate
// file per org, so this is not a flag being ignored — it is a different ledger.
//
// It asserts against a RECORDING finance client rather than a real one on purpose.
// What changed here is the translation, and a recording client states the arguments
// exactly; that finance then routes (org, test) to the right file is finance's own
// property and is covered where it lives (apps/finance).

import (
	"context"
	"testing"

	"github.com/hanzoai/commerce/billing/creditledger"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// recorder is a finance client that answers plausibly and remembers exactly what it
// was asked. Balances are held per WHOLE address, so a deposit into one book is not
// visible from a read of the other — the property the adapter has to get right.
type recorder struct {
	deposits []types.DepositInput
	reads    []readArgs
	bal      map[readArgs]int64
}

// readArgs is one balance read's whole address. It is also the deposits' key, so the
// two can only meet when every component agrees.
type readArgs struct {
	org      string
	subject  string
	currency string
	test     bool
}

var _ types.FinanceClient = (*recorder)(nil)

func newRecorder() *recorder { return &recorder{bal: map[readArgs]int64{}} }

func (r *recorder) Deposit(_ context.Context, in types.DepositInput) (string, error) {
	r.deposits = append(r.deposits, in)
	k := readArgs{in.Org, in.Subject, in.Currency, in.Test}
	r.bal[k] += in.Amount.Cents()
	return "dep_1", nil
}

func (r *recorder) Balance(_ context.Context, org, subject, currency string, test bool) (money.Amount, error) {
	k := readArgs{org, subject, currency, test}
	r.reads = append(r.reads, k)
	return money.FromCents(r.bal[k]), nil
}

func (r *recorder) RecordUsage(context.Context, types.UsageInput) error { return nil }

func (r *recorder) SumUsageSince(context.Context, string, bool, int64) (int64, error) {
	return 0, nil
}

// client is the adapter under test, bound once. A composite literal cannot start an
// if-statement's expression, and naming it also says what it is: the ONE value
// commerce is handed as its credit ledger.
var client = ledger{}

// recording publishes the recorder as the process finance client for one test.
func recording(t *testing.T) *recorder {
	t.Helper()
	r := newRecorder()
	prev := finance.Current()
	finance.Publish(r)
	t.Cleanup(func() { finance.Publish(prev) })
	return r
}

// A sandbox credit writes the SANDBOX books, and a live credit the live ones — the
// deposit AND the balance read the caller is answered with.
//
// The read-back matters as much as the write: it is the number the credit endpoint hands
// back to its caller, and reading the live books after crediting the sandbox ones
// reports a balance that has nothing to do with the credit just made. That was the
// state of both call sites.
func TestLedgerAdapter_TestRoutesToTheSandboxBooks(t *testing.T) {
	for _, tc := range []struct {
		name string
		test bool
	}{
		{"a sandbox credit", true},
		{"a live credit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := recording(t)

			_, cents, err := client.Credit(context.Background(), creditledger.CreditInput{
				Org:            "acme",
				Subject:        "acme/alice",
				Currency:       "usd",
				AmountCents:    2500,
				IdempotencyKey: "pay_1",
				Test:           tc.test,
			})
			if err != nil {
				t.Fatalf("credit: %v", err)
			}

			if len(rec.deposits) != 1 {
				t.Fatalf("deposits=%d, want 1", len(rec.deposits))
			}
			if got := rec.deposits[0].Test; got != tc.test {
				t.Errorf("DepositInput.Test=%v, want %v — the credit went into the wrong books", got, tc.test)
			}
			if got := rec.deposits[0].Org; got != "acme" {
				t.Errorf("DepositInput.Org=%q, want acme", got)
			}
			if got := rec.deposits[0].Subject; got != "acme/alice" {
				t.Errorf("DepositInput.Subject=%q, want acme/alice", got)
			}

			// The read-back repeats the deposit's WHOLE address.
			if len(rec.reads) != 1 {
				t.Fatalf("balance reads=%d, want 1", len(rec.reads))
			}
			want := readArgs{"acme", "acme/alice", "usd", tc.test}
			if rec.reads[0] != want {
				t.Errorf("balance read %+v, want %+v — the credit was read back from a different account", rec.reads[0], want)
			}
			if cents != 2500 {
				t.Errorf("returned balance=%d, want 2500 — the caller was answered from books it did not credit", cents)
			}
		})
	}
}

// The two books do not see each other. A sandbox credit leaves the live balance at
// zero, which is the whole reason `test` is part of the address and not a flag.
func TestLedgerAdapter_SandboxMoneyIsNotSpendable(t *testing.T) {
	recording(t)

	if _, _, err := client.Credit(context.Background(), creditledger.CreditInput{
		Org: "acme", Subject: "acme/alice", Currency: "usd",
		AmountCents: 9900, IdempotencyKey: "pay_sandbox", Test: true,
	}); err != nil {
		t.Fatalf("credit: %v", err)
	}

	live, err := client.Balance(context.Background(), "acme", "acme/alice", "usd", false)
	if err != nil {
		t.Fatalf("live balance: %v", err)
	}
	if live != 0 {
		t.Fatalf("LIVE balance=%d after a SANDBOX credit, want 0 — sandbox money became spendable", live)
	}

	sandbox, err := client.Balance(context.Background(), "acme", "acme/alice", "usd", true)
	if err != nil {
		t.Fatalf("sandbox balance: %v", err)
	}
	if sandbox != 9900 {
		t.Fatalf("sandbox balance=%d, want 9900", sandbox)
	}
}

// Balance reads the account it is ASKED for, and an empty subject is the org's pool —
// the same default Credit applies, so the two halves address one wallet.
//
// Its signature used to be (org, currency) with the org passed for the subject too,
// which meant a member-scoped read opened a LEDGER named after the member: a
// different file, holding nothing, answering a funded customer with a confident zero.
func TestLedgerAdapter_BalanceReadsTheAccountItIsAsked(t *testing.T) {
	rec := recording(t)

	// The pool holds one amount and a member another, in the same org and books.
	for _, seed := range []struct {
		subject string
		cents   int64
	}{{"acme", 100}, {"acme/alice", 700}} {
		if _, _, err := client.Credit(context.Background(), creditledger.CreditInput{
			Org: "acme", Subject: seed.subject, Currency: "usd",
			AmountCents: seed.cents, IdempotencyKey: "seed:" + seed.subject,
		}); err != nil {
			t.Fatalf("seed %s: %v", seed.subject, err)
		}
	}
	rec.reads = nil

	pool, err := client.Balance(context.Background(), "acme", "", "usd", false)
	if err != nil {
		t.Fatalf("pool balance: %v", err)
	}
	if pool != 100 {
		t.Errorf("pool balance=%d, want 100 — an empty subject must be the org's own account", pool)
	}
	if got := rec.reads[0]; got.subject != "acme" {
		t.Errorf("pool read addressed subject %q, want acme", got.subject)
	}

	member, err := client.Balance(context.Background(), "acme", "acme/alice", "usd", false)
	if err != nil {
		t.Fatalf("member balance: %v", err)
	}
	if member != 700 {
		t.Errorf("member balance=%d, want 700 — the read answered a different account than the credit funded", member)
	}
	if got := rec.reads[1]; got.org != "acme" || got.subject != "acme/alice" {
		t.Errorf("member read addressed (%q,%q), want (acme, acme/alice) — the subject was used as the ledger", got.org, got.subject)
	}
}

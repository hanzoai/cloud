package cloud

// The prepaid loop, end to end, with real numbers.
//
// Every leg of prepaid billing already has a test, and every one of them fakes the
// leg next to it: the gate is proven against a fake ledger, the ledger against a
// fake gate, the debit against a fake commerce. A loop assembled entirely from
// tested parts can still fail to close — the classic failure is that the gate reads
// one address and the debit writes another, which this codebase has shipped three
// times. So this test fakes NOTHING on the money path. One real edge, one real
// metering client, one real double-entry ledger file, and arithmetic:
//
//	top up 100¢ -> four 25¢ calls -> balance 75, 50, 25, 0 -> the fifth is refused
//
// It also pins the two structural properties the loop is worthless without:
//
//   - THE MONEY NEVER LEAVES THE PROCESS. The commerce base URL points at a server
//     that FAILS the test if the balance read or the usage debit ever reaches it.
//     That HTTP hop is what deleted the gate once already (COMMERCE_URL defaulted to
//     the public edge — this same binary — so the forwarder re-entered itself, and
//     deleting the loop deleted the gate with it). It must stay deleted.
//   - ONE BALANCED ENTRY PER MOVEMENT. Five movements, five journal entries, each
//     one two postings summing to zero. Not four (a debit that never landed), not
//     six (a debit posted twice), and never an entry that moves money out of nowhere.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
)

// The one surface under test and what it costs. 25¢ is declared ONCE, here, and
// every expectation below is arithmetic on it — so a test that agrees with the
// gate because both were edited to the same wrong number cannot happen.
const (
	probeSurface = "probe"
	probePath    = "/v1/probe/run"
	probeCents   = 25
	topUpCents   = 100
)

// ledgerOrg / ledgerAccount are the address `member` resolves to: the org whose
// books hold the wallet, and the account within them. They are written out rather
// than derived so the assertion is on the ADDRESS, not on a helper that could drift
// with the gate it is checking.
const (
	ledgerOrg     = "hanzo"
	ledgerAccount = "hanzo/stranger"
)

// realLedger publishes a real finance ledger (its own file, in a temp dir) on the
// process-wide money client and returns it, restoring the previous client after.
func realLedger(t *testing.T) *ledgerReader {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	prev := finance.Current()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(prev) })
	return &ledgerReader{fin}
}

// ledgerReader is the narrow view this test needs of the real ledger.
type ledgerReader struct {
	fin interface {
		Balance(ctx context.Context, org, subject, currency string, test bool) (money.Amount, error)
		Deposit(ctx context.Context, in money.DepositInput) (string, error)
		ListEntries(ctx context.Context, org, subject string, test bool, limit int) ([]finance.TxnRow, error)
	}
}

func (l *ledgerReader) cents(t *testing.T) int64 {
	t.Helper()
	bal, err := l.fin.Balance(context.Background(), ledgerOrg, ledgerAccount, "usd", false)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return bal.Cents()
}

// settled waits for the asynchronous debit to reach the ledger. BillingGate records
// on a detached goroutine on purpose — a client disconnect must not cancel a debit —
// so the only honest way to observe the new balance is to wait for it.
func (l *ledgerReader) settled(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := l.cents(t); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("balance settled at %d¢, want %d¢", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// forbiddenCommerce is a commerce that must never be spoken to about money. The
// spend-cap overlay is a POLICY read and is allowed (it fails open by design and in
// production rides the in-process transport); the balance and the usage debit are
// the money itself, and either one arriving here means the ledger the gate charges
// is a network hop away from the ledger this process holds.
func forbiddenCommerce(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the gate read a balance over HTTP — the co-resident ledger client is broken")
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the debit went out over HTTP — the co-resident ledger client is broken")
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/billing/limits/authorize", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"allow":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

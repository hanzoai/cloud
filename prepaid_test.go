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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
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
// process-wide money seam and returns it, restoring the previous seam after.
func realLedger(t *testing.T) *ledgerReader {
	t.Helper()
	fin := finance.New(t.TempDir())
	prev := finance.Current()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(prev) })
	return &ledgerReader{fin}
}

// ledgerReader is the narrow view this test needs of the real ledger.
type ledgerReader struct {
	fin interface {
		Balance(ctx context.Context, org, subject, currency string, test bool) (money.Amount, error)
		Deposit(ctx context.Context, in types.DepositInput) (string, error)
		ListEntries(ctx context.Context, org string, limit int) ([]finance.TxnRow, error)
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
		t.Error("the gate read a balance over HTTP — the co-resident ledger seam is broken")
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the debit went out over HTTP — the co-resident ledger seam is broken")
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/billing/limits/authorize", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"allow":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// probeApp is the edge: the real BillingGate, the real DefaultPrice (which reads the
// declared Price and holds no table), and one handler that does nothing but succeed.
func probeApp(t *testing.T, m *metering.Client, served *atomic.Int64) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(BillingGate(m, DefaultPrice))
	app.Post(probePath, func(c *zip.Ctx) error {
		served.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"ok": "served"})
	})
	return app
}

// TestPrepaidLoop_TopUpSpendExhaustRefuse is the whole loop, closed.
func TestPrepaidLoop_TopUpSpendExhaustRefuse(t *testing.T) {
	led := realLedger(t)
	index(t, &Config{}, Plugin{Name: probeSurface, Price: probeCents})

	// The gate charges what the surface DECLARED — DefaultPrice reads PriceOf and
	// holds no table of its own. Pin the resolution before spending a cent against it.
	if got := PriceOf(probePath).Cents(); got != probeCents {
		t.Fatalf("declared price resolves to %d¢, want %d¢ — the rest of this test is arithmetic on it", got, probeCents)
	}

	m := mustClient(t, forbiddenCommerce(t), false)
	var served atomic.Int64
	app := probeApp(t, m, &served)

	// ── top up ──────────────────────────────────────────────────────────────────
	if _, err := led.fin.Deposit(context.Background(), types.DepositInput{
		Org: ledgerOrg, Subject: ledgerAccount, Amount: money.FromCents(topUpCents), Ref: "topup-1",
	}); err != nil {
		t.Fatalf("top up: %v", err)
	}
	if got := led.cents(t); got != topUpCents {
		t.Fatalf("balance after top up = %d¢, want %d¢", got, topUpCents)
	}

	// ── spend it down, one declared charge at a time ────────────────────────────
	calls := topUpCents / probeCents
	for i := 1; i <= calls; i++ {
		code, body := call(t, app, http.MethodPost, probePath, member)
		if code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want 200 (body=%s)", i, code, body)
		}
		want := int64(topUpCents - i*probeCents)
		led.settled(t, want)
	}
	if got := served.Load(); got != int64(calls) {
		t.Fatalf("handler ran %d times, want %d", got, calls)
	}
	if got := led.cents(t); got != 0 {
		t.Fatalf("balance after spending the lot = %d¢, want 0¢", got)
	}

	// ── at zero, REFUSE ─────────────────────────────────────────────────────────
	code, body := call(t, app, http.MethodPost, probePath, member)
	if code != http.StatusPaymentRequired {
		t.Fatalf("exhausted call: status = %d, want 402 (body=%s)", code, body)
	}
	if got := served.Load(); got != int64(calls) {
		t.Fatalf("the handler ran on an empty wallet — a prepaid system that serves on "+
			"empty is a free tier by accident (ran %d, want %d)", got, calls)
	}
	var refusal struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("refusal body is not the money wire's shape: %v (body=%s)", err, body)
	}
	if refusal.Error.Code != "insufficient_balance" {
		t.Fatalf("refusal code = %q, want %q — a typed refusal or nothing", refusal.Error.Code, "insufficient_balance")
	}
	if refusal.Error.Message == "" {
		t.Fatal("the refusal names no way to cure it; a 402 a caller cannot act on is a 500 with better manners")
	}

	// ── the books ───────────────────────────────────────────────────────────────
	// One movement, one balanced entry. Five movements: the top up and four debits.
	entries, err := led.fin.ListEntries(context.Background(), ledgerOrg, 0)
	if err != nil {
		t.Fatalf("read entries: %v", err)
	}
	if len(entries) != calls+1 {
		t.Fatalf("ledger holds %d entries, want %d (1 deposit + %d debits) — a movement is "+
			"missing or doubled", len(entries), calls+1, calls)
	}
	var deposits, debits int
	for _, e := range entries {
		switch e.Kind {
		case finance.KindDeposit:
			deposits++
			if e.Amount.Cents() != topUpCents {
				t.Errorf("deposit entry %s = %d¢, want %d¢", e.ID, e.Amount.Cents(), topUpCents)
			}
		case finance.KindUsage:
			debits++
			if e.Amount.Cents() != probeCents {
				t.Errorf("usage entry %s = %d¢, want the DECLARED %d¢", e.ID, e.Amount.Cents(), probeCents)
			}
		default:
			t.Errorf("entry %s has kind %q — an entry nobody can classify must not be in the books", e.ID, e.Kind)
		}
	}
	if deposits != 1 || debits != calls {
		t.Fatalf("books hold %d deposits and %d debits, want 1 and %d", deposits, debits, calls)
	}
}

// TestPrepaidLoop_RefusalIsTheOnlyOtherOutcome pins the boundary itself: a wallet
// holding LESS than the declared charge is refused, not served at a discount and not
// taken negative. The balance must cover the whole charge, or the work does not run.
func TestPrepaidLoop_RefusalIsTheOnlyOtherOutcome(t *testing.T) {
	led := realLedger(t)
	index(t, &Config{}, Plugin{Name: probeSurface, Price: probeCents})

	m := mustClient(t, forbiddenCommerce(t), false)
	var served atomic.Int64
	app := probeApp(t, m, &served)

	// One cent short of the declared price.
	if _, err := led.fin.Deposit(context.Background(), types.DepositInput{
		Org: ledgerOrg, Subject: ledgerAccount, Amount: money.FromCents(probeCents - 1), Ref: "short",
	}); err != nil {
		t.Fatalf("top up: %v", err)
	}

	code, body := call(t, app, http.MethodPost, probePath, member)
	if code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 — a balance that cannot cover the charge is not "+
			"a partial sale (body=%s)", code, body)
	}
	if served.Load() != 0 {
		t.Fatal("the handler ran on an underfunded wallet")
	}
	if got := led.cents(t); got != probeCents-1 {
		t.Fatalf("balance = %d¢, want %d¢ untouched — a refused request debits nothing", got, probeCents-1)
	}
}

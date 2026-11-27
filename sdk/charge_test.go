package cloud

// charge_test.go — the commitment's two failure modes, which pull in opposite
// directions and therefore have to be tested together.
//
// Hold it too briefly and the bound is fiction: the deferred Release fires when
// the handler returns while the debit is still crossing to the ledger, so a caller
// pipelining the instant the response lands gets an extra act per window.
//
// Hold it forever and an unreachable ledger locks a customer out of their own
// balance — worse than not billing them, because it takes away money they have.
//
// One fix cannot be shipped without the other, so one file tests both.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/driver/kv/kvtest"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
)

// ledger is a commerce double with a balance and a debit endpoint whose latency
// the test chooses. The debit crosses the internal plane in production; here the
// meter is co-resident (Enabled), so Record posts over HTTP and this stands in for
// the whole ledger.
type moneyLedger struct {
	available int64
	block     chan struct{} // debits wait on this; nil means answer at once
	url       string
}

func newLedger(t *testing.T, available int64, block chan struct{}) *moneyLedger {
	t.Helper()
	l := &moneyLedger{available: available, block: block}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": l.available})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if l.block != nil {
			select {
			case <-l.block:
			case <-r.Context().Done(): // the client gave up; so do we
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	l.url = srv.URL
	return l
}

func meterAt(t *testing.T, l *moneyLedger) *Meter {
	t.Helper()
	m, err := meter.New(meter.Config{BaseURL: l.url})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return NewMeter(Deps{Metering: m}, "test")
}

func spender(w string) Payer { return Payer{Wallet: account.PayerOf("", w)} }

func settles(cond func() bool) bool {
	for range 400 {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// THE OWNERSHIP FIX. Debit takes the hold, so the caller's own deferred Release —
// which every metered surface writes as its safety net — cannot give the
// commitment back while the debit is still in flight.
func TestDebitOwnsTheHoldUntilTheLedgerHasIt(t *testing.T) {
	block := make(chan struct{})
	l := newLedger(t, 100000, block)
	rm := meterAt(t, l)

	ch, err := rm.Reserve(context.Background(), spender("acme"), "kind", 100)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	ch.Debit(meter.Usage{Model: "kind", AmountCents: 100})
	ch.Release() // the deferred safety net every call site writes

	if got := rm.inflight.Pending("acme"); got != 100 {
		t.Fatalf("pending = %d while the debit is still crossing, want 100 — "+
			"Release gave the commitment back before the money was visible, which is "+
			"exactly the window the commitment exists to cover", got)
	}

	close(block) // let the ledger answer
	if !settles(func() bool { return rm.inflight.Pending("acme") == 0 }) {
		t.Fatalf("pending = %d after the debit landed, want 0", rm.inflight.Pending("acme"))
	}
}

// THE LOCKOUT BOUND, which is why the fix above could not ship alone. A ledger
// that ACCEPTS AND NEVER ANSWERS must not hold a customer's commitment forever.
//
// IT BINDS A REAL PEER, and that is the only way this test means anything. The
// debit crosses the internal plane, not HTTP — metering.Client.Record calls
// commerce.FinanceRecord — so a fixture with no peer bound fails fast on
// ErrNoPeer, the bound never runs, and the test passes with the bound DELETED. The
// blocking observer below runs inside the peer's own handler, which is exactly the
// failure being modelled: apps/finance is per-org SQLite with one writer, so lock
// contention presents as a peer that took the call and is taking its time.
//
// The bound cannot be a context either. zip.Call checks ctx.Err once and then
// issues a client Do with no deadline, so the call is uninterruptible from here;
// what returns the commitment is the timer in settle.
func TestAHungLedgerDoesNotLockOutTheWallet(t *testing.T) {
	prev := ledgerCallTimeout
	ledgerCallTimeout = 80 * time.Millisecond
	t.Cleanup(func() { ledgerCallTimeout = prev })

	block := make(chan struct{}) // never closed: the ledger has the call and keeps it
	t.Cleanup(func() { close(block) })
	planetest.ServeWith(t, func(string, client.RecordIn) { <-block })

	l := newLedger(t, 100000, nil) // the BALANCE still answers; only the debit hangs
	rm := meterAt(t, l)

	ch, err := rm.Reserve(context.Background(), spender("acme"), "kind", 100)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	ch.Debit(meter.Usage{Model: "kind", AmountCents: 100})

	if !settles(func() bool { return rm.inflight.Pending("acme") == 0 }) {
		t.Fatalf("pending = %d with an unreachable ledger — the commitment never came "+
			"back, so this wallet can no longer clear its own gate. Losing a charge is "+
			"recoverable; taking a customer's balance away is not.",
			rm.inflight.Pending("acme"))
	}

	// And the wallet still works: the gate weighs a clean slate.
	if _, err := rm.Reserve(context.Background(), spender("acme"), "kind", 100); err != nil {
		t.Fatalf("the wallet is locked out after a hung debit: %v", err)
	}
}

// A REFUSED gate returns the commitment, and it is the deferred release that does
// it — the explicit one on the error path is gone, precisely so that every way out
// of Allow, including an unwind nobody wrote down, gives the money back.
//
// A stranded commitment is not a lost cent. It is added to every later weigh-in
// for that wallet and never expires, so the customer is refused forever with a
// balance they can see and cannot spend.
func TestARefusedGateGivesTheCommitmentBack(t *testing.T) {
	l := newLedger(t, 0, nil) // no money: the gate refuses
	rm := meterAt(t, l)

	if _, err := rm.Reserve(context.Background(), spender("acme"), "kind", 100); err == nil {
		t.Fatal("an empty balance was allowed; this test proves nothing without the refusal")
	}
	if got := rm.inflight.Pending("acme"); got != 0 {
		t.Fatalf("pending = %d after a refused gate, want 0", got)
	}
	// And the wallet is not poisoned: funded, the very next call clears.
	l.available = 100000
	if _, err := rm.Reserve(context.Background(), spender("acme"), "kind", 100); err != nil {
		t.Fatalf("a wallet refused once is still refused when funded: %v", err)
	}
}

// THE BALANCE IS THE DEPLOYMENT'S, so what is held against it is too. A wallet that can
// pay for one call is not authorized for one call on each pod: three pods handed three
// calls at once each weighed the balance against their own commitment alone and each
// cleared it. Held in the KV the pods share, the second pod's call is weighed against
// the first's and refused, and a hold given back lets the next through.
//
// It runs on the in-process KV everywhere, and again on a real server where one is
// installed.
//
// MUTATION: count the holds in the pod again (a map under a mutex) and every pod
// authorizes the call.
func TestTwoPodsCannotBothSpendOneBalance(t *testing.T) {
	t.Run("in-process KV", func(t *testing.T) { shareOneBalance(t, coordtest.Memory(t)) })
	t.Run("valkey", func(t *testing.T) {
		url := kvtest.KV(t)
		shareOneBalance(t, func(id string) *coord.Coord { return kvtest.Pod(t, url, id) })
	})
}

func shareOneBalance(t *testing.T, pod func(id string) *coord.Coord) {
	l := newLedger(t, 150, nil) // one call's worth, and half of another
	pods := make([]*Meter, 3)
	for i := range pods {
		pods[i] = meterAt(t, l)
		pods[i].inflight.Pods = pod(fmt.Sprintf("cloud-%d", i))
	}

	var wg sync.WaitGroup
	charges := make([]*Charge, len(pods))
	errs := make([]error, len(pods))
	for i, m := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			charges[i], errs[i] = m.Reserve(context.Background(), spender("acme"), "kind", 100)
		}()
	}
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won == -1:
			won = i
		case err == nil:
			t.Fatalf("pods %d and %d both authorized 100 against a balance of 150", won, i)
		case !errors.Is(err, meter.ErrInsufficientBalance):
			t.Fatalf("pod %d: %v, want an insufficient balance", i, err)
		}
	}
	if won == -1 {
		t.Fatal("no pod was authorized for a call the balance covers")
	}
	for i, m := range pods {
		if got := m.inflight.Pending("acme"); got != 100 {
			t.Fatalf("pod %d reads %d held, want the winner's 100: the holds are the deployment's", i, got)
		}
	}

	// The winner's call ends unspent, and the next pod is let through.
	charges[won].Release()
	other := pods[(won+1)%len(pods)]
	ch, err := other.Reserve(context.Background(), spender("acme"), "kind", 100)
	if err != nil {
		t.Fatalf("after the hold was given back: %v", err)
	}
	ch.Release()
	if got := pods[won].inflight.Pending("acme"); got != 0 {
		t.Fatalf("holds left standing: %d", got)
	}
}

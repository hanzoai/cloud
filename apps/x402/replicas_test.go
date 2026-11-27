package x402

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/hanzoai/namespace"
)

// Settlement is shared by every replica of a deployment, and so are its claims: a replica
// that withdraws one, or marks one, does so on a row another replica may have changed
// since it read it. These tests are two pods of one deployment: two pools on one store,
// each pod with its own process-local state, as two processes have. A file belongs to one
// process, where the second pod is the first.

// replicas opens two handles on the rail's store: on a server the claims one pod writes
// are the claims the other reads, and neither shares a connection with the other. A file
// has one handle, which both pods are.
func replicas(t *testing.T, open sqltest.Open) (*store, *store) {
	t.Helper()
	mk := func() *store {
		db := open(namespace.System(), "x402")
		s := &store{db: db, d: dialect.Of(db)}
		if err := s.remigrate(); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := mk()
	if !a.d.Postgres() {
		return a, a
	}
	return a, mk()
}

// An authorization is claimed for one resource and paid for once. Replica A's draw was
// refused and it withdraws the claim it took; replica B, which read the same claim,
// drew and credited once the wallet was funded and marks it settled. Whichever of the
// two writes lands second finds the claim not as it took it, and is refused: B is not
// told it settled a claim A withdrew, and A is not allowed to withdraw a claim B settled
// and tell the payer its wallet was short of what B drew.
//
// This is the interleaving a red review reproduced: markSettled reported success on a row
// that was gone, B served the resource, and the same authorization was claimed afresh for
// another.
func TestAClaimAnotherReplicaMovedIsRefusedNotAssumed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		a, b := replicas(t, open)
		ctx := context.Background()
		mk := func(id, resource string) *Settlement {
			return &Settlement{ID: id, PayerOrg: "acme", From: "0xabc", Nonce: "0x01", Resource: resource, Payee: "w1",
				PayeeOrg: "globex", Amount: "1000000000000000000", SettledVia: "ledger", CreatedAt: 100}
		}

		// B settles the claim A took; A's late withdrawal is refused and the claim stays settled.
		if won, _, err := a.claim(ctx, mk("x402_one", "/paid/a")); err != nil || !won {
			t.Fatalf("claim: %v %v", won, err)
		}
		if err := b.markSettled(ctx, "x402_one", "0xtx"); err != nil {
			t.Fatalf("B marks the claim it took: %v", err)
		}
		if err := a.unclaim(ctx, "x402_one"); !errors.Is(err, errClaimMoved) {
			t.Fatalf("A withdrew a claim B settled: %v, want errClaimMoved", err)
		}
		if st, found, err := a.get(ctx, "x402_one"); err != nil || !found || !st.Settled {
			t.Fatalf("the settled claim after A's withdrawal: %+v %v %v", st, found, err)
		}
		if won, existing, err := b.claim(ctx, mk("x402_one", "/paid/b")); err != nil || won || existing == nil || existing.Resource != "/paid/a" {
			t.Fatalf("the settled authorization was claimed again for /paid/b: won=%v existing=%+v err=%v", won, existing, err)
		}

		// A withdraws the claim it took; B's mark finds nothing to mark and is refused.
		if won, _, err := a.claim(ctx, mk("x402_two", "/paid/a")); err != nil || !won {
			t.Fatalf("claim: %v %v", won, err)
		}
		if err := a.unclaim(ctx, "x402_two"); err != nil {
			t.Fatalf("A withdraws its own unsettled claim: %v", err)
		}
		if err := b.markSettled(ctx, "x402_two", "0xtx"); !errors.Is(err, errClaimMoved) {
			t.Fatalf("B settled a claim A withdrew: %v, want errClaimMoved", err)
		}
		if _, found, _ := b.get(ctx, "x402_two"); found {
			t.Fatal("a refused mark left a row behind")
		}
		// And a withdrawn claim is not withdrawn twice.
		if err := b.unclaim(ctx, "x402_two"); !errors.Is(err, errClaimMoved) {
			t.Fatalf("a claim already withdrawn was withdrawn again: %v, want errClaimMoved", err)
		}
	})
}

// gatedLedger holds the ledger's calls where a test says, so two replicas can be caught
// in the middle of settling one authorization.
type gatedLedger struct {
	*halfDown
	mu       sync.Mutex
	draws    int
	onDraw   func(call int) error // before the nth draw reaches the ledger; an error is the ledger's answer
	onCredit func()               // before a settlement's credit reaches the ledger
}

func (g *gatedLedger) Draw(ctx context.Context, in money.UsageInput) (string, bool, error) {
	g.mu.Lock()
	g.draws++
	call, hook := g.draws, g.onDraw
	g.mu.Unlock()
	if hook != nil {
		if err := hook(call); err != nil {
			return "", false, err
		}
	}
	return g.halfDown.Draw(ctx, in)
}

func (g *gatedLedger) Deposit(ctx context.Context, in money.DepositInput) (string, error) {
	g.mu.Lock()
	hook := g.onCredit
	g.mu.Unlock()
	if hook != nil && in.Tags == "x402" {
		hook()
	}
	return g.halfDown.Deposit(ctx, in)
}

// replica is another pod beside the harness's, on the leg's engine: on a server its own
// pool on the rail's store and its own state, in a file the harness's.
func (h *harness) replica(t *testing.T, dsn string, open sqltest.Open) *cloud.Service[state] {
	t.Helper()
	if dsn == "" {
		return live
	}
	t.Setenv(locus.Env, dsn)
	db := open(namespace.System(), "x402")
	t.Setenv(locus.Env, "")
	return &cloud.Service[state]{Base: live.Base, State: state{
		store: &store{db: db, d: dialect.Of(db)}, cfg: live.State.cfg, audit: live.State.audit, drain: live.State.drain,
	}}
}

// queued reports that b's work on id waits behind another's: on a server a backend asking
// for an advisory lock it has not been granted, in a file a second caller of the Keyed.
func queued(b *cloud.Service[state], id string) bool {
	if db := b.State.store.db; dialect.Of(db).Postgres() {
		var n int
		return db.QueryRow(`SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&n) == nil && n > 0
	}
	return b.State.settling.Refs(id) > 1
}

func idOf(t *testing.T, payment string) string {
	t.Helper()
	p, err := protocol.ParsePayment(payment)
	if err != nil {
		t.Fatal(err)
	}
	a := p.Payload.Authorization
	return settlementID(a.From, a.Nonce)
}

// Replica A claims an authorization and its draw is refused; replica B presents the same
// authorization meanwhile. One authorization is settled by one replica at a time, so B
// waits for A to finish withdrawing before it claims, and what B settles is a claim that
// stays: the same authorization presented for another resource is refused as spent, and
// the payer paid once.
//
// Without a lock across replicas, B read A's claim, drew and credited, and A's withdrawal
// then deleted the row B's mark needed: B was told it settled, and the authorization paid
// for a second resource with no second payment.
func TestTwoReplicasSettleOneAuthorizationInTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		dsn := os.Getenv(locus.Env)
		h := newHarness(t)
		price := money.FromCents(100)
		terms := Terms{Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient}
		pubRegistry(map[string]Terms{"/paid/tool": terms, "/paid/other": terms})
		a, b := live, h.replica(t, dsn, open)
		g := &gatedLedger{halfDown: h.ledger}
		finance.Publish(g)

		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)
		id := idOf(t, payment)
		as := cloud.For(context.Background(), h.payerOrg)

		var (
			aDraws   = make(chan struct{}) // A's draw is at the ledger
			wallet   = make(chan struct{}) // the wallet's answer to A: short
			credit   = make(chan struct{}) // B's credit may reach the ledger
			bCredits = make(chan struct{}, 2)
		)
		g.onDraw = func(call int) error {
			if call != 1 {
				return nil
			}
			close(aDraws)
			<-wallet
			return finance.ErrShortWallet
		}
		g.onCredit = func() {
			bCredits <- struct{}{}
			<-credit
		}

		settle := func(s *cloud.Service[state], resource string) <-chan *client.Settled {
			out := make(chan *client.Settled, 1)
			go func() {
				got, err := rail{s}.planeSettle(as, &client.SettleIn{Resource: resource, Payment: payment})
				if err != nil {
					t.Errorf("settle %s: %v", resource, err)
				}
				out <- got
			}()
			return out
		}
		first := settle(a, "/paid/tool")
		<-aDraws
		second := settle(b, "/paid/tool")

		// B is either at the ledger with A's claim in hand, or waiting for A to be done.
		deadline := time.Now().Add(10 * time.Second)
		for len(bCredits) == 0 && !queued(b, id) {
			if time.Now().After(deadline) {
				t.Fatal("B neither queued behind A nor reached the ledger")
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(wallet)
		if refused := <-first; refused == nil || refused.OK || refused.Code != protocol.ReasonInsufficientFunds {
			t.Fatalf("A, whose draw was refused = %+v, want refused for funds", refused)
		}
		close(credit)
		if paid := <-second; paid == nil || !paid.OK {
			t.Fatalf("B, whose draw landed = %+v, want settled", paid)
		}

		st, found, err := a.State.store.get(context.Background(), id)
		if err != nil || !found || !st.Settled || st.Resource != "/paid/tool" {
			t.Fatalf("the claim B settled = %+v found=%v err=%v, want it settled and kept", st, found, err)
		}
		if other, err := (rail{b}).planeSettle(as, &client.SettleIn{Resource: "/paid/other", Payment: payment}); err != nil || other.OK || other.Code != reasonReplay {
			t.Fatalf("the settled authorization for another resource = %+v %v, want refused as spent", other, err)
		}
		h.tie(price, "after one authorization was presented for two resources")
	})
}

// A void asked of one replica while another settles the same authorization waits for it:
// the settlement is claimed, drawn and marked, or given up, and not both at once.
func TestAVoidWaitsForASettlementOnAnotherReplica(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		dsn := os.Getenv(locus.Env)
		h := newHarness(t)
		price := money.FromCents(100)
		terms := Terms{Amount: price, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient}
		pubRegistry(map[string]Terms{"/paid/tool": terms})
		a, b := live, h.replica(t, dsn, open)
		g := &gatedLedger{halfDown: h.ledger}
		finance.Publish(g)

		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)
		as := cloud.For(context.Background(), h.payerOrg)

		drawing, release := make(chan struct{}), make(chan struct{})
		g.onDraw = func(int) error {
			close(drawing)
			<-release
			return nil
		}
		settled := make(chan *client.Settled, 1)
		go func() {
			got, err := rail{a}.planeSettle(as, &client.SettleIn{Resource: "/paid/tool", Payment: payment})
			if err != nil {
				t.Errorf("settle: %v", err)
			}
			settled <- got
		}()
		<-drawing

		voided := make(chan *client.Voided, 1)
		go func() {
			got, err := rail{b}.planeVoid(as, &client.VoidIn{Resource: "/paid/tool", Payment: payment})
			if err != nil {
				t.Errorf("void: %v", err)
			}
			voided <- got
		}()
		select {
		case v := <-voided:
			t.Fatalf("a void answered %+v while another replica was settling the payment", v)
		case <-time.After(400 * time.Millisecond):
		}
		close(release)
		if paid := <-settled; paid == nil || !paid.OK {
			t.Fatalf("the settlement = %+v, want settled", paid)
		}
		if v := <-voided; v == nil || !v.Spent || v.Returned.Decimal != "0" {
			t.Fatalf("the void after the settlement = %+v, want spent and nothing returned", v)
		}
		h.tie(price, "after a void met a settlement")
	})
}

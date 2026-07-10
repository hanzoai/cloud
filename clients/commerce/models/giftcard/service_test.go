package giftcard

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/giftcardredemption"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// nsDB builds a datastore scoped to a tenant the way production does:
// the namespace rides on the CONTEXT (org.Namespaced → nscontext.WithNamespace),
// which is what the SQL layer reads (db getNamespace(ctx)). Setting only the
// datastore struct field does NOT scope queries.
func nsDB(parent context.Context, ns string) *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(parent, ns))
}

// issue creates a $50 gift card in the given org namespace.
func issue(t *testing.T, db *datastore.Datastore, code string, cents currency.Cents) *GiftCard {
	t.Helper()
	g := New(db)
	g.Code = code
	g.InitialBalanceCents = cents
	g.Currency = "usd"
	if err := g.Create(); err != nil {
		t.Fatalf("issue gift card: %v", err)
	}
	return g
}

func TestRedeem_DebitsAndProjectsBalance(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	g := issue(t, db, "GIFT-A", 5000) // $50

	if bal, err := BalanceCents(db, g); err != nil || bal != 5000 {
		t.Fatalf("initial balance = %d, err=%v; want 5000", bal, err)
	}

	_, bal, err := Redeem(db, g, 1500, "usd", "order_1", "idem_1")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if bal != 3500 {
		t.Fatalf("balance after $15 redeem = %d; want 3500", bal)
	}

	// A second, DISTINCT redemption is additive.
	_, bal, err = Redeem(db, g, 2000, "usd", "order_2", "idem_2")
	if err != nil {
		t.Fatalf("redeem 2: %v", err)
	}
	if bal != 1500 {
		t.Fatalf("balance after further $20 redeem = %d; want 1500", bal)
	}
}

// TestRedeem_IdempotentReplay is the money-correctness proof Red will attack:
// re-submitting the SAME idempotency key MUST debit exactly once, even though
// the underlying datastore.RunInTransaction is a no-op.
func TestRedeem_IdempotentReplay(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	g := issue(t, db, "GIFT-IDEM", 5000)

	line1, bal1, err := Redeem(db, g, 1500, "usd", "order_1", "idem_dup")
	if err != nil {
		t.Fatalf("redeem 1: %v", err)
	}

	// Replay with the SAME key. Even if the caller passes a DIFFERENT amount,
	// the stored line (and thus the debit) must be unchanged.
	line2, bal2, err := Redeem(db, g, 9999, "usd", "order_1", "idem_dup")
	if err != nil {
		t.Fatalf("redeem replay: %v", err)
	}
	if line1.Id() != line2.Id() {
		t.Fatalf("replay produced a different line: %s vs %s", line1.Id(), line2.Id())
	}
	if line2.AmountCents != 1500 {
		t.Fatalf("replay changed the debit amount to %d; want 1500", line2.AmountCents)
	}
	if bal1 != 3500 || bal2 != 3500 {
		t.Fatalf("balance drifted on replay: bal1=%d bal2=%d; want 3500 each", bal1, bal2)
	}

	// Exactly ONE ledger line exists for this key.
	lines := make([]*giftcardredemption.GiftCardRedemption, 0, 4)
	if _, err := giftcardredemption.Query(db).Filter("GiftCardId=", g.Id()).GetAll(&lines); err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("idempotent replay created %d ledger lines; want 1", len(lines))
	}
}

// TestRedeem_ConcurrentSameKey hammers the same idempotency key from many
// goroutines. The deterministic id + ON CONFLICT upsert must collapse them to
// a single debit — this is the concurrent double-spend Red will run.
func TestRedeem_ConcurrentSameKey(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	g := issue(t, db, "GIFT-RACE", 5000)

	const n = 25
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			// Each goroutine uses its OWN datastore handle on the same ctx/ns,
			// mirroring separate requests.
			gdb := nsDB(c, "acme")
			gc := New(gdb)
			_ = gc.GetById(g.Id())
			_, _, _ = Redeem(gdb, gc, 1000, "usd", "order_race", "idem_race")
		}()
	}
	wg.Wait()

	// After the storm, exactly one $10 debit must exist and balance = $40.
	lines := make([]*giftcardredemption.GiftCardRedemption, 0, n)
	if _, err := giftcardredemption.Query(db).Filter("GiftCardId=", g.Id()).GetAll(&lines); err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("concurrent same-key redeem created %d ledger lines; want 1 (double-spend!)", len(lines))
	}
	bal, err := BalanceCents(db, g)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal != 4000 {
		t.Fatalf("balance after concurrent same-key redeem = %d; want 4000", bal)
	}
}

func TestRedeem_OverRedeemRejected(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	g := issue(t, db, "GIFT-OVER", 1000) // $10

	if _, _, err := Redeem(db, g, 1500, "usd", "", "idem_over"); err != ErrInsufficientFunds {
		t.Fatalf("over-redeem err = %v; want ErrInsufficientFunds", err)
	}
	// Balance untouched.
	if bal, _ := BalanceCents(db, g); bal != 1000 {
		t.Fatalf("balance after rejected over-redeem = %d; want 1000", bal)
	}
}

func TestRedeem_Guards(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	g := issue(t, db, "GIFT-GUARD", 5000)

	if _, _, err := Redeem(db, g, 0, "usd", "", "k"); err != ErrNonPositiveAmount {
		t.Fatalf("zero amount err = %v; want ErrNonPositiveAmount", err)
	}
	if _, _, err := Redeem(db, g, -5, "usd", "", "k"); err != ErrNonPositiveAmount {
		t.Fatalf("negative amount err = %v; want ErrNonPositiveAmount", err)
	}
	if _, _, err := Redeem(db, g, 100, "usd", "", ""); err != ErrMissingIdempotency {
		t.Fatalf("missing key err = %v; want ErrMissingIdempotency", err)
	}
	if _, _, err := Redeem(db, g, 100, "eur", "", "k"); err != ErrCurrencyMismatch {
		t.Fatalf("currency mismatch err = %v; want ErrCurrencyMismatch", err)
	}

	g.Disabled = true
	if _, _, err := Redeem(db, g, 100, "usd", "", "k"); err != ErrNotRedeemable {
		t.Fatalf("disabled card err = %v; want ErrNotRedeemable", err)
	}
}

func TestVoid_RestoresBalance(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(c, "acme")

	g := issue(t, db, "GIFT-VOID", 5000)
	line, _, err := Redeem(db, g, 2000, "usd", "", "idem_void")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if bal, _ := BalanceCents(db, g); bal != 3000 {
		t.Fatalf("balance after redeem = %d; want 3000", bal)
	}

	bal, err := Void(db, g, line.Id())
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if bal != 5000 {
		t.Fatalf("balance after void = %d; want 5000 (restored)", bal)
	}
	// Voiding again is a no-op.
	if bal, _ = Void(db, g, line.Id()); bal != 5000 {
		t.Fatalf("double-void changed balance to %d; want 5000", bal)
	}
}

// TestRedeem_TenantIsolation proves a gift card in org "acme" is invisible to a
// datastore scoped to org "beta" — cross-tenant redemption is impossible.
func TestRedeem_TenantIsolation(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	acme := nsDB(c, "acme")
	g := issue(t, acme, "GIFT-ISO", 5000)

	beta := nsDB(c, "beta")

	// beta cannot load acme's card by its id.
	stolen := New(beta)
	if err := stolen.GetById(g.Id()); err == nil {
		t.Fatalf("cross-tenant GetById unexpectedly succeeded — tenant isolation broken")
	}

	// And the ledger query in beta's namespace sees no lines for it.
	drawn, err := DrawnCents(beta, g.Id())
	if err != nil {
		t.Fatalf("drawn: %v", err)
	}
	if drawn != 0 {
		t.Fatalf("beta sees %d drawn on acme's card; want 0", drawn)
	}
}

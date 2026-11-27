package x402

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
)

// The claim is what makes a settlement happen once: of any number of writers
// presenting one authorization at once, exactly one is told it won the row, and the rest
// are handed the row that won. It is the primary key that decides, on a file and on a
// server alike.
func TestOneWriterWinsAClaimOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := open(namespace.System(), "x402")
		s := &store{db: db, d: dialect.Of(db)}
		if err := s.remigrate(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		const writers = 16
		var won atomic.Int32
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st := &Settlement{ID: "x402_same", PayerOrg: "acme", From: "0xabc", Nonce: "0x01", Resource: "/paid/tool", Payee: "w1",
					PayeeOrg: "globex", Amount: "1000000000000000000", SettledVia: "ledger", CreatedAt: int64(100 + i)}
				claimed, existing, err := s.claim(ctx, st)
				switch {
				case err != nil:
					t.Errorf("claim: %v", err)
				case claimed:
					won.Add(1)
				case existing == nil || existing.ID != "x402_same":
					t.Errorf("a lost claim was handed %+v, want the row that won", existing)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d writers won one claim, want exactly 1", won.Load())
		}

		// A claim is withdrawn only while unsettled; a settled one stays, and the
		// refusal says so.
		if err := s.markSettled(ctx, "x402_same", "0xtx"); err != nil {
			t.Fatal(err)
		}
		if err := s.unclaim(ctx, "x402_same"); !errors.Is(err, errClaimMoved) {
			t.Fatalf("withdrawing a settled claim: %v, want errClaimMoved", err)
		}
		if err := s.markSettled(ctx, "x402_same", "0xother"); !errors.Is(err, errClaimMoved) {
			t.Fatalf("marking a settled claim again: %v, want errClaimMoved", err)
		}
		if st, found, err := s.get(ctx, "x402_same"); err != nil || !found || !st.Settled || st.TxHash != "0xtx" {
			t.Fatalf("a settled claim after an unclaim: %+v found=%v err=%v", st, found, err)
		}
		if claimed, _, err := s.claim(ctx, &Settlement{ID: "x402_other", PayerOrg: "acme", From: "0xabc", Nonce: "0x02", Resource: "/paid/tool",
			Payee: "w1", PayeeOrg: "globex", Amount: "1", SettledVia: "ledger", CreatedAt: 1}); err != nil || !claimed {
			t.Fatalf("a second authorization: claimed=%v err=%v", claimed, err)
		}
		if err := s.unclaim(ctx, "x402_other"); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := s.get(ctx, "x402_other"); found {
			t.Fatal("an unsettled claim survived an unclaim")
		}
	})
}

// An authorization is held for one resource: asked again for another it answers the
// first, and a hold given up for the wrong resource stays unspent.
func TestAHoldIsForOneResourceOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		db := open(namespace.System(), "x402")
		s := &store{db: db, d: dialect.Of(db)}
		if err := s.remigrate(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		payer := account.Org("acme")
		h, err := s.hold(ctx, "x402_h", "/a", 5000, payer)
		if err != nil || h.resource != "/a" || h.given {
			t.Fatalf("first hold: %+v %v", h, err)
		}
		if h, err = s.hold(ctx, "x402_h", "/b", 5000, payer); err != nil || h.resource != "/a" {
			t.Fatalf("a hold asked for another resource: %+v %v, want /a", h, err)
		}
		if err := s.giveUp(ctx, "x402_h", "/b", 5000); err != nil {
			t.Fatal(err)
		}
		if h, _, _ := s.held(ctx, "x402_h"); h.given {
			t.Fatal("a hold was given up for a resource it is not held for")
		}
		if err := s.giveUp(ctx, "x402_h", "/a", 5000); err != nil {
			t.Fatal(err)
		}
		if h, ok, _ := s.held(ctx, "x402_h"); !ok || !h.given {
			t.Fatalf("a hold given up for its resource: %+v", h)
		}
		// A void that arrives before the hold holds the id first, given.
		if err := s.giveUp(ctx, "x402_early", "/c", 5000); err != nil {
			t.Fatal(err)
		}
		if h, ok, _ := s.held(ctx, "x402_early"); !ok || !h.given || h.resource != "/c" {
			t.Fatalf("an early void: %+v", h)
		}
	})
}

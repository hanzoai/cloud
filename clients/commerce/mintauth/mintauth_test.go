// Copyright © 2026 Hanzo AI. MIT License.

package mintauth

import (
	"context"
	"errors"
	"testing"
)

// fakeLedger is a minimal Guarded stand-in so the invariant can be proven without
// importing the transaction model (which would be a cycle).
type fakeLedger struct{ mint bool }

func (f fakeLedger) MintRequiresAuthorization() bool { return f.mint }

// notGuarded does NOT implement Guarded — a non-ledger entity.
type notGuarded struct{}

// TestEnforce_TruthTable pins the ONE money-mint invariant: a spendable mint is
// refused iff the context is gated AND not authorized; everything else passes.
func TestEnforce_TruthTable(t *testing.T) {
	bg := context.Background()
	gated := WithGate(bg)
	authorized := WithAuthorized(bg)
	gatedAuth := WithAuthorized(WithGate(bg))

	mint := fakeLedger{mint: true}
	nonMint := fakeLedger{mint: false}

	cases := []struct {
		name    string
		ctx     context.Context
		val     interface{}
		wantErr bool
	}{
		{"gated+unauthorized mint → REFUSE", gated, mint, true},
		{"gated+authorized mint → allow", gatedAuth, mint, false},
		{"ungated mint (cron/test) → allow", bg, mint, false},
		{"authorized-but-ungated mint → allow", authorized, mint, false},
		{"gated+unauthorized NON-mint → allow", gated, nonMint, false},
		{"gated+unauthorized non-ledger entity → allow", gated, notGuarded{}, false},
		{"nil ctx mint → allow (ungated)", nil, mint, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Enforce(tc.ctx, tc.val)
			if tc.wantErr && !errors.Is(err, ErrNotAuthorized) {
				t.Fatalf("Enforce=%v, want ErrNotAuthorized", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Enforce=%v, want nil", err)
			}
		})
	}
}

// TestMarkers_Compose proves Gate and Grant are orthogonal and idempotent — a
// context can be gated, then authorized, and both hold; re-applying is a no-op.
func TestMarkers_Compose(t *testing.T) {
	ctx := context.Background()
	if Gated(ctx) || Authorized(ctx) {
		t.Fatal("background context must be neither gated nor authorized")
	}
	ctx = WithGate(ctx)
	if !Gated(ctx) || Authorized(ctx) {
		t.Fatal("after WithGate: want gated, not authorized")
	}
	ctx = WithAuthorized(ctx)
	if !Gated(ctx) || !Authorized(ctx) {
		t.Fatal("after WithAuthorized: want gated AND authorized")
	}
	// Idempotent: no new value wrappers when already set.
	if WithGate(ctx) != ctx || WithAuthorized(ctx) != ctx {
		t.Fatal("re-applying a marker already present must be a no-op")
	}
}

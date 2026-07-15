// Copyright © 2026 Hanzo AI. MIT License.

package commerceclient_test

import (
	"context"
	"testing"

	inproc "github.com/hanzoai/cloud/clients/commerceclient"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/transaction"
	"github.com/hanzoai/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// seedDeposit writes a raw iam-user deposit into org's namespace — the SAME ledger + ancestor
// the real deposit path (billing.zapDeposit) writes and BalanceCents reads. The plain test
// context is ungated (mintauth only gates a *gin.Context), so the mint sink permits it.
func seedDeposit(t *testing.T, ctx context.Context, org, subject string, cents int64) {
	t.Helper()
	writeTx(t, ctx, org, subject, transaction.Deposit, cents)
}

// seedHold writes a raw hold against subject's iam-user wallet — the reservation BalanceCents
// subtracts from balance.
func seedHold(t *testing.T, ctx context.Context, org, subject string, cents int64) {
	t.Helper()
	writeTx(t, ctx, org, subject, transaction.Hold, cents)
}

func writeTx(t *testing.T, ctx context.Context, org, subject string, kind transaction.Type, cents int64) {
	t.Helper()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve org %q: %v", org, err)
	}
	db := datastore.New(o.Namespaced(ctx))
	tr := transaction.New(db)
	tr.Type = kind
	tr.DestinationId = subject
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.MustCreate()
}

// TestBalanceCents proves the native in-process balance read end-to-end over a REAL embedded
// commerce ledger: seed a deposit through the same ledger the HTTP path writes, then read the
// available balance directly (no HTTP hop). Covers subject normalization, the usd default, the
// holds clamp, and the honest-zero for a subject with no transactions.
func TestBalanceCents(t *testing.T) {
	emb := bootCommerce(t)
	_ = emb // published process-wide by bootCommerce; BalanceCents resolves it via currentEmbedded.
	ctx := context.Background()

	const org = "wallettest"
	seedDeposit(t, ctx, org, "z", 5000) // $50 into hanzo/z-style wallet

	t.Run("deposit_read_back", func(t *testing.T) {
		got, err := inproc.BalanceCents(ctx, org, "z", "usd", false)
		if err != nil {
			t.Fatalf("BalanceCents: %v", err)
		}
		if got != 5000 {
			t.Fatalf("BalanceCents = %d, want 5000", got)
		}
	})

	t.Run("subject_lowercased_and_trimmed", func(t *testing.T) {
		got, err := inproc.BalanceCents(ctx, org, "  Z  ", "usd", false)
		if err != nil {
			t.Fatalf("BalanceCents: %v", err)
		}
		if got != 5000 {
			t.Fatalf("normalized subject BalanceCents = %d, want 5000 (same wallet as \"z\")", got)
		}
	})

	t.Run("empty_currency_defaults_usd", func(t *testing.T) {
		got, err := inproc.BalanceCents(ctx, org, "z", "", false)
		if err != nil {
			t.Fatalf("BalanceCents: %v", err)
		}
		if got != 5000 {
			t.Fatalf("default-currency BalanceCents = %d, want 5000", got)
		}
	})

	t.Run("holds_clamp_available_at_zero", func(t *testing.T) {
		seedDeposit(t, ctx, org, "poor", 3000)
		seedHold(t, ctx, org, "poor", 5000) // hold exceeds balance → available would be -2000
		got, err := inproc.BalanceCents(ctx, org, "poor", "usd", false)
		if err != nil {
			t.Fatalf("BalanceCents: %v", err)
		}
		if got != 0 {
			t.Fatalf("BalanceCents = %d, want 0 (Balance-Holds clamped at zero)", got)
		}
	})

	t.Run("no_transactions_is_honest_zero", func(t *testing.T) {
		got, err := inproc.BalanceCents(ctx, org, "ghost", "usd", false)
		if err != nil {
			t.Fatalf("BalanceCents: %v", err)
		}
		if got != 0 {
			t.Fatalf("BalanceCents = %d, want 0 for a subject with no ledger rows", got)
		}
	})
}

// TestBalanceCents_FailClosed proves the two refusal paths: empty args error, and a read while
// commerce is NOT co-resident returns an ERROR (never a phantom 0), so a caller can tell "not
// wired" from a real zero balance.
func TestBalanceCents_FailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("empty_args_error", func(t *testing.T) {
		if _, err := inproc.BalanceCents(ctx, "", "z", "usd", false); err == nil {
			t.Error("empty org must error")
		}
		if _, err := inproc.BalanceCents(ctx, "acme", "  ", "usd", false); err == nil {
			t.Error("empty subject must error")
		}
	})

	t.Run("not_co_resident_errors_not_zero", func(t *testing.T) {
		inproc.PublishEmbedded(nil)
		t.Cleanup(func() { inproc.PublishEmbedded(nil) })

		got, err := inproc.BalanceCents(ctx, "acme", "z", "usd", false)
		if err == nil {
			t.Fatal("BalanceCents must error when commerce is not co-resident (never a phantom 0)")
		}
		if got != 0 {
			t.Fatalf("error path must return 0, got %d", got)
		}
	})
}

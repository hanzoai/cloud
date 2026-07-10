package billing

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestDeposit_OverCeiling_400 — H1: a single deposit is bounded by the
// server-authoritative ceiling (COMMERCE_DEPOSIT_MAX_CENTS). No unbounded mint in
// one request, even for a trusted caller.
func TestDeposit_OverCeiling_400(t *testing.T) {
	t.Setenv("COMMERCE_DEPOSIT_MAX_CENTS", "1000")
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1d-ceiling")

	body := `{"user":"h1d-ceiling/alice","amount":2000}` // over the 1000 ceiling
	w := invokeMoneyHandler(org, ctx, Deposit, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("over-ceiling deposit: status=%d body=%s, want 400", w.Code, w.Body.String())
	}
}

// TestDeposit_UnderCeiling_201 — control: a deposit at/under the ceiling still mints.
func TestDeposit_UnderCeiling_201(t *testing.T) {
	t.Setenv("COMMERCE_DEPOSIT_MAX_CENTS", "1000")
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1d-under")

	body := `{"user":"h1d-under/alice","amount":1000}`
	w := invokeMoneyHandler(org, ctx, Deposit, body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("at-ceiling deposit: status=%d body=%s, want 201", w.Code, w.Body.String())
	}
}

// TestDeposit_IdempotencyKey_SingleEffect — H1: a retry/double-submit carrying the
// SAME X-Idempotency-Key credits AT MOST ONCE; the replay returns the first
// result (same transactionId), not a second credit.
func TestDeposit_IdempotencyKey_SingleEffect(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1d-idem")

	body := `{"user":"h1d-idem/alice","amount":500}`
	hdr := map[string]string{"X-Idempotency-Key": "dep-key-1"}

	w1 := invokeMoneyHandler(org, ctx, Deposit, body, hdr)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first deposit: status=%d body=%s, want 201", w1.Code, w1.Body.String())
	}
	tid1 := txID(t, w1)
	if tid1 == "" {
		t.Fatal("first deposit missing transactionId")
	}

	w2 := invokeMoneyHandler(org, ctx, Deposit, body, hdr)
	if w2.Code != http.StatusOK {
		t.Fatalf("replay deposit: status=%d body=%s, want 200 (idempotent replay)", w2.Code, w2.Body.String())
	}
	if tid2 := txID(t, w2); tid2 != tid1 {
		t.Fatalf("replay minted a NEW transaction %q (first was %q) — double credit", tid2, tid1)
	}
}

// TestDeposit_NoKey_DistinctDeposits — H1 negative control: WITHOUT an idempotency
// key, distinct deposits to the same user are legitimately additive (repeated
// settlements) — we never dedupe by amount.
func TestDeposit_NoKey_DistinctDeposits(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	org := moneyOrg("h1d-nokey")

	body := `{"user":"h1d-nokey/alice","amount":500}`
	w1 := invokeMoneyHandler(org, ctx, Deposit, body, nil)
	w2 := invokeMoneyHandler(org, ctx, Deposit, body, nil)
	if w1.Code != http.StatusCreated || w2.Code != http.StatusCreated {
		t.Fatalf("two keyless deposits: statuses=%d,%d, want 201,201", w1.Code, w2.Code)
	}
	if txID(t, w1) == txID(t, w2) {
		t.Fatal("two keyless deposits collapsed to one transaction — over-deduped")
	}
}

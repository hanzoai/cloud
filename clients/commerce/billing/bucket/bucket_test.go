package bucket

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

const subject = "hanzo/alice"

func dep(amount int64, tags string, expiresAt time.Time) *transaction.Transaction {
	t := &transaction.Transaction{}
	t.Type = transaction.Deposit
	t.DestinationId = subject
	t.DestinationKind = "iam-user"
	t.Currency = currency.USD
	t.Amount = currency.Cents(amount)
	t.Tags = tags
	t.ExpiresAt = expiresAt
	return t
}

func wd(amount int64, tags string) *transaction.Transaction {
	t := &transaction.Transaction{}
	t.Type = transaction.Withdraw
	t.SourceId = subject
	t.SourceKind = "iam-user"
	t.Currency = currency.USD
	t.Amount = currency.Cents(amount)
	t.Tags = tags
	return t
}

func TestDepositKind(t *testing.T) {
	cases := map[string]Kind{
		"starter-credit":          Credit,
		"included-credit:2026-07": Credit,
		"credit:promo":            Credit,
		"grant:onboarding":        Credit,
		"topup":                   Prepaid,
		"husd":                    Prepaid,
		"":                        Prepaid, // bare deposit = real money (fail-closed)
		"deposit":                 Prepaid,
		"STARTER-CREDIT":          Credit, // case-insensitive
	}
	for tag, want := range cases {
		if got := DepositKind(tag); got != want {
			t.Errorf("DepositKind(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestIsGPUWithdrawal(t *testing.T) {
	yes := []string{"gpu", "gpu-hour", "gpu:h100", "GPU"}
	no := []string{"api-usage", "", "gpush", "compute"}
	for _, s := range yes {
		if !IsGPUWithdrawal(s) {
			t.Errorf("IsGPUWithdrawal(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsGPUWithdrawal(s) {
			t.Errorf("IsGPUWithdrawal(%q) = true, want false", s)
		}
	}
}

// The reconciliation invariant must hold for EVERY input:
//
//	CreditsRemaining + PrepaidBalance == Balance
//	Available                        == min(Balance, CreditsRemaining + PrepaidAvailable + 0)  (holds on prepaid)
func assertReconciles(t *testing.T, s Split) {
	t.Helper()
	if s.CreditsRemaining+s.PrepaidBalance != s.Balance {
		t.Fatalf("reconcile: credits(%d)+prepaid(%d) = %d != balance %d",
			s.CreditsRemaining, s.PrepaidBalance, s.CreditsRemaining+s.PrepaidBalance, s.Balance)
	}
	if s.CreditsRemaining < 0 || s.PrepaidAvailable < 0 || s.Available < 0 {
		t.Fatalf("negative bucket: %+v", s)
	}
}

func TestSplit_CreditsFirst_NonGpu(t *testing.T) {
	now := time.Now()
	// $100 welcome credit + $50 real top-up; $30 of api-usage spend.
	transs := []*transaction.Transaction{
		dep(10000, "starter-credit", now.AddDate(1, 0, 0)),
		dep(5000, "topup", time.Time{}),
		wd(3000, "api-usage"),
	}
	s := Compute(transs, subject, now)
	assertReconciles(t, s)

	if s.CreditsGranted != 10000 {
		t.Errorf("CreditsGranted = %d, want 10000", s.CreditsGranted)
	}
	// Non-GPU spend eats credits first: 10000 - 3000 = 7000 credit left.
	if s.CreditsRemaining != 7000 {
		t.Errorf("CreditsRemaining = %d, want 7000", s.CreditsRemaining)
	}
	// Prepaid untouched (spend covered by credits): 5000.
	if s.PrepaidBalance != 5000 {
		t.Errorf("PrepaidBalance = %d, want 5000", s.PrepaidBalance)
	}
	if s.Balance != 12000 {
		t.Errorf("Balance = %d, want 12000", s.Balance)
	}
}

func TestSplit_NonGpuOverflow_SpillsToPrepaid(t *testing.T) {
	now := time.Now()
	// $100 credit + $50 prepaid; $130 usage → credits exhausted, $30 hits prepaid.
	transs := []*transaction.Transaction{
		dep(10000, "starter-credit", now.AddDate(1, 0, 0)),
		dep(5000, "topup", time.Time{}),
		wd(13000, "api-usage"),
	}
	s := Compute(transs, subject, now)
	assertReconciles(t, s)
	if s.CreditsRemaining != 0 {
		t.Errorf("CreditsRemaining = %d, want 0 (exhausted)", s.CreditsRemaining)
	}
	if s.PrepaidBalance != 2000 { // 5000 - (13000-10000)
		t.Errorf("PrepaidBalance = %d, want 2000", s.PrepaidBalance)
	}
}

// THE GPU RULE: a GPU charge draws ONLY prepaid, NEVER credits. With credits
// present and prepaid present, a GPU withdrawal reduces prepaid and leaves the
// credit bucket completely untouched.
func TestSplit_GpuNeverTouchesCredits(t *testing.T) {
	now := time.Now()
	transs := []*transaction.Transaction{
		dep(10000, "starter-credit", now.AddDate(1, 0, 0)),
		dep(5000, "topup", time.Time{}),
		wd(4000, "gpu-hour"), // GPU spend
	}
	s := Compute(transs, subject, now)
	assertReconciles(t, s)
	// Credits are FULLY intact — GPU spend did not touch them.
	if s.CreditsRemaining != 10000 {
		t.Errorf("CreditsRemaining = %d, want 10000 (GPU must not touch credits)", s.CreditsRemaining)
	}
	// GPU spend came out of prepaid: 5000 - 4000 = 1000.
	if s.PrepaidBalance != 1000 {
		t.Errorf("PrepaidBalance = %d, want 1000", s.PrepaidBalance)
	}
	if s.PrepaidAvailable != 1000 {
		t.Errorf("PrepaidAvailable = %d, want 1000", s.PrepaidAvailable)
	}
}

// A GPU charge with ONLY credits and no prepaid drives prepaid negative in the
// projection (the debit lands), which is exactly what the server-side charge
// gate must PREVENT up front — proving credits cannot silently cover GPUs.
func TestSplit_GpuWithNoPrepaid_ExhaustsPrepaidNotCredits(t *testing.T) {
	now := time.Now()
	transs := []*transaction.Transaction{
		dep(10000, "starter-credit", now.AddDate(1, 0, 0)),
		wd(4000, "gpu"),
	}
	s := Compute(transs, subject, now)
	// Credits remain fully intact; only prepaid absorbed the GPU spend (negative).
	if s.CreditsRemaining != 10000 {
		t.Errorf("CreditsRemaining = %d, want 10000", s.CreditsRemaining)
	}
	if s.PrepaidBalance != -4000 {
		t.Errorf("PrepaidBalance = %d, want -4000 (GPU debit on empty prepaid)", s.PrepaidBalance)
	}
	// PrepaidAvailable clamps at 0 — the gate reads 0 and refuses a GPU charge.
	if s.PrepaidAvailable != 0 {
		t.Errorf("PrepaidAvailable = %d, want 0", s.PrepaidAvailable)
	}
	// Reconciliation still holds (credits 10000 + prepaid -4000 = balance 6000).
	if s.CreditsRemaining+s.PrepaidBalance != s.Balance {
		t.Errorf("reconcile broke: %+v", s)
	}
}

func TestSplit_ExpiredCreditGrantedNotRemaining(t *testing.T) {
	now := time.Now()
	transs := []*transaction.Transaction{
		dep(10000, "starter-credit", now.Add(-time.Hour)), // expired
		dep(2000, "topup", time.Time{}),
	}
	s := Compute(transs, subject, now)
	assertReconciles(t, s)
	// Granted total still counts the expired grant.
	if s.CreditsGranted != 10000 {
		t.Errorf("CreditsGranted = %d, want 10000 (incl expired)", s.CreditsGranted)
	}
	// But it contributes nothing spendable.
	if s.CreditsRemaining != 0 {
		t.Errorf("CreditsRemaining = %d, want 0 (expired)", s.CreditsRemaining)
	}
	if s.PrepaidBalance != 2000 {
		t.Errorf("PrepaidBalance = %d, want 2000", s.PrepaidBalance)
	}
}

func TestSplit_HoldsReducePrepaidAvailable(t *testing.T) {
	now := time.Now()
	transs := []*transaction.Transaction{
		dep(3000, "topup", time.Time{}),
		{Type: transaction.Hold, DestinationId: subject, SourceId: "svc", Amount: 1000, Currency: currency.USD},
	}
	s := Compute(transs, subject, now)
	if s.Holds != 1000 {
		t.Errorf("Holds = %d, want 1000", s.Holds)
	}
	if s.PrepaidBalance != 3000 {
		t.Errorf("PrepaidBalance = %d, want 3000", s.PrepaidBalance)
	}
	if s.PrepaidAvailable != 2000 { // 3000 - 1000 hold
		t.Errorf("PrepaidAvailable = %d, want 2000", s.PrepaidAvailable)
	}
	if s.Available != 2000 {
		t.Errorf("Available = %d, want 2000", s.Available)
	}
}

func TestSplit_AllotmentIsCredit(t *testing.T) {
	now := time.Now()
	transs := []*transaction.Transaction{
		dep(2000, "included-credit:2026-07", now.AddDate(0, 1, 0)),
		dep(1000, "topup", time.Time{}),
	}
	s := Compute(transs, subject, now)
	assertReconciles(t, s)
	if s.CreditsRemaining != 2000 {
		t.Errorf("CreditsRemaining = %d, want 2000 (allotment is credit)", s.CreditsRemaining)
	}
	if s.PrepaidBalance != 1000 {
		t.Errorf("PrepaidBalance = %d, want 1000", s.PrepaidBalance)
	}
}

func TestSplit_Empty(t *testing.T) {
	s := Compute(nil, subject, time.Now())
	assertReconciles(t, s)
	if s.Balance != 0 || s.CreditsGranted != 0 {
		t.Errorf("empty ledger not zero: %+v", s)
	}
}

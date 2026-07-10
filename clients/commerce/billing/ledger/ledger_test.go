package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func setup(t *testing.T) (context.Context, *MemLedger) {
	t.Helper()
	return context.Background(), NewMemLedger()
}

// createTestAccounts creates the standard platform accounts for a tenant.
func createTestAccounts(t *testing.T, ctx context.Context, l *MemLedger, tenantID string) (cash, fees, custBal *Account) {
	t.Helper()
	var err error

	cash, err = l.EnsureAccount(ctx, tenantID, "platform:cash", Asset, "usd")
	if err != nil {
		t.Fatalf("create cash account: %v", err)
	}
	fees, err = l.EnsureAccount(ctx, tenantID, "platform:fees", Revenue, "usd")
	if err != nil {
		t.Fatalf("create fees account: %v", err)
	}
	custBal, err = l.EnsureAccount(ctx, tenantID, "customer_balance:cust_1", Liability, "usd")
	if err != nil {
		t.Fatalf("create customer balance account: %v", err)
	}
	return
}

// ---------------------------------------------------------------------------
// Double-Entry Validation
// ---------------------------------------------------------------------------

func TestPostEntry_BalancedPostings(t *testing.T) {
	ctx, l := setup(t)
	cash, _, custBal := createTestAccounts(t, ctx, l, "t1")

	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "test-balanced-1",
		Description:    "balanced entry",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 1000, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -1000, Currency: "usd"},
		},
	}

	if err := l.PostEntry(ctx, entry); err != nil {
		t.Fatalf("PostEntry failed for balanced entry: %v", err)
	}

	if entry.ID == "" {
		t.Fatal("entry ID should be assigned")
	}
	if len(entry.Postings) != 2 {
		t.Fatalf("expected 2 postings, got %d", len(entry.Postings))
	}
	for i, p := range entry.Postings {
		if p.ID == "" {
			t.Fatalf("posting %d ID should be assigned", i)
		}
	}
}

func TestPostEntry_UnbalancedPostings(t *testing.T) {
	ctx, l := setup(t)
	cash, _, custBal := createTestAccounts(t, ctx, l, "t1")

	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "test-unbalanced-1",
		Description:    "unbalanced entry",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 1000, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -500, Currency: "usd"},
		},
	}

	err := l.PostEntry(ctx, entry)
	if err == nil {
		t.Fatal("expected error for unbalanced postings")
	}
	if !errors.Is(err, ErrPostingsNotBalanced) {
		t.Fatalf("expected ErrPostingsNotBalanced, got: %v", err)
	}
}

func TestPostEntry_EmptyPostings(t *testing.T) {
	ctx, l := setup(t)

	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "test-empty-1",
		Description:    "empty postings",
		Postings:       []Posting{},
	}

	err := l.PostEntry(ctx, entry)
	if !errors.Is(err, ErrEmptyPostings) {
		t.Fatalf("expected ErrEmptyPostings, got: %v", err)
	}
}

func TestPostEntry_SinglePosting(t *testing.T) {
	ctx, l := setup(t)
	cash, _, _ := createTestAccounts(t, ctx, l, "t1")

	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "test-single-1",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 0, Currency: "usd"},
		},
	}

	err := l.PostEntry(ctx, entry)
	if !errors.Is(err, ErrEmptyPostings) {
		t.Fatalf("expected ErrEmptyPostings for single posting, got: %v", err)
	}
}

func TestPostEntry_AccountNotFound(t *testing.T) {
	ctx, l := setup(t)
	cash, _, _ := createTestAccounts(t, ctx, l, "t1")

	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "test-notfound-1",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 100, Currency: "usd"},
			{AccountID: "nonexistent", Amount: -100, Currency: "usd"},
		},
	}

	err := l.PostEntry(ctx, entry)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestPostEntry_IdempotencyDuplicate(t *testing.T) {
	ctx, l := setup(t)
	cash, _, custBal := createTestAccounts(t, ctx, l, "t1")

	entry1 := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "idem-1",
		Description:    "first",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 500, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -500, Currency: "usd"},
		},
	}
	if err := l.PostEntry(ctx, entry1); err != nil {
		t.Fatalf("first post: %v", err)
	}
	firstID := entry1.ID

	entry2 := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "idem-1",
		Description:    "duplicate",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 500, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -500, Currency: "usd"},
		},
	}
	err := l.PostEntry(ctx, entry2)
	if !errors.Is(err, ErrDuplicateEntry) {
		t.Fatalf("expected ErrDuplicateEntry, got: %v", err)
	}
	// Should return the original entry
	if entry2.ID != firstID {
		t.Fatalf("duplicate should return original ID %s, got %s", firstID, entry2.ID)
	}
}

func TestPostEntry_IdempotencyDifferentTenants(t *testing.T) {
	ctx, l := setup(t)
	cash1, _, cust1 := createTestAccounts(t, ctx, l, "t1")

	cash2, err := l.EnsureAccount(ctx, "t2", "platform:cash", Asset, "usd")
	if err != nil {
		t.Fatal(err)
	}
	cust2, err := l.EnsureAccount(ctx, "t2", "customer_balance:cust_1", Liability, "usd")
	if err != nil {
		t.Fatal(err)
	}

	e1 := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "same-key",
		Postings: []Posting{
			{AccountID: cash1.ID, Amount: 100, Currency: "usd"},
			{AccountID: cust1.ID, Amount: -100, Currency: "usd"},
		},
	}
	if err := l.PostEntry(ctx, e1); err != nil {
		t.Fatalf("t1 post: %v", err)
	}

	e2 := &Entry{
		TenantID:       "t2",
		IdempotencyKey: "same-key",
		Postings: []Posting{
			{AccountID: cash2.ID, Amount: 200, Currency: "usd"},
			{AccountID: cust2.ID, Amount: -200, Currency: "usd"},
		},
	}
	if err := l.PostEntry(ctx, e2); err != nil {
		t.Fatalf("t2 post should succeed with same idemp key but different tenant: %v", err)
	}

	if e1.ID == e2.ID {
		t.Fatal("entries from different tenants should have different IDs")
	}
}

// ---------------------------------------------------------------------------
// Balance Calculation
// ---------------------------------------------------------------------------

func TestBalance_AfterPostings(t *testing.T) {
	ctx, l := setup(t)
	cash, _, custBal := createTestAccounts(t, ctx, l, "t1")

	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "bal-1",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 5000, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -5000, Currency: "usd"},
		},
	}
	if err := l.PostEntry(ctx, entry); err != nil {
		t.Fatal(err)
	}

	cashBal, err := l.GetBalance(ctx, cash.ID, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if cashBal.PostedBalance != 5000 {
		t.Fatalf("cash posted balance: expected 5000, got %d", cashBal.PostedBalance)
	}
	if cashBal.AvailableBalance != 5000 {
		t.Fatalf("cash available balance: expected 5000, got %d", cashBal.AvailableBalance)
	}

	custBalance, err := l.GetBalance(ctx, custBal.ID, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if custBalance.PostedBalance != -5000 {
		t.Fatalf("customer posted balance: expected -5000, got %d", custBalance.PostedBalance)
	}
}

func TestBalance_MultipleEntries(t *testing.T) {
	ctx, l := setup(t)
	cash, _, custBal := createTestAccounts(t, ctx, l, "t1")

	for i := 0; i < 10; i++ {
		e := &Entry{
			TenantID:       "t1",
			IdempotencyKey: "multi-" + string(rune('a'+i)),
			Postings: []Posting{
				{AccountID: cash.ID, Amount: 100, Currency: "usd"},
				{AccountID: custBal.ID, Amount: -100, Currency: "usd"},
			},
		}
		if err := l.PostEntry(ctx, e); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}

	bal, err := l.GetBalance(ctx, cash.ID, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if bal.PostedBalance != 1000 {
		t.Fatalf("expected posted balance 1000 after 10 entries, got %d", bal.PostedBalance)
	}
}

func TestBalance_ZeroForNewAccount(t *testing.T) {
	ctx, l := setup(t)
	acct, _ := l.EnsureAccount(ctx, "t1", "empty-acct", Asset, "usd")

	bal, err := l.GetBalance(ctx, acct.ID, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if bal.PostedBalance != 0 || bal.AvailableBalance != 0 || bal.HeldBalance != 0 {
		t.Fatalf("new account should have zero balances, got posted=%d available=%d held=%d",
			bal.PostedBalance, bal.AvailableBalance, bal.HeldBalance)
	}
}

func TestBalance_AccountNotFound(t *testing.T) {
	ctx, l := setup(t)

	_, err := l.GetBalance(ctx, "nonexistent", "usd")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Hold Lifecycle
// ---------------------------------------------------------------------------

func TestHold_CreateAndCapture(t *testing.T) {
	ctx, l := setup(t)
	cash, _, custBal := createTestAccounts(t, ctx, l, "t1")

	// Seed customer balance with funds via a posting
	seed := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "seed-hold",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 10000, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -10000, Currency: "usd"},
		},
	}
	if err := l.PostEntry(ctx, seed); err != nil {
		t.Fatal(err)
	}

	// Create a hold on cash
	hold := &Hold{
		TenantID:        "t1",
		AccountID:       cash.ID,
		Amount:          3000,
		Currency:        "usd",
		PaymentIntentID: "pi_123",
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}
	if err := l.CreateHold(ctx, hold); err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	if hold.ID == "" {
		t.Fatal("hold ID should be assigned")
	}
	if hold.Status != HoldPending {
		t.Fatalf("hold status: expected pending, got %s", hold.Status)
	}

	// Verify held balance
	bal, err := l.GetBalance(ctx, cash.ID, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if bal.HeldBalance != 3000 {
		t.Fatalf("held balance: expected 3000, got %d", bal.HeldBalance)
	}
	if bal.AvailableBalance != 7000 {
		t.Fatalf("available balance: expected 7000, got %d", bal.AvailableBalance)
	}

	// Capture the hold
	captureEntry, err := l.CaptureHold(ctx, hold.ID, 3000)
	if err != nil {
		t.Fatalf("CaptureHold: %v", err)
	}
	if captureEntry == nil {
		t.Fatal("capture should return an entry")
	}

	// Verify hold is captured
	if hold.Status != HoldCaptured {
		t.Fatalf("hold status after capture: expected captured, got %s", hold.Status)
	}

	// Held balance should be released
	bal, err = l.GetBalance(ctx, cash.ID, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if bal.HeldBalance != 0 {
		t.Fatalf("held balance after capture: expected 0, got %d", bal.HeldBalance)
	}
}

func TestHold_Void(t *testing.T) {
	ctx, l := setup(t)
	cash, _, _ := createTestAccounts(t, ctx, l, "t1")

	hold := &Hold{
		TenantID:  "t1",
		AccountID: cash.ID,
		Amount:    2000,
		Currency:  "usd",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := l.CreateHold(ctx, hold); err != nil {
		t.Fatal(err)
	}

	bal, _ := l.GetBalance(ctx, cash.ID, "usd")
	if bal.HeldBalance != 2000 {
		t.Fatalf("held: expected 2000, got %d", bal.HeldBalance)
	}

	if err := l.VoidHold(ctx, hold.ID); err != nil {
		t.Fatalf("VoidHold: %v", err)
	}

	if hold.Status != HoldVoided {
		t.Fatalf("hold status after void: expected voided, got %s", hold.Status)
	}

	bal, _ = l.GetBalance(ctx, cash.ID, "usd")
	if bal.HeldBalance != 0 {
		t.Fatalf("held after void: expected 0, got %d", bal.HeldBalance)
	}
	if bal.AvailableBalance != 0 {
		t.Fatalf("available after void: expected 0, got %d", bal.AvailableBalance)
	}
}

func TestHold_CaptureExceedsAmount(t *testing.T) {
	ctx, l := setup(t)
	cash, _, _ := createTestAccounts(t, ctx, l, "t1")

	hold := &Hold{
		TenantID:  "t1",
		AccountID: cash.ID,
		Amount:    1000,
		Currency:  "usd",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := l.CreateHold(ctx, hold); err != nil {
		t.Fatal(err)
	}

	_, err := l.CaptureHold(ctx, hold.ID, 2000)
	if !errors.Is(err, ErrCaptureExceedsHold) {
		t.Fatalf("expected ErrCaptureExceedsHold, got: %v", err)
	}
}

func TestHold_VoidAlreadyCaptured(t *testing.T) {
	ctx, l := setup(t)
	cash, _, _ := createTestAccounts(t, ctx, l, "t1")

	hold := &Hold{
		TenantID:  "t1",
		AccountID: cash.ID,
		Amount:    500,
		Currency:  "usd",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := l.CreateHold(ctx, hold); err != nil {
		t.Fatal(err)
	}

	if _, err := l.CaptureHold(ctx, hold.ID, 500); err != nil {
		t.Fatal(err)
	}

	err := l.VoidHold(ctx, hold.ID)
	if !errors.Is(err, ErrHoldNotPending) {
		t.Fatalf("expected ErrHoldNotPending, got: %v", err)
	}
}

func TestHold_InvalidAmount(t *testing.T) {
	ctx, l := setup(t)
	cash, _, _ := createTestAccounts(t, ctx, l, "t1")

	hold := &Hold{
		TenantID:  "t1",
		AccountID: cash.ID,
		Amount:    0,
		Currency:  "usd",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	err := l.CreateHold(ctx, hold)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount for zero hold, got: %v", err)
	}
}

func TestHold_AccountNotFound(t *testing.T) {
	ctx, l := setup(t)

	hold := &Hold{
		TenantID:  "t1",
		AccountID: "nonexistent",
		Amount:    100,
		Currency:  "usd",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	err := l.CreateHold(ctx, hold)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RecordPayment with Fee Split
// ---------------------------------------------------------------------------

func TestRecordPayment_WithFees(t *testing.T) {
	ctx, l := setup(t)

	entry, err := l.RecordPayment(ctx, "t1", "pi_pay1", 10000, "usd", "cust_1", 250)
	if err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	if entry.PaymentIntentID != "pi_pay1" {
		t.Fatalf("expected paymentIntentID pi_pay1, got %s", entry.PaymentIntentID)
	}
	if len(entry.Postings) != 3 {
		t.Fatalf("expected 3 postings (cash, customer, fees), got %d", len(entry.Postings))
	}

	// Verify postings sum to zero
	var sum int64
	for _, p := range entry.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		t.Fatalf("postings sum: expected 0, got %d", sum)
	}

	// Verify cash debited 10000
	cashAcct, _ := l.findOrFailAccount("t1", "platform:cash")
	cashBal, _ := l.GetBalance(ctx, cashAcct.ID, "usd")
	if cashBal.PostedBalance != 10000 {
		t.Fatalf("cash balance: expected 10000, got %d", cashBal.PostedBalance)
	}

	// Verify customer credited 9750 (10000 - 250 fees)
	custAcct, _ := l.findOrFailAccount("t1", "customer_balance:cust_1")
	custBal, _ := l.GetBalance(ctx, custAcct.ID, "usd")
	if custBal.PostedBalance != -9750 {
		t.Fatalf("customer balance: expected -9750, got %d", custBal.PostedBalance)
	}

	// Verify fees credited 250
	feeAcct, _ := l.findOrFailAccount("t1", "platform:fees")
	feeBal, _ := l.GetBalance(ctx, feeAcct.ID, "usd")
	if feeBal.PostedBalance != -250 {
		t.Fatalf("fee balance: expected -250, got %d", feeBal.PostedBalance)
	}
}

func TestRecordPayment_NoFees(t *testing.T) {
	ctx, l := setup(t)

	entry, err := l.RecordPayment(ctx, "t1", "pi_nofee", 5000, "usd", "cust_2", 0)
	if err != nil {
		t.Fatalf("RecordPayment: %v", err)
	}

	if len(entry.Postings) != 2 {
		t.Fatalf("expected 2 postings without fees, got %d", len(entry.Postings))
	}

	var sum int64
	for _, p := range entry.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		t.Fatalf("postings sum: expected 0, got %d", sum)
	}
}

func TestRecordPayment_InvalidAmount(t *testing.T) {
	ctx, l := setup(t)

	_, err := l.RecordPayment(ctx, "t1", "pi_zero", 0, "usd", "cust_1", 0)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount, got: %v", err)
	}

	_, err = l.RecordPayment(ctx, "t1", "pi_neg", -100, "usd", "cust_1", 0)
	if !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount for negative, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RecordRefund
// ---------------------------------------------------------------------------

func TestRecordRefund(t *testing.T) {
	ctx, l := setup(t)

	// First record a payment
	_, err := l.RecordPayment(ctx, "t1", "pi_ref1", 8000, "usd", "cust_1", 200)
	if err != nil {
		t.Fatal(err)
	}

	// Then record a refund
	refEntry, err := l.RecordRefund(ctx, "t1", "ref_1", 8000, "usd", "cust_1")
	if err != nil {
		t.Fatalf("RecordRefund: %v", err)
	}

	if refEntry.RefundID != "ref_1" {
		t.Fatalf("expected refundID ref_1, got %s", refEntry.RefundID)
	}

	var sum int64
	for _, p := range refEntry.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		t.Fatalf("refund postings sum: expected 0, got %d", sum)
	}

	// Cash should be 8000 (payment) - 8000 (refund) = 0
	cashAcct, _ := l.findOrFailAccount("t1", "platform:cash")
	cashBal, _ := l.GetBalance(ctx, cashAcct.ID, "usd")
	if cashBal.PostedBalance != 0 {
		t.Fatalf("cash after refund: expected 0, got %d", cashBal.PostedBalance)
	}

	// Customer balance should be -7800 (payment credit) + 8000 (refund debit) = 200
	custAcct, _ := l.findOrFailAccount("t1", "customer_balance:cust_1")
	custBal, _ := l.GetBalance(ctx, custAcct.ID, "usd")
	if custBal.PostedBalance != 200 {
		t.Fatalf("customer after refund: expected 200, got %d", custBal.PostedBalance)
	}
}

// ---------------------------------------------------------------------------
// RecordPayout
// ---------------------------------------------------------------------------

func TestRecordPayout(t *testing.T) {
	ctx, l := setup(t)

	// Seed cash
	_, err := l.RecordPayment(ctx, "t1", "pi_payout_seed", 20000, "usd", "cust_1", 0)
	if err != nil {
		t.Fatal(err)
	}

	entry, err := l.RecordPayout(ctx, "t1", "po_1", 15000, "usd", "merch_1")
	if err != nil {
		t.Fatalf("RecordPayout: %v", err)
	}

	if entry.PayoutID != "po_1" {
		t.Fatalf("expected payoutID po_1, got %s", entry.PayoutID)
	}

	var sum int64
	for _, p := range entry.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		t.Fatalf("payout postings sum: expected 0, got %d", sum)
	}

	cashAcct, _ := l.findOrFailAccount("t1", "platform:cash")
	cashBal, _ := l.GetBalance(ctx, cashAcct.ID, "usd")
	// 20000 (payment) - 15000 (payout) = 5000
	if cashBal.PostedBalance != 5000 {
		t.Fatalf("cash after payout: expected 5000, got %d", cashBal.PostedBalance)
	}
}

// ---------------------------------------------------------------------------
// RecordDispute
// ---------------------------------------------------------------------------

func TestRecordDispute(t *testing.T) {
	ctx, l := setup(t)

	// Seed cash
	_, err := l.RecordPayment(ctx, "t1", "pi_disp_seed", 10000, "usd", "cust_1", 0)
	if err != nil {
		t.Fatal(err)
	}

	entry, err := l.RecordDispute(ctx, "t1", "disp_1", 5000, "usd", "cust_1")
	if err != nil {
		t.Fatalf("RecordDispute: %v", err)
	}

	if entry.DisputeID != "disp_1" {
		t.Fatalf("expected disputeID disp_1, got %s", entry.DisputeID)
	}

	var sum int64
	for _, p := range entry.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		t.Fatalf("dispute postings sum: expected 0, got %d", sum)
	}

	// Cash: 10000 - 5000 = 5000
	cashAcct, _ := l.findOrFailAccount("t1", "platform:cash")
	cashBal, _ := l.GetBalance(ctx, cashAcct.ID, "usd")
	if cashBal.PostedBalance != 5000 {
		t.Fatalf("cash after dispute: expected 5000, got %d", cashBal.PostedBalance)
	}

	// Disputes held: 5000
	dispAcct, _ := l.findOrFailAccount("t1", "platform:disputes_held")
	dispBal, _ := l.GetBalance(ctx, dispAcct.ID, "usd")
	if dispBal.PostedBalance != 5000 {
		t.Fatalf("disputes held: expected 5000, got %d", dispBal.PostedBalance)
	}
}

// ---------------------------------------------------------------------------
// ListEntries
// ---------------------------------------------------------------------------

func TestListEntries_ByTenant(t *testing.T) {
	ctx, l := setup(t)

	_, _ = l.RecordPayment(ctx, "t1", "pi_list1", 1000, "usd", "c1", 0)
	_, _ = l.RecordPayment(ctx, "t2", "pi_list2", 2000, "usd", "c2", 0)
	_, _ = l.RecordPayment(ctx, "t1", "pi_list3", 3000, "usd", "c1", 0)

	entries, err := l.ListEntries(ctx, EntryFilter{TenantID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for t1, got %d", len(entries))
	}
}

func TestListEntries_ByPaymentIntent(t *testing.T) {
	ctx, l := setup(t)

	_, _ = l.RecordPayment(ctx, "t1", "pi_target", 1000, "usd", "c1", 0)
	_, _ = l.RecordPayment(ctx, "t1", "pi_other", 2000, "usd", "c1", 0)

	entries, err := l.ListEntries(ctx, EntryFilter{PaymentIntentID: "pi_target"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for pi_target, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// GetEntry
// ---------------------------------------------------------------------------

func TestGetEntry(t *testing.T) {
	ctx, l := setup(t)

	original, _ := l.RecordPayment(ctx, "t1", "pi_get1", 1000, "usd", "c1", 0)

	fetched, err := l.GetEntry(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if fetched.ID != original.ID {
		t.Fatalf("expected ID %s, got %s", original.ID, fetched.ID)
	}
}

func TestGetEntry_NotFound(t *testing.T) {
	ctx, l := setup(t)

	_, err := l.GetEntry(ctx, "nonexistent")
	if !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("expected ErrEntryNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Account operations
// ---------------------------------------------------------------------------

func TestCreateAccount_Duplicate(t *testing.T) {
	ctx, l := setup(t)

	a1 := &Account{TenantID: "t1", Name: "test-acct", Type: Asset, Currency: "usd"}
	if err := l.CreateAccount(ctx, a1); err != nil {
		t.Fatal(err)
	}

	a2 := &Account{TenantID: "t1", Name: "test-acct", Type: Asset, Currency: "usd"}
	err := l.CreateAccount(ctx, a2)
	if err == nil {
		t.Fatal("expected error for duplicate account name in same tenant")
	}
}

func TestCreateAccount_NormalBalanceDefaults(t *testing.T) {
	ctx, l := setup(t)

	cases := []struct {
		typ    AccountType
		expect string
	}{
		{Asset, "debit"},
		{Expense, "debit"},
		{Liability, "credit"},
		{Equity, "credit"},
		{Revenue, "credit"},
	}

	for _, c := range cases {
		a := &Account{TenantID: "t1", Name: "nb-" + string(c.typ), Type: c.typ, Currency: "usd"}
		if err := l.CreateAccount(ctx, a); err != nil {
			t.Fatalf("create %s account: %v", c.typ, err)
		}
		if a.NormalBalance != c.expect {
			t.Fatalf("%s account: expected normal_balance %s, got %s", c.typ, c.expect, a.NormalBalance)
		}
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	ctx, l := setup(t)

	_, err := l.GetAccount(ctx, "missing")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Three-posting entry (fee split correctness)
// ---------------------------------------------------------------------------

func TestThreePostingEntry(t *testing.T) {
	ctx, l := setup(t)
	cash, fees, custBal := createTestAccounts(t, ctx, l, "t1")

	// Payment of 10000 with 300 in fees
	entry := &Entry{
		TenantID:       "t1",
		IdempotencyKey: "three-post-1",
		Description:    "payment with fee split",
		Postings: []Posting{
			{AccountID: cash.ID, Amount: 10000, Currency: "usd"},
			{AccountID: custBal.ID, Amount: -9700, Currency: "usd"},
			{AccountID: fees.ID, Amount: -300, Currency: "usd"},
		},
	}

	if err := l.PostEntry(ctx, entry); err != nil {
		t.Fatalf("PostEntry: %v", err)
	}

	cashBal, _ := l.GetBalance(ctx, cash.ID, "usd")
	custBalance, _ := l.GetBalance(ctx, custBal.ID, "usd")
	feeBal, _ := l.GetBalance(ctx, fees.ID, "usd")

	if cashBal.PostedBalance != 10000 {
		t.Fatalf("cash: expected 10000, got %d", cashBal.PostedBalance)
	}
	if custBalance.PostedBalance != -9700 {
		t.Fatalf("customer: expected -9700, got %d", custBalance.PostedBalance)
	}
	if feeBal.PostedBalance != -300 {
		t.Fatalf("fees: expected -300, got %d", feeBal.PostedBalance)
	}

	// Total across all accounts must be zero
	total := cashBal.PostedBalance + custBalance.PostedBalance + feeBal.PostedBalance
	if total != 0 {
		t.Fatalf("total across all accounts: expected 0, got %d", total)
	}
}

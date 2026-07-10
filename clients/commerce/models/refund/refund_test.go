package refund

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- MarkSucceeded ---

func TestMarkSucceeded_FromPending(t *testing.T) {
	r := &Refund{Status: Pending}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, r.Status)
	}
}

func TestMarkSucceeded_FromFailed(t *testing.T) {
	r := &Refund{Status: Failed}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, r.Status)
	}
}

func TestMarkSucceeded_Idempotent(t *testing.T) {
	r := &Refund{Status: Succeeded}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, r.Status)
	}
}

// --- MarkFailed ---

func TestMarkFailed_FromPending(t *testing.T) {
	r := &Refund{Status: Pending}
	if err := r.MarkFailed("insufficient_funds"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, r.Status)
	}
	if r.FailureReason != "insufficient_funds" {
		t.Errorf("expected failure reason 'insufficient_funds', got %q", r.FailureReason)
	}
}

func TestMarkFailed_SetsReason(t *testing.T) {
	r := &Refund{Status: Pending}
	if err := r.MarkFailed("declined"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.FailureReason != "declined" {
		t.Errorf("expected 'declined', got %q", r.FailureReason)
	}
}

func TestMarkFailed_OverwritesReason(t *testing.T) {
	r := &Refund{
		Status:        Pending,
		FailureReason: "old_reason",
	}
	if err := r.MarkFailed("new_reason"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.FailureReason != "new_reason" {
		t.Errorf("expected 'new_reason', got %q", r.FailureReason)
	}
}

func TestMarkFailed_EmptyReason(t *testing.T) {
	r := &Refund{Status: Pending}
	if err := r.MarkFailed(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, r.Status)
	}
	if r.FailureReason != "" {
		t.Errorf("expected empty failure reason, got %q", r.FailureReason)
	}
}

// --- Status constants ---

func TestStatusConstants(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{Pending, "pending"},
		{Succeeded, "succeeded"},
		{Failed, "failed"},
		{Canceled, "canceled"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Errorf("status %q != %q", tc.status, tc.want)
		}
	}
}

// --- Struct fields ---

func TestRefundFieldDefaults(t *testing.T) {
	r := &Refund{}
	if r.Amount != 0 {
		t.Errorf("expected zero amount, got %d", r.Amount)
	}
	if r.Status != "" {
		t.Errorf("expected empty status, got %s", r.Status)
	}
	if r.FailureReason != "" {
		t.Errorf("expected empty failure reason, got %q", r.FailureReason)
	}
}

func TestRefundFieldAssignment(t *testing.T) {
	r := &Refund{
		Amount:          5000,
		Status:          Pending,
		Reason:          "duplicate",
		PaymentIntentId: "pi_abc",
		InvoiceId:       "inv_123",
	}
	if r.Amount != 5000 {
		t.Errorf("expected 5000, got %d", r.Amount)
	}
	if r.Reason != "duplicate" {
		t.Errorf("expected 'duplicate', got %q", r.Reason)
	}
	if r.PaymentIntentId != "pi_abc" {
		t.Errorf("expected 'pi_abc', got %q", r.PaymentIntentId)
	}
}

// --- MarkSucceeded from every status ---

func TestMarkSucceeded_FromCanceled(t *testing.T) {
	r := &Refund{Status: Canceled}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, r.Status)
	}
}

func TestMarkSucceeded_FromEmpty(t *testing.T) {
	r := &Refund{Status: ""}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, r.Status)
	}
}

// --- MarkFailed from every status ---

func TestMarkFailed_FromSucceeded(t *testing.T) {
	r := &Refund{Status: Succeeded}
	if err := r.MarkFailed("late_failure"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, r.Status)
	}
	if r.FailureReason != "late_failure" {
		t.Errorf("expected 'late_failure', got %q", r.FailureReason)
	}
}

func TestMarkFailed_FromFailed(t *testing.T) {
	r := &Refund{Status: Failed, FailureReason: "first"}
	if err := r.MarkFailed("second"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.FailureReason != "second" {
		t.Errorf("expected 'second', got %q", r.FailureReason)
	}
}

func TestMarkFailed_FromCanceled(t *testing.T) {
	r := &Refund{Status: Canceled}
	if err := r.MarkFailed("canceled_failure"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, r.Status)
	}
}

func TestMarkFailed_FromEmpty(t *testing.T) {
	r := &Refund{Status: ""}
	if err := r.MarkFailed("no_status"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, r.Status)
	}
}

// --- MarkSucceeded preserves other fields ---

func TestMarkSucceeded_PreservesFields(t *testing.T) {
	r := &Refund{
		Amount:          3000,
		Currency:        "eur",
		Status:          Pending,
		ProviderRef:     "re_abc",
		Reason:          "requested_by_customer",
		ReceiptNumber:   "1234-5678",
		PaymentIntentId: "pi_xyz",
		InvoiceId:       "inv_456",
	}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Amount != 3000 {
		t.Errorf("expected 3000, got %d", r.Amount)
	}
	if string(r.Currency) != "eur" {
		t.Errorf("expected eur, got %s", r.Currency)
	}
	if r.ProviderRef != "re_abc" {
		t.Errorf("expected re_abc, got %s", r.ProviderRef)
	}
	if r.Reason != "requested_by_customer" {
		t.Errorf("expected requested_by_customer, got %s", r.Reason)
	}
	if r.ReceiptNumber != "1234-5678" {
		t.Errorf("expected 1234-5678, got %s", r.ReceiptNumber)
	}
}

// --- MarkFailed preserves other fields ---

func TestMarkFailed_PreservesFields(t *testing.T) {
	r := &Refund{
		Amount:          5000,
		Status:          Pending,
		PaymentIntentId: "pi_keep",
		Metadata:        map[string]interface{}{"key": "val"},
	}
	if err := r.MarkFailed("bank_error"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Amount != 5000 {
		t.Errorf("expected 5000, got %d", r.Amount)
	}
	if r.PaymentIntentId != "pi_keep" {
		t.Errorf("expected pi_keep, got %s", r.PaymentIntentId)
	}
	if r.Metadata["key"] != "val" {
		t.Errorf("expected metadata preserved, got %v", r.Metadata)
	}
}

// --- Full lifecycle ---

func TestFullLifecycle_PendingToSucceeded(t *testing.T) {
	r := &Refund{
		Amount:          2000,
		Status:          Pending,
		PaymentIntentId: "pi_life",
	}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected Succeeded, got %s", r.Status)
	}
}

func TestFullLifecycle_PendingToFailed(t *testing.T) {
	r := &Refund{
		Amount: 4000,
		Status: Pending,
	}
	if err := r.MarkFailed("declined"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected Failed, got %s", r.Status)
	}
}

func TestFullLifecycle_FailedThenSucceeded(t *testing.T) {
	r := &Refund{Status: Pending}
	if err := r.MarkFailed("temporary"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if r.Status != Succeeded {
		t.Errorf("expected Succeeded, got %s", r.Status)
	}
}

func TestFullLifecycle_SucceededThenFailed(t *testing.T) {
	r := &Refund{Status: Pending}
	if err := r.MarkSucceeded(); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if err := r.MarkFailed("reversed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if r.Status != Failed {
		t.Errorf("expected Failed, got %s", r.Status)
	}
}

// --- Metadata ---

func TestRefundMetadata(t *testing.T) {
	r := &Refund{
		Status: Pending,
		Metadata: map[string]interface{}{
			"order_id": "ord_123",
			"reason":   "customer_request",
		},
	}
	if len(r.Metadata) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(r.Metadata))
	}
	if r.Metadata["order_id"] != "ord_123" {
		t.Errorf("expected ord_123, got %v", r.Metadata["order_id"])
	}
}

// --- Kind ---

func TestKind(t *testing.T) {
	r := &Refund{}
	if r.Kind() != "refund" {
		t.Errorf("expected 'refund', got %q", r.Kind())
	}
}

// --- ReceiptNumber ---

func TestRefundReceiptNumber(t *testing.T) {
	r := &Refund{
		Status:        Pending,
		ReceiptNumber: "1234-5678",
	}
	if r.ReceiptNumber != "1234-5678" {
		t.Errorf("expected 1234-5678, got %s", r.ReceiptNumber)
	}
}

// --- ProviderRef ---

func TestRefundProviderRef(t *testing.T) {
	r := &Refund{
		Status:      Pending,
		ProviderRef: "re_stripe_abc",
	}
	if r.ProviderRef != "re_stripe_abc" {
		t.Errorf("expected re_stripe_abc, got %s", r.ProviderRef)
	}
}

// --- InvoiceId ---

func TestRefundInvoiceId(t *testing.T) {
	r := &Refund{
		Status:    Pending,
		InvoiceId: "inv_789",
	}
	if r.InvoiceId != "inv_789" {
		t.Errorf("expected inv_789, got %s", r.InvoiceId)
	}
}

// --- Currency ---

func TestRefundCurrency(t *testing.T) {
	r := &Refund{Currency: "eur"}
	if string(r.Currency) != "eur" {
		t.Errorf("expected eur, got %s", r.Currency)
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	r := &Refund{}
	r.Init(db)
	if r.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- Defaults ---

func TestDefaults(t *testing.T) {
	db := testDB()
	r := &Refund{}
	r.Init(db)
	r.Defaults()
	if r.Status != Pending {
		t.Errorf("expected %s, got %s", Pending, r.Status)
	}
	if r.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

func TestDefaults_DoesNotOverwrite(t *testing.T) {
	db := testDB()
	r := &Refund{}
	r.Init(db)
	r.Status = Succeeded
	r.Defaults()
	if r.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, r.Status)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	r := New(db)
	if r == nil {
		t.Fatal("expected non-nil Refund")
	}
	if r.Status != Pending {
		t.Errorf("expected %s, got %s", Pending, r.Status)
	}
}

// --- Query ---

func TestQuery(t *testing.T) {
	db := testDB()
	q := Query(db)
	if q == nil {
		t.Fatal("expected non-nil query")
	}
}

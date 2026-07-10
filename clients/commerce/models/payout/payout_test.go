package payout

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- MarkInTransit ---

func TestMarkInTransit_FromPending(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkInTransit(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != InTransit {
		t.Errorf("expected %s, got %s", InTransit, p.Status)
	}
}

func TestMarkInTransit_InvalidStatus_InTransit(t *testing.T) {
	p := &Payout{Status: InTransit}
	err := p.MarkInTransit()
	if err == nil {
		t.Fatal("expected error transitioning from InTransit")
	}
}

func TestMarkInTransit_InvalidStatus_Paid(t *testing.T) {
	p := &Payout{Status: Paid}
	err := p.MarkInTransit()
	if err == nil {
		t.Fatal("expected error transitioning from Paid")
	}
}

func TestMarkInTransit_InvalidStatus_Failed(t *testing.T) {
	p := &Payout{Status: Failed}
	err := p.MarkInTransit()
	if err == nil {
		t.Fatal("expected error transitioning from Failed")
	}
}

func TestMarkInTransit_InvalidStatus_Canceled(t *testing.T) {
	p := &Payout{Status: Canceled}
	err := p.MarkInTransit()
	if err == nil {
		t.Fatal("expected error transitioning from Canceled")
	}
}

// --- MarkPaid ---

func TestMarkPaid_FromPending(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Paid {
		t.Errorf("expected %s, got %s", Paid, p.Status)
	}
	if p.ArrivalDate.IsZero() {
		t.Error("expected ArrivalDate to be set")
	}
}

func TestMarkPaid_FromInTransit(t *testing.T) {
	p := &Payout{Status: InTransit}
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Paid {
		t.Errorf("expected %s, got %s", Paid, p.Status)
	}
	if p.ArrivalDate.IsZero() {
		t.Error("expected ArrivalDate to be set")
	}
}

func TestMarkPaid_InvalidStatus_Paid(t *testing.T) {
	p := &Payout{Status: Paid}
	err := p.MarkPaid()
	if err == nil {
		t.Fatal("expected error paying already-paid payout")
	}
}

func TestMarkPaid_InvalidStatus_Failed(t *testing.T) {
	p := &Payout{Status: Failed}
	err := p.MarkPaid()
	if err == nil {
		t.Fatal("expected error paying failed payout")
	}
}

func TestMarkPaid_InvalidStatus_Canceled(t *testing.T) {
	p := &Payout{Status: Canceled}
	err := p.MarkPaid()
	if err == nil {
		t.Fatal("expected error paying canceled payout")
	}
}

// --- MarkFailed ---

func TestMarkFailed_FromPending(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkFailed("account_closed", "Bank account is closed"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, p.Status)
	}
	if p.FailureCode != "account_closed" {
		t.Errorf("expected 'account_closed', got %q", p.FailureCode)
	}
	if p.FailureMessage != "Bank account is closed" {
		t.Errorf("expected 'Bank account is closed', got %q", p.FailureMessage)
	}
}

func TestMarkFailed_FromInTransit(t *testing.T) {
	p := &Payout{Status: InTransit}
	if err := p.MarkFailed("could_not_process", "Processing error"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, p.Status)
	}
}

func TestMarkFailed_OverwritesFailureFields(t *testing.T) {
	p := &Payout{
		Status:         Pending,
		FailureCode:    "old_code",
		FailureMessage: "old message",
	}
	if err := p.MarkFailed("new_code", "new message"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.FailureCode != "new_code" {
		t.Errorf("expected 'new_code', got %q", p.FailureCode)
	}
	if p.FailureMessage != "new message" {
		t.Errorf("expected 'new message', got %q", p.FailureMessage)
	}
}

// --- Cancel ---

func TestCancel_FromPending(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.Cancel(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Canceled {
		t.Errorf("expected %s, got %s", Canceled, p.Status)
	}
}

func TestCancel_InvalidStatus_InTransit(t *testing.T) {
	p := &Payout{Status: InTransit}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling in-transit payout")
	}
}

func TestCancel_InvalidStatus_Paid(t *testing.T) {
	p := &Payout{Status: Paid}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling paid payout")
	}
}

func TestCancel_InvalidStatus_Failed(t *testing.T) {
	p := &Payout{Status: Failed}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling failed payout")
	}
}

func TestCancel_InvalidStatus_Canceled(t *testing.T) {
	p := &Payout{Status: Canceled}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling already-canceled payout")
	}
}

// --- Status constants ---

func TestStatusConstants(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{Pending, "pending"},
		{InTransit, "in_transit"},
		{Paid, "paid"},
		{Failed, "failed"},
		{Canceled, "canceled"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Errorf("status %q != %q", tc.status, tc.want)
		}
	}
}

// --- Full lifecycle ---

func TestFullLifecycle_PendingToInTransitToPaid(t *testing.T) {
	p := &Payout{Status: Pending, Amount: 50000, Currency: "usd"}
	if err := p.MarkInTransit(); err != nil {
		t.Fatalf("MarkInTransit: %v", err)
	}
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if p.Status != Paid {
		t.Errorf("expected Paid, got %s", p.Status)
	}
	if p.ArrivalDate.IsZero() {
		t.Error("expected ArrivalDate to be set")
	}
}

func TestFullLifecycle_PendingDirectToPaid(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if p.Status != Paid {
		t.Errorf("expected Paid, got %s", p.Status)
	}
}

func TestFullLifecycle_PendingToFailed(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkFailed("no_account", "No bank account"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected Failed, got %s", p.Status)
	}
}

func TestFullLifecycle_PendingToCanceled(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if p.Status != Canceled {
		t.Errorf("expected Canceled, got %s", p.Status)
	}
}

func TestFullLifecycle_InTransitCannotCancel(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkInTransit(); err != nil {
		t.Fatalf("MarkInTransit: %v", err)
	}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling in-transit payout")
	}
}

func TestFullLifecycle_PaidCannotTransition(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if err := p.MarkInTransit(); err == nil {
		t.Fatal("expected error on MarkInTransit from Paid")
	}
	if err := p.Cancel(); err == nil {
		t.Fatal("expected error on Cancel from Paid")
	}
	if err := p.MarkPaid(); err == nil {
		t.Fatal("expected error on MarkPaid from Paid")
	}
}

// --- Kind ---

func TestKind(t *testing.T) {
	p := &Payout{}
	if p.Kind() != "billing-payout" {
		t.Errorf("expected 'billing-payout', got %q", p.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	p := &Payout{}
	if p.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- MarkInTransit from empty/unknown status ---

func TestMarkInTransit_InvalidStatus_Empty(t *testing.T) {
	p := &Payout{Status: ""}
	err := p.MarkInTransit()
	if err == nil {
		t.Fatal("expected error transitioning from empty status")
	}
}

func TestMarkInTransit_InvalidStatus_Unknown(t *testing.T) {
	p := &Payout{Status: Status("suspended")}
	err := p.MarkInTransit()
	if err == nil {
		t.Fatal("expected error transitioning from unknown status")
	}
}

// --- MarkPaid from empty/unknown status ---

func TestMarkPaid_InvalidStatus_Empty(t *testing.T) {
	p := &Payout{Status: ""}
	err := p.MarkPaid()
	if err == nil {
		t.Fatal("expected error paying from empty status")
	}
}

func TestMarkPaid_InvalidStatus_Unknown(t *testing.T) {
	p := &Payout{Status: Status("processing")}
	err := p.MarkPaid()
	if err == nil {
		t.Fatal("expected error paying from unknown status")
	}
}

// --- MarkPaid sets ArrivalDate ---

func TestMarkPaid_SetsArrivalDate(t *testing.T) {
	before := time.Now()
	p := &Payout{Status: Pending}
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now()
	if p.ArrivalDate.Before(before) || p.ArrivalDate.After(after) {
		t.Errorf("ArrivalDate %v not between %v and %v", p.ArrivalDate, before, after)
	}
}

// --- MarkFailed from all states ---

func TestMarkFailed_FromPaid(t *testing.T) {
	p := &Payout{Status: Paid}
	if err := p.MarkFailed("returned", "Funds returned"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, p.Status)
	}
}

func TestMarkFailed_FromCanceled(t *testing.T) {
	p := &Payout{Status: Canceled}
	if err := p.MarkFailed("error", "System error"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, p.Status)
	}
}

func TestMarkFailed_FromFailed(t *testing.T) {
	p := &Payout{Status: Failed, FailureCode: "old", FailureMessage: "old msg"}
	if err := p.MarkFailed("new_code", "new msg"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.FailureCode != "new_code" {
		t.Errorf("expected 'new_code', got %q", p.FailureCode)
	}
	if p.FailureMessage != "new msg" {
		t.Errorf("expected 'new msg', got %q", p.FailureMessage)
	}
}

func TestMarkFailed_EmptyCodeAndMessage(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkFailed("", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected %s, got %s", Failed, p.Status)
	}
	if p.FailureCode != "" {
		t.Errorf("expected empty code, got %q", p.FailureCode)
	}
}

// --- Cancel from empty/unknown status ---

func TestCancel_InvalidStatus_Empty(t *testing.T) {
	p := &Payout{Status: ""}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling from empty status")
	}
}

func TestCancel_InvalidStatus_Unknown(t *testing.T) {
	p := &Payout{Status: Status("held")}
	err := p.Cancel()
	if err == nil {
		t.Fatal("expected error canceling from unknown status")
	}
}

// --- MarkFailed preserves other fields ---

func TestMarkFailed_PreservesFields(t *testing.T) {
	p := &Payout{
		Amount:          100000,
		Currency:        "gbp",
		Status:          InTransit,
		DestinationType: "bank_account",
		DestinationId:   "ba_123",
		Description:     "Monthly payout",
		ProviderRef:     "po_xyz",
	}
	if err := p.MarkFailed("insufficient_funds", "Not enough balance"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Amount != 100000 {
		t.Errorf("expected 100000, got %d", p.Amount)
	}
	if string(p.Currency) != "gbp" {
		t.Errorf("expected gbp, got %s", p.Currency)
	}
	if p.DestinationType != "bank_account" {
		t.Errorf("expected bank_account, got %s", p.DestinationType)
	}
	if p.Description != "Monthly payout" {
		t.Errorf("expected 'Monthly payout', got %s", p.Description)
	}
}

// --- Full lifecycle: in_transit -> failed ---

func TestFullLifecycle_InTransitToFailed(t *testing.T) {
	p := &Payout{Status: Pending}
	if err := p.MarkInTransit(); err != nil {
		t.Fatalf("MarkInTransit: %v", err)
	}
	if err := p.MarkFailed("bank_error", "Bank rejected"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if p.Status != Failed {
		t.Errorf("expected Failed, got %s", p.Status)
	}
}

// --- Failed/Canceled cannot go to InTransit or Paid ---

func TestFailedCannotTransition(t *testing.T) {
	p := &Payout{Status: Failed}
	if err := p.MarkInTransit(); err == nil {
		t.Fatal("expected error on MarkInTransit from Failed")
	}
	if err := p.MarkPaid(); err == nil {
		t.Fatal("expected error on MarkPaid from Failed")
	}
	if err := p.Cancel(); err == nil {
		t.Fatal("expected error on Cancel from Failed")
	}
}

func TestCanceledCannotTransition(t *testing.T) {
	p := &Payout{Status: Canceled}
	if err := p.MarkInTransit(); err == nil {
		t.Fatal("expected error on MarkInTransit from Canceled")
	}
	if err := p.MarkPaid(); err == nil {
		t.Fatal("expected error on MarkPaid from Canceled")
	}
	if err := p.Cancel(); err == nil {
		t.Fatal("expected error on Cancel from Canceled")
	}
}

// --- Struct fields ---

func TestPayoutZeroValue(t *testing.T) {
	p := &Payout{}
	if p.Amount != 0 {
		t.Errorf("expected 0, got %d", p.Amount)
	}
	if p.Status != "" {
		t.Errorf("expected empty, got %s", p.Status)
	}
	if p.DestinationType != "" {
		t.Errorf("expected empty, got %s", p.DestinationType)
	}
	if !p.ArrivalDate.IsZero() {
		t.Error("expected zero ArrivalDate")
	}
	if p.Metadata != nil {
		t.Error("expected nil metadata")
	}
}

func TestPayoutFieldAssignment(t *testing.T) {
	p := &Payout{
		Amount:          50000,
		Currency:        "usd",
		Status:          Pending,
		DestinationType: "card",
		DestinationId:   "card_abc",
		Description:     "Weekly payout",
		ProviderRef:     "po_ref",
	}
	if p.Amount != 50000 {
		t.Errorf("expected 50000, got %d", p.Amount)
	}
	if p.DestinationType != "card" {
		t.Errorf("expected card, got %s", p.DestinationType)
	}
}

// --- Save serializes Metadata_ ---

func TestSave_SerializesMetadata(t *testing.T) {
	p := &Payout{
		Metadata: map[string]interface{}{"batch": "weekly"},
	}
	ps, err := p.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if p.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after Save")
	}
}

func TestSave_NilMetadata(t *testing.T) {
	p := &Payout{}
	_, err := p.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if p.Metadata_ == "" {
		t.Error("expected Metadata_ to be set")
	}
}

// --- Load deserializes Metadata_ ---

func TestLoad_DeserializesMetadata(t *testing.T) {
	p := &Payout{
		Metadata: map[string]interface{}{"period": "2026-02"},
	}
	_, err := p.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	saved := p.Metadata_

	p2 := &Payout{}
	p2.Metadata_ = saved
	props := []datastore.Property{
		{Name: "Metadata_", Value: saved},
	}
	err = p2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if p2.Metadata == nil {
		t.Fatal("expected non-nil Metadata after Load")
	}
	if p2.Metadata["period"] != "2026-02" {
		t.Errorf("expected period=2026-02, got %v", p2.Metadata["period"])
	}
}

func TestLoad_EmptyMetadataString(t *testing.T) {
	p := &Payout{}
	err := p.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if p.Metadata != nil {
		t.Error("expected nil metadata when Metadata_ is empty")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	p := &Payout{
		Amount:          50000,
		Currency:        "usd",
		Status:          Pending,
		DestinationType: "bank_account",
		DestinationId:   "ba_123",
		Description:     "Monthly payout",
		ProviderRef:     "po_abc",
		Metadata:        map[string]interface{}{"batch_id": "batch_42"},
	}

	ps, err := p.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	p2 := &Payout{}
	err = p2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if p2.DestinationType != "bank_account" {
		t.Errorf("expected bank_account, got %s", p2.DestinationType)
	}
	if p2.Description != "Monthly payout" {
		t.Errorf("expected 'Monthly payout', got %s", p2.Description)
	}
}

// --- Load error paths ---

func TestLoad_LoadStructError(t *testing.T) {
	p := &Payout{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := p.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestLoad_InvalidMetadataJSON(t *testing.T) {
	p := &Payout{}
	p.Metadata_ = "not-valid-json"
	err := p.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Metadata_ JSON")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	p := &Payout{}
	p.Init(db)
	if p.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- Defaults ---

func TestDefaults(t *testing.T) {
	db := testDB()
	p := &Payout{}
	p.Init(db)
	p.Defaults()
	if p.Status != Pending {
		t.Errorf("expected %s, got %s", Pending, p.Status)
	}
	if p.Currency != "usd" {
		t.Errorf("expected usd, got %s", p.Currency)
	}
	if p.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

func TestDefaults_DoesNotOverwrite(t *testing.T) {
	db := testDB()
	p := &Payout{}
	p.Init(db)
	p.Status = Paid
	p.Currency = "gbp"
	p.Defaults()
	if p.Status != Paid {
		t.Errorf("expected %s, got %s", Paid, p.Status)
	}
	if p.Currency != "gbp" {
		t.Errorf("expected gbp, got %s", p.Currency)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	p := New(db)
	if p == nil {
		t.Fatal("expected non-nil Payout")
	}
	if p.Status != Pending {
		t.Errorf("expected %s, got %s", Pending, p.Status)
	}
	if p.Currency != "usd" {
		t.Errorf("expected usd, got %s", p.Currency)
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

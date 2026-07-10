package balancetransaction

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- Kind ---

func TestKind(t *testing.T) {
	bt := &BalanceTransaction{}
	if bt.Kind() != "balance-transaction" {
		t.Errorf("expected 'balance-transaction', got %q", bt.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	bt := &BalanceTransaction{}
	if bt.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Save serializes Metadata_ ---

func TestSave_SerializesMetadata(t *testing.T) {
	bt := &BalanceTransaction{
		Metadata: map[string]interface{}{"source": "api"},
	}
	ps, err := bt.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if bt.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after Save")
	}
}

func TestSave_NilMetadata(t *testing.T) {
	bt := &BalanceTransaction{}
	_, err := bt.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if bt.Metadata_ == "" {
		t.Error("expected Metadata_ to be set")
	}
}

// --- Load deserializes Metadata_ ---

func TestLoad_DeserializesMetadata(t *testing.T) {
	bt := &BalanceTransaction{
		Metadata: map[string]interface{}{"ref": "order_123"},
	}
	_, err := bt.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	saved := bt.Metadata_

	bt2 := &BalanceTransaction{}
	bt2.Metadata_ = saved
	props := []datastore.Property{
		{Name: "Metadata_", Value: saved},
	}
	err = bt2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if bt2.Metadata == nil {
		t.Fatal("expected non-nil Metadata after Load")
	}
	if bt2.Metadata["ref"] != "order_123" {
		t.Errorf("expected ref=order_123, got %v", bt2.Metadata["ref"])
	}
}

func TestLoad_EmptyMetadataString(t *testing.T) {
	bt := &BalanceTransaction{}
	err := bt.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if bt.Metadata != nil {
		t.Error("expected nil metadata when Metadata_ is empty")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	bt := &BalanceTransaction{
		CustomerId:    "cus_rt",
		Amount:        -2500,
		Currency:      "usd",
		Type:          "invoice_payment",
		Description:   "Invoice #123",
		InvoiceId:     "inv_rt",
		EndingBalance: 7500,
		Metadata:      map[string]interface{}{"auto": true},
	}

	ps, err := bt.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	bt2 := &BalanceTransaction{}
	err = bt2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if bt2.CustomerId != "cus_rt" {
		t.Errorf("expected cus_rt, got %s", bt2.CustomerId)
	}
	if bt2.Type != "invoice_payment" {
		t.Errorf("expected invoice_payment, got %s", bt2.Type)
	}
	if bt2.InvoiceId != "inv_rt" {
		t.Errorf("expected inv_rt, got %s", bt2.InvoiceId)
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	bt := &BalanceTransaction{}
	if bt.CustomerId != "" {
		t.Errorf("expected empty, got %q", bt.CustomerId)
	}
	if bt.Amount != 0 {
		t.Errorf("expected 0, got %d", bt.Amount)
	}
	if bt.Currency != "" {
		t.Errorf("expected empty, got %s", bt.Currency)
	}
	if bt.Type != "" {
		t.Errorf("expected empty, got %q", bt.Type)
	}
	if bt.Description != "" {
		t.Errorf("expected empty, got %q", bt.Description)
	}
	if bt.InvoiceId != "" {
		t.Errorf("expected empty, got %q", bt.InvoiceId)
	}
	if bt.CreditNoteId != "" {
		t.Errorf("expected empty, got %q", bt.CreditNoteId)
	}
	if bt.SourceRef != "" {
		t.Errorf("expected empty, got %q", bt.SourceRef)
	}
	if bt.EndingBalance != 0 {
		t.Errorf("expected 0, got %d", bt.EndingBalance)
	}
	if bt.Metadata != nil {
		t.Error("expected nil metadata")
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	bt := &BalanceTransaction{
		CustomerId:    "cus_456",
		Amount:        5000,
		Currency:      "eur",
		Type:          "deposit",
		Description:   "Manual deposit",
		InvoiceId:     "inv_789",
		CreditNoteId:  "cn_abc",
		SourceRef:     "ext_ref_123",
		EndingBalance: 15000,
	}
	if bt.CustomerId != "cus_456" {
		t.Errorf("expected cus_456, got %s", bt.CustomerId)
	}
	if bt.Amount != 5000 {
		t.Errorf("expected 5000, got %d", bt.Amount)
	}
	if string(bt.Currency) != "eur" {
		t.Errorf("expected eur, got %s", bt.Currency)
	}
	if bt.Type != "deposit" {
		t.Errorf("expected deposit, got %s", bt.Type)
	}
	if bt.CreditNoteId != "cn_abc" {
		t.Errorf("expected cn_abc, got %s", bt.CreditNoteId)
	}
	if bt.SourceRef != "ext_ref_123" {
		t.Errorf("expected ext_ref_123, got %s", bt.SourceRef)
	}
	if bt.EndingBalance != 15000 {
		t.Errorf("expected 15000, got %d", bt.EndingBalance)
	}
}

// --- Transaction types ---

func TestTransactionTypes(t *testing.T) {
	types := []string{
		"adjustment", "credit_note", "invoice_payment",
		"deposit", "bank_transfer", "refund",
	}
	for _, typ := range types {
		bt := &BalanceTransaction{Type: typ}
		if bt.Type != typ {
			t.Errorf("expected %s, got %s", typ, bt.Type)
		}
	}
}

// --- Positive amount (credit) ---

func TestPositiveAmount(t *testing.T) {
	bt := &BalanceTransaction{Amount: 10000, Type: "deposit"}
	if bt.Amount <= 0 {
		t.Error("expected positive amount for credit")
	}
}

// --- Negative amount (debit) ---

func TestNegativeAmount(t *testing.T) {
	bt := &BalanceTransaction{Amount: -5000, Type: "invoice_payment"}
	if bt.Amount >= 0 {
		t.Error("expected negative amount for debit")
	}
}

// --- Zero amount ---

func TestZeroAmount(t *testing.T) {
	bt := &BalanceTransaction{Amount: 0, Type: "adjustment"}
	if bt.Amount != 0 {
		t.Errorf("expected 0, got %d", bt.Amount)
	}
}

// --- Load error paths ---

func TestLoad_InvalidMetadataJSON(t *testing.T) {
	bt := &BalanceTransaction{}
	bt.Metadata_ = "not-valid-json"
	err := bt.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Metadata_ JSON")
	}
}

func TestLoad_LoadStructError(t *testing.T) {
	bt := &BalanceTransaction{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := bt.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	bt := &BalanceTransaction{}
	bt.Init(db)
	if bt.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- Defaults via New ---

func TestNew_Defaults(t *testing.T) {
	db := testDB()
	bt := New(db)
	if bt.Currency != "usd" {
		t.Errorf("expected usd, got %s", bt.Currency)
	}
	if bt.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

func TestInit_OrmDefault(t *testing.T) {
	db := testDB()
	bt := &BalanceTransaction{}
	bt.Init(db)
	// orm:"default:usd" applied by Init
	if bt.Currency != "usd" {
		t.Errorf("expected usd, got %s", bt.Currency)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	bt := New(db)
	if bt == nil {
		t.Fatal("expected non-nil BalanceTransaction")
	}
	if bt.Currency != "usd" {
		t.Errorf("expected usd, got %s", bt.Currency)
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

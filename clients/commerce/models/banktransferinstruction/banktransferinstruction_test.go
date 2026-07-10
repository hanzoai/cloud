package banktransferinstruction

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
	i := &BankTransferInstruction{}
	if i.Kind() != "bank-transfer-instruction" {
		t.Errorf("expected 'bank-transfer-instruction', got %q", i.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	i := &BankTransferInstruction{}
	if i.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Save serializes Metadata_ ---

func TestSave_SerializesMetadata(t *testing.T) {
	i := &BankTransferInstruction{
		Metadata: map[string]interface{}{"key": "value", "num": float64(42)},
	}
	ps, err := i.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if i.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after Save")
	}
}

func TestSave_NilMetadata(t *testing.T) {
	i := &BankTransferInstruction{}
	_, err := i.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	// nil metadata serializes to "null"
	if i.Metadata_ == "" {
		t.Error("expected Metadata_ to be set even for nil")
	}
}

func TestSave_EmptyMetadata(t *testing.T) {
	i := &BankTransferInstruction{
		Metadata: map[string]interface{}{},
	}
	_, err := i.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if i.Metadata_ == "" {
		t.Error("expected Metadata_ to be set")
	}
}

// --- Load deserializes Metadata_ ---

func TestLoad_DeserializesMetadata(t *testing.T) {
	i := &BankTransferInstruction{
		Metadata: map[string]interface{}{"foo": "bar"},
	}
	// Save to populate Metadata_
	_, err := i.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	saved := i.Metadata_

	// Create new instance, set the serialized field, then Load
	i2 := &BankTransferInstruction{}
	i2.Metadata_ = saved
	props := []datastore.Property{
		{Name: "Metadata_", Value: saved},
	}
	// LoadStruct will set fields from properties, then we deserialize
	// We need to simulate the full Load path
	err = i2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if i2.Metadata == nil {
		t.Fatal("expected non-nil Metadata after Load")
	}
	if i2.Metadata["foo"] != "bar" {
		t.Errorf("expected foo=bar, got %v", i2.Metadata["foo"])
	}
}

func TestLoad_EmptyMetadataString(t *testing.T) {
	i := &BankTransferInstruction{}
	// Load with empty properties - Metadata_ stays empty, no deserialization
	err := i.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if i.Metadata != nil {
		t.Error("expected nil metadata when Metadata_ is empty")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	i := &BankTransferInstruction{
		CustomerId:    "cus_123",
		Currency:      "usd",
		Type:          "ach",
		Reference:     "REF-001",
		BankName:      "Test Bank",
		AccountHolder: "John Doe",
		AccountNumber: "4567",
		RoutingNumber: "021000021",
		Status:        "active",
		Metadata:      map[string]interface{}{"purpose": "deposit"},
	}

	// Save populates Metadata_
	ps, err := i.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if len(ps) == 0 {
		t.Fatal("expected non-empty properties from Save")
	}

	// Load into fresh instance
	i2 := &BankTransferInstruction{}
	err = i2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	// Properties carry JSON fields (camelCase)
	if i2.CustomerId != "cus_123" {
		t.Errorf("expected cus_123, got %s", i2.CustomerId)
	}
	if i2.Reference != "REF-001" {
		t.Errorf("expected REF-001, got %s", i2.Reference)
	}
}

// --- Status constants ---

func TestStatusFieldAssignment(t *testing.T) {
	cases := []struct {
		status string
	}{
		{"active"},
		{"expired"},
	}
	for _, tc := range cases {
		i := &BankTransferInstruction{Status: tc.status}
		if i.Status != tc.status {
			t.Errorf("expected %s, got %s", tc.status, i.Status)
		}
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	i := &BankTransferInstruction{}
	if i.CustomerId != "" {
		t.Errorf("expected empty, got %q", i.CustomerId)
	}
	if i.Currency != "" {
		t.Errorf("expected empty, got %s", i.Currency)
	}
	if i.Type != "" {
		t.Errorf("expected empty, got %q", i.Type)
	}
	if i.Reference != "" {
		t.Errorf("expected empty, got %q", i.Reference)
	}
	if i.BankName != "" {
		t.Errorf("expected empty, got %q", i.BankName)
	}
	if i.AccountNumber != "" {
		t.Errorf("expected empty, got %q", i.AccountNumber)
	}
	if i.IBAN != "" {
		t.Errorf("expected empty, got %q", i.IBAN)
	}
	if i.BIC != "" {
		t.Errorf("expected empty, got %q", i.BIC)
	}
	if i.Status != "" {
		t.Errorf("expected empty, got %q", i.Status)
	}
	if i.Metadata != nil {
		t.Error("expected nil metadata")
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	i := &BankTransferInstruction{
		CustomerId:    "cus_abc",
		Currency:      "eur",
		Type:          "sepa",
		Reference:     "SEPA-REF-123",
		BankName:      "Euro Bank",
		AccountHolder: "Jane Doe",
		AccountNumber: "7890",
		IBAN:          "DE89370400440532013000",
		BIC:           "COBADEFFXXX",
		Status:        "active",
	}
	if i.CustomerId != "cus_abc" {
		t.Errorf("expected cus_abc, got %s", i.CustomerId)
	}
	if string(i.Currency) != "eur" {
		t.Errorf("expected eur, got %s", i.Currency)
	}
	if i.Type != "sepa" {
		t.Errorf("expected sepa, got %s", i.Type)
	}
	if i.IBAN != "DE89370400440532013000" {
		t.Errorf("expected IBAN, got %s", i.IBAN)
	}
	if i.BIC != "COBADEFFXXX" {
		t.Errorf("expected BIC, got %s", i.BIC)
	}
}

// --- ACH type ---

func TestACHFields(t *testing.T) {
	i := &BankTransferInstruction{
		Type:          "ach",
		RoutingNumber: "021000021",
		AccountNumber: "1234",
	}
	if i.RoutingNumber != "021000021" {
		t.Errorf("expected routing number, got %s", i.RoutingNumber)
	}
}

// --- Wire type ---

func TestWireFields(t *testing.T) {
	i := &BankTransferInstruction{
		Type:          "wire",
		BankName:      "Wire Bank",
		AccountNumber: "5678",
	}
	if i.Type != "wire" {
		t.Errorf("expected wire, got %s", i.Type)
	}
}

// --- Load error paths ---

func TestLoad_LoadStructError(t *testing.T) {
	i := &BankTransferInstruction{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := i.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestLoad_InvalidMetadataJSON(t *testing.T) {
	i := &BankTransferInstruction{}
	i.Metadata_ = "not-valid-json"
	err := i.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Metadata_ JSON")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	i := &BankTransferInstruction{}
	i.Init(db)
	if i.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- ORM Defaults ---

func TestInit_OrmDefaults(t *testing.T) {
	db := testDB()
	i := &BankTransferInstruction{}
	i.Init(db)
	// orm:"default:active" and orm:"default:usd" applied by Init
	if i.Status != "active" {
		t.Errorf("expected active, got %s", i.Status)
	}
	if i.Currency != "usd" {
		t.Errorf("expected usd, got %s", i.Currency)
	}
}

func TestNew_SetsParent(t *testing.T) {
	db := testDB()
	i := New(db)
	if i.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	i := New(db)
	if i == nil {
		t.Fatal("expected non-nil BankTransferInstruction")
	}
	if i.Status != "active" {
		t.Errorf("expected active, got %s", i.Status)
	}
	if i.Currency != "usd" {
		t.Errorf("expected usd, got %s", i.Currency)
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

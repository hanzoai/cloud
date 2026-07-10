package subscriptionitem

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
	si := &SubscriptionItem{}
	if si.Kind() != "subscription-item" {
		t.Errorf("expected 'subscription-item', got %q", si.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	si := &SubscriptionItem{}
	if si.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Save serializes Metadata_ ---

func TestSave_SerializesMetadata(t *testing.T) {
	si := &SubscriptionItem{
		Metadata: map[string]interface{}{"feature": "premium"},
	}
	ps, err := si.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if si.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after Save")
	}
}

func TestSave_NilMetadata(t *testing.T) {
	si := &SubscriptionItem{}
	_, err := si.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if si.Metadata_ == "" {
		t.Error("expected Metadata_ to be set")
	}
}

// --- Load deserializes Metadata_ ---

func TestLoad_DeserializesMetadata(t *testing.T) {
	si := &SubscriptionItem{
		Metadata: map[string]interface{}{"tier": "gold"},
	}
	_, err := si.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	saved := si.Metadata_

	si2 := &SubscriptionItem{}
	si2.Metadata_ = saved
	props := []datastore.Property{
		{Name: "Metadata_", Value: saved},
	}
	err = si2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if si2.Metadata == nil {
		t.Fatal("expected non-nil Metadata after Load")
	}
	if si2.Metadata["tier"] != "gold" {
		t.Errorf("expected tier=gold, got %v", si2.Metadata["tier"])
	}
}

func TestLoad_EmptyMetadataString(t *testing.T) {
	si := &SubscriptionItem{}
	err := si.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if si.Metadata != nil {
		t.Error("expected nil metadata when Metadata_ is empty")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	si := &SubscriptionItem{
		SubscriptionId: "sub_123",
		PriceId:        "price_abc",
		PlanId:         "plan_def",
		MeterId:        "meter_ghi",
		Quantity:        5,
		BillingMode:    "licensed",
		Metadata:       map[string]interface{}{"seats": float64(5)},
	}

	ps, err := si.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	si2 := &SubscriptionItem{}
	err = si2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if si2.SubscriptionId != "sub_123" {
		t.Errorf("expected sub_123, got %s", si2.SubscriptionId)
	}
	if si2.PriceId != "price_abc" {
		t.Errorf("expected price_abc, got %s", si2.PriceId)
	}
	if si2.BillingMode != "licensed" {
		t.Errorf("expected licensed, got %s", si2.BillingMode)
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	si := &SubscriptionItem{}
	if si.SubscriptionId != "" {
		t.Errorf("expected empty, got %q", si.SubscriptionId)
	}
	if si.PriceId != "" {
		t.Errorf("expected empty, got %q", si.PriceId)
	}
	if si.PlanId != "" {
		t.Errorf("expected empty, got %q", si.PlanId)
	}
	if si.MeterId != "" {
		t.Errorf("expected empty, got %q", si.MeterId)
	}
	if si.Quantity != 0 {
		t.Errorf("expected 0, got %d", si.Quantity)
	}
	if si.BillingMode != "" {
		t.Errorf("expected empty, got %q", si.BillingMode)
	}
	if si.Metadata != nil {
		t.Error("expected nil metadata")
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	si := &SubscriptionItem{
		SubscriptionId: "sub_xyz",
		PriceId:        "price_123",
		PlanId:         "plan_456",
		MeterId:        "meter_789",
		Quantity:        10,
		BillingMode:    "metered",
	}
	if si.SubscriptionId != "sub_xyz" {
		t.Errorf("expected sub_xyz, got %s", si.SubscriptionId)
	}
	if si.Quantity != 10 {
		t.Errorf("expected 10, got %d", si.Quantity)
	}
	if si.BillingMode != "metered" {
		t.Errorf("expected metered, got %s", si.BillingMode)
	}
	if si.MeterId != "meter_789" {
		t.Errorf("expected meter_789, got %s", si.MeterId)
	}
}

// --- BillingMode values ---

func TestBillingModeValues(t *testing.T) {
	cases := []string{"licensed", "metered"}
	for _, mode := range cases {
		si := &SubscriptionItem{BillingMode: mode}
		if si.BillingMode != mode {
			t.Errorf("expected %s, got %s", mode, si.BillingMode)
		}
	}
}

// --- Load error paths ---

func TestLoad_InvalidMetadataJSON(t *testing.T) {
	si := &SubscriptionItem{}
	si.Metadata_ = "not-valid-json"
	err := si.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Metadata_ JSON")
	}
}

func TestLoad_LoadStructError(t *testing.T) {
	si := &SubscriptionItem{}
	// Property with unmarshalable value (func) triggers json.Marshal error in LoadStruct
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := si.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	si := &SubscriptionItem{}
	si.Init(db)
	if si.Datastore() != db {
		t.Error("expected Datastore() to be set")
	}
}

// --- New sets defaults ---

func TestNew_SetsDefaults(t *testing.T) {
	db := testDB()
	si := New(db)
	if si.BillingMode != "licensed" {
		t.Errorf("expected licensed, got %s", si.BillingMode)
	}
	if si.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	si := New(db)
	if si == nil {
		t.Fatal("expected non-nil SubscriptionItem")
	}
	if si.BillingMode != "licensed" {
		t.Errorf("expected licensed, got %s", si.BillingMode)
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

package billingevent

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
	e := &BillingEvent{}
	if e.Kind() != "billing-event" {
		t.Errorf("expected 'billing-event', got %q", e.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	e := &BillingEvent{}
	if e.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Save serializes Data_ and PreviousData_ ---

func TestSave_SerializesData(t *testing.T) {
	e := &BillingEvent{
		Data:         map[string]interface{}{"amount": float64(5000), "status": "succeeded"},
		PreviousData: map[string]interface{}{"amount": float64(5000), "status": "processing"},
	}
	ps, err := e.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if e.Data_ == "" {
		t.Error("expected Data_ to be populated after Save")
	}
	if e.PreviousData_ == "" {
		t.Error("expected PreviousData_ to be populated after Save")
	}
}

func TestSave_NilData(t *testing.T) {
	e := &BillingEvent{}
	_, err := e.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	// nil maps serialize to "null"
	if e.Data_ == "" {
		t.Error("expected Data_ to be set")
	}
	if e.PreviousData_ == "" {
		t.Error("expected PreviousData_ to be set")
	}
}

func TestSave_EmptyData(t *testing.T) {
	e := &BillingEvent{
		Data:         map[string]interface{}{},
		PreviousData: map[string]interface{}{},
	}
	_, err := e.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if e.Data_ == "" {
		t.Error("expected Data_ to be set")
	}
}

// --- Load deserializes Data_ and PreviousData_ ---

func TestLoad_DeserializesData(t *testing.T) {
	e := &BillingEvent{
		Data:         map[string]interface{}{"key": "val"},
		PreviousData: map[string]interface{}{"old": "state"},
	}
	_, err := e.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	savedData := e.Data_
	savedPrev := e.PreviousData_

	e2 := &BillingEvent{}
	e2.Data_ = savedData
	e2.PreviousData_ = savedPrev
	props := []datastore.Property{
		{Name: "Data_", Value: savedData},
		{Name: "PreviousData_", Value: savedPrev},
	}
	err = e2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if e2.Data == nil {
		t.Fatal("expected non-nil Data after Load")
	}
	if e2.Data["key"] != "val" {
		t.Errorf("expected key=val, got %v", e2.Data["key"])
	}
	if e2.PreviousData == nil {
		t.Fatal("expected non-nil PreviousData after Load")
	}
	if e2.PreviousData["old"] != "state" {
		t.Errorf("expected old=state, got %v", e2.PreviousData["old"])
	}
}

func TestLoad_EmptyStrings(t *testing.T) {
	e := &BillingEvent{}
	err := e.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if e.Data != nil {
		t.Error("expected nil Data when Data_ is empty")
	}
	if e.PreviousData != nil {
		t.Error("expected nil PreviousData when PreviousData_ is empty")
	}
}

func TestLoad_OnlyData(t *testing.T) {
	e := &BillingEvent{
		Data: map[string]interface{}{"only": "data"},
	}
	_, _ = e.Save()
	savedData := e.Data_

	e2 := &BillingEvent{}
	e2.Data_ = savedData
	props := []datastore.Property{
		{Name: "Data_", Value: savedData},
	}
	err := e2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if e2.Data == nil {
		t.Fatal("expected non-nil Data")
	}
	if e2.PreviousData != nil {
		t.Error("expected nil PreviousData")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	e := &BillingEvent{
		Type:       "payment_intent.succeeded",
		ObjectType: "payment_intent",
		ObjectId:   "pi_abc",
		CustomerId: "cus_123",
		Data:       map[string]interface{}{"amount": float64(5000)},
		Pending:    true,
		Livemode:   true,
	}

	ps, err := e.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	e2 := &BillingEvent{}
	err = e2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if e2.Type != "payment_intent.succeeded" {
		t.Errorf("expected type, got %s", e2.Type)
	}
	if e2.ObjectType != "payment_intent" {
		t.Errorf("expected objectType, got %s", e2.ObjectType)
	}
	if e2.ObjectId != "pi_abc" {
		t.Errorf("expected objectId, got %s", e2.ObjectId)
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	e := &BillingEvent{}
	if e.Type != "" {
		t.Errorf("expected empty, got %q", e.Type)
	}
	if e.ObjectType != "" {
		t.Errorf("expected empty, got %q", e.ObjectType)
	}
	if e.ObjectId != "" {
		t.Errorf("expected empty, got %q", e.ObjectId)
	}
	if e.CustomerId != "" {
		t.Errorf("expected empty, got %q", e.CustomerId)
	}
	if e.Data != nil {
		t.Error("expected nil Data")
	}
	if e.PreviousData != nil {
		t.Error("expected nil PreviousData")
	}
	if e.Pending {
		t.Error("expected false Pending")
	}
	if e.Livemode {
		t.Error("expected false Livemode")
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	e := &BillingEvent{
		Type:       "invoice.paid",
		ObjectType: "invoice",
		ObjectId:   "inv_456",
		CustomerId: "cus_789",
		Pending:    false,
		Livemode:   true,
	}
	if e.Type != "invoice.paid" {
		t.Errorf("expected invoice.paid, got %s", e.Type)
	}
	if e.ObjectType != "invoice" {
		t.Errorf("expected invoice, got %s", e.ObjectType)
	}
	if e.Livemode != true {
		t.Error("expected livemode true")
	}
}

// --- Pending and Livemode ---

func TestPendingAndLivemode(t *testing.T) {
	e := &BillingEvent{Pending: true, Livemode: false}
	if !e.Pending {
		t.Error("expected Pending true")
	}
	if e.Livemode {
		t.Error("expected Livemode false")
	}

	e.Pending = false
	e.Livemode = true
	if e.Pending {
		t.Error("expected Pending false")
	}
	if !e.Livemode {
		t.Error("expected Livemode true")
	}
}

// --- Load error paths ---

func TestLoad_LoadStructError(t *testing.T) {
	e := &BillingEvent{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := e.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestLoad_InvalidDataJSON(t *testing.T) {
	e := &BillingEvent{}
	e.Data_ = "not-valid-json"
	err := e.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Data_ JSON")
	}
}

func TestLoad_InvalidPreviousDataJSON(t *testing.T) {
	e := &BillingEvent{}
	e.PreviousData_ = "not-valid-json"
	// Data_ is empty so it skips that, but PreviousData_ is invalid
	err := e.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid PreviousData_ JSON")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	e := &BillingEvent{}
	e.Init(db)
	if e.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- ORM Defaults ---

func TestInit_OrmDefaults(t *testing.T) {
	db := testDB()
	e := &BillingEvent{}
	e.Init(db)
	// orm:"default:true" applied by Init
	if !e.Pending {
		t.Error("expected Pending true")
	}
	if !e.Livemode {
		t.Error("expected Livemode true")
	}
}

func TestNew_SetsParent(t *testing.T) {
	db := testDB()
	e := New(db)
	if e.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

// --- New ---

func TestNewFunc(t *testing.T) {
	db := testDB()
	e := New(db)
	if e == nil {
		t.Fatal("expected non-nil BillingEvent")
	}
	if !e.Pending {
		t.Error("expected Pending true")
	}
	if !e.Livemode {
		t.Error("expected Livemode true")
	}
}

// --- Query ---

func TestQueryFunc(t *testing.T) {
	db := testDB()
	q := Query(db)
	if q == nil {
		t.Fatal("expected non-nil query")
	}
}

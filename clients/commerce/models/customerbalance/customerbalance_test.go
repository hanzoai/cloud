package customerbalance

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- Kind ---

func TestKind(t *testing.T) {
	cb := &CustomerBalance{}
	if cb.Kind() != "customer-balance" {
		t.Errorf("expected 'customer-balance', got %q", cb.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	cb := &CustomerBalance{}
	if cb.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Save/Load ---

func TestSave_NoError(t *testing.T) {
	cb := &CustomerBalance{
		CustomerId: "cus_123",
		Currency:   "usd",
		Balance:    5000,
	}
	ps, err := cb.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
}

func TestLoad_NoError(t *testing.T) {
	cb := &CustomerBalance{}
	err := cb.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
}

func TestLoad_LoadStructError(t *testing.T) {
	cb := &CustomerBalance{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := cb.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	cb := &CustomerBalance{
		CustomerId: "cus_round",
		Currency:   "eur",
		Balance:    15000,
	}

	ps, err := cb.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cb2 := &CustomerBalance{}
	err = cb2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cb2.CustomerId != "cus_round" {
		t.Errorf("expected cus_round, got %s", cb2.CustomerId)
	}
	if string(cb2.Currency) != "eur" {
		t.Errorf("expected eur, got %s", cb2.Currency)
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	cb := &CustomerBalance{}
	if cb.CustomerId != "" {
		t.Errorf("expected empty, got %q", cb.CustomerId)
	}
	if cb.Currency != "" {
		t.Errorf("expected empty, got %s", cb.Currency)
	}
	if cb.Balance != 0 {
		t.Errorf("expected 0, got %d", cb.Balance)
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	cb := &CustomerBalance{
		CustomerId: "cus_abc",
		Currency:   "gbp",
		Balance:    -3000,
	}
	if cb.CustomerId != "cus_abc" {
		t.Errorf("expected cus_abc, got %s", cb.CustomerId)
	}
	if string(cb.Currency) != "gbp" {
		t.Errorf("expected gbp, got %s", cb.Currency)
	}
	if cb.Balance != -3000 {
		t.Errorf("expected -3000, got %d", cb.Balance)
	}
}

// --- Positive balance ---

func TestPositiveBalance(t *testing.T) {
	cb := &CustomerBalance{Balance: 10000}
	if cb.Balance <= 0 {
		t.Error("expected positive balance")
	}
}

// --- Negative balance (owed) ---

func TestNegativeBalance(t *testing.T) {
	cb := &CustomerBalance{Balance: -5000}
	if cb.Balance >= 0 {
		t.Error("expected negative balance")
	}
}

// --- Zero balance ---

func TestZeroBalance(t *testing.T) {
	cb := &CustomerBalance{Balance: 0}
	if cb.Balance != 0 {
		t.Errorf("expected 0, got %d", cb.Balance)
	}
}

// --- Currency types ---

func TestCurrencyTypes(t *testing.T) {
	currencies := []string{"usd", "eur", "gbp", "jpy", "cad"}
	for _, cur := range currencies {
		cb := &CustomerBalance{Currency: currency.Type(cur)}
		if string(cb.Currency) != cur {
			t.Errorf("expected %s, got %s", cur, cb.Currency)
		}
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	cb := &CustomerBalance{}
	cb.Init(db)
	if cb.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- ORM Defaults ---

func TestInit_OrmDefaults(t *testing.T) {
	db := testDB()
	cb := &CustomerBalance{}
	cb.Init(db)
	// orm:"default:usd" applied by Init
	if cb.Currency != "usd" {
		t.Errorf("expected usd, got %s", cb.Currency)
	}
}

func TestNew_SetsParent(t *testing.T) {
	db := testDB()
	cb := New(db)
	if cb.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	cb := New(db)
	if cb == nil {
		t.Fatal("expected non-nil CustomerBalance")
	}
	if cb.Currency != "usd" {
		t.Errorf("expected usd, got %s", cb.Currency)
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

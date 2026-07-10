package usagewatermark

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- Kind ---

func TestKind(t *testing.T) {
	w := &UsageWatermark{}
	if w.Kind() != "usage-watermark" {
		t.Errorf("expected 'usage-watermark', got %q", w.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	w := &UsageWatermark{}
	if w.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Save/Load round trip ---

func TestSave_NoError(t *testing.T) {
	w := &UsageWatermark{
		SubscriptionItemId: "si_abc",
		MeterId:            "meter_123",
		InvoiceId:          "inv_456",
		AggregatedValue:    5000,
		EventCount:         42,
	}
	ps, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
}

func TestLoad_NoError(t *testing.T) {
	w := &UsageWatermark{}
	err := w.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
}

func TestLoad_LoadStructError(t *testing.T) {
	w := &UsageWatermark{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := w.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	start := now.Add(-24 * time.Hour)
	end := now

	w := &UsageWatermark{
		SubscriptionItemId: "si_round",
		MeterId:            "meter_round",
		InvoiceId:          "inv_round",
		PeriodStart:        start,
		PeriodEnd:          end,
		AggregatedValue:    10000,
		EventCount:         100,
		LastEventTimestamp:  now,
	}

	ps, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	w2 := &UsageWatermark{}
	err = w2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if w2.SubscriptionItemId != "si_round" {
		t.Errorf("expected si_round, got %s", w2.SubscriptionItemId)
	}
	if w2.MeterId != "meter_round" {
		t.Errorf("expected meter_round, got %s", w2.MeterId)
	}
	if w2.InvoiceId != "inv_round" {
		t.Errorf("expected inv_round, got %s", w2.InvoiceId)
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	w := &UsageWatermark{}
	if w.SubscriptionItemId != "" {
		t.Errorf("expected empty, got %q", w.SubscriptionItemId)
	}
	if w.MeterId != "" {
		t.Errorf("expected empty, got %q", w.MeterId)
	}
	if w.InvoiceId != "" {
		t.Errorf("expected empty, got %q", w.InvoiceId)
	}
	if w.AggregatedValue != 0 {
		t.Errorf("expected 0, got %d", w.AggregatedValue)
	}
	if w.EventCount != 0 {
		t.Errorf("expected 0, got %d", w.EventCount)
	}
	if !w.PeriodStart.IsZero() {
		t.Error("expected zero PeriodStart")
	}
	if !w.PeriodEnd.IsZero() {
		t.Error("expected zero PeriodEnd")
	}
	if !w.LastEventTimestamp.IsZero() {
		t.Error("expected zero LastEventTimestamp")
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	now := time.Now()
	w := &UsageWatermark{
		SubscriptionItemId: "si_test",
		MeterId:            "meter_test",
		InvoiceId:          "inv_test",
		PeriodStart:        now.Add(-48 * time.Hour),
		PeriodEnd:          now,
		AggregatedValue:    7500,
		EventCount:         55,
		LastEventTimestamp:  now,
	}
	if w.SubscriptionItemId != "si_test" {
		t.Errorf("expected si_test, got %s", w.SubscriptionItemId)
	}
	if w.MeterId != "meter_test" {
		t.Errorf("expected meter_test, got %s", w.MeterId)
	}
	if w.AggregatedValue != 7500 {
		t.Errorf("expected 7500, got %d", w.AggregatedValue)
	}
	if w.EventCount != 55 {
		t.Errorf("expected 55, got %d", w.EventCount)
	}
}

// --- Period time ordering ---

func TestPeriodStartBeforeEnd(t *testing.T) {
	now := time.Now()
	w := &UsageWatermark{
		PeriodStart: now.Add(-24 * time.Hour),
		PeriodEnd:   now,
	}
	if !w.PeriodStart.Before(w.PeriodEnd) {
		t.Error("expected PeriodStart before PeriodEnd")
	}
}

// --- Event count and aggregated value ---

func TestLargeValues(t *testing.T) {
	w := &UsageWatermark{
		AggregatedValue: 999999999,
		EventCount:      1000000,
	}
	if w.AggregatedValue != 999999999 {
		t.Errorf("expected large value, got %d", w.AggregatedValue)
	}
	if w.EventCount != 1000000 {
		t.Errorf("expected large count, got %d", w.EventCount)
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	w := &UsageWatermark{}
	w.Init(db)
	if w.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- Defaults (via New) ---

func TestDefaults(t *testing.T) {
	db := testDB()
	w := New(db)
	if w.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	w := New(db)
	if w == nil {
		t.Fatal("expected non-nil UsageWatermark")
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

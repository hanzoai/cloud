package webhookendpoint

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
	w := &WebhookEndpoint{}
	if w.Kind() != "webhook-endpoint" {
		t.Errorf("expected 'webhook-endpoint', got %q", w.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	w := &WebhookEndpoint{}
	if w.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- MatchesEvent ---

func TestMatchesEvent_EmptyList_MatchesAll(t *testing.T) {
	w := &WebhookEndpoint{}
	if !w.MatchesEvent("invoice.paid") {
		t.Error("empty events list should match all events")
	}
	if !w.MatchesEvent("payment_intent.succeeded") {
		t.Error("empty events list should match all events")
	}
}

func TestMatchesEvent_ExactMatch(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{"invoice.paid", "payment_intent.succeeded"},
	}
	if !w.MatchesEvent("invoice.paid") {
		t.Error("expected match for invoice.paid")
	}
	if !w.MatchesEvent("payment_intent.succeeded") {
		t.Error("expected match for payment_intent.succeeded")
	}
}

func TestMatchesEvent_NoMatch(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{"invoice.paid"},
	}
	if w.MatchesEvent("payment_intent.succeeded") {
		t.Error("expected no match for payment_intent.succeeded")
	}
}

func TestMatchesEvent_Wildcard(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{"*"},
	}
	if !w.MatchesEvent("invoice.paid") {
		t.Error("wildcard should match any event")
	}
	if !w.MatchesEvent("anything.at.all") {
		t.Error("wildcard should match any event")
	}
}

func TestMatchesEvent_WildcardAmongOthers(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{"invoice.paid", "*"},
	}
	if !w.MatchesEvent("unknown.event") {
		t.Error("wildcard in list should match any event")
	}
}

func TestMatchesEvent_SingleEvent(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{"subscription.updated"},
	}
	if !w.MatchesEvent("subscription.updated") {
		t.Error("expected match")
	}
	if w.MatchesEvent("subscription.created") {
		t.Error("expected no match")
	}
}

func TestMatchesEvent_EmptyEventType(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{"invoice.paid"},
	}
	if w.MatchesEvent("") {
		t.Error("expected no match for empty event type")
	}
}

func TestMatchesEvent_NilEvents_MatchesAll(t *testing.T) {
	w := &WebhookEndpoint{Events: nil}
	if !w.MatchesEvent("any.event") {
		t.Error("nil events list should match all events")
	}
}

// --- Save serializes Events_ and Metadata_ ---

func TestSave_SerializesEvents(t *testing.T) {
	w := &WebhookEndpoint{
		Events:   []string{"invoice.paid", "payment_intent.succeeded"},
		Metadata: map[string]interface{}{"env": "production"},
	}
	ps, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if w.Events_ == "" {
		t.Error("expected Events_ to be populated after Save")
	}
	if w.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after Save")
	}
}

func TestSave_NilEvents(t *testing.T) {
	w := &WebhookEndpoint{}
	_, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if w.Events_ == "" {
		t.Error("expected Events_ to be set")
	}
}

func TestSave_EmptyEvents(t *testing.T) {
	w := &WebhookEndpoint{
		Events: []string{},
	}
	_, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if w.Events_ == "" {
		t.Error("expected Events_ to be set")
	}
}

// --- Load deserializes Events_ and Metadata_ ---

func TestLoad_DeserializesEvents(t *testing.T) {
	w := &WebhookEndpoint{
		Events:   []string{"invoice.paid", "charge.refunded"},
		Metadata: map[string]interface{}{"team": "billing"},
	}
	_, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	savedEvents := w.Events_
	savedMeta := w.Metadata_

	w2 := &WebhookEndpoint{}
	w2.Events_ = savedEvents
	w2.Metadata_ = savedMeta
	props := []datastore.Property{
		{Name: "Events_", Value: savedEvents},
		{Name: "Metadata_", Value: savedMeta},
	}
	err = w2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(w2.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(w2.Events))
	}
	if w2.Events[0] != "invoice.paid" {
		t.Errorf("expected invoice.paid, got %s", w2.Events[0])
	}
	if w2.Metadata == nil {
		t.Fatal("expected non-nil Metadata")
	}
	if w2.Metadata["team"] != "billing" {
		t.Errorf("expected team=billing, got %v", w2.Metadata["team"])
	}
}

func TestLoad_EmptyStrings(t *testing.T) {
	w := &WebhookEndpoint{}
	err := w.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if w.Events != nil {
		t.Error("expected nil Events when Events_ is empty")
	}
	if w.Metadata != nil {
		t.Error("expected nil Metadata when Metadata_ is empty")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	w := &WebhookEndpoint{
		Url:         "https://example.com/webhook",
		Secret:      "whsec_test123",
		Status:      "enabled",
		Events:      []string{"invoice.paid"},
		Description: "Production webhook",
		Metadata:    map[string]interface{}{"version": "2"},
	}

	ps, err := w.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	w2 := &WebhookEndpoint{}
	err = w2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if w2.Url != "https://example.com/webhook" {
		t.Errorf("expected url, got %s", w2.Url)
	}
	if w2.Status != "enabled" {
		t.Errorf("expected enabled, got %s", w2.Status)
	}
}

// --- Struct zero values ---

func TestZeroValue(t *testing.T) {
	w := &WebhookEndpoint{}
	if w.Url != "" {
		t.Errorf("expected empty, got %q", w.Url)
	}
	if w.Secret != "" {
		t.Errorf("expected empty, got %q", w.Secret)
	}
	if w.Status != "" {
		t.Errorf("expected empty, got %q", w.Status)
	}
	if w.Events != nil {
		t.Error("expected nil events")
	}
	if w.Description != "" {
		t.Errorf("expected empty, got %q", w.Description)
	}
	if w.Metadata != nil {
		t.Error("expected nil metadata")
	}
}

// --- Field assignment ---

func TestFieldAssignment(t *testing.T) {
	w := &WebhookEndpoint{
		Url:         "https://api.example.com/hooks",
		Secret:      "whsec_abc123",
		Status:      "disabled",
		Description: "Test endpoint",
		Events:      []string{"*"},
	}
	if w.Url != "https://api.example.com/hooks" {
		t.Errorf("expected url, got %s", w.Url)
	}
	if w.Secret != "whsec_abc123" {
		t.Errorf("expected secret, got %s", w.Secret)
	}
	if w.Status != "disabled" {
		t.Errorf("expected disabled, got %s", w.Status)
	}
	if w.Description != "Test endpoint" {
		t.Errorf("expected description, got %s", w.Description)
	}
}

// --- Load error paths ---

func TestLoad_LoadStructError(t *testing.T) {
	w := &WebhookEndpoint{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := w.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestLoad_InvalidEventsJSON(t *testing.T) {
	w := &WebhookEndpoint{}
	w.Events_ = "not-valid-json"
	err := w.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Events_ JSON")
	}
}

func TestLoad_InvalidMetadataJSON(t *testing.T) {
	w := &WebhookEndpoint{}
	w.Metadata_ = "not-valid-json"
	// Events_ is empty so it skips that, but Metadata_ is invalid
	err := w.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Metadata_ JSON")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	w := &WebhookEndpoint{}
	w.Init(db)
	if w.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- Defaults ---

func TestDefaults(t *testing.T) {
	db := testDB()
	w := &WebhookEndpoint{}
	w.Init(db)
	w.Defaults()
	if w.Status != "enabled" {
		t.Errorf("expected enabled, got %s", w.Status)
	}
	if w.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

func TestDefaults_DoesNotOverwrite(t *testing.T) {
	db := testDB()
	w := &WebhookEndpoint{}
	w.Init(db)
	w.Status = "disabled"
	w.Defaults()
	if w.Status != "disabled" {
		t.Errorf("expected disabled, got %s", w.Status)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	w := New(db)
	if w == nil {
		t.Fatal("expected non-nil WebhookEndpoint")
	}
	if w.Status != "enabled" {
		t.Errorf("expected enabled, got %s", w.Status)
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

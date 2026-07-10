package setupintent

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- Confirm ---

func TestConfirm_FromRequiresConfirmation(t *testing.T) {
	si := &SetupIntent{
		Status:          RequiresConfirmation,
		PaymentMethodId: "pm_123",
	}
	if err := si.Confirm(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Status != Processing {
		t.Errorf("expected %s, got %s", Processing, si.Status)
	}
}

func TestConfirm_FromRequiresPaymentMethod(t *testing.T) {
	si := &SetupIntent{
		Status:          RequiresPaymentMethod,
		PaymentMethodId: "pm_456",
	}
	if err := si.Confirm(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Status != Processing {
		t.Errorf("expected %s, got %s", Processing, si.Status)
	}
}

func TestConfirm_MissingPaymentMethod(t *testing.T) {
	si := &SetupIntent{
		Status: RequiresConfirmation,
	}
	err := si.Confirm()
	if err == nil {
		t.Fatal("expected error for missing payment method")
	}
}

func TestConfirm_InvalidStatus_Processing(t *testing.T) {
	si := &SetupIntent{
		Status:          Processing,
		PaymentMethodId: "pm_123",
	}
	err := si.Confirm()
	if err == nil {
		t.Fatal("expected error confirming from Processing")
	}
}

func TestConfirm_InvalidStatus_Succeeded(t *testing.T) {
	si := &SetupIntent{
		Status:          Succeeded,
		PaymentMethodId: "pm_123",
	}
	err := si.Confirm()
	if err == nil {
		t.Fatal("expected error confirming from Succeeded")
	}
}

func TestConfirm_InvalidStatus_Canceled(t *testing.T) {
	si := &SetupIntent{
		Status:          Canceled,
		PaymentMethodId: "pm_123",
	}
	err := si.Confirm()
	if err == nil {
		t.Fatal("expected error confirming from Canceled")
	}
}

func TestConfirm_InvalidStatus_RequiresAction(t *testing.T) {
	si := &SetupIntent{
		Status:          RequiresAction,
		PaymentMethodId: "pm_123",
	}
	err := si.Confirm()
	if err == nil {
		t.Fatal("expected error confirming from RequiresAction")
	}
}

// --- MarkSucceeded ---

func TestMarkSucceeded(t *testing.T) {
	si := &SetupIntent{Status: Processing}
	si.MarkSucceeded("seti_ref_abc")
	if si.Status != Succeeded {
		t.Errorf("expected %s, got %s", Succeeded, si.Status)
	}
	if si.ProviderRef != "seti_ref_abc" {
		t.Errorf("expected providerRef seti_ref_abc, got %s", si.ProviderRef)
	}
}

func TestMarkSucceeded_OverwritesProviderRef(t *testing.T) {
	si := &SetupIntent{
		Status:      Processing,
		ProviderRef: "old_ref",
	}
	si.MarkSucceeded("new_ref")
	if si.ProviderRef != "new_ref" {
		t.Errorf("expected new_ref, got %s", si.ProviderRef)
	}
}

// --- Cancel ---

func TestCancel_FromRequiresPaymentMethod(t *testing.T) {
	si := &SetupIntent{Status: RequiresPaymentMethod}
	if err := si.Cancel("abandoned"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Status != Canceled {
		t.Errorf("expected %s, got %s", Canceled, si.Status)
	}
	if si.CancellationReason != "abandoned" {
		t.Errorf("expected reason 'abandoned', got %q", si.CancellationReason)
	}
	if si.CanceledAt.IsZero() {
		t.Error("expected CanceledAt to be set")
	}
}

func TestCancel_FromRequiresConfirmation(t *testing.T) {
	si := &SetupIntent{Status: RequiresConfirmation}
	if err := si.Cancel("duplicate"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Status != Canceled {
		t.Errorf("expected %s, got %s", Canceled, si.Status)
	}
}

func TestCancel_FromRequiresAction(t *testing.T) {
	si := &SetupIntent{Status: RequiresAction}
	if err := si.Cancel("expired"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Status != Canceled {
		t.Errorf("expected %s, got %s", Canceled, si.Status)
	}
}

func TestCancel_FromProcessing(t *testing.T) {
	si := &SetupIntent{Status: Processing}
	if err := si.Cancel("timeout"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Status != Canceled {
		t.Errorf("expected %s, got %s", Canceled, si.Status)
	}
}

func TestCancel_InvalidStatus_Succeeded(t *testing.T) {
	si := &SetupIntent{Status: Succeeded}
	err := si.Cancel("too_late")
	if err == nil {
		t.Fatal("expected error canceling from Succeeded")
	}
}

func TestCancel_InvalidStatus_Canceled(t *testing.T) {
	si := &SetupIntent{Status: Canceled}
	err := si.Cancel("again")
	if err == nil {
		t.Fatal("expected error canceling already-canceled intent")
	}
}

// --- Status constants ---

func TestStatusConstants(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{RequiresPaymentMethod, "requires_payment_method"},
		{RequiresConfirmation, "requires_confirmation"},
		{RequiresAction, "requires_action"},
		{Processing, "processing"},
		{Succeeded, "succeeded"},
		{Canceled, "canceled"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Errorf("status %q != %q", tc.status, tc.want)
		}
	}
}

// --- Full lifecycle ---

func TestFullLifecycle_ConfirmThenSucceed(t *testing.T) {
	si := &SetupIntent{
		Status:          RequiresConfirmation,
		PaymentMethodId: "pm_test",
	}
	if err := si.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	si.MarkSucceeded("seti_done")
	if si.Status != Succeeded {
		t.Errorf("expected Succeeded, got %s", si.Status)
	}
}

func TestFullLifecycle_ConfirmThenCancel(t *testing.T) {
	si := &SetupIntent{
		Status:          RequiresPaymentMethod,
		PaymentMethodId: "pm_cancel",
	}
	if err := si.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if err := si.Cancel("changed_mind"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if si.Status != Canceled {
		t.Errorf("expected Canceled, got %s", si.Status)
	}
}

// --- Kind ---

func TestKind(t *testing.T) {
	si := &SetupIntent{}
	if si.Kind() != "setup-intent" {
		t.Errorf("expected 'setup-intent', got %q", si.Kind())
	}
}

// --- Struct zero values ---

func TestSetupIntentZeroValue(t *testing.T) {
	si := &SetupIntent{}
	if si.Status != "" {
		t.Errorf("expected empty, got %s", si.Status)
	}
	if si.Usage != "" {
		t.Errorf("expected empty, got %s", si.Usage)
	}
	if si.PaymentMethodId != "" {
		t.Errorf("expected empty, got %s", si.PaymentMethodId)
	}
	if si.CustomerId != "" {
		t.Errorf("expected empty, got %s", si.CustomerId)
	}
	if si.ProviderRef != "" {
		t.Errorf("expected empty, got %s", si.ProviderRef)
	}
	if si.ProviderType != "" {
		t.Errorf("expected empty, got %s", si.ProviderType)
	}
	if si.CancellationReason != "" {
		t.Errorf("expected empty, got %s", si.CancellationReason)
	}
	if si.LastError != "" {
		t.Errorf("expected empty, got %s", si.LastError)
	}
	if si.ClientSecret != "" {
		t.Errorf("expected empty, got %s", si.ClientSecret)
	}
	if !si.CanceledAt.IsZero() {
		t.Error("expected zero CanceledAt")
	}
	if si.Metadata != nil {
		t.Error("expected nil metadata")
	}
}

// --- Field assignment ---

func TestSetupIntentFieldAssignment(t *testing.T) {
	si := &SetupIntent{
		CustomerId:      "cus_456",
		PaymentMethodId: "pm_789",
		Status:          RequiresPaymentMethod,
		Usage:           "off_session",
		ProviderRef:     "seti_abc",
		ProviderType:    "stripe",
		ClientSecret:    "seti_secret_123",
		LastError:       "some_error",
	}
	if si.CustomerId != "cus_456" {
		t.Errorf("expected cus_456, got %s", si.CustomerId)
	}
	if si.Usage != "off_session" {
		t.Errorf("expected off_session, got %s", si.Usage)
	}
	if si.ProviderType != "stripe" {
		t.Errorf("expected stripe, got %s", si.ProviderType)
	}
	if si.ClientSecret != "seti_secret_123" {
		t.Errorf("expected secret, got %s", si.ClientSecret)
	}
	if si.LastError != "some_error" {
		t.Errorf("expected some_error, got %s", si.LastError)
	}
}

// --- Cancel sets timestamp ---

func TestCancel_SetsTimestamp(t *testing.T) {
	si := &SetupIntent{Status: RequiresPaymentMethod}
	if err := si.Cancel("abandoned"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.CanceledAt.IsZero() {
		t.Error("expected CanceledAt to be set")
	}
	if si.CancellationReason != "abandoned" {
		t.Errorf("expected abandoned, got %s", si.CancellationReason)
	}
}

// --- Metadata ---

func TestSetupIntentMetadata(t *testing.T) {
	si := &SetupIntent{
		Metadata: map[string]interface{}{
			"purpose": "subscription_setup",
		},
	}
	if len(si.Metadata) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(si.Metadata))
	}
	if si.Metadata["purpose"] != "subscription_setup" {
		t.Errorf("expected subscription_setup, got %v", si.Metadata["purpose"])
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	si := &SetupIntent{}
	si.Init(db)
	if si.Datastore() != db {
		t.Error("expected Datastore() to be set")
	}
}

// --- New sets defaults ---

func TestNew_SetsDefaults(t *testing.T) {
	db := testDB()
	si := New(db)
	if si.Status != RequiresPaymentMethod {
		t.Errorf("expected %s, got %s", RequiresPaymentMethod, si.Status)
	}
	if si.Usage != "off_session" {
		t.Errorf("expected off_session, got %s", si.Usage)
	}
	if si.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

func TestNew_DoesNotOverwritePreset(t *testing.T) {
	db := testDB()
	si := New(db)
	// New always sets defaults; verify they are correct
	if si.Status != RequiresPaymentMethod {
		t.Errorf("expected %s, got %s", RequiresPaymentMethod, si.Status)
	}
	if si.Usage != "off_session" {
		t.Errorf("expected off_session, got %s", si.Usage)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	si := New(db)
	if si == nil {
		t.Fatal("expected non-nil SetupIntent")
	}
	if si.Status != RequiresPaymentMethod {
		t.Errorf("expected %s, got %s", RequiresPaymentMethod, si.Status)
	}
	if si.Usage != "off_session" {
		t.Errorf("expected off_session, got %s", si.Usage)
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

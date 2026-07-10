package dispute

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// Dispute has no lifecycle methods. Tests cover struct initialization,
// field assignment, status constants, and evidence struct.

// --- Status constants ---

func TestStatusConstants(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{WarningNeedsResponse, "warning_needs_response"},
		{NeedsResponse, "needs_response"},
		{UnderReview, "under_review"},
		{Won, "won"},
		{Lost, "lost"},
		{WarningUnderReview, "warning_under_review"},
		{WarningClosed, "warning_closed"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Errorf("status %q != %q", tc.status, tc.want)
		}
	}
}

// --- Struct zero values ---

func TestDisputeZeroValue(t *testing.T) {
	d := &Dispute{}
	if d.Amount != 0 {
		t.Errorf("expected zero amount, got %d", d.Amount)
	}
	if d.Status != "" {
		t.Errorf("expected empty status, got %s", d.Status)
	}
	if d.Evidence != nil {
		t.Error("expected nil evidence")
	}
	if d.Metadata != nil {
		t.Error("expected nil metadata")
	}
	if !d.EvidenceDueBy.IsZero() {
		t.Error("expected zero EvidenceDueBy")
	}
	if !d.Created.IsZero() {
		t.Error("expected zero Created")
	}
}

// --- Field assignment ---

func TestDisputeFieldAssignment(t *testing.T) {
	now := time.Now()
	due := now.Add(7 * 24 * time.Hour)

	d := &Dispute{
		Amount:          10000,
		Currency:        "usd",
		Status:          NeedsResponse,
		ProviderRef:     "dp_abc",
		Reason:          "fraudulent",
		EvidenceDueBy:   due,
		PaymentIntentId: "pi_123",
		Created:         now,
		Metadata:        map[string]interface{}{"key": "val"},
	}

	if d.Amount != 10000 {
		t.Errorf("expected 10000, got %d", d.Amount)
	}
	if string(d.Currency) != "usd" {
		t.Errorf("expected usd, got %s", d.Currency)
	}
	if d.Status != NeedsResponse {
		t.Errorf("expected %s, got %s", NeedsResponse, d.Status)
	}
	if d.ProviderRef != "dp_abc" {
		t.Errorf("expected dp_abc, got %s", d.ProviderRef)
	}
	if d.Reason != "fraudulent" {
		t.Errorf("expected fraudulent, got %s", d.Reason)
	}
	if d.PaymentIntentId != "pi_123" {
		t.Errorf("expected pi_123, got %s", d.PaymentIntentId)
	}
	if d.EvidenceDueBy != due {
		t.Errorf("expected due time %v, got %v", due, d.EvidenceDueBy)
	}
	if d.Created != now {
		t.Errorf("expected created time %v, got %v", now, d.Created)
	}
}

// --- DisputeEvidence ---

func TestDisputeEvidenceFields(t *testing.T) {
	e := &DisputeEvidence{
		CustomerName:         "Jane Doe",
		CustomerEmailAddress: "jane@example.com",
		ProductDescription:   "Widget Pro",
		ServiceDate:          "2026-01-15",
		UncategorizedText:    "Customer confirmed receipt",
	}
	if e.CustomerName != "Jane Doe" {
		t.Errorf("expected 'Jane Doe', got %q", e.CustomerName)
	}
	if e.CustomerEmailAddress != "jane@example.com" {
		t.Errorf("expected 'jane@example.com', got %q", e.CustomerEmailAddress)
	}
	if e.ProductDescription != "Widget Pro" {
		t.Errorf("expected 'Widget Pro', got %q", e.ProductDescription)
	}
	if e.ServiceDate != "2026-01-15" {
		t.Errorf("expected '2026-01-15', got %q", e.ServiceDate)
	}
	if e.UncategorizedText != "Customer confirmed receipt" {
		t.Errorf("expected 'Customer confirmed receipt', got %q", e.UncategorizedText)
	}
}

func TestDisputeWithEvidence(t *testing.T) {
	d := &Dispute{
		Status: NeedsResponse,
		Evidence: &DisputeEvidence{
			CustomerName: "Test User",
		},
	}
	if d.Evidence == nil {
		t.Fatal("expected non-nil evidence")
	}
	if d.Evidence.CustomerName != "Test User" {
		t.Errorf("expected 'Test User', got %q", d.Evidence.CustomerName)
	}
}

// --- Status transitions (manual, no methods) ---

func TestDisputeStatusTransition_NeedsResponseToUnderReview(t *testing.T) {
	d := &Dispute{Status: NeedsResponse}
	d.Status = UnderReview
	if d.Status != UnderReview {
		t.Errorf("expected %s, got %s", UnderReview, d.Status)
	}
}

func TestDisputeStatusTransition_UnderReviewToWon(t *testing.T) {
	d := &Dispute{Status: UnderReview}
	d.Status = Won
	if d.Status != Won {
		t.Errorf("expected %s, got %s", Won, d.Status)
	}
}

func TestDisputeStatusTransition_UnderReviewToLost(t *testing.T) {
	d := &Dispute{Status: UnderReview}
	d.Status = Lost
	if d.Status != Lost {
		t.Errorf("expected %s, got %s", Lost, d.Status)
	}
}

// --- Warning status transitions ---

func TestDisputeStatusTransition_WarningNeedsResponseToWarningUnderReview(t *testing.T) {
	d := &Dispute{Status: WarningNeedsResponse}
	d.Status = WarningUnderReview
	if d.Status != WarningUnderReview {
		t.Errorf("expected %s, got %s", WarningUnderReview, d.Status)
	}
}

func TestDisputeStatusTransition_WarningUnderReviewToWarningClosed(t *testing.T) {
	d := &Dispute{Status: WarningUnderReview}
	d.Status = WarningClosed
	if d.Status != WarningClosed {
		t.Errorf("expected %s, got %s", WarningClosed, d.Status)
	}
}

func TestDisputeStatusTransition_WarningNeedsResponseToWarningClosed(t *testing.T) {
	d := &Dispute{Status: WarningNeedsResponse}
	d.Status = WarningClosed
	if d.Status != WarningClosed {
		t.Errorf("expected %s, got %s", WarningClosed, d.Status)
	}
}

// --- Metadata ---

func TestDisputeMetadata(t *testing.T) {
	d := &Dispute{
		Status: NeedsResponse,
		Metadata: map[string]interface{}{
			"order_id":   "ord_123",
			"ip_address": "192.168.1.1",
			"attempts":   float64(3),
		},
	}
	if len(d.Metadata) != 3 {
		t.Fatalf("expected 3 metadata entries, got %d", len(d.Metadata))
	}
	if d.Metadata["order_id"] != "ord_123" {
		t.Errorf("expected ord_123, got %v", d.Metadata["order_id"])
	}
}

func TestDisputeMetadata_Empty(t *testing.T) {
	d := &Dispute{
		Status:   NeedsResponse,
		Metadata: map[string]interface{}{},
	}
	if d.Metadata == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(d.Metadata) != 0 {
		t.Errorf("expected 0 entries, got %d", len(d.Metadata))
	}
}

// --- Currency ---

func TestDisputeCurrency(t *testing.T) {
	currencies := []string{"usd", "eur", "gbp", "jpy"}
	for _, cur := range currencies {
		d := &Dispute{
			Amount:   1000,
			Currency: currency.Type(cur),
		}
		if string(d.Currency) != cur {
			t.Errorf("expected %s, got %s", cur, d.Currency)
		}
	}
}

// --- Evidence partial fill ---

func TestDisputeEvidencePartialFields(t *testing.T) {
	e := &DisputeEvidence{
		CustomerName: "Partial User",
	}
	if e.CustomerEmailAddress != "" {
		t.Errorf("expected empty email, got %q", e.CustomerEmailAddress)
	}
	if e.ProductDescription != "" {
		t.Errorf("expected empty product, got %q", e.ProductDescription)
	}
	if e.ServiceDate != "" {
		t.Errorf("expected empty date, got %q", e.ServiceDate)
	}
	if e.UncategorizedText != "" {
		t.Errorf("expected empty text, got %q", e.UncategorizedText)
	}
}

func TestDisputeEvidenceZeroValue(t *testing.T) {
	e := &DisputeEvidence{}
	if e.CustomerName != "" {
		t.Errorf("expected empty, got %q", e.CustomerName)
	}
}

// --- Multiple disputes ---

func TestMultipleDisputesIndependent(t *testing.T) {
	d1 := &Dispute{Status: NeedsResponse, Amount: 5000}
	d2 := &Dispute{Status: Won, Amount: 3000}
	d1.Status = UnderReview
	if d2.Status != Won {
		t.Errorf("changing d1 should not affect d2, got %s", d2.Status)
	}
}

// --- Kind ---

func TestKind(t *testing.T) {
	d := &Dispute{}
	if d.Kind() != "dispute" {
		t.Errorf("expected 'dispute', got %q", d.Kind())
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	d := &Dispute{}
	d.Init(db)
	if d.Datastore() != db {
		t.Error("expected Datastore to be set")
	}
}

// --- Defaults ---

func TestDefaults(t *testing.T) {
	db := testDB()
	d := &Dispute{}
	d.Init(db)
	d.Defaults()
	if d.Status != NeedsResponse {
		t.Errorf("expected %s, got %s", NeedsResponse, d.Status)
	}
	if d.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

func TestDefaults_DoesNotOverwrite(t *testing.T) {
	db := testDB()
	d := &Dispute{}
	d.Init(db)
	d.Status = Won
	d.Defaults()
	if d.Status != Won {
		t.Errorf("expected %s, got %s", Won, d.Status)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	d := New(db)
	if d == nil {
		t.Fatal("expected non-nil Dispute")
	}
	if d.Status != NeedsResponse {
		t.Errorf("expected %s, got %s", NeedsResponse, d.Status)
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

// --- All statuses unique ---

func TestAllStatusesUnique(t *testing.T) {
	statuses := []Status{
		WarningNeedsResponse,
		NeedsResponse,
		UnderReview,
		Won,
		Lost,
		WarningUnderReview,
		WarningClosed,
	}
	seen := make(map[Status]bool)
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate status: %s", s)
		}
		seen[s] = true
	}
	if len(seen) != 7 {
		t.Errorf("expected 7 unique statuses, got %d", len(seen))
	}
}

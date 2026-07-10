package subscriptionschedule

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore {
	return datastore.New(context.Background())
}

// --- Release ---

func TestRelease_FromNotStarted(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	if err := s.Release(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != Released {
		t.Errorf("expected %s, got %s", Released, s.Status)
	}
}

func TestRelease_FromActive(t *testing.T) {
	s := &SubscriptionSchedule{Status: Active}
	if err := s.Release(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != Released {
		t.Errorf("expected %s, got %s", Released, s.Status)
	}
}

func TestRelease_InvalidStatus_Completed(t *testing.T) {
	s := &SubscriptionSchedule{Status: Completed}
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing from Completed")
	}
}

func TestRelease_InvalidStatus_Released(t *testing.T) {
	s := &SubscriptionSchedule{Status: Released}
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing already-released schedule")
	}
}

func TestRelease_InvalidStatus_Canceled(t *testing.T) {
	s := &SubscriptionSchedule{Status: SSCanceled}
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing canceled schedule")
	}
}

// --- Cancel ---

func TestCancel_FromNotStarted(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	if err := s.Cancel(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != SSCanceled {
		t.Errorf("expected %s, got %s", SSCanceled, s.Status)
	}
}

func TestCancel_FromActive(t *testing.T) {
	s := &SubscriptionSchedule{Status: Active}
	if err := s.Cancel(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != SSCanceled {
		t.Errorf("expected %s, got %s", SSCanceled, s.Status)
	}
}

func TestCancel_InvalidStatus_Completed(t *testing.T) {
	s := &SubscriptionSchedule{Status: Completed}
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling completed schedule")
	}
}

func TestCancel_InvalidStatus_Released(t *testing.T) {
	s := &SubscriptionSchedule{Status: Released}
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling released schedule")
	}
}

func TestCancel_InvalidStatus_Canceled(t *testing.T) {
	s := &SubscriptionSchedule{Status: SSCanceled}
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling already-canceled schedule")
	}
}

// --- Complete ---

func TestComplete_FromActive(t *testing.T) {
	s := &SubscriptionSchedule{Status: Active}
	s.Complete()
	if s.Status != Completed {
		t.Errorf("expected %s, got %s", Completed, s.Status)
	}
}

func TestComplete_FromNotStarted(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Complete()
	if s.Status != Completed {
		t.Errorf("expected %s, got %s", Completed, s.Status)
	}
}

func TestComplete_Idempotent(t *testing.T) {
	s := &SubscriptionSchedule{Status: Completed}
	s.Complete()
	if s.Status != Completed {
		t.Errorf("expected %s, got %s", Completed, s.Status)
	}
}

// --- Start ---

func TestStart_SetsActiveAndSubscriptionId(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("sub_abc")
	if s.Status != Active {
		t.Errorf("expected %s, got %s", Active, s.Status)
	}
	if s.SubscriptionId != "sub_abc" {
		t.Errorf("expected sub_abc, got %s", s.SubscriptionId)
	}
}

func TestStart_OverwritesSubscriptionId(t *testing.T) {
	s := &SubscriptionSchedule{
		Status:         NotStarted,
		SubscriptionId: "old_sub",
	}
	s.Start("new_sub")
	if s.SubscriptionId != "new_sub" {
		t.Errorf("expected new_sub, got %s", s.SubscriptionId)
	}
}

// --- Status constants ---

func TestStatusConstants(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{NotStarted, "not_started"},
		{Active, "active"},
		{Completed, "completed"},
		{Released, "released"},
		{SSCanceled, "canceled"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Errorf("status %q != %q", tc.status, tc.want)
		}
	}
}

// --- Full lifecycle ---

func TestFullLifecycle_StartThenComplete(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("sub_lifecycle")
	if s.Status != Active {
		t.Fatalf("expected Active after Start, got %s", s.Status)
	}
	s.Complete()
	if s.Status != Completed {
		t.Errorf("expected Completed, got %s", s.Status)
	}
}

func TestFullLifecycle_StartThenCancel(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("sub_cancel")
	if err := s.Cancel(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != SSCanceled {
		t.Errorf("expected %s, got %s", SSCanceled, s.Status)
	}
}

func TestFullLifecycle_StartThenRelease(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("sub_release")
	if err := s.Release(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != Released {
		t.Errorf("expected %s, got %s", Released, s.Status)
	}
}

func TestFullLifecycle_CompletedCannotCancel(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("sub_done")
	s.Complete()
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling completed schedule")
	}
}

func TestFullLifecycle_CompletedCannotRelease(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("sub_done2")
	s.Complete()
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing completed schedule")
	}
}

// --- Kind ---

func TestKind(t *testing.T) {
	s := &SubscriptionSchedule{}
	if s.Kind() != "subscription-schedule" {
		t.Errorf("expected 'subscription-schedule', got %q", s.Kind())
	}
}

// --- Validator ---

func TestValidator_ReturnsNil(t *testing.T) {
	s := &SubscriptionSchedule{}
	if s.Validator() != nil {
		t.Error("expected nil validator")
	}
}

// --- Release from empty status ---

func TestRelease_InvalidStatus_Empty(t *testing.T) {
	s := &SubscriptionSchedule{Status: ""}
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing from empty status")
	}
}

func TestRelease_InvalidStatus_Unknown(t *testing.T) {
	s := &SubscriptionSchedule{Status: Status("suspended")}
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing from unknown status")
	}
}

// --- Cancel from empty/unknown status ---

func TestCancel_InvalidStatus_Empty(t *testing.T) {
	s := &SubscriptionSchedule{Status: ""}
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling from empty status")
	}
}

func TestCancel_InvalidStatus_Unknown(t *testing.T) {
	s := &SubscriptionSchedule{Status: Status("paused")}
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling from unknown status")
	}
}

// --- Complete from all terminal states (idempotent) ---

func TestComplete_FromReleased(t *testing.T) {
	s := &SubscriptionSchedule{Status: Released}
	s.Complete()
	if s.Status != Completed {
		t.Errorf("expected %s, got %s", Completed, s.Status)
	}
}

func TestComplete_FromCanceled(t *testing.T) {
	s := &SubscriptionSchedule{Status: SSCanceled}
	s.Complete()
	if s.Status != Completed {
		t.Errorf("expected %s, got %s", Completed, s.Status)
	}
}

// --- Start sets status regardless of current ---

func TestStart_FromActive(t *testing.T) {
	s := &SubscriptionSchedule{Status: Active, SubscriptionId: "old_sub"}
	s.Start("new_sub")
	if s.Status != Active {
		t.Errorf("expected %s, got %s", Active, s.Status)
	}
	if s.SubscriptionId != "new_sub" {
		t.Errorf("expected new_sub, got %s", s.SubscriptionId)
	}
}

func TestStart_EmptySubscriptionId(t *testing.T) {
	s := &SubscriptionSchedule{Status: NotStarted}
	s.Start("")
	if s.Status != Active {
		t.Errorf("expected %s, got %s", Active, s.Status)
	}
	if s.SubscriptionId != "" {
		t.Errorf("expected empty subscriptionId, got %q", s.SubscriptionId)
	}
}

// --- Phase struct ---

func TestPhaseStruct(t *testing.T) {
	now := time.Now()
	end := now.Add(30 * 24 * time.Hour)
	phase := Phase{
		PlanId:            "plan_abc",
		StartDate:         now,
		EndDate:           end,
		ProrationBehavior: "create_prorations",
		Items: []PhaseItem{
			{PriceId: "price_1", Quantity: 2},
			{PriceId: "price_2", Quantity: 1},
		},
	}
	if phase.PlanId != "plan_abc" {
		t.Errorf("expected plan_abc, got %s", phase.PlanId)
	}
	if len(phase.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(phase.Items))
	}
	if phase.Items[0].PriceId != "price_1" {
		t.Errorf("expected price_1, got %s", phase.Items[0].PriceId)
	}
	if phase.Items[0].Quantity != 2 {
		t.Errorf("expected quantity 2, got %d", phase.Items[0].Quantity)
	}
	if phase.ProrationBehavior != "create_prorations" {
		t.Errorf("expected create_prorations, got %s", phase.ProrationBehavior)
	}
}

func TestPhaseItemZeroValue(t *testing.T) {
	pi := PhaseItem{}
	if pi.PriceId != "" {
		t.Errorf("expected empty, got %q", pi.PriceId)
	}
	if pi.Quantity != 0 {
		t.Errorf("expected 0, got %d", pi.Quantity)
	}
}

// --- SubscriptionSchedule with phases ---

func TestSubscriptionScheduleWithPhases(t *testing.T) {
	now := time.Now()
	s := &SubscriptionSchedule{
		Status:    NotStarted,
		StartDate: now,
		Phases: []Phase{
			{PlanId: "plan_a", StartDate: now, EndDate: now.Add(30 * 24 * time.Hour)},
			{PlanId: "plan_b", StartDate: now.Add(30 * 24 * time.Hour), EndDate: now.Add(60 * 24 * time.Hour)},
		},
	}
	if len(s.Phases) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(s.Phases))
	}
	if s.Phases[0].PlanId != "plan_a" {
		t.Errorf("expected plan_a, got %s", s.Phases[0].PlanId)
	}
}

// --- EndBehavior field ---

func TestEndBehavior(t *testing.T) {
	s := &SubscriptionSchedule{EndBehavior: "cancel"}
	if s.EndBehavior != "cancel" {
		t.Errorf("expected 'cancel', got %q", s.EndBehavior)
	}
}

// --- Terminal states cannot release or cancel (comprehensive) ---

func TestReleasedThenCancel(t *testing.T) {
	s := &SubscriptionSchedule{Status: Active}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	err := s.Cancel()
	if err == nil {
		t.Fatal("expected error canceling released schedule")
	}
}

func TestCanceledThenRelease(t *testing.T) {
	s := &SubscriptionSchedule{Status: Active}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	err := s.Release()
	if err == nil {
		t.Fatal("expected error releasing canceled schedule")
	}
}

// --- Save serializes Phases_ and Metadata_ ---

func TestSave_SerializesPhases(t *testing.T) {
	now := time.Now()
	s := &SubscriptionSchedule{
		Phases: []Phase{
			{PlanId: "plan_a", StartDate: now, EndDate: now.Add(30 * 24 * time.Hour)},
		},
		Metadata: map[string]interface{}{"campaign": "launch"},
	}
	ps, err := s.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if ps == nil {
		t.Fatal("expected non-nil properties")
	}
	if s.Phases_ == "" {
		t.Error("expected Phases_ to be populated after Save")
	}
	if s.Metadata_ == "" {
		t.Error("expected Metadata_ to be populated after Save")
	}
}

func TestSave_NilPhases(t *testing.T) {
	s := &SubscriptionSchedule{}
	_, err := s.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if s.Phases_ == "" {
		t.Error("expected Phases_ to be set")
	}
}

func TestSave_EmptyPhases(t *testing.T) {
	s := &SubscriptionSchedule{
		Phases: []Phase{},
	}
	_, err := s.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if s.Phases_ == "" {
		t.Error("expected Phases_ to be set")
	}
}

func TestSave_NilMetadata(t *testing.T) {
	s := &SubscriptionSchedule{}
	_, err := s.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if s.Metadata_ == "" {
		t.Error("expected Metadata_ to be set")
	}
}

// --- Load deserializes Phases_ and Metadata_ ---

func TestLoad_DeserializesPhases(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	s := &SubscriptionSchedule{
		Phases: []Phase{
			{PlanId: "plan_x", StartDate: now, EndDate: now.Add(30 * 24 * time.Hour),
				Items: []PhaseItem{{PriceId: "price_1", Quantity: 3}}},
		},
		Metadata: map[string]interface{}{"test": "value"},
	}
	_, err := s.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}
	savedPhases := s.Phases_
	savedMeta := s.Metadata_

	s2 := &SubscriptionSchedule{}
	s2.Phases_ = savedPhases
	s2.Metadata_ = savedMeta
	props := []datastore.Property{
		{Name: "Phases_", Value: savedPhases},
		{Name: "Metadata_", Value: savedMeta},
	}
	err = s2.Load(props)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(s2.Phases) != 1 {
		t.Fatalf("expected 1 phase, got %d", len(s2.Phases))
	}
	if s2.Phases[0].PlanId != "plan_x" {
		t.Errorf("expected plan_x, got %s", s2.Phases[0].PlanId)
	}
	if len(s2.Phases[0].Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(s2.Phases[0].Items))
	}
	if s2.Phases[0].Items[0].PriceId != "price_1" {
		t.Errorf("expected price_1, got %s", s2.Phases[0].Items[0].PriceId)
	}
	if s2.Metadata == nil {
		t.Fatal("expected non-nil Metadata")
	}
	if s2.Metadata["test"] != "value" {
		t.Errorf("expected test=value, got %v", s2.Metadata["test"])
	}
}

func TestLoad_EmptyStrings(t *testing.T) {
	s := &SubscriptionSchedule{}
	err := s.Load([]datastore.Property{})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if s.Phases != nil {
		t.Error("expected nil Phases when Phases_ is empty")
	}
	if s.Metadata != nil {
		t.Error("expected nil Metadata when Metadata_ is empty")
	}
}

// --- Save/Load round trip ---

func TestSaveLoadRoundTrip(t *testing.T) {
	s := &SubscriptionSchedule{
		CustomerId:  "cus_rt",
		Status:      Active,
		EndBehavior: "cancel",
		Phases: []Phase{
			{PlanId: "plan_rt", StartDate: time.Now().Truncate(time.Second)},
		},
		Metadata: map[string]interface{}{"source": "api"},
	}

	ps, err := s.Save()
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	s2 := &SubscriptionSchedule{}
	err = s2.Load(ps)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if s2.CustomerId != "cus_rt" {
		t.Errorf("expected cus_rt, got %s", s2.CustomerId)
	}
	if s2.EndBehavior != "cancel" {
		t.Errorf("expected cancel, got %s", s2.EndBehavior)
	}
}

// --- SubscriptionSchedule zero value ---

func TestSubscriptionScheduleZeroValue(t *testing.T) {
	s := &SubscriptionSchedule{}
	if s.CustomerId != "" {
		t.Errorf("expected empty, got %q", s.CustomerId)
	}
	if s.SubscriptionId != "" {
		t.Errorf("expected empty, got %q", s.SubscriptionId)
	}
	if s.Status != "" {
		t.Errorf("expected empty, got %s", s.Status)
	}
	if s.EndBehavior != "" {
		t.Errorf("expected empty, got %q", s.EndBehavior)
	}
	if s.Phases != nil {
		t.Error("expected nil phases")
	}
	if s.Metadata != nil {
		t.Error("expected nil metadata")
	}
	if !s.StartDate.IsZero() {
		t.Error("expected zero StartDate")
	}
}

// --- Phase TrialEnd ---

func TestPhaseTrialEnd(t *testing.T) {
	now := time.Now()
	trial := now.Add(14 * 24 * time.Hour)
	phase := Phase{
		PlanId:   "plan_trial",
		TrialEnd: trial,
	}
	if phase.TrialEnd.IsZero() {
		t.Error("expected non-zero TrialEnd")
	}
}

// --- Phase ProrationBehavior ---

func TestPhaseProrationBehavior(t *testing.T) {
	cases := []string{"create_prorations", "none"}
	for _, pb := range cases {
		phase := Phase{ProrationBehavior: pb}
		if phase.ProrationBehavior != pb {
			t.Errorf("expected %s, got %s", pb, phase.ProrationBehavior)
		}
	}
}

// --- Load error paths ---

func TestLoad_LoadStructError(t *testing.T) {
	s := &SubscriptionSchedule{}
	props := []datastore.Property{
		{Name: "bad", Value: func() {}},
	}
	err := s.Load(props)
	if err == nil {
		t.Fatal("expected error from LoadStruct with unmarshalable property")
	}
}

func TestLoad_InvalidPhasesJSON(t *testing.T) {
	s := &SubscriptionSchedule{}
	s.Phases_ = "not-valid-json"
	err := s.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Phases_ JSON")
	}
}

func TestLoad_InvalidMetadataJSON(t *testing.T) {
	s := &SubscriptionSchedule{}
	s.Metadata_ = "not-valid-json"
	// Phases_ is empty so it skips that, but Metadata_ is invalid
	err := s.Load([]datastore.Property{})
	if err == nil {
		t.Fatal("expected error for invalid Metadata_ JSON")
	}
}

// --- Init ---

func TestInit(t *testing.T) {
	db := testDB()
	s := &SubscriptionSchedule{}
	s.Init(db)
	if s.Datastore() != db {
		t.Error("expected Datastore() to be set")
	}
}

// --- New sets defaults ---

func TestNew_SetsDefaults(t *testing.T) {
	db := testDB()
	s := New(db)
	if s.Status != NotStarted {
		t.Errorf("expected %s, got %s", NotStarted, s.Status)
	}
	if s.EndBehavior != "release" {
		t.Errorf("expected release, got %s", s.EndBehavior)
	}
	if s.Parent == nil {
		t.Error("expected Parent to be set")
	}
}

// --- New ---

func TestNew(t *testing.T) {
	db := testDB()
	s := New(db)
	if s == nil {
		t.Fatal("expected non-nil SubscriptionSchedule")
	}
	if s.Status != NotStarted {
		t.Errorf("expected %s, got %s", NotStarted, s.Status)
	}
	if s.EndBehavior != "release" {
		t.Errorf("expected release, got %s", s.EndBehavior)
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

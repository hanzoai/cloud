package o11y

import (
	"encoding/json"
	"testing"
	"time"
)

var probedAt = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// A fleet that is entirely up carries no incidents — which is what makes the
// client render "All systems operational". The maintenance lists must still be
// ARRAYS: the panel calls .filter on both, so a null there is a TypeError, not an
// empty state.
func TestSummaryAllUpHasNoIncidents(t *testing.T) {
	s := buildSummary("hanzo", []serviceUp{
		{Name: "iam", Up: true}, {Name: "kms", Up: true},
	}, probedAt)

	if len(s.OngoingIncidents) != 0 {
		t.Errorf("all services up: got %d incidents, want 0", len(s.OngoingIncidents))
	}
	if s.InProgressMaintenances == nil || s.ScheduledMaintenances == nil {
		t.Error("maintenance lists must be empty arrays, never null — the client filters them")
	}
	if s.CheckedAt != "2026-07-31T12:00:00Z" {
		t.Errorf("checkedAt = %q, want the probe time", s.CheckedAt)
	}
}

// One down service is one incident naming it, and the platform impact is
// partial_outage — some of the platform is out, not all of it. The component's
// OWN status is full_outage, because that service is entirely unavailable: the
// two fields answer different questions and must not be collapsed.
func TestSummaryDownServiceBecomesIncident(t *testing.T) {
	s := buildSummary("hanzo", []serviceUp{
		{Name: "iam", Up: true}, {Name: "kms", Up: false}, {Name: "s3", Up: true},
	}, probedAt)

	if len(s.OngoingIncidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(s.OngoingIncidents))
	}
	in := s.OngoingIncidents[0]
	if in.CurrentWorstImpact != "partial_outage" {
		t.Errorf("impact = %q, want partial_outage", in.CurrentWorstImpact)
	}
	if in.ID != "service-kms" {
		t.Errorf("id = %q, want a stable id derived from the service", in.ID)
	}
	if len(in.AffectedComponents) != 1 || in.AffectedComponents[0].Name != "kms" {
		t.Fatalf("affected components = %+v, want the one down service", in.AffectedComponents)
	}
	if got := in.AffectedComponents[0].CurrentStatus; got != "full_outage" {
		t.Errorf("component status = %q, want full_outage — that service is entirely out", got)
	}
	if in.LastUpdateAt != "2026-07-31T12:00:00Z" {
		t.Errorf("lastUpdateAt = %q, want the measurement time", in.LastUpdateAt)
	}
}

// Every service down is a full outage. The distinction is COUNTED, never read
// from a table of which services are allowed to matter.
func TestSummaryEverythingDownIsFullOutage(t *testing.T) {
	s := buildSummary("hanzo", []serviceUp{
		{Name: "iam", Up: false}, {Name: "kms", Up: false},
	}, probedAt)

	if len(s.OngoingIncidents) != 2 {
		t.Fatalf("got %d incidents, want one per down service", len(s.OngoingIncidents))
	}
	for _, in := range s.OngoingIncidents {
		if in.CurrentWorstImpact != "full_outage" {
			t.Errorf("%s: impact = %q, want full_outage", in.Name, in.CurrentWorstImpact)
		}
	}
	// Stable order, so a polling client does not see the list shuffle every 5
	// minutes for reasons that are not the platform changing.
	if s.OngoingIncidents[0].ID != "service-iam" || s.OngoingIncidents[1].ID != "service-kms" {
		t.Errorf("incidents are not name-ordered: %q then %q",
			s.OngoingIncidents[0].ID, s.OngoingIncidents[1].ID)
	}
}

// The document white-labels: a lux caller is never handed Hanzo's status page,
// and every human-facing link points at the brand's own HTML status host — never
// at this JSON endpoint.
func TestSummaryWhiteLabelsTheStatusPage(t *testing.T) {
	for brand, want := range map[string]string{
		"hanzo": "https://status.hanzo.ai",
		"lux":   "https://status.lux.network",
		"zoo":   "https://status.zoo.ngo",
	} {
		s := buildSummary(brand, []serviceUp{{Name: "iam", Up: false}}, probedAt)
		if s.PageURL != want {
			t.Errorf("%s: page_url = %q, want %q", brand, s.PageURL, want)
		}
		if got := s.OngoingIncidents[0].URL; got != want {
			t.Errorf("%s: incident url = %q, want the human status page %q", brand, got, want)
		}
	}
	if got := buildSummary("hanzo", nil, probedAt).PageTitle; got != "Hanzo status" {
		t.Errorf("page_title = %q, want %q", got, "Hanzo status")
	}
}

// The wire keys are the contract the insights side panel parses. A rename here
// is a silent break there, so they are pinned.
func TestSummaryWireKeys(t *testing.T) {
	raw, err := json.Marshal(buildSummary("hanzo", []serviceUp{{Name: "kms", Up: false}}, probedAt))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"page_title", "page_url", "ongoing_incidents",
		"in_progress_maintenances", "scheduled_maintenances"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("document is missing %q — the side panel reads it", k)
		}
	}

	var incidents []map[string]json.RawMessage
	if err := json.Unmarshal(doc["ongoing_incidents"], &incidents); err != nil {
		t.Fatalf("ongoing_incidents: %v", err)
	}
	for _, k := range []string{"id", "name", "status", "url", "last_update_at",
		"last_update_message", "current_worst_impact", "affected_components"} {
		if _, ok := incidents[0][k]; !ok {
			t.Errorf("incident is missing %q — the side panel reads it", k)
		}
	}
}

// The client's status vocabulary is a closed set of three; sending a fourth word
// leaves the panel rendering nothing it understands.
func TestSummaryIncidentStatusIsInTheClientVocabulary(t *testing.T) {
	allowed := map[string]bool{"investigating": true, "identified": true, "monitoring": true}
	for _, in := range buildSummary("hanzo", []serviceUp{{Name: "kms", Up: false}}, probedAt).OngoingIncidents {
		if !allowed[in.Status] {
			t.Errorf("incident status %q is outside the client's closed set", in.Status)
		}
	}
}

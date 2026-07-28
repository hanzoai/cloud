package experiments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud"
)

func sampleExp(id string) Experiment {
	return Experiment{
		Project: "", ID: id, Name: id, SubjectKind: SubjectUser,
		FlagKey: "exp_" + id, ExposureEvent: defaultExposureEvent, MetricEvent: "order_completed",
		Variants: []Variant{
			{Key: "control", Control: true, Weight: 50, Payload: json.RawMessage(`{"on":false}`)},
			{Key: "treatment", Weight: 50, Payload: json.RawMessage(`{"on":true}`)},
		},
		Status: StatusRunning, CreatedBy: "z@hanzo.ai", CreatedAt: "2026-01-01T00:00:00Z",
	}
}

func TestStore_CreateGetListDecide(t *testing.T) {
	stores := cloud.NewOrgStore[*store](t.TempDir(), "experiments", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	st, err := stores.For("acme", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()

	if err := st.create(ctx, sampleExp("checkout")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A second create of the same (project,id) must fail — an id is claimed once.
	if err := st.create(ctx, sampleExp("checkout")); err == nil {
		t.Fatalf("duplicate create must fail")
	}

	got, found, err := st.get(ctx, "", "checkout")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if got.FlagKey != "exp_checkout" || len(got.Variants) != 2 || !got.Variants[0].Control {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
	if string(got.Variants[1].Payload) != `{"on":true}` {
		t.Fatalf("variant payload lost: %s", got.Variants[1].Payload)
	}

	list, err := st.list(ctx, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list))
	}

	if err := st.decide(ctx, "", "checkout", "treatment", "z@hanzo.ai", "2026-01-02T00:00:00Z"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	got, _, _ = st.get(ctx, "", "checkout")
	if got.Status != StatusDecided || got.Winner != "treatment" || got.DecidedBy != "z@hanzo.ai" {
		t.Fatalf("decide not recorded: %+v", got)
	}
}

// TestStore_TenantIsolation proves the physical org boundary: an experiment created
// in one org is invisible to another org's store (a distinct SQLite file), so no
// query can cross the tenant boundary.
func TestStore_TenantIsolation(t *testing.T) {
	stores := cloud.NewOrgStore[*store](t.TempDir(), "experiments", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	ctx := context.Background()

	acme, err := stores.For("acme", "")
	if err != nil {
		t.Fatalf("acme: %v", err)
	}
	globex, err := stores.For("globex", "")
	if err != nil {
		t.Fatalf("globex: %v", err)
	}
	if err := acme.create(ctx, sampleExp("secret")); err != nil {
		t.Fatalf("create in acme: %v", err)
	}

	if _, found, _ := globex.get(ctx, "", "secret"); found {
		t.Fatalf("CROSS-TENANT LEAK: globex read acme's experiment")
	}
	if l, _ := globex.list(ctx, ""); len(l) != 0 {
		t.Fatalf("CROSS-TENANT LEAK: globex listed %d acme rows", len(l))
	}
	// decide against a foreign org's experiment finds nothing (fail-closed).
	if err := globex.decide(ctx, "", "secret", "treatment", "attacker", "2026-01-02T00:00:00Z"); err == nil {
		t.Fatalf("globex must not be able to decide acme's experiment")
	}
}

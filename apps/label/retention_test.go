package label

// retention_test.go covers the collision retention and litigation hold were always
// going to have, and the hole it opened in the answer key.
//
// THE SHAPE. dispose identifies expired records (hold = 0), sweeps them from the
// derived columnar copy FIRST — deliberately, so nothing is orphaned in a warehouse
// that no id can name any more — and then deletes them from the record, re-asserting
// `hold = 0` in the WHERE clause so a hold that arrived in between still wins.
//
// The re-assertion protected the record and silently corrupted the copy. A record
// the delete declines to remove has already been swept; its seq is far behind the
// delivery cursor, and deliver() asks the cursor rather than the world, so no retry
// re-sends it and pending() answers zero. The row was then present in the tenant's
// compliance record, permanently absent from the answer key a training join reads,
// and the row it happens to is the one somebody is litigating. A missing fraud label
// reads as an honest customer.
//
// The collision is a race in production and DETERMINISTIC here, because the columnar
// plane is a value on the state: the test places the hold from inside sweep, which is
// exactly the window, with no sleep and no goroutine.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/tenant"
)

// errWarehouseDown stands in for the derived copy being unreachable.
var errWarehouseDown = errors.New("the warehouse is not connected")

// TestAHoldPlacedDuringASweepKeepsTheRecordInBothPlanes is the property.
func TestAHoldPlacedDuringASweepKeepsTheRecordInBothPlanes(t *testing.T) {
	var (
		st       *store
		swept    []string
		restored [][]Fact
	)
	// The seam: sweep places a litigation hold on everything it was just asked to
	// remove, which is the exact interleaving a concurrent hold op produces.
	c := columnar{
		send: func(_ context.Context, _ tenant.Key, f []Fact) error {
			restored = append(restored, f)
			return nil
		},
		sweep: func(ctx context.Context, _ tenant.Key, ids []string) error {
			swept = append(swept, ids...)
			if _, _, err := st.setHold(ctx, ids, true); err != nil {
				return err
			}
			return nil
		},
	}
	app, s := wireWith(t, "", c)
	st = storeOf(t, s, "acme")

	// Two assertions written six years ago: old enough for the five-year floor.
	old := time.Now().UTC().Add(-6 * 365 * 24 * time.Hour).Truncate(time.Second)
	a := asserted(t, st, "tx-old-1", old, old, Productive, Dispute, "dp_1", 1)
	b := asserted(t, st, "tx-old-2", old, old, Unproductive, Review, "rv_1", 0.4)

	before := time.Now().UTC().Add(-5*365*24*time.Hour - 24*time.Hour).Format(time.RFC3339)
	code, raw := req(t, app, http.MethodPost, "/v1/label/dispose", "acme", "u_acme",
		`{"before":"`+before+`"}`)
	if code != http.StatusOK {
		t.Fatalf("dispose = %d %s", code, raw)
	}
	var out riskDisposeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	if len(swept) != 2 {
		t.Fatalf("the sweep named %d records in the derived copy, want 2 — the seam did not fire", len(swept))
	}

	// 1. THE RECORD IS KEPT. A hold beats retention, which is the property the
	//    re-assertion in the WHERE clause already had.
	left, err := st.facts(t.Context(), query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Fatalf("the plane holds %d records after the sweep, want both — a hold must beat retention", len(left))
	}

	// 2. THE COUNT IS HONEST. `disposed` is what was disposed of, not what was
	//    identified. A compliance report that says it deleted a record it is still
	//    holding is the wrong answer to the only question the report is asked.
	if out.Disposed != 0 {
		t.Errorf("disposed = %d, want 0 — nothing was deleted, every record was held mid-sweep", out.Disposed)
	}
	if out.Restored != 2 {
		t.Errorf("restored = %d, want 2 — the state must be NAMED, not repaired in silence", out.Restored)
	}
	if out.Total != 2 {
		t.Errorf("total = %d, want 2", out.Total)
	}

	// 3. THE DERIVED COPY IS WHOLE AGAIN. This is the half that was missing: the
	//    swept rows are written back, by id, from the record that still holds them.
	var back []Fact
	for _, batch := range restored {
		back = append(back, batch...)
	}
	got := map[string]bool{}
	for _, f := range back {
		got[f.ID] = true
	}
	for _, want := range []Fact{a, b} {
		if !got[want.ID] {
			t.Errorf("the derived copy was swept of %s (%s) and it was never written back — the record "+
				"survives, the answer key does not, pending() reports zero, and a missing fraud label "+
				"reads as an honest customer", want.ID, want.Subject)
		}
	}
}

// TestARepairTheDerivedCopyRefusesIsNotAcknowledged. The repair is not best effort.
// A sweep that kept a record, could not put it back in the copy, and answered 200
// would have told a tenant its litigation hold held while the answer key it trains
// on had quietly lost the row — which is the same silent hole, one retry later.
func TestARepairTheDerivedCopyRefusesIsNotAcknowledged(t *testing.T) {
	var st *store
	c := columnar{
		send: func(context.Context, tenant.Key, []Fact) error {
			return errWarehouseDown
		},
		sweep: func(ctx context.Context, _ tenant.Key, ids []string) error {
			_, _, err := st.setHold(ctx, ids, true)
			return err
		},
	}
	app, s := wireWith(t, "", c)
	st = storeOf(t, s, "acme")

	old := time.Now().UTC().Add(-6 * 365 * 24 * time.Hour).Truncate(time.Second)
	asserted(t, st, "tx-old-1", old, old, Productive, Dispute, "dp_1", 1)

	before := time.Now().UTC().Add(-5*365*24*time.Hour - 24*time.Hour).Format(time.RFC3339)
	code, raw := req(t, app, http.MethodPost, "/v1/label/dispose", "acme", "u_acme",
		`{"before":"`+before+`"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("dispose = %d %s, want 503 — a copy that is short must not be acknowledged as a clean sweep", code, raw)
	}
	// And the record is still there, so the retry the 503 asks for is safe.
	left, err := st.facts(t.Context(), query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("the plane holds %d records, want the held one — a refused repair must not delete anything", len(left))
	}
}

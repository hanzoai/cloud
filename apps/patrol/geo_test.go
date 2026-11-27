package patrol

import (
	"math"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
)

// TestHaversine measures the distance function against two known separations: a
// degree of latitude anywhere is about 111 km, and a point is no distance from
// itself.
func TestHaversine(t *testing.T) {
	if d := haversine(51.8985, -8.4756, 51.8985, -8.4756); d != 0 {
		t.Fatalf("a point is %v km from itself, want 0", d)
	}
	if d := haversine(51.0, -8.0, 52.0, -8.0); math.Abs(d-111.19) > 0.2 {
		t.Fatalf("one degree of latitude is %.2f km, want about 111.19", d)
	}
	// Cork to Dublin, about 220 km.
	if d := haversine(51.8985, -8.4756, 53.3498, -6.2603); math.Abs(d-220) > 10 {
		t.Fatalf("Cork to Dublin is %.1f km, want about 220", d)
	}
}

// TestNearestPutsTheFreeUnitFirst runs three units at a site: the closest one is
// busy, so the nearest free unit answers first even though it is further away.
// A unit with no position is left out.
func TestNearestPutsTheFreeUnitFirst(t *testing.T) {
	const lat, lon = 51.8985, -8.4756
	units := []framework.Document{
		unitAt("gf-01", UnitOnsite, 51.8990, -8.4760),    // closest, busy
		unitAt("gf-02", UnitAvailable, 51.9200, -8.5000), // further, free
		unitAt("gf-03", UnitAvailable, 52.2000, -8.9000), // furthest, free
		unitAt("gf-04", UnitAvailable, 0, 0),             // never reported
	}
	got := nearest(units, lat, lon, 3)
	if len(got) != 3 {
		t.Fatalf("nearest returned %d units, want 3 (the one with no position is left out)", len(got))
	}
	want := []string{"gf-02", "gf-03", "gf-01"}
	for i, w := range want {
		if got[i].unit != w {
			t.Fatalf("position %d is %s, want %s — free units first, then by distance: %+v", i, got[i].unit, w, got)
		}
	}
	if got[0].km <= 0 || got[0].km > got[1].km {
		t.Fatalf("the free units must be ordered by distance: %+v", got)
	}
	if got[0].eta <= 0 {
		t.Fatalf("a unit at %.2f km has an eta of %d minutes", got[0].km, got[0].eta)
	}
	// A limit smaller than the fleet is honoured.
	if n := len(nearest(units, lat, lon, 1)); n != 1 {
		t.Fatalf("limit 1 returned %d units", n)
	}
}

// unitAt builds one unit document at a position.
func unitAt(sign, state string, lat, lon float64) framework.Document {
	return framework.Document{
		Name:    sign,
		DocType: DTUnit,
		Data:    map[string]any{"sign": sign, "state": state, "lat": lat, "lon": lon},
	}
}

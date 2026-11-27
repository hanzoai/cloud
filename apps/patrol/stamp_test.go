package patrol

import (
	"net/http"
	"strings"
	"testing"
)

// stamp_test.go holds the record to the server's own clock. When a unit was
// where it says it was, and when an activation fired, are what an insurer and a
// court read off this product, and every SLA clock runs from them — so the
// server stamps them and a caller cannot.

// TestFixTimeIsTheServersOwn proves a position report cannot be dated by the
// caller. The trail is the proof-of-presence record an operation is paid for and
// sued over; a guard who chooses `at` writes where they were an hour ago, and a
// value that is not a time at all corrupts the field for every reader of it.
func TestFixTimeIsTheServersOwn(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	for _, forged := range []string{"1999-01-01 00:00:00", "not a time at all"} {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/fix", org, officer,
			map[string]any{"lat": 51.95, "lon": -8.50, "at": forged}); code != http.StatusBadRequest {
			t.Errorf("a fix dated %q want 400, got %d (%s)", forged, code, raw)
		}
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list units want 200, got %d (%s)", code, raw)
	}
	var units []patrolUnit
	data(t, raw, &units)
	for _, u := range units {
		if u.Seen == "1999-01-01 00:00:00" || u.Seen == "not a time at all" {
			t.Errorf("a caller chose the unit's last-seen stamp: %q", u.Seen)
		}
		for _, f := range u.Trail {
			if strings.HasPrefix(f.At, "1999") || f.At == "not a time at all" {
				t.Errorf("a caller dated their own position on the trail: %+v", f)
			}
		}
	}
}

// TestAlarmTimeIsTheServersOwn proves the same of the intake. When an activation
// fired is the one fact an insurer and a court read off it, and the SLA clocks
// on the incident run from it. The engine refuses a value that is not a
// timestamp; it has no opinion about one that is a lie.
func TestAlarmTimeIsTheServersOwn(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	const backdated = "1999-01-01 00:00:00"
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-1", "account": "1001", "trigger": "Intruder", "at": backdated,
	}); code != http.StatusCreated {
		t.Fatalf("seed the activation want 201, got %d (%s)", code, raw)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTAlarm.String(), org, boss, nil)
	if code != http.StatusOK {
		t.Fatalf("read the activations want 200, got %d (%s)", code, raw)
	}
	if strings.Contains(string(raw), backdated) {
		t.Fatalf("a caller dated their own activation %s: %s", backdated, raw)
	}
}

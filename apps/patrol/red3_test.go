package patrol

import (
	"net/http"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// red3_test.go — the intake's memory, and the one act that stamps without moving.

// TestARepeatedReferenceAfterTheFirstCloseOpensAnIncident. The intake dedupes on
// the receiving centre's own reference, forever: already() searches every alarm
// ever recorded for that ref and answers with what the FIRST one became. A
// centre that recycles references — and they all do — reports the next break-in
// at that site and is answered 201 pointing at an incident that closed hours
// ago. Nobody is dispatched and the ARC is told somebody was.
func TestARepeatedReferenceAfterTheFirstCloseOpensAnIncident(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	first := activation(t, app, "ARC-77")
	for _, a := range []struct {
		act, as string
	}{
		{Verify, controller}, {Dispatch, controller}, {Roll, controller}, {Arrive, controller},
		{Check, controller}, {Secure, controller}, {Close, supervisor},
	} {
		body := map[string]any{"act": a.act}
		if a.act == Dispatch {
			body["unit"] = "gf-01"
		}
		if code, _ := stepped(t, app, first, a.as, body); code != http.StatusOK {
			t.Fatalf("%s want 200, got %d", a.act, code)
		}
	}

	second := activation(t, app, "ARC-77")
	if second == first {
		t.Errorf("a second activation under a recycled reference was answered with the closed incident %s: "+
			"no incident was opened and nobody was dispatched", first)
	}
}

// activation posts one activation and answers the incident it opened.
func activation(t *testing.T, app *zip.App, ref string) string {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, centre, map[string]any{
		"ref": ref, "account": "1001", "zone": "Z3", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("activation %s want 201, got %d (%s)", ref, code, first(raw, 200))
	}
	var made patrolActivation
	data(t, raw, &made)
	if made.Incident == nil {
		t.Fatalf("activation %s opened no incident: %s", ref, first(raw, 200))
	}
	return made.Incident.Name
}

// TestTwoEscalationsInFlightLandOnce drives M6 under contention rather than in
// sequence: the stamp is the guard, and the guard is only as good as the lease
// the read sits behind.
func TestTwoEscalationsInFlightLandOnce(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	for _, a := range []string{Verify, Dispatch} {
		body := map[string]any{"act": a}
		if a == Dispatch {
			body["unit"] = "gf-01"
		}
		if code, _ := stepped(t, app, name, controller, body); code != http.StatusOK {
			t.Fatalf("%s want 200, got %d", a, code)
		}
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = stepped(t, app, name, controller, map[string]any{"act": Escalate})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("two ESCALATE in flight landed %d times (codes %v)", ok, codes)
	}
	// One stamp, one step.
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the incident want 200, got %d", code)
	}
	var v patrolIncident
	data(t, raw, &v)
	n := 0
	for _, s := range v.Steps {
		if s.Act == Escalate {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the trail carries %d ESCALATE steps, want 1", n)
	}
}

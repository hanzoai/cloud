package patrol

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/zap-proto/zip"
)

// intake_test.go is the activation door. A centre's own reference identifies an
// activation while the response it opened is still running, so a redelivery —
// a timeout on their side, a panel repeating itself — must be ANSWERED rather
// than run again: two incidents for one break-in is two units dispatched and two
// accounts filed.

// took posts one activation and answers the status and what became of it.
func took(t *testing.T, app *zip.App, body map[string]any) (int, patrolActivation) {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, body)
	var got patrolActivation
	if code == http.StatusCreated || code == http.StatusAccepted {
		data(t, raw, &got)
	}
	return code, got
}

// TestAnActivationMatchesTheSiteItNames proves the first way an activation finds
// its site: the caller already knows which one, and names it, so the panel
// account it reported is not what resolves it.
func TestAnActivationMatchesTheSiteItNames(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, got := took(t, app, map[string]any{
		"ref": "ARC-1", "site": "depot", "account": "no-such-account", "trigger": "Intruder",
	})
	if code != http.StatusCreated {
		t.Fatalf("an activation naming its site want 201, got %d", code)
	}
	if got.Site != "depot" || got.Incident == nil {
		t.Fatalf("a named site must open an incident there: %+v", got)
	}
}

// TestAnActivationNamingNothingIsStillRecorded proves the floor: an activation
// carrying neither a site nor a panel account matches nothing and is kept
// anyway, because an activation is never dropped.
func TestAnActivationNamingNothingIsStillRecorded(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, got := took(t, app, map[string]any{"ref": "ARC-1", "trigger": "Panic"})
	if code != http.StatusAccepted {
		t.Fatalf("an activation naming nothing want 202, got %d", code)
	}
	if got.Matched || got.Site != "" || got.Incident != nil {
		t.Fatalf("an unmatched activation claims no site and no incident: %+v", got)
	}
	if got.Alarm == "" || got.Alarm == "ARC-1" {
		t.Fatalf("an activation answers the name it was recorded under, got %q", got.Alarm)
	}
	if !hasEvent(t, app, LevelAmber) {
		t.Error("an unmatched activation must raise an amber row")
	}
}

// TestARedeliveredActivationIsAnsweredNotRerun proves what a repeated reference
// costs while the response it opened is still running: the same incident back,
// and no second one.
func TestARedeliveredActivationIsAnsweredNotRerun(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, first := took(t, app, map[string]any{
		"ref": "ARC-7", "account": "1001", "zone": "Z3", "trigger": "Intruder"})
	if code != http.StatusCreated || first.Incident == nil {
		t.Fatalf("the first activation want 201 with an incident, got %d %+v", code, first)
	}
	code, again := took(t, app, map[string]any{
		"ref": "ARC-7", "account": "1001", "zone": "Z3", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("a redelivery want 201, got %d", code)
	}
	if again.Incident == nil || again.Incident.Name != first.Incident.Name {
		t.Fatalf("a redelivery opened %+v, want the response already running (%s)",
			again.Incident, first.Incident.Name)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident", org, controller, nil)
	var incs []patrolIncident
	data(t, raw, &incs)
	if code != http.StatusOK || len(incs) != 1 {
		t.Fatalf("one break-in opened %d incidents", len(incs))
	}
}

// TestARedeliveredUnmatchedActivationIsAnsweredWhileItIsLive proves the other
// half. An activation that matched no site opened nothing, so what is live is
// the record of it — for as long as a redelivery takes, and no longer.
func TestARedeliveredUnmatchedActivationIsAnsweredWhileItIsLive(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, first := took(t, app, map[string]any{"ref": "ARC-8", "account": "9999", "trigger": "Panic"})
	if code != http.StatusAccepted {
		t.Fatalf("an unmatched activation want 202, got %d", code)
	}
	code, again := took(t, app, map[string]any{"ref": "ARC-8", "account": "9999", "trigger": "Panic"})
	if code != http.StatusAccepted || again.Alarm != first.Alarm {
		t.Fatalf("a redelivery inside the window answered %+v, want the record already taken (%q)",
			again, first.Alarm)
	}
	if kept := alarms(t, app, "ARC-8"); kept != 1 {
		t.Errorf("a redelivery inside the window recorded %d activations, want 1", kept)
	}
}

// TestTheSameReferenceLaterIsTheNextBreakIn proves the reference is a centre's
// own and centres recycle them: once the response has closed, or the record of
// an unmatched one has gone stale, the same reference is a new activation and
// not an answer about an old one.
func TestTheSameReferenceLaterIsTheNextBreakIn(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	// An unmatched activation taken longer ago than a redelivery takes.
	stale := time.Now().UTC().Add(-2 * retryLife).Format(stampLayout)
	if _, err := framework.Ingest(context.Background(), org, DTAlarm, map[string]any{
		"ref": "ARC-9", "account": "9999", "trigger": "Panic", "at": stale,
	}, ""); err != nil {
		t.Fatalf("seed a stale activation: %v", err)
	}

	code, got := took(t, app, map[string]any{"ref": "ARC-9", "account": "1001", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("the same reference after the window want 201, got %d", code)
	}
	if got.Incident == nil || got.Site != "depot" {
		t.Fatalf("the next break-in at that reference must open its own incident: %+v", got)
	}
	if kept := alarms(t, app, "ARC-9"); kept != 2 {
		t.Errorf("the reference holds %d activations, want both the stale one and the new one", kept)
	}
}

// alarms counts the activations on record under one reference.
func alarms(t *testing.T, app *zip.App, ref string) int {
	t.Helper()
	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTAlarm.String(), org, boss, nil)
	if code != http.StatusOK {
		t.Fatalf("read the activations want 200, got %d (%s)", code, first(raw, 200))
	}
	return strings.Count(string(raw), `"ref":"`+ref+`"`)
}

// TestAnActivationAndAnIncidentAreWorkedOnceAtATime proves the two claims that
// keep one break-in to one record. A second caller on the same reference, and a
// second caller on the same incident, are told it is being worked rather than
// let through to write a duplicate — the claim is the engine's, so the answer is
// the same behind one replica or four.
func TestAnActivationAndAnIncidentAreWorkedOnceAtATime(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	ctx := context.Background()

	taken, held, err := framework.AcquireLease(ctx, org, "alarm/ARC-1", actLife, actWait)
	if err != nil || !held {
		t.Fatalf("hold the activation's claim: %v %v", err, held)
	}
	defer func() { _ = taken.Release(ctx) }()
	worked, held, err := framework.AcquireLease(ctx, org, "incident/"+name, actLife, actWait)
	if err != nil || !held {
		t.Fatalf("hold the incident's claim: %v %v", err, held)
	}
	defer func() { _ = worked.Release(ctx) }()

	// Both waits run at once, so the test costs one of them.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		codes[0] = waits(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
			map[string]any{"ref": "ARC-1", "account": "1001", "trigger": "Intruder"})
	}()
	go func() {
		defer wg.Done()
		codes[1] = waits(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, controller,
			map[string]any{"act": Verify})
	}()
	wg.Wait()

	if codes[0] != http.StatusConflict {
		t.Errorf("an activation already being taken want 409, got %d", codes[0])
	}
	if codes[1] != http.StatusConflict {
		t.Errorf("an incident already being worked want 409, got %d", codes[1])
	}
}

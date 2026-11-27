package patrol

import (
	"fmt"
	"net/http"
	"testing"
)

// window_test.go is about the shape of a bounded read. Every list here is
// framework.Search with a limit and no order, which the store answers
// updated_at ASC — the OLDEST rows — and several of them then narrow what came
// back. A window taken from the wrong end of the table, and a narrowing applied
// after the window instead of inside it, are the same mistake twice: the caller
// is answered with rows that are not the ones they asked about, and nothing
// says so.

// TestTheFeedListShowsTheNewestRows asks the feed list for the feed. The live
// stream carries a row as it is written and the list routes are "the truth" a
// dropped frame is recovered from — but Search takes the first `limit` rows in
// updated_at ASCENDING order, so past maxEvents rows the list is a fixed
// photograph of the org's first 80 events and the activation that just fired is
// not in it. A control room that reloads its screen sees a feed frozen at the
// day the tenant was installed.
func TestTheFeedListShowsTheNewestRows(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	const newest = "Intruder — the row that matters"
	for i := range maxEvents {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
			map[string]any{"level": LevelCyan, "title": fmt.Sprintf("routine %d", i)}); code != http.StatusCreated {
			t.Fatalf("seed feed row %d want 201, got %d (%s)", i, code, first(raw, 160))
		}
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelRed, "title": newest, "site": "depot"}); code != http.StatusCreated {
		t.Fatalf("raise the newest row want 201, got %d (%s)", code, first(raw, 160))
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/event", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list the feed want 200, got %d (%s)", code, first(raw, 200))
	}
	var rows []patrolEvent
	data(t, raw, &rows)
	for _, r := range rows {
		if r.Title == newest {
			return
		}
	}
	t.Errorf("the feed list answered %d rows and the row raised a moment ago is not among them: "+
		"the window is the OLDEST %d, so a screen that reloads reads a feed that stopped",
		len(rows), maxEvents)
}

// TestAClientSeesTheirOwnIncidentInAFullWindow measures narrowing applied after
// the window rather than inside it. incidents takes up to maxIncidents rows and
// THEN drops the ones reach does not see, so a customer's own break-in is
// invisible whenever other customers have filled the window — and whether they
// have is the operation's activity, not the customer's. The site list is
// narrowed in the query (reach.narrow) and gets this right; the rows about a
// site are not.
func TestAClientSeesTheirOwnIncidentInAFullWindow(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	for i := range maxIncidents {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
			map[string]any{"site": "rivals", "ref": fmt.Sprintf("R-%d", i), "trigger": "Intruder"}); code != http.StatusCreated {
			t.Fatalf("seed the rival's incident %d want 201, got %d (%s)", i, code, first(raw, 160))
		}
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "theirs", "ref": "MINE-1", "trigger": "Intruder"}); code != http.StatusCreated {
		t.Fatalf("open the customer's own incident want 201, got %d (%s)", code, first(raw, 160))
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident?open=1", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing open incidents want 200, got %d (%s)", code, first(raw, 200))
	}
	var incs []patrolIncident
	data(t, raw, &incs)
	if len(incs) == 0 {
		t.Errorf("a customer with a break-in running at their own site was answered an empty list: "+
			"%d other incidents filled the window before reach narrowed it, so the portal says "+
			"nothing is happening", maxIncidents)
	}
}

// TestEscalateIsAppliedOnce holds ESCALATE to what the lease was added for. The
// transition table gives it no landing state, so it leaves the incident in a
// state its own `from` set names and the same act runs again, and again: each
// pass restamps `escalated`, appends a step, writes a red feed row and mails
// every address on the controllers list. One act, unbounded applications — and
// the lease cannot help, because each pass is a separate, sequential, entirely
// legal call.
func TestEscalateIsAppliedOnce(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	for _, act := range []string{Verify, Dispatch} {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, controller,
			map[string]any{"act": act, "unit": "gf-01"}); code != http.StatusOK {
			t.Fatalf("%s want 200, got %d (%s)", act, code, first(raw, 160))
		}
	}
	for i := range 5 {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, controller,
			map[string]any{"act": Escalate}); code != http.StatusOK && i == 0 {
			t.Fatalf("the first ESCALATE want 200, got %d (%s)", code, first(raw, 160))
		}
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the incident want 200, got %d (%s)", code, first(raw, 200))
	}
	var inc patrolIncident
	data(t, raw, &inc)
	n := 0
	for _, s := range inc.Steps {
		if s.Act == Escalate {
			n++
		}
	}
	if n > 1 {
		t.Errorf("ESCALATE landed %d times on one incident: the trail says it was escalated five "+
			"times, the escalated stamp is the last one, and the controllers list was mailed once "+
			"per pass", n)
	}
}

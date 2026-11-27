package patrol

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// incident_test.go walks the state machine: every transition it allows, every
// transition it refuses, and every role refusal per act.

// stepped performs one act and returns the status and the incident it answered
// with.
func stepped(t *testing.T, app *zip.App, name, user string, body map[string]any) (int, patrolIncident) {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, user, body)
	var v patrolIncident
	if code == http.StatusOK {
		data(t, raw, &v)
	}
	return code, v
}

// opened seeds an estate and opens one incident in state received.
func opened(t *testing.T, app *zip.App) string {
	t.Helper()
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "zone": "Z3", "ref": "ARC-1", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open incident want 201, got %d (%s)", code, raw)
	}
	var v patrolIncident
	data(t, raw, &v)
	if v.State != Received {
		t.Fatalf("a new incident is %s, want %s", v.State, Received)
	}
	if v.Tier != "gold" || v.SLA != 20 {
		t.Fatalf("a new incident must carry the site's tier and SLA, got %q %d", v.Tier, v.SLA)
	}
	return v.Name
}

// TestEveryTransition walks the whole machine forward, each act by a role the
// act admits, and proves every one stamps its own timestamp and moves the state.
func TestEveryTransition(t *testing.T) {
	app := mount(t)
	name := opened(t, app)

	walk := []struct {
		act   string
		user  string
		state string
		stamp func(patrolIncident) string
	}{
		{Verify, controller, Verified, func(v patrolIncident) string { return v.Verified }},
		{Dispatch, controller, Dispatched, func(v patrolIncident) string { return v.Dispatched }},
		{Roll, officer, Enroute, func(v patrolIncident) string { return v.Enroute }},
		{Arrive, officer, Onsite, func(v patrolIncident) string { return v.Onsite }},
		{Check, officer, Checked, func(v patrolIncident) string { return v.Checked }},
		{Secure, officer, Secured, func(v patrolIncident) string { return v.Secured }},
		{File, controller, Filed, func(v patrolIncident) string { return v.Filed }},
		{Close, supervisor, Closed, func(v patrolIncident) string { return v.Closed }},
	}
	for _, w := range walk {
		body := map[string]any{"act": w.act}
		if w.act == Dispatch {
			body["unit"] = "gf-01"
		}
		if w.act == Close {
			body["outcome"], body["false"] = "secure, nothing found", true
		}
		code, v := stepped(t, app, name, w.user, body)
		if code != http.StatusOK {
			t.Fatalf("%s want 200, got %d", w.act, code)
		}
		if v.State != w.state {
			t.Fatalf("%s left the incident %s, want %s", w.act, v.State, w.state)
		}
		if w.stamp(v) == "" {
			t.Fatalf("%s did not stamp its own time", w.act)
		}
		if len(v.Steps) == 0 || v.Steps[len(v.Steps)-1].Act != w.act {
			t.Fatalf("%s did not append to the trail: %+v", w.act, v.Steps)
		}
	}

	// The trail holds the open plus the eight acts.
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read incident want 200, got %d", code)
	}
	var read patrolIncident
	data(t, raw, &read)
	if len(read.Steps) != len(walk)+1 {
		t.Fatalf("trail has %d acts, want %d", len(read.Steps), len(walk)+1)
	}
	if read.Outcome != "secure, nothing found" || !read.False {
		t.Fatalf("CLOSE must record the outcome and the false flag, got %q %v", read.Outcome, read.False)
	}
	if len(read.Clocks) != 3 {
		t.Fatalf("an incident carries three SLA clocks, got %d", len(read.Clocks))
	}
	for _, c := range read.Clocks {
		if c.Settled < 0 {
			t.Errorf("clock %q never settled on a closed incident", c.Label)
		}
	}
}

// TestRefusedTransition proves every act from every state it does not leave is
// 409, and that the refusal names the state the incident is in.
func TestRefusedTransition(t *testing.T) {
	app := mount(t)
	name := opened(t, app)

	// From received, only VERIFY runs. Every other act is 409 naming received.
	for a, m := range acts {
		if m.leaves(Received) {
			continue
		}
		code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, owner,
			map[string]any{"act": a, "unit": "gf-01"})
		if code != http.StatusConflict {
			t.Fatalf("%s from received want 409, got %d (%s)", a, code, raw)
		}
		if !strings.Contains(string(raw), Received) {
			t.Fatalf("%s from received must name the state it is in: %s", a, raw)
		}
	}

	// Walk to secured and prove the acts already spent are refused.
	for _, s := range []struct{ act, user, unit string }{
		{Verify, controller, ""}, {Dispatch, controller, "gf-01"},
		{Roll, officer, ""}, {Arrive, officer, ""}, {Check, officer, ""}, {Secure, officer, ""},
	} {
		body := map[string]any{"act": s.act}
		if s.unit != "" {
			body["unit"] = s.unit
		}
		if code, _ := stepped(t, app, name, s.user, body); code != http.StatusOK {
			t.Fatalf("%s want 200, got %d", s.act, code)
		}
	}
	for _, spent := range []string{Verify, Dispatch, Roll, Arrive, Check, Secure} {
		code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, owner,
			map[string]any{"act": spent, "unit": "gf-01"})
		if code != http.StatusConflict {
			t.Fatalf("%s from secured want 409, got %d (%s)", spent, code, raw)
		}
		if !strings.Contains(string(raw), Secured) {
			t.Fatalf("%s from secured must name secured: %s", spent, raw)
		}
	}
	// An act nobody declared is a bad request, not a conflict.
	if code, _ := stepped(t, app, name, owner, map[string]any{"act": "TELEPORT"}); code != http.StatusBadRequest {
		t.Fatalf("an unknown act want 400, got %d", code)
	}
}

// TestRoleRefusedPerAct proves each act refuses a caller who does not hold one
// of its roles, and admits one who does — every act, both directions.
func TestRoleRefusedPerAct(t *testing.T) {
	// The role each act must refuse, and one it must admit.
	cases := []struct {
		act     string
		refused string
		admits  string
		reach   []struct{ act, user, unit string }
	}{
		{act: Verify, refused: officer, admits: controller},
		{act: Dispatch, refused: officer, admits: controller,
			reach: []struct{ act, user, unit string }{{Verify, controller, ""}}},
		{act: Roll, refused: buyer, admits: officer,
			reach: []struct{ act, user, unit string }{{Verify, controller, ""}, {Dispatch, controller, "gf-01"}}},
		{act: File, refused: officer, admits: controller,
			reach: []struct{ act, user, unit string }{
				{Verify, controller, ""}, {Dispatch, controller, "gf-01"}, {Roll, officer, ""},
				{Arrive, officer, ""}, {Check, officer, ""}, {Secure, officer, ""}}},
		{act: Close, refused: controller, admits: supervisor,
			reach: []struct{ act, user, unit string }{
				{Verify, controller, ""}, {Dispatch, controller, "gf-01"}, {Roll, officer, ""},
				{Arrive, officer, ""}, {Check, officer, ""}, {Secure, officer, ""}}},
		{act: Escalate, refused: officer, admits: controller,
			reach: []struct{ act, user, unit string }{{Verify, controller, ""}, {Dispatch, controller, "gf-01"}}},
	}
	for _, c := range cases {
		t.Run(c.act, func(t *testing.T) {
			app := mount(t)
			name := opened(t, app)
			for _, r := range c.reach {
				body := map[string]any{"act": r.act}
				if r.unit != "" {
					body["unit"] = r.unit
				}
				if code, _ := stepped(t, app, name, r.user, body); code != http.StatusOK {
					t.Fatalf("reaching %s: %s want 200, got %d", c.act, r.act, code)
				}
			}
			body := map[string]any{"act": c.act, "unit": "gf-01"}
			if code, _ := stepped(t, app, name, c.refused, body); code != http.StatusForbidden {
				t.Fatalf("%s by %s want 403, got %d", c.act, c.refused, code)
			}
			if code, _ := stepped(t, app, name, c.admits, body); code != http.StatusOK {
				t.Fatalf("%s by %s want 200, got %d", c.act, c.admits, code)
			}
		})
	}
}

// TestEscalateLeavesTheState proves ESCALATE stamps its time and changes nothing
// else: an escalated incident is still where it was.
func TestEscalateLeavesTheState(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	for _, s := range []struct{ act, user, unit string }{
		{Verify, controller, ""}, {Dispatch, controller, "gf-01"},
	} {
		body := map[string]any{"act": s.act}
		if s.unit != "" {
			body["unit"] = s.unit
		}
		if code, _ := stepped(t, app, name, s.user, body); code != http.StatusOK {
			t.Fatalf("%s want 200, got %d", s.act, code)
		}
	}
	code, v := stepped(t, app, name, controller, map[string]any{"act": Escalate})
	if code != http.StatusOK {
		t.Fatalf("ESCALATE want 200, got %d", code)
	}
	if v.State != Dispatched {
		t.Fatalf("ESCALATE moved the state to %s; it must leave it at %s", v.State, Dispatched)
	}
	if v.Escalated == "" {
		t.Fatal("ESCALATE did not stamp escalated")
	}
}

// TestCloseRunsFromSecuredAndFromFiled proves CLOSE has two sources, which is
// the one act in the table that does.
func TestCloseRunsFromSecuredAndFromFiled(t *testing.T) {
	for _, from := range []string{Secured, Filed} {
		t.Run(from, func(t *testing.T) {
			app := mount(t)
			name := opened(t, app)
			walk := []struct{ act, user, unit string }{
				{Verify, controller, ""}, {Dispatch, controller, "gf-01"}, {Roll, officer, ""},
				{Arrive, officer, ""}, {Check, officer, ""}, {Secure, officer, ""},
			}
			if from == Filed {
				walk = append(walk, struct{ act, user, unit string }{File, controller, ""})
			}
			for _, s := range walk {
				body := map[string]any{"act": s.act}
				if s.unit != "" {
					body["unit"] = s.unit
				}
				if code, _ := stepped(t, app, name, s.user, body); code != http.StatusOK {
					t.Fatalf("%s want 200, got %d", s.act, code)
				}
			}
			code, v := stepped(t, app, name, supervisor, map[string]any{"act": Close})
			if code != http.StatusOK || v.State != Closed {
				t.Fatalf("CLOSE from %s want 200/closed, got %d/%s", from, code, v.State)
			}
			if v.Filed == "" {
				t.Fatal("CLOSE must leave a filed report behind it")
			}
		})
	}
}

// TestDispatchBindsTheUnitAndMovesIt proves the act's side effect: the incident
// carries the unit and the unit is assigned to it.
func TestDispatchBindsTheUnitAndMovesIt(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatal("VERIFY failed")
	}
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Dispatch}); code != http.StatusBadRequest {
		t.Fatalf("DISPATCH with no unit want 400, got %d", code)
	}
	code, v := stepped(t, app, name, controller, map[string]any{"act": Dispatch, "unit": "gf-01"})
	if code != http.StatusOK || v.Unit != "gf-01" {
		t.Fatalf("DISPATCH want 200 binding gf-01, got %d/%q", code, v.Unit)
	}
	if got := unitState(t, app, "gf-01"); got != UnitAssigned {
		t.Fatalf("the dispatched unit is %s, want %s", got, UnitAssigned)
	}
	if code, _ := stepped(t, app, name, officer, map[string]any{"act": Roll}); code != http.StatusOK {
		t.Fatal("ROLL failed")
	}
	if got := unitState(t, app, "gf-01"); got != UnitEnroute {
		t.Fatalf("the rolling unit is %s, want %s", got, UnitEnroute)
	}
}

// TestOfficerWorksOnlyTheirOwnUnit proves the acts an officer may perform are
// bounded by whose unit is on the incident.
func TestOfficerWorksOnlyTheirOwnUnit(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	seedUnit(t, app, org, "gf-02", "someone.else@acme.test", UnitAvailable, 52.0, -8.0)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatal("VERIFY failed")
	}
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Dispatch, "unit": "gf-02"}); code != http.StatusOK {
		t.Fatal("DISPATCH failed")
	}
	if code, _ := stepped(t, app, name, officer, map[string]any{"act": Roll}); code != http.StatusForbidden {
		t.Fatalf("an officer rolling another officer's unit want 403, got %d", code)
	}
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Roll}); code != http.StatusOK {
		t.Fatal("the control room may roll any unit")
	}
}

// unitState reads one unit's state back through the surface.
func unitState(t *testing.T, app *zip.App, sign string) string {
	t.Helper()
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list units want 200, got %d", code)
	}
	var units []patrolUnit
	data(t, raw, &units)
	for _, u := range units {
		if u.Name == sign {
			return u.State
		}
	}
	t.Fatalf("no unit %q", sign)
	return ""
}

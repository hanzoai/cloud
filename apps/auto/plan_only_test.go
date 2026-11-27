package auto

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// wantPlanOnly is what a plan run's summary begins with.
const wantPlanOnly = "Plan only (nothing was changed): "

// An ask automation's Dev run is a plan, so a run that completes says it only
// planned; an auto automation's run does not.
func TestPlanRunSummarySaysPlanOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		devPlane(t, client.TurnEvents{Next: 2, Outcome: client.TurnCompleted, Events: []client.TurnEvent{
			{Seq: 2, Kind: client.TurnAnswer, Text: "Reply to the board"},
		}})
		for _, c := range []struct{ permissions, want string }{
			{"ask", wantPlanOnly + "Reply to the board"},
			{"auto", "Reply to the board"},
		} {
			a := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
				"name": c.permissions, "instructions": "Draft replies.", "permissions": c.permissions,
			}))
			r := req(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)
			got := until(t, "acme", decode[runStarted](t, r).Run.ID)
			if got.Status != RunSucceeded || got.Summary != c.want {
				t.Errorf("%s run = %q (%s), want %q", c.permissions, got.Summary, got.Status, c.want)
			}
		}
	})
}

// A start answered from the session it already recorded still knows the Dev run
// was a plan, from the automation's stored definition.
func TestRetriedStartKnowsItIsAPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		captureStarter(t)
		devPlane(t, client.TurnEvents{})
		for _, c := range []struct {
			permissions string
			plan        bool
		}{{"ask", true}, {"auto", false}} {
			a := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
				"name": c.permissions, "instructions": "Draft ideas.", "permissions": c.permissions,
			}))
			run := decode[runStarted](t, req(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)).Run
			in := AgentStart{Owner: "acme", FlowID: a.ID, RunID: run.ID}
			for range 2 {
				if got, err := StartAgentActivity(context.Background(), in); err != nil || got.Plan != c.plan {
					t.Fatalf("%s start = %+v, %v; want plan %v", c.permissions, got, err, c.plan)
				}
			}
		}
	})
}

// The prefix leads whatever the run's outcome says.
func TestPlanSummaryLeadsEveryOutcome(t *testing.T) {
	for _, outcome := range []string{client.TurnCompleted, client.TurnStopped, client.TurnFailed} {
		in := AgentFinish{Outcome: outcome, Answer: "x", Plan: true}
		if got := in.summary(); !strings.HasPrefix(got, wantPlanOnly) || !strings.HasSuffix(got, summarize(outcome, "x", "")) {
			t.Errorf("%s summary = %q", outcome, got)
		}
		if got := (AgentFinish{Outcome: outcome, Answer: "x"}).summary(); strings.HasPrefix(got, wantPlanOnly) {
			t.Errorf("%s summary of a build run = %q", outcome, got)
		}
	}
}

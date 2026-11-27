package auto

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestOutcomeAutomationIsArmedWithoutAClockOrAnImmediateRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		starts := captureStarter(t)
		got := ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "Continuous research", "instructions": "Review the measured evidence and propose a tested draft change.",
			"event": map[string]any{"source": "research", "name": "improvement.reviewed"},
			"model": "halogen-qwen3.8-flash-next", "target": "my-local-machine", "permissions": "auto",
		})
		if got.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", got.Code, got.Body)
		}
		a := decode[Automation](t, got)
		if a.Event == nil || a.Event.Source != "research" || a.Target == nil || *a.Target != "my-local-machine" || a.Next != nil || !a.Enabled {
			t.Fatalf("wrong automation: %+v", a)
		}
		if len(*starts) != 0 {
			t.Fatal("creating an event automation started a run")
		}
		ev := TriggerEvent{Source: "research", Name: "improvement.reviewed", DedupeKey: "measured-version", Payload: map[string]any{"review": map[string]any{"status": "improved"}}}
		if n, err := Deliver(context.Background(), "acme", ev); err != nil || n != 1 {
			t.Fatalf("deliver: %d %v", n, err)
		}
		if len(*starts) != 1 || !(*starts)[0].Exclusive || (*starts)[0].Trigger == nil {
			t.Fatalf("not a durable exclusive event run: %+v", *starts)
		}
		v, err := mounted.State.store.LatestVersion(context.Background(), "acme", a.ID)
		if err != nil {
			t.Fatal(err)
		}
		d, ok := definitionOf(&v)
		if !ok || d.Event == nil {
			t.Fatal("event definition did not round-trip")
		}
		task := agentTask(d.trigger().NextAction.Settings.Input)
		if task.TargetID != "my-local-machine" || task.Model != "halogen-qwen3.8-flash-next" {
			t.Fatalf("local route lost: %+v", task)
		}
		// No timer is involved and repeating the same evidence cannot mint another run.
		if n, err := Deliver(context.Background(), "acme", ev); err != nil || n != 0 {
			t.Fatalf("duplicate: %d %v", n, err)
		}
	})
}

func TestOutcomeAutomationRefusesAmbiguousTriggersAndKeepsEvidenceAsData(t *testing.T) {
	d := definition{Name: "x", Instructions: "Review evidence", Event: &OutcomeTrigger{Source: "research", Name: "improvement.reviewed"}, Schedule: Schedule{Kind: KindDaily, At: "09:00", TZ: "UTC"}}
	if d.check() == nil {
		t.Fatal("event plus timed schedule accepted")
	}
	d.Schedule = Schedule{}
	d.Event.Source = "../other"
	if d.check() == nil {
		t.Fatal("malformed source accepted")
	}
	evidence, _ := json.Marshal(map[string]any{"review": map[string]any{"status": "regressed"}})
	task := agentTask(map[string]any{"instructions": "{{leave this literal}}", "permissions": "ask", "evidence": string(evidence)})
	if task.Mode != modePlan || !strings.Contains(task.Prompt, "{{leave this literal}}") || !strings.Contains(task.Prompt, "untrusted evidence") {
		t.Fatalf("evidence changed authority: %+v", task)
	}
}

func TestOutcomeAutomationKeepsItsDeclaredProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		starts := captureStarter(t)
		got := ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "Project research", "project": "one", "instructions": "Review evidence", "event": map[string]any{"source": "research", "name": "improvement.reviewed"},
		})
		if got.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", got.Code, got.Body)
		}
		for _, project := range []string{"", "other", "one"} {
			n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "research", Name: "improvement.reviewed", DedupeKey: "key-" + project, Payload: map[string]any{"review": map[string]any{"project": project}}})
			want := 0
			if project == "one" {
				want = 1
			}
			if err != nil || n != want {
				t.Fatalf("project %q: %d %v", project, n, err)
			}
		}
		if len(*starts) != 1 {
			t.Fatalf("starts: %d", len(*starts))
		}
	})
}

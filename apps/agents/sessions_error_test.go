package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/mint"
)

// A session in error says why, whoever ended it: the writer's reason, else the
// run's own status event, else the no-reason line.
//
// MUTATION: drop the guard at the top of UpdateSession and the first two cases read
// back an error with no reason.
func TestASessionInErrorAlwaysHasAReason(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testSessionStore(t)
		ctx := context.Background()
		for name, c := range map[string]struct {
			event string // the run's own last status event, "" for none
			given string // the reason the closing writer gave, "" for none
			want  string
		}{
			"nothing said why":             {"", "", errNoReason},
			"the run's own event said why": {`{"status":"error","error":"workspace has no repository named \"x\""}`, "", `workspace has no repository named "x"`},
			"the writer gave the reason":   {`{"status":"error","error":"from the event"}`, "from the writer", "from the writer"},
			"an event with no reason":      {`{"status":"error"}`, "", errNoReason},
			"a reason of only whitespace":  {"", "  \n ", errNoReason},
			"an event too long to list":    {`{"status":"error","error":"` + strings.Repeat("x", 5000) + `"}`, "", strings.Repeat("x", maxError)},
			"an event with no error key":   {`{"status":"started"}`, "", errNoReason},
		} {
			t.Run(name, func(t *testing.T) {
				id := "sess_" + mint.ID("e")
				if err := s.CreateSession(ctx, mkSession("acme", id, "", id)); err != nil {
					t.Fatal(err)
				}
				if c.event != "" {
					if _, err := s.AppendEvent(ctx, Event{ID: mint.ID("evt"), SessionID: id, Org: "acme", Kind: KindStatus, Payload: c.event, CreatedAt: time.Now().Unix()}); err != nil {
						t.Fatal(err)
					}
				}
				x, _ := s.GetSession(ctx, "acme", id)
				x.Status, x.Error, x.EndedAt, x.UpdatedAt = StatusError, c.given, time.Now().Unix(), time.Now().Unix()
				if err := s.UpdateSession(ctx, x); err != nil {
					t.Fatal(err)
				}
				got, _ := s.GetSession(ctx, "acme", id)
				if got.Error != c.want {
					t.Fatalf("a session ended in error reads %q, want %q", got.Error, c.want)
				}
			})
		}

		// A session that did not end in error is given no reason it has no use for.
		id := "sess_" + mint.ID("d")
		_ = s.CreateSession(ctx, mkSession("acme", id, "", id))
		x, _ := s.GetSession(ctx, "acme", id)
		x.Status, x.EndedAt = StatusDone, time.Now().Unix()
		_ = s.UpdateSession(ctx, x)
		if got, _ := s.GetSession(ctx, "acme", id); got.Error != "" {
			t.Fatalf("a done session reads the error %q", got.Error)
		}
	})
}

// A surface reports why its session ended in error with its status.
//
// MUTATION: drop the Error field from patchSessionIn and the reason is ignored.
func TestAPatchEndsASessionInErrorWithItsReason(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		register := func() string {
			code, b := do(t, app, http.MethodPost, "/v1/agent/sessions", "acme", map[string]any{"agent": "dev", "title": "t"})
			if code != http.StatusCreated {
				t.Fatalf("register want 201, got %d (%s)", code, b)
			}
			var v sessionView
			mustJSON(t, b, &v)
			return v.ID
		}
		patch := func(id string, body map[string]any) sessionView {
			code, b := do(t, app, http.MethodPatch, "/v1/agent/sessions/"+id, "acme", body)
			if code != http.StatusOK {
				t.Fatalf("patch %v want 200, got %d (%s)", body, code, b)
			}
			var v sessionView
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatal(err)
			}
			return v
		}

		if v := patch(register(), map[string]any{"status": "error", "error": "dev exited with status 101: thread 'main' panicked"}); v.Status != StatusError || v.Error != "dev exited with status 101: thread 'main' panicked" {
			t.Fatalf("a reported reason reads %q (%s)", v.Error, v.Status)
		}
		if v := patch(register(), map[string]any{"status": "error"}); v.Error != errNoReason {
			t.Fatalf("an unexplained error reads %q, want %q", v.Error, errNoReason)
		}
		if v := patch(register(), map[string]any{"status": "error", "error": strings.Repeat("y", 3000)}); len(v.Error) != maxError {
			t.Fatalf("a long reason is %d bytes, want %d", len(v.Error), maxError)
		}
		// A reason is no reopening: a session already in error takes the reason it lacked.
		id := register()
		patch(id, map[string]any{"status": "error"})
		if v := patch(id, map[string]any{"error": "late reason"}); v.Error != "late reason" || v.Status != StatusError {
			t.Fatalf("a late reason reads %q (%s)", v.Error, v.Status)
		}
		// A session that does not end in error keeps no reason.
		if v := patch(register(), map[string]any{"status": "done", "error": "not an error"}); v.Error != "" {
			t.Fatalf("a done session took the reason %q", v.Error)
		}
	})
}

// Registering a session without an agent says what the field is called and that a
// session row dispatches nothing, so a caller after a linked machine goes to the
// operation that does.
func TestRegisterWithoutAnAgentNamesTheRightOperation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		code, b := do(t, app, http.MethodPost, "/v1/agent/sessions", "acme", map[string]any{"title": "t"})
		if code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d %s", code, b)
		}
		var prob struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(b, &prob)
		for _, want := range []string{"agent is required", `the field is "agent"`, "dispatches nothing", "create_agent_coding", "targetId", "dir"} {
			if !strings.Contains(prob.Detail, want) {
				t.Fatalf("refusal %s lacks %q", b, want)
			}
		}
	})
}

package task

// create_test.go pins what filing an issue answers (cloud#433): 201 with the card
// it made — its number, its id and where it is read — or an error that says what
// went wrong. Never an empty 2xx, which a caller reads as "filed" for an issue
// that may not exist anywhere.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestCreateIssue_AnswersCreatedWithTheCard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.repo("hanzoai", "api")
		app := mountForge(t, f)

		code, raw := asUser(t, app, http.MethodPost, "/v1/task/projects/api/issues", "hanzo", "alice",
			map[string]any{"title": "the ask", "description": "what it needs"})
		if code != http.StatusCreated {
			t.Fatalf("create = %d %s, want 201", code, raw)
		}
		var card issueView
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatalf("the answer is not a card: %v (%s)", err, raw)
		}
		if card.Number != 7 || card.ID != "99" || card.URL != "https://git.hanzo.ai/hanzoai/api/issues/7" {
			t.Fatalf("the card does not say what was made and where: %+v", card)
		}
	})
}

// The forge answering 201 with nothing is not an issue made. It was a card
// numbered 0 with no address; it is a 502 that says to look before filing again.
func TestCreateIssue_AnAnswerWithoutTheIssueIsAnError(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.repo("hanzoai", "api")
		f.hollow = true
		app := mountForge(t, f)

		code, raw := asUser(t, app, http.MethodPost, "/v1/task/projects/api/issues", "hanzo", "alice",
			map[string]any{"title": "the ask"})
		if code != http.StatusBadGateway {
			t.Fatalf("a hollow create = %d %s, want 502", code, raw)
		}
		if !strings.Contains(string(raw), "without the issue it opened") {
			t.Fatalf("the refusal does not say what went wrong: %s", raw)
		}
	})
}

// A request that cannot make a card is refused before the forge hears of it,
// with a message naming what is missing.
func TestCreateIssue_ATitleIsRequired(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.visible["alice"] = []string{"hanzoai"}
		f.repo("hanzoai", "api")
		app := mountForge(t, f)

		// Refused by the handler, not by a validate tag on the field (see
		// forgeCreateIssue): missing, empty and whitespace-only all answer 400.
		for _, body := range []map[string]any{{}, {"title": ""}, {"title": "   "}, {"title": "\t\n"}} {
			code, raw := asUser(t, app, http.MethodPost, "/v1/task/projects/api/issues", "hanzo", "alice", body)
			if code != http.StatusBadRequest || !strings.Contains(string(raw), "title") {
				t.Errorf("create %v = %d %s, want 400 naming the title", body, code, raw)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.writes) != 0 {
			t.Fatalf("an untitled create reached the forge: %+v", f.writes)
		}
	})
}

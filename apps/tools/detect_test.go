package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// brandPool lists the brand's catalogue as skills, as the skills app does in
// production: a candidate's evidence is weighed against the pool it competes in.
func brandPool(t *testing.T) {
	t.Helper()
	var ix struct {
		Skills []struct{ Name, Description string }
	}
	readJSON(t, "../skills/catalog/hanzo/index.json", &ix)
	tools := make([]Tool, 0, len(ix.Skills))
	for _, s := range ix.Skills {
		tools = append(tools, Tool{Name: "skill_" + s.Name, Source: SourceSkill, Description: s.Description})
	}
	std.Register(&fakeProvider{src: SourceSkill, tools: tools})
}

func detect(t *testing.T, app *zip.App, body map[string]any) detectResult {
	t.Helper()
	res := do(t, app, http.MethodPost, "/v1/tool/detect", "acme", body)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /v1/tool/detect = %d (%s), want 200", res.Code, res.Body)
	}
	var out detectResult
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode detectResult: %v (%s)", err, res.Body)
	}
	return out
}

func recNames(recs []detectRecommendation) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Name)
	}
	return out
}

// Detect ranks in process: the decision service is not asked, so a prompt is
// answered without waiting on it (it used to wait out a 3 s timeout on every
// call, and Kai's yes/no answers did not tell one skill from another).
func TestDetectTools_RanksWithoutAskingTheDecisionService(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)
		brandPool(t)
		std.Register(&fakeProvider{
			src: SourceConnector,
			tools: []Tool{
				{Name: "github_create_pull_request", Source: SourceConnector, Description: "Create a pull request on GitHub repository"},
				{Name: "slack_send_message", Source: SourceConnector, Description: "Send a chat message to a Slack channel"},
			},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("the decision service was asked: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}))
		defer srv.Close()
		t.Setenv("KAI_URL", srv.URL)

		out := detect(t, app, map[string]any{
			"prompt":       "Review the code diff and open a GitHub pull request",
			"autoActivate": true,
		})
		if out.Model != detectModel || out.DecisionID != "" {
			t.Fatalf("model=%q decisionId=%q, want %q and none", out.Model, out.DecisionID, detectModel)
		}
		if len(out.Skills) == 0 || out.Skills[0].Name != "skill_code-review" || !out.Skills[0].Activated || out.Skills[0].Probability < 0.8 {
			t.Fatalf("skills = %+v, want skill_code-review first, sure and activated", out.Skills)
		}
		if len(out.Tools) == 0 || out.Tools[0].Name != "github_create_pull_request" || !out.Tools[0].Activated {
			t.Fatalf("tools = %+v, want github_create_pull_request first and activated", out.Tools)
		}
		for _, tl := range out.Tools {
			if tl.Name == "slack_send_message" {
				t.Fatalf("slack_send_message was recommended for a pull request: %+v", out.Tools)
			}
		}
	})
}

// A skill is found by what it does: the built-in ui and code-review describe
// layouts and reviews, and the prompt names neither skill.
func TestDetectTools_RanksByWhatASkillDoes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)
		brandPool(t)

		out := detect(t, app, map[string]any{"prompt": "the hero section on our homepage looks cramped on mobile, can you fix the layout"})
		if len(out.Skills) == 0 || out.Skills[0].Name != "skill_ui" || out.Skills[0].Probability < 0.8 {
			t.Fatalf("skills = %+v, want skill_ui first at 0.8 or more", out.Skills)
		}
		out = detect(t, app, map[string]any{"prompt": "please review my diff for race conditions"})
		if len(out.Skills) == 0 || out.Skills[0].Name != "skill_code-review" || out.Skills[0].Probability < 0.8 {
			t.Fatalf("skills = %+v, want skill_code-review first at 0.8 or more", out.Skills)
		}
	})
}

// A message no skill fits is recommended no skill at hanzo.app's bar, however
// many of the brand's descriptors share a word with it.
func TestDetectTools_NoSkillForAMessageNoSkillFits(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)
		brandPool(t)
		for _, prompt := range []string{
			"what is the capital of France?",
			"write a birthday message for my sister",
			"help me plan a marketing strategy for a bakery",
		} {
			out := detect(t, app, map[string]any{"prompt": prompt, "maxSkills": 8})
			for _, s := range out.Skills {
				if s.Probability >= 0.8 {
					t.Errorf("%q: %s at %.3f, want nothing at 0.8 or more (all: %v)", prompt, s.Name, s.Probability, recNames(out.Skills))
				}
			}
		}
	})
}

func TestDetectTools_PROpenAndPonytailAutoActivatesCodeReview(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)

		out := detect(t, app, map[string]any{
			"prompt":       "PR #42 opened: run ponytail review on https://github.com/hanzoai/app/pull/42",
			"autoActivate": true,
		})
		if len(out.Skills) == 0 || out.Skills[0].Name != "skill_code-review" || !out.Skills[0].Activated {
			t.Fatalf("expected skill_code-review to be top activated skill for PR/ponytail prompt, got %+v", out.Skills)
		}
		activated := activateDefaultSkillsForPR(t.Context(), "acme-pr", "app")
		if len(activated) != 2 || activated[0] != "skill_code-review" || activated[1] != "skill_dev" {
			t.Fatalf("expected activateDefaultSkillsForPR to activate skill_code-review and skill_dev, got %+v", activated)
		}
	})
}

// Naming a pull request is not asking for a review.
func TestAsksForReview(t *testing.T) {
	for prompt, want := range map[string]bool{
		"can you review this pull request before i merge it?": true,
		"go through pr 88 and tell me what could break":       true,
		"run ponytail on https://github.com/a/b/pull/1":       true,
		"list my open pull requests with the gh cli":          false,
		"write the description for my pr":                     false,
		"make the button blue":                                false,
	} {
		if got := asksForReview(prompt); got != want {
			t.Errorf("asksForReview(%q) = %v, want %v", prompt, got, want)
		}
	}
}

package code

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
)

// Synthesizer turns a retrieval-grounded prompt into a cited answer. It wraps the
// existing in-process chat path (deps.AI) behind an interface so /ask is testable
// without a live model.
type Synthesizer interface {
	Synthesize(ctx context.Context, org, billingOrg, project, prompt string) (string, error)
	Enabled() bool
}

// aiSynth is the production synthesizer over cloud's in-process AI client (the
// same OpenAI-compatible chat path agents/eval use). A nil client disables it and
// /ask degrades to citations-only.
type aiSynth struct {
	ai    cloud.AIClient
	model string
}

func newSynth(ai cloud.AIClient, model string) *aiSynth {
	if model == "" {
		model = "deepseek-v4-flash"
	}
	return &aiSynth{ai: ai, model: model}
}

func (a *aiSynth) Enabled() bool { return a.ai != nil }

func (a *aiSynth) Synthesize(ctx context.Context, org, billingOrg, project, prompt string) (string, error) {
	if a.ai == nil {
		return "", fmt.Errorf("synth: no AI client")
	}
	resp, err := a.ai.ChatCompletion(ctx, &cloud.ChatRequest{Model: a.model, Prompt: prompt, Org: org, BillingOrg: billingOrg, Project: project})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("synth: nil response")
	}
	return resp.Content, nil
}

// Citation points an /ask answer back at exact code.
type Citation struct {
	Repo    string `json:"repo,omitempty"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
	Symbol  string `json:"symbol,omitempty"`
}

// AskAnswer is the /ask result: the synthesized answer plus the exact spans it
// was grounded on. Degraded=true means retrieval succeeded but synthesis was
// unavailable — the caller still gets cited spans to reason over.
type AskAnswer struct {
	Question  string     `json:"question"`
	Answer    string     `json:"answer"`
	Citations []Citation `json:"citations"`
	Degraded  bool       `json:"degraded,omitempty"`
}

const askContextBudget = 6000

// ask retrieves grounding context (hybrid → budget-packed), then synthesizes a
// cited answer. Fail-honest: with no matched code it returns an empty answer with
// a note; with no synthesizer it returns the citations so the caller can answer
// itself. It never fabricates an answer without grounding.
func (e *engine) ask(ctx context.Context, synth Synthesizer, repo, question string) (AskAnswer, error) {
	bundle, err := e.packContext(ctx, repo, question, askContextBudget)
	if err != nil {
		return AskAnswer{}, err
	}
	ans := AskAnswer{Question: question, Citations: citationsOf(bundle.Spans)}
	if len(bundle.Spans) == 0 {
		ans.Answer = "No indexed code matched this question. Index the repo first, or rephrase."
		return ans, nil
	}
	if synth == nil || !synth.Enabled() {
		ans.Degraded = true
		return ans, nil
	}
	out, err := synth.Synthesize(ctx, e.org, e.billingOrg, e.project, buildAskPrompt(question, bundle.Spans))
	if err != nil {
		ans.Degraded = true // synthesis outage: return grounding, not a 5xx
		return ans, nil
	}
	ans.Answer = strings.TrimSpace(out)
	return ans, nil
}

func citationsOf(spans []Span) []Citation {
	out := make([]Citation, 0, len(spans))
	for _, s := range spans {
		out = append(out, Citation{Repo: s.Repo, File: s.File, Line: s.Line, EndLine: s.EndLine, Symbol: s.Symbol})
	}
	return out
}

// buildAskPrompt grounds the model: answer ONLY from the numbered context and
// cite file:line. The contract keeps answers honest and traceable.
func buildAskPrompt(question string, spans []Span) string {
	var b strings.Builder
	b.WriteString("You are a code-intelligence assistant. Answer the question using ONLY the code context below. ")
	b.WriteString("Cite the files you rely on as `file:line`. If the context is insufficient, say so plainly.\n\n")
	b.WriteString("# Context\n")
	for i, s := range spans {
		fmt.Fprintf(&b, "\n[%d] %s:%d-%d", i+1, s.File, s.Line, s.EndLine)
		if s.Symbol != "" {
			fmt.Fprintf(&b, " (%s %s)", s.Kind, s.Symbol)
		}
		b.WriteString("\n")
		b.WriteString(s.Snippet)
		b.WriteString("\n")
	}
	b.WriteString("\n# Question\n")
	b.WriteString(question)
	b.WriteString("\n\n# Answer\n")
	return b.String()
}

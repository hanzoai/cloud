package agents

// pin.go — one generation turn is served by one model (#802).
//
// A turn of a run is many completions: the tool loop asks, runs what the model
// called, and asks again with the results (tools.go). Every round used to start from
// the agent's own model, so a round that model could not answer went to the fallback
// (completeWithFailover) and the NEXT round went back to the agent's model: one tool
// sequence begun by one model and continued by another that never made its plan.
//
// Now a turn keeps one model (turnModel):
//
//   - The model that answers the turn's first round holds the turn. Every later
//     round asks it first, and the route's other model only when it cannot answer.
//     A first round the fallback answered is the fallback's turn from the start:
//     nothing of the turn was the agent model's, so that is no switch.
//   - A later round another model answers is a SWITCH. That model starts a fresh
//     reasoning context — it is shown what the turn said and did, none of the
//     reasoning the previous model wrote (freshContext) — and the turn stays with
//     it, so it does not swing back.
//   - The agent.step span says so: hanzo.agent.turn.model is the model holding the
//     turn, hanzo.agent.turn.switches how many times it moved, and each move is a
//     "model switch" event naming from, to, the round, and
//     hanzo.agent.turn.fresh_context.
//
// Hidden reasoning never reaches this transcript: sdk/infer reads a completion's
// content and tool calls and nothing else, so a reasoning_content field or a signed
// thinking block is never carried into the next request. What can survive is
// reasoning a model wrote inline, as a block at the head of its content, and that is
// what a fresh context leaves out. Whether two models could share reasoning (one
// thinking-block family, one account) is a property of the models (#801); until one
// says so, every switch starts fresh, and shown is the one place that decides.

import (
	"cmp"
	"context"
	"strings"
	"unicode"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hanzoai/cloud/sdk/infer"
)

// turnModel is the model one turn is served by.
type turnModel struct {
	own      string // the agent's model
	fallback string // the route's reliable model; "" is retry-only
	pinned   string // the model holding the turn; "" until a round is answered
	switches int    // how many times the turn moved to another model
}

// route is the model a round asks first, and the one it fails over to: the model
// holding the turn, then the other of the agent's model and the fallback.
func (t *turnModel) route() (first, next string) {
	first = cmp.Or(t.pinned, t.own)
	if first == t.own {
		return first, t.fallback
	}
	return first, t.own
}

// shown is the transcript model m is asked with: as written for the model holding
// the turn, or for any model before one does; a fresh reasoning context for any
// other. A nil turnModel is a completion that belongs to no turn.
func (t *turnModel) shown(m string, msgs []infer.ChatMessage) []infer.ChatMessage {
	if t == nil || t.pinned == "" || m == t.pinned {
		return msgs
	}
	return freshContext(msgs)
}

// served records that m answered round, on the span in ctx, and reports whether
// that moved the turn to m from another model.
func (t *turnModel) served(ctx context.Context, m string, round int) bool {
	if m == t.pinned {
		return false
	}
	from := t.pinned
	t.pinned = m
	span := trace.SpanFromContext(ctx)
	if from != "" {
		t.switches++
		span.AddEvent("model switch", trace.WithAttributes(
			attribute.String("hanzo.agent.turn.from", from),
			attribute.String("hanzo.agent.turn.to", m),
			attribute.Int("hanzo.agent.turn.round", round),
			attribute.Bool("hanzo.agent.turn.fresh_context", true),
		))
	}
	span.SetAttributes(
		attribute.String("hanzo.agent.turn.model", m),
		attribute.Int("hanzo.agent.turn.switches", t.switches),
	)
	return from != ""
}

// reasoningTags are the inline spellings of reasoning: a block a model opens its
// content with, before the answer.
var reasoningTags = []string{"think", "thinking"}

// freshContext is msgs without the reasoning any assistant turn wrote inline: what a
// model the turn switched to is shown. It is a new slice; msgs, and any request
// already sent from it, are left as they were. An assistant turn that was nothing
// but reasoning says nothing to the next model and is left out; one that called a
// tool keeps the call its result answers.
func freshContext(msgs []infer.ChatMessage) []infer.ChatMessage {
	out := make([]infer.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == infer.RoleAssistant {
			m.Content = withoutReasoning(m.Content)
			if m.Content == "" && len(m.ToolCalls) == 0 {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// withoutReasoning is content without the reasoning blocks it opens with. A block
// that never closes is reasoning to the end: the model never reached an answer. A
// tag anywhere but the head is the answer's own text and stays.
func withoutReasoning(content string) string {
	s := strings.TrimLeftFunc(content, unicode.IsSpace)
	cut := false
	for {
		tag := opening(s)
		if tag == "" {
			break
		}
		end := strings.Index(s, "</"+tag+">")
		if end < 0 {
			return ""
		}
		s = strings.TrimLeftFunc(s[end+len(tag)+3:], unicode.IsSpace)
		cut = true
	}
	if !cut {
		return content
	}
	return s
}

// opening is the reasoning tag s opens with, "" when it opens with none.
func opening(s string) string {
	for _, tag := range reasoningTags {
		if strings.HasPrefix(s, "<"+tag+">") {
			return tag
		}
	}
	return ""
}

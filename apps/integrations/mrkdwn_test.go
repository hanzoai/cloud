package integrations

// What the owner actually saw in Slack: a reply that "shits our markdown, not
// marked up properly" — asterisks and hashes rendering as literal punctuation
// because Slack reads mrkdwn and the model writes Markdown.

import "testing"

func TestMrkdwnTranslatesWhatModelsWrite(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"bold", "**Agentic coding** — ready", "*Agentic coding* — ready"},
		{"bold underscores", "__Task tracking__ works", "*Task tracking* works"},
		{"heading", "### Honest picture", "*Honest picture*"},
		{"heading atx closed", "## Status ##", "*Status*"},
		{"link", "see [the docs](https://hanzo.ai/docs)", "see <https://hanzo.ai/docs|the docs>"},
		{"strike", "~~gone~~", "~gone~"},
		{"bullet dash", "- Agents\n- Code", "• Agents\n• Code"},
		{"bullet star", "* Agents", "• Agents"},
		{"nested bullet keeps indent", "  - deep", "  • deep"},
		{"already mrkdwn italic untouched", "_italic_ stays", "_italic_ stays"},
		{"plain text untouched", "no markup here", "no markup here"},
	} {
		if got := mrkdwn(c.in); got != c.want {
			t.Errorf("%s:\n  in   %q\n  got  %q\n  want %q", c.name, c.in, got, c.want)
		}
	}
}

// Code is the case where translating is WORSE than the bug: `**x**` inside a
// sample is text the reader is meant to see.
func TestMrkdwnLeavesCodeAlone(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"fenced", "```\n**not bold** and [not](a link)\n```"},
		{"fenced with lang", "```go\nx := **p\n```"},
		{"inline span", "call `**kwargs` please"},
		{"bullet inside fence", "```\n- stays a dash\n```"},
	} {
		if got := mrkdwn(c.in); got != c.in {
			t.Errorf("%s: code was rewritten\n  in  %q\n  got %q", c.name, c.in, got)
		}
	}
}

// A fence and prose in one message: translate around the fence, not through it.
func TestMrkdwnMixed(t *testing.T) {
	in := "**Status**\n```\n**raw**\n```\n- done"
	want := "*Status*\n```\n**raw**\n```\n• done"
	if got := mrkdwn(in); got != want {
		t.Errorf("mixed:\n  got  %q\n  want %q", got, want)
	}
}

// A model that gets cut off mid-fence must not cost us the delimiter.
func TestMrkdwnUnbalancedFenceKeepsItsMarker(t *testing.T) {
	got := mrkdwn("**hi**\n```\nunclosed")
	if got != "*hi*\n```\nunclosed```" {
		t.Errorf("unbalanced fence: got %q", got)
	}
}

func TestMrkdwnEmpty(t *testing.T) {
	if mrkdwn("") != "" {
		t.Error("empty string should stay empty")
	}
}

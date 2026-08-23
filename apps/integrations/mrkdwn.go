package integrations

// Slack does not read Markdown. It reads mrkdwn, which is a DIFFERENT language
// that happens to share punctuation, so a model's ordinary prose arrives looking
// like source: `**bold**` renders with the asterisks showing, `### Heading` is a
// literal hash, and `[docs](url)` prints the brackets next to the parentheses.
//
// The model is not going to stop writing Markdown — every model writes Markdown,
// it is what they are trained on, and telling one not to in a system prompt
// spends context on a rule it will drop by the third turn. So the translation
// belongs here, at the edge, where prose becomes a Slack message.
//
// It is applied at the two clients where MODEL text goes out, and deliberately not
// inside slackChatPost. Block Kit callers in this package already write mrkdwn by
// hand (mrkdwnSection), and running a Markdown translator over correct mrkdwn
// corrupts it: `*bold*` is mrkdwn bold and Markdown italic, so a blanket pass at
// the bottom would rewrite every one of those into `_bold_`. One translator, at
// the boundary the untranslated language actually enters through.

import (
	"regexp"
	"strings"
)

var (
	// Bold first, and before any single-asterisk rule could see it: `**x**`
	// contains `*x*`, so the wide match has to win or bold degrades to italic.
	mdBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	// Markdown's ~~strike~~ is mrkdwn's ~strike~.
	mdStrike = regexp.MustCompile(`~~([^~\n]+)~~`)
	// `[text](url)` → `<url|text>`. The url stops at whitespace or `)` so a
	// trailing sentence paren cannot be swallowed into the link.
	mdLink = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	// mrkdwn has NO headings. A heading is a bold line — that is the whole of
	// the available vocabulary, and it reads correctly in a chat message.
	mdHead = regexp.MustCompile(`(?m)^ {0,3}#{1,6}[ \t]+(.+?)[ \t]*#*$`)
	// `- item` / `* item` → `• item`, indentation preserved so nesting survives.
	mdBullet = regexp.MustCompile(`(?m)^([ \t]*)[-*][ \t]+`)
)

// mrkdwn rewrites Markdown as Slack mrkdwn.
//
// Code is EXEMPT and that is the point of the split below rather than a single
// pass: inside a fence or a span, `**x**` is text a person meant to see, and
// "helpfully" translating a code sample is worse than the formatting bug this
// fixes. Fenced blocks and inline spans already mean the same thing in both
// languages, so they need no translation — only protection.
func mrkdwn(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	for i, part := range strings.Split(s, "```") {
		if i%2 == 1 { // inside a fence — verbatim, fence markers restored
			b.WriteString("```")
			b.WriteString(part)
			b.WriteString("```")
			continue
		}
		b.WriteString(spans(part))
	}
	out := b.String()
	// An unbalanced fence would have eaten its own marker above. Put it back so
	// a truncated answer degrades to "one stray backtick run" instead of losing
	// the delimiter and reflowing the rest of the message.
	if strings.Count(s, "```")%2 == 1 && !strings.HasSuffix(out, "```") {
		out += "```"
	}
	return out
}

// spans translates one non-fenced region, holding inline `code` out of it.
func spans(s string) string {
	var b strings.Builder
	for i, part := range strings.Split(s, "`") {
		if i%2 == 1 {
			b.WriteString("`")
			b.WriteString(part)
			b.WriteString("`")
			continue
		}
		b.WriteString(prose(part))
	}
	return b.String()
}

// prose is the translation itself, in an order the rules depend on.
func prose(s string) string {
	s = mdHead.ReplaceAllString(s, "*$1*")
	s = mdLink.ReplaceAllString(s, "<$2|$1>")
	s = mdBold.ReplaceAllStringFunc(s, func(m string) string {
		if after, ok := strings.CutPrefix(m, "**"); ok {
			return "*" + strings.TrimSuffix(after, "**") + "*"
		}
		return "*" + strings.TrimSuffix(strings.TrimPrefix(m, "__"), "__") + "*"
	})
	s = mdStrike.ReplaceAllString(s, "~$1~")
	s = mdBullet.ReplaceAllString(s, "$1• ")
	return s
}

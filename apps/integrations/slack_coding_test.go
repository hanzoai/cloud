package integrations

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodingIntent(t *testing.T) {
	cases := []struct {
		in     string
		rest   string
		isCode bool
	}{
		{"code: api fix the bug", "api fix the bug", true},
		{"CODE:  api do it", "api do it", true}, // case-insensitive, trimmed
		{"  code: api x", "api x", true},        // leading whitespace tolerated
		{"what's the weather", "", false},
		{"code", "", false},         // no colon
		{"coder: api x", "", false}, // not the trigger
	}
	for _, c := range cases {
		rest, ok := codingIntent(c.in)
		if ok != c.isCode || (ok && rest != c.rest) {
			t.Errorf("codingIntent(%q) = (%q,%v), want (%q,%v)", c.in, rest, ok, c.rest, c.isCode)
		}
	}
}

func TestParseCoding(t *testing.T) {
	cases := []struct {
		in                 string
		repo, target, task string
		ok                 bool
	}{
		{"api fix the null deref", "api", "", "fix the null deref", true},
		{"my-repo.v2   do a thing", "my-repo.v2", "", "do a thing", true},
		{"api", "", "", "", false},             // repo only, no task
		{"", "", "", "", false},                // empty
		{"bad/repo fix it", "", "", "", false}, // slash is not a valid repo char
		{"../etc fix", "", "", "", false},      // traversal token rejected
		{"repo    ", "", "", "", false},        // whitespace-only task
		// `on <machine>` routing prefix — only when `on` is the token after the repo.
		{"api on evo add a test", "api", "evo", "add a test", true},
		{"api on tgt_abc123 refactor the parser", "api", "tgt_abc123", "refactor the parser", true},
		{"api on evo", "", "", "", false}, // `on <machine>` with no task
		{"api on", "", "", "", false},     // `on` with no machine and no task
		// A task that merely CONTAINS "on" later is not routing — the repo's very
		// next token must be exactly `on`.
		{"api only fix the thing", "api", "", "only fix the thing", true},
		{"api fix the on-call handler", "api", "", "fix the on-call handler", true},
	}
	for _, c := range cases {
		repo, target, task, ok := parseCoding(c.in)
		if ok != c.ok || repo != c.repo || target != c.target || task != c.task {
			t.Errorf("parseCoding(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)", c.in, repo, target, task, ok, c.repo, c.target, c.task, c.ok)
		}
	}
}

func blocksJSON(t *testing.T, blocks []any) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // see the literal mrkdwn (&lt;) not JSON's &lt;
	if err := enc.Encode(blocks); err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	return b.String()
}

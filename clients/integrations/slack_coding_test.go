package integrations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/coding"
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

func TestCodingResultCard_States(t *testing.T) {
	// success with a PR
	sum, blocks := codingResultCard("hanzo", "acme", "api", coding.Result{
		OK: true, Changed: true, Branch: "agent/x", CommitSha: "deadbeefcafef00d",
		PR: coding.PRRef{Identifier: "API-7"}, Diffstat: "1 file changed", SessionID: "sess_1",
	})
	if !strings.Contains(sum, "acme/api") {
		t.Fatalf("summary missing repo: %q", sum)
	}
	js := blocksJSON(t, blocks)
	if !strings.Contains(js, "API-7") || !strings.Contains(js, "agent/x") || !strings.Contains(js, "deadbee") {
		t.Fatalf("success card missing PR/branch/commit: %s", js)
	}

	// no changes → no PR
	_, blocks = codingResultCard("hanzo", "acme", "api", coding.Result{OK: true, Changed: false})
	if strings.Contains(blocksJSON(t, blocks), "*PR*") {
		t.Fatalf("no-changes card should not show a PR field")
	}

	// failure → error field
	_, blocks = codingResultCard("hanzo", "acme", "api", coding.Result{OK: false, Error: "boom"})
	if !strings.Contains(blocksJSON(t, blocks), "boom") {
		t.Fatalf("failure card missing error")
	}
}

func TestCodingResultCard_EscapesInjection(t *testing.T) {
	// A hostile branch / diffstat / error must be mrkdwn-escaped so it can't smuggle
	// a <!channel> broadcast or a disguised link into the posted card.
	_, blocks := codingResultCard("hanzo", "acme", "api", coding.Result{
		OK: false, Branch: "<!channel>", Error: "<https://evil|click> & <@U123>",
	})
	js := blocksJSON(t, blocks)
	// The security property: NO raw angle bracket survives anywhere in the card, so
	// no user/agent value can smuggle <!channel>, <@U…>, or a <url|label> link. (In
	// JSON the escaped form renders as &lt;/&gt; — an explicit &lt;/&gt;.)
	if strings.ContainsAny(js, "<>") {
		t.Fatalf("raw angle bracket leaked unescaped: %s", js)
	}
	if !strings.Contains(js, `&lt;!channel&gt;`) {
		t.Fatalf("branch was not mrkdwn-escaped: %s", js)
	}
}

// A ROUTED-and-accepted run renders a "routed to a machine, follow it live" card —
// not a premature branch-pushed/no-changes verdict. The run is queued, not done.
func TestCodingResultCard_RoutedIsQueuedNotDone(t *testing.T) {
	_, blocks := codingResultCard("hanzo", "acme", "api", coding.Result{
		OK: true, Routed: true, TargetID: "tgt_evo", SessionID: "sess_r", Branch: "agent/r",
	})
	js := blocksJSON(t, blocks)
	if !strings.Contains(js, "Routed to a machine") || !strings.Contains(js, "tgt_evo") {
		t.Fatalf("routed card must name the machine: %s", js)
	}
	// It must NOT claim a branch was pushed / PR filed — the machine drives that.
	if strings.Contains(js, "Branch pushed") || strings.Contains(js, "*PR*") {
		t.Fatalf("a queued routed run must not show a completed verdict: %s", js)
	}
	if !strings.Contains(js, "sess_r") {
		t.Fatalf("routed card must link the session to follow: %s", js)
	}
}

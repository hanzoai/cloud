package coding

// The tenant a coding run acts for has to travel ON THE WIRE, and these pin the
// two halves of why that is not obvious — the same pair apps/integrations keeps
// for the chat turn, kept here because this is the other place it shipped wrong.
//
// Every client a run touches (session, git, todo, the balance gate behind them)
// authorizes on the CALLER's org and never on an argument, so no caller can name
// the tenant it acts for. The org therefore rides the caller. But zip reads a
// STATED caller only where there is NO REQUEST behind the context
// (caller.go:352-356) — otherwise CallerOf reads the request's own headers — so
// cloud.For applied to an inbound request is a SILENT NO-OP.
//
// The coding path had neither half: the run was spawned on a bare
// context.Background() with no statement at all, and the routed target lookup
// ran on the webhook's request context where a statement would have been
// discarded anyway. Every client call in every run answered "authorize: no org on
// the call", which is a run that dies before a model is asked anything.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// A run's context must name the tenant, or every client it touches refuses it.
func TestRunContextStatesTheTenant(t *testing.T) {
	ctx, cancel, _ := runContext("acme", 0, ceiling())
	defer cancel()
	if got := cloud.Who(ctx).Org; got != "acme" {
		t.Fatalf("a run must act for a named tenant, got %q", got)
	}
}

// The statement is only READABLE off a context with no request behind it. This
// is the regression that shipped twice: the same call, the wrong base context,
// silently no org.
func TestStatingOnARequestContextIsANoOp(t *testing.T) {
	if got := cloud.Who(cloud.For(context.Background(), "acme")).Org; got != "acme" {
		t.Fatalf("a stated tenant must be readable off a background context, got %q", got)
	}
	// runContext must not be derivable from a caller-supplied context: it takes
	// an org and a budget (and how far it may be moved) and nothing else, so there is no parameter through
	// which a request could be threaded back in. If this ever grows a
	// context.Context argument the bug returns — the SIGNATURE is the guard.
	var _ func(string, int, time.Duration) (context.Context, context.CancelFunc, *clock) = runContext
}

// An empty org states nothing rather than a blank tenant: downstream must refuse
// on "no org" rather than act for an account named "".
func TestEmptyTenantIsNotStated(t *testing.T) {
	ctx, cancel, _ := runContext("", 0, ceiling())
	defer cancel()
	if got := cloud.Who(ctx).Org; got != "" {
		t.Errorf("an empty org must not become a tenant, got %q", got)
	}
}

// A run outlives the handler that started it by minutes, so its deadline must
// come from the run budget and not from a request that has already been
// answered.
func TestRunContextIsBoundedAndNotAlreadyDone(t *testing.T) {
	ctx, cancel, clk := runContext("acme", 60, ceiling())
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("a fresh run context is already done; the run would be cancelled before it starts")
	default:
	}
	// The run is held to the budget it asked for by the clock, which a caller may
	// move; the context's own deadline is the furthest the clock may be moved to.
	if left := clk.left(time.Now()); left > 2*time.Minute {
		t.Errorf("the caller's budget was ignored: %v left on a 60s run", left)
	}
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a run must be bounded")
	}
	if left := time.Until(d); left > ceiling()+time.Minute {
		t.Errorf("the run's deadline passes its ceiling: %v", left)
	}
}

// The dispatch endpoint must not accept a tenant, a repo that is a path, or a
// run with no human behind it. Each of these is refused BEFORE a slot is taken,
// so a bad request cannot consume capacity.
func TestStartRefusesWhatItMustRefuse(t *testing.T) {
	for name, tc := range map[string]struct{ org, subject, repo, prompt string }{
		"no tenant":                {"", "u", "api", "do a thing"},
		"tenant is a path":         {"../other", "u", "api", "do a thing"},
		"tenant has a dot segment": {"a/../b", "u", "api", "do a thing"},
		"no subject":               {"acme", "", "api", "do a thing"},
		"no repo":                  {"acme", "u", "", "do a thing"},
		"no task":                  {"acme", "u", "api", ""},
		"repo is a path":           {"acme", "u", "../other-org/api", "do a thing"},
		"repo escapes":             {"acme", "u", "a/b", "do a thing"},
	} {
		_, err := Start(context.Background(), tc.org, tc.subject, startIn(tc.repo, tc.prompt), nil)
		if err == nil {
			t.Errorf("%s: accepted; it must be refused before a slot is spent", name)
		}
	}
}

// A run narrates itself into a chat thread, and every value in that narration is
// derived from model output or from repo content the model read. Unescaped, a
// run could page an entire workspace with <!channel>, or post a link with an
// arbitrary URL under Hanzo's name. THIS IS AN INJECTION TEST.
func TestARunCannotInjectSlackMarkup(t *testing.T) {
	hostile := `<!channel> <https://evil.example|click here> & <@U123>`
	for name, payload := range map[string][]byte{
		"in an error":   []byte(`{"status":"error","error":` + q(hostile) + `}`),
		"in a branch":   []byte(`{"status":"done","changed":true,"branch":` + q(hostile) + `}`),
		"in a PR key":   []byte(`{"status":"done","changed":true,"branch":"b","pr":` + q(hostile) + `}`),
		"in a log line": []byte(`{"message":` + q(hostile) + `}`),
		"in a step":     []byte(`{"step":` + q(hostile) + `}`),
	} {
		line, _ := renderLine(kindToolCall, payload)
		if line == "" {
			t.Errorf("%s: rendered nothing; the case is not being exercised", name)
			continue
		}
		if strings.ContainsAny(line, "<>") {
			t.Errorf("%s: raw markup reached the message — a run can broadcast or forge a link: %q", name, line)
		}
		if strings.Contains(line, "&amp;lt;") {
			t.Errorf("%s: double-escaped, the & pass must run first: %q", name, line)
		}
	}
	// One enormous value must not become the whole message.
	long := []byte(`{"message":"` + strings.Repeat("x", 50000) + `"}`)
	if line, _ := renderLine(kindLog, long); len(line) > maxLine+32 {
		t.Errorf("an unbounded value reached the message: %d chars", len(line))
	}
}

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

// startIn builds the endpoint's request. NEITHER half of the identity is a field
// of it — not the tenant and not the subject, which is the whole point of the
// contract — so both are passed to Start separately, exactly as a real endpoint
// passes what it read off the caller.
func startIn(repo, prompt string) client.CodingStartIn {
	return client.CodingStartIn{Repo: repo, Prompt: prompt}
}

// A finished run must always get the last word, and it must say which ending it
// was. A terminal that rendered empty would leave the thread reading "working…"
// forever on a run that is over.
func TestEveryTerminalGetsTheLastWord(t *testing.T) {
	for name, tc := range map[string]struct {
		payload []byte
		want    string
	}{
		"failed":     {[]byte(`{"status":"error","error":"it broke"}`), "it broke"},
		"no changes": {[]byte(`{"status":"done","changed":false}`), "No changes"},
		"pushed":     {[]byte(`{"status":"done","changed":true,"branch":"agent/abc","pr":"ENG-1"}`), "agent/abc"},
	} {
		line, terminal := renderLine(kindStatus, tc.payload)
		if !terminal {
			t.Errorf("%s: not reported as terminal; the edit budget could swallow the run's ending", name)
		}
		if !strings.Contains(line, tc.want) {
			t.Errorf("%s: the thread would not say what happened: %q", name, line)
		}
	}
	// A run in flight is NOT terminal, or the first step would spend the reserve.
	if _, terminal := renderLine(kindStatus, []byte(`{"status":"started","branch":"agent/abc"}`)); terminal {
		t.Error("a started run was treated as terminal")
	}
}

// The last word carries the ADDRESS. A run that pushed a branch and filed a PR
// is finished work, and a thread that names it without saying where to read it
// leaves the person who asked to go hunting for their own change.
func TestTheLastWordSaysWhereToReadIt(t *testing.T) {
	line, _ := renderLine(kindStatus, []byte(
		`{"status":"done","changed":true,"branch":"agent/abc","pr":"ENG-1","url":"https://github.com/hanzo-inc/api/pull/7"}`))
	if !strings.Contains(line, "https://github.com/hanzo-inc/api/pull/7") {
		t.Fatalf("the address never reached the thread: %q", line)
	}
	// BARE, so Slack links it on its own. A `<url|text>` element is the one provider
	// of mrkdwn that carries an arbitrary destination, and nothing a run emits may
	// construct one.
	if strings.Contains(line, "<") || strings.Contains(line, ">") {
		t.Fatalf("the address was wrapped in a link element: %q", line)
	}
	// And a run with no address still gets its last word.
	if l, _ := renderLine(kindStatus, []byte(`{"status":"done","changed":true,"branch":"agent/abc"}`)); !strings.Contains(l, "agent/abc") {
		t.Fatalf("a run without an address lost its ending: %q", l)
	}
}

// The sink reads untrusted payloads. A malformed one must be ignored, never
// panic a run that has already pushed a branch.
func TestProgressSurvivesAHostilePayload(t *testing.T) {
	for _, payload := range [][]byte{
		nil, []byte(""), []byte("not json"), []byte("[]"), []byte(`{"step":123}`), []byte(`{`),
	} {
		if line, _ := renderLine(kindLog, payload); line != "" {
			t.Errorf("an unreadable payload rendered %q; it must change nothing", line)
		}
	}
	// No address means no sink, and a nil sink is a no-op so no caller branches.
	var none *progress
	none.watch(context.Background(), kindLog, []byte(`{"message":"hi"}`))
	if newProgress("acme", "sess_1", client.Reply{}, "") != nil {
		t.Error("a sink was built with nowhere to post")
	}
	// An address on a transport that cannot be edited in place is not an address
	// this sink can use, and it must not be treated as one.
	if newProgress("acme", "sess_1", client.Reply{Provider: "discord", Channel: "C1"}, "") != nil {
		t.Error("a sink was built for a transport with no chat.update")
	}
}

// A run a turn is following posts nothing of its own — the turn shows it — unless
// it is handed a message to carry on in, which nobody else is writing.
func TestARunATurnFollowsPostsNothingOfItsOwn(t *testing.T) {
	turn := client.Reply{Provider: "slack", Channel: "C1", Turn: "sess_turn"}
	for name, tc := range map[string]struct {
		to   client.Reply
		ts   string
		want bool
	}{
		"a turn follows it":                   {turn, "", false},
		"a button carries it on in a message": {turn, "1700000000.000100", true},
		"a slash command started it":          {client.Reply{Provider: "slack", Channel: "C1"}, "", true},
	} {
		if got := narrates(tc.to, tc.ts); got != tc.want {
			t.Errorf("%s: narrates %v, want %v", name, got, tc.want)
		}
	}
}

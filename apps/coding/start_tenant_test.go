package coding

// The tenant a coding run acts for has to travel ON THE WIRE, and these pin the
// two halves of why that is not obvious — the same pair apps/integrations keeps
// for the chat turn, kept here because this is the other place it shipped wrong.
//
// Every seam a run touches (session, git, tracker, the balance gate behind them)
// authorizes on the CALLER's org and never on an argument, so no caller can name
// the tenant it acts for. The org therefore rides the caller. But zip reads a
// STATED caller only where there is NO REQUEST behind the context
// (caller.go:352-356) — otherwise CallerOf reads the request's own headers — so
// cloud.For applied to an inbound request is a SILENT NO-OP.
//
// The coding path had neither half: the run was spawned on a bare
// context.Background() with no statement at all, and the routed target lookup
// ran on the webhook's request context where a statement would have been
// discarded anyway. Every seam call in every run answered "authorize: no org on
// the call", which is a run that dies before a model is asked anything.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// A run's context must name the tenant, or every seam it touches refuses it.
func TestRunContextStatesTheTenant(t *testing.T) {
	ctx, cancel := runContext("acme", 0)
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
	// an org and a budget and nothing else, so there is no parameter through
	// which a request could be threaded back in. If this ever grows a
	// context.Context argument the bug returns — the SIGNATURE is the guard.
	var _ func(string, int) (context.Context, context.CancelFunc) = runContext
}

// An empty org states nothing rather than a blank tenant: downstream must refuse
// on "no org" rather than act for an account named "".
func TestEmptyTenantIsNotStated(t *testing.T) {
	ctx, cancel := runContext("", 0)
	defer cancel()
	if got := cloud.Who(ctx).Org; got != "" {
		t.Errorf("an empty org must not become a tenant, got %q", got)
	}
}

// A run outlives the door that started it by minutes, so its deadline must come
// from the run budget and not from a request that has already been answered.
func TestRunContextIsBoundedAndNotAlreadyDone(t *testing.T) {
	ctx, cancel := runContext("acme", 60)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("a fresh run context is already done; the run would be cancelled before it starts")
	default:
	}
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a run must be bounded")
	}
	if left := time.Until(d); left > 2*time.Minute {
		t.Errorf("the caller's budget was ignored: %v left on a 60s run", left)
	}
}

// The dispatch door must not accept a tenant, a repo that is a path, or a run
// with no human behind it. Each of these is refused BEFORE a slot is taken, so a
// bad request cannot consume capacity.
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

// The pool is the availability isolation: one tenant may not exhaust the
// sandbox capacity every other tenant shares.
func TestOneTenantCannotStarveTheOthers(t *testing.T) {
	l := newLimiter(4, 2)
	if !l.acquire("acme") || !l.acquire("acme") {
		t.Fatal("an org could not reach its own cap")
	}
	if l.acquire("acme") {
		t.Fatal("an org exceeded its per-org cap; one workspace could take the whole pool")
	}
	if !l.acquire("other") {
		t.Fatal("a second org was starved by the first")
	}
	// A refusal must not leak a slot, or the pool bleeds down to nothing.
	l.release("acme")
	if !l.acquire("acme") {
		t.Fatal("a released slot was not reusable; refusals leak capacity")
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

// startIn builds the door's request. NEITHER half of the identity is a field of
// it — not the tenant and not the subject, which is the whole point of the
// contract — so both are passed to Start separately, exactly as a real door
// passes what it read off the caller.
func startIn(repo, prompt string) plane.CodingStartIn {
	return plane.CodingStartIn{Repo: repo, Prompt: prompt}
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
	// BARE, so Slack links it on its own. A `<url|text>` element is the one piece
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
	if newProgress("acme", "", "") != nil {
		t.Error("a sink was built with nowhere to post")
	}
}

package coding

// progress.go reports a run into a chat thread while it is still running.
//
// # Build for the stream that exists
//
// Slack has no server-sent events. There is no socket to push a token down and
// no way for a client to subscribe to a run. What the platform actually offers
// is chat.update: post one message, get its id, and rewrite that message as
// often as you like. So this posts ONE message into the thread and edits it —
// the run's status line, in place — instead of the dozen messages a
// phase-per-message design would bury the channel under.
//
// # The engine does not hold the token
//
// It says "put this text at this address" over the plane, and the process that
// owns the workspace's bot credential does the posting (integrations_slack_send,
// which takes the org from the caller and never from an argument). The engine
// therefore reports into a workspace it cannot otherwise reach: it has no bot
// token, so a compromised run cannot post anywhere but the thread it was asked
// from, and cannot read that workspace at all.
//
// # Everything here is best-effort, and that is a decision
//
// A run's work — the branch, the commit, the PR — has already happened by the
// time most of these fire. A failed edit must never fail a run that succeeded,
// so every error is swallowed after being counted out of the retry budget. The
// opposite choice would let a Slack outage roll back real work.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// editBudget bounds how many edits one run spends. A run emits a line per step
// and a hostile prompt can make a model emit a great many; without a cap, one
// run could issue thousands of chat.update calls and exhaust the workspace's
// Slack rate limit for every other feature that shares the token. When the
// budget runs out the message simply stops moving — the terminal card still
// posts, because that one is reserved below.
const editBudget = 60

// editFloor is the minimum gap between two edits of the same message. Slack's
// chat.update tier allows roughly one per second per channel; a run narrating
// faster than that gets coalesced rather than throttled, so the thread shows the
// LATEST state instead of a backlog of stale ones.
const editFloor = time.Second

// progress is one run's status line in one thread.
type progress struct {
	org, channel, thread string

	mu      sync.Mutex
	ts      string // the message being rewritten; empty until the first post lands
	spent   int
	lastAt  time.Time
	lastMsg string
	dead    bool // a post/edit failed in a way that will not get better
}

// newProgress returns a sink for the given address, or nil when there is no
// address — a nil *progress's methods are no-ops, so the caller never branches.
func newProgress(org, channel, thread string) *progress {
	if strings.TrimSpace(channel) == "" {
		return nil
	}
	return &progress{org: org, channel: channel, thread: thread}
}

// watch is the Dispatcher.Watch seam: it turns one mirrored session event into
// the message's next state. It reads only the fields it needs and never the
// whole payload, and it cannot see a credential because no payload the run
// mirrors carries one.
func (p *progress) watch(ctx context.Context, kind string, payload []byte) {
	if p == nil {
		return
	}
	line, terminal := renderLine(kind, payload)
	if line == "" {
		return
	}
	p.set(ctx, line, terminal)
}

// renderLine turns one mirrored session event into the message's next state.
// PURE, so what a thread will say about a run is testable without a Slack, and
// so the injection rules below are checkable as themselves rather than as a
// side effect of posting.
//
// An unparsable or uninteresting event renders "" and changes nothing: a run
// that has already pushed a branch must not be disturbed by a payload it cannot
// read. terminal reports the last word, which is always spent even when the
// edit budget is gone — a finished run must never leave a thread reading
// "working…".
func renderLine(kind string, payload []byte) (line string, terminal bool) {
	var e struct {
		Step    string `json:"step"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Branch  string `json:"branch"`
		PR      string `json:"pr"`
		URL     string `json:"url"`
		Error   string `json:"error"`
		Changed bool   `json:"changed"`
	}
	if json.Unmarshal(payload, &e) != nil {
		return "", false
	}
	// EVERY value below is untrusted. A step name, a log line, a branch and an
	// error are all derived from model output or from repo content the model
	// read, and this text is posted into a Slack channel as mrkdwn. Unescaped, a
	// run could emit `<!channel>` and page a whole workspace, or a link element
	// and put an arbitrary URL under Hanzo's name. Escaped once, HERE, where the
	// value meets the markup — not at the transport, which would double-escape
	// text that was already safe.
	terminal = e.Status == "done" || e.Status == "error"
	switch {
	case e.Status == "error":
		return ":x: " + esc(firstLine(e.Error)), true
	case e.Status == "done" && !e.Changed:
		return ":white_check_mark: No changes were needed.", true
	case e.Status == "done":
		l := ":sparkles: Pushed `" + esc(e.Branch) + "`"
		if e.PR != "" {
			l += " · PR `" + esc(e.PR) + "`"
		}
		// The address goes out BARE. Slack turns a plain URL into a link on its
		// own, so nothing here has to build `<url|text>` — which is the one markup
		// element that carries an arbitrary destination, and therefore the one
		// esc() exists to stop a run from writing. A link nobody had to construct
		// cannot be constructed by a prompt.
		if e.URL != "" {
			l += " " + esc(e.URL)
		}
		return l, true
	case e.Status == "started":
		return ":hourglass_flowing_sand: Working `" + esc(e.Branch) + "`…", false
	case kind == kindToolCall && e.Step != "":
		l := ":gear: " + esc(e.Step)
		if e.Message != "" {
			l += " — " + esc(firstLine(e.Message))
		}
		return l, false
	case e.Message != "":
		return ":speech_balloon: " + esc(firstLine(e.Message)), false
	}
	return "", terminal
}

// set moves the message to text, posting it the first time and editing it after.
func (p *progress) set(ctx context.Context, text string, terminal bool) {
	p.mu.Lock()
	if p.dead || text == p.lastMsg {
		p.mu.Unlock()
		return
	}
	if !terminal {
		if p.spent >= editBudget || time.Since(p.lastAt) < editFloor {
			p.mu.Unlock()
			return
		}
	}
	p.spent++
	p.lastAt = time.Now()
	p.lastMsg = text
	ts := p.ts
	p.mu.Unlock()

	// A short, independent deadline: a wedged Slack must not hold a run's step
	// open, and this call is not on the run's critical path.
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := plane.Ask[plane.SlackSendIn, plane.SlackSent](sctx, "integrations", plane.IntegrationsSlackSend,
		&plane.SlackSendIn{Channel: p.channel, Thread: p.thread, Text: text, Update: ts})
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		// One failure is transient; a failure with nothing posted yet means there
		// is no message to edit and never will be. Stop trying either way once the
		// budget is gone, so a broken workspace costs a run one call and not sixty.
		if p.ts == "" {
			p.dead = true
		}
		return
	}
	if out != nil && out.TS != "" && p.ts == "" {
		p.ts = out.TS // first post: remember what to rewrite
	}
}

// esc neutralizes the three mrkdwn-meaningful characters so agent-derived text
// cannot inject a link or a <!channel> broadcast, and caps the length so one
// enormous log line cannot become the whole message. & goes first, or the
// entities it writes get escaped again by the two that follow.
func esc(s string) string {
	if len(s) > maxLine {
		s = s[:maxLine] + "…"
	}
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// maxLine bounds one interpolated value. Slack truncates a long message anyway;
// this makes the truncation ours, so the status line stays readable and a
// hostile prompt cannot push the run's actual state off the end of it.
const maxLine = 300

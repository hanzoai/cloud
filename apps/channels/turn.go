package channels

// turn.go — the agent answering on a channel.
//
// This is the half integrations used to carry beside the inbox: every adapter
// emitted an event here AND separately spawned an agent turn of its own, so one
// message drove two mechanisms and only the second one ever replied. The turn
// belongs on this side because everything it needs is already here — the policy
// gate that decides whether a sender may speak, the route that says where a
// reply goes, and the four egress doors.
//
// What is NOT here is custody. Which Hanzo account a chat user has linked is
// integrations' to answer (plane.ChatIdentity) because the link lives in KMS
// under that subsystem; only the answer crosses, never a token.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// turnTracer is a var so a test can rebind it: a span that is created and never
// exported is exactly the failure worth catching.
var turnTracer = otel.Tracer("hanzo.channels")

const (
	// turnBudget bounds one turn end to end: identity, the model completion, and
	// the post back to the platform. Generous, because a run is a real completion.
	turnBudget = 110 * time.Second
	// turnsAtOnce caps simultaneous turns across ALL orgs, so no one tenant can
	// exhaust goroutines. turnsPerOrg is a fraction of it, so no one tenant can
	// starve the others.
	turnsAtOnce = 32
	turnsPerOrg = 8
)

var (
	poolOnce sync.Once
	pool     *limiter
)

// limiter is a global cap with a per-org sub-cap. A turn holds one of each.
type limiter struct {
	all chan struct{}
	mu  sync.Mutex
	per map[string]int
	cap int
}

func newLimiter(all, per int) *limiter {
	return &limiter{all: make(chan struct{}, all), per: map[string]int{}, cap: per}
}

// take reports whether a slot was acquired. It never blocks: a full pool is an
// answer, not a queue, and the caller sheds rather than piling up goroutines
// holding untrusted input.
func (l *limiter) take(org string) bool {
	select {
	case l.all <- struct{}{}:
	default:
		return false
	}
	l.mu.Lock()
	if l.per[org] >= l.cap {
		l.mu.Unlock()
		<-l.all
		return false
	}
	l.per[org]++
	l.mu.Unlock()
	return true
}

func (l *limiter) done(org string) {
	l.mu.Lock()
	if l.per[org]--; l.per[org] <= 0 {
		delete(l.per, org)
	}
	l.mu.Unlock()
	<-l.all
}

// spawn runs a turn in a recovered goroutine, or reports that the pool is full.
//
// The recover is not optional. A turn walks a model completion and a platform
// post over UNTRUSTED input on a shared multi-tenant process, and middleware
// recovery wraps only the synchronous request goroutine — an unrecovered panic
// here would take every tenant down with it. The slot is released on every exit,
// panicking or not, because a pool that leaks slots stops answering.
func spawn(s *cloud.Service[state], org string, run func()) bool {
	poolOnce.Do(func() { pool = newLimiter(turnsAtOnce, turnsPerOrg) })
	if !pool.take(org) {
		s.Log.Warn("channels: turn shed, pool full", "org", org)
		return false
	}
	go func() {
		defer pool.done(org)
		defer func() {
			if r := recover(); r != nil {
				s.Log.Error("channels: turn panic (recovered)", "org", org, "err", r)
			}
		}()
		run()
	}()
	return true
}

// turn answers one message and sends the answer back where it came from.
func turn(s *cloud.Service[state], tr transport, org string, m Message) {
	ctx, cancel := turnContext(org)
	defer cancel()

	// The turn's own span, and the one record that ties a CONVERSATION to a run.
	//
	// The webhook's request span ended when the adapter answered the platform 200,
	// and everything worth observing happens after that, here, on a detached
	// context. So "someone asked @hanzo something in this thread" and "a run
	// happened" were two facts with nothing in common.
	//
	// It carries the run id explicitly because the trace cannot be relied on to:
	// this context has no inbound request behind it, so on a split deployment the
	// run's spans sit in a trace of their own. The id is the join that holds
	// either way — thread → this span → run_id → the run row → its own spans.
	ctx, span := turnTracer.Start(ctx, "agent.turn "+m.Channel, trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(
		// hanzo.org is the key the trace plane files a row under; without it this
		// span belongs to the platform rather than the tenant, and the tenant
		// cannot read its own conversation.
		attribute.String("hanzo.org", org),
		attribute.String("hanzo.chat.provider", m.Channel),
	)
	if m.Room.ID != "" {
		span.SetAttributes(attribute.String("hanzo.chat.channel", m.Room.ID))
	}
	if m.ReplyTo != "" {
		span.SetAttributes(attribute.String("hanzo.chat.thread", m.ReplyTo))
	}

	text, ephemeral, runID := answer(ctx, s, org, m)
	if runID != "" {
		span.SetAttributes(attribute.String("hanzo.agent.run_id", runID))
	}
	if text == "" {
		return
	}
	// An ephemeral answer carries a sign-in URL and must reach only the person
	// who asked. No transport here has a private reply, so it is not sent to the
	// room — saying it to everyone is worse than not saying it.
	if ephemeral {
		s.Log.Debug("channels: reply withheld, needs a private channel", "channel", m.Channel)
		return
	}
	if _, err := tr.send(ctx, s, org, Message{
		Channel: m.Channel,
		Account: m.Account,
		Room:    m.Room,
		ReplyTo: m.ReplyTo,
		Text:    text,
	}); err != nil {
		s.Log.Warn("channels: reply", "channel", m.Channel, "err", err)
	}
}

// turnContext is the context ONE turn runs on: detached from any request,
// carrying the tenant it bills, bounded by the turn budget.
//
// Both halves are load-bearing. A run bills, and the balance gate takes the org
// from the CALLER's identity and never from an argument, so the org has to ride
// the caller. cloud.For states it — but a stated caller is read only where there
// is NO REQUEST behind the context, deliberately, so that stating an identity can
// never override an authenticated one. On a webhook's ctx it is a silent no-op.
//
// Detaching is independently required: the adapter answered the platform long
// ago, so on its context the completion would be cancelled immediately.
//
// It is not a laundering hole. For supplies a tenant where there is none and
// cannot override one, and this org came from OrgForExternalID on a
// signature-verified payload — never from a field a client chose.
func turnContext(org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(context.Background(), org), turnBudget)
}

// answer resolves who the asker is and runs the agent as them, or returns the
// sentence to say instead. Every string it returns is safe to post: internal
// failures are logged and surfaced as one terse line, never as an error a
// platform would render.
func answer(ctx context.Context, s *cloud.Service[state], org string, m Message) (reply string, ephemeral bool, runID string) {
	who, err := plane.Ask[plane.ChatIdentityIn, plane.ChatIdentityOut](ctx, "integrations", plane.ChatIdentity,
		&plane.ChatIdentityIn{Org: org, Provider: m.Channel, ExternalID: m.Account, User: m.Sender.ExternalID})
	if err != nil || who == nil {
		// SILENT, and deliberately. This is a transport failure — the identity door
		// is unreachable — which is our problem and not the asker's. Answering it
		// would post an apology into every room on every message for as long as a
		// socket is down, which is worse than saying nothing and is not a fact the
		// person can act on. A LINKED-ACCOUNT problem is different and is told to
		// them below, because that one they can fix.
		s.Log.Warn("channels: identity unreachable, turn dropped", "channel", m.Channel, "org", org, "err", err)
		return "", false, ""
	}
	if who.Say != "" {
		// No run happened, so there is nothing to attribute and nothing to bill.
		return who.Say, who.Ephemeral, ""
	}

	run, err := plane.Ask[plane.RunOnBehalfIn, plane.RunOnBehalfOut](ctx, "agents", plane.AgentsRunOnBehalf,
		&plane.RunOnBehalfIn{Org: org, Subject: who.Subject, Ref: agentFor(m.Channel), Input: m.Text, Model: who.Model})
	if err != nil {
		s.Log.Warn("channels: agent run", "channel", m.Channel, "org", org, "err", err) // never a token
		return "Sorry — the agent hit an error handling that. Please try again.", false, ""
	}
	if run == nil || run.Status != "ok" {
		// SAID, not merely returned. A run that EXECUTED and whose model failed
		// comes back with a non-"ok" status and a nil error, so this branch — not
		// the one above — is where a broken inference path lands. It logged
		// nothing once, and the whole failure was therefore invisible: the op
		// answered 200 and the bridge posted its generic sentence. The run id is
		// here so the row is findable.
		status, id := "", ""
		if run != nil {
			status, id = run.Status, run.RunID
		}
		s.Log.Warn("channels: agent run did not succeed", "channel", m.Channel, "org", org, "status", status, "run_id", id)
		return "Sorry — the agent hit an error handling that. Please try again.", false, id
	}
	if strings.TrimSpace(run.Output) == "" {
		return "(the agent returned an empty response)", false, run.RunID
	}
	return run.Output, false, run.RunID
}

// agentFor names the agent a channel's turns run. One agent for every channel
// unless a deployment says otherwise, because the answer should not depend on
// which app someone happened to open.
func agentFor(channel string) string {
	if v := strings.TrimSpace(os.Getenv(strings.ToUpper(channel) + "_AGENT_REF")); v != "" {
		return v
	}
	return "hanzo"
}

package coding

// steer.go — stopping, pausing and redirecting a coding run while it works.
//
// A run is one `dev exec` in a sandbox: it takes its task on the command line
// and cannot be told anything after. So what a person says to a run in flight —
// "no, use the other endpoint" — cannot reach it, and the honest way to take it
// is the one a person would: stop, keep what was done, and start again from
// there with the new instruction.
//
// KEEPING THE WORK is the part that makes that true rather than a restart. A run
// that is stopped or paused has its command interrupted and then does what it
// does at the end of a finished run — commits the checkout and pushes it to the
// run's own branch (sandboxrunner.go keep). A run REDIRECTED by a new instruction
// commits and pushes nothing (#553): the follow-up is started with After naming
// it, resumes its sandbox, finds the work in the tree where it was left, and
// pushes it once its own checks are done.
//
// ONE REGISTRY, ONE CANCEL. Each sandbox run is a flight here, and its session
// is held with agents (agents.Hold), so a stop or a pause from ANY surface — the
// session API, a button, a word, the reaper — reaches the run as its context's
// cause, and the run keeps its work the same way whoever asked. A paused run
// keeps its flight, and its hold, so a Continue can follow it up and a Stop can
// let it go.
//
// The run is process-local: its goroutine and its sandbox call are in this process
// and nowhere else. But a person's word reaches whichever pod took the request, so a
// pod that does not fly the run asks the others, and the one that does acts on it
// (fleet.go); the run itself is never moved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	"github.com/zap-proto/zip"
)

// errFollowed is how a run redirected by a new instruction is stopped. It is a
// stop — the run keeps its work and its session ends — with one thing more to
// say: a follow-up takes it from here.
var errFollowed = fmt.Errorf("%w: for a new instruction", agents.ErrStopped)

// errTimeUp is how a run whose budget ran out is ended. It is a stop — the run
// keeps its work on its branch and its session ends — with one thing more to say:
// the budget is why, and a follow-up (`after`) carries the task on. That is how a
// task longer than one run, a migration over days, goes on.
var errTimeUp = fmt.Errorf("%w: its time ran out", agents.ErrStopped)

// kept reports whether a run that ended with cause keeps its work: it was
// stopped, paused or redirected, rather than finished, failed or out of time.
func kept(cause error) bool {
	return errors.Is(cause, agents.ErrStopped) || errors.Is(cause, agents.ErrPaused)
}

// errDrain is how a run is stopped when the pod driving it is being replaced: a stop that
// keeps the work, after which the run parks to go on at once on whichever pod is up next.
var errDrain = fmt.Errorf("%w: the platform is restarting", agents.ErrStopped)

// followWait bounds waiting for a redirected run to keep its work and let go:
// the interrupt, a commit and a push.
var followWait = 3 * time.Minute

// flight is one sandbox run this process is executing, or one it paused.
type flight struct {
	org, subject, session string
	// in is what the run was asked, with the base it started from, which is what
	// a follow-up is asked from.
	in     client.CodingStartIn
	sink   *progress
	ctx    context.Context // the run's own, done when it is stopped or paused
	clock  *clock          // the time it is held to, which Extend moves (extend.go)
	cancel context.CancelCauseFunc
	done   chan struct{} // closed when the run has ended and let everything go
	letGo  func()        // releases the session's hold
	ticket string        // the digest of the run's ask ticket (key.go)
	fleet  *fleet        // the registry it flies in

	// draining serializes the reads of the run's inbox (inbox.go), and unlisten ends
	// the flight's subscription to it. unlisten is held by mu.
	draining sync.Mutex
	drains   atomic.Int64   // how many times the inbox has been read
	handed   map[int64]bool // the rows (by seq) this run's turn has been handed; held by draining
	unlisten func()

	mu      sync.Mutex
	paused  bool              // it ended paused, and is waiting for a Continue or a Stop
	pushed  bool              // its work so far is on its branch
	claimed bool              // a follow-up has taken it
	steerer func(string) bool // queues an instruction into an active dev turn without stopping it
	// servers are the org's MCP servers its kit carries, by tool-plane id: the
	// only ones its ticket opens (Relay). Empty until the kit is resolved.
	servers map[string]bool
	// tools are the admitted, unmuted tool names for each MCP server in the
	// run's kit, enforced at Relay before the tool plane is asked.
	tools map[string][]string
	// relays holds a slot for each relayed request in flight (relayCalls), and
	// paced/pacedAt are the run's token bucket (relayRate, relayBurst).
	relays  chan struct{}
	paced   float64
	pacedAt time.Time
}

// fly registers f and holds its session with agents, so every stop and pause of
// the session reaches it.
func fly(f *flight) { flights.fly(f) }

func (fl *fleet) fly(f *flight) {
	f.fleet = fl
	fl.Lock()
	fl.m[f.session] = f
	if f.ticket != "" {
		fl.tickets[f.ticket] = f
	}
	fl.Unlock()
	f.letGo, _ = agents.Hold(f.session, f.interrupt)
	// What was said to the run while no pod held it (inbox.go).
	f.listen()
}

// interrupt is what a stop or a pause of the session does to the flight: a run
// in flight is cancelled with the cause, and a paused one that is stopped is
// forgotten — nothing is left to continue.
func (f *flight) interrupt(cause error) {
	f.mu.Lock()
	paused := f.paused
	f.mu.Unlock()
	if !paused {
		f.cancel(cause)
		return
	}
	if errors.Is(cause, agents.ErrStopped) {
		f.forget()
	}
}

// land records how the run ended, and keeps the flight only when it paused.
func (f *flight) land(res Result, cause error) {
	f.mu.Lock()
	f.pushed = res.Changed
	f.paused = errors.Is(cause, agents.ErrPaused)
	paused := f.paused
	f.mu.Unlock()
	if !paused {
		f.forget()
	}
	close(f.done)
}

// Handoff stops every run this process is driving, keeping its work and parking it to go on
// by itself on the next pod, and answers how many had landed when ctx ended.
func Handoff(ctx context.Context) int { return flights.handoff(ctx) }

func (fl *fleet) handoff(ctx context.Context) int {
	fl.Lock()
	live := make([]*flight, 0, len(fl.m))
	for _, f := range fl.m {
		f.mu.Lock()
		if !f.paused {
			live = append(live, f)
		}
		f.mu.Unlock()
	}
	fl.Unlock()
	for _, f := range live {
		f.cancel(errDrain)
	}
	landed := 0
	for _, f := range live {
		select {
		case <-f.done:
			landed++
		case <-ctx.Done():
			return landed
		}
	}
	return landed
}

// forget lets the flight go: out of the registry, and its session's hold released.
func (f *flight) forget() {
	fl := f.fleet
	fl.Lock()
	if fl.m[f.session] == f {
		delete(fl.m, f.session)
	}
	if fl.tickets[f.ticket] == f {
		delete(fl.tickets, f.ticket)
	}
	fl.Unlock()
	f.mu.Lock()
	off := f.unlisten
	f.unlisten = nil
	f.mu.Unlock()
	if off != nil {
		off()
	}
	if f.letGo != nil {
		f.letGo()
	}
}

// find is the flight in says: the session it names, or the one its subject has
// running in its conversation.
func find(org string, in client.CodingSteerIn) *flight { return flights.find(org, in) }

func (fl *fleet) find(org string, in client.CodingSteerIn) *flight {
	fl.Lock()
	defer fl.Unlock()
	if id := strings.TrimSpace(in.SessionID); id != "" {
		if f := fl.m[id]; f != nil && f.org == org {
			return f
		}
		return nil
	}
	for _, f := range fl.m {
		f.mu.Lock()
		running := !f.paused
		f.mu.Unlock()
		to := f.in.Reply
		if f.org == org && f.subject == in.Subject && running && to.Provider == in.Reply.Provider &&
			to.Channel == in.Reply.Channel && to.Thread == in.Reply.Thread {
			return f
		}
	}
	return nil
}

// Follow gives a subject's coding run a new instruction — or continues a paused
// one — the only way a run can take one: it is stopped with its work kept on its
// branch, and a follow-up run starts from that branch with the task and the
// instruction, narrating into the same message.
//
// org and subject are the caller's, as for Start. The run must be the subject's
// own: anyone else's is refused. It answers once the run has been told, on whichever
// pod flies it; the answer says Queued when the instruction is kept and the run has not
// taken it yet. A run no pod flies right now, between the halves of a rolling restart or
// while it is resumed, keeps the instruction in its inbox for the pod that will
// (inbox.go); one that has ended refuses it. The follow-up starts when the run has let
// go, and a follow-up that cannot start says so in the run's message.
//
// With a request id the instruction is said once, however often and wherever it is
// asked: a retry answers what the first answered (ledger.go).
func Follow(ctx context.Context, org, subject string, in client.CodingSteerIn, log func(msg string, kv ...any)) (client.CodingAck, error) {
	st, err := flights.follow(ctx, org, subject, in, log)
	return client.CodingAck{OK: err == nil, Queued: st.Queued}, err
}

// steered is what a steer answered, as the ledger keeps it: whether the instruction is
// kept for a run that has not taken it yet, in its inbox, rather than delivered.
type steered struct {
	Queued bool `json:"queued"`
}

func (fl *fleet) follow(ctx context.Context, org, subject string, in client.CodingSteerIn, log func(msg string, kv ...any)) (steered, error) {
	return requested(ctx, fl.ledger(), org, subject, scoped("steer", in.SessionID), in.RequestID,
		printOf(struct{ Session, Text string }{strings.TrimSpace(in.SessionID), strings.TrimSpace(in.Text)}),
		func(ctx context.Context) (steered, error) {
			if f := fl.find(org, in); f != nil {
				return f.followed(ctx, subject, in, log)
			}
			return fl.followElsewhere(ctx, org, subject, in)
		})
}

func (f *flight) setSteerer(fn func(string) bool) {
	f.mu.Lock()
	f.steerer = fn
	f.mu.Unlock()
	// A turn that can be told things has begun: what waited for one is handed to it.
	if fn != nil && f.fleet != nil {
		go f.drain()
	}
}

// followed is the redirect of a run this pod flies. Where a ledger is kept the
// instruction is a row first (inbox.go) and the flight hands rows to the run by seq, so
// it is recorded before it is applied, and a replay finds it recorded. It answers Queued
// when the row is still waiting: another read of the inbox is in progress, and it will
// hand over this row after the ones before it.
func (f *flight) followed(ctx context.Context, subject string, in client.CodingSteerIn, log func(msg string, kv ...any)) (steered, error) {
	if f.subject != subject {
		return steered{}, zip.ErrForbidden("coding: only the person whose run this is can steer it")
	}
	var l *ledger
	if f.fleet != nil {
		l = f.fleet.ledger()
	}
	if l == nil || strings.TrimSpace(in.Text) == "" {
		// Nothing is kept here (or the word is "continue", which says nothing to keep).
		if f.turn(in.Text, in.RequestID, log) {
			return steered{}, nil
		}
		return steered{}, f.redirect(subject, in, log)
	}
	if f.following() {
		return steered{}, zip.Errorf(http.StatusConflict, "coding: that run is already being followed up")
	}
	rid, err := ident(in.RequestID)
	if err != nil {
		return steered{}, err
	}
	// A run whose harness takes nothing while it works is told by being stopped, and its
	// follow-up carries what is queued in one task: an instruction that would not fit there
	// beside the rest is refused now, not accepted and never delivered.
	if !f.live() {
		if err := l.room(ctx, f.org, f.session, rid, f.base(), strings.TrimSpace(in.Text)); err != nil {
			return steered{}, err
		}
	}
	if err := l.enqueue(ctx, f.org, f.session, subject, rid, strings.TrimSpace(in.Text)); err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return steered{}, err
		}
		return steered{}, unavailable(err)
	}
	if !f.tryDrain() {
		go f.drain()
		return steered{Queued: true}, nil
	}
	waiting, err := l.waiting(ctx, f.org, f.session, rid)
	if err != nil {
		return steered{}, unavailable(err)
	}
	return steered{Queued: waiting}, nil
}

// turn queues text into the dev turn running now, without stopping it, and says
// whether the turn took it. The instruction is recorded on the session as a message,
// with the request id it came with, so a person unsure whether it arrived can read it
// there.
func (f *flight) turn(text, request string, log func(msg string, kv ...any)) bool {
	f.mu.Lock()
	steerer := f.steerer
	paused := f.paused
	claimed := f.claimed
	f.mu.Unlock()

	text = strings.TrimSpace(text)
	if text == "" || paused || claimed || steerer == nil || !steerer(text) {
		return false
	}
	as := paying(zip.WithCaller(context.Background(), zip.Caller{Org: f.org, User: f.subject}), f.ctx)
	said := map[string]any{"role": "user", "text": text}
	if request = strings.TrimSpace(request); request != "" && !strings.HasPrefix(request, "steer_") {
		said["requestId"] = request
	}
	b, _ := json.Marshal(said)
	if err := Engine(log).Sessions.Log(as, f.org, f.session, kindMessage, f.subject, b); err != nil && log != nil {
		log("coding: an in-turn steer message was not recorded on the session", "org", f.org, "session", f.session, "err", err)
	}
	return true
}

// redirect stops the run, keeping its work, and starts a follow-up that carries the
// instruction. A run already being followed up refuses a second.
func (f *flight) redirect(subject string, in client.CodingSteerIn, log func(msg string, kv ...any)) error {
	f.mu.Lock()
	if f.claimed {
		f.mu.Unlock()
		return zip.Errorf(http.StatusConflict, "coding: that run is already being followed up")
	}
	f.claimed = true
	paused := f.paused
	f.mu.Unlock()
	if !paused {
		f.interrupt(errFollowed)
	}
	wait := followWait
	go f.followAfter(subject, in, paused, wait, log)
	return nil
}

// follow starts the follow-up once the run has let go. It is detached from the
// caller that asked: the run is already stopped, so the follow-up is owed
// whether or not anyone is still waiting on the answer.
func (f *flight) follow(subject string, in client.CodingSteerIn, paused bool, log func(msg string, kv ...any)) {
	f.followAfter(subject, in, paused, followWait, log)
}

func (f *flight) followAfter(subject string, in client.CodingSteerIn, paused bool, wait time.Duration, log func(msg string, kv ...any)) {
	// The follow-up acts as the subject, as the run it follows did. It is stated
	// on a context of its own because this call came from a surface that resolved
	// them, and a statement is read only where no request is behind it. It names
	// the person and nothing about what they may do: a paused run can be followed
	// long after it started, and GitHub and IAM are asked again (github.go). It is
	// paid by what the run was paid by (paying), read off the run's own context.
	as := paying(zip.WithCaller(context.Background(), zip.Caller{Org: f.org, User: subject}), f.ctx)
	// Whoever is showing the stopped run is told how it was carried on, on the
	// stopped run's own session: the follow-up that took it, or why none did. A
	// turn following the run reads this to follow the follow-up in the same
	// message (apps/agents/follow.go).
	carried := func(payload map[string]any) {
		payload["status"] = "followed"
		b, _ := json.Marshal(payload)
		if err := Engine(log).Sessions.Log(as, f.org, f.session, kindStatus, subject, b); err != nil && log != nil {
			log("coding: a run's follow-up was not recorded on it", "org", f.org, "session", f.session, "err", err)
		}
	}
	fail := func(err error) {
		if log != nil {
			log("coding: a follow-up run did not start", "org", f.org, "session", f.session, "err", err)
		}
		f.sink.set(as, ":x: "+esc("The follow-up run could not start: "+firstLine(err.Error())), true, client.Controls{})
		carried(map[string]any{"error": err.Error()})
	}
	select {
	case <-f.done:
	case <-time.After(wait):
		fail(fmt.Errorf("the run did not stop in time"))
		return
	}
	if paused {
		// The paused run is over now: the follow-up is what carries it on.
		f.forget()
		if err := Engine(log).Sessions.Close(as, f.org, f.session, statusStopped); err != nil && log != nil {
			log("coding: a continued run's session was not closed", "org", f.org, "session", f.session, "err", err)
		}
	}
	// What the run's own turn was handed and its mark missed is delivered, not repeated;
	// what is still queued rides with the follow-up, which is started after this session
	// and carries it (startSession).
	f.settle()
	next := f.in
	next.Prompt = followUp(f.in.Prompt, in.Text, f.pushed, !paused)
	// Always carry After so the follow-up reuses the earlier run's sandbox and
	// branch even when steered before the first commit was pushed.
	next.After = f.session
	if f.pushed {
		next.Base = "" // from the branch the work is on
	}
	// Where the follow-up is shown: the message a button was pressed on, else the
	// stopped run's own. A run a turn was following has no message of its own —
	// the turn follows the follow-up too, told by carried below.
	ts := strings.TrimSpace(in.Update)
	if ts == "" {
		ts = f.sink.message()
	}
	acc, err := start(as, f.org, subject, next, log, ts)
	if err != nil {
		fail(err)
		return
	}
	followed := map[string]any{"next": acc.SessionID}
	if acc.Note != "" {
		followed["carried"], followed["left"], followed["dropped"], followed["note"] = acc.Carried, acc.Left, acc.Dropped, acc.Note
	}
	carried(followed)
}

// followUp is the follow-up's task: what was asked, and what was said since.
// steered is a run stopped for this instruction, which leaves its work in the
// checkout the follow-up resumes, uncommitted, rather than on the branch.
func followUp(task, said string, pushed, steered bool) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(task))
	switch {
	case pushed:
		b.WriteString("\n\nAn earlier run already worked on this; its work so far is on this branch. Build on it.")
	case steered:
		b.WriteString("\n\nAn earlier run was stopped for this instruction. When this checkout holds its work, it is " +
			"there uncommitted or in commits not yet pushed (git status, git diff, git log): build on it.")
	}
	if said = strings.TrimSpace(said); said != "" {
		b.WriteString("\n\nThen the person who asked said:\n")
		b.WriteString(said)
	} else {
		b.WriteString("\n\nContinue where the earlier run left off.")
	}
	return b.String()
}

// ErrNoTicket is a ticket that opens nothing: never minted, or its run is over.
var ErrNoTicket = errors.New("coding: that ticket names no run in flight")

// Holds reports whether ticket names a run in flight, on this pod or another. It
// answers an error when the other pods could not all be asked (ErrPods), because
// then the run may be on one of them.
func Holds(ticket string) (bool, error) { return flights.holds(ticket) }

func held(ticket string) *flight { return flights.held(ticket) }

func (fl *fleet) held(ticket string) *flight {
	if ticket == "" {
		return nil
	}
	fl.Lock()
	defer fl.Unlock()
	return fl.tickets[ticketKey(ticket)]
}

func (fl *fleet) holds(ticket string) (bool, error) {
	if fl.held(ticket) != nil {
		return true, nil
	}
	if ticket == "" {
		return false, nil
	}
	_, found, err := fl.elsewhere(context.Background(), fleetHolds, ticket)
	return found, err
}

// Ask is ask_user for the run ticket names (key.go): the question goes to the
// person the run is for, in the thread it narrates into, and the call waits for
// the answer as agents.Ask does.
//
// It waits on the RUN as well as on ctx, the caller's: a run stopped or paused
// while it waits stops waiting, so a Stop or a Pause still works on a run that is
// blocked on a question, and the question is closed with it.
//
// The run may be flown by another pod than the one the harness's request landed on;
// that pod asks, and this one waits, and stops waiting when ctx ends (the harness went
// away), which ends the question there too.
func Ask(ctx context.Context, ticket string, in client.AskIn) (string, error) {
	return flights.ask(ctx, ticket, in)
}

func (fl *fleet) ask(ctx context.Context, ticket string, in client.AskIn) (string, error) {
	if f := fl.held(ticket); f != nil {
		return f.asked(ctx, in)
	}
	v, found, err := fl.elsewhere(ctx, fleetAsk, ticketCall{Ticket: ticket, In: in})
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNoTicket
	}
	if err := v.err(); err != nil {
		return "", err
	}
	var text string
	if json.Unmarshal(v.Out, &text) != nil {
		return "", errUnreadable
	}
	return text, nil
}

// asked is the question of a run this pod flies.
func (f *flight) asked(ctx context.Context, in client.AskIn) (string, error) {
	f.mu.Lock()
	paused := f.paused
	f.mu.Unlock()
	if paused {
		return "", errors.New("coding: the run is paused")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(f.ctx, func() { cancel(context.Cause(f.ctx)) })
	defer stop()
	if f.sink != nil {
		f.sink.set(ctx, ":grey_question: Waiting for your answer…", false, f.sink.ctl)
	}
	return askAgents(ctx, f.org, f.session, f.in.Reply, in, agents.AskWait)
}

// askAgents is the question itself, as a value so a test can stand in for agents.
var askAgents = agents.Ask

// ErrNotCarried is a server a run's ticket does not open: one its kit did not
// carry when the run started.
var ErrNotCarried = errors.New("coding: that MCP server is not in the run's kit")

// ErrToolNotCarried is a tool on a carried server that the run's kit did not
// admit (or that the run's person muted).
var ErrToolNotCarried = errors.New("coding: that tool is not in the run's kit")

// stow records on the flight ticket names which of its org's MCP servers its kit
// carries, and which tools of each server are admitted, which is what its ticket
// opens at the relay from then on.
func stow(ticket string, k Kit) {
	f := held(ticket)
	if f == nil {
		return
	}
	servers := make(map[string]bool, len(k.Servers))
	tools := make(map[string][]string, len(k.Servers))
	for _, s := range k.Servers {
		servers[s.ID] = true
		if len(s.Tools) > 0 {
			tools[s.ID] = slices.Clone(s.Tools)
		}
	}
	f.mu.Lock()
	f.servers = servers
	f.tools = tools
	f.mu.Unlock()
}

// Carried reports whether ticket opens server: a run in flight, on any pod, not
// paused, whose kit carries it.
func Carried(ticket, server string) error { return flights.carried(ticket, server) }

func (fl *fleet) carried(ticket, server string) error {
	if f := fl.held(ticket); f != nil {
		_, err := f.carrying(server)
		return err
	}
	v, found, err := fl.elsewhere(context.Background(), fleetCarried, ticketCall{Ticket: ticket, Server: server})
	switch {
	case err != nil:
		return err
	case !found:
		return ErrNoTicket
	}
	return v.err()
}

// carrying is the tools of server this flight's kit admitted, when its ticket opens
// server.
func (f *flight) carrying(server string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paused {
		return nil, errors.New("coding: the run is paused")
	}
	if !f.servers[server] {
		return nil, ErrNotCarried
	}
	return slices.Clone(f.tools[server]), nil
}

// relayWait bounds one relayed request: the tool plane gives the server twenty
// seconds, and this is that and the plane's own round trip.
const relayWait = time.Minute

// A run's relayed requests are bounded twice. Each one dials the org's own
// server with the org's credential, and the harness that sends them runs what a
// model wrote, so a run in a loop would otherwise be as many connections to that
// server as it cared to open and as many calls as the run's hours allow — on the
// org's account with the vendor, at no cost to the run. relayCalls is how many
// may be in flight at once; relayBurst requests may arrive together, and after
// that relayRate a second. A request past either is refused, not queued, and the
// harness asks again. What a relayed call costs the org is a price, and no price
// is set here.
const (
	relayCalls = 4
	relayBurst = 20
	relayRate  = 1.0 // requests a second, once the burst is spent
)

// ErrRelayLimit is a relayed request past the run's bound: relayCalls already in
// flight, or its bucket spent.
var ErrRelayLimit = errors.New("coding: the run is relaying as much as it may")

// relay takes one of the run's relay slots and a token from its bucket, and
// answers what gives the slot back. The slot is taken first, so a request the
// concurrency bound refuses spends no token.
func (f *flight) relay() (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.relays == nil {
		f.relays, f.paced, f.pacedAt = make(chan struct{}, relayCalls), relayBurst, time.Now()
	}
	select {
	case f.relays <- struct{}{}:
	default:
		return nil, fmt.Errorf("%w: %d requests are in flight", ErrRelayLimit, relayCalls)
	}
	now := time.Now()
	f.paced = min(relayBurst, f.paced+now.Sub(f.pacedAt).Seconds()*relayRate)
	f.pacedAt = now
	if f.paced < 1 {
		<-f.relays
		return nil, fmt.Errorf("%w: more than %g requests a second", ErrRelayLimit, relayRate)
	}
	f.paced--
	slots := f.relays
	return func() { <-slots }, nil
}

// Relay carries one MCP request of the run ticket names to one of its org's
// servers — the relay the harness is handed for it (relayURL) — through the tool
// app, which adds the server's credential and dials it. It answers only a server
// the run's kit carries, within the run's bound (ErrRelayLimit), and acts as the
// run's person in the run's org.
//
// The request that brought it is the harness's, anonymous but for the ticket, so
// the person is stated on a context of its own: nothing the harness sent is
// forwarded. It ends with the run as Ask does, and with the caller.
func Relay(ctx context.Context, ticket, server, method string, params json.RawMessage) (json.RawMessage, error) {
	return flights.relay(ctx, ticket, server, method, params)
}

func (fl *fleet) relay(ctx context.Context, ticket, server, method string, params json.RawMessage) (json.RawMessage, error) {
	if f := fl.held(ticket); f != nil {
		return f.relayed(ctx, server, method, params)
	}
	v, found, err := fl.elsewhere(ctx, fleetRelay, ticketCall{Ticket: ticket, Server: server, Method: method, Params: params})
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, ErrNoTicket
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), v.Out...), nil
}

// relayed is the relay of a run this pod flies.
func (f *flight) relayed(ctx context.Context, server, method string, params json.RawMessage) (json.RawMessage, error) {
	allowedTools, err := f.carrying(server)
	if err != nil {
		return nil, err
	}
	if method == "tools/call" && len(allowedTools) > 0 {
		var call struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &call); err != nil || strings.TrimSpace(call.Name) == "" {
			return nil, ErrToolNotCarried
		}
		if !slices.Contains(allowedTools, call.Name) {
			return nil, ErrToolNotCarried
		}
	}
	done, err := f.relay()
	if err != nil {
		return nil, err
	}
	defer done()
	as, cancel := context.WithTimeout(zip.WithCaller(context.Background(), zip.Caller{Org: f.org, User: f.subject}), relayWait)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	defer context.AfterFunc(f.ctx, cancel)()
	out, err := relayTools(as, &client.RelayIn{Server: server, Method: method, Params: params, Session: f.session})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("coding: the tool app answered nothing")
	}
	return out.Result, nil
}

// relayTools is the tool app's relay, as a value so a test can stand in for it.
var relayTools = toolpeer.ToolsRelay

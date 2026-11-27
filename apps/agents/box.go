package agents

// box.go — the sandbox a run holds.
//
// A run always has a computer to reach for: a script to run, a URL to curl, a
// repository to clone and read. It is leased LAZILY, on the first tool call that
// needs one, so a run that only talks never waits for a pod or pays for one.
// Every later sandbox call reuses it, and the model never carries an id: a
// sandbox call that names no sandbox is the run's.
//
// IT ENDS WITH THE RUN'S CONTEXT, and that is its whole lifecycle. runAgent binds
// the box to a context it cancels on the way out, and every way a turn ends
// cancels that context first:
//
//	completed, failed  runAgent returns
//	stopped, paused    stop() or a pause cancels the turn's context through `running`
//	the ceiling        the turn's context passes turnCeiling
//	a panic            the turn recovers, and its deferred cancel runs
//
// HOW it ends is the context's CAUSE. A turn that was stopped or paused is one
// somebody may come back to, so its box is PARKED — the pod stops, the disk stays,
// nothing is billed for compute — and the session names it (Session.Box), so a
// resume hands it to the run that picks up and the model finds its tree. Every
// other end gives the box back. A turn's box asks for a disk of its own for this,
// since a scratch box's files otherwise die with its pod; a run with no session is
// never resumed, and its box stays scratch. A park nobody resumes is retired by
// apps/sandbox's reaper after its hold (parkHold), disk and all.
//
// The app's conversation (talk.go) holds none: its code is worked on by coding
// runs, whose sandbox is their own and carried from one to the next (`after`).
//
// The reaper's orphan path ends a session whose run is NOT in this process, so
// there is no box here to end: its run either ended the box on the way out or
// died with its process. A process that dies leaves the lease to apps/sandbox's
// own reaper — the lease asks for no longer than the tool loop may run
// (toolRunBudget), and an untouched sandbox is reaped idle sooner.
//
// A LEASE IS NEVER CANCELLED. Cancelling a create midway is how a pod runs with no
// row naming it, so the lease runs detached, and one that lands after its run has
// ended is ended at once.
//
// It is billed to the run's org: the lease states that org as its caller, which is
// the ledger apps/sandbox gates and debits. And it is the run's PERSON'S: every box
// call states the subject the run's tool calls carry (actorSub), because
// apps/sandbox records who leased a sandbox and lets nobody else in the org act on
// it but an admin — so the model's calls reach the box, and a colleague's turn does
// not.
//
// A coding run does not borrow it. It outlives the turn that starts it, so it
// keeps a lease of its own (apps/coding/sandboxrunner.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// kindCoding is the session Kind a coding run tags its session with — the one
// whose Box is the coding run's sandbox and never a turn's.
const kindCoding = "coding"

// boxClass is dev: a run's sandbox is for research and code alike, and dev
// carries git, curl and the toolchain where exec carries a bare interpreter
// (apps/coding classFor). With no project it is scratch — its disk dies with the
// pod — and it counts against the ceiling apps/sandbox keeps on scratch leases.
const boxClass = "dev"

// boxEndWait bounds giving a box back. Past it the pod is left to apps/sandbox's
// idle reaper, which is where a crash leaves it too.
const boxEndWait = 30 * time.Second

// The sandbox ops as the tool plane names them: apps/sandbox declares these ids
// and the MCP server publishes them as they are.
const (
	opLease    = "lease_sandbox"
	opRun      = "run_in_sandbox"
	opRead     = "read_sandbox_file"
	opWrite    = "write_sandbox_file"
	opComputer = "computer_in_sandbox"
	opStop     = "stop_run"
	opEnd      = "end_sandbox"
)

var boxOps = map[string]bool{
	opLease: true, opRun: true, opRead: true, opWrite: true,
	opComputer: true, opStop: true, opEnd: true,
}

const boxDesktopClass = "desktop"

var reConflictSandboxID = regexp.MustCompile(`\(m_[A-Za-z0-9]+\)`)

// sandboxTool is the one tool the surface groups the sandbox ops under.
const sandboxTool = "sandbox"

// errBoxTalks answers a sandbox call made in the app's conversation (talk.go),
// which works on code through a coding run: `dev`, with the CLI's own tools.
var errBoxTalks = errors.New("agents: in Dev, code is worked on by a coding run (post_agent_coding), which runs dev with its own tools; " +
	"start one, or carry on the conversation's last")

// errBoxOver answers a sandbox call made after its run has ended.
var errBoxOver = errors.New("agents: the run has ended, and its sandbox with it")

// errBoxUnkeyed answers a sandbox call of a run whose person names no subject a
// sandbox can be held by.
var errBoxUnkeyed = zip.ErrForbidden("agents: a sandbox is held by the person it is for, and this credential names no subject to hold it by")

type box struct {
	org string
	sub string // the run's person, as its tool calls name them; "" for a run with none
	// keyed is whether sub is a subject a sandbox may be held by
	// (principal.SubjectFrom). A run for a person whose credential names none
	// leases nothing: its sandbox would be held under a name somebody else shares.
	keyed bool
	log   logger
	turn  *live // the session the box is kept for across a stop or a pause; nil for a run with none
	// as and pays are who the run was asked by and who pays for it, as the run's
	// context states them (talk.go: the asker's request, detached): every box call
	// is made as that person, paid by that payer — never with their admin bits.
	as   zip.Caller
	pays cloud.Payer

	mu        sync.Mutex
	id        string  // the sandbox the run holds; "" until one is leased
	agentName string  // optional agent handle for persistent bot desktop sandboxes
	bound     bool    // true when ComputeRef bound an existing desktop sandbox onto the bot
	desktop   bool    // true when this run uses a GUI desktop sandbox (bound or computer_in_sandbox)
	flight    *flight // the lease under way, if any
	over      bool    // the run has ended: nothing more is leased
	cause     error   // how it ended, once it has

	ended chan struct{} // closed once end has let the sandbox go
}

// flight is one lease, which every caller wanting the box meanwhile waits on.
type flight struct {
	done chan struct{}
	id   string
	err  error
}

type boxKey struct{}

// withBox gives the run on ctx its box. ctx must be one the run cancels on every
// way out: the box ends when ctx does, the way its cause says.
//
// turn is the run's session, when it has one. A resumed turn's box starts out
// holding the sandbox its session parked, so its first lease is the resume.
//
// ONE COLUMN, TWO LIFECYCLES, and only one of them is a turn's. Session.Box also
// names a CODING run's sandbox (TagSession), which holds a write key to a
// repository and the codebase's secrets, and which the coding run leases, ends
// and never parks for a turn. A turn never starts holding one: a session of kind
// coding hands its box to nobody, so the turn leases a box of its own.
func withBox(ctx context.Context, org, sub string, log logger, turn *live) context.Context {
	return withBoxForAgent(ctx, org, sub, log, turn, "", "")
}

// withBoxForAgent gives a bot run its sandbox, reusing an explicit ComputeRef
// desktop machine ("m_...") when bound or promoting to a persistent desktop
// sandbox when the bot invokes computer_in_sandbox.
func withBoxForAgent(ctx context.Context, org, sub string, log logger, turn *live, agentName, computeRef string) context.Context {
	_, keyed := principal.SubjectFrom(ctx)
	as := zip.CallerOf(ctx)
	as.Admin, as.OrgAdmin = false, false
	b := &box{
		org: org, sub: sub, keyed: keyed, log: log, turn: turn,
		as: as, pays: cloud.PayerOf(ctx), agentName: strings.TrimSpace(agentName),
		ended: make(chan struct{}),
	}
	if turn != nil && turn.x.Kind != kindCoding && strings.TrimSpace(turn.x.Box) != "" {
		b.id = turn.x.Box
	} else if ref := strings.TrimSpace(computeRef); strings.HasPrefix(ref, "m_") {
		b.id = ref
		b.bound = true
		b.desktop = true
	}
	context.AfterFunc(ctx, func() { b.end(context.Cause(ctx)) })
	return context.WithValue(ctx, boxKey{}, b)
}

func botDesktopProject(agentName string) string {
	s := strings.ToLower(strings.TrimSpace(agentName))
	if s == "" {
		return ""
	}
	var out strings.Builder
	out.WriteString("bot-")
	lastDash := true
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out.WriteRune(r)
			lastDash = false
		case !lastDash:
			out.WriteByte('-')
			lastDash = true
		}
		if out.Len() >= 48 {
			break
		}
	}
	res := strings.TrimRight(out.String(), "-")
	if res == "bot" {
		return ""
	}
	return res
}

func boxFrom(ctx context.Context) *box {
	b, _ := ctx.Value(boxKey{}).(*box)
	return b
}

// boxCall hands a sandbox call the run's sandbox.
//
// ONE RULE: a call that names no sandbox means the run's. lease, run, read and
// write lease it on the first of them; stop and end are handed it only while
// there is one, since nothing is leased to be stopped. A call that NAMES a
// sandbox keeps it — that one the org already held, and the run neither leased it
// nor ends it.
//
// A lease inside a run is therefore the run's own box, whatever class it asked
// for, and the reply says which class it got.
//
// Both spellings, as address reads them: the op's own name, and the
// subsystem envelope {"op":…,"input":{…}} the built-in calls. A describe of a
// sandbox op starts the lease, because the call it is reading up on comes next.
// Anything else is returned untouched.
func boxCall(ctx context.Context, name, args string) (string, error) {
	b := boxFrom(ctx)
	if b == nil {
		return args, nil
	}
	talking := b.turn != nil && talks(b.turn.x)
	if boxOps[name] {
		if talking {
			return args, errBoxTalks
		}
		return b.fill(ctx, name, args)
	}
	obj, err := object(args)
	if err != nil {
		return args, nil
	}
	var op string
	if json.Unmarshal(obj["op"], &op) != nil || !boxOps[op] {
		return args, nil
	}
	if talking {
		return args, errBoxTalks
	}
	if name == client.Describe {
		if op == opComputer {
			b.promoteDesktop()
		}
		if op != opStop && op != opEnd {
			b.warm()
		}
		return args, nil
	}
	input := "{}"
	if len(obj["input"]) > 0 {
		input = string(obj["input"])
	}
	filled, err := b.fill(ctx, op, input)
	if err != nil {
		return args, err
	}
	obj["input"] = json.RawMessage(filled)
	return encode(obj, args), nil
}

// fill writes the run's sandbox into one sandbox op's own arguments.
func (b *box) fill(ctx context.Context, op, args string) (string, error) {
	obj, err := object(args)
	if err != nil {
		return args, nil // the tool plane refuses it in its own words
	}
	var named string
	_ = json.Unmarshal(obj["id"], &named)
	if strings.TrimSpace(named) != "" {
		return args, nil
	}
	if op == opStop || op == opEnd {
		if id := b.held(); id != "" {
			return set(args, "id", id), nil
		}
		return args, nil
	}
	if op == opComputer {
		b.promoteDesktop()
	}
	id, err := b.take(ctx)
	if err != nil {
		return args, err
	}
	return set(args, "id", id), nil
}

// take is the run's sandbox, leased on the first call that needs one.
//
// It ASKS every time, and every ask after the first is a resume of the sandbox the
// run holds: apps/sandbox answers with that one while it runs and a fresh one once
// it is gone — ended by the model, or reaped idle. So the run never holds an id the
// sandbox has forgotten, for one local round trip. A caller that stops waiting
// leaves the lease to land for the next one.
func (b *box) take(ctx context.Context) (string, error) {
	if !b.keyed {
		return "", errBoxUnkeyed
	}
	b.mu.Lock()
	if b.over {
		b.mu.Unlock()
		return "", errBoxOver
	}
	f := b.fly()
	b.mu.Unlock()
	select {
	case <-f.done:
		return f.id, f.err
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

// warm starts the first lease without waiting on it, so the pod starts while the
// model writes the call rather than after.
func (b *box) warm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.keyed && !b.over && b.id == "" {
		b.fly()
	}
}

// fly is the lease under way, started if there is none. Called under b.mu.
func (b *box) fly() *flight {
	if b.flight == nil {
		b.flight = &flight{done: make(chan struct{})}
		go b.lease(b.flight, b.id)
	}
	return b.flight
}

// caller is who every box call is made as: the run's person in the run's org —
// as the run's context names them, so the lease is paid by whoever pays for the
// run (cloud.PayerOf, and the name and billing account apps/sandbox resolves a
// wallet from) — on a context of its own, because a lease is never cancelled and
// an end outlives the run that asks for it.
func (b *box) caller() context.Context {
	c := b.as
	c.Org, c.User = b.org, b.sub
	ctx := zip.WithCaller(context.Background(), c)
	if b.pays.Billable() {
		ctx = cloud.WithPayer(ctx, b.pays)
	}
	return ctx
}

// held is the sandbox the run holds now, leasing nothing.
func (b *box) held() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.id
}

func (b *box) promoteDesktop() {
	b.mu.Lock()
	b.desktop = true
	b.mu.Unlock()
}

// lease asks apps/sandbox for the run's sandbox, naming the one it holds so a
// running one comes back as it is and a parked one is given a pod again
// (client.LeaseIn). The lease is as long as the tool loop may run: no call can
// use the box after that, so a lease a dead process leaves behind expires with it.
func (b *box) lease(f *flight, held string) {
	ctx, cancel := context.WithTimeout(b.caller(), toolRunBudget)
	defer cancel()
	b.mu.Lock()
	desktop := b.desktop
	agentName := b.agentName
	b.mu.Unlock()

	class := boxClass
	project := ""
	disk := b.turn != nil
	if desktop {
		class = boxDesktopClass
		disk = true
		if held == "" {
			project = botDesktopProject(agentName)
		}
	}
	got, err := sandboxpeer.SandboxLease(ctx, &client.LeaseIn{
		ID: held, Class: class, Project: project, TTLSec: int(toolRunBudget / time.Second), Disk: disk,
	})
	if err != nil && desktop && held == "" && project != "" {
		// If this bot already has a running or parked desktop sandbox under its
		// project, resume that exact machine id; if another org member owns the
		// project slug, fall back to an isolated scratch desktop for this caller.
		if m := reConflictSandboxID.FindString(err.Error()); len(m) > 2 {
			existingID := strings.TrimSuffix(strings.TrimPrefix(m, "("), ")")
			got, err = sandboxpeer.SandboxLease(ctx, &client.LeaseIn{
				ID: existingID, Class: class, TTLSec: int(toolRunBudget / time.Second), Disk: true,
			})
		} else {
			var he *zip.HTTPError
			if errors.As(err, &he) && (he.Status == http.StatusConflict || he.Status == http.StatusPaymentRequired) {
				got, err = sandboxpeer.SandboxLease(ctx, &client.LeaseIn{
					Class: class, TTLSec: int(toolRunBudget / time.Second), Disk: true,
				})
			}
		}
	}
	id := ""
	switch {
	case err != nil:
		err = fmt.Errorf("agents: lease a sandbox: %w", err)
	case got == nil || strings.TrimSpace(got.ID) == "":
		err = errors.New("agents: lease a sandbox: it answered with no id")
	default:
		id = got.ID
	}

	b.mu.Lock()
	b.flight = nil
	over, cause := b.over, b.cause
	if !over && id != "" {
		b.id = id
	}
	b.mu.Unlock()

	switch {
	case over && id != "" && id == held:
		// The run ended while this resumed the sandbox it held. end() let that one
		// go, and the resume may have landed after it and given it a pod again; it
		// is let go the same way once more, and asking twice is the same answer.
		b.close(id, cause)
	case over && id != "":
		// The run ended while this was under way, and a fresh one is this lease's to
		// give back: nothing was ever done in it.
		b.release(id)
	case id != "" && id != held && b.turn != nil:
		// The session names the box as soon as there is one, not once it is parked:
		// a resume that lands while a stop is still parking it finds it all the same.
		b.turn.keep(id)
	}
	if over {
		f.err = errBoxOver
	} else {
		f.id, f.err = id, err
	}
	close(f.done)
}

// end lets the run's sandbox go and refuses any later lease. It runs once, when
// the run's context is done; a lease still under way is let go when it lands.
func (b *box) end(cause error) {
	defer close(b.ended)
	b.mu.Lock()
	id := b.id
	b.id, b.over, b.cause = "", true, cause
	b.mu.Unlock()
	if id != "" {
		b.close(id, cause)
	}
}

// kept reports whether a run that ended with cause is one its turn comes back to:
// it was stopped or paused.
func kept(cause error) bool { return errors.Is(cause, ErrStopped) || errors.Is(cause, ErrPaused) }

// keeps reports whether the box of a run that ended with cause is PARKED for its
// session to come back to rather than given back: a turn that was stopped or
// paused. A park nobody comes back to is retired by apps/sandbox with its disk
// (lifecycle.go parkHold).
func (b *box) keeps(cause error) bool {
	b.mu.Lock()
	desktop, bound := b.desktop, b.bound
	b.mu.Unlock()
	if bound {
		return false
	}
	if desktop {
		return true
	}
	return b.turn != nil && kept(cause)
}

// close lets one sandbox go the way the run ended: parked when it is kept, left
// running when explicitly bound to the bot's ComputeRef (so live noVNC stays up),
// and given back otherwise.
func (b *box) close(id string, cause error) {
	b.mu.Lock()
	bound := b.bound
	b.mu.Unlock()
	if bound {
		return
	}
	if b.keeps(cause) {
		b.park(id)
		return
	}
	b.release(id)
}

// settle waits, when the run ended with cause and its box is being KEPT, until the
// box is where a resume will look for it: parked by end, and a lease still under
// way when the run ended parked too when it lands (for as long as boxEndWait). The
// turn frees its session only after this, so a resume never races its own park —
// the pod a resume starts is never stopped by the park of the run before it. A box
// being given back is not waited for: nothing comes back to it.
func (b *box) settle(cause error) {
	if !b.keeps(cause) {
		return
	}
	<-b.ended
	b.mu.Lock()
	f := b.flight
	b.mu.Unlock()
	if f == nil {
		return
	}
	select {
	case <-f.done:
	case <-time.After(boxEndWait):
	}
}

// park keeps one sandbox for its turn: the pod stops and the disk stays. One
// already gone is not a failure — the resume finds it gone and leases afresh.
func (b *box) park(id string) {
	ctx, cancel := context.WithTimeout(b.caller(), boxEndWait)
	defer cancel()
	_, err := sandboxpeer.SandboxPark(ctx, &client.ParkIn{ID: id})
	var he *zip.HTTPError
	if err == nil || (errors.As(err, &he) && he.Status == http.StatusNotFound) || b.log == nil {
		return
	}
	b.log.Warn("agents: a stopped run's sandbox was not parked; the sandbox reaper will end it",
		"org", b.org, "sandbox", id, "err", err)
}

// release ends one sandbox on the run's org. One already gone — the model ended
// it, or the reaper did — is not a failure.
func (b *box) release(id string) {
	ctx, cancel := context.WithTimeout(b.caller(), boxEndWait)
	defer cancel()
	_, err := sandboxpeer.SandboxEnd(ctx, &client.EndIn{ID: id})
	var he *zip.HTTPError
	if err == nil || (errors.As(err, &he) && he.Status == http.StatusNotFound) || b.log == nil {
		return
	}
	b.log.Warn("agents: a run's sandbox was not ended; the sandbox reaper will end it",
		"org", b.org, "sandbox", id, "err", err)
}

package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
)

// mailbox.go is the LIVE hand-off between a routed run's durable owner (the
// coding RoutedRunWorkflow, running on the embedded tasks engine) and the
// external machine that claims and executes it over HTTP. It is the rendezvous
// ONLY — never the durable queue. The tasks engine is the queue of record: it
// survives a cloud restart, times a never-claimed run out, and retries. On every
// (re)start of the delivery activity the run is (re-)Offered here, so a machine
// that long-polls Claim always finds work the engine still owns; a cloud restart
// simply re-populates the mailbox from durable history.
//
// ACROSS PODS. The pod whose worker runs the delivery activity and the pod the
// machine's long poll lands on are not the same pod. An offer stays in the memory of
// the pod that made it, because that pod is the one awaiting its result, and the
// other pods reach it over the bus (sdk/coord): an offer is announced; a Claim
// asks the pods which hold one for its key and takes it from the pod that does, by
// name, so exactly one claimer gets it whoever else is asking; a Report asks the
// pods which await the run. Nothing is stored: a pod that is lost takes its offers,
// and the workflow that owns the run re-offers it, as it does after any restart.
//
// ISOLATION IS STRUCTURAL. Every offer is filed under the key (org, target), and
// Claim/Report only ever touch that one key's slot. A run offered for (orgB,
// targetY) is unreachable from a Claim or Report for (orgA, targetX) — the tenant
// + machine boundary is a property of the map key, not a check a caller can skip.

// RoutedRun is the spec of one coding run dispatched to a target. The engine's
// history holds everything but Grant, which is minted when the run is offered and
// never written there: the executing machine authenticates model routing, and git
// on a repository it can reach, with its OWN already-held credentials (the same
// ones `hanzo code` uses).
type RoutedRun struct {
	Org            string `json:"org"`
	TargetID       string `json:"targetId"`
	SessionID      string `json:"sessionId"` // the live session opened at dispatch; the machine streams into it
	Repo           string `json:"repo"`
	Project        string `json:"project,omitempty"`
	Base           string `json:"base,omitempty"`
	Branch         string `json:"branch"`
	Prompt         string `json:"prompt"`
	CloneURL       string `json:"cloneUrl"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	// Actor + AgentRef are CLOUD-SIDE attribution for the completion path (session
	// close + PR assignee). They are NOT part of routedRunView, so they never cross
	// to the executing machine — the machine needs neither.
	Actor    string `json:"actor,omitempty"`
	AgentRef string `json:"agentRef,omitempty"`
	// ForgeActor is the forge login the run acts as — carried for the same reason
	// and with the same confinement: the completion opens the pull request as
	// that person, and the machine never sees it.
	ForgeActor string `json:"forgeActor,omitempty"`
	// Home is owner/name on GitHub for a run on a repository the forge does not
	// hold. It is the completion's and never crosses to the machine; the
	// completion is admitted on what GitHub and IAM answer then, so the run
	// carries no authority of its person's.
	Home string `json:"home,omitempty"`
	// Model and Effort are what the person asked the run to think with. They DO
	// cross to the machine (routedRunView); empty is the machine's own default.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Instructions are the person's own instructions for their runs, and they
	// cross to the machine too; empty when the person set none.
	Instructions string `json:"instructions,omitempty"`
	// Pull is a run asked to open a pull request. The completion reads it;
	// like Actor it is NOT part of routedRunView and never crosses to the machine.
	Pull bool `json:"pull,omitempty"`
	// Made is a run in a workspace the platform made: its repository is on the forge,
	// where the machine holds no credential, so the run is given one of its own.
	// Read is a run that may only read it.
	Made bool `json:"made,omitempty"`
	Read bool `json:"read,omitempty"`
	// Grant is that credential, added in memory when the run is offered and withdrawn
	// when it ends. It reaches the machine in the claim response and nowhere else.
	Grant *RoutedGrant `json:"grant,omitempty"`
	// Dir is the folder on the machine the run works in, inside one its owner
	// granted (folders.go); empty is the machine's own choice. It crosses to the
	// machine, which re-checks the real path before it runs.
	Dir string `json:"dir,omitempty"`
	// ReadOnly constrains the whole run on the machine: no writes to the granted
	// folder or checkout, no commit, no push, no PR. It is checked again at claim,
	// the way Dir is, so a machine that lost the capability refuses the queued run.
	// It is NOT Read, which only makes a platform-minted deploy key read-only.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// RoutedGrant is a run's own access to its repository: a deploy key and the
// address it opens, with the forge's host key to check it against.
type RoutedGrant struct {
	Remote string `json:"remote"`
	Key    string `json:"key"`
	Known  string `json:"known"`
}

// String names nothing, so a log line that takes the grant leaves the key out.
func (RoutedGrant) String() string { return "routed grant" }

// RoutedResult is a routed run's terminal outcome, reported by the machine and
// returned to the durable activity so the workflow completes.
type RoutedResult struct {
	OK        bool   `json:"ok"`
	Changed   bool   `json:"changed"`
	Branch    string `json:"branch,omitempty"`
	CommitSha string `json:"commitSha,omitempty"`
	Diffstat  string `json:"diffstat,omitempty"`
	Error     string `json:"error,omitempty"`
}

// offer is one run waiting to be claimed, plus the channel its durable owner
// blocks on for the terminal result. result is buffered(1) so Report never blocks
// even if the owner is between selects; closed fires when the offer is finished
// (reported OR abandoned) so a waiter always unblocks.
type offer struct {
	mb     *mailbox
	key    string // (org,target)
	rk     string // (org,target,sessionID)
	run    RoutedRun
	result chan RoutedResult
	closed chan struct{}
	once   sync.Once
}

// Await blocks until the machine reports this run's result, the offer is
// abandoned, or ctx (the activity's StartToClose budget) fires. It is the
// durable owner's half of the rendezvous.
func (o *offer) Await(ctx context.Context) (RoutedResult, bool) {
	select {
	case res := <-o.result:
		return res, true
	case <-o.closed:
		// Abandoned or reported-then-closed: drain a delivered result if one raced in.
		select {
		case res := <-o.result:
			return res, true
		default:
			return RoutedResult{}, false
		}
	case <-ctx.Done():
		return RoutedResult{}, false
	}
}

// Close removes the offer from the mailbox (if still present) and unblocks any
// waiter. Idempotent — the durable owner defers it so a timed-out or crashed
// delivery never leaks a queued or claimed offer.
func (o *offer) Close() { o.mb.discard(o) }

// mailbox is the process-wide rendezvous. queues holds each key's FIFO of
// unclaimed offers; byRun indexes every live offer by (org,target,sessionID) for
// Report + re-offer dedupe; signal is a per-key broadcast channel (closed and
// recreated on Offer) that Claim waits on.
type mailbox struct {
	mu     sync.Mutex
	queues map[string][]*offer
	byRun  map[string]*offer
	signal map[string]chan struct{}
	// inflight is the per-org set of live routed sessions (offered, not yet finished),
	// the gauge the per-org admission cap reads. A SET keyed by session id (not a bare
	// counter) so a re-offer after a restart re-adds idempotently and a superseded
	// offer never double-counts or wrongly decrements the still-live session.
	inflight map[string]map[string]struct{}

	// bus is the deployment's coord when it spans pods (attach), else nil; leave
	// stops serving the other pods.
	bus   *coord.Coord
	leave func()
}

// The questions a mailbox answers for the other pods, and how long it waits on one.
const (
	mbHas    = "mailbox.has"
	mbTake   = "mailbox.take"
	mbReport = "mailbox.report"
	// claimPeerWait bounds one question to the other pods during a Claim. A pod that
	// does not answer in it is taken for having nothing; the claim asks again when
	// it wakes.
	claimPeerWait = 3 * time.Second
)

// remoteReport is a Report as another pod asks it.
type remoteReport struct {
	Org     string       `json:"org"`
	Target  string       `json:"target"`
	Session string       `json:"session"`
	Result  RoutedResult `json:"result"`
}

func newMailbox() *mailbox {
	return &mailbox{
		queues:   map[string][]*offer{},
		byRun:    map[string]*offer{},
		signal:   map[string]chan struct{}{},
		inflight: map[string]map[string]struct{}{},
	}
}

// routedMailbox is the ONE process-wide rendezvous, shared by the coding
// delivery activity (Offer/Await) and the machine-facing HTTP surface
// (Claim/Report). One mailbox, one way. mountRouting attaches it to the deployment's
// other pods.
var routedMailbox = newMailbox()

// attach joins the mailbox to the other pods of the deployment, or to none: a coord
// that answers for this process alone leaves it as it was, a rendezvous of one pod.
// Called again, it leaves the pods it was attached to first.
func (m *mailbox) attach(c *coord.Coord) {
	m.mu.Lock()
	leave := m.leave
	m.bus, m.leave = nil, nil
	m.mu.Unlock()
	if leave != nil {
		leave()
	}
	if c == nil || !c.Shared() {
		return
	}
	stopHas := c.Serve(mbHas, m.serveHas)
	stopTake := c.Serve(mbTake, m.serveTake)
	stopReport := c.Serve(mbReport, m.serveReport)
	m.mu.Lock()
	m.bus = c
	m.leave = func() { stopHas(); stopTake(); stopReport() }
	m.mu.Unlock()
}

func (m *mailbox) coord() *coord.Coord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bus
}

// serveHas says whether this pod holds an unclaimed offer for key.
func (m *mailbox) serveHas(_ context.Context, _ string, key []byte) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return nil, len(m.queues[string(key)]) > 0
}

// serveTake hands the oldest unclaimed offer for key to the pod that asked, and
// forgets it as a claim does. It says no when another claim got there first.
func (m *mailbox) serveTake(_ context.Context, _ string, key []byte) ([]byte, bool) {
	run, ok := m.popLocal(string(key))
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(run)
	return b, err == nil
}

// serveReport delivers a result another pod was handed, when this pod awaits the
// run.
func (m *mailbox) serveReport(_ context.Context, _ string, body []byte) ([]byte, bool) {
	var r remoteReport
	if json.Unmarshal(body, &r) != nil {
		return nil, false
	}
	return nil, m.reportLocal(r.Org, r.Target, r.Session, r.Result)
}

// announce tells the pods whose machines are waiting on key that an offer is here.
func (m *mailbox) announce(key string) {
	if c := m.coord(); c != nil {
		_ = c.Publish(announcing(key), nil)
	}
}

// announcing is the subject a Claim for key waits on: the key is a tenant and a
// machine, so it is hashed into one that names neither.
func announcing(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "mailbox/" + hex.EncodeToString(sum[:16])
}

func mbKey(org, target string) string        { return org + "\x00" + target }
func runKey(org, target, sess string) string { return org + "\x00" + target + "\x00" + sess }

// Offer files run for its (org,target) and returns the handle its durable owner
// awaits. A re-offer of the same (org,target,sessionID) — the workflow retrying
// or replaying after a restart — supersedes the stale prior offer (removing it
// from the queue and unblocking its dead waiter) so a machine never claims a run
// whose owner has already moved on.
func (m *mailbox) Offer(run RoutedRun) *offer {
	key := mbKey(run.Org, run.TargetID)
	rk := runKey(run.Org, run.TargetID, run.SessionID)
	o := &offer{mb: m, key: key, rk: rk, run: run, result: make(chan RoutedResult, 1), closed: make(chan struct{})}
	m.mu.Lock()
	if prev := m.byRun[rk]; prev != nil {
		m.removeFromQueueLocked(key, prev)
		prev.finish()
	}
	m.byRun[rk] = o
	m.queues[key] = append(m.queues[key], o)
	m.broadcastLocked(key)
	m.mu.Unlock()
	m.announce(key)
	return o
}

// Claim blocks until an unclaimed run exists for (org,target) or ctx fires,
// returning the oldest — from this pod's queue, else from another pod's. The
// claimed offer leaves the queue but stays in byRun, awaiting Report. Only this
// key's queue is ever read, so a claim can never surface another tenant's or
// another machine's run.
func (m *mailbox) Claim(ctx context.Context, org, target string) (RoutedRun, bool) {
	key := mbKey(org, target)
	// Listen BEFORE the first look, on this pod and the others: an offer made between
	// the look and the wait wakes the wait.
	wake := make(chan struct{}, 1)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	c := m.coord()
	if c != nil {
		defer c.Subscribe(announcing(key), func(string, []byte) { poke() }, poke)()
	}
	for {
		if run, ok := m.popLocal(key); ok {
			return run, true
		}
		if c != nil {
			if run, ok := m.takeElsewhere(ctx, c, key); ok {
				return run, true
			}
		}
		m.mu.Lock()
		if len(m.queues[key]) > 0 {
			m.mu.Unlock()
			continue // an offer landed between the look and here
		}
		sig := m.signalLocked(key)
		m.mu.Unlock()
		select {
		case <-sig:
			// a new offer (or a superseding one) arrived — re-check
		case <-wake:
			// another pod made one
		case <-ctx.Done():
			return RoutedRun{}, false
		}
	}
}

// popLocal takes the oldest unclaimed offer for key off this pod's queue.
func (m *mailbox) popLocal(key string) (RoutedRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queues[key]
	if len(q) == 0 {
		return RoutedRun{}, false
	}
	o := q[0]
	m.queues[key] = q[1:]
	return o.run, true
}

// takeElsewhere asks the other pods which hold an offer for key and takes one from
// the first that still does. The ask names no run, so it takes nothing; the take is
// to one pod by name, whose queue decides who got there first.
func (m *mailbox) takeElsewhere(ctx context.Context, c *coord.Coord, key string) (RoutedRun, bool) {
	ctx, cancel := context.WithTimeout(ctx, claimPeerWait)
	defer cancel()
	holders, _ := c.Gather(ctx, mbHas, []byte(key))
	for _, h := range holders {
		ans, ok, err := c.CallPod(ctx, h.From, mbTake, []byte(key))
		if err != nil || !ok {
			continue
		}
		var run RoutedRun
		if json.Unmarshal(ans.Body, &run) == nil {
			return run, true
		}
	}
	return RoutedRun{}, false
}

// Report delivers a terminal result to the run's durable owner — on this pod, else
// on the pod that awaits it. Scoped to (org,target,sessionID): a report can only
// ever complete a run that exact key owns, so one machine can never report on
// behalf of another. Returns false when no live offer matches (already reported,
// abandoned, or never existed), and an error when the other pods could not all be
// asked, which is not the same answer: the machine reports again.
func (m *mailbox) Report(ctx context.Context, org, target, sess string, res RoutedResult) (bool, error) {
	if m.reportLocal(org, target, sess, res) {
		return true, nil
	}
	c := m.coord()
	if c == nil {
		return false, nil
	}
	b, err := json.Marshal(remoteReport{Org: org, Target: target, Session: sess, Result: res})
	if err != nil {
		return false, err
	}
	_, ok, err := c.Call(ctx, mbReport, b)
	return ok, err
}

func (m *mailbox) reportLocal(org, target, sess string, res RoutedResult) bool {
	rk := runKey(org, target, sess)
	m.mu.Lock()
	o := m.byRun[rk]
	if o == nil {
		m.mu.Unlock()
		return false
	}
	delete(m.byRun, rk)
	m.removeFromQueueLocked(mbKey(org, target), o)
	m.mu.Unlock()
	o.deliver(res)
	return true
}

// discard drops an offer the owner is done with (ctx timeout / crash / normal
// close) so neither the queue nor byRun retains it.
func (m *mailbox) discard(o *offer) {
	m.mu.Lock()
	if m.byRun[o.rk] == o {
		delete(m.byRun, o.rk)
	}
	m.removeFromQueueLocked(o.key, o)
	m.mu.Unlock()
	o.finish()
}

func (m *mailbox) removeFromQueueLocked(key string, o *offer) {
	q := m.queues[key]
	for i, e := range q {
		if e == o {
			m.queues[key] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}

// broadcastLocked wakes every Claim waiting on key by closing its signal channel;
// a fresh channel replaces it for the next wait.
func (m *mailbox) broadcastLocked(key string) {
	if ch, ok := m.signal[key]; ok {
		close(ch)
		delete(m.signal, key)
	}
}

func (m *mailbox) signalLocked(key string) chan struct{} {
	ch, ok := m.signal[key]
	if !ok {
		ch = make(chan struct{})
		m.signal[key] = ch
	}
	return ch
}

func (o *offer) deliver(res RoutedResult) {
	o.once.Do(func() {
		o.result <- res // buffered(1) — never blocks
		close(o.closed)
	})
}

func (o *offer) finish() {
	o.once.Do(func() { close(o.closed) })
}

// OfferRoutedRun is the exported client the coding delivery activity uses to place
// a run into the live rendezvous. Kept here (agents owns targets + sessions) so
// the machine-facing HTTP surface and the durable activity share ONE mailbox.
func OfferRoutedRun(run RoutedRun) *offer { return routedMailbox.Offer(run) }

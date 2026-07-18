package agents

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"
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
// ISOLATION IS STRUCTURAL. Every offer is filed under the key (org, target), and
// Claim/Report only ever touch that one key's slot. A run offered for (orgB,
// targetY) is unreachable from a Claim or Report for (orgA, targetX) — the tenant
// + machine boundary is a property of the map key, not a check a caller can skip.

// RoutedRun is the NON-SECRET spec of one coding run dispatched to a target. It
// carries no credential by design: the executing machine authenticates git +
// model routing with its OWN already-held credentials (the same ones `hanzo code`
// uses), so no secret ever enters the durable store or crosses to the machine in
// the claim response. Everything here is safe to persist in the tasks engine.
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
}

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
}

func newMailbox() *mailbox {
	return &mailbox{
		queues: map[string][]*offer{},
		byRun:  map[string]*offer{},
		signal: map[string]chan struct{}{},
	}
}

// routedMailbox is the ONE process-wide rendezvous, shared by the coding
// delivery activity (Offer/Await) and the machine-facing HTTP surface
// (Claim/Report). One mailbox, one way.
//
// SINGLE-REPLICA DEPENDENCY (accepted, inherited). This rendezvous is IN-PROCESS: the
// durable delivery activity (Offer/Await, on whichever replica's tasks worker polls
// the agent-routed queue) and the external machine's POST /claim (ingress load-
// balanced to any replica) must land on the SAME process, because the mailbox is a
// package global, not a shared broker. cloud already runs HARD single-replica —
// Recreate, replicas:1 — because the embedded Badger KMS holds an exclusive file lock
// and the audit sequence is an in-memory counter (infra/k8s/operator/crs/cloud.yaml),
// so route-work INHERITS that guarantee for free and needs no broker. assertSingleReplica
// logs the assumption at mount and warns loudly if a multi-replica signal is present.
//
// IF cloud is ever made multi-replica (the KMS lock lifted): this rendezvous MUST
// become replica-aware — either a sticky route that pins a target's /claim to the
// replica whose worker owns its delivery, or a shared broker (the embedded NATS/
// JetStream already in-process, keyed by (org,target)) so Offer and Claim meet
// regardless of which replica each hits. Until then, single-replica is the contract.
var routedMailbox = newMailbox()

// assertSingleReplica records the single-replica assumption the process-global
// rendezvous depends on, and warns LOUDLY if a multi-replica signal is detectable
// (CLOUD_REPLICAS > 1). It does not fail mount — cloud's replicas:1 is enforced by the
// deployment (the KMS lock), so this is a defensive breadcrumb for the day that
// changes, not a runtime gate. Called once from mountRouting.
func assertSingleReplica(log luxlog.Logger) {
	if log == nil {
		return
	}
	replicas := 1
	if v := strings.TrimSpace(os.Getenv("CLOUD_REPLICAS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			replicas = n
		}
	}
	if replicas > 1 {
		log.Warn("route-work: the routed-run rendezvous is process-global and REQUIRES cloud to run single-replica, but CLOUD_REPLICAS>1 — routed /claim will silently fail on a replica that does not own the delivery. Make the rendezvous replica-aware (sticky target route or shared broker) before scaling out.",
			"replicas", replicas)
		return
	}
	log.Info("route-work: routed-run rendezvous is in-process; assumes cloud single-replica (inherited from the KMS exclusive lock)")
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
	return o
}

// Claim blocks until an unclaimed run exists for (org,target) or ctx fires,
// returning the oldest. The claimed offer leaves the queue but stays in byRun,
// awaiting Report. Only this key's queue is ever read, so a claim can never
// surface another tenant's or another machine's run.
func (m *mailbox) Claim(ctx context.Context, org, target string) (RoutedRun, bool) {
	key := mbKey(org, target)
	for {
		m.mu.Lock()
		if q := m.queues[key]; len(q) > 0 {
			o := q[0]
			m.queues[key] = q[1:]
			m.mu.Unlock()
			return o.run, true
		}
		sig := m.signalLocked(key)
		m.mu.Unlock()
		select {
		case <-sig:
			// a new offer (or a superseding one) arrived — re-check
		case <-ctx.Done():
			return RoutedRun{}, false
		}
	}
}

// Report delivers a terminal result to the run's durable owner. Scoped to
// (org,target,sessionID): a report can only ever complete a run that exact key
// owns, so one machine can never report on behalf of another. Returns false when
// no live offer matches (already reported, abandoned, or never existed).
func (m *mailbox) Report(org, target, sess string, res RoutedResult) bool {
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

// OfferRoutedRun is the exported seam the coding delivery activity uses to place
// a run into the live rendezvous. Kept here (agents owns targets + sessions) so
// the machine-facing HTTP surface and the durable activity share ONE mailbox.
func OfferRoutedRun(run RoutedRun) *offer { return routedMailbox.Offer(run) }

package risk

// agency.go decides what KIND of actor a decision is about, from facts the
// caller does not get to state.
//
// THIS IS THE DIFFERENTIATOR, SO IT HAS TO BE REAL. Telling a declared,
// registered, metered agent from an anonymous script is the thing nobody who does
// not run agents can compute — and it is worth exactly nothing if the answer is a
// string off the request body. The first cut read `actor.agent != ""` and called
// that "declared", while its own comment claimed a registry lookup: any caller
// could type a name and be classified `agent`, and the `bot` lane was
// structurally unreachable besides, because reaching any op here already requires
// a validated principal.
//
// WHAT IS ACTUALLY KNOWN. The registry of a tenant's agents belongs to the agents
// app, in another process, so it is asked over the internal plane
// (plane.AgentsDeclared) with the caller's own identity — the org rides the
// caller and can never be an argument, so a reference can only ever resolve
// against the ASKING org's registry.
//
//	resolves       the org registered this agent      → AgencyAgent
//	does not       the caller claimed one that is not → AgencyBot
//	nothing named  no claim to check                  → AgencyHuman or AgencyUnknown
//	cannot ask     the registry is unreachable        → AgencyUnknown + RefusalUnverified
//
// A CLAIM THAT DOES NOT RESOLVE IS THE STRONGEST BOT SIGNAL AVAILABLE, which is
// why it is not merely downgraded to unknown: an actor asserting an agency the
// registry can disprove has done something an honest one never does. That is also
// what makes the bot lane reachable — it is entered by a fact, not by the absence
// of a credential the surface already requires.
//
// WHAT BOUNDS THE LOOKUP, EXACTLY. It happens only when the caller CLAIMS an
// agent, so the cost lands on the claimant, and every decide that reaches it is
// itself metered on that caller's ledger — the registry call is one per PAID
// decision, never an amplification of one. A repeated reference costs nothing
// after the first: answers are memoised per tenant, negatives included, under
// that tenant's own bound. A NOVEL reference on every request is still a call on
// every request, which is why the call carries a deadline measured against an
// authorization window: a payment decision waiting on a registry is a payment
// plane that fails when the registry does.
//
// A reference longer than the registry can name is answered HERE, without the
// call. agents bounds an agent label at 128 bytes and answers "not declared" to
// anything longer, so asking is a round trip whose answer is already known — and
// taking that answer as a verdict would classify a legitimately long name as a
// bot on a fact about our own field width rather than about the caller.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// agentsApp is the manifest name of the process holding the registry.
const agentsApp = "agents"

// verifyBudget is how long a decision will wait for the registry. An
// authorization window is tens of milliseconds of slack, not seconds; past this
// the honest answer is that agency could not be verified, and that answer is
// published rather than guessed.
const verifyBudget = 250 * time.Millisecond

// The cache. Positive and negative answers are both kept, because a negative is
// the answer a flood produces and caching only the positives would leave exactly
// the abusive case uncached.
const (
	// agencyTTL is how long a resolution is trusted. Short: an agent retired
	// this minute must stop counting as declared this minute.
	agencyTTL = time.Minute
	// agencyCacheMax bounds the per-tenant cache. It is per tenant like
	// everything else here, so a tenant inventing references evicts its OWN
	// oldest answers and nobody else's — and every entry is at most refMax
	// bytes, so the count is a byte figure (agencyMemo in bound.go) and not a
	// number over values the caller sizes.
	agencyCacheMax = 1024
	// refMax is the longest agent reference the registry can name: agents'
	// own maxAgentLabel (apps/agents/sessions.go). Stated here because it is
	// what makes a longer claim answerable without a call.
	refMax = 128
)

// registry answers whether a reference names an agent in an org's own registry.
// An interface with one method, so a test can state the registry's answer
// without a second process — and so the production path has exactly one
// implementation and no flag choosing between them.
type registry interface {
	declared(ctx context.Context, ref string) (bool, error)
}

// peerRegistry is the production implementation: the agents app, over the
// internal plane, as the caller.
type peerRegistry struct{}

func (peerRegistry) declared(ctx context.Context, ref string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyBudget)
	defer cancel()
	out, err := cloud.Ask[plane.AgentRef, plane.AgentDeclared](ctx, agentsApp, plane.AgentsDeclared,
		&plane.AgentRef{Ref: ref})
	if err != nil {
		return false, err
	}
	return out.Declared, nil
}

// agencyCache is one tenant's memo of registry answers.
type agencyCache struct {
	mu  sync.Mutex
	at  map[string]agencyAnswer
	seq uint64
}

type agencyAnswer struct {
	declared bool
	until    time.Time
	// used orders eviction within this tenant's own cache.
	used uint64
}

func (c *agencyCache) get(ref string, now time.Time) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.at[ref]
	if !ok || now.After(a.until) {
		return false, false
	}
	c.seq++
	a.used = c.seq
	c.at[ref] = a
	return a.declared, true
}

func (c *agencyCache) put(ref string, declared bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at == nil {
		c.at = make(map[string]agencyAnswer, 16)
	}
	if len(c.at) >= agencyCacheMax {
		c.evictLocked()
	}
	c.seq++
	c.at[ref] = agencyAnswer{declared: declared, until: now.Add(agencyTTL), used: c.seq}
}

// clear forgets everything this tenant memoised. Called when its cell is
// retired, so a retired tenant costs nothing at all — a bounded map left behind
// by every tenant that ever passed through is still an unbounded process.
func (c *agencyCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = nil
}

// evictLocked drops this tenant's least recently used answer. Caller holds mu.
func (c *agencyCache) evictLocked() {
	var oldest string
	var at uint64
	first := true
	for ref, a := range c.at {
		if first || a.used < at {
			oldest, at, first = ref, a.used, false
		}
	}
	if oldest != "" {
		delete(c.at, oldest)
	}
}

// agencyOf classifies the actor a decision is about.
//
// It returns the agency AND a refusal, because "we could not check" is a third
// answer and folding it into `unknown` would make an unreachable registry look
// like an ordinary unclassified caller. A refusal here rides the decision, so a
// reader can tell a classification from a gap in one.
// user is the VALIDATED user id the scope carries — empty for a machine
// credential. It is taken as a value rather than read off the request here,
// because tenantOf already resolved the whole principal and two readers of the
// raw request would be two places the identity rules live.
func agencyOf(ctx context.Context, reg registry, cache *agencyCache, user string, obs observation) (string, string) {
	ref := strings.TrimSpace(obs.agent)
	if ref == "" {
		// No claim to check. A live session bound to a validated user is a
		// person; anything else is honestly unclassified, and an honest "we
		// cannot tell" is worth more than a guess in either direction.
		if user != "" && strings.TrimSpace(obs.session) != "" {
			return AgencyHuman, ""
		}
		return AgencyUnknown, ""
	}
	if len(ref) > refMax {
		// Longer than the registry can name, so there is no lookup to make and no
		// verdict to reach. Unknown + unverified is the honest answer: the claim
		// was not checked, and saying it was disproved would be a statement about
		// a field width rather than about the caller.
		return AgencyUnknown, RefusalUnverified
	}
	if reg == nil {
		return AgencyUnknown, RefusalUnverified
	}

	now := time.Now()
	if declared, hit := cache.get(ref, now); hit {
		return classify(declared), ""
	}
	declared, err := reg.declared(ctx, ref)
	if err != nil {
		// The registry could not answer. NOT a bot and NOT an agent: the claim
		// is unchecked, and saying so is the only answer that does not silently
		// promote or demote whoever is asking.
		return AgencyUnknown, RefusalUnverified
	}
	cache.put(ref, declared, now)
	return classify(declared), ""
}

// classify turns the registry's verdict into an agency.
//
// Two lanes from one verified fact, which is the whole vocabulary this can
// honestly support: the org registered this agent, or it did not and something is
// claiming otherwise.
func classify(declared bool) string {
	if declared {
		return AgencyAgent
	}
	return AgencyBot
}

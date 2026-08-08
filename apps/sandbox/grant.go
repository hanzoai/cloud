package sandbox

// grant.go — a credential that authenticates INFERENCE for one run and nothing
// else.
//
// A coding run needs to call a model. What it must NOT be given to do that is
// the caller's own bearer, and two designs have already failed review for
// exactly that: one widened a general resolver into a cross-tenant read, the
// other handed the pod an unscoped machine-identity token. Both fail the same
// way — a run executing model-authored code holds a credential that is good for
// something other than the run.
//
// The threat is not hypothetical and it is not about a malicious user. Indirect
// prompt injection makes the PAGE BEING READ the instruction source: a repo's
// README, a fetched doc, a dependency's changelog. Whatever the pod holds, the
// text it reads can spend. So the only safe credential is one whose entire
// authority is "ask a model, billed to this org, until this lease ends".
//
// ── WHY THIS IS THE TERMINAL TICKET AND NOT A NEW IDEA ───────────────────────
//
// terminal.go already mints one permission to do one thing with one sandbox.
// This is the same value with a different verb, so it is the same shape: 32
// random bytes, bound to (org, sandbox), swept on every mint. Making it a second
// unrelated mechanism would be two answers to "may this pod do X", and the whole
// reason the terminal ticket is trustworthy is that there is one of it.
//
// TWO THINGS DIFFER, and each is a property of inference rather than a taste:
//
//   - IT IS NOT SINGLE-USE. A terminal ticket is spent on one dial; a run calls
//     the model repeatedly for as long as it works. Redeeming would kill the run
//     on its second thought.
//   - IT LIVES AS LONG AS THE RUN, not thirty seconds. The lease is the bound
//     that matters, so the grant expires WITH it: when the reaper takes the
//     sandbox the credential in it is already worthless, which is what makes
//     "the pod was compromised" a bounded statement.
//
// ── WHAT IT DOES NOT CARRY ───────────────────────────────────────────────────
//
// No user identity. It answers ORG, because billing is per-org and inference is
// the only thing it authorises — a run cannot read the user's projects, rotate a
// key, or reach any other product with it. That is the difference between this
// and the machine-identity token that was rejected: that one was an identity,
// and this is a permission.

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// grant is one permission to ask a model, on behalf of one org, from inside one
// sandbox, until one moment.
type grant struct {
	org     string
	sandbox string
	expires time.Time
}

// grants is the live set, swept on every mint like the ticket set it mirrors.
// Bounded by the number of live sandboxes rather than by traffic: a run holds
// exactly one, and the reaper's lease bound is what keeps the map small.
type grants struct {
	mu   sync.Mutex
	live map[string]grant
}

func newGrants() *grants { return &grants{live: map[string]grant{}} }

// mint issues the run's inference credential, expiring no later than the lease.
//
// `now` is a parameter for the same reason it is one in tickets: expiry is then
// a fact a test can state rather than one it has to wait for.
func (g *grants) mint(now time.Time, org, sandbox string, expires time.Time) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := "hsb_" + base64.RawURLEncoding.EncodeToString(b[:])
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(now)
	g.live[tok] = grant{org: org, sandbox: sandbox, expires: expires}
	return tok, nil
}

// check answers which org may be billed for an inference call made with tok, and
// whether it may be made at all.
//
// It does NOT spend the token — see the file comment: a run thinks more than
// once. The bound is time and the lease, not a count.
func (g *grants) check(now time.Time, tok string) (org string, sandbox string, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k, found := g.live[tok]
	if !found || !now.Before(k.expires) {
		return "", "", false
	}
	return k.org, k.sandbox, true
}

// revoke drops a sandbox's grant the moment its lease ends.
//
// Expiry alone would be enough eventually, and eventually is the problem: a
// sandbox released early — a run that finished, a user who stopped it — would
// otherwise leave a working credential for the rest of its window, in a pod
// whose whole point was that it is gone. The reaper and the release path both
// call this, so "the sandbox is over" and "its credential is worthless" are one
// event rather than two that usually coincide.
func (g *grants) revoke(sandbox string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for tok, k := range g.live {
		if k.sandbox == sandbox {
			delete(g.live, tok)
		}
	}
}

// sweep drops what has expired. Called under the lock by mint, so an unspent
// grant cannot accumulate.
func (g *grants) sweep(now time.Time) {
	for tok, k := range g.live {
		if !now.Before(k.expires) {
			delete(g.live, tok)
		}
	}
}

// size is for tests and for the one log line that says whether anything leaked.
func (g *grants) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.live)
}

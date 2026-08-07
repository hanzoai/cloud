package ai

// run.go is the credential a coding run holds to BUY INFERENCE, and it is
// deliberately not an identity.
//
// # What it replaces
//
// Nothing, which is the point. There was no way for a sandbox to reach a model at
// all, and the obvious repair — put an API key on the box's environment — is the
// mistake apps/git/grant.go already paid for once with the org's git token. A key
// that IAM can resolve is a USER: cloud mints X-Org-Id and X-User-Id from it, and
// the one process executing untrusted model output holds a credential that opens
// /v1/kms/secrets and every other org-scoped door. It would also bypass the
// per-org meter, so the org would never be billed for its own agent's tokens.
//
// # What a run grant is
//
// A bounded permission to spend ONE org's inference balance, until it expires. It
// authenticates NOBODY: it names an act, not a person, and the containment follows
// structurally rather than from a list of checks:
//
//   - It carries none of cloud's key prefixes (pk-/sk-), so validatedPrincipal
//     cannot resolve it, no X-User-Id is minted, and every cloud.Guard refuses it.
//   - The table lives in THIS PROCESS. apps are separate binaries routed by prefix
//     (manifest/apps.go), so /v1/kms/* is served by a process that has never heard
//     of this token and has no code to read it. A run grant presented there is
//     simply an unknown bearer — the same 401 a stranger gets. Nothing had to be
//     told to say no.
//   - The ORG is in the grant, not in the request. The inference door reads the
//     payer from the token, so a run cannot be aimed at another tenant's ledger by
//     a header (controllers.resolveProviderFromRunKey states the same rule).
//   - Every call it makes goes through the ordinary balance gate, budget
//     reservation and usage debit. It is a cheaper credential, never a cheaper
//     call, and there is no exempt path.
//
// # Why it lives here
//
// ai owns the inference door, so ai decides who may spend at it — the same
// sentence apps/git/grant.go makes about refs. The orchestrator asks for a grant
// over the internal plane at the moment it dispatches, and is no longer a
// credential custodian because there is no standing credential to custody.
//
// # Lifetime
//
// A grant lives in the process that will judge it and dies with it: no key to
// manage, nothing at rest, nothing to leak from disk. A restart of the ai process
// invalidates every outstanding grant, which fails the run's next model call
// CLOSED — the honest direction. The token is held only as its SHA-256, so a heap
// dump yields nothing usable.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	aictl "github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// runPrefix marks the token as what it is, in the one place a human or a secret
// scanner will see it. It matches the spelling the inference door dispatches on
// (controllers.runPrefix) and is deliberately NOT one of cloud's API-key prefixes
// (auth_identity.go APIKeyPrefixes): a grant that could be mistaken for a key
// would be sent to IAM, and a credential class is only narrow if nothing upstream
// tries to widen it.
const runPrefix = "hrun_"

const (
	// runMaxTTL caps a grant a little beyond the longest run the engine admits, so
	// a model call at the very end of a run still lands. A caller asking for longer
	// gets this; a caller asking for nothing gets this.
	runMaxTTL = 30 * time.Minute

	// runMax bounds the live table. The plane is a trusted socket, but a bound that
	// only holds while every peer behaves is not a bound. Issue refuses past this —
	// fail closed, and a refused dispatch is a run that does not start.
	runMax = 4096
)

// runGrant is one bounded permission: this org's ledger, this run, until this time.
type runGrant struct {
	org, run string
	expires  time.Time
}

// runs is the process's live grant table, keyed by the SHA-256 of the bearer. The
// token itself is never stored: a lookup hashes what was presented and finds the
// entry, which is also why no constant-time comparison is needed — there is no
// secret in the table to compare against.
type runs struct {
	mu sync.Mutex
	m  map[[32]byte]runGrant
}

// granted is the process's ONE table, package-level for the same reason apps/git's
// is: it is per-process state of the inference door, and that door is one process.
var granted = runs{m: make(map[[32]byte]runGrant)}

// issue mints a grant and returns the bearer plus the handle that revokes it. The
// handle is the hex of the key — safe to log, useless as a credential — so
// revoking never means presenting the secret a second time.
func (g *runs) issue(gr runGrant, ttl time.Duration) (token, handle string, err error) {
	if ttl <= 0 || ttl > runMaxTTL {
		ttl = runMaxTTL
	}
	gr.expires = time.Now().Add(ttl)

	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("ai: rng: %w", err)
	}
	token = runPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	key := sha256.Sum256([]byte(token))

	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(time.Now())
	if len(g.m) >= runMax {
		return "", "", fmt.Errorf("ai: too many outstanding run grants")
	}
	g.m[key] = gr
	return token, hex.EncodeToString(key[:]), nil
}

// lookup resolves a presented bearer. An unknown, malformed or expired token is
// simply not a grant — there is no third answer.
func (g *runs) lookup(token string) (runGrant, bool) {
	if !strings.HasPrefix(token, runPrefix) {
		return runGrant{}, false
	}
	key := sha256.Sum256([]byte(token))
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[key]
	if !ok {
		return runGrant{}, false
	}
	if !time.Now().Before(gr.expires) {
		delete(g.m, key)
		return runGrant{}, false
	}
	return gr, true
}

// revoke drops a grant by handle, for the org that holds it. The org check is what
// keeps revocation from being a cross-tenant denial of service: knowing a handle
// is not authority over it.
func (g *runs) revoke(org, handle string) {
	raw, err := hex.DecodeString(handle)
	if err != nil || len(raw) != 32 {
		return
	}
	var key [32]byte
	copy(key[:], raw)
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr, ok := g.m[key]; ok && gr.org == org {
		delete(g.m, key)
	}
}

// sweep drops expired entries. Called under the lock on every issue, so the table
// is bounded by what is LIVE rather than by what was ever minted, and no timer
// goroutine has to exist to make that true.
func (g *runs) sweep(now time.Time) {
	for k, gr := range g.m {
		if !now.Before(gr.expires) {
			delete(g.m, k)
		}
	}
}

// ---- the two doors --------------------------------------------------------

// exposeRun publishes the grant ops on the internal plane and installs the
// resolver the inference door asks. Mount calls it.
//
// Both halves are here because they are the same fact seen from either side: this
// table is what a grant IS, and the resolver is the only way anything reads it.
// Splitting them would put the table's one reader somewhere it could be forgotten.
func exposeRun() {
	// THE ONE READ POINT. controllers.resolveRun is the only thing in the binary
	// that consults this table, so the set of doors a run grant opens is the set of
	// doors that call it: the inference path, and nothing else.
	aictl.SetRunResolver(func(token string) (aictl.Run, bool) {
		gr, ok := granted.lookup(token)
		if !ok {
			return aictl.Run{}, false
		}
		return aictl.Run{Org: gr.org, ID: gr.run}, true
	})

	zip.Post[plane.RunGrantIn, plane.RunGranted](cloud.Plane(), "/ai/grant", planeRunGrant,
		zip.WithOperationID(plane.AIGrant),
		zip.WithSummary("Delegate the right to buy inference on ONE org's ledger for ONE run"))

	zip.Post[plane.RevokeIn, plane.Revoked](cloud.Plane(), "/ai/revoke", planeRunRevoke,
		zip.WithOperationID(plane.AIRevoke),
		zip.WithSummary("Drop a run's inference grant before it expires"))
}

// planeRunGrant mints an inference grant for the CALLER's org.
//
// The org is the caller's plane identity and never an argument, so an app acting
// for one tenant cannot spend another's balance — the same rule planeGrant states
// in apps/git, for the same reason.
func planeRunGrant(ctx context.Context, in *plane.RunGrantIn) (*plane.RunGranted, error) {
	who := cloud.Who(ctx)
	if strings.TrimSpace(who.Org) == "" {
		return nil, zip.ErrForbidden("ai grant: org required")
	}
	run := strings.TrimSpace(in.Run)
	if run == "" {
		return nil, zip.ErrBadRequest("ai grant: a grant is always for a named run")
	}
	token, handle, err := granted.issue(
		runGrant{org: who.Org, run: run},
		time.Duration(in.TTLSeconds)*time.Second)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	gr, _ := granted.lookup(token)
	return &plane.RunGranted{Token: token, Handle: handle, ExpiresAt: gr.expires.Unix()}, nil
}

// planeRunRevoke drops a grant the caller's org holds, so a grant's life is the
// RUN's life rather than its TTL. The TTL is the backstop for a run that dies
// without saying so; this is the ordinary path.
func planeRunRevoke(ctx context.Context, in *plane.RevokeIn) (*plane.Revoked, error) {
	who := cloud.Who(ctx)
	if strings.TrimSpace(who.Org) == "" {
		return nil, zip.ErrForbidden("ai revoke: org required")
	}
	granted.revoke(who.Org, strings.TrimSpace(in.Handle))
	return &plane.Revoked{}, nil
}

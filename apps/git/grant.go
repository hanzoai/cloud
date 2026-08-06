package git

// grant.go is the credential a coding run holds, and it is deliberately not an
// identity.
//
// # What was wrong
//
// A run used to carry the org's sealed `agent` git token — an ordinary IAM
// secret key. IAM resolves such a key to a USER, cloud mints X-Org-Id and
// X-User-Id from it, and cloud.Member "admits any validated principal". So the
// one process executing untrusted model output held a credential that opened
// every org-scoped door in the platform: /v1/kms/secrets (the org's other
// secrets, including the ones that post to its Slack), /v1/git/repos/*/mirror,
// every /v1/* surface the org has. The push was confined; the CREDENTIAL was
// not, and the credential is what a compromised run actually has.
//
// # What a grant is
//
// A bounded permission to drive git's pack protocol against ONE repository,
// creating ONE ref, until it expires. It authenticates NOBODY: it names an act,
// not a person. That is the whole design, and the containment follows from it
// structurally rather than from a list of checks:
//
//   - It is not a JWT and carries none of cloud's key prefixes (pk-/sk-), so
//     validatedPrincipal cannot resolve it. No X-User-Id is minted, therefore
//     principal.Validated is false, therefore EVERY cloud.Guard refuses it and
//     every typed git op's tenantOf refuses it. Nothing had to be told to say
//     no — saying no is the default, and this file is the single exception.
//   - The exception is read in exactly one place (resolvePackRepo), so the set
//     of doors a grant opens is the set of doors that call that function: the
//     three smart-HTTP pack handlers, and nothing else in the binary.
//   - The repository is IN the grant, not in the request. A grant for
//     acme/widgets addresses acme/widgets and answers 404 for everything else,
//     the same 404 a stranger gets.
//   - The ref is IN the grant too, and travels to the ref policy, which refuses
//     any command naming another ref. A run cannot touch main even in the
//     repository it is working in.
//
// # Why it lives here and not in the orchestrator
//
// git owns refs, so git decides who may write one. The orchestrator asks for a
// grant over the internal plane at the moment it dispatches, exactly where it
// used to ask KMS for the org's token — the same one hop, but what comes back
// can only do the job. The orchestrator is no longer a credential custodian;
// there is no longer an org-wide agent token for it to custody.
//
// # Lifetime
//
// A grant lives in the process that will judge it and dies with it. There is no
// key to manage, no signature to verify, nothing at rest that survives a restart
// and nothing to leak from disk. A restart of the forge invalidates every
// outstanding grant, which fails the run's push CLOSED — the honest direction.
// The token is held only as its SHA-256, so a heap dump yields nothing usable.
//
// This makes the store process-local, which is correct for a forge that serves
// the bare repositories off one read-write volume and is therefore single-writer
// by construction. If it is ever replicated, a grant minted on one replica is
// simply unknown to another and the push is refused — it degrades to a refusal,
// never to an admission.

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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// grantPrefix marks the token as what it is, in the one place a human or a
// secret scanner will see it. It is deliberately NOT one of cloud's API-key
// prefixes (auth_identity.go APIKeyPrefixes): a grant that could be mistaken for
// a key would be sent to IAM, and a credential class is only narrow if nothing
// upstream tries to widen it.
const grantPrefix = "hgg_"

const (
	// grantMaxTTL caps a grant at a little over the longest run the engine will
	// admit (coding.defaultRunBudget is 25 minutes). A caller asking for longer
	// gets this; a caller asking for nothing gets this.
	grantMaxTTL = 30 * time.Minute

	// grantMax bounds the live table. The plane is a trusted socket, but a bound
	// that only holds while every peer behaves is not a bound. Issue refuses past
	// this — fail closed, and a refused dispatch is a run that does not start.
	grantMax = 4096
)

// grant is one bounded permission: this repository, this ref, until this time.
type grant struct {
	org, project, repo string
	// ref is the FULL ref (refs/heads/agent/<x>) and the ONLY one this grant may
	// write. It is a create — the ref policy independently refuses a rewrite —
	// so the two controls answer different questions and neither is the other's
	// backstop.
	ref     string
	expires time.Time
}

// grants is the forge's live grant table, keyed by the SHA-256 of the bearer.
// The token itself is never stored: a lookup hashes what was presented and finds
// the entry, which is also why no constant-time comparison is needed — there is
// no secret in the table to compare against.
type grants struct {
	mu sync.Mutex
	m  map[[32]byte]grant
}

// issued is the process's ONE table. Package-level for the same reason packSem
// is: it is per-process state of the forge itself, and the forge is one process.
var issued = grants{m: make(map[[32]byte]grant)}

// issue mints a grant and returns the bearer plus the handle that revokes it.
//
// The handle is the hex of the key — safe to log, safe to carry back over the
// plane, and useless as a credential, so revoking never means presenting the
// secret a second time.
func (g *grants) issue(gr grant, ttl time.Duration) (token, handle string, err error) {
	if ttl <= 0 || ttl > grantMaxTTL {
		ttl = grantMaxTTL
	}
	gr.expires = time.Now().Add(ttl)

	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("git: rng: %w", err)
	}
	token = grantPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	key := sha256.Sum256([]byte(token))

	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(time.Now())
	if len(g.m) >= grantMax {
		return "", "", fmt.Errorf("git: too many outstanding push grants")
	}
	g.m[key] = gr
	return token, hex.EncodeToString(key[:]), nil
}

// lookup resolves a presented bearer to its grant. An unknown, malformed or
// expired token is simply not a grant — there is no third answer.
func (g *grants) lookup(token string) (grant, bool) {
	if !strings.HasPrefix(token, grantPrefix) {
		return grant{}, false
	}
	key := sha256.Sum256([]byte(token))
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[key]
	if !ok {
		return grant{}, false
	}
	if !time.Now().Before(gr.expires) {
		delete(g.m, key)
		return grant{}, false
	}
	return gr, true
}

// revoke drops a grant by handle, for the org that holds it. The org check is
// what keeps revocation from being a cross-tenant denial of service: knowing a
// handle is not authority over it.
func (g *grants) revoke(org, handle string) {
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

// sweep drops expired entries. Called under the lock on every issue, so the
// table is bounded by what is LIVE rather than by what was ever minted, and no
// timer goroutine has to exist to make that true.
func (g *grants) sweep(now time.Time) {
	for k, gr := range g.m {
		if !now.Before(gr.expires) {
			delete(g.m, k)
		}
	}
}

// ---- the plane door -------------------------------------------------------

// exposeGrant publishes the grant ops on the internal plane. Mount calls it.
//
// It is a separate boundary from the import ops (import_plane.go) because it
// answers a different question — that one moves repositories between processes,
// this one delegates a write — and a file that held both would be saying they
// are the same seam.
func exposeGrant() {
	zip.Post[plane.GrantIn, plane.Granted](cloud.Plane(), "/git/grant", planeGrant,
		zip.WithOperationID(plane.GitGrant),
		zip.WithSummary("Delegate the right to create ONE ref in ONE repository"))

	zip.Post[plane.RevokeIn, plane.Revoked](cloud.Plane(), "/git/revoke", planeRevoke,
		zip.WithOperationID(plane.GitRevoke),
		zip.WithSummary("Drop a push grant before it expires"))
}

// planeGrant mints a push grant for the CALLER's org.
//
// The org is the caller's plane identity and never an argument, so an app acting
// for one tenant cannot delegate a write into another's repository — the same
// rule planeImport and planeInbound state, for the same reason.
//
// The ref is checked against the agent namespace HERE as well as at the pack
// door. Not as a second line of defence but because it is a different sentence:
// the door says "this push may only write the ref its grant names", and this
// says "the only ref the forge will ever delegate is a machine ref". A grant for
// refs/heads/main would satisfy the door and must therefore never be minted.
func planeGrant(ctx context.Context, in *plane.GrantIn) (*plane.Granted, error) {
	who := cloud.Who(ctx)
	if who.Org == "" || !orgRE.MatchString(who.Org) {
		return nil, zip.ErrForbidden("git grant: org required")
	}
	repo := normalizeName(in.Repo)
	if repo == "" || !nameRE.MatchString(repo) {
		return nil, zip.ErrBadRequest("git grant: invalid repo name")
	}
	project := strings.TrimSpace(in.Project)
	if project != "" && !projectRE.MatchString(project) {
		return nil, zip.ErrBadRequest("git grant: invalid project")
	}
	ref := strings.TrimSpace(in.Ref)
	if !refRE.MatchString(ref) || !strings.HasPrefix(ref, agentRefPrefix) {
		return nil, zip.ErrBadRequest("git grant: a grant is only ever for " + agentRefPrefix + "*")
	}
	// The repository must exist and belong to this org. A grant for a repo that
	// is not there would otherwise be a grant to CREATE one on first push, which
	// is a capability nobody asked for.
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "git grant: the forge is not mounted")
	}
	store, err := storeFor(s, who.Org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "git grant: open store: %v", err)
	}
	if _, gerr := store.Get(ctx, who.Org, project, repo); gerr != nil {
		return nil, zip.ErrNotFound("repo not found")
	}
	token, handle, err := issued.issue(
		grant{org: who.Org, project: project, repo: repo, ref: ref},
		time.Duration(in.TTLSeconds)*time.Second)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	gr, _ := issued.lookup(token)
	return &plane.Granted{Token: token, Handle: handle, ExpiresAt: gr.expires.Unix()}, nil
}

// planeRevoke drops a grant the caller's org holds, so a grant's life is the
// RUN's life rather than its TTL. The TTL is the backstop for a run that dies
// without saying so; this is the ordinary path.
func planeRevoke(ctx context.Context, in *plane.RevokeIn) (*plane.Revoked, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git revoke: org required")
	}
	issued.revoke(who.Org, strings.TrimSpace(in.Handle))
	return &plane.Revoked{}, nil
}

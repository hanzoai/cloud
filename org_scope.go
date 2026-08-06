package cloud

// Org sub-scope sanitization — the project/app half of the identity trust
// boundary (the org half is SanitizeIdentity in middleware_identity.go).
//
// THE PROBLEM. X-Org-Id is minted un-forgeably from the validated IAM owner, but
// X-Project-Id / X-App-Id were historically passed through verbatim: a caller
// could assert ANY project, including one REGISTERED to ANOTHER org, and that
// value reached downstream attribution (compute_usage.project) and the optional
// per-project sub-scope some subsystems read. The un-forgeable org column already
// bounds a forged label to the attacker's OWN subtree (every consumer AND-s the
// validated org), but asserting another org's registered project id is a
// cross-org identity claim we refuse at the boundary.
//
// THE RULE (one predicate, one place). A forwarded X-Project-Id must never name a
// project REGISTERED to a different org than the caller's validated org. It is
// refused iff it is registered to some OTHER org AND not to the caller's own — a
// precise cross-org impersonation guard. An UNREGISTERED value (the free-form,
// within-org sub-scope label the git/security/eval subsystems already accept) is
// PRESERVED: it can only ever scope the caller's own org data, so it is not a
// cross-org claim. The caller's OWN registered project is preserved (provably
// theirs). This IS the "project-membership check UNDER the validated org" the
// data plane always required of per-project scope.
//
// DEPENDENCY INVERSION (why a resolver, not an import). The project registries
// (clients/projects, clients/platform) import THIS package, so cloud must not
// import them. Each registry registers a OrgScopeResolver at its Mount —
// exactly like sites.SetResolver — and the boundary consults the registered set
// per request. With no registry mounted, nothing is ever "foreign", so the guard
// is a no-op passthrough (an unmounted registry owns nothing), never a crash.

// THE HOLE THIS CLOSED. "With no registry mounted, nothing is ever foreign" was
// written as a safe default and was not one, because no registry is EVER mounted
// where this is read. The boundary is edge middleware in every process; the
// registry belongs to `projects`, which runs as its own plugin. So the guard
// consulted an empty list, concluded "not foreign", and passed every asserted
// X-Project-Id through — the cross-org project impersonation check was off
// fleet-wide, and its own passing tests only ever exercised the co-resident case.
//
// Absence of an ANSWER may never read as permission. The registry is now ASKED,
// over the plane, and the three outcomes stay three:
//
//	the answer says mine        keep      (provably the caller's own)
//	the answer says other       refuse    (a cross-org claim)
//	the answer says neither     keep      (an unregistered within-org label)
//	no registry in the fleet    keep      — and this is now PROVEN rather than
//	                                       assumed: ErrNoPeer means no projects
//	                                       app exists anywhere, so no project id
//	                                       is registered and none can be foreign
//	the registry failed         REFUSE    (fail closed, as before)
//
// The read is cached per (org, project) with a short TTL, on a detached context,
// exactly like the scope-rule read in middleware_ratelimit.go — this runs on
// every request that asserts a project, and a client disconnect must not poison
// what the cache holds.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// OrgScopeResolver reports the ownership of a project identifier relative to
// an org, WITHOUT this package importing the registry that holds it. The
// identifier may be a slug or an opaque id — the implementation matches whichever.
type OrgScopeResolver interface {
	// ProjectOwnership reports, for the project addressed by idOrSlug:
	//   mine  — org itself owns a project with this id/slug,
	//   other — some org OTHER than org owns a project with this id/slug.
	// A store failure is returned as err; the boundary then fails CLOSED (refuses
	// the claim) so a transient registry error can never let a cross-org claim
	// through.
	ProjectOwnership(ctx context.Context, org, idOrSlug string) (mine, other bool, err error)
}

var (
	orgResolverMu sync.RWMutex
	orgResolvers  []OrgScopeResolver
)

// RegisterOrgScopeResolver adds a project-ownership registry consulted by the
// identity trust boundary. Called once per registry at its Mount (mirrors
// sites.SetResolver). Registries COMPOSE: a project is "mine" if ANY registry
// owns it for the org, and "foreign" only if some registry owns it for another
// org while NONE owns it for this org. A nil resolver is ignored.
func RegisterOrgScopeResolver(r OrgScopeResolver) {
	if r == nil {
		return
	}
	orgResolverMu.Lock()
	orgResolvers = append(orgResolvers, r)
	orgResolverMu.Unlock()
}

// ResetOrgScopeResolvers drops every registered registry. TEST-ONLY: a test
// mounts and unmounts repeatedly, and registration APPENDS, so without this a
// later test is answered by an earlier one's registry. Production registers once
// at Mount.
func ResetOrgScopeResolvers() {
	orgResolverMu.Lock()
	orgResolvers = nil
	orgResolverMu.Unlock()
	ownershipCache.Lock()
	ownershipCache.at = nil
	ownershipCache.Unlock()
}

func currentOrgResolvers() []OrgScopeResolver {
	orgResolverMu.RLock()
	rs := orgResolvers
	orgResolverMu.RUnlock()
	return rs
}

// ownershipTTL bounds how long one verdict is reused. Short, because it gates a
// cross-org claim: a project moved between orgs must stop being "mine" quickly.
// Long enough that a burst of requests from one caller costs one plane call.
const ownershipTTL = 30 * time.Second

var ownershipCache struct {
	sync.Mutex
	at map[string]ownershipEntry
}

type ownershipEntry struct {
	mine, other bool
	expiry      time.Time
}

// ProjectOwnership answers whether org owns the project named by idOrSlug, and
// whether some other org does.
//
// It is TOTAL: co-resident registries answer directly, and otherwise the
// projects app is asked over the plane. There is no third state where it
// silently declines to look — the only way it produces no answer is by
// returning an error, and ErrNoPeer specifically means no registry exists in
// this fleet at all.
//
// Both booleans false with a nil error is a real answer: nobody has registered
// this identifier, so it is a free-form within-org label.
func ProjectOwnership(ctx context.Context, org, idOrSlug string) (mine, other bool, err error) {
	if rs := currentOrgResolvers(); len(rs) > 0 {
		var hadErr error
		for _, r := range rs {
			m, o, rerr := r.ProjectOwnership(ctx, org, idOrSlug)
			if rerr != nil {
				hadErr = rerr
				continue
			}
			mine = mine || m
			other = other || o
		}
		if mine || other {
			return mine, other, nil
		}
		return false, false, hadErr
	}

	key := org + "\x00" + idOrSlug
	ownershipCache.Lock()
	e, ok := ownershipCache.at[key]
	ownershipCache.Unlock()
	if ok && time.Now().Before(e.expiry) {
		return e.mine, e.other, nil
	}

	// The org is STATED rather than forwarded: this may run on a detached context,
	// and the question is "does THIS org own it", which the callee must be told.
	out, err := Ask[plane.OwnerIn, plane.Ownership](For(ctx, org), "projects",
		plane.ProjectsOwnership, &plane.OwnerIn{IDOrSlug: idOrSlug})
	if err != nil {
		return false, false, err
	}
	if out == nil {
		return false, false, errors.New("cloud: projects answered nothing about project ownership")
	}

	ownershipCache.Lock()
	if ownershipCache.at == nil {
		ownershipCache.at = map[string]ownershipEntry{}
	}
	ownershipCache.at[key] = ownershipEntry{mine: out.Mine, other: out.Other, expiry: time.Now().Add(ownershipTTL)}
	ownershipCache.Unlock()
	return out.Mine, out.Other, nil
}

// projectIsForeign reports whether project is a cross-org impersonation for org:
// registered to some OTHER org and NOT to org itself. The aggregation is exact
// across registries — a project the caller's org owns in ANY registry is never
// foreign, even if a same-slug project exists under another org in a different
// registry. Fails CLOSED: a registry error with no confirming "mine" treats the
// claim as foreign (refused), never forwarding an unverifiable claim. An
// unregistered value (no registry knows it) is NOT foreign — a free-form,
// within-org label survives.
func projectIsForeign(ctx context.Context, org, project string) bool {
	if org == "" || project == "" {
		return false
	}
	mine, other, err := ProjectOwnership(ctx, org, project)
	switch {
	case mine:
		return false // provably the caller's own project → keep
	case other:
		return true // only another org's registered project → refuse
	case err == nil:
		return false // nobody registered it → a free-form within-org label → keep
	case errors.Is(err, ErrNoPeer):
		// No projects app anywhere in this fleet, so no project identifier is
		// registered and none can belong to another org. This is the ONE case the
		// old passthrough was right about, and it is the only one it could tell
		// apart from a registry that simply was not in this process — which was
		// every other case, and why the guard was off.
		return false
	default:
		return true // the registry is there and could not answer → fail CLOSED
	}
}

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

import (
	"context"
	"sync"
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

func currentOrgResolvers() []OrgScopeResolver {
	orgResolverMu.RLock()
	rs := orgResolvers
	orgResolverMu.RUnlock()
	return rs
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
	var mine, other, hadErr bool
	for _, r := range currentOrgResolvers() {
		m, o, err := r.ProjectOwnership(ctx, org, project)
		if err != nil {
			hadErr = true
			continue
		}
		mine = mine || m
		other = other || o
	}
	switch {
	case mine:
		return false // provably the caller's own project → keep
	case other:
		return true // only another org's registered project → refuse
	default:
		return hadErr // unregistered free-form label → keep, unless a lookup failed
	}
}

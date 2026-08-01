package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
)

// tenancy.go holds the ONE way this package reaches storage, and it is short on
// purpose: it is the whole isolation argument, so it has to fit in a reading.
//
// An org's records live in an org's FILE — {DataDir}/orgs/{slug}/agents.db,
// resolved by cloud.OrgDB through SanitizeOrg (the injective slugger) and opened
// under that org's own cek key. There is no cross-org file, so there is no
// cross-org query to write by accident: the boundary is the file, and a query
// cannot reach past the database it runs in.
//
// That only holds if the org naming the file is the VALIDATED one. Every route
// into storage below therefore begins at a value the gateway asserted — the
// principal that cloud.Bridge parked on the context — or at an org an
// in-process caller has already resolved server-side and states as its contract.
// storeFor is the sole producer of a *Store, so "which file does this request
// touch" has exactly one answer and it is derived from exactly one input.
//
// A namespace taken from a path, a query or a body would be the same bug in a
// new costume, and TestNoStoreOutsideTenancy in tenancy_test.go is what keeps it
// that way as the package grows: it fails if any file other than this one
// resolves a store.

// storeFor opens (on first touch) and returns the database holding org's agent
// records. org MUST already be validated — principal.OrgFrom for a request, or
// the caller's own server-side resolution for an in-process seam. cloud.OrgDB
// folds it through SanitizeOrg, so an org that slugger refuses is an error here
// rather than a silent fall-through to another org's file.
func (st *state) storeFor(org string) (*Store, error) {
	if st == nil || st.stores == nil {
		return nil, fmt.Errorf("agents: not mounted")
	}
	return st.stores.For(org, "")
}

// tenantStore is the ONE entry a request-scoped handler uses: the org the
// gateway validated, and the file that org's records live in. It is tenantOf
// followed by storeFor and nothing else, so a handler cannot accidentally pair
// one org's identity with another org's database — there is no call shape that
// lets it name them separately.
func tenantStore(ctx context.Context, st *state) (*Store, string, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	sto, err := st.storeFor(org)
	if err != nil {
		return nil, "", err
	}
	return sto, org, nil
}

// mountedStore is the entry every IN-PROCESS seam uses — ListForOrg,
// TargetsForOrg, ResolveTarget, LoadOn, the inproc session calls, StopSessions,
// SeedPersonalities. Their shared contract is that the CALLER has already
// resolved the org server-side (principal.Org, or a verified token claim) and
// never hands on a raw client header; this re-applies the same bound the HTTP
// path applies, so an org that could not have come from a validated principal is
// refused here rather than reaching the filesystem.
//
// The eight seams used to repeat this check by hand, which is eight chances for
// one of them to be written without it.
func mountedStore(org string) (*Store, string, error) {
	if mounted == nil {
		return nil, "", fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	if org == "" || len(org) > principal.MaxOrgLen {
		return nil, "", fmt.Errorf("agents: invalid org")
	}
	sto, err := mounted.State.storeFor(org)
	if err != nil {
		return nil, "", err
	}
	return sto, org, nil
}

// storeForPublic is storeFor for the ONE read whose org is named by an
// UNAUTHENTICATED caller: the public build page, addressed as {org}/{project}.
//
// It differs in one way that matters. storeFor creates the org's file on first
// touch, which is right for a validated principal and wrong for a stranger: a
// caller who can name any org could otherwise mint a directory and an open
// handle for every name they invent. This refuses to materialise anything —
// an org with no agents store yet simply is not found — so the public route can
// only ever reach a database that already exists because a real tenant wrote to
// it. What that database will then answer is still gated by the published flag.
func (st *state) storeForPublic(org string) (*Store, bool) {
	if st == nil || st.stores == nil || !st.stores.Has(org, "") {
		return nil, false
	}
	sto, err := st.stores.For(org, "")
	if err != nil {
		return nil, false
	}
	return sto, true
}

// eachStore folds fn over every org that has an agents database on disk, handing
// it the org's slug and store. It exists for the two genuinely cross-org reads —
// the scheduler's long-running sweep and the public published-builds list — and
// for nothing else. Both are trusted, non-tenant callers: the scheduler is an
// in-process subsystem, and a published build is on the list only because its
// own author set the publish flag.
//
// A per-org OPEN failure is passed to fn rather than aborting the fold, so one
// unreadable org's file cannot take down the scheduler for every other org.
func (st *state) eachStore(fn func(slug string, sto *Store, err error)) error {
	if st == nil || st.stores == nil {
		return fmt.Errorf("agents: not mounted")
	}
	return st.stores.Each(func(slug string, sto *Store, err error) { fn(slug, sto, err) })
}

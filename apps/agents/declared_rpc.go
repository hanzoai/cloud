package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The registry's yes/no, published on the internal plane.
//
// ListForOrg is the same fact for a subsystem sharing this binary. This is the
// same fact for one that does not — and the risk plane is exactly that: its own
// manifest row, its own process, and a decision that turns on whether an actor
// is a DECLARED agent or an undeclared script. Without this op that distinction
// could only be read off the request body, which is the caller asserting its own
// agency.
//
// The projection is one boolean. See plane.AgentDeclared for why the record
// itself is not the answer.

// exposeDeclared publishes the resolution. Mount calls it.
//
// A NAMED handler, not a closure: zipdoc lifts an op's prose off its handler's
// doc comment and can lift nothing from an anonymous one.
func exposeDeclared() {
	zip.Post[plane.AgentRef, plane.AgentDeclared](cloud.Plane(), "/agents/declared",
		declared,
		zip.WithOperationID(plane.AgentsDeclared),
		zip.WithSummary("Whether a reference names an agent in the caller's own org"))
}

// declared answers whether ref resolves to an agent in the CALLER'S OWN org.
//
// The org is taken from the authenticated call and can never be an argument: it
// is the registry's tenancy key, so a caller able to name it could probe another
// tenant's agents by reference. A call carrying no org is refused rather than
// answered false — "no" and "we would not say" are different facts, and only the
// second one is a reason for the asker to treat the actor as unclassified.
//
// The store comes from mountedStore, the ONE door for an in-process seam whose
// org was resolved server-side. It re-applies the same bound the HTTP path
// applies, so this op cannot be granted an org key a request would have been
// refused — and it FAILS CLOSED when the registry is not open, because answering
// false there would silently reclassify every one of an org's real agents as
// undeclared automation.
func declared(ctx context.Context, in *plane.AgentRef) (*plane.AgentDeclared, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, zip.ErrUnauthorized("agents: no org on the call")
	}
	ref := strings.TrimSpace(in.Ref)
	if ref == "" || len(ref) > maxAgentLabel {
		// An empty or oversized reference names nothing. It is a well-formed
		// question with the answer "no", not an error.
		return &plane.AgentDeclared{}, nil
	}
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, fmt.Errorf("agents: registry unavailable: %w", err)
	}
	if _, err := sto.Resolve(ctx, org, ref); err != nil {
		if err == errNotFound {
			return &plane.AgentDeclared{}, nil
		}
		return nil, fmt.Errorf("agents: resolve: %w", err)
	}
	return &plane.AgentDeclared{Declared: true}, nil
}

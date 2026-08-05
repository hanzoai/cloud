package projects

import (
	"context"

	"github.com/zap-proto/zip"

	cloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// The identity trust boundary asks projects who owns a claimed project.
//
// Same seam and same reason as key_rpc.go and sites_rpc.go, and of the three this
// is the one that was load-bearing for SECURITY. cloud.SanitizeIdentity refuses an
// X-Project-Id registered to a DIFFERENT org than the caller's validated org, and
// it answered that question from a package-level registry this app fills at Mount.
// The boundary is edge middleware in EVERY process; this store is in exactly one.
// So the registry was empty wherever the guard ran, "no registry owns it" was read
// as "not foreign", and the cross-org project impersonation check was off across
// the whole fleet.
//
// In-process when the boundary and this store are co-resident, over the plane when
// they are not — but now the question is always actually ASKED.
func exposeOwnership() {
	zip.Post[plane.OwnerIn, plane.Ownership](cloud.Plane(), "/projects/ownership", planeOwnership,
		zip.WithOperationID(plane.ProjectsOwnership),
		zip.WithSummary("Report whether the calling org owns a project, and whether another org does"))
}

// planeOwnership answers whether the CALLER's org owns the named project and
// whether some other org does.
//
// The org is the caller's plane identity and never the argument — plane.OwnerIn
// has no org field, deliberately, because the org is precisely what the answer is
// relative to: a caller able to state it could ask the question about somebody
// else and act on the answer. An anonymous caller is refused rather than defaulted.
//
// Both false is a REAL answer: nobody has registered this identifier, so it is a
// free-form within-org label and the boundary keeps it. That third outcome is why
// the reply carries two booleans instead of one verdict — collapsing "nobody owns
// it" into either "mine" or "another's" would respectively open the guard or break
// every subsystem that uses a free-form project label.
func planeOwnership(ctx context.Context, in *plane.OwnerIn) (*plane.Ownership, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("projects ownership: org required")
	}
	r, err := currentScopeResolver()
	if err != nil {
		return nil, err
	}
	mine, other, err := r.ProjectOwnership(ctx, who.Org, in.IDOrSlug)
	if err != nil {
		return nil, err
	}
	return &plane.Ownership{Mine: mine, Other: other}, nil
}

// The resolver this process serves plane answers from. Set at Mount beside
// cloud.RegisterOrgScopeResolver, so the in-process and plane legs can never name
// different stores.
var planeScopeResolver projectScopeResolver

func setScopeResolverForPlane(r projectScopeResolver) { planeScopeResolver = r }

// currentScopeResolver REFUSES rather than answering "nobody owns it" when the
// store is absent.
//
// This is the whole lesson of the defect, in one function. A process that has not
// mounted projects cannot know who owns a project, and the honest shape of that is
// an error. Answering "not owned by another org" would be a zero value standing in
// for an unasked question — and the boundary would read it as permission and
// forward a cross-org claim, which is exactly what shipped.
func currentScopeResolver() (projectScopeResolver, error) {
	if planeScopeResolver.store == nil {
		return projectScopeResolver{}, zip.ErrInternal("projects: this process does not own the project store")
	}
	return planeScopeResolver, nil
}

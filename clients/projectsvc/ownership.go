package projectsvc

import (
	"context"

	"github.com/hanzoai/cloud"
)

// ProjectOwnership reports whether a project addressed by id-or-slug is owned by
// org (mine) and/or by some OTHER org (other) — the cross-org impersonation
// signal the identity trust boundary (cloud.SanitizeIdentity) uses to refuse a
// forged X-Project-Id. Matched by BOTH slug and id so the check holds whichever
// addressing the caller used, in one indexed round trip. Tenant isolation is the
// org column, exactly as everywhere else in this store.
func (s *Store) ProjectOwnership(ctx context.Context, org, idOrSlug string) (mine, other bool, err error) {
	var m, o int
	row := s.db.QueryRowContext(ctx,
		`SELECT
		   EXISTS(SELECT 1 FROM projects WHERE org =  ? AND (slug = ? OR id = ?)),
		   EXISTS(SELECT 1 FROM projects WHERE org <> ? AND (slug = ? OR id = ?))`,
		org, idOrSlug, idOrSlug, org, idOrSlug, idOrSlug)
	if err := row.Scan(&m, &o); err != nil {
		return false, false, err
	}
	return m == 1, o == 1, nil
}

// projectScopeResolver adapts the projects Store to cloud.TenantScopeResolver so
// the identity trust boundary can refuse a cross-org X-Project-Id WITHOUT cloud
// importing this package (projectsvc imports cloud). Registered once at Mount,
// exactly like sites.SetResolver.
type projectScopeResolver struct{ store *Store }

// ensure the adapter satisfies the boundary's contract at compile time.
var _ cloud.TenantScopeResolver = projectScopeResolver{}

func (r projectScopeResolver) ProjectOwnership(ctx context.Context, org, idOrSlug string) (bool, bool, error) {
	return r.store.ProjectOwnership(ctx, org, idOrSlug)
}

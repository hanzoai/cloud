package admin

// The TENANT-SCOPE predicate — the ONE rule the whole cockpit obeys so admin.hanzo.ai
// is a single pane for BOTH tiers off ONE identity primitive:
//
//   owner == the admin org  (SuperAdmin, c.IsAdmin())  ⇒ CROSS-TENANT: every org.
//   any other validated admin caller                   ⇒ OWN SUBTREE: their org
//                                                         (+ the sub-orgs they own).
//
// This is decomplected into exactly one place (resolveScope + scopedOrgs + descendants)
// so no handler re-derives it and the escalation line — a non-super caller reaching
// ANOTHER tenant — cannot be crossed by any single panel. c.IsAdmin() is the SANITIZED
// X-User-IsAdmin, which SanitizeIdentity (middleware_identity.go) sets true ONLY for a
// validated principal whose org IS the admin org; a non-super caller's org is pinned by
// the same boundary to their own owner (never a client-chosen value), so the subtree is
// derived from un-forgeable identity, not request input.
//
// RECURSION SEAM (honest gap). The subtree is TODAY the singleton {org}: IAM's
// Organization (hanzoai/iam object/organization.go) has NO parent-org / hierarchy field
// (Owner/Name compose the PK; Owner is the PLATFORM owner, not a tenant parent), so no
// tenant subtree exists to walk yet. `descendants` is the ONE function that becomes a
// parent-index BFS once IAM adds the ParentOrg link — every scoped read composes over it,
// so recursion lands there and nowhere else, with zero change to the callers. Until then
// the singleton is correct, not a placeholder.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// tenantScope is a request's resolved visibility window. `super` and `orgs` are the two
// mutually exclusive views: a SuperAdmin sees all tenants (orgs ignored); anyone else
// sees exactly `orgs` (their own subtree).
type tenantScope struct {
	super bool
	orgs  []string
}

// scopedToOrg reports whether the scope admits reads for org o. Super admits every org;
// a scoped caller admits only orgs in their subtree. Used by panels that filter an
// upstream list (e.g. bases) rather than fanning out per-org.
func (t tenantScope) scopedToOrg(o string) bool {
	if t.super {
		return true
	}
	o = strings.TrimSpace(o)
	for _, s := range t.orgs {
		if s == o {
			return true
		}
	}
	return false
}

// resolveScope derives the request's tenant window from the SANITIZED identity only —
// never a client-forgeable field. A SuperAdmin (c.IsAdmin(), owner == admin org) is
// cross-tenant; any other caller is pinned to the subtree of their own (sanitized) org.
func resolveScope(s *cloud.Service[state], c *zip.Ctx) tenantScope {
	if c.IsAdmin() {
		return tenantScope{super: true}
	}
	org := strings.TrimSpace(c.Org())
	if org == "" {
		return tenantScope{} // no validated org ⇒ empty window ⇒ sees nothing
	}
	return tenantScope{orgs: descendants(s, org)}
}

// descendants returns org + every sub-org it owns — the subtree the caller administers.
// See the RECURSION SEAM note above: today the singleton {org}; the ONE place a future
// IAM parent-org index is walked.
func descendants(s *cloud.Service[state], org string) []string {
	org = strings.TrimSpace(org)
	if org == "" {
		return nil
	}
	return []string{org}
}

// scopedOrgs is the ONE fan-in the org-scoped read panels (overview, orgs, usage,
// analytics) fold over — enforcing the two-scope predicate in a single place. A
// SuperAdmin gets EVERY org (the cross-tenant list, IAM-authorized as the admin org);
// any other caller gets ONLY their own subtree, each row read from IAM so the display
// name / createdTime are the REAL values (analytics' signup cohort needs a real
// createdTime). An org row that can't be read best-effort degrades to a name-only row
// (Name = the sanitized org) rather than failing the panel — the scope is unaffected.
func scopedOrgs(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, cr creds) ([]iamOrg, error) {
	sc := resolveScope(s, c)
	if sc.super {
		return listOrgs(s, ctx, cr)
	}
	rows := make([]iamOrg, 0, len(sc.orgs))
	for _, name := range sc.orgs {
		row := iamOrg{Owner: s.State.adminOrg, Name: name, DisplayName: name}
		if full, err := s.State.iam.getOrg(ctx, cr, s.State.adminOrg+"/"+name); err == nil && full.Name != "" {
			row = full
		}
		rows = append(rows, row)
	}
	return rows, nil
}

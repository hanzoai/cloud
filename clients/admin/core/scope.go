package core

// The TENANT-SCOPE predicate — the ONE rule the whole cockpit obeys so admin.hanzo.ai
// is a single pane for BOTH tiers off ONE identity primitive:
//
//	owner == the admin org  (SuperAdmin, c.IsAdmin())  ⇒ CROSS-TENANT: every org.
//	any other validated admin caller                   ⇒ OWN SUBTREE: their org
//	                                                      (+ the sub-orgs they own).
//
// Decomplected into exactly one place (ResolveScope + ScopedOrgs + Descendants) so no
// handler re-derives it and the escalation line — a non-super caller reaching ANOTHER
// tenant — cannot be crossed by any single panel.
//
// RECURSION SEAM (honest gap). The subtree is TODAY the singleton {org}: IAM's
// Organization has NO parent-org / hierarchy field yet, so no tenant subtree exists to
// walk. `Descendants` is the ONE function that becomes a parent-index BFS once IAM adds
// the ParentOrg link — every scoped read composes over it, so recursion lands there and
// nowhere else, with zero change to the callers.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// TenantScope is a request's resolved visibility window. Super and Orgs are the two
// mutually exclusive views: a SuperAdmin sees all tenants (Orgs ignored); anyone else
// sees exactly Orgs (their own subtree).
type TenantScope struct {
	Super bool
	Orgs  []string
}

// ScopedToOrg reports whether the scope admits reads for org o. Super admits every org;
// a scoped caller admits only orgs in their subtree. Used by panels that filter an
// upstream list (e.g. bases) rather than fanning out per-org.
func (t TenantScope) ScopedToOrg(o string) bool {
	if t.Super {
		return true
	}
	o = strings.TrimSpace(o)
	for _, s := range t.Orgs {
		if s == o {
			return true
		}
	}
	return false
}

// ResolveScope derives the request's tenant window from the SANITIZED identity only —
// never a client-forgeable field. A SuperAdmin (c.IsAdmin(), owner == admin org) is
// cross-tenant; any other caller is pinned to the subtree of their own (sanitized) org.
func ResolveScope(s *cloud.Service[State], c *zip.Ctx) TenantScope {
	if c.IsAdmin() {
		return TenantScope{Super: true}
	}
	org, ok := principal.Org(c)
	if !ok {
		return TenantScope{} // no validated principal/org ⇒ empty window ⇒ sees nothing
	}
	return TenantScope{Orgs: Descendants(s, org)}
}

// Descendants returns org + every sub-org it owns — the subtree the caller administers.
// See the RECURSION SEAM note above: today the singleton {org}; the ONE place a future
// IAM parent-org index is walked.
func Descendants(s *cloud.Service[State], org string) []string {
	org = strings.TrimSpace(org)
	if org == "" {
		return nil
	}
	return []string{org}
}

// ScopedOrgs is the ONE fan-in the org-scoped read panels (overview, orgs, usage,
// analytics) fold over — enforcing the two-scope predicate in a single place. A
// SuperAdmin gets EVERY org (the cross-tenant list); any other caller gets ONLY their
// own subtree, each row read from IAM so the display name / createdTime are the REAL
// values. An org row that can't be read best-effort degrades to a name-only row rather
// than failing the panel — the scope is unaffected.
func ScopedOrgs(s *cloud.Service[State], ctx context.Context, c *zip.Ctx, cr iam.Creds) ([]iam.Org, error) {
	sc := ResolveScope(s, c)
	if sc.Super {
		return ListOrgs(s, ctx, cr)
	}
	rows := make([]iam.Org, 0, len(sc.Orgs))
	for _, name := range sc.Orgs {
		row := iam.Org{Owner: s.State.AdminOrg, Name: name, DisplayName: name}
		if full, err := s.State.IAM.Org(ctx, cr, s.State.AdminOrg+"/"+name); err == nil && full.Name != "" {
			row = full
		}
		rows = append(rows, row)
	}
	return rows, nil
}

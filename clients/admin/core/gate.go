package core

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Handler is the admin handler shape: a free function over the shared kernel. Every
// domain handler is `func h(s *cloud.Service[core.State], c *zip.Ctx) error`.
type Handler = func(*cloud.Service[State], *zip.Ctx) error

// Guard wraps a handler with the SuperAdmin gate. Fail-closed: any request whose
// validated identity is not a SuperAdmin (X-User-IsAdmin != "true", which
// SanitizeIdentity sets only for owner == AdminOrg) is refused 403 before the handler
// — no upstream is touched, no data leaks.
func Guard(s *cloud.Service[State], h Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("SuperAdmin required")
		}
		return h(s, c)
	}
}

// GuardScoped is the gate for the ORG-SCOPED panels (me/overview/orgs/users/usage/
// analytics/bases + spend-caps). It admits a SuperAdmin (principal.IsSuperAdmin) OR an
// admin of an ENABLED WHITE-LABEL TENANT org (principal.IsOrgAdmin pinned to its
// validated org AND that org ∈ State.WLTenants), and the handler then scopes every read
// to ResolveScope(c) — so a non-super caller passes the gate but the DATA layer
// hard-limits them to their own org subtree. Cross-tenant reads are impossible for a
// non-super caller regardless of input.
//
// THREE admission facts, ALL required for the non-super tier — the escalation line:
//   - X-User-IsOrgAdmin (principal.IsOrgAdmin — "admin of my own org", unforgeable
//     because SanitizeIdentity strips it on ingress and re-mints only from a validated
//     isAdmin claim), AND
//   - a validated principal pinned to its own org (principal.Org: validated X-User-Id +
//     non-empty in-bounds X-Org-Id — the boundary PINS a non-super caller's X-Org-Id to
//     their own owner, never client-chosen), AND
//   - that org is an ENABLED WL tenant (State.IsWhiteLabelTenant — the fail-closed
//     allowlist). This is the tier the old gate was MISSING: it admitted ANY org-admin,
//     so any customer's own-org admin could open the cockpit. Now an org-admin whose org
//     is not an enabled WL tenant is REFUSED — the SAME 403 a non-admin member or a
//     forged-X-Org-Id anonymous caller gets. A SuperAdmin never consults the allowlist.
//
// Fail-closed everywhere: nil/empty WLTenants ⇒ ONLY SuperAdmins reach these panels.
func GuardScoped(s *cloud.Service[State], h Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if principal.IsSuperAdmin(c) {
			return h(s, c) // SuperAdmin: cross-tenant, admitted regardless of org pin.
		}
		if org, ok := principal.Org(c); ok && principal.IsOrgAdmin(c) && s.State.IsWhiteLabelTenant(org) {
			return h(s, c)
		}
		return zip.ErrForbidden("admin required")
	}
}

// CallerCreds captures the caller's replayed authorization context for the IAM
// fan-out: the raw Cookie header (session model) and the Authorization bearer.
func CallerCreds(c *zip.Ctx) iam.Creds {
	return iam.Creds{
		Cookie: string(c.Fiber().Request().Header.Peek("Cookie")),
		Auth:   c.Header("Authorization"),
	}
}

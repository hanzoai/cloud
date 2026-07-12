package core

import (
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Handler is the admin handler shape: a free function over the shared kernel. Every
// domain handler is `func h(s *cloud.Service[core.State], c *zip.Ctx) error`.
type Handler = func(*cloud.Service[State], *zip.Ctx) error

// Guard wraps a handler with the global-admin gate. Fail-closed: any request whose
// validated identity is not a global admin (X-User-IsAdmin != "true", which
// SanitizeIdentity sets only for owner == AdminOrg) is refused 403 before the handler
// — no upstream is touched, no data leaks.
func Guard(s *cloud.Service[State], h Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("global admin required")
		}
		return h(s, c)
	}
}

// GuardScoped is the gate for the ORG-SCOPED panels (me/overview/orgs/users/usage/
// analytics/bases). It admits a SuperAdmin (c.IsAdmin()) OR an ORG admin (an admin of
// their OWN org — principal.OrgAdmin(c)) pinned to a validated org, and the handler then
// scopes every read to ResolveScope(c) — so a non-super caller passes the gate but the
// DATA layer hard-limits them to their own org subtree. Cross-tenant reads are impossible
// for a non-super caller regardless of input.
//
// The non-super admission requires BOTH the sanitizer-minted X-User-IsOrgAdmin
// (principal.OrgAdmin — the "admin of my own org" bit, unforgeable because the boundary
// strips it on ingress) AND c.User() (X-User-Id) + c.Org() non-empty (a validated
// principal pinned to their own owner). So a validated but NON-admin MEMBER of an org is
// REFUSED here — the same denial an anonymous caller who forged X-Org-Id gets — closing
// the same-tenant over-visibility gap where any org member could read their org's admin
// panels. A validated non-super principal's X-Org-Id is PINNED by the boundary to their
// own owner, never client-chosen.
func GuardScoped(s *cloud.Service[State], h Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if c.IsAdmin() {
			return h(s, c) // SuperAdmin: cross-tenant, admitted regardless of org pin.
		}
		if principal.OrgAdmin(c) && strings.TrimSpace(c.User()) != "" && strings.TrimSpace(c.Org()) != "" {
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

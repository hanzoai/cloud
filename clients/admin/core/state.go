// Package core is the shared kernel of the admin subsystem: the resolved upstream
// clients (State) plus the one-copy business primitives every admin domain composes
// — the two-tier gate, the /v1 envelope writers, the tenant-scope predicate, the IAM
// fan-in, the single credit-grant path, the tamper-evident audit emit, and the fleet
// activity/time-series model. Each primitive lives EXACTLY once here; the domain
// packages (audit/customer/revenue/finance) and the top-level admin Mount import it,
// never duplicating a helper — there is one path to grant, one read, one scope rule.
package core

import (
	"strings"

	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/commerce"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/hanzoai/cloud/clients/admin/health"
	"github.com/hanzoai/cloud/clients/admin/iam"
)

// State is admin's own data: the resolved upstream clients + the admin org for this
// deployment. admin holds NO Base shared deps (it fans out over HTTP replaying the
// caller's own creds); the embedded cloud.Base carries only the mount-time logger.
//
// AuditStore is cloud's OWN tamper-evident audit store (nil when unconfigured, in
// which case /v1/admin/audit falls back to the IAM get-records proxy). Serve builds
// it and hands it over via deps.Audit.
type State struct {
	IAM        *iam.Client
	Commerce   *commerce.Client
	Health     *health.Client
	DO         *digitalocean.Client
	AdminOrg   string
	AuditStore *audit.Recorder

	// WLTenants is the fail-closed allowlist of enabled WHITE-LABEL TENANT orgs — the
	// resellers/brands whose OWN-org admins may reach the org-scoped cockpit panels
	// (GuardScoped) at admin.<brand>. It is the second admission tier next to the admin
	// org: a SuperAdmin (owner == AdminOrg) is cross-tenant and never consults this set;
	// EVERY other caller must be an admin of an org that is IN this set, and is then
	// hard-scoped to that org's subtree. Seeded ONCE from ADMIN_WL_TENANT_ORGS at Mount
	// and otherwise immutable, so the decision is a deliberate, git-/KMS-auditable
	// onboarding — never self-service. EMPTY by default: with no entry, NO customer
	// org-admin is admitted (only SuperAdmins reach the cockpit), so a mis-set/absent
	// env fails CLOSED, never open.
	WLTenants map[string]bool
}

// IsWhiteLabelTenant reports whether org is an enabled white-label tenant — the ONE
// place the WL admission decision is read. Nil/empty set ⇒ always false (fail-closed):
// no org is a WL tenant until explicitly enabled. The org is matched VERBATIM (only
// trimmed), the same owner key principal.Org yields, so folding can never collapse a
// distinct owner into an enabled one.
func (st State) IsWhiteLabelTenant(org string) bool {
	if len(st.WLTenants) == 0 {
		return false
	}
	return st.WLTenants[strings.TrimSpace(org)]
}

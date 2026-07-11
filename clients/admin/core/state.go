// Package core is the shared kernel of the admin subsystem: the resolved upstream
// clients (State) plus the one-copy business primitives every admin domain composes
// — the two-tier gate, the /v1 envelope writers, the tenant-scope predicate, the IAM
// fan-in, the single credit-grant path, the tamper-evident audit emit, and the fleet
// activity/time-series model. Each primitive lives EXACTLY once here; the domain
// packages (audit/customer/revenue/finance) and the top-level admin Mount import it,
// never duplicating a helper — there is one path to grant, one read, one scope rule.
package core

import (
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
}

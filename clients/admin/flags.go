package admin

// The PLATFORM CONTROL PLANE board (/v1/admin/flags) — every runtime LAUNCH / RELEASE
// switch (waitlist, public signup, subsystem activation, gateway limits, network ids)
// with its LIVE value, evaluated through the Hanzo Insights feature-flag engine
// (clients/featureflags → insights rust/feature-flags). Global-admin only (mounted
// behind s.guard, like every /v1/admin/* route).
//
// ONE flag engine, not two. Insights OWNS the flag definitions, targeting, percentage
// rollout, and the change/activity log. This endpoint READS the switches for the
// cockpit and hands the operator the deep-links to the Insights flag MANAGER (where a
// switch is toggled / rolled out / cohort-targeted) and its ACTIVITY LOG (the native
// change audit). A flip there is hot — the consuming subsystems re-read within one
// evaluation TTL, no redeploy. The board is read-only here on purpose: management is
// the native Insights UI (the one-and-one-way flag surface), surfaced in the cockpit.

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/featureflags"
	"github.com/zap-proto/zip"
)

// flags answers GET /v1/admin/flags — the platform control-plane read board.
func flags(s *cloud.Service[core.State], c *zip.Ctx) error {
	return core.OK(c, featureflags.Board())
}

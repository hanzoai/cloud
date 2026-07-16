package admin

// The PLATFORM CONTROL PLANE board (/v1/admin/flags) — every runtime LAUNCH / RELEASE
// switch (waitlist, public signup, subsystem activation, gateway limits, network ids)
// with its LIVE value, evaluated through the embedded native flag engine
// (clients/featureflags → native/flags, SQLite-per-project definitions + Rust FFI
// evaluation). SuperAdmin only (mounted behind core.Guard, like every /v1/admin/*).
//
// ONE flag engine, TWO verbs. GET reads the board; PUT writes a switch's definition
// through featureflags.SetPlatformSwitch — the ONE write path, audited in the store's
// activity log. A flip is hot: this pod applies immediately, peers converge within one
// evaluation TTL (default 15s), no redeploy. Org/project product flags are managed on
// /v1/flags (org-scoped); this surface is the platform's own switchboard.

import (
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/featureflags"
	"github.com/zap-proto/zip"
)

// flags answers GET /v1/admin/flags — the platform control-plane read board.
func flags(s *cloud.Service[core.State], c *zip.Ctx) error {
	return core.OK(c, featureflags.Board())
}

// setFlag answers PUT /v1/admin/flags/:key — store/overwrite one platform switch's
// definition. The body is the flag definition JSON; the two common shapes:
//
//	{"active": true}                          — boolean switch on/off
//	{"active": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}],
//	                             "payloads": {"true": 250}}} — valued switch (int/string payload)
func setFlag(s *cloud.Service[core.State], c *zip.Ctx) error {
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		return zip.ErrBadRequest("key is required")
	}
	body := c.Body()
	if len(body) == 0 || !json.Valid(body) {
		return zip.ErrBadRequest("body must be the flag definition JSON")
	}
	if err := featureflags.SetPlatformSwitch(key, json.RawMessage(body), c.UserEmail()); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	return core.OK(c, featureflags.Board())
}

package cloud

import (
	"strings"

	"github.com/hanzoai/cloud/internal/hareplica"
	"github.com/zap-proto/zip"
)

// haWriteForward returns a zip middleware that forwards MUTATING /v1 requests to
// the primary-only Service when this pod is a standby. It sits BEFORE
// IdentityMiddleware so the PRIMARY performs identity/audit/billing exactly once
// (the standby is a pure transport). Reads are served locally. No-op when HA is
// disabled or this pod holds the primary lease.
//
// Loop guard: a forwarded write that still arrives at a non-primary means the
// role label is stale / there is momentarily no primary — it fails closed (503)
// rather than re-forwarding, so a write can never ping-pong between pods.
func haWriteForward(m *hareplica.Manager) zip.Handler {
	return func(c *zip.Ctx) error {
		if m == nil || !m.Enabled() || m.IsPrimary() {
			return c.Next()
		}
		if hareplica.IsReadMethod(c.Method()) || !strings.HasPrefix(c.Path(), "/v1/") {
			return c.Next()
		}
		fc := c.Fiber()
		if len(fc.Request().Header.Peek(hareplica.ForwardedHeader)) > 0 {
			return c.JSON(503, map[string]string{"error": "no primary available"})
		}
		res := m.ForwardWrite(c.Context(), c.Method(), fc.OriginalURL(), c.Body(),
			func(add func(k, v string)) {
				fc.Request().Header.VisitAll(func(k, v []byte) { add(string(k), string(v)) })
			})
		fc.Status(res.Status)
		for k, vv := range res.Header {
			for _, v := range vv {
				fc.Set(k, v)
			}
		}
		return fc.Send(res.Body)
	}
}

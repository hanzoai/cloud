// SPDX-License-Identifier: Apache-2.0

package cloud

// Resource metering seam (OSS core).
//
// In the private Hanzo Cloud build, ResourceMeter is the per-org gate+meter every
// non-LLM "create a resource" handler uses so provisioned infrastructure is paid
// for. The OSS core ships the SAME API shape — so subsystem handlers compile and
// read identically — but as a NO-OP: Gate always allows, Meter does nothing,
// Enabled() is false. Billing is a private-SaaS concern; the OSS binary provisions
// resources without a spend gate. An operator who wants billing runs the private
// build, which replaces this meter with the real commerce-backed one.
//
// The pure helpers (ResourceFeeCents, ClientIP, DenyResource) are kept intact —
// they carry no billing dependency and are used across the request pipeline.

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// DefaultResourceFeeCents is the fallback flat provision/create fee (in cents)
// when no operator override is set. In the OSS core the meter is a no-op so this
// is only the value ResourceFeeCents returns when no env knob is set.
const DefaultResourceFeeCents int64 = 100

// ResourceMeter is the OSS no-op per-org gate+meter. It preserves the API the
// private build's real meter exposes so subsystem handlers are identical across
// builds. Build it with NewResourceMeter; Gate allows, Meter is a no-op,
// Enabled() is false.
type ResourceMeter struct {
	provider string // attribution label (kept for API parity / logs).
	env      string
	log      luxlog.Logger
}

// NewResourceMeter builds a no-op ResourceMeter from the shared deps.
func NewResourceMeter(deps Deps, provider string) *ResourceMeter {
	return &ResourceMeter{provider: provider, env: deps.Env, log: deps.Logger}
}

// Enabled reports whether billing will enforce. Always false in the OSS core.
func (rm *ResourceMeter) Enabled() bool { return false }

// Gate is the pre-create balance gate. In the OSS core it always allows.
func (rm *ResourceMeter) Gate(ctx context.Context, org, project string, projectValidated bool, kind string, costCents int64) error {
	return nil
}

// Meter records a successful charge. No-op in the OSS core.
func (rm *ResourceMeter) Meter(org, project, kind string, amountCents int64, requestID, clientIP string) {
}

// DenyResource renders a Gate denial as the canonical billing error contract.
// The OSS Gate never denies, so this is unreachable on the OSS billing path; it
// is kept so a subsystem that calls it (on a real build's denial) still compiles
// and emits the one error shape.
func DenyResource(c *zip.Ctx, err error) error {
	return c.JSON(http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"code":    "balance_unavailable",
			"message": "Billing temporarily unavailable",
		},
	})
}

// ResourceFeeCents resolves the flat create fee (in cents) for kind from
// operator config, most specific first:
//
//	<envPrefix>_<KIND>   e.g. CLOUD_PROVISION_FEE_CENTS_SQL=500
//	<envPrefix>          e.g. CLOUD_PROVISION_FEE_CENTS=100  (all kinds)
//	DefaultResourceFeeCents
//
// A clearly-named, configurable policy knob. A value of 0 makes the kind free.
// Negative/invalid values are ignored (fall through).
func ResourceFeeCents(envPrefix, kind string) int64 {
	if v, ok := parseNonNegCents(os.Getenv(envPrefix + "_" + strings.ToUpper(kind))); ok {
		return v
	}
	if v, ok := parseNonNegCents(os.Getenv(envPrefix)); ok {
		return v
	}
	return DefaultResourceFeeCents
}

func parseNonNegCents(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ClientIP extracts the originating client IP from X-Forwarded-For (the gateway
// sets it); the left-most entry is the real client.
func ClientIP(c *zip.Ctx) string {
	xff := c.Header("X-Forwarded-For")
	if xff == "" {
		return ""
	}
	if i := strings.IndexByte(xff, ','); i > 0 {
		return strings.TrimSpace(xff[:i])
	}
	return strings.TrimSpace(xff)
}

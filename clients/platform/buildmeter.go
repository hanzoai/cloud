package platform

// Usage-native metering for git builds. A git deploy launches an in-cluster
// BuildKit Job (deploy.go → launchBuildJob) that consumes real cluster compute
// for however long the build runs; the reconciler (reconcile.go) owns its
// completion. This is the ONE place that prices that compute: wall-clock BUILD
// MINUTES, from the build record's creation (the Job launches immediately after)
// to the moment the Job is observed finished. Metered on the CALLER's org ledger
// via the shared cloud.ResourceMeter, exactly once per completed build.

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/commerce/metering"
)

// buildMinuteFeeEnvPrefix is the operator knob for the per-build-minute rate, in
// cents, resolved via cloud.ResourceFeeCents (global CLOUD_BUILD_MINUTE_CENTS,
// else the $1.00/min policy default). Set it to 0 to make builds free (no meter).
const buildMinuteFeeEnvPrefix = "CLOUD_BUILD_MINUTE_CENTS"

// buildMinutesCents prices a build's wall-clock span into an integer-cent debit:
//
//	minutes = (endUnix − startUnix) / 60
//	cents   = round(minutes × feePerMinuteCents)
//
// Integer-exact (no float): cents = round(seconds × fee / 60). A non-positive
// span (clock skew, missing timestamp) or a non-positive fee yields 0, which
// makes MeterUsage a no-op. Sub-minute builds round to the nearest cent (half up).
func buildMinutesCents(startUnix, endUnix, feePerMinuteCents int64) int64 {
	secs := endUnix - startUnix
	if secs <= 0 || feePerMinuteCents <= 0 {
		return 0
	}
	return (secs*feePerMinuteCents + 30) / 60
}

// meterBuild debits b's org for the build's wall-clock minutes, once the build
// has reached a terminal state where its Job actually ran to completion (endUnix
// = the observed finish). Fire-and-forget on the shared meter (a nil/unconfigured
// meter is a no-op), so it never blocks reconciliation. b.CreatedAt is the build
// record's birth — the Job launches immediately after — so the span is honest
// wall-clock compute, never the reconcile poll interval or the failure deadline.
func (s *svc) meterBuild(b Build, endUnix int64) {
	cents := buildMinutesCents(b.CreatedAt, endUnix, cloud.ResourceFeeCents(buildMinuteFeeEnvPrefix, "build"))
	if cents <= 0 {
		return
	}
	s.bill.MeterUsage(b.Org, "build", metering.Usage{
		Model:       "build", // the billed unit: wall-clock build minutes.
		AmountCents: cents,
	})
}

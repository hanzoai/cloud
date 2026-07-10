package functions

// Usage-native compute metering for serverless invocations.
//
// A flat per-invocation request fee (invokeFeeEnvPrefix) already gates + bills
// the ACT of invoking. This file adds the second half of the standard
// serverless billing model — the COMPUTE fee — priced on the resource actually
// consumed: GB-seconds = (memory in GB) × (wall-clock seconds). It is the same
// unit AWS Lambda / GCP Functions bill on, so the number is meaningful, not
// fabricated. Both debits land on the CALLER's org ledger via the ONE shared
// cloud.ResourceMeter; either is independently free (fee 0 → no-op).

import (
	"strconv"
	"strings"
)

// gbSecFeeEnvPrefix is the operator knob for the per-GB-second compute rate, in
// cents, resolved via cloud.ResourceFeeCents (global CLOUD_FUNCTION_GBSEC_CENTS,
// else the $1.00/GB-s policy default). Set it to 0 to disable the compute meter
// and bill invocations by the flat request fee alone.
const gbSecFeeEnvPrefix = "CLOUD_FUNCTION_GBSEC_CENTS"

// defaultMemMB is the assumed memory when a function's MemoryLimit is absent or
// unparseable — the same 256Mi the create handler defaults to, so billing never
// falls to 0 (free compute) nor spikes on a malformed quantity.
const defaultMemMB int64 = 256

// gbSecondsCents prices one invocation's compute into an integer-cent debit:
//
//	GB-seconds = (memMB / 1024) × (durationMs / 1000)
//	cents      = round(GB-seconds × feePerGBSecCents)
//
// GB is 1024 MB (the MiB convention Lambda uses). The math is integer-exact (no
// float drift): cents = round(durationMs × memMB × fee / 1_024_000). A
// non-positive duration, memory, or fee yields 0, which makes MeterUsage a
// no-op. Sub-cent charges round to the nearest cent (half up).
func gbSecondsCents(durationMs, memMB, feePerGBSecCents int64) int64 {
	if durationMs <= 0 || memMB <= 0 || feePerGBSecCents <= 0 {
		return 0
	}
	const denom = 1024 * 1000 // MB·ms per GB·s
	num := durationMs * memMB * feePerGBSecCents
	return (num + denom/2) / denom
}

// memLimitMB parses a Kubernetes-style memory quantity ("256Mi", "1Gi", "512M",
// "2G") into whole MB (GB=1024 MB) for GB-second billing. An empty, bare-number,
// or otherwise unrecognized value degrades to defaultMemMB (256) rather than 0 —
// so a weird quantity never zeroes the bill or produces a wild overcharge.
func memLimitMB(s string) int64 {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil || n <= 0 {
		return defaultMemMB
	}
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "mi", "m":
		return n
	case "gi", "g":
		return n * 1024
	default:
		return defaultMemMB
	}
}

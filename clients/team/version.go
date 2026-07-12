package team

// This file owns the Team platform MODEL version — the single number the front
// SPA handshakes against. Ported VERBATIM (as package-local functions) from
// github.com/hanzoai/team-go/pkg/model.
//
// It is DISTINCT from the cloud binary's own release identity: MODEL_VERSION is
// the Team model the front validates against — the transactor hello's
// serverVersion AND each workspace's versionMajor/Minor/Patch. Both places that
// answer the front's model-version handshake read from HERE, so serverVersion and
// the per-workspace version can never drift: one source, one number, parsed one
// way.

import (
	"os"
	"strconv"
	"strings"
)

// defaultModelVersion is the front SPA's current model version. Shipping it as
// the code default keeps the binary in sync with the front with no configuration
// — MODEL_VERSION exists only to fast-track a front bump ahead of a cloud
// release. It MUST stay 0.6.0 (the front's version-check compares against it).
const defaultModelVersion = "0.6.0"

// modelVersion is the MODEL version string (e.g. "0.6.0"), overridable via
// MODEL_VERSION.
func modelVersion() string {
	if v := os.Getenv("MODEL_VERSION"); v != "" {
		return v
	}
	return defaultModelVersion
}

// modelMajor, modelMinor and modelPatch are modelVersion parsed into its numeric
// components — the shape the workspace-info handshake needs. A missing or
// non-numeric component reads as 0, so a malformed override degrades to 0.0.0
// rather than panicking.
func modelMajor() int { return versionPart(0) }
func modelMinor() int { return versionPart(1) }
func modelPatch() int { return versionPart(2) }

func versionPart(i int) int {
	segs := strings.SplitN(modelVersion(), ".", 3)
	if i >= len(segs) {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(segs[i]))
	return n
}

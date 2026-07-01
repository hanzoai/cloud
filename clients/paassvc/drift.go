// Package paassvc mounts the native, in-process Hanzo PaaS control plane at
// /v1/paas/*: the "one and only one way to deploy" made native to the cloud
// binary. It is the Go port of the standalone Dokploy-based platform's
// build→deploy lifecycle (pkg/platform/src/services/ci/deploy-executor.ts +
// services/apps/inventory.ts + db/schema/apps-drift.ts), collapsed into an
// in-process cloud subsystem exactly like clients/ml is the k8s bridge for the
// Kubeflow CRDs.
//
// The deploy mechanism is the SAME one the operator already reconciles: a
// merge-patch of the operator `Service` CR's `.spec.image`. No second deployer
// is invented; the Hanzo operator owns the rollout. This module only observes
// the declared/running/latest tags per service (the drift board) and flips the
// one CR field a deploy changes — the identical contract the Node deploy-executor
// implemented, now native.
//
// drift.go is the PURE half: it derives the drift verdict for one observed
// service row and performs no IO. It is a faithful port of
// `pkg/platform/src/db/schema/apps-drift.ts` so the two implementations can never
// disagree about what "drift" means (one way to compute drift, period). The
// cluster reader (paas.go) owns observing the tags; this file only interprets
// them.
package paassvc

import "regexp"

// semverTagRE is the semver-only policy from the platform contract's constraint 1:
// every declared/running tag MUST be exactly `vMAJOR.MINOR.PATCH`. Anything else
// (`:main`, `:latest`, `:dev`, `:edge`, `sha-…`, `1.42.33-billing`, …) is a
// floating reference. Mirrors the `^v\d+\.\d+\.\d+$` gate the platform's
// reconciler enforces at the patch boundary (apps-drift.ts `SEMVER_TAG`).
var semverTagRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// IsSemverTag reports whether tag is a strict `vX.Y.Z` semver tag.
func IsSemverTag(tag string) bool { return tag != "" && semverTagRE.MatchString(tag) }

// DriftKind enumerates the kinds of drift from the platform contract
// (apps-drift.ts `DriftKind`). Each value is independent — one service row can
// carry several at once (e.g. a floating running tag with a zero-asset release).
type DriftKind string

const (
	// DriftStale — declared ≠ latest: a newer release exists but is not declared. (yellow)
	DriftStale DriftKind = "stale"
	// DriftUnrolled — running ≠ declared: the cluster has not rolled to the declared tag. (yellow)
	DriftUnrolled DriftKind = "un-rolled"
	// DriftFloatingDeclared — declaredTag is not strict semver; the reconciler would refuse it. (red)
	DriftFloatingDeclared DriftKind = "floating-declared"
	// DriftFloatingRunning — runningTag is not strict semver; policy violation on the cluster. (red)
	DriftFloatingRunning DriftKind = "floating-running"
	// DriftNoRelease — no GH Release found for the declared tag. (red)
	DriftNoRelease DriftKind = "no-release"
	// DriftZeroAssets — GH Release exists but shipped 0 assets. (red)
	DriftZeroAssets DriftKind = "zero-assets"
)

// DriftSeverity is the aggregate drift severity. "ok" = no flags; otherwise the
// max over flags.
type DriftSeverity string

const (
	SeverityOK     DriftSeverity = "ok"
	SeverityYellow DriftSeverity = "yellow"
	SeverityRed    DriftSeverity = "red"
)

// severityOf maps each kind to its severity. Stale/un-rolled are warnings; the
// rest are hard drift. Mirrors apps-drift.ts `SEVERITY`.
var severityOf = map[DriftKind]DriftSeverity{
	DriftStale:            SeverityYellow,
	DriftUnrolled:         SeverityYellow,
	DriftFloatingDeclared: SeverityRed,
	DriftFloatingRunning:  SeverityRed,
	DriftNoRelease:        SeverityRed,
	DriftZeroAssets:       SeverityRed,
}

// DriftFlag is a single drift finding: its kind, severity, and a human-readable
// reason (apps-drift.ts `DriftFlag`).
type DriftFlag struct {
	Kind     DriftKind     `json:"kind"`
	Severity DriftSeverity `json:"severity"`
	Message  string        `json:"message"`
}

// Drift is the drift verdict for one observed service row: the ordered flags plus
// the rolled-up severity (apps-drift.ts `Drift`).
type Drift struct {
	Severity DriftSeverity `json:"severity"`
	Flags    []DriftFlag   `json:"flags"`
}

// Observed is the minimal set of already-observed tag fields the drift derivation
// reads — mirrors the `Pick<App, …>` the TS `computeDrift` accepts. The reader
// (paas.go) fills these from the cluster; the release fields are populated by the
// GH-release reader (a follow-up), so today they are the honest zero value
// (ReleaseURL == "" ⇒ no-release, exactly like the un-populated TS columns).
type Observed struct {
	DeclaredTag   string // what SHOULD run — spec.image.tag on the operator Service CR
	RunningTag    string // what ACTUALLY runs — observed from the CR status / Deployment
	LatestTag     string // newest released tag (GH release reader; empty until wired)
	ReleaseURL    string // GH Release URL for DeclaredTag (empty ⇒ no-release)
	ReleaseAssets int    // asset count on the GH Release (0 ⇒ zero-assets)
}

func flag(kind DriftKind, message string) DriftFlag {
	return DriftFlag{Kind: kind, Severity: severityOf[kind], Message: message}
}

// ComputeDriftFlags derives the drift flags for one observed service row, exactly
// per the platform contract (apps-drift.ts `computeDriftFlags`).
//
// Detection rules (each independent; a row may trip several):
//
//   - floating-declared — DeclaredTag is set but not vX.Y.Z. The reconciler
//     refuses non-semver declarations, so this is hard drift. (When the
//     declaration itself is floating, comparing it against LatestTag for "stale"
//     is meaningless, so stale is suppressed in that case.)
//   - floating-running — RunningTag is set but not vX.Y.Z: the cluster is running
//     a floating image. Hard drift.
//   - stale — DeclaredTag and LatestTag are both known semver and differ: a newer
//     release exists that is not yet declared.
//   - un-rolled — DeclaredTag and RunningTag are both known and differ: the
//     declaration has not reached the cluster yet.
//   - no-release — a DeclaredTag exists but no GH Release was found (ReleaseURL "").
//   - zero-assets — a GH Release exists (ReleaseURL set) but ReleaseAssets == 0.
//
// Tags are compared verbatim (the reader stores reality un-normalized); no
// ordering is assumed beyond equality — matching the contract.
func ComputeDriftFlags(o Observed) []DriftFlag {
	var flags []DriftFlag

	declaredFloating := o.DeclaredTag != "" && !IsSemverTag(o.DeclaredTag)
	runningFloating := o.RunningTag != "" && !IsSemverTag(o.RunningTag)

	if declaredFloating {
		flags = append(flags, flag(DriftFloatingDeclared,
			"declared tag \""+o.DeclaredTag+"\" is not semver (vX.Y.Z); the reconciler will refuse it"))
	}
	if runningFloating {
		flags = append(flags, flag(DriftFloatingRunning,
			"running tag \""+o.RunningTag+"\" is a floating reference, not semver"))
	}

	// "stale" only makes sense for a semver declaration: declared ≠ latest.
	if !declaredFloating && o.DeclaredTag != "" && o.LatestTag != "" && o.DeclaredTag != o.LatestTag {
		flags = append(flags, flag(DriftStale,
			"declared "+o.DeclaredTag+" is behind latest "+o.LatestTag))
	}

	// "un-rolled": running ≠ declared (only once running is known and not already
	// flagged as floating, to avoid double-counting).
	if !runningFloating && o.DeclaredTag != "" && o.RunningTag != "" && o.RunningTag != o.DeclaredTag {
		flags = append(flags, flag(DriftUnrolled,
			"running "+o.RunningTag+" has not rolled to declared "+o.DeclaredTag))
	}

	// Release-artifact integrity is keyed off the declared tag.
	if o.DeclaredTag != "" {
		if o.ReleaseURL == "" {
			flags = append(flags, flag(DriftNoRelease,
				"no GH Release found for declared tag "+o.DeclaredTag))
		} else if o.ReleaseAssets == 0 {
			flags = append(flags, flag(DriftZeroAssets,
				"GH Release for "+o.DeclaredTag+" shipped 0 assets"))
		}
	}

	return flags
}

// DriftSeverityOf rolls a list of flags up to a single severity (red > yellow >
// ok). Mirrors apps-drift.ts `driftSeverity`.
func DriftSeverityOf(flags []DriftFlag) DriftSeverity {
	red, yellow := false, false
	for _, f := range flags {
		switch f.Severity {
		case SeverityRed:
			red = true
		case SeverityYellow:
			yellow = true
		}
	}
	if red {
		return SeverityRed
	}
	if yellow {
		return SeverityYellow
	}
	return SeverityOK
}

// ComputeDrift is the full drift verdict (flags + rolled-up severity) for one
// observed service row (apps-drift.ts `computeDrift`). Flags is always non-nil so
// the JSON encodes `[]`, never `null`.
func ComputeDrift(o Observed) Drift {
	flags := ComputeDriftFlags(o)
	if flags == nil {
		flags = []DriftFlag{}
	}
	return Drift{Severity: DriftSeverityOf(flags), Flags: flags}
}

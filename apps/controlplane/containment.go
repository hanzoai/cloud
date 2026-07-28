//go:build controlplane

package controlplane

import "testing"

// ProductionBCCSigningReady reports whether the REAL byzantine cert-composer
// crypto (RoundSigner core, CertComposer, PartialZVerifier — the stub-boundary
// table in doc.go) has replaced every increment-1 stub. It is hardcoded false
// today. There is no override: no env var, no config flag, no constructor
// argument flips it. It becomes true only when this function's body is edited
// in the same change that deletes the stub types it currently guards — i.e.
// increment-2 landing for real.
//
// mustHarnessOnly is the single fact this function's return value gates.
func ProductionBCCSigningReady() bool { return false }

// harnessSafe is the pure decision table mustHarnessOnly enforces: safe to
// construct iff the real crypto is ready, OR the calling binary is a go-test
// harness. Factored out so the logic itself is unit-testable without needing
// a subprocess for every case.
func harnessSafe(productionReady, isTestBinary bool) bool {
	return productionReady || isTestBinary
}

// mustHarnessOnly is the fail-closed choke point for every increment-1
// stub-crypto construction path in this package: the PartialZVerifier
// registration in signer.go's init, NewSigner, and NewStubComposer. It panics
// unless harnessSafe holds.
//
// testing.Testing() is set by the Go toolchain itself for any binary produced
// by `go test` — it is not a flag or env var this package reads, so nothing
// short of actually being the test harness satisfies it. A `go build`/`go
// run`/`go install` binary (exactly what a Dockerfile or release pipeline
// produces) is never a test binary, so if the `controlplane` build tag ever
// leaks into a real build (the CI grep guard's job to prevent — see
// .github/workflows/containment.yml), this still refuses to run the moment
// anything in this package is touched: import-time for the PartialZVerifier
// registration, construction-time for a Signer or the stub composer. Fail
// closed, not a silent degrade.
func mustHarnessOnly(who string) {
	if harnessSafe(ProductionBCCSigningReady(), testing.Testing()) {
		return
	}
	panic("controlplane: " + who + " refused — increment-1 crypto is stub/forgeable " +
		"(ProductionBCCSigningReady()==false) and this process is not a go-test harness; " +
		"see clients/controlplane/doc.go")
}

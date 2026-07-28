//go:build controlplane

package controlplane

import (
	"os/exec"
	"strings"
	"testing"
)

// TestHarnessSafe_TruthTable exhaustively locks the decision mustHarnessOnly
// enforces, independent of the runtime testing.Testing() plumbing: safe iff
// production crypto is ready OR the binary is a go-test harness. Today
// ProductionBCCSigningReady() is hardcoded false — that value is also locked
// below, since flipping it silently would be the single most dangerous
// one-line regression this package could suffer.
func TestHarnessSafe_TruthTable(t *testing.T) {
	cases := []struct {
		productionReady, isTestBinary, want bool
	}{
		{false, false, false}, // stub crypto, real (non-test) binary — MUST refuse
		{false, true, true},   // stub crypto, go-test harness — the N=7 suite
		{true, false, true},   // real crypto landed — safe on a real binary
		{true, true, true},    // real crypto landed, still safe under test
	}
	for _, c := range cases {
		if got := harnessSafe(c.productionReady, c.isTestBinary); got != c.want {
			t.Errorf("harnessSafe(%v, %v) = %v, want %v", c.productionReady, c.isTestBinary, got, c.want)
		}
	}
}

// TestProductionBCCSigningReady_LockedFalse locks today's value so any future
// PR that flips it is forced to touch this test — a deliberate speed bump, not
// an accident.
func TestProductionBCCSigningReady_LockedFalse(t *testing.T) {
	if ProductionBCCSigningReady() {
		t.Fatal("ProductionBCCSigningReady()==true: the increment-1 stub crypto must not be declared production-ready; " +
			"if increment-2 landed real crypto, update this test deliberately")
	}
}

// TestContainment_NonHarnessProcessRefuses is the end-to-end proof that
// mustHarnessOnly's guard is really wired to the Go toolchain's own
// testing.Testing() signal, not just correct in isolation (that part is
// TestHarnessSafe_TruthTable). It runs testdata/nonharness — a program that
// does nothing but blank-import this package — as a REAL `go run` subprocess.
// That subprocess is not a go-test binary, so testing.Testing() is false
// inside it, and clients/controlplane's package-level init() must panic
// before main() ever runs.
//
// This is the strongest form of this test possible in-process is exactly the
// one thing it CANNOT prove: testing.Testing() is unconditionally true inside
// go test itself, so only a subprocess can exercise the "false" branch for
// real.
func TestContainment_NonHarnessProcessRefuses(t *testing.T) {
	cmd := exec.Command("go", "run", "-tags", "controlplane", "./testdata/nonharness")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("nonharness process exited 0 — the containment guard did not fire; output:\n%s", out)
	}
	if !strings.Contains(string(out), "controlplane: package import (RegisterPartialZVerifier) refused") {
		t.Fatalf("nonharness process failed, but not with the containment panic — output:\n%s", out)
	}
	if !strings.Contains(string(out), "not a go-test harness") {
		t.Fatalf("containment panic message regressed; output:\n%s", out)
	}
}

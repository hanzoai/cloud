// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"os"
	"strings"
	"testing"
)

// `ai` runs as its OWN process (ps in a prod pod: /cloud, /kms, /tasks, /ai, …) and
// these hooks are package-level vars, so a reader wireFinance sets in the CLOUD
// process is invisible here — permanently, not just at mount time.
//
// The ai module's contract is that a NIL hook means "use my own path": the HTTP call
// to /v1/billing/balance, which cloud accepts with the S2S token. Installing a hook
// that merely reports the host has none therefore SHADOWS the only path that works in
// this process, and because the balance gate is fail-CLOSED that turns every
// completion into 503 balance_unavailable — chat, copilot and documents all dead, on
// a pod whose commerce subsystem is perfectly healthy. Verified in prod on
// v1.801.338: "balance_gate: balance unverifiable … no native balance reader
// installed in this process" while /ai (pid 83) and /cloud (pid 7) were separate.
//
// So: install a money hook ONLY when this process actually has one.
func TestMoneyHooksAreNotInstalledWhenThisProcessHasNone(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)

	for _, hook := range []string{"TierReader", "BalanceReader", "UsageRecorder"} {
		guard := "if f := cloud." + hook + "(); f != nil {"
		if !strings.Contains(body, guard) {
			t.Errorf("%s must be installed ONLY when this process has one (expected %q); "+
				"an unconditional install shadows the ai module's own fallback and "+
				"fail-closes every completion", hook, guard)
		}
	}
}

// The rolling cap IS a genuine trampoline and must stay one: clients/rollingcap
// installs it from Mount — after this package, in some binaries — and unlike the
// money hooks a missing cap is benign (uncapped), so resolving per call is safe.
func TestRollingCapStaysATrampoline(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "aiobject.SetRollingCapReader(func(") {
		t.Error("RollingCapReader must resolve cloud.RollingCapReader() per call")
	}
	if !strings.Contains(body, "cloud.RollingCapReader()") {
		t.Error("RollingCapReader trampoline must re-resolve the host hook inside the closure")
	}
}

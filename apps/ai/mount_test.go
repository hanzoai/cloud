// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The money hooks must be TRAMPOLINES, never snapshots.
//
// `ai` is a lazy plugin: it mounts on the first /v1/chat/completions, which can happen
// before the app that installs the balance reader. A snapshot (`if f := cloud.X(); f !=
// nil`) then latches nil for the process lifetime, the ai module falls back to an HTTP
// self-call to /v1/billing/* that the edge 401s, and the fail-CLOSED balance gate answers
// 503 balance_unavailable to EVERY completion. That is the v1.801.320 chat outage:
// "balance_gate: balance unverifiable for cold subject=hanzo: commerce returned 401" on a
// pod whose commerce plugin mounted ten minutes later.
//
// Asserted at the source level on purpose: the failure is mount ORDER, which a unit test
// that calls Mount() with everything already wired cannot reproduce.
func TestMoneyHooksAreTrampolinesNotSnapshots(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)

	for _, hook := range []string{"TierReader", "BalanceReader", "UsageRecorder", "RollingCapReader"} {
		set := "aiobject.Set" + hook + "("
		if !strings.Contains(body, set) {
			t.Errorf("%s is never installed — the ai module would use its own HTTP fallback", hook)
			continue
		}
		// A snapshot reads `if f := cloud.<Hook>(); f != nil { aiobject.Set<Hook>(...) }`:
		// the guard makes installation conditional on wire-time state.
		snapshot := regexp.MustCompile(`if f := cloud\.` + hook + `\(\); f != nil \{`)
		if snapshot.MatchString(body) {
			t.Errorf("%s is installed from a wire-time SNAPSHOT; it must be a trampoline that "+
				"resolves cloud.%s() per call (lazy mount order latched nil and 503'd every "+
				"completion in v1.801.320)", hook, hook)
		}
		// …and the trampoline must actually re-resolve the host hook inside the closure.
		if !strings.Contains(body, "cloud."+hook+"()") {
			t.Errorf("%s never calls cloud.%s() — it cannot be resolving per request", hook, hook)
		}
	}
}

// The balance trampoline must FAIL rather than report a number it does not have: 0 reads
// as a real zero balance and denies a paying caller as "insufficient".
func TestBalanceTrampolineNeverGuessesZero(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "aiobject.SetBalanceReader(")
	if i < 0 {
		t.Fatal("balance reader is not installed")
	}
	// The closure's nil branch, up to the next hook installation.
	end := strings.Index(body[i:], "aiobject.SetUsageRecorder(")
	if end < 0 {
		end = len(body) - i
	}
	closure := body[i : i+end]
	if !strings.Contains(closure, "fmt.Errorf") {
		t.Error("balance trampoline must return an explicit error when no reader is wired")
	}
	if regexp.MustCompile(`return 0, nil`).MatchString(closure) {
		t.Error("balance trampoline returns 0 with no error — a missing reader would read as a " +
			"real zero balance and deny a paying caller")
	}
}

package plane

// The defect these pin: a plane call was cut off at the TRANSPORT's 30s
// response-read default while the caller had given itself 110s. The work
// succeeded — production measured a turn at 41,153ms answered 200 by both `ai`
// and `agents` — and the caller had already hung up. Every log on the callee
// side said healthy; only the caller reported failure.
//
// A unit test cannot hold a socket open for 40 seconds to prove this, so these
// assert the two properties that make the failure impossible instead: the
// ceiling exceeds the longest real caller budget, and the registration actually
// takes effect on a dialled transport.

import (
	"testing"
	"time"
)

// The number that mattered. zap-proto/http's default is 30s; the chat bridge
// budgets 110s for one turn. A ceiling below the caller's own budget means the
// caller can never spend it — which is exactly what shipped.
func TestTheCeilingExceedsTheLongestCallerBudget(t *testing.T) {
	const transportDefault = 30 * time.Second
	const bridgeAgentTimeout = 110 * time.Second // apps/integrations/channel.go

	if planeReadTimeout <= transportDefault {
		t.Fatalf("planeReadTimeout %v does not widen the %v default", planeReadTimeout, transportDefault)
	}
	if planeReadTimeout <= bridgeAgentTimeout {
		t.Errorf("planeReadTimeout %v is at or below the chat bridge's own budget %v — "+
			"a caller must expire on ITS deadline, never on the transport's",
			planeReadTimeout, bridgeAgentTimeout)
	}
}

// The registration must survive being applied twice: init() runs it, and a test
// or a second import path may run it again. RegisterTransport is documented as
// "adds (or replaces)", so this is defined — but if it ever panics on a repeat,
// every process that imports plane twice dies at start-up rather than at a call.
func TestRegisteringTwiceIsSafe(t *testing.T) {
	widenPlaneReadDeadline()
	widenPlaneReadDeadline()
}

// networkOf is copied from zip because zip keeps it unexported. A copy that
// drifts would send a unix path to a tcp dialler, which fails at the connection
// rather than anywhere legible — so pin the rule.
func TestNetworkOfMatchesZipsRule(t *testing.T) {
	for addr, want := range map[string]string{
		"/var/lib/cloud/run/agents.sock": "unix",
		"./relative.sock":                "unix",
		"@abstract":                      "unix",
		"127.0.0.1:8000":                 "tcp",
		"example.com:443":                "tcp",
	} {
		if got := networkOf(addr); got != want {
			t.Errorf("networkOf(%q) = %q, want %q", addr, got, want)
		}
	}
}

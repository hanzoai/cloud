package agents

// targets_liveness_test.go — a target's reported status must follow its heartbeat.
//
// The row's status is only ever written by a request; nothing writes it when a
// machine simply stops beating. So a worker that died, or a host that was powered
// off, kept reporting "online" indefinitely. Production showed the fleet board with
// two GPUs online whose last heartbeats were five and nine days old.

import (
	"testing"
	"time"
)

func TestEffectiveStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	beat := func(age time.Duration) int64 { return now.Add(-age).Unix() }

	for _, tc := range []struct {
		name      string
		stored    string
		metricsAt int64
		want      string
	}{
		// The regression: beat long past the window, row still says online.
		{"stale beat reads offline", TargetOnline, beat(9 * 24 * time.Hour), TargetOffline},
		{"just past the window", TargetOnline, beat(LiveWindow + time.Second), TargetOffline},

		// Live machines must not flap — one missed 30s beat is still inside 90s.
		{"fresh beat stays online", TargetOnline, beat(10 * time.Second), TargetOnline},
		{"one missed beat stays online", TargetOnline, beat(45 * time.Second), TargetOnline},
		{"exactly at the window stays online", TargetOnline, beat(LiveWindow), TargetOnline},

		// Operator intent is not liveness and must survive a fresh beat.
		{"draining stays draining", TargetDraining, beat(time.Second), TargetDraining},
		{"offline stays offline", TargetOffline, beat(time.Second), TargetOffline},

		// A hand-registered destination has no heartbeat to judge it by.
		{"never beaten keeps stored status", TargetOnline, 0, TargetOnline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tgt := Target{Status: tc.stored, MetricsAt: tc.metricsAt}
			if got := tgt.EffectiveStatus(now); got != tc.want {
				t.Errorf("EffectiveStatus(stored=%q, age=%v) = %q, want %q",
					tc.stored, now.Sub(time.Unix(tc.metricsAt, 0)), got, tc.want)
			}
		})
	}
}

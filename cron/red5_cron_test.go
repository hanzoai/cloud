package cron

import (
	"testing"
	"time"

	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// Two pods, two engines, one schedule. Pod A fires the 01:00 tick on time; pod B
// was restarting at 01:00 and its engine catches the tick up once on boot. Both
// runs carry the tick the engine stamped on them, so B adopts A's Job however late
// it fired; an operator's trigger, carrying none, is its own.
func TestRed5LateCatchUpOnASecondEngineIsASecondJob(t *testing.T) {
	fire := func() *tasksengine.WorkflowExecution {
		return &tasksengine.WorkflowExecution{SearchAttrs: map[string]any{"TemporalScheduledStartTime": "2026-10-03T01:00:00Z"}}
	}
	onTime := jobNameFor("sql-backup", firingOf(fire(), "wf-a/run-a", "", time.Time{}))
	for _, run := range []string{"wf-b/run-55s-late", "wf-b/run-61s-late", "wf-b/run-3m-late"} {
		if late := jobNameFor("sql-backup", firingOf(fire(), run, "", time.Time{})); late != onTime {
			t.Errorf("a catch-up run %s of a tick another engine already ran names a second Job", run)
		}
	}
	if manual := jobNameFor("sql-backup", firingOf(&tasksengine.WorkflowExecution{}, "wf-m/run-m", "", time.Time{})); manual == onTime {
		t.Error("an operator's trigger adopted the tick's Job")
	}
}

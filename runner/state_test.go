package runner

import (
	"testing"
	"time"
)

func TestDaemonStatePauseFlag(t *testing.T) {
	s := newDaemonState()
	if s.IsPaused() {
		t.Fatal("state must start unpaused")
	}
	s.SetPaused(true)
	if !s.IsPaused() {
		t.Fatal("SetPaused(true) did not stick")
	}
	s.SetPaused(false)
	if s.IsPaused() {
		t.Fatal("SetPaused(false) did not stick")
	}
}

func TestDaemonStateJobLifecycle(t *testing.T) {
	s := newDaemonState()
	rec := JobRecord{
		Org:          "hanzoai",
		Repo:         "arc",
		WorkflowName: "ci",
		JobName:      "build",
		JobID:        42,
		RunnerName:   "evo-hanzoai-abcd1234",
		HTMLURL:      "https://github.com/hanzoai/arc/actions/runs/1",
	}
	s.StartJob(rec)
	if got := s.SnapshotActive(); len(got) != 1 || got[0].JobID != 42 {
		t.Fatalf("SnapshotActive after StartJob: %+v", got)
	}
	if got := s.SnapshotRecent(0); len(got) != 0 {
		t.Fatalf("SnapshotRecent before EndJob: %+v", got)
	}
	s.EndJob(42, false)
	if got := s.SnapshotActive(); len(got) != 0 {
		t.Fatalf("SnapshotActive after EndJob: %+v", got)
	}
	got := s.SnapshotRecent(0)
	if len(got) != 1 || got[0].JobID != 42 || got[0].Status != "done" {
		t.Fatalf("SnapshotRecent after EndJob: %+v", got)
	}
	if got[0].EndedAt.IsZero() {
		t.Fatal("EndedAt was not set")
	}
}

func TestDaemonStateRecentRingBound(t *testing.T) {
	s := newDaemonState()
	for i := int64(0); i < 50; i++ {
		s.StartJob(JobRecord{JobID: i, Org: "x", Repo: "y"})
		s.EndJob(i, false)
	}
	got := s.SnapshotRecent(0)
	if len(got) != s.recentN {
		t.Fatalf("recent ring exceeded bound: got %d want %d", len(got), s.recentN)
	}
	if got[len(got)-1].JobID != 49 {
		t.Fatalf("newest job is not last: got %d", got[len(got)-1].JobID)
	}
}

func TestDaemonStateIdleSince(t *testing.T) {
	s := newDaemonState()
	t0 := s.IdleSince()
	if t0.IsZero() {
		t.Fatal("IdleSince must be set at construction")
	}
	time.Sleep(2 * time.Millisecond)
	s.MarkIdle()
	t1 := s.IdleSince()
	if !t1.After(t0) {
		t.Fatalf("MarkIdle did not advance idle_since: t0=%v t1=%v", t0, t1)
	}
}

func TestDaemonStateEndJobUnknownIsNoop(t *testing.T) {
	s := newDaemonState()
	s.EndJob(999, false)
	if got := s.SnapshotRecent(0); len(got) != 0 {
		t.Fatalf("EndJob on unknown id must not append: %+v", got)
	}
}

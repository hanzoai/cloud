// Daemon state shared between the JIT loop and the tray HTTP surface.
// Kept separate so the tray UI's observability surface doesn't grow new
// fields on JITDaemon for every menu item we add later.
package runner

import (
	"sync"
	"sync/atomic"
	"time"
)

// JobRecord is one observed unit of work — either an active runner
// subprocess or a recently-finished one. The tray surfaces both in a
// single chronological list, distinguishing them by Status.
type JobRecord struct {
	Org          string    `json:"org"`
	Repo         string    `json:"repo"`
	WorkflowName string    `json:"workflow"`
	JobName      string    `json:"job"`
	JobID        int64     `json:"job_id"`
	RunnerName   string    `json:"runner_name"`
	HTMLURL      string    `json:"html_url"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	Status       string    `json:"status"` // running | done | failed
}

// daemonState holds the runtime view the tray surfaces: pause flag,
// last-tick timestamp, in-flight set, and a bounded ring of completed
// jobs. All access is mutex-guarded; the JIT loop's hot path
// (markSeen / inFlight) stays on its existing atomic.
type daemonState struct {
	paused    atomic.Bool
	idleSince atomic.Int64 // unix-nano of last in-flight -> 0 transition

	mu      sync.Mutex
	active  map[int64]*JobRecord
	recent  []JobRecord // newest last
	recentN int
}

func newDaemonState() *daemonState {
	s := &daemonState{
		active:  make(map[int64]*JobRecord),
		recent:  make([]JobRecord, 0, 32),
		recentN: 32,
	}
	s.idleSince.Store(time.Now().UnixNano())
	return s
}

func (s *daemonState) IsPaused() bool { return s.paused.Load() }
func (s *daemonState) SetPaused(v bool) {
	if v {
		s.paused.Store(true)
	} else {
		s.paused.Store(false)
	}
}

func (s *daemonState) StartJob(rec JobRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := rec
	r.Status = "running"
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	s.active[r.JobID] = &r
}

func (s *daemonState) EndJob(jobID int64, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.active[jobID]
	if !ok {
		return
	}
	delete(s.active, jobID)
	rec.EndedAt = time.Now().UTC()
	if failed {
		rec.Status = "failed"
	} else {
		rec.Status = "done"
	}
	s.recent = append(s.recent, *rec)
	if len(s.recent) > s.recentN {
		s.recent = s.recent[len(s.recent)-s.recentN:]
	}
}

// SnapshotActive returns a copy of currently-running jobs (chronological).
func (s *daemonState) SnapshotActive() []JobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobRecord, 0, len(s.active))
	for _, r := range s.active {
		out = append(out, *r)
	}
	return out
}

// SnapshotRecent returns the last N completed jobs (newest last).
// If n <= 0 or n > len(recent), all are returned.
func (s *daemonState) SnapshotRecent(n int) []JobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n >= len(s.recent) {
		out := make([]JobRecord, len(s.recent))
		copy(out, s.recent)
		return out
	}
	out := make([]JobRecord, n)
	copy(out, s.recent[len(s.recent)-n:])
	return out
}

func (s *daemonState) IdleSince() time.Time {
	return time.Unix(0, s.idleSince.Load()).UTC()
}

func (s *daemonState) MarkIdle() {
	s.idleSince.Store(time.Now().UnixNano())
}

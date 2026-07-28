package cloud

import (
	"sort"
	"sync"
)

// degraded.go — a subsystem that mounted FAIL-CLOSED says so out loud, in state
// rather than only in a log line.
//
// Several subsystems can boot into a fail-closed shape: commerce serves an honest
// 503 on every one of its prefixes when its embed cannot start, team does the same
// in degraded mode. That is the right call for the PROCESS — one broken plane must
// not take the binary down — but it left no way to tell two very different things
// apart from outside:
//
//	staged  — the subsystem was never enabled here. 503 is correct and boring.
//	failed  — the subsystem WAS enabled, could not boot, and is now dead.
//
// Both answered 503, so the release smoke tolerated both, and a dead revenue plane
// shipped behind a green pod, a green deploy and a green test gate. That is exactly
// what happened: commerce could not parse KV_URL, every /v1/plans answered
// "commerce unavailable", and nothing in the pipeline objected.
//
// So failure becomes a fact the process reports. Mark it here, serve it on
// /v1/health, and let the release gate refuse an image whose planes are dead.
// Fail-closed stays fail-closed; it just stops being silent.

var degradations struct {
	mu sync.RWMutex
	m  map[string]string // subsystem → why
}

// Degraded records that subsystem mounted fail-closed because of err. Calling it
// twice keeps the FIRST reason: the first failure is the cause, later ones are
// usually its echo.
func Degraded(subsystem string, err error) {
	if subsystem == "" || err == nil {
		return
	}
	degradations.mu.Lock()
	defer degradations.mu.Unlock()
	if degradations.m == nil {
		degradations.m = map[string]string{}
	}
	if _, seen := degradations.m[subsystem]; !seen {
		degradations.m[subsystem] = err.Error()
	}
}

// Degradations returns subsystem → reason for every plane that mounted
// fail-closed. The result is a COPY: a caller must not be able to edit the
// registry through the map it was handed.
func Degradations() map[string]string {
	degradations.mu.RLock()
	defer degradations.mu.RUnlock()
	out := make(map[string]string, len(degradations.m))
	for k, v := range degradations.m {
		out[k] = v
	}
	return out
}

// DegradedNames lists the failed subsystems, sorted so output is stable enough to
// diff and to assert on.
func DegradedNames() []string {
	degradations.mu.RLock()
	defer degradations.mu.RUnlock()
	out := make([]string, 0, len(degradations.m))
	for k := range degradations.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsDegraded reports whether any subsystem mounted fail-closed. The release smoke
// treats true as a HARD failure: an image whose planes are dead must not ship,
// however healthy the process looks.
func IsDegraded() bool {
	degradations.mu.RLock()
	defer degradations.mu.RUnlock()
	return len(degradations.m) > 0
}

// resetDegradedForTest clears the registry between tests.
func resetDegradedForTest() {
	degradations.mu.Lock()
	defer degradations.mu.Unlock()
	degradations.m = nil
}

package cd

import (
	"fmt"
	"sort"
	"sync"
)

// Targets is the registry: it maps a name to the Target that owns it.
//
// It exists so cd holds no compiled dependency on any kind. Kinds register
// themselves at mount time; this package imports none of them and never branches
// on Kind. Adding "function" or "worker" later is a new Register call, not an
// edit here — which is the property that keeps the lifecycle from re-growing the
// per-kind pipelines it replaced.
//
// Registration is a WRITE against a map that rollouts read concurrently, so it is
// guarded. Mounting is single-threaded today, but a lifecycle that panics under a
// race the first time a subsystem is registered lazily is a bad trade for one
// mutex on a cold path.
type Targets struct {
	mu sync.RWMutex
	m  map[string]Target
}

func NewTargets() *Targets { return &Targets{m: map[string]Target{}} }

// Register adds a target. A duplicate name is an ERROR, never a silent
// overwrite: two registrations for one name mean two owners for one deployment,
// which is exactly the two-writer confusion this design exists to remove. Failing
// at mount is loud and early; overwriting would surface later as a rollout that
// mysteriously went somewhere else.
func (t *Targets) Register(target Target) error {
	if target == nil {
		return fmt.Errorf("cd: register nil target")
	}
	name := target.Name()
	if name == "" {
		return fmt.Errorf("cd: register target with empty name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.m[name]; ok {
		return fmt.Errorf("cd: target %q already registered (%s); one name, one owner",
			name, existing.Kind())
	}
	t.m[name] = target
	return nil
}

// Target resolves a name. Satisfies Registry.
func (t *Targets) Target(name string) (Target, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	target, ok := t.m[name]
	return target, ok
}

// Names lists registered targets, sorted — a stable order so a dashboard or a
// CLI listing does not reshuffle between reads for no reason.
func (t *Targets) Names() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.m))
	for n := range t.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

var _ Registry = (*Targets)(nil)

package auto

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// concurrencyLimiter is a per-key in-flight counter with a hard ceiling. acquire
// reports whether a slot was granted (false ⇒ at capacity, caller returns 429);
// release returns it. Bounded, allocation-light, and fair per key.
type concurrencyLimiter struct {
	mu       sync.Mutex
	inflight map[string]int
	max      int
}

func newConcurrencyLimiter(max int) *concurrencyLimiter {
	return &concurrencyLimiter{inflight: make(map[string]int), max: max}
}

func (l *concurrencyLimiter) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[key] >= l.max {
		return false
	}
	l.inflight[key]++
	return true
}

func (l *concurrencyLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[key] > 0 {
		l.inflight[key]--
		if l.inflight[key] == 0 {
			delete(l.inflight, key)
		}
	}
}

// validateTrigger bounds a flow's step tree at every write (MED-3): the linear
// action chain is capped at maxSteps and the whole serialized trigger at
// maxTriggerBytes, so a hostile/oversized flow doc can't amplify the shared store or
// spawn an unbounded step fan-out. A nil trigger (empty flow) passes.
func validateTrigger(t *FlowTrigger) error {
	if t == nil {
		return nil
	}
	n := 0
	for a := t.NextAction; a != nil; a = a.NextAction {
		n++
		if n > maxSteps {
			return fmt.Errorf("flow exceeds the %d-step limit", maxSteps)
		}
		// An agent step acts as a person, so only the routes that record that
		// person with it write one (automation.go).
		if p, act := stepProviderAction(a); p == coreProvider && (act == agentAction || act == postAction) {
			return errors.New("an agent or post step is written through /v1/auto/automations")
		}
	}
	if cron, ok := pollingCron(FlowVersion{Trigger: t}, nil); ok {
		if err := checkCron(cron); err != nil {
			return err
		}
	}
	b, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("encode trigger: %w", err)
	}
	if len(b) > maxTriggerBytes {
		return fmt.Errorf("flow tree exceeds the %d-byte limit", maxTriggerBytes)
	}
	return nil
}

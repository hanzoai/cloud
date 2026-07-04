package automations

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

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

// validOrg accepts a DNS-1123-ish label. The org is folded into per-org engine
// namespaces + the SQLite store key, so it is validated strictly at every boundary.
// Identical rule to clients/integrations' tenant boundary (kept local because that
// copy is unexported; both mirror the SAME platform org-slug shape).
func validOrg(org string) bool {
	if org == "" || len(org) > 63 {
		return false
	}
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

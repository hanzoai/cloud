package leaderboard

import (
	"sync"
	"time"
)

// retry is how long a failed read is not tried again, so a source that is down is
// asked once in that time and not once per reader.
const retry = 10 * time.Second

// memo is one shared read of public numbers: the board and the pulse are read by
// anyone, so every reader would otherwise cost a scan of the warehouse. A read is
// reused while it is fresh; a failed one serves the last good read, and fails when
// there never was one; and a failure is not retried at once.
type memo[T any] struct {
	mu     sync.Mutex
	at     time.Time
	val    *T
	failed time.Time
	err    error
}

// get is the read, from the cache while it is younger than ttl.
func (m *memo[T]) get(ttl time.Duration, read func() (*T, error)) (*T, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.val != nil && nowFn().Sub(m.at) < ttl {
		return m.val, nil
	}
	if m.err != nil && nowFn().Sub(m.failed) < retry {
		return m.last()
	}
	v, err := read()
	if err != nil {
		m.failed, m.err = nowFn(), err
		return m.last()
	}
	m.val, m.at, m.err = v, nowFn(), nil
	return v, nil
}

// last is the last good read, or the failure when there was none.
func (m *memo[T]) last() (*T, error) {
	if m.val != nil {
		return m.val, nil
	}
	return nil, m.err
}

// reset forgets the read and the failure.
func (m *memo[T]) reset() {
	m.mu.Lock()
	m.val, m.at, m.failed, m.err = nil, time.Time{}, time.Time{}, nil
	m.mu.Unlock()
}

// expire makes the read stale, keeping it to serve while the next fails.
func (m *memo[T]) expire() {
	m.mu.Lock()
	m.at, m.failed, m.err = time.Time{}, time.Time{}, nil
	m.mu.Unlock()
}

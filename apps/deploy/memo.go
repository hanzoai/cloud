package deploy

// memo.go — what is read from outside the cluster, and kept.
//
// CD knows the images its pods run and the universe commit it applied, and
// nothing about how either was made. The registry answers what built an image and
// the universe repository answers what a service is pinned to, and both are named
// by something immutable: a digest is one set of bytes forever, a commit is one
// tree forever. So a value read once is the answer for the life of the process,
// and a request pays for the reads it has not made.

import (
	"context"
	"sync"
	"time"
)

const (
	// budget bounds how long one request waits on those reads. A value not read
	// inside it is left out of this answer and asked for again on the next, so a
	// cold cache costs the first few requests completeness, never time.
	budget = 4 * time.Second
	// reads bounds the reads one request makes at once.
	reads = 8
	// retry is how long a failed read is not repeated: a private image this pod
	// holds no credential for fails the same way every time.
	retry = 10 * time.Minute
	// keep bounds a memo. Past it the memo starts over, which costs one cold
	// request and nothing else.
	keep = 4096
)

// memo remembers what read answered for a key.
type memo[V any] struct {
	mu     sync.Mutex
	known  map[string]V
	failed map[string]time.Time // key → when its read last failed
}

func newMemo[V any]() *memo[V] {
	return &memo[V]{known: map[string]V{}, failed: map[string]time.Time{}}
}

// get answers key from what this process knows, else reads it. False means the
// answer is unknown, not empty: the read failed, or ran out of the caller's
// budget and will be made again.
func (m *memo[V]) get(ctx context.Context, key string, read func(context.Context) (V, error)) (V, bool) {
	var none V
	m.mu.Lock()
	if v, ok := m.known[key]; ok {
		m.mu.Unlock()
		return v, true
	}
	if at, ok := m.failed[key]; ok && time.Since(at) <= retry {
		m.mu.Unlock()
		return none, false
	}
	m.mu.Unlock()

	v, err := read(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.known)+len(m.failed) >= keep {
		m.known, m.failed = map[string]V{}, map[string]time.Time{}
	}
	if err != nil {
		// Out of budget is this request giving up, not the source refusing.
		if ctx.Err() == nil {
			m.failed[key] = time.Now()
		}
		return none, false
	}
	m.known[key] = v
	return v, true
}

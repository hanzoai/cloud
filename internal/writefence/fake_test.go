package writefence

import (
	"context"
	"fmt"
	"strconv"
	"sync"
)

// fakeStore is an in-process model of a KEYED object store's atomic
// conditional write (S3 If-Match / GCS generation-match) — one independent
// (data, generation) slot per key, exactly like a real bucket. A single
// mutex makes the compare-and-set in PutIfVersion indivisible from every
// caller's point of view — exactly like the real store's server-side
// atomicity, NOT a client-side "read then write" pair with an exploitable
// gap.
//
// beforeCAS, when set, runs OUTSIDE the lock, right before PutIfVersion
// attempts to acquire it. Tests use it to force two goroutines to complete
// their Get before either one's PutIfVersion proceeds — deterministically
// reproducing, on every run, the race a real object store would otherwise
// only expose under specific network timing.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string]*fakeObject

	beforeCAS func(key string)
}

type fakeObject struct {
	data []byte
	ver  int // generation counter; the version token is its decimal string.
}

func newFakeStore() *fakeStore { return &fakeStore{objects: map[string]*fakeObject{}} }

func (s *fakeStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[key]
	if !ok {
		return nil, "", ErrNotFound
	}
	return append([]byte(nil), obj.data...), strconv.Itoa(obj.ver), nil
}

func (s *fakeStore) PutIfVersion(_ context.Context, key string, data []byte, expectVersion string) (string, error) {
	if s.beforeCAS != nil {
		s.beforeCAS(key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(obj.ver)
	}
	if cur != expectVersion {
		return "", fmt.Errorf("fake store: %w: have version %q, want %q", ErrConflict, cur, expectVersion)
	}
	if !ok {
		obj = &fakeObject{}
		s.objects[key] = obj
	}
	obj.ver++
	obj.data = append([]byte(nil), data...)
	return strconv.Itoa(obj.ver), nil
}

// barrierAt2 returns a beforeCAS hook that blocks the first two callers
// until both have arrived, then releases both simultaneously; every call
// after that (retries) passes straight through. This is what turns "two
// goroutines racing" into a guaranteed, repeatable interleaving instead of a
// flaky one.
func barrierAt2() func(string) {
	var mu sync.Mutex
	arrived := 0
	release := make(chan struct{})
	return func(string) {
		mu.Lock()
		arrived++
		n := arrived
		mu.Unlock()
		if n == 2 {
			close(release) // exactly the 2nd caller closes it, once.
		}
		<-release
	}
}

// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package host

// durable_proven_test.go holds provenStore to its caching rule: a verdict about
// the store (atomic, or proven non-atomic) is kept for the process life; a probe
// the store did not answer is kept only until the retry interval passes.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/vfs/replica"
)

// unreachableStore answers every call with a deadline while down is set, and
// otherwise delegates to a faithful in-memory CAS store. calls counts every call
// that reached it, so a cached answer is distinguishable from a fresh probe.
type unreachableStore struct {
	inner replica.ConditionalStore
	down  atomic.Bool
	calls atomic.Int64
}

// lastWriteWins admits every put regardless of its precondition — the store the
// probe exists to refuse.
type lastWriteWins struct {
	mu   sync.Mutex
	seq  int
	objs map[string]string
}

// clock is a settable time source for provenStore.now.
type clock struct{ t time.Time }

func newProven(s replica.ConditionalStore, c *clock) *provenStore {
	p := proven(s, luxlog.NewNoOpLogger()).(*provenStore)
	p.now = c.now
	return p
}

func TestProvenStoreRetriesAfterUnreachableProbe(t *testing.T) {
	ctx := context.Background()
	store := &unreachableStore{inner: &recordingStore{objects: map[string]versioned{}}}
	c := &clock{t: time.Unix(1_790_000_000, 0)}
	p := newProven(store, c)

	store.down.Store(true)
	_, err := p.PutIfVersion(ctx, "orgs/acme/.owner", []byte("lease"), "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe against an unreachable store: got %v, want a wrapped deadline", err)
	}
	if errors.Is(err, tenure.ErrNonAtomicStore) {
		t.Fatalf("an unreachable store was judged non-atomic: %v", err)
	}

	// Inside the retry interval the failure is answered from memory: no call
	// reaches the store.
	before := store.calls.Load()
	if _, _, err := p.Get(ctx, "orgs/acme/.owner"); err == nil {
		t.Fatal("inside the retry interval the store answered as usable")
	}
	if got := store.calls.Load(); got != before {
		t.Fatalf("inside the retry interval the store saw %d more calls, want 0", got-before)
	}

	// The store answers again. Once the interval has passed the next caller
	// probes, the probe passes, and the write lands.
	store.down.Store(false)
	c.add(probeRetryMin)
	if _, err := p.PutIfVersion(ctx, "orgs/acme/.owner", []byte("lease"), ""); err != nil {
		t.Fatalf("after the store recovered and the interval passed: %v", err)
	}
	data, _, err := p.Get(ctx, "orgs/acme/.owner")
	if err != nil || string(data) != "lease" {
		t.Fatalf("read-back after recovery: data=%q err=%v", data, err)
	}

	// A confirmed store stays confirmed: a later outage surfaces as the store's
	// own error on the call, never as a re-run probe.
	store.down.Store(true)
	before = store.calls.Load()
	_, _, err = p.Get(ctx, "orgs/acme/.owner")
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "atomicity") {
		t.Fatalf("outage after confirmation: got %v, want the store's own deadline", err)
	}
	if got := store.calls.Load() - before; got != 1 {
		t.Fatalf("outage after confirmation made %d store calls, want 1 (no probe)", got)
	}
}

func TestProvenStoreRetryIntervalDoublesToCap(t *testing.T) {
	ctx := context.Background()
	store := &unreachableStore{inner: &recordingStore{objects: map[string]versioned{}}}
	store.down.Store(true)
	c := &clock{t: time.Unix(1_790_000_000, 0)}
	p := newProven(store, c)

	want := probeRetryMin
	for range 10 {
		if _, _, err := p.Get(ctx, "k"); err == nil {
			t.Fatal("unreachable store answered as usable")
		}
		if p.wait != want {
			t.Fatalf("retry interval = %s, want %s", p.wait, want)
		}
		c.add(p.wait)
		want = min(2*want, probeRetryMax)
	}
}

func TestProvenStoreRemembersNonAtomicVerdict(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Unix(1_790_000_000, 0)}
	p := newProven(&lastWriteWins{objs: map[string]string{}}, c)

	_, err := p.PutIfVersion(ctx, "orgs/acme/.owner", []byte("lease"), "")
	if !errors.Is(err, tenure.ErrNonAtomicStore) {
		t.Fatalf("last-write-wins store: got %v, want ErrNonAtomicStore", err)
	}
	c.add(24 * time.Hour)
	if _, _, err := p.Get(ctx, "orgs/acme/.owner"); !errors.Is(err, tenure.ErrNonAtomicStore) {
		t.Fatalf("a day later: got %v, want the remembered non-atomic verdict", err)
	}
}

func (c *clock) now() time.Time { return c.t }

func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func (s *unreachableStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	s.calls.Add(1)
	if s.down.Load() {
		return nil, "", fmt.Errorf("get %s: %w", key, context.DeadlineExceeded)
	}
	return s.inner.Get(ctx, key)
}

func (s *unreachableStore) PutIfVersion(ctx context.Context, key string, data []byte, expect string) (string, error) {
	s.calls.Add(1)
	if s.down.Load() {
		return "", fmt.Errorf("put %s: %w", key, context.DeadlineExceeded)
	}
	return s.inner.PutIfVersion(ctx, key, data, expect)
}

func (s *lastWriteWins) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.objs[key]
	if !ok {
		return nil, "", fmt.Errorf("get %s: %w", key, replica.ErrNotFound)
	}
	return []byte("x"), v, nil
}

func (s *lastWriteWins) PutIfVersion(_ context.Context, key string, _ []byte, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	v := fmt.Sprintf("v%d", s.seq)
	s.objs[key] = v
	return v, nil
}

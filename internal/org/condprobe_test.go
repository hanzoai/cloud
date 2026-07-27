// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// condprobe_test.go proves the boot atomicity self-check: an atomic store is
// confirmed, a last-write-wins (non-atomic) store is REFUSED (ErrNonAtomicStore, the
// caller then runs local-only), and an unreachable store never reads as atomic. This
// is H2 as a self-validating boot probe rather than a human-set flag.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/hanzoai/vfs/replica"
)

// TestProbeCASAtomicStoreConfirmed: over the SAME atomic fakeCondStore the fence trusts
// (single mutex making PutIfVersion an indivisible compare-and-set), the probe confirms
// atomicity — exactly one winner in both the create-race and the update-race.
func TestProbeCASAtomicStoreConfirmed(t *testing.T) {
	if err := ProbeCAS(context.Background(), newFakeCondStore(), ".probe/cas-"); err != nil {
		t.Fatalf("atomic store must confirm: %v", err)
	}
}

// TestProbeCASNonAtomicRefused: a store whose conditional PUT ignores the precondition
// (both racers "win") is the split-brain hazard the probe exists to catch — it must
// return ErrNonAtomicStore so buildDurability fails safe to local-only.
func TestProbeCASNonAtomicRefused(t *testing.T) {
	err := ProbeCAS(context.Background(), &nonAtomicStore{objects: map[string][]byte{}}, ".probe/cas-")
	if !errors.Is(err, ErrNonAtomicStore) {
		t.Fatalf("non-atomic store must be refused: err=%v, want ErrNonAtomicStore", err)
	}
}

// TestProbeCASUnreachableNotAtomic: an unreachable store surfaces a hard error and is
// NEVER reported atomic (zero winners must not be mistaken for a pass).
func TestProbeCASUnreachableNotAtomic(t *testing.T) {
	cs := newFakeCondStore()
	cs.failAll = errors.New("object store unreachable")
	if err := ProbeCAS(context.Background(), cs, ".probe/cas-"); err == nil {
		t.Fatal("unreachable store must not confirm atomicity")
	}
}

// nonAtomicStore is a last-write-wins object store: PutIfVersion ignores expectVersion
// and always succeeds, modelling an S3 gateway with no real conditional-PUT support —
// the exact store the probe must refuse to fence on.
type nonAtomicStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	ver     int
}

func (s *nonAtomicStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), data...), strconv.Itoa(s.ver), nil
}

func (s *nonAtomicStore) PutIfVersion(_ context.Context, key string, data []byte, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ver++
	s.objects[key] = append([]byte(nil), data...)
	return strconv.Itoa(s.ver), nil // ignores the precondition — always "wins".
}

var _ replica.ConditionalStore = (*nonAtomicStore)(nil)

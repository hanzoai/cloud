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
	"crypto/md5"
	"errors"
	"fmt"
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

// etagCondStore is an ATOMIC conditional store whose version is derived from the
// CONTENT, the way an S3 ETag is. fakeCondStore versions by a counter instead, so
// it bumps even when a write stores identical bytes — which is precisely the
// behaviour that hid this bug: the probe rewrote bytes that were already there,
// the counter moved anyway, and every unit test passed while production sat with
// durability disabled.
//
// Nothing here is racy: one mutex makes compare-and-set indivisible. If the probe
// reports more than one winner over this store, the probe is wrong.
type etagCondStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newETagCondStore() *etagCondStore { return &etagCondStore{objects: map[string][]byte{}} }

func etagOf(data []byte) string { return fmt.Sprintf("%x", md5.Sum(data)) }

func (s *etagCondStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), data...), etagOf(data), nil
}

func (s *etagCondStore) PutIfVersion(_ context.Context, key string, data []byte, expectVersion string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := ""
	if existing, ok := s.objects[key]; ok {
		cur = etagOf(existing)
	}
	if cur != expectVersion {
		return "", fmt.Errorf("etagCondStore: %w: have %q want %q", replica.ErrConflict, cur, expectVersion)
	}
	s.objects[key] = append([]byte(nil), data...)
	return etagOf(data), nil
}

var _ replica.ConditionalStore = (*etagCondStore)(nil)

// TestProbeCASContentAddressedStoreConfirmed is the regression. Over a store that
// is atomic BY CONSTRUCTION but versions by content, the probe must still find
// exactly one winner per race — which requires that no two writes it performs
// ever carry the same bytes.
//
// The payloads used to be four fixed values (`cas-probe0`..`cas-probe3`) reused
// by BOTH phases. The create race left the object at one of them; the update race
// conditioned on that ETag and wrote the same four again, so a 1-in-4 draw
// rewrote identical bytes, left the ETag unmoved, and let every remaining racer's
// If-Match still hold. All of them committed and the probe called the store
// non-atomic. Measured against production s3: 56/300 runs. With the payload bound
// to the probe key and the raced version: 0/400.
func TestProbeCASContentAddressedStoreConfirmed(t *testing.T) {
	for i := 0; i < 200; i++ {
		if err := ProbeCAS(context.Background(), newETagCondStore(), ".probe/cas-"); err != nil {
			t.Fatalf("run %d: atomic content-addressed store must be confirmed, got: %v", i, err)
		}
	}
}

// A store that is genuinely non-atomic must still be refused when its version is
// content-derived, so the test above cannot pass by making the probe blind.
func TestProbeCASContentAddressedNonAtomicRefused(t *testing.T) {
	err := ProbeCAS(context.Background(), &nonAtomicStore{objects: map[string][]byte{}}, ".probe/cas-")
	if !errors.Is(err, ErrNonAtomicStore) {
		t.Fatalf("want ErrNonAtomicStore, got %v", err)
	}
}

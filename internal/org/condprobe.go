// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// condprobe.go is the startup self-validation that decides — with NO operator flag —
// whether it is SAFE to run the durable+fenced plane over a given object store. The
// fence's whole safety (fence.go, github.com/hanzoai/vfs/replica.FencedStore) rests on
// the store enforcing conditional-PUT (If-None-Match on create, If-Match on update)
// ATOMICALLY server-side: two racing conditional writes at the same expected version
// must resolve to EXACTLY ONE winner. A store that silently admits both (a
// last-write-wins gateway with no real precondition) would let two elected writers each
// "win" a lease round and split-brain the org. So the binary PROVES the property at
// boot against the actual deployed store, once, rather than trusting a human toggle.
//
// This is the atomicity gate as a self-check the binary runs — a sane default, not a
// staging flag: durable when the store is provably atomic, local-only (fail-safe) when
// it is not, and never probed at all when no store is reachable (native-Go / dev, which
// is why durability degrades gracefully everywhere). buildDurability runs it exactly
// once at boot and caches the outcome for the process life (deps.Durable is nil-or-set
// thereafter), so it is the single "once" the way cek's master-key probe is.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/hanzoai/vfs/replica"
)

// ErrNonAtomicStore reports that the object store did not enforce conditional-PUT
// atomically — the caller MUST fail safe (run local-only, never fence on it), because
// fencing on a store that admits two winners for one round is split-brain by
// construction.
var ErrNonAtomicStore = errors.New("org: object store does not enforce conditional-PUT atomically — refusing to fence (would risk split-brain)")

// probeRacers is the concurrency each race phase fires at the store. A correct
// compare-and-set admits exactly one; more racers only sharpen the odds of catching a
// non-atomic gateway, at a fixed, tiny boot cost.
const probeRacers = 4

// ProbeCAS proves the store enforces compare-and-set atomically, exercising BOTH
// preconditions the fence relies on, against a fresh throwaway key:
//
//	create-race : probeRacers goroutines each PutIfVersion(key, _, "") — If-None-Match
//	              on a non-existent object — so exactly one may CREATE it.
//	update-race : read the create winner's version, then probeRacers goroutines each
//	              PutIfVersion(key, _, thatVersion) — If-Match — so exactly one may
//	              ADVANCE it.
//
// Exactly-one-winner in BOTH phases ⇒ atomic-confirmed (nil). Any other outcome —
// more than one winner (non-atomic ⇒ ErrNonAtomicStore), zero winners, or a hard store
// error (unreachable / refused) — is returned for the caller to fail safe on. The probe
// writes only under the caller's chosen throwaway prefix (a ".probe/" object per boot,
// reapable by a bucket lifecycle rule), never into the orgs/ tree.
func ProbeCAS(ctx context.Context, store replica.ConditionalStore, keyPrefix string) error {
	key, err := probeKey(keyPrefix)
	if err != nil {
		return err
	}
	// Phase 1 — create-race (If-None-Match): a fresh key, so exactly one racer creates.
	winners, err := raceCAS(ctx, store, key, "")
	if err != nil {
		return fmt.Errorf("org: CAS probe create-race: %w", err)
	}
	if winners != 1 {
		return fmt.Errorf("%w: create-race admitted %d writers (want 1) — If-None-Match not atomic", ErrNonAtomicStore, winners)
	}
	// Read the created object's version to condition the update-race on it.
	_, version, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("org: CAS probe read-back: %w", err)
	}
	// Phase 2 — update-race (If-Match at the SAME version): exactly one may advance it.
	winners, err = raceCAS(ctx, store, key, version)
	if err != nil {
		return fmt.Errorf("org: CAS probe update-race: %w", err)
	}
	if winners != 1 {
		return fmt.Errorf("%w: update-race admitted %d writers (want 1) — If-Match not atomic", ErrNonAtomicStore, winners)
	}
	return nil
}

// raceCAS fires probeRacers concurrent PutIfVersion(key, distinct, expectVersion) and
// counts how many the store admitted. A loser that failed the precondition
// (replica.ErrConflict) is the correct, expected outcome; any OTHER error is a hard
// store failure surfaced so the caller fails safe (an unreachable store yields zero
// winners AND a hard error, so it never reads as "atomic").
func raceCAS(ctx context.Context, store replica.ConditionalStore, key, expectVersion string) (winners int, hardErr error) {
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	wg.Add(probeRacers)
	for i := 0; i < probeRacers; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := store.PutIfVersion(ctx, key, probePayload(i), expectVersion)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, replica.ErrConflict):
				// Expected loser: the precondition held and rejected this racer.
			case hardErr == nil:
				hardErr = err
			}
		}(i)
	}
	wg.Wait()
	return winners, hardErr
}

// probeKey mints a unique-per-boot throwaway object key under keyPrefix so the
// create-race always tests a genuine creation (a fixed key would exist on the second
// boot and make the create-race admit zero winners — a false non-atomic verdict).
func probeKey(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("org: CAS probe key: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// probePayload is a small distinct value per racer so a store that (wrongly) accepts
// two writes leaves observably different bytes, not an idempotent no-op.
func probePayload(i int) []byte { return []byte{'c', 'a', 's', '-', 'p', 'r', 'o', 'b', 'e', byte('0' + i)} }

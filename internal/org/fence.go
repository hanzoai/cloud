// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// fence.go is the INTERIM round source: a monotone per-org writer lease over a
// single linearizable register (the object store's compare-and-set). It is the
// concrete ha.Fencer the cloud wires today, before the Lux BFT round graduates —
// and by design it sits BEHIND the ha.Fencer seam, so swapping in the BFT round
// later changes this file and nothing at any call site.
//
// # How the three concerns compose (one lane each)
//
//	ASSIGNMENT (who)   Owner()/IsOwner() over the live membership — pure HRW.
//	ROUND (fencing)    THIS file: a lease object per org holding {round, owner};
//	                   a takeover strictly bumps the round via a CAS, a renewal
//	                   keeps it. The round is monotone and strictly increases on
//	                   every ownership handoff — the one invariant ha.Fencer owes.
//	ADMISSION (store)  replica.FencedStore rejects any per-org-DB ship stamped
//	                   below the recorded round (ErrStaleRound).
//
// HRW is only an OPTIMIZATION here: it decides who SHOULD try, cutting lease
// contention to (normally) one candidate and giving deterministic failover order.
// Safety does NOT depend on HRW being fresh or agreed — if two replicas with
// divergent membership views each believe they own an org, both claim the lease,
// the CAS linearizes them, the higher round wins, and the loser's data ships are
// fenced by FencedStore. A stale/split HRW view costs liveness (claim ping-pong),
// never safety. That is precisely why the round source is composed in, not folded
// into the coordination-free election.
//
// # Interim vs. the Lux BFT phase
//
// This lease is correct under CRASH faults: the object store is a single
// linearizable register, so a minority that cannot reach it cannot advance the
// round and is fenced. It is NOT Byzantine-fault-tolerant and has a single
// linearizing dependency. The roadmap replaces readLease/claim's CAS with a read
// of the quasar PQ-BFT RSM's agreed round (deterministic finality, 3/5 quorum), so
// the round survives f faults with no single register to lose — same ha.Fencer
// seam, same FencedStore admission.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path"

	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

// ErrNoMembership is returned by a Fencer when the live replica set is empty: no
// safe owner can be named, so the caller fails CLOSED (does not write).
var ErrNoMembership = errors.New("org: empty membership — no safe writer")

// ErrNotOwner is returned when this replica is not the HRW-elected writer for the
// org. The caller does not write; it forwards to, or steps aside for, the owner.
var ErrNotOwner = errors.New("org: not the elected writer for this org")

// maxLeaseAttempts bounds the claim CAS loop. Under HRW there is normally a single
// candidate and zero contention; the bound only caps a rare split-brain ping-pong,
// failing closed (the caller does not write) rather than spinning.
const maxLeaseAttempts = 8

// ownerView is the slice of Membership the Fencer needs: this replica's id and the
// live set. *Membership satisfies it; tests substitute a stub. (Member == ha.Member.)
type ownerView interface {
	Self() string
	Members() []Member
}

// CASFencer implements ha.Fencer by claiming/renewing a per-org writer lease in a
// linearizable object store. Acquire returns the caller's monotone lease round;
// FencedStore then stamps every ship with it.
type CASFencer struct {
	store replica.ConditionalStore
	view  ownerView
}

// NewCASFencer builds a CASFencer over an atomic-CAS store and a membership view.
// The SAME store instance also backs the per-org replica.FencedStore, so the lease
// and the data ships share one linearizable register.
func NewCASFencer(store replica.ConditionalStore, view ownerView) *CASFencer {
	return &CASFencer{store: store, view: view}
}

// Acquire implements ha.Fencer. It fails CLOSED unless this replica is the elected
// writer over a non-empty membership, then claims (strictly bumping the round) or
// renews (keeping the round) the org's lease.
func (f *CASFencer) Acquire(ctx context.Context, orgID string) (ha.Lease, error) {
	members := f.view.Members()
	if len(members) == 0 {
		return ha.Lease{}, fmt.Errorf("%w: org=%s", ErrNoMembership, orgID)
	}
	self := f.view.Self()
	if !IsOwner(orgID, self, members) {
		owner, _ := Owner(orgID, members)
		return ha.Lease{}, fmt.Errorf("%w: org=%s owner=%s self=%s", ErrNotOwner, orgID, owner.ID, self)
	}
	return f.claim(ctx, orgID, self)
}

// ElectsSelf reports whether this replica is the HRW-elected writer for orgID under the
// CURRENT live membership — the election half of Acquire WITHOUT the lease CAS, so it does
// no I/O. It is the cheap gate a degraded (read-only) store consults to decide whether a
// membership change has made it the owner and it should attempt promotion. Fail-closed on
// an empty set (no safe owner ⇒ not self).
func (f *CASFencer) ElectsSelf(orgID string) bool {
	members := f.view.Members()
	return len(members) > 0 && IsOwner(orgID, f.view.Self(), members)
}

// claim reads the current lease and either renews it (owner == self: keep round)
// or takes it over (strictly bump the round to recorded+1, via a version-
// conditioned CAS so two racing claimers cannot both win the same round).
func (f *CASFencer) claim(ctx context.Context, orgID, self string) (ha.Lease, error) {
	key := leaseKey(orgID)
	var lastErr error
	for attempt := 0; attempt < maxLeaseAttempts; attempt++ {
		recorded, owner, version, err := f.readLease(ctx, key)
		if err != nil {
			return ha.Lease{}, err
		}
		if owner == self {
			// Renew: we already hold the lease; keep the round so a stable owner's
			// successive ships stay at one round (same-round re-ship, admitted).
			return ha.Lease{Key: orgID, Owner: Member{ID: self}, Round: ha.Round(recorded)}, nil
		}
		next := recorded + 1
		if _, err := f.store.PutIfVersion(ctx, key, encodeLease(next, self), version); err != nil {
			if errors.Is(err, replica.ErrConflict) {
				lastErr = err
				continue // another claimer moved the lease; re-read and re-decide.
			}
			return ha.Lease{}, fmt.Errorf("org: claim lease %s: %w", orgID, err)
		}
		return ha.Lease{Key: orgID, Owner: Member{ID: self}, Round: ha.Round(next)}, nil
	}
	if lastErr == nil {
		lastErr = replica.ErrConflict
	}
	return ha.Lease{}, fmt.Errorf("org: claim lease %s exhausted %d attempts: %w", orgID, maxLeaseAttempts, lastErr)
}

// leaseKey is the object holding an org's writer lease — one per org, distinct from
// the org's DB objects (orgs/<id>/<svc>.db). The lease round governs ownership;
// each DB object fences on that same round when the owner ships it.
func leaseKey(orgID string) string { return path.Join("orgs", orgID, ".owner") }

const leaseMagic = "olse1\x00" // one-org-lease v1; detects a foreign object at the key.

func encodeLease(round uint64, owner string) []byte {
	buf := make([]byte, 0, len(leaseMagic)+8+len(owner))
	buf = append(buf, leaseMagic...)
	var r [8]byte
	binary.BigEndian.PutUint64(r[:], round)
	buf = append(buf, r[:]...)
	return append(buf, owner...)
}

func decodeLease(b []byte) (round uint64, owner string, err error) {
	m := len(leaseMagic)
	if len(b) < m+8 || string(b[:m]) != leaseMagic {
		return 0, "", fmt.Errorf("org: corrupt lease record")
	}
	return binary.BigEndian.Uint64(b[m : m+8]), string(b[m+8:]), nil
}

// readLease returns the recorded (round, owner) and the store version token to
// condition the next claim on. An absent lease is (0, "", "", nil) — the first
// owner claims round 1.
func (f *CASFencer) readLease(ctx context.Context, key string) (round uint64, owner, version string, err error) {
	data, version, err := f.store.Get(ctx, key)
	switch {
	case errors.Is(err, replica.ErrNotFound):
		return 0, "", "", nil
	case err != nil:
		return 0, "", "", fmt.Errorf("org: read lease %s: %w", key, err)
	}
	round, owner, err = decodeLease(data)
	if err != nil {
		return 0, "", "", err
	}
	return round, owner, version, nil
}

var _ ha.Fencer = (*CASFencer)(nil)

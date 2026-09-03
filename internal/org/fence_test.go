// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

// fakeCondStore is an in-process replica.ConditionalStore: one atomic
// (data, generation) slot per key, a single mutex making PutIfVersion's
// compare-and-set indivisible — the server-side CAS a real S3 gateway
// provides. Shared by the fencer and the handoff tests as the ONE object store two
// pods talk to. failAll models a pod partitioned from the store.
type fakeCondStore struct {
	mu        sync.Mutex
	objects   map[string]*fakeSlot
	beforeCAS func(key string)
	failAll   error // when set, every op returns it (a store-unreachable partition).
}

type fakeSlot struct {
	data []byte
	ver  int
}

func newFakeCondStore() *fakeCondStore { return &fakeCondStore{objects: map[string]*fakeSlot{}} }

func (s *fakeCondStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAll != nil {
		return nil, "", s.failAll
	}
	obj, ok := s.objects[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), obj.data...), strconv.Itoa(obj.ver), nil
}

func (s *fakeCondStore) PutIfVersion(_ context.Context, key string, data []byte, expectVersion string) (string, error) {
	if s.beforeCAS != nil {
		s.beforeCAS(key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAll != nil {
		return "", s.failAll
	}
	obj, ok := s.objects[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(obj.ver)
	}
	if cur != expectVersion {
		return "", fmt.Errorf("fakeCondStore: %w: have %q want %q", replica.ErrConflict, cur, expectVersion)
	}
	if !ok {
		obj = &fakeSlot{}
		s.objects[key] = obj
	}
	obj.ver++
	obj.data = append([]byte(nil), data...)
	return strconv.Itoa(obj.ver), nil
}

// stubView is a fixed membership view — the client the fencer's HRW gate runs over.
type stubView struct {
	id  string
	set []Member
}

func (v stubView) Self() string      { return v.id }
func (v stubView) Members() []Member { return v.set }

// electedOwnerID returns the HRW owner of orgID over set — to pick a self that IS
// or is NOT the owner deterministically.
func electedOwnerID(orgID string, set []Member) string {
	o, _ := Owner(orgID, set)
	return o.ID
}

// TestCASFencerElectedOwnerClaims_NonOwnerRefused: over a fixed set, only the
// HRW-elected owner's Acquire succeeds; a non-owner is refused (ErrNotOwner) and
// does not write.
func TestCASFencerElectedOwnerClaims_NonOwnerRefused(t *testing.T) {
	cs := newFakeCondStore()
	set := []Member{{ID: "pod-a"}, {ID: "pod-b"}, {ID: "pod-c"}}
	owner := electedOwnerID("acme", set)
	nonOwner := "pod-a"
	if nonOwner == owner {
		nonOwner = "pod-b"
	}

	ownerF := NewCASFencer(cs, stubView{id: owner, set: set})
	l, err := ownerF.Acquire(context.Background(), "acme")
	if err != nil {
		t.Fatalf("elected owner Acquire: %v", err)
	}
	if l.Owner.ID != owner || l.Round != 1 {
		t.Fatalf("owner lease = %+v, want owner=%s round=1", l, owner)
	}

	nonF := NewCASFencer(cs, stubView{id: nonOwner, set: set})
	if _, err := nonF.Acquire(context.Background(), "acme"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner Acquire: err=%v, want ErrNotOwner", err)
	}
}

// TestCASFencerFailsClosedOnEmptyMembership: no live set → no safe writer →
// ErrNoMembership, never a claim.
func TestCASFencerFailsClosedOnEmptyMembership(t *testing.T) {
	f := NewCASFencer(newFakeCondStore(), stubView{id: "pod-a", set: nil})
	if _, err := f.Acquire(context.Background(), "acme"); !errors.Is(err, ErrNoMembership) {
		t.Fatalf("empty membership: err=%v, want ErrNoMembership", err)
	}
}

// TestCASFencerRenewKeepsRound: a stable owner re-acquiring keeps its round (so its
// successive ships stay at one round — same-round re-ship, admitted by FencedStore).
func TestCASFencerRenewKeepsRound(t *testing.T) {
	cs := newFakeCondStore()
	set := []Member{{ID: "solo"}}
	f := NewCASFencer(cs, stubView{id: "solo", set: set})
	a, err := f.Acquire(context.Background(), "acme")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := f.Acquire(context.Background(), "acme")
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if a.Round != 1 || b.Round != 1 {
		t.Fatalf("renew changed the round: %d then %d, want 1 then 1", a.Round, b.Round)
	}
}

// TestCASFencerMonotoneRoundOnHandoff: when ownership moves to a new pod, the new
// owner's lease round STRICTLY exceeds the prior owner's — the fencing-token
// invariant that lets FencedStore reject the deposed writer.
func TestCASFencerMonotoneRoundOnHandoff(t *testing.T) {
	cs := newFakeCondStore()
	ctx := context.Background()

	a := NewCASFencer(cs, stubView{id: "pod-a", set: []Member{{ID: "pod-a"}}})
	la, err := a.Acquire(ctx, "acme")
	if err != nil {
		t.Fatalf("A acquire: %v", err)
	}

	// Ownership moves to pod-b (its view names itself the sole owner).
	b := NewCASFencer(cs, stubView{id: "pod-b", set: []Member{{ID: "pod-b"}}})
	lb, err := b.Acquire(ctx, "acme")
	if err != nil {
		t.Fatalf("B acquire: %v", err)
	}
	if lb.Round <= la.Round {
		t.Fatalf("handoff round not monotone: A=%d B=%d (B must strictly exceed A)", la.Round, lb.Round)
	}
	if lb.Owner.ID != "pod-b" {
		t.Fatalf("lease owner after handoff = %s, want pod-b", lb.Owner.ID)
	}
}

// TestCASFencerConcurrentClaimsUniqueRoundPerOwner is the split-brain safety proof:
// two pods that BOTH believe they own the org (divergent views) race to claim. The
// CAS linearizes them, so they receive DISTINCT rounds (never two owners at one
// round), and the lease object converges to the highest round's owner — a single,
// monotone fencing token even under split-brain. Liveness (ping-pong) is the cost;
// safety (unique round per owner) is preserved.
func TestCASFencerConcurrentClaimsUniqueRoundPerOwner(t *testing.T) {
	cs := newFakeCondStore()
	a := NewCASFencer(cs, stubView{id: "pod-a", set: []Member{{ID: "pod-a"}}})
	b := NewCASFencer(cs, stubView{id: "pod-b", set: []Member{{ID: "pod-b"}}})

	var wg sync.WaitGroup
	var la, lb ha.Lease
	var ea, eb error
	wg.Add(2)
	go func() { defer wg.Done(); la, ea = a.Acquire(context.Background(), "acme") }()
	go func() { defer wg.Done(); lb, eb = b.Acquire(context.Background(), "acme") }()
	wg.Wait()

	if ea != nil || eb != nil {
		t.Fatalf("both claimers should eventually acquire: A=%v B=%v", ea, eb)
	}
	if la.Round == lb.Round {
		t.Fatalf("two owners at the SAME round %d — fencing token not unique", la.Round)
	}
}

var _ ha.Leases = (*CASFencer)(nil)

// TestAShortLeaseReadIsNeverAbsence is the safety property behind an error that
// looks like it wants softening.
//
// Production reads .owner and gets "unexpected EOF": Stat says the object is
// there, then fewer bytes arrive. The obvious fix is to treat that as an absent
// lease and carry on — and it is the one fix that must never be made. Absent means
// "nobody owns this org, claim round 1", so a short read reported as absence hands
// a second replica ownership of a database another replica already holds. Losing
// the read costs a 500; guessing it costs two writers on one prefix, which is the
// failure the fence exists to prevent.
//
// So: a truncated or empty lease must ERROR, and must not read as round 0.
func TestAShortLeaseReadIsNeverAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty object", []byte{}},
		{"header cut short", []byte("olse1")},
		{"magic but no round", []byte(leaseMagic)},
		{"round cut short", append([]byte(leaseMagic), 0, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeCondStore()
			st.objects[leaseKey("acme")] = &fakeSlot{data: tc.data, ver: 1}
			f := &CASFencer{store: st}

			round, owner, _, err := f.readLease(context.Background(), leaseKey("acme"))
			if err == nil {
				t.Fatalf("a %s read as round=%d owner=%q with no error; absence means "+
					"'claim round 1', so this would elect a second writer", tc.name, round, owner)
			}
			if round != 0 || owner != "" {
				t.Errorf("failed read still yielded round=%d owner=%q; it must yield nothing", round, owner)
			}
		})
	}
}

// TestAnAbsentLeaseIsRound0 is the other half: a key that genuinely does not exist
// IS absence, and the first owner claims round 1. Without this the pair above
// could be satisfied by refusing everything.
func TestAnAbsentLeaseIsRound0(t *testing.T) {
	f := &CASFencer{store: newFakeCondStore()}
	round, owner, version, err := f.readLease(context.Background(), leaseKey("acme"))
	if err != nil || round != 0 || owner != "" || version != "" {
		t.Fatalf("absent lease = (%d,%q,%q,%v); want (0,\"\",\"\",nil) so the first owner can claim", round, owner, version, err)
	}
}

// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// rollingupgrade_test.go is the zero-downtime PROOF: N pods each own a shard of orgs, and
// we ROLL them — drain a pod, let ownership transition to a live successor, rejoin it —
// while a client writes continuously to every org (always routing to the org's current
// elected owner, as the shard router does). It asserts the three properties a true
// zero-downtime rolling upgrade must hold:
//
//	(a) ZERO lost acked writes    — every write the store acknowledged is readable from the
//	                                durable object after all transitions (the successor
//	                                hydrated it).
//	(b) ZERO split-brain          — no (org, round) is ever acknowledged by two different
//	                                pods; a deposed owner's ship is fenced, never acked.
//	(c) Continuous availability   — every org is served by SOME ready owner at every step,
//	                                within a bounded re-route (the write always lands).
//
// It exercises BOTH takeover paths: a pod RESTART (fresh rejoin → hydrate from the durable
// object) and an in-place ownership FLAP with no restart (scale a pod in then out → the
// deposed store's next write is fenced, and the retry PROMOTES it live via M3). Sequential
// and deterministic (one client goroutine, membership changed at controlled points), the
// same fake-CAS + membership harness durable_test.go uses.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/hanzoai/vfs/replica"
)

// sharedSet is the converged live membership every pod reads — the atomic snapshot the
// production K8s source maintains, here mutated at controlled points to model a roll.
type sharedSet struct {
	mu  sync.Mutex
	set []Member
}

func (s *sharedSet) get() []Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Member(nil), s.set...)
}

func (s *sharedSet) put(ids ...string) {
	ms := make([]Member, len(ids))
	for i, id := range ids {
		ms[i] = Member{ID: id}
	}
	s.mu.Lock()
	s.set = ms
	s.mu.Unlock()
}

// podView is one pod's membership view: its own self over the shared converged set.
type podView struct {
	self   string
	shared *sharedSet
}

func (v podView) Self() string      { return v.self }
func (v podView) Members() []Member { return v.shared.get() }

type rollOrg struct {
	d  *Durable
	db *sql.DB // the bound handle, closed on quiesce/reopen
}

// rollPod is one replica: its own local data dir, a Durability over the shared object
// store + its own view, and the per-org stores it currently holds open.
type rollPod struct {
	id     string
	dir    string
	dy     *Durability
	stores map[string]*rollOrg
}

type rollHarness struct {
	t          *testing.T
	ctx        context.Context
	cs         replica.ConditionalStore
	shared     *sharedSet
	orgs       []string
	pods       map[string]*rollPod
	ackedSeq   map[string]int    // org → highest seq the store acknowledged
	roundOwner map[string]string // "org:round" → the ONE pod that acked at that round
}

const rerouteAttempts = 6 // bounded re-route: a transition resolves within this many tries.

func newRollHarness(t *testing.T, orgs []string) *rollHarness {
	return &rollHarness{
		t: t, ctx: context.Background(), cs: newFakeCondStore(), shared: &sharedSet{},
		orgs: orgs, pods: map[string]*rollPod{},
		ackedSeq: map[string]int{}, roundOwner: map[string]string{},
	}
}

// startPod (re)creates a pod process: a FRESH local dir + empty store set — modelling a
// restart, where the process re-hydrates each org from the durable object on open.
func (h *rollHarness) startPod(id string) {
	h.pods[id] = &rollPod{
		id: id, dir: h.t.TempDir(),
		dy:     NewDurability(h.cs, podView{self: id, shared: h.shared}, nil),
		stores: map[string]*rollOrg{},
	}
}

func (h *rollHarness) stopPod(id string) { delete(h.pods, id) }

func orgDBKey(org string) string { return replica.DBPath(org, "", "research") }
func (p *rollPod) orgPath(org string) string {
	return filepath.Join(p.dir, "orgs", org, "research.db")
}

// ensureOwned opens the org's store on first use, or PROMOTES it in place (M3) when it
// opened degraded and this pod is now the elected owner — TryClaim, quiesce the read-only
// handle, reopen as owner (Hydrate renews + CarryForward-restores). Exactly OrgStore.forPath
// + promote, driven through the same Durable primitives.
func (h *rollHarness) ensureOwned(p *rollPod, org string) *rollOrg {
	oo, ok := p.stores[org]
	if !ok {
		d := p.dy.For(org, orgDBKey(org), p.orgPath(org))
		_ = d.Hydrate(h.ctx)
		oo = &rollOrg{d: d, db: openBoundDB(h.t, d)}
		p.stores[org] = oo
		return oo
	}
	if !oo.d.Owned() && oo.d.PendingPromotion() {
		if claimed, _ := oo.d.TryClaim(h.ctx); claimed {
			_ = oo.db.Close() // quiesce before CarryForward swaps the file
			d := p.dy.For(org, orgDBKey(org), p.orgPath(org))
			_ = d.Hydrate(h.ctx)
			oo = &rollOrg{d: d, db: openBoundDB(h.t, d)}
			p.stores[org] = oo
		}
	}
	return oo
}

// write routes seq to the org's CURRENT elected owner and records the acknowledgement,
// retrying across a bounded re-route while ownership transitions. It FAILS the test if no
// owner can serve within the budget (availability) or if two pods ack one round (split
// brain).
func (h *rollHarness) write(org string, seq int) {
	for attempt := 0; attempt < rerouteAttempts; attempt++ {
		owner, ok := Owner(org, h.shared.get())
		if !ok {
			h.t.Fatalf("org %s: empty membership — no owner (availability)", org)
		}
		p := h.pods[owner.ID]
		if p == nil {
			h.t.Fatalf("org %s: elected owner %s is not a live pod", org, owner.ID)
		}
		oo := h.ensureOwned(p, org)
		if !oo.d.Owned() {
			continue // just opened degraded / promotion pending — retry drives it owned.
		}
		putKV(h.t, oo.db, "seq", strconv.Itoa(seq))
		acked, err := oo.d.Sync(h.ctx)
		if err != nil {
			h.t.Fatalf("org %s seq %d on %s: hard sync error: %v", org, seq, owner.ID, err)
		}
		if !acked {
			continue // deposed mid-write (fenced): retry re-routes / promotes.
		}
		h.recordAck(org, seq, owner.ID, uint64(oo.d.lease.Round))
		return
	}
	h.t.Fatalf("AVAILABILITY VIOLATED: org %s seq %d not served within %d attempts", org, seq, rerouteAttempts)
}

// recordAck registers a durable acknowledgement and checks the split-brain invariant: a
// given (org, round) belongs to exactly ONE pod, because a handoff strictly bumps the
// round and the fence rejects a deposed writer — so two distinct pods acking one round
// would mean two live writers.
func (h *rollHarness) recordAck(org string, seq int, podID string, round uint64) {
	key := org + ":" + strconv.FormatUint(round, 10)
	if prev, ok := h.roundOwner[key]; ok && prev != podID {
		h.t.Fatalf("SPLIT-BRAIN: org %s round %d acked by BOTH %s and %s", org, round, prev, podID)
	}
	h.roundOwner[key] = podID
	if seq > h.ackedSeq[org] {
		h.ackedSeq[org] = seq
	}
}

// writeAll writes the next seq to every org (a full sweep of continuous client traffic).
func (h *rollHarness) writeAll(seq *int) {
	for _, org := range h.orgs {
		*seq++
		h.write(org, *seq)
	}
}

// verifyNoLostWrites opens each org's durable object fresh (the exact path a brand-new
// successor takes) and asserts it carries at least the highest seq the store ever acked —
// no acknowledged write was lost across any transition.
func (h *rollHarness) verifyNoLostWrites() {
	for _, org := range h.orgs {
		want := h.ackedSeq[org]
		if want == 0 {
			continue
		}
		v, ok := durableValue(h.t, h.ctx, h.cs, orgDBKey(org), "seq")
		if !ok {
			h.t.Fatalf("org %s: durable object has no state, want acked seq %d (LOST)", org, want)
		}
		got, _ := strconv.Atoi(v)
		if got < want {
			h.t.Fatalf("org %s: durable seq %d < highest acked %d — LOST ACKED WRITE", org, got, want)
		}
	}
}

// TestRollingUpgradeZeroDowntime is the end-to-end proof over 4 orgs and 3 pods.
func TestRollingUpgradeZeroDowntime(t *testing.T) {
	orgs := []string{"acme", "globex", "initech", "umbrella", "hooli", "piedpiper", "wonka", "stark"}
	h := newRollHarness(t, orgs)
	for _, id := range []string{"cloud-0", "cloud-1", "cloud-2"} {
		h.startPod(id)
	}
	h.shared.put("cloud-0", "cloud-1", "cloud-2")
	seq := 0

	// Warm: every org served by its initial owner.
	h.writeAll(&seq)

	// Phase A — rolling RESTART of each pod: drain it (leaves the set → its orgs re-home to
	// live successors that hydrate), write continuously, then rejoin it FRESH (a restart:
	// new local dir → re-hydrate from the durable object). Zero request lost throughout.
	for _, rolling := range []string{"cloud-0", "cloud-1", "cloud-2"} {
		// Drain.
		remaining := without([]string{"cloud-0", "cloud-1", "cloud-2"}, rolling)
		h.shared.put(remaining...)
		h.stopPod(rolling)
		h.writeAll(&seq) // successors take over; every org still served.
		h.writeAll(&seq)

		// Rejoin (restart): fresh process, rehydrates the orgs HRW hands back to it.
		h.startPod(rolling)
		h.shared.put("cloud-0", "cloud-1", "cloud-2")
		h.writeAll(&seq)
		h.writeAll(&seq)
	}

	// Phase B — in-place ownership FLAP, NO restart (exercises M3): scale a 4th pod IN
	// (steals + fences some orgs from the running 0/1/2), write, then scale it OUT. The
	// orgs return to their original still-running owners, whose cached stores are now at a
	// stale round — the next write is fenced and the retry PROMOTES them live (no restart).
	h.startPod("cloud-3")
	h.shared.put("cloud-0", "cloud-1", "cloud-2", "cloud-3")
	// The flap only exercises in-place promotion if cloud-3 actually steals (and fences) an
	// org from a still-running 0/1/2 — assert it does, so the test never silently degenerates.
	stolen := 0
	for _, org := range orgs {
		if o, _ := Owner(org, h.shared.get()); o.ID == "cloud-3" {
			stolen++
		}
	}
	if stolen == 0 {
		t.Fatal("phase B degenerate: cloud-3 stole no org (widen the org set)")
	}
	h.writeAll(&seq)
	h.writeAll(&seq)
	h.stopPod("cloud-3")
	h.shared.put("cloud-0", "cloud-1", "cloud-2")
	h.writeAll(&seq) // returning orgs: fenced-then-promoted in place on the original owners.
	h.writeAll(&seq)

	// (a) No acked write lost anywhere. ((b) split-brain and (c) availability were asserted
	// continuously inside write().)
	h.verifyNoLostWrites()

	// Sanity: the proof actually drove ownership across pods (not a degenerate single-owner
	// run) — more than one distinct (org,round)→pod binding was recorded.
	distinct := map[string]bool{}
	for _, pod := range h.roundOwner {
		distinct[pod] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("proof did not exercise a handoff: only %d pod(s) ever owned a round", len(distinct))
	}
}

func without(all []string, drop string) []string {
	out := make([]string, 0, len(all))
	for _, s := range all {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

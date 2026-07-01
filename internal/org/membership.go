package org

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Source yields the current live replica set. It is the ONE thing in the
// horizontally-scaled cloud that needs a peer view — everything else is a pure
// function of (org, members). Any discovery mechanism plugs in as a Source
// without touching the ownership core: a static list (dev), a K8s Endpoints
// poll (prod), or a zapd gossip view.
type Source func(context.Context) ([]Member, error)

// Membership caches the latest replica snapshot and refreshes it from a Source
// on an interval. Reads are lock-free via an atomic snapshot pointer, so the
// per-request AmOwner hot-path never blocks on the refresher.
type Membership struct {
	src      Source
	interval time.Duration
	selfID   string
	snap     atomic.Pointer[[]Member]

	stopOnce sync.Once
	stop     chan struct{}
}

// NewMembership builds a Membership over src. selfID is this replica's stable id
// (see Member.ID). interval<=0 defaults to 5s.
func NewMembership(selfID string, src Source, interval time.Duration) *Membership {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	m := &Membership{src: src, interval: interval, selfID: selfID, stop: make(chan struct{})}
	empty := []Member{}
	m.snap.Store(&empty)
	return m
}

// Start does an initial synchronous refresh (so Members() is populated before
// the first request) then refreshes on the interval until Stop or ctx cancel.
// Returns the initial refresh error, if any; callers may serve with a stale or
// self-only set regardless.
func (m *Membership) Start(ctx context.Context) error {
	err := m.refresh(ctx)
	go m.loop(ctx)
	return err
}

func (m *Membership) loop(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-t.C:
			_ = m.refresh(ctx)
		}
	}
}

func (m *Membership) refresh(ctx context.Context) error {
	ms, err := m.src(ctx)
	if err != nil {
		return err // keep the last good snapshot — never flap to empty
	}
	m.snap.Store(normalize(ms))
	return nil
}

// Stop halts the refresh loop. Idempotent.
func (m *Membership) Stop() { m.stopOnce.Do(func() { close(m.stop) }) }

// Members returns the current membership snapshot (never nil; may be empty).
func (m *Membership) Members() []Member { return *m.snap.Load() }

// Self is this replica's id.
func (m *Membership) Self() string { return m.selfID }

// OwnerOf resolves the writer-owner replica for orgID under the live set.
func (m *Membership) OwnerOf(orgID string) (Member, bool) { return Owner(orgID, m.Members()) }

// AmOwner reports whether THIS replica owns orgID's writer right now — the
// per-write hot-path check. Lock-free.
func (m *Membership) AmOwner(orgID string) bool { return IsOwner(orgID, m.selfID, m.Members()) }

// normalize dedupes by ID and sorts by ID so the snapshot is canonical; a
// canonical set keeps HRW answers identical across replicas that observed the
// same members in different order.
func normalize(ms []Member) *[]Member {
	seen := make(map[string]Member, len(ms))
	for _, x := range ms {
		if x.ID != "" {
			seen[x.ID] = x
		}
	}
	out := make([]Member, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return &out
}

// StaticSource yields a fixed set — single-node / local dev. With no explicit
// members it reads CLOUD_REPLICAS ("id@addr,id2@addr2", or bare "id" with
// addr==id).
func StaticSource(members ...Member) Source {
	if len(members) == 0 {
		members = parseReplicasEnv(os.Getenv("CLOUD_REPLICAS"))
	}
	fixed := *normalize(members)
	return func(context.Context) ([]Member, error) { return fixed, nil }
}

func parseReplicasEnv(v string) []Member {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	var out []Member
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, addr, ok := strings.Cut(part, "@")
		if !ok {
			id, addr = part, part
		}
		out = append(out, Member{ID: strings.TrimSpace(id), Addr: strings.TrimSpace(addr)})
	}
	return out
}

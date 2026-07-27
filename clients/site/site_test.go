package site

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/cd"
)

type memStore struct {
	prefixes map[string]bool
	copies   int
	failCopy error
}

func newStore() *memStore { return &memStore{prefixes: map[string]bool{}} }

func (m *memStore) Copy(_ context.Context, _, dst string) (int, int64, error) {
	if m.failCopy != nil {
		return 0, 0, m.failCopy
	}
	m.copies++
	m.prefixes[dst] = true
	return 3, 1024, nil
}
func (m *memStore) Exists(_ context.Context, p string) (bool, error) { return m.prefixes[p], nil }
func (m *memStore) Prune(_ context.Context, p string) error          { delete(m.prefixes, p); return nil }

type memRouter struct {
	bound   map[string]string
	purges  int
	failPtr error
}

func newRouter() *memRouter { return &memRouter{bound: map[string]string{}} }

func (r *memRouter) Point(_ context.Context, host, prefix string) error {
	if r.failPtr != nil {
		return r.failPtr
	}
	r.bound[host] = prefix
	return nil
}
func (r *memRouter) Resolve(_ context.Context, host string) (string, error) { return r.bound[host], nil }
func (r *memRouter) Purge(_ context.Context, host string) error             { r.purges++; return nil }

func origin(s *memStore, r *memRouter) *Origin {
	return &Origin{Slug: "bitcoin", Org: "lux", Host: "bitcoin.lux.network",
		Bucket: "hanzo-sites", Keep: 2, Store: s, Router: r}
}

func rel(v int64) cd.Release {
	return cd.Release{ID: "rel", Target: "lux/bitcoin", Version: v,
		Artifact: cd.Artifact{Kind: cd.KindBundle, Ref: "s3://build/x"}}
}

// Each release gets its OWN prefix. This is the property that makes rollback
// possible at all: the previous bytes must still exist.
func TestEachReleaseGetsItsOwnPrefix(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	ctx := context.Background()

	p1, err := o.Place(ctx, rel(1))
	if err != nil {
		t.Fatalf("Place v1: %v", err)
	}
	p2, err := o.Place(ctx, rel(2))
	if err != nil {
		t.Fatalf("Place v2: %v", err)
	}
	if p1.ID == p2.ID {
		t.Fatalf("both releases landed at %q — an overwrite makes rollback impossible", p1.ID)
	}
	for _, p := range []cd.Placement{p1, p2} {
		if ok, _ := s.Exists(ctx, p.ID); !ok {
			t.Errorf("prefix %q missing after Place", p.ID)
		}
	}
}

// Place must not go live. Uploading and activating are separate so a failed
// upload cannot affect what is currently served.
func TestPlaceDoesNotGoLive(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	if _, err := o.Place(context.Background(), rel(1)); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if got := r.bound[o.Host]; got != "" {
		t.Errorf("host bound to %q by Place alone; only Activate may move the pointer", got)
	}
}

// A retried rollout must reuse the prefix, not re-upload it.
func TestPlaceIsIdempotent(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	ctx := context.Background()

	if _, err := o.Place(ctx, rel(1)); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if _, err := o.Place(ctx, rel(1)); err != nil {
		t.Fatalf("Place retry: %v", err)
	}
	if s.copies != 1 {
		t.Errorf("Copy ran %d times for one release; a retry must reuse the prefix", s.copies)
	}
}

// THE TRAP: rolling back to a placement whose bytes were pruned would point a
// live host at an empty prefix and serve 404s to real traffic — during an
// incident, which is exactly when rollback is used. Refuse instead.
func TestActivateRefusesMissingBundle(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	ctx := context.Background()

	p, _ := o.Place(ctx, rel(1))
	if err := o.Activate(ctx, p); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	_ = s.Prune(ctx, p.ID) // bytes gone

	if err := o.Activate(ctx, p); !errors.Is(err, ErrNotReady) {
		t.Errorf("Activate(pruned) = %v, want ErrNotReady — never point a live host at nothing", err)
	}
}

func TestActivatePointsHostAndPurges(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	ctx := context.Background()

	p, _ := o.Place(ctx, rel(7))
	if err := o.Activate(ctx, p); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if r.bound[o.Host] != p.ID {
		t.Errorf("host -> %q, want %q", r.bound[o.Host], p.ID)
	}
	if !strings.Contains(p.ID, "v7") {
		t.Errorf("prefix %q should carry the version so a human can order the rollback menu", p.ID)
	}
	if r.purges == 0 {
		t.Error("edge not purged; a stale cache would mask the flip")
	}
}

// Retention is a storage policy and must never delete what is serving.
func TestSweepNeverPrunesTheActiveRelease(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	ctx := context.Background()

	var ps []cd.Placement
	for v := int64(1); v <= 4; v++ {
		p, _ := o.Place(ctx, rel(v))
		ps = append([]cd.Placement{p}, ps...) // newest first
	}
	oldest := ps[len(ps)-1]
	if err := o.Activate(ctx, oldest); err != nil { // deliberately live on an OLD one
		t.Fatalf("Activate: %v", err)
	}
	if err := o.Sweep(ctx, ps); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if ok, _ := s.Exists(ctx, oldest.ID); !ok {
		t.Error("Sweep pruned the ACTIVE release — retention must never delete what is serving")
	}
}

func TestNoHostIsAnError(t *testing.T) {
	s, r := newStore(), newRouter()
	o := origin(s, r)
	o.Host = ""
	p, _ := o.Place(context.Background(), rel(1))
	if err := o.Activate(context.Background(), p); !errors.Is(err, ErrNoHost) {
		t.Errorf("Activate(no host) = %v, want ErrNoHost", err)
	}
}

// It must satisfy the lifecycle contract, or none of the above matters.
func TestOriginIsATarget(t *testing.T) {
	var _ cd.Target = (*Origin)(nil)
}

package org

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A static source populates members; OwnerOf resolves over the snapshot; AmOwner
// agrees with OwnerOf for self.
func TestMembershipStaticAndOwnership(t *testing.T) {
	src := StaticSource(
		Member{ID: "r1", Addr: "r1:9653"},
		Member{ID: "r2", Addr: "r2:9653"},
		Member{ID: "r3", Addr: "r3:9653"},
	)
	m := NewMembership("r2", src, 0)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()

	if len(m.Members()) != 3 {
		t.Fatalf("want 3 members, got %d", len(m.Members()))
	}
	for _, org := range []string{"a", "b", "hanzo", "acme"} {
		o, ok := m.OwnerOf(org)
		if !ok {
			t.Fatalf("org %q: no owner", org)
		}
		if m.AmOwner(org) != (o.ID == "r2") {
			t.Fatalf("org %q: AmOwner disagrees with OwnerOf (%s)", org, o.ID)
		}
	}
}

// The snapshot is canonical regardless of source order — two memberships from
// the same set in different orders agree on every owner.
func TestMembershipCanonicalRegardlessOfSourceOrder(t *testing.T) {
	a := NewMembership("x", StaticSource(Member{ID: "r3"}, Member{ID: "r1"}, Member{ID: "r2"}), 0)
	b := NewMembership("x", StaticSource(Member{ID: "r1"}, Member{ID: "r2"}, Member{ID: "r3"}), 0)
	_ = a.Start(context.Background())
	_ = b.Start(context.Background())
	defer a.Stop()
	defer b.Stop()
	for _, org := range []string{"o1", "o2", "o3", "hanzo"} {
		oa, _ := a.OwnerOf(org)
		ob, _ := b.OwnerOf(org)
		if oa.ID != ob.ID {
			t.Fatalf("org %q: order-dependent owner %s vs %s", org, oa.ID, ob.ID)
		}
	}
}

// A source error keeps the last good snapshot (no flapping to empty).
func TestMembershipKeepsLastGoodOnError(t *testing.T) {
	var fail atomic.Bool
	src := func(context.Context) ([]Member, error) {
		if fail.Load() {
			return nil, errString("source down")
		}
		return []Member{{ID: "r1"}, {ID: "r2"}}, nil
	}
	m := NewMembership("r1", src, 10*time.Millisecond)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()
	if len(m.Members()) != 2 {
		t.Fatalf("want 2, got %d", len(m.Members()))
	}
	fail.Store(true)
	time.Sleep(40 * time.Millisecond) // let failing refreshes run
	if len(m.Members()) != 2 {
		t.Fatalf("source error must retain last good set; got %d", len(m.Members()))
	}
}

// CLOUD_REPLICAS env parsing: "id@addr" and bare "id" forms.
func TestParseReplicasEnv(t *testing.T) {
	ms := parseReplicasEnv("r1@10.0.0.1:9653, r2 , r3@10.0.0.3:9653")
	if len(ms) != 3 {
		t.Fatalf("want 3, got %d", len(ms))
	}
	if ms[0].ID != "r1" || ms[0].Addr != "10.0.0.1:9653" {
		t.Fatalf("r1 parse: %+v", ms[0])
	}
	if ms[1].ID != "r2" || ms[1].Addr != "r2" { // bare id → addr==id
		t.Fatalf("bare id parse: %+v", ms[1])
	}
	if got := parseReplicasEnv(""); got != nil {
		t.Fatalf("empty env → nil, got %+v", got)
	}
}

// Empty membership → no owner (fail-closed; never a wrong writer).
func TestMembershipEmptyNoOwner(t *testing.T) {
	m := NewMembership("r1", StaticSource(), 0)
	_ = m.Start(context.Background())
	defer m.Stop()
	if _, ok := m.OwnerOf("o"); ok {
		t.Fatal("empty membership must have no owner")
	}
	if m.AmOwner("o") {
		t.Fatal("AmOwner must be false with no members")
	}
}

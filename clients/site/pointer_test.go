package site

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/cd"
)

type memObj struct {
	objs map[string][]byte
	puts int
}

func newObj() *memObj { return &memObj{objs: map[string][]byte{}} }

func (m *memObj) GetObject(_ context.Context, k string) ([]byte, error) {
	b, ok := m.objs[k]
	if !ok {
		return nil, errors.New("no such key")
	}
	return b, nil
}
func (m *memObj) PutObject(_ context.Context, k string, b []byte) error {
	m.puts++
	m.objs[k] = b
	return nil
}

// A site that has never been activated is a normal state, not an error — a
// project exists before its first deploy.
func TestUnactivatedSiteResolvesEmpty(t *testing.T) {
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: newObj()}
	got, err := r.Resolve(context.Background(), "bitcoin.lux.network")
	if err != nil {
		t.Fatalf("Resolve() = %v, want nil for a never-activated site", err)
	}
	if got != "" {
		t.Errorf("Resolve() = %q, want empty", got)
	}
}

// Going live is exactly one PUT, so readers see the old prefix or the new one and
// never a half-swapped state.
func TestPointIsOneAtomicWrite(t *testing.T) {
	o := newObj()
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: o}
	ctx := context.Background()

	if err := r.Point(ctx, "bitcoin.lux.network", "lux/bitcoin/v3/"); err != nil {
		t.Fatalf("Point() = %v", err)
	}
	if o.puts != 1 {
		t.Errorf("Point wrote %d objects, want exactly 1 (atomicity depends on it)", o.puts)
	}
	got, _ := r.Resolve(ctx, "bitcoin.lux.network")
	if got != "lux/bitcoin/v3/" {
		t.Errorf("Resolve() = %q, want the prefix just pointed at", got)
	}
}

// The pointer lives beside the bundles it names, so "what is live" is answerable
// from the same store — and readable with a plain object GET.
func TestPointerLivesBesideTheBundles(t *testing.T) {
	o := newObj()
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: o}
	_ = r.Point(context.Background(), "h", "lux/bitcoin/v1/")
	if _, ok := o.objs["lux/bitcoin/CURRENT"]; !ok {
		t.Errorf("pointer not at lux/bitcoin/CURRENT; keys=%v", keys(o.objs))
	}
}

// Repointing overwrites rather than accumulating — there is one live version.
func TestRepointReplaces(t *testing.T) {
	o := newObj()
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: o}
	ctx := context.Background()
	_ = r.Point(ctx, "h", "lux/bitcoin/v1/")
	_ = r.Point(ctx, "h", "lux/bitcoin/v2/")
	got, _ := r.Resolve(ctx, "h")
	if got != "lux/bitcoin/v2/" {
		t.Errorf("Resolve() = %q, want v2", got)
	}
	if len(o.objs) != 1 {
		t.Errorf("%d pointer objects, want 1 — there is one live version", len(o.objs))
	}
}

// Never point a host at nothing; an empty prefix would serve 404s to live traffic.
func TestPointRejectsEmptyPrefix(t *testing.T) {
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: newObj()}
	if err := r.Point(context.Background(), "h", "  "); err == nil {
		t.Error("Point(empty) = nil, want an error")
	}
}

// A missing edge is not a failure: purging is best-effort by contract.
func TestPurgeWithoutEdgeIsNotAnError(t *testing.T) {
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: newObj()}
	if err := r.Purge(context.Background(), "h"); err != nil {
		t.Errorf("Purge() = %v, want nil when no edge is configured", err)
	}
}

// End to end on the real types: deploy twice, roll back, and confirm the pointer
// followed — with no re-upload.
func TestRollbackMovesThePointerWithoutReuploading(t *testing.T) {
	st, obj := newStore(), newObj()
	r := PointerRouter{Org: "lux", Slug: "bitcoin", Obj: obj}
	o := &Origin{Slug: "bitcoin", Org: "lux", Host: "bitcoin.lux.network",
		Store: st, Router: r, Keep: 5}
	ctx := context.Background()

	p1, _ := o.Place(ctx, rel(1))
	_ = o.Activate(ctx, p1)
	p2, _ := o.Place(ctx, rel(2))
	_ = o.Activate(ctx, p2)
	uploads := st.copies

	if err := o.Activate(ctx, p1); err != nil { // rollback == activate an older placement
		t.Fatalf("rollback: %v", err)
	}
	if st.copies != uploads {
		t.Errorf("rollback re-uploaded (%d -> %d); it must only move the pointer", uploads, st.copies)
	}
	live, _ := r.Resolve(ctx, o.Host)
	if live != p1.ID {
		t.Errorf("live = %q after rollback, want %q", live, p1.ID)
	}
}

var _ Router = PointerRouter{}
var _ cd.Target = (*Origin)(nil)

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

package cd

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The properties worth testing here are the ones the old five pipelines each got
// wrong in a different way:
//
//  1. a failed Place must not disturb what is live  (a broken build cannot take
//     production down — it can only fail to replace it)
//  2. rollback must not rebuild                     (recovery is a pointer move)
//  3. Place must be idempotent                      (a retried rollout is safe)
//  4. Activate happens only after Place succeeds    (never a half-swapped state)

type fakeTarget struct {
	kind      Kind
	placed    []string // release IDs, in order
	activated []string // placement IDs, in order
	failPlace error
	live      string
}

func (f *fakeTarget) Name() string { return "fake" }
func (f *fakeTarget) Kind() Kind   { return f.kind }

func (f *fakeTarget) Place(_ context.Context, r Release) (Placement, error) {
	if f.failPlace != nil {
		return Placement{}, f.failPlace
	}
	id := fmt.Sprintf("p-%d", r.Version)
	for _, seen := range f.placed { // idempotence: same release, same placement
		if seen == r.ID {
			return Placement{ID: id, ReleaseID: r.ID, Target: f.Name()}, nil
		}
	}
	f.placed = append(f.placed, r.ID)
	return Placement{ID: id, ReleaseID: r.ID, Target: f.Name()}, nil
}

func (f *fakeTarget) Activate(_ context.Context, p Placement) error {
	f.activated = append(f.activated, p.ID)
	f.live = p.ID
	return nil
}

func (f *fakeTarget) Status(context.Context) (State, error) {
	return State{Healthy: true, Active: f.live}, nil
}

type reg map[string]Target

func (r reg) Target(n string) (Target, bool) { t, ok := r[n]; return t, ok }

type memStore struct {
	ver        int64
	releases   map[string]Release
	placements []Placement
	active     string
}

func newStore() *memStore { return &memStore{releases: map[string]Release{}} }

func (m *memStore) NextVersion(context.Context, string) (int64, error) { m.ver++; return m.ver, nil }
func (m *memStore) PutRelease(_ context.Context, r Release) error      { m.releases[r.ID] = r; return nil }
func (m *memStore) PutPlacement(_ context.Context, p Placement) error {
	m.placements = append([]Placement{p}, m.placements...) // newest first
	return nil
}
func (m *memStore) SetActive(_ context.Context, _, id string) error { m.active = id; return nil }
func (m *memStore) Placements(context.Context, string) ([]Placement, error) {
	return m.placements, nil
}
func (m *memStore) Release(_ context.Context, id string) (Release, error) { return m.releases[id], nil }

func engine(t Target, s *memStore) Engine {
	n := 0
	return Engine{
		Reg: reg{"fake": t}, Store: s,
		NewID: func(p string) (string, error) { n++; return fmt.Sprintf("%s-%d", p, n), nil },
	}
}

func bundle(ref string) Artifact {
	return Artifact{Kind: KindBundle, Ref: ref, Digest: "sha256:" + ref}
}

// A failed Place must leave the live placement untouched. This is the property
// that lets a broken build be a non-event instead of an outage.
func TestFailedPlaceDoesNotDisturbLive(t *testing.T) {
	ft := &fakeTarget{kind: KindBundle}
	st := newStore()
	e := engine(ft, st)
	ctx := context.Background()

	if _, err := e.Deploy(ctx, "fake", bundle("good"), nil, "c1"); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	wasLive := ft.live

	ft.failPlace = errors.New("build artifact corrupt")
	if _, err := e.Deploy(ctx, "fake", bundle("bad"), nil, "c2"); err == nil {
		t.Fatal("Deploy() = nil, want error when Place fails")
	}
	if ft.live != wasLive {
		t.Errorf("live moved to %q after a failed Place; want it pinned at %q", ft.live, wasLive)
	}
	if len(ft.activated) != 1 {
		t.Errorf("Activate called %d times; a failed Place must never activate", len(ft.activated))
	}
}

// Rollback re-activates an existing placement. It must not place again — no
// rebuild, no re-upload — because that is the whole reason recovery is fast.
func TestRollbackActivatesWithoutPlacing(t *testing.T) {
	ft := &fakeTarget{kind: KindBundle}
	st := newStore()
	e := engine(ft, st)
	ctx := context.Background()

	first, _ := e.Deploy(ctx, "fake", bundle("v1"), nil, "c1")
	if _, err := e.Deploy(ctx, "fake", bundle("v2"), nil, "c2"); err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	placesBefore := len(ft.placed)

	if err := e.Rollback(ctx, "fake", first.ID); err != nil {
		t.Fatalf("Rollback() = %v", err)
	}
	if len(ft.placed) != placesBefore {
		t.Errorf("Rollback placed again (%d -> %d); recovery must be a pointer move", placesBefore, len(ft.placed))
	}
	if ft.live != first.ID {
		t.Errorf("live = %q after rollback, want %q", ft.live, first.ID)
	}
	if st.active != first.ID {
		t.Errorf("store active = %q, want %q — records must follow the pointer", st.active, first.ID)
	}
}

// Rolling back to something never placed must fail loudly rather than point a
// live host at nothing.
func TestRollbackToUnknownPlacementFails(t *testing.T) {
	e := engine(&fakeTarget{kind: KindBundle}, newStore())
	if err := e.Rollback(context.Background(), "fake", "p-nope"); !errors.Is(err, ErrNotPlaced) {
		t.Errorf("Rollback(unknown) = %v, want ErrNotPlaced", err)
	}
}

// A target only accepts artifacts of its own kind; an image can never be placed
// on an origin. Caught before any work, so a mismatch is never half-applied.
func TestKindMismatchRejected(t *testing.T) {
	e := engine(&fakeTarget{kind: KindBundle}, newStore())
	_, err := e.Deploy(context.Background(), "fake", Artifact{Kind: KindImage, Ref: "img"}, nil, "c")
	if err == nil {
		t.Fatal("Deploy() = nil, want error placing an image on a bundle target")
	}
}

func TestUnknownTargetRejected(t *testing.T) {
	e := engine(&fakeTarget{kind: KindBundle}, newStore())
	_, err := e.Deploy(context.Background(), "ghost", bundle("v1"), nil, "c")
	if !errors.Is(err, ErrUnknownTarget) {
		t.Errorf("Deploy(unknown target) = %v, want ErrUnknownTarget", err)
	}
}

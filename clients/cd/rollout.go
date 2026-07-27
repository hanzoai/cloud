package cd

import (
	"context"
	"errors"
	"fmt"
)

// Rollout is the whole of continuous delivery, and it is two calls.
//
// Deploy and Rollback are the same function with a different Placement, which is
// not a trick — it is the consequence of Releases being immutable. There is no
// third code path that only runs during an incident and is therefore only tested
// during an incident.
var (
	ErrUnknownTarget = errors.New("cd: unknown target")
	ErrNotPlaced     = errors.New("cd: release was never placed on this target")
)

// Store persists the lifecycle. It is an interface because cd should not care
// whether that is SQLite in the unified binary or something else later; the
// lifecycle is the thing worth owning, storage is not.
type Store interface {
	NextVersion(ctx context.Context, target string) (int64, error)
	PutRelease(ctx context.Context, r Release) error
	PutPlacement(ctx context.Context, p Placement) error
	SetActive(ctx context.Context, target, placementID string) error
	Placements(ctx context.Context, target string) ([]Placement, error) // newest first
	Release(ctx context.Context, id string) (Release, error)
}

// Engine wires a Registry to a Store. It holds no state of its own.
type Engine struct {
	Reg   Registry
	Store Store
	NewID func(prefix string) (string, error)
}

// Deploy places an Artifact on a Target and makes it live.
//
// Order matters and is not negotiable: Place fully materialises the new release
// BEFORE Activate flips the pointer. A failed Place leaves the currently active
// placement untouched, so a broken build can never take production down — it can
// only fail to replace it. That is the same reason publishSite uploads before it
// flips, generalised to every kind.
func (e Engine) Deploy(ctx context.Context, target string, a Artifact, cfg map[string]string, commit string) (Placement, error) {
	t, ok := e.Reg.Target(target)
	if !ok {
		return Placement{}, fmt.Errorf("%w: %q", ErrUnknownTarget, target)
	}
	if a.Kind != t.Kind() {
		return Placement{}, fmt.Errorf("cd: target %q takes %s artifacts, got %s", target, t.Kind(), a.Kind)
	}

	v, err := e.Store.NextVersion(ctx, target)
	if err != nil {
		return Placement{}, fmt.Errorf("version: %w", err)
	}
	id, err := e.NewID("rel")
	if err != nil {
		return Placement{}, err
	}
	r := Release{ID: id, Target: target, Version: v, Artifact: a, Config: cfg, Commit: commit}
	if err := e.Store.PutRelease(ctx, r); err != nil {
		return Placement{}, fmt.Errorf("persist release: %w", err)
	}

	p, err := t.Place(ctx, r)
	if err != nil {
		// Nothing was activated, so nothing regressed. Surface it honestly.
		return Placement{}, fmt.Errorf("place: %w", err)
	}
	if err := e.Store.PutPlacement(ctx, p); err != nil {
		return Placement{}, fmt.Errorf("persist placement: %w", err)
	}
	return p, e.activate(ctx, t, p)
}

// Rollback activates an EARLIER placement. It re-runs no build and re-uploads no
// bytes: the artifact is still there because releases are immutable, so recovery
// costs one pointer flip. Roll forward again by activating a newer placement —
// the menu is symmetric.
func (e Engine) Rollback(ctx context.Context, target, placementID string) error {
	t, ok := e.Reg.Target(target)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTarget, target)
	}
	ps, err := e.Store.Placements(ctx, target)
	if err != nil {
		return fmt.Errorf("placements: %w", err)
	}
	for _, p := range ps {
		if p.ID == placementID {
			return e.activate(ctx, t, p)
		}
	}
	return fmt.Errorf("%w: %s", ErrNotPlaced, placementID)
}

// activate is the one place the live pointer moves — for every kind, for both
// deploy and rollback. One writer, so "what is live" has exactly one answer.
func (e Engine) activate(ctx context.Context, t Target, p Placement) error {
	if err := t.Activate(ctx, p); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	if err := e.Store.SetActive(ctx, t.Name(), p.ID); err != nil {
		return fmt.Errorf("record active: %w", err)
	}
	return nil
}

// History is the rollback menu, newest first.
func (e Engine) History(ctx context.Context, target string) ([]Placement, error) {
	if _, ok := e.Reg.Target(target); !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTarget, target)
	}
	return e.Store.Placements(ctx, target)
}

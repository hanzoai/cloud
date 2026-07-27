// Package site serves static bundles. It is one implementation of cd.Target — the
// origin kind — and it is the only place that knows what an object prefix, a host
// binding, or an edge cache is.
//
// It knows nothing about git, CI, versions, or when to deploy. Those are cd's, and
// keeping them out of here is the entire reason a site can now be rolled back: the
// lifecycle lives in one place for every kind, so it only had to be made correct
// once.
//
// WHAT CHANGED, AND WHY IT MATTERS
//
// The previous implementation (cloud/clients/projects publishSite) uploaded every
// deploy to the SAME prefix, "<org>/<slug>/". That single decision is what made a
// static site unrollbackable: the new bytes destroyed the old ones, so "go back to
// the version that worked" had no bytes to go back to. Recovery meant rebuilding
// from source and hoping the source still built — during an incident.
//
// Here a bundle lands at "<org>/<slug>/<release>/" and a pointer names the live
// one. Uploads never overwrite; going live is a pointer move. Rollback stops being
// a rebuild and becomes a pointer move too — the same pointer move, which is why
// it is exercised on every deploy instead of only during incidents.
//
// The cost is storage for old releases, bounded by Keep. That is the correct
// trade: object storage is cheap and an outage is not.
package site

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/hanzoai/cloud/clients/cd"
)

// Store is the object store a bundle lives in. Narrow on purpose: cd's contract
// needs exactly these, and a wider interface would let site-specific storage
// concerns leak back into the lifecycle.
type Store interface {
	// Copy materialises a bundle at dst. Implementations must be idempotent so a
	// retried rollout re-uses the prefix rather than duplicating bytes.
	Copy(ctx context.Context, srcRef, dst string) (files int, bytes int64, err error)
	// Exists reports whether a prefix holds a complete bundle.
	Exists(ctx context.Context, prefix string) (bool, error)
	// Prune removes a prefix once it falls out of the rollback window.
	Prune(ctx context.Context, prefix string) error
}

// Router binds a host to a prefix. This is the live pointer, and it is the ONLY
// mutable thing in this package — everything else is append-only.
//
// Point must be atomic from a reader's perspective: a request resolves to the old
// prefix or the new one, never to a half-written binding. Without that, "activate"
// would have a window where a site serves a mix of two releases, which is exactly
// the failure mode versioned prefixes exist to remove.
type Router interface {
	Point(ctx context.Context, host, prefix string) error
	Resolve(ctx context.Context, host string) (prefix string, err error)
	Purge(ctx context.Context, host string) error // edge invalidation; best-effort
}

var (
	ErrNoHost   = errors.New("site: no host bound")
	ErrNotReady = errors.New("site: bundle missing at prefix")
)

// Origin is a static site: a bundle store, a host, and the pointer between them.
// It satisfies cd.Target.
type Origin struct {
	Slug   string
	Org    string
	Host   string // the public name, e.g. "bitcoin.lux.network"
	Bucket string
	Keep   int // releases retained for rollback; <=0 means keep all

	Store  Store
	Router Router
}

func (o *Origin) Name() string { return o.Org + "/" + o.Slug }
func (o *Origin) Kind() cd.Kind { return cd.KindBundle }

// prefix is where one release's bytes live. Release version is enough to be
// unique per target and, unlike a digest, it reads in order in a bucket listing —
// which matters when a human is picking a rollback target under pressure.
func (o *Origin) prefix(r cd.Release) string {
	return path.Join(o.Org, o.Slug, fmt.Sprintf("v%d", r.Version)) + "/"
}

// Place uploads the bundle to its own prefix. It does NOT go live — that is
// Activate's job, and keeping them separate is what makes a failed upload
// harmless: the currently live release is never touched here.
func (o *Origin) Place(ctx context.Context, r cd.Release) (cd.Placement, error) {
	if r.Kind != cd.KindBundle {
		return cd.Placement{}, fmt.Errorf("site: %s takes bundles, got %s", o.Name(), r.Kind)
	}
	dst := o.prefix(r)

	// Idempotent: a retried rollout finds the bundle already materialised and
	// skips the copy rather than re-uploading or, worse, duplicating it.
	if ok, err := o.Store.Exists(ctx, dst); err == nil && ok {
		return cd.Placement{ID: dst, ReleaseID: r.ID, Target: o.Name()}, nil
	}
	if _, _, err := o.Store.Copy(ctx, r.Ref, dst); err != nil {
		return cd.Placement{}, fmt.Errorf("upload %s: %w", dst, err)
	}
	return cd.Placement{ID: dst, ReleaseID: r.ID, Target: o.Name()}, nil
}

// Activate points the host at a placement's prefix and purges the edge.
//
// It verifies the bytes are present FIRST. Pointing a live host at an empty
// prefix would serve 404s to real traffic, and that is precisely the mistake a
// rollback is most likely to make — naming a placement whose bytes were pruned.
// Refusing here turns a silent outage into a loud error.
func (o *Origin) Activate(ctx context.Context, p cd.Placement) error {
	if o.Host == "" {
		return fmt.Errorf("%w: %s", ErrNoHost, o.Name())
	}
	ok, err := o.Store.Exists(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("verify %s: %w", p.ID, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotReady, p.ID)
	}
	if err := o.Router.Point(ctx, o.Host, p.ID); err != nil {
		return fmt.Errorf("point %s: %w", o.Host, err)
	}
	// Stale edge copies would mask the flip. Best-effort: a failed purge is a
	// slow rollout, not a failed one, and must not fail a correct deploy.
	_ = o.Router.Purge(ctx, o.Host)
	return nil
}

// Status reports what is actually serving, read from the Router rather than from
// cd's records. Asking the thing itself is the only way to notice drift between
// what we believe is live and what is.
func (o *Origin) Status(ctx context.Context) (cd.State, error) {
	if o.Host == "" {
		return cd.State{Message: "no host bound"}, nil
	}
	prefix, err := o.Router.Resolve(ctx, o.Host)
	if err != nil {
		return cd.State{Message: err.Error()}, err
	}
	if prefix == "" {
		return cd.State{Message: "host bound to nothing"}, nil
	}
	return cd.State{Healthy: true, Active: prefix, Message: o.Host}, nil
}

// Sweep prunes releases outside the rollback window. The active prefix is never
// pruned regardless of position — retention is a storage policy and must never be
// able to delete what is currently serving.
func (o *Origin) Sweep(ctx context.Context, placements []cd.Placement) error {
	if o.Keep <= 0 || len(placements) <= o.Keep {
		return nil
	}
	active, _ := o.Router.Resolve(ctx, o.Host)
	var errs []string
	for _, p := range placements[o.Keep:] {
		if p.ID == active || p.Active {
			continue
		}
		if err := o.Store.Prune(ctx, p.ID); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.ID, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("site: sweep: %s", strings.Join(errs, "; "))
	}
	return nil
}

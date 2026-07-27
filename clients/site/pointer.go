package site

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// The live pointer is DATA, not a service.
//
// Today a host resolves to (org, slug) and a slug resolves to exactly one prefix,
// so there is nowhere to record WHICH release is live. That absence is the whole
// reason rollback did not exist: with one prefix per site, "the live version" is
// not a fact anyone stores, it is just whatever bytes were written last.
//
// Rather than add a table, a controller, or a new service to hold that fact, we
// write one object next to the bundles:
//
//	<org>/<slug>/CURRENT  ->  "<org>/<slug>/v7/"
//
// A single PUT is atomic, so readers see the old prefix or the new one and never
// a half-swapped state — the exact property cd.Target.Activate requires. It also
// keeps the pointer in the same failure domain as the bytes it names: if the
// store is reachable, so is the answer to "what is live". A separate pointer
// service could be up while the origin is down, or vice versa, and then the two
// disagree — which is the failure this avoids by construction.
//
// It is also self-describing: `aws s3 cp s3://bucket/org/slug/CURRENT -` answers
// "what is serving" with no console, no API and no credentials beyond read.
const pointerKey = "CURRENT"

// ObjectStore is the minimal object access the pointer needs. Deliberately
// separate from Store: reading and writing one small object is a different
// capability from moving whole bundles, and a kind should not have to implement
// bundle copying to answer "what is live".
type ObjectStore interface {
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, body []byte) error
}

// Edge invalidates cached responses for a host. Best-effort by contract: a failed
// purge is a slow rollout, never a failed one, so it must not be able to fail a
// correct deploy.
type Edge interface {
	Purge(ctx context.Context, host string) error
}

// PointerRouter implements Router over an object store. It holds no state; the
// store is the state.
type PointerRouter struct {
	Org  string
	Slug string
	Obj  ObjectStore
	Edge Edge // optional
}

func (p PointerRouter) key() string { return path.Join(p.Org, p.Slug, pointerKey) }

// Point makes prefix live. One PUT, so the swap is atomic for readers.
//
// It does NOT verify the prefix — Origin.Activate already did, and re-checking
// here would split that responsibility across two places. The router's job is to
// move the pointer; deciding whether the pointer SHOULD move is the lifecycle's.
func (p PointerRouter) Point(ctx context.Context, _ string, prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return fmt.Errorf("site: refusing to point %s at an empty prefix", p.key())
	}
	if err := p.Obj.PutObject(ctx, p.key(), []byte(prefix)); err != nil {
		return fmt.Errorf("write %s: %w", p.key(), err)
	}
	return nil
}

// Resolve reads what is actually live. A missing pointer is not an error — it is
// a site that has never been activated, which is a normal state for a project
// that was just created.
func (p PointerRouter) Resolve(ctx context.Context, _ string) (string, error) {
	b, err := p.Obj.GetObject(ctx, p.key())
	if err != nil {
		return "", nil // never activated
	}
	return strings.TrimSpace(string(b)), nil
}

func (p PointerRouter) Purge(ctx context.Context, host string) error {
	if p.Edge == nil {
		return nil
	}
	return p.Edge.Purge(ctx, host)
}

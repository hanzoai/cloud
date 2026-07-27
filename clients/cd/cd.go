// Package cd is continuous delivery: it moves Releases onto Targets.
//
// It is deliberately ignorant. cd does not know what a site is, what a container
// is, or how either is served. It knows that an immutable Artifact becomes a
// Release, that a Release is Placed on a Target, and that exactly one Placement
// per Target is Active. Everything a kind needs to know about itself lives behind
// the Target interface, in that kind's own package.
//
// WHY THIS EXISTS
//
// "Get code into production" was five mechanisms that each re-derived the whole
// pipeline:
//
//   - platform /v1/runner        source -> image -> Service CR
//   - projects /v1/projects/…    source -> bundle -> S3 + host
//   - hanzocd                    universe CRs -> cluster
//   - universe-git-sync (cron)   GitHub <-> git.hanzo.ai mirror
//   - hand-rolled Deployments    (the lux.* marketing sites)
//
// Each knew how to build, where to put the result, how to version it, and how to
// undo it. Braiding those four independent facts together is why a static site
// could not be rolled back, could not be seen in the platform UI, and could not be
// tracked by CD at all. The kinds are not the problem; the repetition is.
//
// THE SEPARATION
//
//	Source     a repo at a ref                      (input, not modelled here)
//	Build      Source -> Artifact                   (a builder's job)
//	Artifact   immutable bytes + digest             (OCI image OR static bundle)
//	Release    an Artifact + the config it runs with
//	Placement  a Release materialised on a Target
//	Rollout    the transition that makes a Placement Active
//
// Artifact KIND (image vs bundle) is a property of the BUILD OUTPUT, not a
// different lifecycle. That single observation is what collapses five pipelines
// into one: a site and a container differ in how bytes are produced and served,
// never in what "promote" or "roll back" mean.
//
// A Release is immutable. Rollback is not an inverse operation that undoes work —
// it is Activate applied to an earlier Placement. There is no "undo" code path to
// keep correct, which is the whole point: the safe operation and the normal
// operation are the same operation.
package cd

import "context"

// Kind names an artifact's shape. It selects a Target implementation and nothing
// else; cd never branches on it.
type Kind string

const (
	KindImage  Kind = "image"  // an OCI image -> a workload target
	KindBundle Kind = "bundle" // a static file tree -> an origin target
)

// Artifact is immutable content addressed by digest. Ref locates it: an image
// reference for KindImage, an object-store prefix for KindBundle. cd treats both
// as opaque — only the Target that accepts them knows how to read them.
type Artifact struct {
	Kind   Kind
	Ref    string // "ghcr.io/org/app@sha256:…" | "s3://bucket/org/slug/rev/"
	Digest string // content hash; the identity of the bytes
	Bytes  int64
}

// Release is an Artifact plus the configuration it runs with, versioned per
// target. Version is monotonic and assigned by cd, never by a caller — two
// concurrent deploys of the same digest are two Releases, because config differs.
type Release struct {
	ID      string
	Target  string // Target.Name()
	Version int64
	Artifact
	Config    map[string]string
	Commit    string // provenance: the source ref this came from
	CreatedAt int64
}

// Placement is a Release materialised on a Target — an uploaded prefix, a running
// ReplicaSet. Active marks the one serving traffic. Keeping placements after they
// stop being active IS the rollback menu.
type Placement struct {
	ID        string
	ReleaseID string
	Target    string
	Active    bool
	CreatedAt int64
}

// State is what a Target reports about itself, in the vocabulary every kind
// shares. A kind with richer detail exposes it on its own surface, not here —
// widening this interface to fit one kind is how the braid grows back.
type State struct {
	Healthy bool
	Active  string // Placement.ID currently serving
	Message string
}

// Target is where a Release can be placed. Four verbs, because four is what a
// lifecycle needs; a fifth belongs to the kind, not to cd.
//
// Place must be idempotent on (Target, Release): a retried rollout re-uses the
// existing Placement rather than duplicating it. Activate must be atomic from a
// reader's perspective — a client sees the old placement or the new one, never a
// half-swapped state. Those two properties are the entire contract, and they are
// what let rollback be "Activate an older Placement" instead of bespoke undo.
type Target interface {
	Name() string
	Kind() Kind
	Place(context.Context, Release) (Placement, error)
	Activate(context.Context, Placement) error
	Status(context.Context) (State, error)
}

// Registry resolves a target name to its implementation. Kinds register at mount
// time; cd holds no compiled dependency on any of them, so adding a kind never
// edits this package.
type Registry interface {
	Target(name string) (Target, bool)
}

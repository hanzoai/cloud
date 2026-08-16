package sites

import "context"

// Edge is the CDN in front of a site, as the site server needs it — and it needs
// exactly two things, which is why this interface is two methods long.
//
// A published site is stale until the edge is told otherwise. Everything about
// HOW it is told — which vendor, which zone, which quota, how calls are
// coalesced — is the provider's business and appears nowhere above this line.
// `apps/sites` asks for an invalidation and asks whether anyone is listening.
//
// Declared HERE, by the consumer, for the same reason `apps/domain` declares
// Registrar and Zones next to the code that calls them rather than next to
// name.com: the interface belongs to whoever depends on it, so a second provider
// is a new implementation and not an edit to this file. cloudflare.Edge is the
// first one; a site served through another CDN, or through none, satisfies this
// with its own type and `apps/sites` does not change.
//
// UNCONFIGURED IS A VALID STATE, and it is the reason `Configured` is on the
// interface rather than being an internal detail. An edge with no credential is
// not an error — it degrades to stale-until-TTL, which is a documented and
// survivable posture — but it IS a thing an operator has to be able to see, so
// the seam reports it rather than hiding it behind a purge that silently does
// nothing.
type Edge interface {
	// PurgeTags invalidates every object stamped with any of these cache tags.
	// Failure is non-fatal by contract: a deploy that cannot purge has still
	// deployed, and the edge will catch up when the TTL expires.
	PurgeTags(ctx context.Context, tags ...string) error

	// Name is the provider behind this edge, lowercase, for an operator reading a
	// status page. It is the ONE place a vendor name is allowed to reach an API
	// response: everything else about the provider stays on its side of the seam,
	// but "which CDN is in front of my site" is a fair question and the answer
	// cannot come from anywhere else.
	Name() string

	// Configured reports whether this edge can actually act. False means every
	// purge is a no-op and content is live only after its TTL.
	Configured() bool

	// EnsureVerbatim makes the edge serve our documents byte-for-byte, correcting
	// it if it does not. A CDN that "optimises" HTML — minifying it, rewriting
	// links, injecting a loader script — is serving something we did not build,
	// and the failure is invisible from here because the bytes leaving S3 are
	// still right.
	//
	// Generic on purpose, though only one provider implements it today: every CDN
	// worth using offers some version of this and every one of them is wrong to
	// have it on by default. It returns nothing because it cannot fail usefully —
	// an edge that will not stop rewriting is a warning for an operator, not an
	// error for a deploy to abort on.
	EnsureVerbatim(ctx context.Context)
}

// NoEdge is the edge of a deployment that has none: every purge succeeds by
// doing nothing, and it says plainly that it is not configured.
//
// It exists so the absence of a CDN is a TYPE rather than a nil check scattered
// through the callers. A nil interface would make every call site responsible
// for remembering the same guard, which is how one of them eventually forgets
// and panics on a deploy path that must never fail.
type NoEdge struct{}

func (NoEdge) Name() string                               { return "none" }
func (NoEdge) PurgeTags(context.Context, ...string) error { return nil }
func (NoEdge) Configured() bool                           { return false }
func (NoEdge) EnsureVerbatim(context.Context)             {}

// CacheTag is the ONE canonical edge cache-tag for a project's site objects. The
// site server (streamSite) emits it as the Cache-Tag response header on every
// served object; the edge targets it on deploy, domain-bind, delete, and the
// dedicated POST .../purge. Both derive it from server-owned Org+Slug so the
// emitted tag and the purged tag never diverge.
//
// It lives HERE, with the interface, because it is the vocabulary the two sides
// share rather than anything a provider decides: the server stamps the tag and
// the edge purges it, and a tag format that lived with one of them would be a
// contract only half the code could see. It followed the first provider into
// its own package for a moment and the compiler caught it, which is the seam
// doing its job — the shared word belongs to the seam.
func CacheTag(org, slug string) string { return "site-" + org + "-" + slug }

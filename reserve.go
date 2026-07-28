package cloud

import "context"

// reserve.go — the platform's own reserve balance, as a VALUE anyone may read
// without linking whoever happens to store it.
//
// WHY THIS EXISTS. apps/admin's money board shows one number from the treasury:
// `treasury.Reserve(ctx)`. Reading it by IMPORT cost admin the whole of
// apps/treasury — 691 packages for a single int64. Admin does that to six apps,
// which is most of why its binary reaches 2260 packages against ~600 for a
// typical app. The coupling was never deep; it was one call each, priced at an
// entire dependency graph.
//
// So the arrow flips, exactly as it does for RegisterServiceReleaser and
// RegisterGitImporter: treasury (which owns the number) REGISTERS a reader, and
// admin (which only displays it) reads through here. A console that renders a
// figure should not link the ledger that computes it.
//
// The value is named for what it IS, not for who keeps it. If the reserve ever
// moves out of treasury, this seam does not change.

// reserve reports the platform's reserve balance in cents, and whether the
// subsystem that owns it is even present. Exactly one registration.
var reserve func(ctx context.Context) (int64, bool)

// RegisterReserve installs the reserve-balance reader. apps/treasury calls
// this from its Mount when co-resident.
func RegisterReserve(f func(ctx context.Context) (int64, bool)) { reserve = f }

// Reserve returns the platform reserve in cents and whether it could be
// read at all. ok=false means the owning subsystem is not in this binary — which
// a caller must render as "unavailable", never as a balance of zero. That
// distinction is the whole reason this returns two values instead of one.
func Reserve(ctx context.Context) (int64, bool) {
	if reserve == nil {
		return 0, false
	}
	return reserve(ctx)
}

package cloud

// stage.go — a capability that is not ga answers 404 to an org that has not been
// let into it (HIP-0139 §8).
//
// # Why 404 and not 403
//
// 403 is an existence oracle. A customer who is told "forbidden" at /v1/captable
// has learned that a cap-table product exists, is built, and is being withheld
// from them — which is a roadmap, published to anyone who can spell a URL, in the
// exact place a competitor would look. A capability a customer has not been let
// into does not exist FOR THEM, and the answer is the answer any other unrouted
// path gets, to the byte.
//
// # Why the refusal lives here
//
// [Listen] is the body every plugin binary runs, so a capability's refusal is
// installed in the process that serves it, from the row that declares the stage —
// one statement, applying to every route the app has and every route it grows.
// The light host learns nothing: cmd/cloud links manifest and zip and stops, and
// a host that decided this would need the flag plane, an org and a per-app policy
// in the one process that is deliberately ignorant of all three.
//
// It is also the only position from which the answer can be honest. The host
// proxies a request to the child that owns the prefix; nothing between them can
// say "this does not exist" without knowing what the child serves, and the child
// knows it because it IS the app.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/plane/flags"
	"github.com/zap-proto/zip"
)

// wait is how long the refusal waits for the flags app before failing closed.
//
// It is short because it is on the request path of every call to a staged
// capability, and because the alternative to an answer is a refusal rather than a
// guess: a caller that waits ten seconds to be told 404 has been made to pay for
// somebody else's outage. It is not shorter because a lazy flags child pays a
// cold start on the first request that reaches it, and refusing every early
// caller during a boot would read as the flag having been revoked.
const wait = 3 * time.Second

// Stage refuses requests to a capability the caller's org has not been let into,
// or composes NOTHING when the capability is ga.
//
// Returning nil for the ga case is the whole of the API: zip's Use takes a nil
// component as "a composition root that decided not to compose anything", so
// [Listen] states the rule once for every app it mounts and no call site carries
// an `if`. A ga app pays one nil check at boot and nothing per request.
//
//	app.Use(Stage(p.Name))
//
// The name is the app's, and it is three things at once — the manifest row this
// reads, the prefixes the refusal covers, and the FLAG KEY the org must hold.
// That is HIP-0139's point rather than a convenience: one capability, one name,
// so there is no mapping table between a product and its flag to get wrong.
func Stage(name string) zip.Handler {
	stage := manifest.StageOf(name)
	if stage == "" {
		return nil
	}
	return refuse(name, "stage", func(ctx context.Context) (bool, error) {
		held, err := flags.FlagsHold(ctx, &plane.FlagIn{Key: name})
		if err != nil {
			return false, err
		}
		return held.On, nil
	})
}

// refuse builds a capability refusal: whose path this is, who is asking, and what
// an unanswerable question means. Only the question is a parameter.
//
// apps/framework/elective.go is the sibling that refuses by MODULE; it resolves a
// DocType where this resolves a prefix. `why` reaches the log, never the wire.
func refuse(name, why string, ask func(context.Context) (bool, error)) zip.Handler {
	prefixes := manifest.PrefixesFor(name)
	return func(c *zip.Ctx) error {
		// Prefixes nest (/v1/risk is risk's, /v1/risk/labels is label's) and the
		// router resolves by specificity, so ownership is [manifest.OwnerOf]'s
		// answer, not a string compare. The covers() test in front is the fast
		// reject: OwnerOf walks every prefix in the fleet at ~33µs.
		if !covers(prefixes, c.Path()) || manifest.OwnerOf(c.Path()) != name {
			return c.Next()
		}
		// 404, not 401: answering 401 would confirm the address is real to the one
		// caller with no standing to know it.
		org, ok := principal.Org(c)
		if !ok || org == "" {
			return missing()
		}
		ctx, cancel := context.WithTimeout(For(c.Context(), org), wait)
		defer cancel()
		on, err := ask(ctx)
		if err != nil {
			// Fail closed: admitting when the authority cannot answer opens every
			// gated product to everyone at once.
			c.Log().Warn("refusing: the authority did not answer", "app", name, "why", why, "err", err)
			return missing()
		}
		if !on {
			return missing()
		}
		return c.Next()
	}
}

// missing is the answer a path nobody registered gets, built the same way zip
// builds it: status 404, message http.StatusText(404). Held against a real
// unrouted request rather than against a literal, so a zip that changed its own
// answer would fail here instead of quietly making this one distinguishable —
// see TestBetaWithoutTheFlagIsNotThere.
//
// It carries no header, no code and no sentence of its own. Every one of those
// would be the oracle 404 was chosen to close: a body that says "not enabled"
// tells a reader the thing exists as surely as a 403 does.
func missing() error { return zip.ErrNotFound(http.StatusText(http.StatusNotFound)) }

// covers reports whether path is one of the prefixes or beneath one.
//
// Segment-wise, so /v1/adsense is not under /v1/ad: a byte compare would refuse
// a neighbouring capability's whole surface on a shared spelling, and the two
// have nothing to do with each other.
func covers(prefixes []string, path string) bool {
	for _, p := range prefixes {
		if path == p {
			return true
		}
		if strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

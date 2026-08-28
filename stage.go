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
	"github.com/hanzoai/cloud/plane/entitlement"
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

// Elective refuses requests to a capability the caller's org has not turned on,
// or composes NOTHING when the capability is universal.
//
//	app.Use(Elective(p.Name))
//
// It is the twin of [Stage] and asks a different subsystem a different question:
// Stage asks flags whether this customer has been LET INTO a product that is not
// finished, Elective asks entitlement whether this customer has ASKED FOR one
// that is. Both refuse with the same 404 because a refusal that distinguished
// them would tell a stranger which products exist and which they could buy.
//
// An org holds nothing until it says so — the enablement store has no row for a
// product nobody enabled, and no row is `false`. That is the opt-in semantic, and
// it means marking a row Elective is a CUTOVER for anyone already using it: see
// manifest.App.Elective.
func Elective(name string) zip.Handler {
	if !manifest.IsElective(name) {
		return nil
	}
	return refuse(name, "elective", func(ctx context.Context) (bool, error) {
		on, err := entitlement.EntitlementHolds(ctx, &plane.ProductIn{Product: name})
		if err != nil {
			return false, err
		}
		return on.On, nil
	})
}

// refuse is the rule both capability refusals share: whose path this is, who is
// asking, and what an unanswerable question means. Only the question differs, so
// only the question is a parameter.
//
// It is one function because the parts that are easy to get wrong are the parts
// that are NOT the question — the ownership test, the unvalidated caller, the
// fail-closed — and two copies would be two places for those to drift. `why`
// names the asking subsystem in the log and nowhere else; it never reaches the
// wire.
func refuse(name, why string, ask func(context.Context) (bool, error)) zip.Handler {
	prefixes := manifest.PrefixesFor(name)
	return func(c *zip.Ctx) error {
		// WHOSE PATH IS THIS. Prefixes nest — /v1/risk is risk's and
		// /v1/risk/labels is label's — and the router resolves that by
		// SPECIFICITY, so a capability holding the shallower prefix must not
		// answer for the deeper one. [manifest.OwnerOf] is that rule already, and
		// asking it rather than comparing strings is what keeps the refusal on
		// exactly the surface the host routes here.
		//
		// The prefix test in front of it is not a second rule, it is the fast
		// reject: OwnerOf walks every prefix in the fleet and costs ~33µs, which is
		// nothing on a request that is about to ask a peer over a socket and far too
		// much on the request that merely shares this process. A path under none
		// of this app's prefixes cannot be owned by it, so skipping the scan there
		// cannot change the answer.
		if !covers(prefixes, c.Path()) || manifest.OwnerOf(c.Path()) != name {
			return c.Next()
		}
		// An unvalidated caller is 404, not 401. A refused capability owes a
		// stranger nothing, and answering 401 would confirm the address is real to
		// exactly the caller with no standing to know it. A request that would have
		// been refused for its credentials anyway loses nothing by being refused
		// for its address first.
		org, ok := principal.Org(c)
		if !ok || org == "" {
			return missing()
		}
		ctx, cancel := context.WithTimeout(For(c.Context(), org), wait)
		defer cancel()
		on, err := ask(ctx)
		if err != nil {
			// FAIL CLOSED, and say why HERE rather than on the wire. An outage in
			// the subsystem being asked must not open every gated product to every
			// customer — that is the one failure this refusal exists to prevent, and
			// it is the failure an "if we cannot ask, let them through" would cause
			// fleet-wide and at once. The operator reads the reason in the log; the
			// caller reads the same 404 they would have read anyway, so an outage is
			// not a signal either.
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

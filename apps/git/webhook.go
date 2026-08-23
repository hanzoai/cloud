package git

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// webhook.go is a TOMBSTONE. This endpoint never built anything, and its 204 said
// otherwise.
//
// git.hanzo.ai used to POST every push here; we HMAC-verified it and handed the
// push to cloud.OnGitPush, the single-registrant push-to-deploy client. That
// registrant lives in apps/platform and cloud runs each app as its OWN OS
// PROCESS, so in the git process the builder is nil FOREVER — and the handler
// answered 204 whether or not it dispatched. Delivered, signature valid, green
// on the forge's hook page, nothing built. main once drifted eight commits past
// what was live behind that 204, and the forge's own hook_task record was the
// only place the truth appeared.
//
// Push-to-deploy belongs to platform.hanzo.ai, which owns the buildJob
// system-of-record and dispatches BuildKit Jobs on its own runner pools. ONE
// forge-wide system webhook on git.hanzo.ai delivers there for every repo; a
// repo opts in by committing hanzo.yml, not by owning a hook.
//
// The route is KEPT and answers 410 rather than being deleted, because a
// deleted route 404s and a 404 from this estate is the exact signal that has
// already cost two investigations: Hanzo Git serves /v1, so /api/v1 404s and
// reads as "the API is switched off". A retired endpoint must SAY it is retired
// and NAME its replacement — an unexplained silence is what made the 204 expensive.
//
// It reads no body, holds no secret and verifies nothing: there is nothing left
// here to authenticate. GIT_WEBHOOK_SECRET is no longer read by this binary.

// buildDoor is where a forge delivery belongs. Stated once, in the message a
// caller actually receives, so the answer carries its own fix.
const buildDoor = "https://platform.hanzo.ai/v1/git-webhook"

// "Cannot be a typed op" is not "must be undocumented". Every route in git's
// untypedByDesign list (typed_wire_test.go) published an operationId, tags and
// NOTHING ELSE, which no consumer of the document can tell apart from a route
// that takes no body — so every generated SDK offered a forge webhook with
// nowhere to put the delivery, and three ZAP procedures with nowhere to put the
// repo name. openapi.Register states the request each one really reads, keyed by
// the router's own pattern, without touching the route: pure DESCRIPTION, no
// status, field or byte moves, and the reasons those routes stay raw are
// untouched by it.
//
// What is declared, and what is deliberately not:
//
//   - the webhook reads NOTHING and answers 410 on every path, so it declares no
//     request. It used to Register pushEvent, which was honest while it parsed a
//     delivery; declaring a body a retired endpoint never looks at would hand every
//     generated SDK a payload parameter for a call that ignores it.
//   - the two pack POSTs read an application/x-git-*-request pack stream:
//     openapi.Binary, the same declaration a receipt upload gets. Their root-host
//     twins are separate router patterns and are declared separately.
//   - createRepo/getRepo/deleteRepo bind zapProcReq; listRepos and usage read NO
//     body at all (zap.go), so declaring one for them would be a fresh falsehood
//     in place of the silence.
//   - no RESPONSE is declared for the ZAP five. Their envelope is real
//     ({status, msg, data}) but its data is repoView / []repoView / usageView,
//     names zip's typed fold ALREADY publishes as components off the /v1 ops —
//     so reflecting them here a second time would put two derivations behind one
//     schema name, which is the collision openapi.Compose exists to refuse. One
//     name, one shape, one generator: the envelope waits until the two clients
//     agree on who owns a shared view type.
//   - the pack responses and the twelve HTML pages have no declaration to make:
//     openapi.Binary is request-only by design, and a text/html response is the
//     second half it deliberately does not invent.
//
// The PROSE those same routes were also missing is a separate client
// (openapi.Describe) and lives beside each route's own registration, not here:
// the twelve smart-HTTP operations in smart_http.go, the five ZAP procedures in
// zap.go, the twelve pages in ui.go. Only the webhook's prose is stated here,
// because this is where the webhook is declared. Splitting a family's prose by
// which half of it happens to have a declarable body would put one thing in two
// places; a Register and a Describe for the SAME route stay together.
//
// init, not routes(): Register panics on a duplicate declaration and routes()
// runs once per Mount.
func init() {
	openapi.Describe("/v1/git/webhook", "POST",
		"Retired — forge pushes build via platform.hanzo.ai",
		"GONE (410). This was the canonical forge's push-to-deploy endpoint, and it never "+
			"dispatched a build in its life.\n\n"+
			"It handed each verified push to cloud.OnGitPush, a single-registrant client "+
			"whose only registrant lives in apps/platform. cloud runs each app as its own "+
			"OS process, so in the git process that builder is nil forever — and this "+
			"handler answered 204 either way. Delivered, signature valid, green on the "+
			"forge's hook page, and nothing built.\n\n"+
			"Push-to-deploy now belongs to POST "+buildDoor+", which owns the build "+
			"system-of-record and dispatches BuildKit Jobs. git.hanzo.ai delivers there "+
			"through ONE forge-wide system webhook covering every repository; a repo opts "+
			"in by committing hanzo.yml, not by owning a hook of its own.\n\n"+
			"The route is kept, and answers 410 naming that address, precisely so a "+
			"misdirected delivery says what is wrong. Deleting it would 404, and a 404 "+
			"here reads as 'the API is switched off' — the wrong conclusion this estate "+
			"has already drawn twice.")

	openapi.Register("/v1/git/zap/createRepo", "POST", zapProcReq{}, nil)
	openapi.Register("/v1/git/zap/getRepo", "POST", zapProcReq{}, nil)
	openapi.Register("/v1/git/zap/deleteRepo", "POST", zapProcReq{}, nil)

	openapi.Register("/v1/git/:org/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/v1/git/:org/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)

	// The same pack streams for a project-scoped repo, which names its project as
	// a middle segment because a git client has no header to carry it.
	openapi.Register("/v1/git/:org/:project/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/v1/git/:org/:project/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:project/:repo/git-upload-pack", "POST", openapi.Binary{}, nil)
	openapi.Register("/:org/:project/:repo/git-receive-pack", "POST", openapi.Binary{}, nil)
}

// webhook answers every delivery 410 Gone, naming the endpoint that builds. It
// reads no body: there is nothing here to authenticate and nothing to parse.
//
// 410, not 404: the address was real and its meaning moved, which is exactly the
// distinction 410 carries. 404 would say "no such route" about a route this
// binary still serves, and would be indistinguishable from the /api/v1 prefix
// mistake that has already sent two investigations after a switched-off API.
//
// cloud.Terminal (git.go) writes this in-band so the co-mounted /v1
// ErrorHandlerJSON cannot flatten it to a 500 — the same reject-parity the
// bad-signature 401 needed when this endpoint still verified one.
func webhook(*cloud.Service[state], *zip.Ctx) error {
	return zip.Errorf(http.StatusGone,
		"POST /v1/git/webhook is retired and never dispatched a build: its trigger is "+
			"registered in another process, so it answered 204 without building. Send forge "+
			"deliveries to %s instead — on git.hanzo.ai this is already the forge-wide "+
			"system webhook, and a repo opts in by committing hanzo.yml.", buildDoor)
}

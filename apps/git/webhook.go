package git

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// webhook.go is a TOMBSTONE: this address is retired and answers 410.
//
// Push-to-deploy belongs to platform.hanzo.ai, which owns the buildJob
// system-of-record and dispatches BuildKit Jobs on its own runner pools. ONE
// forge-wide system webhook on git.hanzo.ai delivers there for every repo; a
// repo opts in by committing hanzo.yml, not by owning a hook.
//
// The route is KEPT rather than deleted, because a deleted route 404s and a 404
// from this estate is ambiguous: Hanzo Git serves /v1, so /api/v1 404s too and
// reads as "the API is switched off". A retired endpoint SAYS it is retired and
// NAMES its replacement, so the answer carries its own fix.
//
// It reads no body, holds no secret and verifies nothing: there is nothing left
// here to authenticate. GIT_WEBHOOK_SECRET is no longer read by this binary.
//
// # Why it is not a typed op, at the pinned zip
//
// The reason this route USED to carry — "a typed op needs an In or an Out; a
// tombstone has neither" — was false twice over: `noInput` and `noContent` are
// git's own (ops.go) and six typed ops already use them. It was replaced by the
// one that holds and is MEASURED rather than argued.
//
// ANY BYTES ANSWER 410, and that is the whole meaning of "retired" — the property
// TestWebhookIsGoneForEveryDelivery states in as many words and drives with a real
// push, an empty body and a malformed one. A typed op cannot express it: op.invoke
// json-decodes any non-empty body BEFORE the handler is entered (zip
// typed.go:485-490) and answers an undecodable one with zip's 400 problem
// document, so `{not json` — a live case in that test — would stop being told
// where the delivery belongs and be told its body is bad instead. Measured on a
// typed prototype of this exact route: an empty body and a real JSON delivery
// answered 410 byte-for-byte, a JSON array and a non-JSON body answered 400.
//
// Preserving it is not one flag away. `encoding/json` validates the whole
// document before it invokes any custom unmarshaler (jsonenc is stdlib —
// zip/internal/jsonenc/v1.go), so an In that tolerates every SHAPE still cannot
// tolerate bytes that are not JSON. The conversion is available the day zip can
// declare a route that reads no body at all.
//
// cloud.Terminal (git.go) therefore stays: it writes the 410 in-band so a
// co-mounted /v1 ErrorHandlerJSON cannot flatten the propagated error to 500.

// buildDoor is where a forge delivery belongs. Stated once, in the message a
// caller actually receives, so the answer carries its own fix.
const buildDoor = "https://platform.hanzo.ai/v1/git-webhook"

// The prose. "Cannot be a typed op" is not "must be undocumented": a raw route
// carries an operationId and a tag and nothing else, which no consumer of the
// document can tell apart from a route that says nothing because there is nothing
// to say. zipdoc lifts a doc comment off a TYPED registration only, so a refusal
// states its prose through openapi.Describe, keyed by the router's own pattern —
// pure DESCRIPTION, with no status, field or byte moved.
//
// It declares NO request body, deliberately. It used to Register `pushEvent`,
// which was honest while it parsed a delivery; declaring a body an endpoint never
// looks at would hand every generated SDK a payload parameter for a call that
// ignores it — an honest silence replaced by a fresh falsehood.
//
// init, not routes(): Describe panics on a duplicate declaration and routes()
// runs once per Mount.
func init() {
	openapi.Describe("/v1/git/webhook", http.MethodPost,
		"Retired — forge pushes build via platform.hanzo.ai",
		"GONE (410). Push-to-deploy belongs to POST "+buildDoor+", which owns the build "+
			"system-of-record and dispatches BuildKit Jobs. git.hanzo.ai delivers there "+
			"through ONE forge-wide system webhook covering every repository; a repo opts "+
			"in by committing hanzo.yml, not by owning a hook of its own.\n\n"+
			"Every delivery answers 410 whatever it carries — this endpoint reads no body "+
			"and authenticates nothing.\n\n"+
			"410 rather than 404, because the address was real and its meaning moved, which "+
			"is the distinction 410 carries. A 404 from this estate is ambiguous: Hanzo Git "+
			"serves /v1, so /api/v1 404s too and reads as \"the API is switched off\". A "+
			"retired endpoint says it is retired and names its replacement, so the answer "+
			"carries its own fix.")
}

// webhook answers every delivery 410 Gone, naming the endpoint that builds. It
// reads no body: there is nothing here to authenticate and nothing to parse.
//
// 410, not 404: the address was real and its meaning moved, which is exactly the
// distinction 410 carries. 404 would say "no such route" about a route this
// binary still serves, and is indistinguishable here from the /api/v1 prefix
// mistake, since Hanzo Git serves /v1.
func webhook(*cloud.Service[state], *zip.Ctx) error {
	return zip.Errorf(http.StatusGone,
		"POST /v1/git/webhook is retired. Send forge deliveries to %s instead — on "+
			"git.hanzo.ai this is already the forge-wide system webhook, and a repo opts "+
			"in by committing hanzo.yml.", buildDoor)
}

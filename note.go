package cloud

// note.go — which operation a request reached, kept where the trail can read it.
//
// THE HOLE THIS CLOSES, MEASURED. The audit trail is written by transport
// middleware, and transport middleware reads the path the TRANSPORT carried. A
// typed operation is reachable more than one way, and only one of them puts the
// operation in the path. Driven against a real recorder and read back
// (audit_doors_test.go), one op registered once at /v1/probe/run said:
//
//	REST   action "POST /v1/probe/run"                   <- the operation
//	MCP    action "POST /mcp"                            <- an envelope
//	ZAP    action "POST /.well-known/zip/op/probe_run"   <- an envelope
//	CLI    no record at all                              <- no request to wrap
//
// Two of those name the door rather than what came through it. On the MCP row
// the operation is not recoverable at all: every tools/call writes the same four
// words, so the record answers "an agent called something". That is the door
// agents use, and the trail is what AU-3 asks to say WHAT happened. The resource
// goes with it, because resourceFromPath reads the same value.
//
// THE FIX IS TO ASK ABOUT THE OPERATION. zip hands every projection of a typed
// handler through ONE dispatcher and offers ONE hook inside it, App.Authorize,
// whose argument is a zip.Op — {Method, Path, OperationID}, the same value on
// every door. So the operation is noted on the request as it passes, and the
// middleware that writes the record reads what was noted instead of guessing
// from the envelope.
//
// WHAT IS STILL OUT OF REACH. The CLI row is an in-process LocalInvoke: there is
// no request, so there is nothing for a transport middleware to wrap and nothing
// here to write to. Covering it honestly needs a post-invoke hook in zip — the
// record carries an outcome, and Authorize runs before there is one. Stated
// rather than papered over: the test asserts the bound so it stays a known edge.

import (
	"context"

	"github.com/zap-proto/zip"
)

type (
	// requestSlot carries the request itself, so code reached through the
	// op-invoke seam — which is handed a context and not a *zip.Ctx — can find it.
	requestSlot struct{}
	// opSlot is where the operation is written. A private type as the key, so
	// nothing else can collide with it or read it by guessing.
	opSlot struct{}
)

// Carry puts the request on its own context. Install it once, ahead of anything
// that needs to reach the request from a context: the op-invoke seam is given a
// context.Context, and without this there is no way back to the *zip.Ctx that
// the transport middleware is holding.
func Carry() zip.Handler {
	return func(c *zip.Ctx) error {
		c.SetContext(context.WithValue(c.Context(), requestSlot{}, c))
		return c.Continue()
	}
}

// Request answers the request behind a context, and whether there is one. There
// is not, for an in-process invoke.
func Request(ctx context.Context) (*zip.Ctx, bool) {
	c, ok := ctx.Value(requestSlot{}).(*zip.Ctx)
	return c, ok && c != nil
}

// Note records which operation this invoke is, on the request that carried it.
// It answers nothing and refuses nothing — it is a fact about the invoke, put
// where the code that writes the record can reach it. With no request behind the
// call there is nowhere to put it and nothing to read it, so it does nothing.
func Note(ctx context.Context, op zip.Op) {
	if c, live := Request(ctx); live {
		c.Fiber().Locals(opSlot{}, op)
	}
}

// Noted answers the operation this request reached, and whether it reached one.
// False for a request that never entered a typed op — a raw route, a static
// asset, a probe — where the transport path is the only thing there is and is
// also the right answer.
func Noted(c *zip.Ctx) (zip.Op, bool) {
	op, ok := c.Fiber().Locals(opSlot{}).(zip.Op)
	return op, ok
}

// matched is the route the request matched: the operation's own route when a
// caller addressed the operation directly, and the envelope's route — /mcp, the
// call plane — when the operation was named inside the body instead. Comparing
// it to the noted operation is how a reader of the request tells the two apart
// without knowing which envelopes zip happens to serve.
func matched(c *zip.Ctx) string {
	if r := c.Fiber().Route(); r != nil {
		return r.Path
	}
	return ""
}

// Rule is the decision every operation this program serves answers to, wherever
// it was reached from. It asks nothing today: the one thing it does is state
// which operation an invoke is, before anything else could refuse it, so a
// refusal is recorded under the name of what was refused rather than under the
// name of the door somebody knocked at.
func Rule() zip.Authorizer {
	return func(ctx context.Context, op zip.Op, _ any) error {
		Note(ctx, op)
		return nil
	}
}

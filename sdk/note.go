package cloud

// note.go — which operation a request reached, kept where the trail can read it.
//
// THE HOLE THIS CLOSES, MEASURED. The audit trail is written by transport
// middleware, and transport middleware reads the path the TRANSPORT carried.
// That is the same value toll.go found the money gate reading, and it goes wrong
// in the same place: one typed op, `probe_run`, registered once at
// /v1/probe/run, reached four ways, is seen by the edge as four different things.
// Driven against a real recorder and read back (audit_doors_test.go), the trail
// said:
//
//	REST   action "POST /v1/probe/run"                   <- the operation
//	MCP    action "POST /mcp"                            <- an envelope
//	ZAP    action "POST /.well-known/zip/op/probe_run"   <- an envelope
//	CLI    no record at all                              <- no request to wrap
//
// Two of those name the door rather than what came through it. On the MCP row
// the operation is not recoverable at all: every tools/call in the fleet writes
// the same four words, so the record answers "an agent called something". That
// is the door agents use, and the trail is what AU-3 asks to say WHAT happened.
// The resource goes with it — resourceFromPath reads the transport path too, so
// the MCP row named no resource and the plane row named a resource of type "op".
//
// THE FIX IS TO ASK ABOUT THE OPERATION. zip hands every projection of a typed
// handler through ONE dispatcher and offers ONE hook inside it, App.Authorize,
// whose argument is a zip.Op — {Method, Path, OperationID}, the same value on
// every door. Rule already runs there. So the operation is noted on the request
// as it passes, and the middleware that writes the record reads what was noted
// instead of guessing from the envelope.
//
// IT IS NOTED FIRST, before either question Rule asks. A refusal is the most
// security-relevant record there is, and a refusal attributed to "POST /mcp"
// says a caller was denied without saying what they were denied.
//
// WHAT IS STILL OUT OF REACH. The CLI row above is an in-process LocalInvoke:
// there is no request, so there is nothing for a transport middleware to wrap
// and nothing here to write to. Ordinary `hanzo` against a deployed cloud is a
// Remote invoker (zip cli.go) and arrives as REST, so it is on the first row;
// what this does not cover is the binary invoking its own op, which is an
// operator on the pod. Covering that honestly needs a post-invoke hook in zip —
// the record has to carry the outcome, and Authorize runs before there is one.
// Stated rather than papered over: audit_doors_test.go asserts the bound so it
// stays a known edge instead of becoming a surprise.

import (
	"context"
	"log"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// opSlot is the request-scoped slot the operation is written to. A private type
// as the key, so nothing else can collide with it or read it by guessing.
type opSlot struct{}

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

// Matched is the route the request Matched: the operation's own route when a
// caller addressed the operation directly, and the envelope's route — /mcp, the
// call plane — when the operation was named inside the body instead. Comparing
// it to the noted operation is how a reader of the request tells the two apart
// without knowing which envelopes zip happens to serve.
func Matched(c *zip.Ctx) string {
	if r := c.Fiber().Route(); r != nil {
		return r.Path
	}
	return ""
}

// ReachSlot carries, from the abuse gate to the op-invoke seam, the one thing
// the gate could not know: which operation the request went on to reach. It
// holds a closure rather than the sensor and the observation, so the observation
// is still built in exactly one place (agency.go) and nothing downstream can
// construct a second one under a different reading of the same request.
type ReachSlot struct{}

// Reach tells the sensor which operation a request reached, for a request whose
// path named a door. It answers nothing and refuses nothing.
//
// It is offered only where the gate stood down — a request that addressed its
// operation directly was already counted under that operation's own family — so
// one request contributes one path to the spread set however it arrived.
func Reach() zip.Authorizer {
	return func(ctx context.Context, op zip.Op, _ any) (zip.Decision, error) {
		allow := zip.Decision{Effect: zip.Allow}
		c, live := Request(ctx)
		if !live {
			return allow, nil
		}
		tell, ok := c.Fiber().Locals(ReachSlot{}).(func(string))
		if !ok || Matched(c) == op.Path {
			return allow, nil
		}
		tell(RouteFamily(op.Path))
		return allow, nil
	}
}

// Trail returns the op-invoke hook that records an operation no request carried.
//
// THE DOOR THIS CLOSES, MEASURED. audit_doors_test.go drives one operation over
// every door and reads the trail back. Three of them write a record, because a
// request exists and AuditTrail wraps it. The fourth — an in-process invoke, the
// CLI dispatching against ops registered in this process — has no request, so no
// transport middleware runs and the operation left no trace of having happened.
// That is an operator on a pod running a privileged operation, and it is exactly
// the kind of act a trail is kept for.
//
// It stands down whenever a request exists. The middleware is the better witness
// there: it sees the response the client actually got, while this sees only
// whether the handler returned an error. Two witnesses to one event would give
// it two records that can disagree, so there is one, chosen by whether the
// better one was able to run.
//
// The coverage predicate is the SAME one, asked about the same operation, so a
// safe read is no more an event through this door than through any other.
func Trail(rec *audit.Recorder) func(context.Context, zip.Op, error) {
	if rec == nil {
		return nil
	}
	return func(ctx context.Context, op zip.Op, err error) {
		if _, live := Request(ctx); live {
			return // a request exists; AuditTrail wrote the record.
		}
		status := EffectiveStatus(200, err)
		who := Who(ctx)
		if !isSecurityRelevant(op.Method, op.Path, status, who.Admin) {
			return
		}
		// A failure to record is loud where it can be: this is called after the
		// handler on a path with no response to fail closed into, so there is
		// nothing to refuse — the operation has already run. The alternative is
		// silence, which is what this exists to end.
		if _, aerr := rec.Append(ctx, audit.Record{
			Actor: audit.Actor{Org: who.Org, Sub: who.User, Email: who.Email},
			// The door is named because it is the one fact a reader cannot
			// recover: every other record in the trail came off a request, and
			// this one did not.
			Action:   op.Method + " " + RouteFamily(op.Path),
			Resource: resourceFromPath(op.Path),
			Auth:     audit.AuthContext{Method: "in-process", IsAdmin: who.Admin},
			Outcome:  outcomeOf(status, err),
			Method:   op.Method,
			Path:     scrubCredentialSegments(op.Path),
		}); aerr != nil {
			log.Printf("cloud: audit append failed for in-process %s: %v", op.OperationID, aerr)
		}
	}
}

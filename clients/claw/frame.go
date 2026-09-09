package claw

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/zap-proto/zip"
)

// The envelope, read from the TypeScript that speaks it.
//
// packages/gateway-protocol/src/schema/frames.ts declares three top-level
// frames as closed objects (additionalProperties: false):
//
//	RequestFrameSchema   { type: "req",   id, method, params?, traceparent? }
//	ResponseFrameSchema  { type: "res",   id, ok, payload?, error? }
//	EventFrameSchema     { type: "event", event, payload?, seq?, stateVersion? }
//	ErrorShapeSchema     { code, message, details?, retryable?, retryAfterMs? }
//
// The client writes a request as one JSON text message —
// packages/gateway-client/src/pending-request.ts:146:
//
//	sender.send(JSON.stringify({ type: "req", id, method, params }));
//
// and settles it by id alone (pending-request.ts:159):
//
//	handleResponse(frame) { const pending = this.pending.get(frame.id); ... }
//	if (frame.ok) { pending.resolve(frame.payload) }
//	else { pending.reject(new GatewayProtocolRequestError(frame.error ?? {})) }
//
// The id is minted by the client as "<sequence>:<random>" and is unique within
// one socket generation, never reused after a settle.
//
// There is no stream frame and no subscription frame. A method that wants to
// acknowledge before it finishes answers { status: "accepted" } first: the
// client keeps the pending entry open when the caller passed expectFinal and
// the payload's status is exactly "accepted" (pending-request.ts:163), and
// settles on the next frame carrying the same id. Everything else that flows
// the other way is an event frame, correlated by name rather than by id, with
// seq monotone per socket — the client reports a gap when it sees
// seq > lastSeq + 1 (protocol-client.ts:412) — so seq belongs to the
// connection, not to the publisher.
//
// The handshake is itself a request: on open the client sends
// method "connect" with ConnectParams and expects a response payload of
// HelloOk (protocol-client.ts:341). See hello.go.

// frame kinds, spelled as the schema literals.
const (
	kindRequest  = "req"
	kindResponse = "res"
	kindEvent    = "event"
)

// Request is one inbound call.
type Request struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Trace  string          `json:"traceparent,omitempty"`
}

// Response answers exactly one Request, correlated by ID.
type Response struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Payload any    `json:"payload,omitempty"`
	Error   *Fault `json:"error,omitempty"`
}

// Event is an unsolicited frame. Seq is stamped per connection as the frame is
// written, so a client can tell a dropped event from a quiet server.
type Event struct {
	Type    string `json:"type"`
	Event   string `json:"event"`
	Payload any    `json:"payload,omitempty"`
	Seq     uint64 `json:"seq"`
}

// Fault is the wire's error shape and also a Go error, so a method returns one
// directly. Code is drawn from the closed set below; a client switches on it.
type Fault struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Details      any    `json:"details,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
	RetryAfterMs int    `json:"retryAfterMs,omitempty"`
}

func (f *Fault) Error() string { return f.Message }

// The closed code set, from packages/gateway-protocol/src/gateway-error-details.ts
// (ErrorCodes). A code outside this set is one the client has no branch for.
const (
	codeInvalid    = "INVALID_REQUEST"
	codeForbidden  = "FORBIDDEN"
	codeUnavail    = "UNAVAILABLE"
	codeNotPaired  = "NOT_PAIRED"
	codeNoApproval = "APPROVAL_NOT_FOUND"
)

// missingScope is the details discriminant for a refusal that names the scope
// the caller lacks (GatewayErrorDetailCodes.MISSING_SCOPE). The client reads it
// to offer an upgrade instead of parsing the message.
const missingScope = "MISSING_SCOPE"

// Invalid rejects a request the caller can fix: a malformed parameter, an
// unknown name, a precondition the caller stated wrongly.
func Invalid(format string, a ...any) *Fault {
	return &Fault{Code: codeInvalid, Message: fmt.Sprintf(format, a...)}
}

// Forbidden refuses a caller who is known but not permitted.
func Forbidden(format string, a ...any) *Fault {
	return &Fault{Code: codeForbidden, Message: fmt.Sprintf(format, a...)}
}

// Unavailable reports that the work could not be attempted now. It is
// retryable, which is what tells a client to back off rather than give up.
func Unavailable(format string, a ...any) *Fault {
	return &Fault{Code: codeUnavail, Message: fmt.Sprintf(format, a...), Retryable: true}
}

// NotPaired reports a device that exists but has not been admitted.
func NotPaired(format string, a ...any) *Fault {
	return &Fault{Code: codeNotPaired, Message: fmt.Sprintf(format, a...)}
}

// NoApproval reports an approval that has expired or never existed.
func NoApproval(format string, a ...any) *Fault {
	return &Fault{Code: codeNoApproval, Message: fmt.Sprintf(format, a...)}
}

// MissingScope refuses a caller for want of one capability and says which,
// in the shape missingScopeErrorShape builds in
// packages/gateway-protocol/src/schema/error-codes.ts:
//
//	{ code: "FORBIDDEN", message: `missing scope: ${scope}`,
//	  details: { code: "MISSING_SCOPE", missingScope, requiredScopes } }
func MissingScope(need Scope) *Fault {
	return &Fault{
		Code:    codeForbidden,
		Message: "missing scope: " + string(need),
		Details: map[string]any{
			"code":           missingScope,
			"missingScope":   string(need),
			"requiredScopes": []string{string(need)},
		},
	}
}

// fault normalizes whatever a method returned into the wire shape. A *Fault
// passes through unchanged. A *zip.HTTPError — what every other subsystem in
// this cloud returns — maps by status, so a method may call clients/session or
// clients/tasks and hand back their error without translating it by hand.
// Anything else is a failure the caller did not cause and cannot read, so it
// becomes an opaque UNAVAILABLE; the real text goes to the log.
func fault(err error) *Fault {
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	var he *zip.HTTPError
	if errors.As(err, &he) {
		switch {
		case he.Status == http.StatusUnauthorized, he.Status == http.StatusForbidden:
			return Forbidden("%s", he.Msg)
		case he.Status >= 400 && he.Status < 500:
			// The protocol has no not-found or conflict code: a bad id and a
			// stale precondition are both requests the caller can correct.
			return Invalid("%s", he.Msg)
		default:
			return Unavailable("%s", he.Msg)
		}
	}
	return Unavailable("the request could not be completed")
}

// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// errmap.go — the ONE place a Go error becomes an HTTP status.
//
// zip renders 500 for anything that is not a *zip.HTTPError, so a clean refusal
// reached the browser as an internal fault: commerce answers 402 out of funds,
// the gate answers 403, a plane callee answers either — and the console drew a
// DEAD CARD, because "something went wrong" is not a fact a UI can act on. There
// was nothing to branch on either: no status, no code, one sentence.
//
// The status ALWAYS EXISTED. metering.ErrInsufficientBalance means 402 and
// nothing else; a refusal that crossed the plane arrives as an *HTTPError
// carrying the number the callee chose (zip's callFault preserves it precisely
// so the crossing does not flatten it). What was missing is a client that carries
// both to the wire — so about twenty handlers remembered to call Denied and every
// other one 500'd. One fact decided in twenty places is a fact decided nowhere.
//
// It is decided HERE, once, and installed on the app (Serve). A handler
// PROPAGATES the refusal it received and the status survives; a handler that
// renders its own refusal writes a response and returns nil, and never arrives
// here at all.
//
// The BODY stays zip's own {status, code, error}. Every SDK in the fleet already
// reads that shape and `code` is exactly the machine-readable reason a paywall
// branches on — this FILLS it rather than inventing a second envelope beside it.

import (
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// refused classifies err as a refusal the CALLER can act on: the status to answer
// with, the code to answer it under, and the sentence that says how to clear it.
// ok=false means there is nothing the client can do about it — a fault or an
// outage — which each caller then answers with its own fallback.
//
// It is the one classifier BOTH renderings read — this file's ErrorHandler and
// the money wire's denial (resource_billing.go) — so a propagated error, an
// untyped handler and a typed op can never describe one refusal three ways.
func refused(err error) (*zip.HTTPError, bool) {
	// An upstream that already CHOSE a status keeps it, with its own code and its
	// own sentence: 402 from commerce, 403 from the gate, and every refusal zip
	// rebuilt from a plane callee.
	//
	// 4xx ONLY. A 5xx is not a refusal — it is the absence of a decision, and the
	// difference is the whole fail-closed contract: an unreachable commerce comes
	// back as zip's own 502 for the call, and if that read as a refusal the money
	// wire would answer it with a transport message where "Billing temporarily
	// unavailable" belongs. Every 5xx therefore falls through to each caller's own
	// fallback: 503 balance_unavailable for the money wire, its own status for the
	// renderer.
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
		return &zip.HTTPError{Status: he.Status, Code: codeFor(he.Code, he.Status), Msg: he.Msg}, true
	}
	// The money sentinels: statuses spelled as errors, which is the whole reason a
	// funded-account paywall could render as an internal fault. Each carries its
	// CURE, because a 402 that does not say how to clear it is a dead end with a
	// number on it.
	switch {
	case errors.Is(err, metering.ErrSpendCapExceeded):
		return &zip.HTTPError{
			Status: http.StatusPaymentRequired, Code: "spend_cap_exceeded",
			Msg: "Spend cap reached for this scope. Raise it at console.hanzo.ai/limits",
		}, true
	case errors.Is(err, metering.ErrInsufficientBalance):
		return &zip.HTTPError{
			Status: http.StatusPaymentRequired, Code: "insufficient_balance",
			Msg: "Add credits at console.hanzo.ai",
		}, true
	}
	return nil, false
}

// codeFor keeps an upstream's own code and, where it sent none, names the refusal
// by its status — but only for the three a client BRANCHES on: pay, sign in, ask
// for access. Any other status keeps an empty code rather than gaining a
// vocabulary nobody agreed to.
func codeFor(code string, status int) string {
	if code != "" {
		return code
	}
	switch status {
	case http.StatusPaymentRequired:
		return "payment_required"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	}
	return ""
}

// internalFault is what a 500 says when nobody decided anything.
//
// The rendered body used to be err.Error() verbatim, which is how a customer's
// first fault told them about ZapDB migrations, CLOUD_KMS_MASTER_KEY_REF,
// http://iam.hanzo.svc and features that are "not yet implemented". None of that
// is theirs to act on and all of it is ours to know: an undecided error is text
// written for an operator, and putting it on the wire published our internals to
// whoever tripped it.
//
// The rule is provenance, not status. An *HTTPError or a *fiber.Error was
// CONSTRUCTED by a handler that chose a status AND a sentence — a decision the
// client does not second-guess, which is what keeps "Billing temporarily
// unavailable" readable. An error that arrives having chosen neither gets this
// sentence instead, and its detail goes to the log (ErrorHandler), keyed by the
// X-Request-Id the response carries so support can find the one line that matters.
const internalFault = "Something went wrong on our side. The request id in this response's X-Request-Id header identifies it to support."

// mapError is the whole rule: every error to exactly one {status, code, message}.
// Past the refusals it is zip's own behaviour, kept identical so installing this
// changes what a REFUSAL renders as and nothing else.
func mapError(err error) *zip.HTTPError {
	if he, ok := refused(err); ok {
		return he
	}
	if he, ok := errors.AsType[*zip.HTTPError](err); ok {
		out := *he // a copy: the error may be a sentinel its owner still holds
		if out.Status == 0 {
			out.Status = http.StatusInternalServerError
		}
		return &out
	}
	if fe, ok := errors.AsType[*fiber.Error](err); ok {
		return &zip.HTTPError{Status: fe.Code, Code: codeFor("", fe.Code), Msg: fe.Message}
	}
	// UNDECIDED: nobody chose a status, so nobody chose a sentence either. The
	// status is the honest part and it stays; the text does not.
	return &zip.HTTPError{Status: http.StatusInternalServerError, Msg: internalFault}
}

// faultLog is where a 5xx's detail goes now that it no longer goes to the client.
// Package-scoped so rendering an error costs no logger construction.
var faultLog = luxlog.New("cloud").New("subsystem", "errmap")

// ErrorHandler renders any error a handler propagates. Give it to zip.Config so
// the app has one, rather than the default that reads only *zip.HTTPError.
//
// It is also the ONE place a fault is recorded. Every 5xx is logged whole —
// including the ones whose sentence DID survive to the wire, because an operator
// wants the wrapped chain and the client only ever sees the outermost sentence —
// with the request id that ties the log line to the response the caller holds.
func ErrorHandler(c fiber.Ctx, err error) error {
	he := mapError(err)
	if he.Status >= 500 {
		faultLog.Error("request failed",
			"status", he.Status,
			"method", c.Method(),
			"path", c.Path(),
			"request_id", c.Get("X-Request-Id"),
			"err", err)
	}
	// A handler that CHOSE its sentence is not second-guessed about what it says —
	// but a chosen sentence routinely quotes text this process did not write. The
	// fleet's handlers wrap an upstream's refusal into their own message
	// ("generate: %s", "custody: %v", "scan extraction failed: %s"), and a provider
	// refusing a call quotes the credential it refused back at us. So the WORDS are
	// the handler's and a credential-shaped token in them is nobody's.
	//
	// This is the one place every propagated error is rendered, which is why the
	// scrub belongs here rather than at each of the several hundred sites that
	// compose a sentence: a site added tomorrow is covered without anyone
	// remembering. The log above is deliberately left WHOLE — an operator needs the
	// wrapped chain, and it is not what leaves the process.
	//
	// Copied first: mapError's refusal arm may return a sentinel its owner still
	// holds, and rewriting that would corrupt every later rendering of it.
	out := *he
	out.Msg = ScrubText(out.Msg)
	c.Status(out.Status)
	return c.JSON(&out)
}

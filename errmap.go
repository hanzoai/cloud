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
// so the crossing does not flatten it). What was missing is a seam that carries
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

// mapError is the whole rule: every error to exactly one {status, code, message}.
// Past the refusals it is zip's own behaviour, kept identical so installing this
// changes what a REFUSAL renders as and nothing else.
func mapError(err error) *zip.HTTPError {
	if he, ok := refused(err); ok {
		return he
	}
	var he *zip.HTTPError
	if errors.As(err, &he) {
		out := *he // a copy: the error may be a sentinel its owner still holds
		if out.Status == 0 {
			out.Status = http.StatusInternalServerError
		}
		return &out
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return &zip.HTTPError{Status: fe.Code, Code: codeFor("", fe.Code), Msg: fe.Message}
	}
	return &zip.HTTPError{Status: http.StatusInternalServerError, Msg: err.Error()}
}

// ErrorHandler renders any error a handler propagates. Give it to zip.Config so
// the app has one, rather than the default that reads only *zip.HTTPError.
func ErrorHandler(c fiber.Ctx, err error) error {
	he := mapError(err)
	c.Status(he.Status)
	return c.JSON(he)
}

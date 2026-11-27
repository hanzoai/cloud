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
// The BODY stays zip's own: the RFC 9457 problem members, whose `code` is exactly
// the machine-readable reason a paywall branches on — this FILLS it rather than
// inventing a second envelope beside it. errmap_reserved_test.go measures that key
// set against the pinned zip, so no comment here has to assert it.

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/fault"
	"github.com/hanzoai/cloud/sdk/mcp"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/supply"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// billingPages are the console pages that clear each money refusal, by the code a
// client branches on: out of funds is cleared by adding credit, and so is a spent
// Free-plan allowance (credit makes the caller a payer); a spend cap by raising the
// budget, and "no subscription and no credit" on the billing page that offers both.
// A brand with a checkout of its own takes credit there instead (BillingURL).
var billingPages = map[string]string{
	"insufficient_balance": "/billing/credits",
	"allowance_spent":      "/billing/credits",
	"spend_cap_exceeded":   "/billing/budgets",
	"payment_required":     "/billing",
}

// BillingURL is where a person clears a refusal with this code, on the brand host
// belongs to, so a Lux caller is sent to Lux and never to Hanzo. A refusal money
// clears (funded) is cleared where the brand takes credit — its checkout's amount
// step (brand.AddCredit), hanzo.ai/pay/amount/card for Hanzo, the same page every
// Add credits in its apps opens — and on its console's credits page where it has no
// checkout; a spend cap on the console's budgets page. It is "" for a code no
// billing page clears.
//
// An empty host is a refusal with no request behind it — an in-process CLI call —
// and names this deployment's own brand. A brand that runs no console is sent
// nowhere: a link to a host that does not answer is worse than none.
func BillingURL(host, code string) string {
	page, ok := billingPages[code]
	if !ok {
		return ""
	}
	if funded[code] {
		if url := brand.AddCredit(own(host)); url != "" {
			return url
		}
	}
	return consolePage(host, page)
}

// KeysURL is where a caller nobody identified gets the key a 401 asks for: the
// API-keys page on the console of the brand host belongs to, "" for a brand that
// runs no console.
func KeysURL(host string) string { return consolePage(host, "/api-keys") }

// AgentURL is the agent manifest of the brand host belongs to (brand.Agent): the
// one document that tells an agent how to get a key, pay and call this API with
// no person. "" for a host no brand claims, so a refusal on one names nothing.
// An empty host is a refusal with no request behind it, and names this
// deployment's own, as KeysURL does.
func AgentURL(host string) string {
	b, _ := brand.Agent(own(host))
	return b.Agent
}

// TopupPath is where a caller holding a key funds its balance over the API with
// no person (apps/billing rails.go). Relative, because it is the API that
// refused the caller: the same deployment, under whichever host the caller asked.
//
// It names the Circle Gateway terms rather than the deposit-address menu, and the
// difference is which rail pays. A deposit address is minted whether or not
// anything watches its asset, and only a watched asset is ever credited — so a
// caller sent to the menu can be handed a real address and lose what it sends. The
// Gateway terms name the treasury, the networks Circle is serving now, and the
// EIP-712 payment to sign; the USDC Gateway locks is what credits, so the one path
// an agent can follow alone is this one. The deposit rail is still offered beside
// it for a caller that has an address to send to.
const TopupPath = "/v1/billing/crypto/gateway"

// funded are the refusals a deposit clears: no balance, a spent Free-plan
// allowance, and no subscription with no credit. A spend cap is not one; money
// does not raise a ceiling the account set on itself.
var funded = map[string]bool{
	"insufficient_balance": true,
	"allowance_spent":      true,
	"payment_required":     true,
}

// guided adds what an AGENT needs beside the page a person would open, on every
// refusal it can clear by itself — a 401 nobody was identified on by getting a
// key, a 402 by paying, a 429 by waiting or paying: `agent`, the brand's
// manifest, and where money clears the refusal, `topup`. One rule for all three,
// so a caller that meets any of them reads the same members in the same place. A
// 401 an upstream coded for itself (a step-up) is about a credential the caller
// already holds, and is left as it is, exactly as keysUrl leaves it.
func guided(he *zip.HTTPError, host string) *zip.HTTPError {
	switch {
	case he.Status == http.StatusUnauthorized && he.Code == "unauthorized":
	case he.Status == http.StatusPaymentRequired, he.Status == http.StatusTooManyRequests:
	default:
		return he
	}
	if funded[he.Code] {
		he = he.With(map[string]any{"topup": TopupPath})
	}
	return linked(he, "agent", AgentURL(host))
}

// consolePage is page on the console of the brand host belongs to.
func consolePage(host, page string) string { return brand.ConsolePage(own(host), page) }

// own is host, or this deployment's own domain for a refusal with no request
// behind it.
func own(host string) string {
	if host == "" {
		return Domain()
	}
	return host
}

// At ends a refusal's sentence with the page that clears it, when there is one.
func At(sentence, url string) string {
	if url == "" {
		return sentence
	}
	return sentence + " at " + url
}

// refused classifies err as a refusal the CALLER can act on: the status to answer
// with, the code to answer it under, and the sentence that says how to clear it.
// ok=false means there is nothing the client can do about it — a fault or an
// outage — which each caller then answers with its own fallback.
//
// host is the request's, and only decides WHERE a refusal sends the caller: a
// money refusal to its billing page (BillingURL), riding as `billingUrl`, and a
// caller nobody identified to the page that mints a key (KeysURL), riding as
// `keysUrl`. Beside the page, an agent is pointed at its brand's manifest and,
// where money clears it, at the API that takes a deposit (guided). A sentence
// this file writes names the page too. An upstream's sentence is its own and is
// not rewritten.
//
// It is the one classifier BOTH renderings read — this file's ErrorHandler and
// the money wire's denial (resource_billing.go) — so a propagated error, an
// untyped handler and a typed op can never describe one refusal three ways.
func refused(err error, host string) (*zip.HTTPError, bool) {
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
		code := codeFor(he.Code, he.Status)
		// What the refusal carries besides its message travels with it: a window that
		// is spent names itself, when it resets and where to raise it.
		out := (&zip.HTTPError{Status: he.Status, Code: code, Msg: he.Msg}).With(he.Detail)
		if code == "unauthorized" {
			return guided(linked(out, "keysUrl", KeysURL(host)), host), true
		}
		return guided(linked(out, "billingUrl", BillingURL(host, code)), host), true
	}
	// The money sentinels: statuses spelled as errors, which is the whole reason a
	// funded-account paywall could render as an internal fault. Each carries its
	// CURE, because a 402 that does not say how to clear it is a dead end with a
	// number on it.
	switch {
	case errors.Is(err, meter.ErrSpendCapExceeded):
		url := BillingURL(host, "spend_cap_exceeded")
		return guided(linked(&zip.HTTPError{
			Status: http.StatusPaymentRequired, Code: "spend_cap_exceeded",
			Msg: At("Spend cap reached for this scope. Raise it", url),
		}, "billingUrl", url), host), true
	case errors.Is(err, meter.ErrInsufficientBalance):
		url := BillingURL(host, "insufficient_balance")
		return guided(linked(&zip.HTTPError{
			Status: http.StatusPaymentRequired, Code: "insufficient_balance",
			Msg: At("Add credits", url),
		}, "billingUrl", url), host), true
	}
	return nil, false
}

// unchecked classifies a spend cap nobody could read. It is not a refusal the
// caller can clear and not an undecided fault either: the gate DECIDED not to run
// the request because the ceiling it must stay under could not be checked, so it
// answers a 503 that says so, under a code a client can branch on, and the cure is
// a retry. Both renderings read it, as they read refused.
func unchecked(err error) (*zip.HTTPError, bool) {
	if !errors.Is(err, meter.ErrSpendCapUnknown) {
		return nil, false
	}
	return &zip.HTTPError{
		Status: http.StatusServiceUnavailable, Code: "spend_cap_unavailable",
		Msg: "The spend cap on this account could not be checked, so the request was not run. Retry shortly.",
	}, true
}

// linked carries the page that clears a refusal as a member of the answer, so a
// client follows a field rather than parsing a sentence.
func linked(he *zip.HTTPError, member, url string) *zip.HTTPError {
	if url == "" {
		return he
	}
	return he.With(map[string]any{member: url})
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
func mapError(err error, host string) *zip.HTTPError {
	if he, ok := refused(err, host); ok {
		return he
	}
	if he, ok := unchecked(err); ok {
		return he
	}
	// A provider's budget stop is its own class, decided by whoever read the
	// provider's refusal: 503 budget_exhausted naming the layer that stopped
	// (sdk/supply), so a client tells it from capacity, sign-in and a fault.
	if ex, ok := supply.As(err); ok {
		return ex.HTTPError()
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
	// The host the request was sent to, off its own Host header: a forwarded one
	// is the caller's to write, and both the brand pages and the challenge below
	// are built from this.
	host := string(c.Request().Header.Host())
	he := mapError(err, host)
	route := c.Path()
	if r := c.Route(); r != nil && r.Path != "" {
		route = r.Path
	}
	// EVERY NON-2XX HAS A KEY. The fault is built here, from the status and code
	// the response will carry and the error behind them, so a refusal, a 4xx a
	// handler chose and a 5xx nobody did all read the same way: a code, a route
	// pattern, a site and a sentence. Its fingerprint rides the body below.
	f := fault.Request(c.Method(), route, he.Status, he.Code, err)
	f.Request = c.Get("X-Request-Id")
	if he.Status >= 500 {
		faultLog.Error("request failed",
			"status", he.Status,
			"method", c.Method(),
			"path", c.Path(),
			"request_id", f.Request,
			"fingerprint", f.Fingerprint(),
			"err", err)
		// A 5xx is ours, so it is also stated on the event plane, where it is
		// grouped by this fingerprint and counted (internal/host/faults.go).
		ReportFault(f)
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
	out.Msg = scrubMessage(out.Status, out.Msg)
	// A refusal always says why: a status with no sentence is read as the status's
	// own phrase, never as an empty reason.
	if strings.TrimSpace(out.Msg) == "" {
		out.Msg = http.StatusText(out.Status)
	}
	// The key rides the body as an extension member (RFC 9457 §3.2), so a client
	// that reports a failure, a person who writes to support and an issue the
	// platform files name one thing. The member map is copied: the refusal may be
	// a sentinel its owner still holds.
	detail := make(map[string]any, len(out.Detail)+1)
	for k, v := range out.Detail {
		detail[k] = v
	}
	detail["fingerprint"] = f.Fingerprint()
	out.Detail = detail
	// A 401 names the scheme that would have been accepted (RFC 9110 §11.6.1),
	// and where to learn how to get one: the resource metadata this host serves
	// (RFC 9728 §5.1), which names the authorization server. Without it a client
	// that met a 401 had to know hanzo.id already (hanzo-inc/cloud#420). The
	// origin is the request's own (brand.Origin of its Host header), as the MCP
	// challenge's is (client/mcp.go), so a white-label host never points a client at
	// a host it did not call, and a forwarded header names nothing here. A handler
	// that issued its own challenge — step-up, an MCP resource pointer — has said
	// something more specific, and keeps it.
	if out.Status == http.StatusUnauthorized && c.GetRespHeader("WWW-Authenticate") == "" {
		c.Set("WWW-Authenticate", `Bearer resource_metadata="`+brand.Origin(host)+mcp.Metadata+`"`)
	}
	c.Status(out.Status)
	return c.JSON(&out)
}

// A card that needs its bank's authentication is answered 402 with the step the
// buyer runs: the PaymentIntent (or SetupIntent) id and its client secret, which
// Stripe.js needs to open the challenge. The client secret is the buyer's own,
// minted for this one intent, and it is the whole payload; ScrubText rewrites it
// to [REDACTED-TOKEN] because it has the shape of a credential, and a buyer who
// is sent "[REDACTED-TOKEN]" cannot authenticate.
//
// So this one message is rebuilt from fields that each match Stripe's own shape,
// and nothing else is exempt: the id, a secret that is that id's secret, and the
// sentence, which is scrubbed as every sentence is. Anything that fails the shape
// is scrubbed whole.
var (
	stepID     = regexp.MustCompile(`^(pi|seti)_[A-Za-z0-9]+$`)
	stepSecret = regexp.MustCompile(`^(pi|seti)_[A-Za-z0-9]+_secret_[A-Za-z0-9]+$`)
)

type authenticationStep struct {
	Code         string `json:"code"`
	ID           string `json:"id"`
	ClientSecret string `json:"clientSecret"`
	Message      string `json:"message"`
}

func scrubMessage(status int, msg string) string {
	if status != http.StatusPaymentRequired || !strings.HasPrefix(msg, `{"code":"authentication_required"`) {
		return ScrubText(msg)
	}
	var st authenticationStep
	if err := json.Unmarshal([]byte(msg), &st); err != nil || st.Code != "authentication_required" ||
		!stepID.MatchString(st.ID) || !stepSecret.MatchString(st.ClientSecret) ||
		!strings.HasPrefix(st.ClientSecret, st.ID+"_secret_") {
		return ScrubText(msg)
	}
	st.Message = ScrubText(st.Message)
	b, err := json.Marshal(st)
	if err != nil {
		return ScrubText(msg)
	}
	return string(b)
}

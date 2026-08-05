// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// risk.go — the credit door is SCREENED, and the scorer that screens it lives in
// another process.
//
// Two halves of one seam, and they belong in one file because neither is
// intelligible without the other:
//
//	the CLIENT — cloud.SetRiskScorer's first producer. Package cloud has held
//	the seam and the fail policy since it was written and has never had anyone
//	to ask: the model is in-process mutable state, so exactly one binary may
//	hold it, and the pod forks one process per app. Every gate in the fleet
//	therefore read a nil scorer and allowed, unscored. This installs one that
//	reaches the risk child over its socket.
//
//	the GATE — the one place that asks. A settled card charge is its own mint
//	authority, so a stolen card that clears is money in an account, and the account
//	is what buys inference. It is the sharpest lifecycle moment this binary owns.
//
// THERE ARE TWO DOORS ONTO THAT MINT AND THE GATE HOLDS BOTH. commerce has exactly
// ONE card money move (billing.TakePayment) and this binary opens two addresses onto
// it — the browser's POST /v1/billing/topup/token and the agent's typed
// POST /v1/payments (payments.go), which the module note there calls "the same core
// the console's card top-up runs". A screen on one of them is not a control: it is a
// control standing beside an unscreened entrance to the same ledger write, and the
// unscreened one is the one an agent already holds an MCP tool for. So the gate is a
// zip.Middleware and mount.go composes it onto BOTH registrations; ONE decision, ONE
// payer rule, ONE settlement key, two addresses.
//
// THE GATE IS PRIVILEGED AND SAYS SO IN SO MANY WORDS. cloud.Privileged() reads a
// list of grant PATHS and neither route is on it, so the default would be
// the fail-OPEN branch — a scorer outage would wave every payment through, on the
// only two routes where waving one through mints spendable balance. The bit is set here
// rather than added to that list because the list describes IAM and KMS surfaces
// and this is neither; the gate that knows what it is guarding states it.
//
// IT SHIPS IN SHADOW, and that is a property of the MODEL rather than of this
// code. A model nobody has reviewed is in shadow (apps/risk policy), shadow forces
// its alert false however high the score, and this gate turns a non-alert into an
// allow. So today every legitimate payment proceeds at either door and every decision
// is on the record with what the model WOULD have said. What still refuses is a scorer
// that is HERE and cannot answer — the fail-closed branch this bit exists to select.
// Widening the gate to the second door widens the RECORD, not the enforcement: no
// organisation is armed by this, and the default regime is untouched.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	accountclient "github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	riskpeer "github.com/hanzoai/cloud/plane/risk"
)

// installRiskScorer publishes the ONE scorer to package cloud's seam. Mount calls
// it, beside the ledger ops, because both are this process telling the fleet what
// it can reach through it.
//
// It is installed unconditionally: the seam's own fail policy already answers for
// a scorer that cannot be reached, and installing conditionally would mean asking
// at mount time a question whose answer changes every time the risk child starts
// or stops.
func installRiskScorer(lg log.Logger) {
	cloud.SetRiskScorer(func(ctx context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return scoreOverPlane(ctx, lg, org, q)
	})
	// THE UNARMED AXIS IS ANNOUNCED, ONCE, AT BOOT. A rule half that cannot fire
	// because nothing states its axis is indistinguishable from a rule half that
	// found nothing, and the second reads as a clean bill of health. Said here, where
	// the seam is published, so it is on the record before any payment needs it —
	// which is the same reason apps/risk announces a jurisdiction listing it cannot
	// use at mount rather than on the first decision that wanted one.
	lg.Info("credit door risk axes", "armed", paymentAxes, "unarmed", paymentUnarmed,
		"doors", []string{"/v1/billing/topup/token", paymentsPrefix},
		"why", "no device fingerprint reaches this binary from a card payment")
}

// paymentAxes is exactly what a credit door STATES, and [paymentUnarmed] exactly what
// it does not. They are stated rather than left to be inferred from
// [paymentSignals] because the difference decides which halves of the risk rule can
// fire at these doors, and a half that cannot fire must never be mistaken for a half
// that found nothing. A test holds [paymentSignals] to these two lists, so the
// declaration cannot drift from the doors.
//
// ONE LIST FOR BOTH DOORS, because both send the same body: the browser's top-up and
// the agent's typed payment each carry a card token, an amount and a currency under
// the SAME field names, so [paymentSignals] reads either one and the axes it can
// state do not depend on which address was called.
var paymentAxes = []string{plane.SignalNano, plane.SignalCountry, plane.SignalPeer}

// paymentUnarmed is the DEVICE axis, and the reason is a fact about the payment path
// rather than a decision of this file's.
//
// No device fingerprint reaches this binary from a card payment at either door. The
// request body is a card token, an amount and a currency; the card is tokenised in the
// browser and its number never arrives here; the browser does not run the payment SDK's
// buyer-verification step, so there is no verification token either; and no header
// carries a device id. An agent calling the typed door has less still. There is nothing
// to state.
//
// AND NOTHING IS INVENTED IN ITS PLACE. A user-agent string — or a digest of the
// request's headers — is shared by millions of unrelated people, so stated as a
// device it would put ordinary customers past the fan-out bound and summon a person
// for every payment: the same control useless in the louder direction, and wearing
// the name of a fingerprint while holding nothing of the kind. The axis stays unarmed
// and says so. When a real fingerprint is collected, [paymentSignals] states it in one
// line and this list loses an entry.
//
// The device half of the fan-out is not dead everywhere: POST /v1/risk/learn takes a
// device from a caller that has one, so the rule is exercised where a real value
// exists.
var paymentUnarmed = []string{plane.SignalDevice}

// scoreOverPlane asks the risk child, and translates the two vocabularies.
//
// THE TENANT IS STATED, NOT FORWARDED. cloud.For on a context with no request
// behind it is the one place zip reads a stated caller; on a request-derived
// context zip prefers the gateway's assertion and returns before it looks, so the
// org resolved by the gate — which for a SuperAdmin acting in another org is not
// the inbound assertion — would be silently replaced by the inbound one. Same
// correction apps/x402's peerCtx makes, for the same reason: the org that PAYS is
// not always the org that asked.
//
// THE BUDGET IS THE SEAM'S. cloud.Decide answers at RiskBudget whatever this
// returns, so a hop bounded any longer would only hold one of the seam's 256
// slots past the point where its answer could still be used.
func scoreOverPlane(ctx context.Context, lg log.Logger, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
	// NOT LISTENING IS NOT AN OUTAGE, and telling the two apart is the whole
	// reason this is a probe and not just a call.
	//
	// The fleet starts 106 of its apps lazily: an app is brought up by a request
	// reaching its prefix, and nothing reaches risk's. So the steady state of a
	// fresh pod is a scorer that is not there yet — which, asked synchronously,
	// costs a child's whole startup inside a 150ms budget, times out, and (being
	// privileged) REFUSES THE TOP-UP. Every cold start would take the credit door
	// down for the first customer to reach it.
	//
	// A socket with no listener is exactly cloud's ABSENT fact — no scorer here,
	// allow and say so — so it is answered as one, and the child is brought up off
	// the request path for the next caller. A socket that IS there and does not
	// answer stays an outage and still denies, which is the fact this gate exists
	// to fail closed on.
	//
	// AND THE PROBE ITSELF CAN FAIL, which is a THIRD fact and not either of those
	// two. [plane.Listening] reports ENOENT and ECONNREFUSED as "no listener" and
	// returns every other dial error UNTOUCHED, precisely so that a socket which is
	// present and unusable is never read as one that is absent. Under fd exhaustion
	// (EMFILE), a mode that denies the dial (EACCES) or a run directory whose path
	// has grown past the sun_path bound (ENAMETOOLONG), the probe fails in
	// microseconds against a scorer that is perfectly healthy — so the budget below
	// never catches it, and discarding the error here would answer ABSENT and wave
	// the top-up through. That is the fail-open this gate exists to close, reachable
	// by putting the commerce process under fd pressure.
	up, err := scorerUp()
	if err != nil {
		// The judge's door is THERE and this process cannot use it. An outage, handed
		// to the seam as one — [cloud.Decide] renders it RefusalError, and the fail
		// policy denies it because the query is privileged.
		return cloud.RiskVerdict{}, fmt.Errorf("risk: the scorer's socket is unusable: %w", err)
	}
	if !up {
		wakeScorer(lg)
		return cloud.RiskUnavailable(q, cloud.RefusalAbsent), nil
	}
	cctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), cloud.RiskBudget)
	defer cancel()

	out, err := riskpeer.RiskDecide(cctx, &plane.RiskDecideIn{
		Stage:   q.Stage,
		Kind:    q.Subject.Kind,
		Subject: q.Subject.ID,
		Signals: signalsOf(q.Signals),
	})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		// The router owns the manifest and it says this fleet runs no risk app.
		// That is the absent fact again, decided by the only thing that can decide
		// it — never guessed from a failed call.
		return cloud.RiskUnavailable(q, cloud.RefusalAbsent), nil
	case err != nil:
		return cloud.RiskVerdict{}, fmt.Errorf("risk: decide over the plane: %w", err)
	case out == nil:
		// A void reply from the only thing that can judge is not a judgement.
		return cloud.RiskVerdict{}, fmt.Errorf("risk: the scorer answered nothing")
	}
	// The AGENCY is not carried in either direction: this scorer has no opinion on
	// which lane a caller is in, so the asking gate's own lane stands. An empty
	// Agency here is what says that.
	return cloud.RiskVerdict{
		Action:  out.Action,
		Score:   out.Score,
		Cause:   out.Cause,
		Refusal: out.Refusal,
		Shape:   out.Shape,
		Policy:  out.Policy,
	}, nil
}

// signalsOf puts the gate's observations on the wire. A map cannot cross the
// plane at all — zapenc refuses one at encode — so they travel as a list, SORTED,
// because an unordered wire is one that cannot be compared with itself.
func signalsOf(facts map[string]string) []plane.Signal {
	if len(facts) == 0 {
		return nil
	}
	out := make([]plane.Signal, 0, len(facts))
	for name, value := range facts {
		out = append(out, plane.Signal{Name: name, Value: value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// scorerUp reports whether the risk child's socket has a LISTENER behind it right
// now. It connects, because the file does not answer the question: a socket left
// by a dead pod outlives it, and for a lazily started app that difference is the
// whole answer.
//
// IT RETURNS THE ERROR, and that is the whole of it. [plane.Listening] separates
// three facts and this is a pass-through of that separation: no listener (false,
// nil), a listener (true, nil), and a socket that is present and UNUSABLE (false,
// err). Folding the third into the first — `return err == nil && up` — is the one
// line that turns an outage into an absence, and absence is the exemption the fail
// policy grants a privileged grant. This is the same contract [plane.Reach] keeps
// for the router's own start door, for the same reason.
func scorerUp() (bool, error) {
	plane.Bind()
	return plane.Listening(zip.SocketPath(riskpeer.App))
}

// waking holds the ONE start request in flight. The host single-flights the start
// itself, so this bounds the goroutines rather than the starts: without it a burst
// of top-ups against a cold scorer would spawn one waiter each.
var waking atomic.Bool

// wakeScorer brings the risk child up OFF the request path. The caller has
// already been answered — absent, allowed, on the record — so this exists only to
// make the NEXT decision a real one.
func wakeScorer(lg log.Logger) {
	if !waking.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer waking.Store(false)
		// Reach applies the host's own plugin-start budget. A fleet that runs no
		// risk app answers ErrNoPeer immediately and this is a no-op that repeats
		// at most once per decision, one at a time.
		if err := plane.Reach(context.Background(), riskpeer.App); err != nil {
			lg.Debug("commerce: the risk scorer could not be started", "err", err)
		}
	}()
}

// ── the gate ─────────────────────────────────────────────────────────────────

// riskGate screens one credit-door request and refuses what the scorer will not
// have.
//
// IT GUARDS EVERY DOOR ONTO THE MINT, and there are two. commerce holds ONE card
// money move (commerce billing.TakePayment) and this binary opens two addresses
// onto it: the browser's POST /v1/billing/topup/token and the agent's typed
// POST /v1/payments (payments.go). Both end in the same authorized deposit, so a
// screen on one of them is not a control — it is a control with a second door
// beside it, and the second door is the one an agent already holds a tool for.
// One gate, both doors, stated at the composition root (mount.go) where each
// address is registered.
//
// IT IS A MIDDLEWARE AND NOT A HANDLER, and that is what lets it reach the second
// door at all. A typed op is registered as a CONTRACT rather than as a chain
// (zip.Post takes the handler, not a handler list), so there is no c.Next() for a
// screen to sit in front of — zip composes middleware around a typed op at
// registration (zip.With). Holding its own `next` is therefore the ONE form that
// gates both an untyped chain and a typed op; the alternative was a second
// spelling of this decision for the typed door, which is two screens to drift.
//
// WHERE IT SITS IS PART OF WHAT IT IS. On the browser door it runs AFTER
// PinBillingSubject, which is what makes the subject it judges the subject the
// charge will credit: that middleware refuses every caller that is neither a
// validated customer nor the trusted service token, and pins the credited subject
// to the caller's own. A screen placed before it would judge a subject a later
// middleware could still change, which is a control on a value rather than on an
// act. The typed door pins nothing — it reads the validated tenant off the context
// and names no subject a caller could send — so there is nothing there to run
// after; what both doors share is [payerOrg] and [principal.Subject], the ONE
// payer rule, which is why an accrual reached through either of them lands on one
// key. See [teachSettlement].
//
// It refuses in TWO different sentences because they are two different facts, and
// a customer can act on only one of them:
//
//	the scorer is here and could not answer — 503, retry. The model exists, this
//	question went unanswered, and a privileged grant waits rather than proceeds.
//	It is an operational fact, so saying it is honest and useful.
//
//	the SCREEN DECIDED against it — 403, and nothing more. A risk reason handed
//	back to whoever triggered it is a feedback channel for tuning the next
//	attempt. The reason is written to the log with the shape and policy version
//	that produced it, which is where an operator reads it.
//
// WHICH OF THE TWO IT IS, IS READ OFF THE ACTION AND THE REFUSAL TOGETHER — never
// off the refusal alone. A decision is the screen's whenever anything decided it,
// and after the rule and the model were fused that includes a determination made
// over stated facts while the model itself had no opinion to offer. Such a verdict
// carries BOTH a decided action and the model's own refusal, and only the pair
// tells it apart from the one the fail policy invented.
//
// It is never a 402. Out of funds is what a 402 means at this door and this is
// not that — the whole point of the door is that the caller has no funds yet.
func riskGate(lg log.Logger) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			org := payerOrg(c)
			// THE SUBJECT IS RESOLVED ONCE and used by both halves of this middleware — the
			// screen below and the record after it. A screen that judged one subject while
			// the record taught another would be two subjects, and the velocity of the one
			// being judged would be empty forever however many payments settled.
			subject := principal.Subject(c, org)
			facts := paymentSignals(c)
			v := cloud.Decide(c.Context(), org, cloud.RiskQuery{
				Stage: cloud.StagePayment,
				// THE PAYER, not the account. They are different populations and this door
				// judges the first: an account's learned history is its metered inference
				// spend, so screening a payment as an account compares money moving IN
				// against a distribution of money spent OUT — and the windowed value bounds
				// the aggregate rule reads are a PAYMENTS appetite accruing on the very same
				// key. A customer with a large inference bill was examined for it. See
				// [plane.KindPayer].
				Subject: cloud.RiskSubject{Kind: plane.KindPayer, ID: subject},
				// STATED, never derived. cloud.Privileged() does not match either of the
				// two routes this gate holds and the default is fail-open, so an unset bit
				// here is a scorer outage minting balance.
				Privileged: true,
				Signals:    cloud.Facts(facts),
			})
			// EVERY decision is recorded, including the allows — an unscored allow and a
			// clean one are different rows, and the refusal is what tells them apart. The
			// PATH is on the row because one gate now answers for two addresses, and a
			// record that does not say which door a decision was reached at cannot be read
			// back against the traffic that produced it.
			lg.Info("credit door screened",
				"door", c.Path(),
				"org", org, "action", v.Action, "scored", v.Scored(), "refusal", v.Refusal,
				"cause", v.Cause, "score", v.Score, "shape", v.Shape, "policy", v.Policy)
			if v.Allowed() {
				if err := next(c); err != nil {
					return err
				}
				// AND WHAT ACTUALLY HAPPENED GOES BACK TO THE MODEL. The screen above reads
				// history; this is the only thing in the fleet that WRITES any. It runs after
				// the handler because a settlement is a fact about the past, and only the
				// handler's own answer says whether there was one.
				teachSettlement(c, lg, org, subject, facts)
				return nil
			}
			// A NO-DECISION IS A BLOCK CARRYING A REFUSAL, and both halves are the test.
			// [cloud.riskUnavailable] is the ONLY producer of that pair — it is what the
			// fail policy returns for a privileged grant the scorer could not answer — and
			// a scored verdict never carries a refusal at all. So this is the whole of "the
			// judge is here and did not answer", and nothing else reaches it.
			//
			// THE REFUSAL ALONE IS NOT THE TEST, because Action and Refusal became
			// INDEPENDENT the moment the rule and the model were fused: the severest of the
			// two stands, and the model's own refusal is carried beside it. An armed
			// organisation whose model is still warming, on a payment the rule froze,
			// answers {restrict, "warming"} — a DETERMINATION, reached from stated facts,
			// with the model merely having had no opinion to add. Read off the refusal that
			// is a 503 "try again in a moment", which invites the retry that settles the
			// payment the rule just froze, and reports a working control as an outage.
			if v.Action == cloud.ActionBlock && v.Refusal != "" {
				return zip.Errorf(http.StatusServiceUnavailable,
					"the payment screen could not answer (%s) — try again in a moment", v.Refusal)
			}
			// Block, challenge and restrict all land here. Neither door has a way to
			// present a challenge and neither has a reduced ceiling to fall back to, so
			// anything short of "proceed" is a refusal — never a quiet proceed.
			return zip.ErrForbidden("this payment was not authorised")
		}
	}
}

// screenChain is the screen in a CHAIN position, and it exists because the two doors
// are registered two different ways — not because they are screened two different ways.
//
// A raw route takes a handler LIST and each handler continues with c.Next(); a typed op
// takes no list at all and is composed at registration (zip.With). [riskGate] holds its
// own `next` so it can serve the second form, and this hands it the first form's `next`,
// which is the chain. One line, one decision, no second screen.
//
// The raw route also has to stay registered on cloud's own Router rather than on a
// With-decorated one, and that is a zipdoc constraint rather than a routing one:
// registering a raw money route through a resolvable wrapper makes zipdoc lift the FIRST
// HANDLER's doc comment as the route's description, so the top-up's published prose
// became RequireCSRF's. A gate must not be able to rewrite the document.
func screenChain(screen zip.Middleware) zip.Handler {
	return screen(func(c *zip.Ctx) error { return c.Next() })
}

// ── what settled ─────────────────────────────────────────────────────────────

// maxTeaching is how many settlement observations may be in flight at once. Past it
// one is DROPPED rather than queued, for [risk.emit]'s reason: a queue defers the
// loss instead of bounding it, and the memory this process holds must not be a
// function of how fast money is arriving.
const maxTeaching = 32

// teaching is that ceiling, as a counting semaphore. A send that cannot proceed is
// an answer — there is no room — and never a place to block a settled payment.
var teaching = make(chan struct{}, maxTeaching)

// teach is the ONE plane call, held in a variable for the one thing a variable buys
// here: a test can observe exactly what LEAVES this process — the subject, the kind,
// the settlement key and the signals — without standing up a risk child to receive
// it. It is never reassigned in production; the only writer is a test, and the
// compiler holds the signature to the generated client's.
var teach = riskpeer.RiskObserve

// teachBudget bounds ONE teaching call end to end, including waking the risk child.
// It is generous where the decide budget is tight because it bounds a DETACHED
// goroutine rather than a request: the customer has already been charged and
// answered, so nothing here is on anybody's critical path, and a cold risk child
// needs room to come up (plane.Ask reaches the peer and single-flights the start).
const teachBudget = 5 * time.Second

// teachSettlement tells the risk plane that a card payment SETTLED, at whichever of
// the two doors took it.
//
// # This is the only thing that teaches the credit door's rule anything
//
// The screen reads what a subject had already done; nothing was making that true.
// [plane.RiskDecide] records nothing by design, the published learn door is an
// organisation calling itself, and at a self-serve credit door the organisation IS
// the payer — so a fresh org's pace and fan-out read an empty history, which is
// precisely the subject those halves exist for. Five payments of eleven thousand
// looked like five first payments. After this, the fifth reads forty-four thousand of
// prior accrual and the sixth reads fifty-five.
//
// # Why the payer cannot steer it
//
//	IT IS DRIVEN BY THE SETTLEMENT, NOT THE REQUEST. Nothing is stated unless the
//	handler ANSWERED that money moved: the status is the handler's, the processor
//	reference is the gateway's, and both are read off the response this process
//	produced rather than off the body the customer sent. A request that fails, is
//	declined, or is refused upstream teaches nothing at all.
//
//	THE AMOUNT IS THE AMOUNT THAT SETTLED. It is the same value the screen was given
//	— which is the body's, because that is what a card door charges — but it is only
//	ever recorded once a real card actually paid it. A payer inflating it inflates
//	their own bill; a payer understating it is charged the smaller amount. Either way
//	the accrual and the money agree, which is the property a velocity bound needs.
//
//	IT CANNOT BE LOWERED. The accrual is a sum of non-negative payments
//	([risk.observe] refuses a negative), so there is no observation a payer can add
//	that reduces it, and none they can withhold: this states it, not them.
//
//	IT CANNOT BE PRE-EMPTED. The record deduplicates on the settlement id, so an id
//	the payer could guess is an id they could claim first — after which the real
//	observation is inert. The id lands in the risk plane's reserved namespace, which
//	the public learn door refuses outright.
//
// # Idempotent, on the settlement's own identifier — ACROSS BOTH DOORS
//
// Settlement is at-least-once: the door retries, a webhook replays, an event is
// redelivered. The key is therefore the PROCESSOR's reference for the charge — the
// gateway's own payment id, which is the one identifier that is the same across every
// path that can credit one payment — falling back to the ledger receipt only where
// the processor stated none. Velocity that double-counted a retry would freeze a
// customer for paying once.
//
// THAT IS ALSO WHAT MAKES THE TWO DOORS ONE ACCRUAL. Both doors return the SAME core's
// answer, so a payment taken at either one carries the same gateway payment id and
// keys the same observation: a burst split across the two accumulates on one subject
// instead of hiding half of itself behind the address that was not screened, and one
// payment sent to both doors converges rather than counting twice. The receipt
// fallback converges too — it is the ledger transaction id, which each door merely
// names differently on the wire ([settlementOf] reads both spellings) — so neither the
// primary key nor the fallback can disagree about what "the same payment" is.
//
// # It can never fail the payment
//
// The money has moved and the customer has been answered by the time this runs. So it
// is detached, bounded, dropped under pressure and panic-guarded, and its errors are
// logged and discarded — the same discipline apps/risk applies to stating a decision
// on the event plane, for the same reason. A telemetry row is expendable; a settled
// payment is not.
func teachSettlement(c *zip.Ctx, lg log.Logger, org, subject string, facts map[string]string) {
	if org == "" || subject == "" {
		return
	}
	// THE HANDLER'S OWN ANSWER IS THE SETTLEMENT FACT. A non-2xx is a payment that did
	// not credit — declined, refused, or broken — and teaching from one would tell the
	// model money moved when none did, which is a velocity bound a caller fills by
	// sending payments that fail.
	//
	// THE WHOLE 2xx BAND, not one code, and that is what carries it onto the second
	// door: the browser's top-up answers 200 and the typed payment op DECLARES 201
	// (zip.WithStatus, payments.go), so a screen that recognised only 200 would allow
	// the agent's payment, watch it settle, and teach nothing — the accrual silently
	// blind on exactly the door an agent holds a tool for.
	res := c.Fiber().Response()
	if res.StatusCode() < 200 || res.StatusCode() > 299 {
		return
	}
	ref, ok := settlementOf(res.Body())
	if !ok {
		// The door answered success and named nothing this process can key on. Say so
		// rather than minting a key: an observation under an invented id counts the same
		// money again on the next retry, which is worse than the one it did not record.
		lg.Warn("a settled payment could not be taught to the risk model: the answer named no settlement",
			"door", c.Path(), "org", org)
		return
	}
	nano := facts[plane.SignalNano]
	if nano == "" {
		// No amount this door could state in USD ([paymentSignals]). The event still
		// happened, so it is still taught — the value features read blind, which is a
		// different and honest fact from a payment of nothing.
		lg.Debug("teaching a settled payment with no stated value", "door", c.Path(), "org", org)
	}
	in := &plane.RiskObserveIn{
		Stage:      cloud.StagePayment,
		Kind:       plane.KindPayer,
		Subject:    subject,
		Settlement: ref,
		// THE SAME SIGNALS THE SCREEN WAS GIVEN, minus the ones that describe the
		// question rather than the event. The axes must match what [riskGate] asked
		// about or the screen reads a history taught under different identifiers.
		Signals: signalsOf(map[string]string{
			plane.SignalNano: nano,
			plane.SignalPeer: facts[plane.SignalPeer],
		}),
	}
	select {
	case teaching <- struct{}{}:
	default:
		lg.Debug("a settled top-up was not taught to the risk model: teachings in flight are at the ceiling",
			"ceiling", maxTeaching)
		return
	}
	// The peer call is read HERE, on the request's own goroutine, so the detached
	// goroutine below holds a VALUE rather than reading a package variable while
	// something else writes it — a seam read from a goroutine nobody joins is a seam
	// no test can put back.
	call := teach
	// THE VALUE IS BUILT BEFORE THE GOROUTINE and the context is NOT the request's,
	// for two different reasons that both point the same way. The request's context is
	// dead the moment this middleware returns, so it cannot bound the call — and it is
	// request-DERIVED, which is the one shape [cloud.For] will not restate a tenant on:
	// it prefers the gateway's inbound assertion and returns before it reads a stated
	// org, and the org that PAYS is not always the org that asked. The same correction
	// [scoreOverPlane] makes, for the same reason. What is lost is the request's trace
	// values; what would be lost otherwise is the tenant.
	go func() {
		defer func() { <-teaching }()
		defer func() {
			if r := recover(); r != nil {
				lg.Error("teaching a settled top-up panicked", "err", r)
			}
		}()
		// THE TENANT IS STATED, for [scoreOverPlane]'s reason: cloud.For on a
		// request-derived context prefers the gateway's assertion and returns before it
		// reads a stated one, and the org that PAYS is not always the org that asked.
		ask, cancel := context.WithTimeout(cloud.For(context.Background(), org), teachBudget)
		defer cancel()
		out, err := call(ask, in)
		switch {
		case err != nil:
			// Every failure is the same fact and none of them is the payment's: an absent
			// peer, a refusing one and a timed-out one all mean the model does not know
			// about this payment. There is nothing this process may do to the settlement.
			lg.Debug("a settled top-up was not taught to the risk model", "org", org, "err", err)
		case out != nil && out.Learned == 0:
			// The idempotent answer. Worth a line at debug: it is what a retried or
			// replayed settlement looks like, and it is the property working.
			lg.Debug("a settled top-up was already in the risk model's record", "org", org)
		}
	}()
}

// settlementOf reads the settlement's own identifier out of the door's answer.
//
// THE PROCESSOR'S REFERENCE FIRST, because it is the one identifier that is the same
// across every path that can credit ONE payment — the two synchronous doors and a
// replayed webhook all carry the gateway's payment id, while each writes its own ledger
// row with its own receipt. Keyed on the reference, several paths crediting one payment
// teach ONE observation; keyed on the receipt they would teach one each, and the
// velocity bound would count money that arrived once as having arrived twice.
//
// The ledger receipt is the fallback and not the default: a processor that states no
// reference still settled, and refusing to teach it would leave a real payment out of
// the accrual. Both are minted by us or by the gateway and neither is a field the
// paying customer can set.
//
// THE RECEIPT HAS TWO SPELLINGS AND THIS READS BOTH, because the two doors publish one
// value under two names: commerce's core returns TakePaymentOut.TransactionID, the
// browser door forwards that struct verbatim as `transactionId`, and the typed op
// renames it to `id` on its own PaymentOut (payments.go) — the ledger transaction id
// either way, which is why reading both is one identifier and not two. Reading only
// `transactionId` would leave the typed door with no key on any settlement whose
// processor stated no reference, and a settlement with no key is a payment that teaches
// nothing; the two cannot collide, because neither answer carries the other's field.
//
// ok=false means the answer named none of them, which is a door this process cannot key
// an idempotent record on — reported by the caller rather than papered over with a
// generated id.
func settlementOf(body []byte) (string, bool) {
	var out struct {
		ProcessorRef  string `json:"processorRef"`
		TransactionID string `json:"transactionId"`
		ID            string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false
	}
	for _, id := range []string{out.ProcessorRef, out.TransactionID, out.ID} {
		if id = strings.TrimSpace(id); id != "" {
			return id, true
		}
	}
	return "", false
}

// payerOrg is the org whose ledger this payment will credit, and therefore the
// organisation whose model judges it.
//
// The two lanes are exactly the two PinBillingSubject admits on the browser door, and
// no others reach this gate:
//
//	a validated customer — principal.Ledger, the SELECTED org that pays. It is
//	the same key the balance read and the spend gate use.
//	the trusted service token — a verified COMMERCE_SERVICE_TOKEN naming its own
//	org. It carries no validated user, so there is no ledger to resolve and the
//	org it named is the one being credited.
//
// The typed door admits the first lane only — it has no service-token branch, and its
// handler resolves the same validated tenant off the context (payments.go payingOrg) —
// so ONE resolver answers for both addresses. That is the point: an accrual is only a
// bound if both doors key it the same way, and a second resolver written for the second
// door would be two payers wearing one name.
//
// Anything else resolves to "" and the scorer refuses to mint a tenant for it, so
// a request that reaches a credit door naming no organisation is denied by the
// privileged branch rather than screened against nobody.
func payerOrg(c *zip.Ctx) string {
	if org := principal.Ledger(c); org != "" {
		return org
	}
	if accountclient.IsServiceToken(c) {
		return strings.TrimSpace(c.Org())
	}
	return ""
}

// paymentSignals is what this gate SAW, in the scorer's own vocabulary.
//
// ONE READER FOR BOTH DOORS, and it is the wire that makes that honest rather than
// convenient: commerce's top-up body and the typed op's PaymentIn declare the amount and
// the currency under the SAME field names (`amountCents`, `currency`), because both are
// projections of one core that takes one set of values. So the facts a screen can state
// do not depend on which address was called, and there is no second signal rule to drift
// out of step with the first.
//
// The amount is the fact that matters at a credit door — value velocity is the
// axis a stolen card moves — and it is the one signal the model reads as a
// coordinate. The rest are the gate's record of why it asked.
//
// THE COUNTRY IS THE ADDRESS'S, NOT THE PAYER'S, and that is a limitation this
// door cannot fix from here. The jurisdiction worth judging is the account's
// billing or KYC one, and no part of it reaches this process: the payment body is
// a Square nonce, an amount and a currency; the card is tokenised in the browser
// and its PAN never touches this binary, so there is no billing address to read;
// the validated principal carries an org, a user, a name and an email and no
// geography; and there is no customer or KYC record here holding one. Stating a
// billing country from any of that would be inventing the one fact the rule turns
// on.
//
// So this states the strongest thing that IS true — the jurisdiction our own edge
// resolved from the connecting address, and only when the peer is one of our own
// hops ([cloud.ClientCountry]) — and the scorer's rule is documented as reading a
// weak signal. It is spoofable by a VPN, which means it can be evaded DOWNWARD
// into silence; it cannot be forged upward into somebody else's freeze, and
// silence is the state this door was already in. Wiring the billing or KYC
// jurisdiction, when there is one to wire, replaces this signal at this one line.
// THE PEER IS THE ADDRESS, AND THAT IS WHAT ARMS THE FAN-OUT. Two of the rule's
// four halves read aggregation AXES rather than one event's numbers, and an axis a
// gate never states does not exist: with only ip, currency, country and nano stated,
// the counterparty and device axes were empty on every payment, so [risk.onFan] — the
// half that exists to see one actor behind twenty nominally unrelated accounts —
// could not fire at this door for any input at all, and [risk.onPace] read one axis
// where it reads three. Account farming is unremarkable from every account taken by
// itself; the only place the pattern exists is in what the accounts SHARE.
//
// So the strongest shared identifier this door actually has is stated on the peer
// axis: the address our own edge resolved, from the same trusted-hop path the country
// comes from ([cloud.ClientIP]). It is not a counterparty in the settlement sense —
// nothing at a card door is — but it is exactly what that axis is for: an identifier
// several nominally unrelated subjects can be found behind. It is also observed by us
// rather than stated by the payer, so it can be evaded (a fresh address per account,
// which costs the attacker something) and cannot be forged into somebody else's
// finding.
//
// WHAT IT IS NOT is a fabricated device. See [paymentAxes].
// It SUPPLIES THE ARGUMENTS, and [paymentFacts] is the rule — the same split
// [cloud.ClientIP] makes over [cloud.clientAddr], for the same reason. What a
// door states decides which halves of the risk rule can fire at it, and that is a
// property worth testing without a socket, a proxy set or a request.
func paymentSignals(c *zip.Ctx) map[string]string {
	var body struct {
		AmountCents int64  `json:"amountCents"`
		Currency    string `json:"currency"`
	}
	// A body that does not decode is not this middleware's refusal to make: the
	// handler behind it validates its own wire and answers 400 in its own words.
	// Here it simply means the amount was not observed.
	_ = json.Unmarshal(c.Body(), &body)
	return paymentFacts(cloud.ClientIP(c), cloud.ClientCountry(c), body.AmountCents, body.Currency)
}

// paymentFacts IS the rule, as a pure function of the four facts the door observed:
// the address our own edge resolved, the jurisdiction it resolved from it, and the
// amount and currency the request asked to move.
func paymentFacts(address, country string, amountCents int64, currency string) map[string]string {
	signals := map[string]string{
		"ip":       address,
		"currency": strings.ToLower(strings.TrimSpace(currency)),
	}
	// THE PEER AXIS, and it is omitted when the edge resolved nothing — for the
	// country's reason below and one of its own. An axis stated as the empty string
	// is every payer whose address we could not resolve pooling into ONE identifier,
	// which reaches the fan-out bound on volume alone and reports a farm made of
	// strangers.
	if address != "" {
		signals[plane.SignalPeer] = address
	}
	// Omitted when nothing trustworthy stated one. An absent country is a fact the
	// rule reads as "the geography half cannot judge"; an empty string sent as a
	// value would be a gate claiming to have looked.
	if country != "" {
		signals[plane.SignalCountry] = country
	}
	// NANO IS USD. A minor unit in another currency converted as though it were
	// cents would be a number the value features read as a different amount of
	// money, so an amount this gate cannot state in USD is not stated at all —
	// absent, blind, and counted as blind on the org's own model state.
	if amountCents > 0 && (signals["currency"] == "" || signals["currency"] == "usd") {
		signals[plane.SignalNano] = strconv.FormatInt(amountCents*nanoPerCent, 10)
	}
	return signals
}

// nanoPerCent converts the wire's minor unit to the model's. A cent is 10^-2 USD
// and a nano is 10^-9, so one cent is 10^7 nano.
const nanoPerCent = 10_000_000

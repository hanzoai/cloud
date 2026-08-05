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
//	the GATE — the one place that asks. POST /v1/billing/topup/token is the
//	self-serve CREDIT DOOR: a settled card charge is its mint authority, so a
//	stolen card that clears is money in an account, and the account is what buys
//	inference. It is the sharpest lifecycle moment this binary owns.
//
// THE GATE IS PRIVILEGED AND SAYS SO IN SO MANY WORDS. cloud.Privileged() reads a
// list of grant PATHS and this route is on none of them, so the default would be
// the fail-OPEN branch — a scorer outage would wave every top-up through, on the
// one route where waving one through mints spendable balance. The bit is set here
// rather than added to that list because the list describes IAM and KMS surfaces
// and this is neither; the gate that knows what it is guarding states it.
//
// IT SHIPS IN SHADOW, and that is a property of the MODEL rather than of this
// code. A model nobody has reviewed is in shadow (apps/risk policy), shadow forces
// its alert false however high the score, and this gate turns a non-alert into an
// allow. So today every legitimate top-up proceeds and every decision is on the
// record with what the model WOULD have said. What still refuses is a scorer that
// is HERE and cannot answer — the fail-closed branch this bit exists to select.

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
}

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
	if !scorerUp() {
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
func scorerUp() bool {
	plane.Bind()
	up, err := plane.Listening(zip.SocketPath(riskpeer.App))
	return err == nil && up
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
// WHERE IT SITS IS PART OF WHAT IT IS. It runs AFTER PinBillingSubject, which is
// what makes the subject it judges the subject the charge will credit: that
// middleware refuses every caller that is neither a validated customer nor the
// trusted service token, and pins the credited subject to the caller's own. A
// screen placed before it would judge a subject a later middleware could still
// change, which is a control on a value rather than on an act.
//
// It refuses in TWO different sentences because they are two different facts, and
// a customer can act on only one of them:
//
//	the scorer is here and could not answer — 503, retry. The model exists, this
//	question went unanswered, and a privileged grant waits rather than proceeds.
//	It is an operational fact, so saying it is honest and useful.
//
//	the model DECIDED against it — 403, and nothing more. A risk reason handed
//	back to whoever triggered it is a feedback channel for tuning the next
//	attempt. The reason is written to the log with the shape and policy version
//	that produced it, which is where an operator reads it.
//
// It is never a 402. Out of funds is what a 402 means at this door and this is
// not that — the whole point of the door is that the caller has no funds yet.
func riskGate(lg log.Logger) zip.Handler {
	return func(c *zip.Ctx) error {
		org := payerOrg(c)
		v := cloud.Decide(c.Context(), org, cloud.RiskQuery{
			Stage:   cloud.StagePayment,
			Subject: cloud.RiskSubject{Kind: plane.KindAccount, ID: principal.Subject(c, org)},
			// STATED, never derived. cloud.Privileged() does not match this route
			// and the default is fail-open, so an unset bit here is a scorer outage
			// minting balance.
			Privileged: true,
			Signals:    cloud.Facts(topupSignals(c)),
		})
		// EVERY decision is recorded, including the allows — an unscored allow and a
		// clean one are different rows, and the refusal is what tells them apart.
		lg.Info("credit door screened",
			"org", org, "action", v.Action, "scored", v.Scored(), "refusal", v.Refusal,
			"cause", v.Cause, "score", v.Score, "shape", v.Shape, "policy", v.Policy)
		if v.Allowed() {
			return c.Next()
		}
		if v.Refusal != "" {
			return zip.Errorf(http.StatusServiceUnavailable,
				"the payment screen could not answer (%s) — try again in a moment", v.Refusal)
		}
		// Block, challenge and restrict all land here. This door has no way to
		// present a challenge and no reduced ceiling to fall back to, so anything
		// short of "proceed" is a refusal — never a quiet proceed.
		return zip.ErrForbidden("this top-up was not authorised")
	}
}

// payerOrg is the org whose ledger this top-up will credit, and therefore the
// organisation whose model judges it.
//
// The two lanes are exactly the two PinBillingSubject admits, and no others reach
// this gate:
//
//	a validated customer — principal.Ledger, the SELECTED org that pays. It is
//	the same key the balance read and the spend gate use.
//	the trusted service token — a verified COMMERCE_SERVICE_TOKEN naming its own
//	org. It carries no validated user, so there is no ledger to resolve and the
//	org it named is the one being credited.
//
// Anything else resolves to "" and the scorer refuses to mint a tenant for it, so
// a request that reaches the credit door naming no organisation is denied by the
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

// topupSignals is what this gate SAW, in the scorer's own vocabulary.
//
// The amount is the fact that matters at a credit door — value velocity is the
// axis a stolen card moves — and it is the one signal the model reads as a
// coordinate. The rest are the gate's record of why it asked.
//
// THE COUNTRY IS THE ADDRESS'S, NOT THE PAYER'S, and that is a limitation this
// door cannot fix from here. The jurisdiction worth judging is the account's
// billing or KYC one, and no part of it reaches this process: the top-up body is
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
func topupSignals(c *zip.Ctx) map[string]string {
	var body struct {
		AmountCents int64  `json:"amountCents"`
		Currency    string `json:"currency"`
	}
	// A body that does not decode is not this middleware's refusal to make: the
	// handler behind it validates its own wire and answers 400 in its own words.
	// Here it simply means the amount was not observed.
	_ = json.Unmarshal(c.Body(), &body)

	signals := map[string]string{
		"ip":       cloud.ClientIP(c),
		"currency": strings.ToLower(strings.TrimSpace(body.Currency)),
	}
	// Omitted when nothing trustworthy stated one. An absent country is a fact the
	// rule reads as "the geography half cannot judge"; an empty string sent as a
	// value would be a gate claiming to have looked.
	if country := cloud.ClientCountry(c); country != "" {
		signals[plane.SignalCountry] = country
	}
	// NANO IS USD. A minor unit in another currency converted as though it were
	// cents would be a number the value features read as a different amount of
	// money, so an amount this gate cannot state in USD is not stated at all —
	// absent, blind, and counted as blind on the org's own model state.
	if body.AmountCents > 0 && (signals["currency"] == "" || signals["currency"] == "usd") {
		signals[plane.SignalNano] = strconv.FormatInt(body.AmountCents*nanoPerCent, 10)
	}
	return signals
}

// nanoPerCent converts the wire's minor unit to the model's. A cent is 10^-2 USD
// and a nano is 10^-9, so one cent is 10^7 nano.
const nanoPerCent = 10_000_000

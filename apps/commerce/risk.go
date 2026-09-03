// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// risk.go — the credit endpoint is SCREENED, and the scorer that screens it lives in
// another process.
//
// Two halves of one client, and they belong in one file because neither is
// intelligible without the other:
//
//	the CLIENT — cloud.SetRiskScorer's first producer. Package cloud has held
//	the client and the fail policy since it was written and has never had anyone
//	to ask: the model is in-process mutable state, so exactly one binary may
//	hold it, and the pod forks one process per app. Every gate in the fleet
//	therefore read a nil scorer and allowed, unscored. This installs one that
//	reaches the risk child over its socket.
//
//	the GATE — the one place that asks. A settled card charge is its own mint
//	authority, so a stolen card that clears is money in an account, and the account
//	is what buys inference. It is the sharpest lifecycle moment this binary owns.
//
// THERE ARE TWO ENDPOINTS ONTO THAT MINT AND THE GATE HOLDS BOTH. commerce has exactly
// ONE card money move (billing.TakePayment) and this binary opens two addresses onto
// it — the browser's POST /v1/billing/topup/token and the agent's typed
// POST /v1/commerce/payments (payments.go), which the module note there calls "the same core
// the console's card top-up runs". A screen on one of them is not a control: it is a
// control standing beside an unscreened entrance to the same ledger write.
//
// AND THE TYPED ENDPOINT IS NOT ONE ENTRANCE, IT IS FOUR. zip records a typed op ONCE
// and every projection reads that one record: the REST route, the MCP tool, the by-name
// call plane and the CLI all dispatch to the op's HANDLER (registeredOp.invoke).
// ROUTER middleware is composed around the fiber handler the REST route serves and
// around nothing else — so a screen mounted with app.With(screen) guards the URL and
// waves the tool through. `takePayment` is published in tools/list. An agent calling
// it reached the same authorized deposit with no screen in front of it and no
// settlement taught behind it, which is the whole control absent on the one plane it
// was built for.
//
// So the screen is composed onto the HANDLER of each endpoint rather than onto either
// endpoint's router: ONE decision, ONE payer rule, ONE settlement key, every
// projection.
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
// allow. So today every legitimate payment proceeds at either endpoint and every
// decision is on the record with what the model WOULD have said. What still refuses is
// a scorer that is HERE and cannot answer — the fail-closed branch this bit exists to
// select. Widening the gate to the second endpoint widens the RECORD, not the
// enforcement: no organisation is armed by this, and the default regime is untouched.

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

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	riskpeer "github.com/hanzoai/cloud/plane/risk"
)

// installRiskScorer publishes the ONE scorer to package cloud's client. Mount calls
// it, beside the ledger ops, because both are this process telling the fleet what
// it can reach through it.
//
// It is installed unconditionally: the client's own fail policy already answers for
// a scorer that cannot be reached, and installing conditionally would mean asking
// at mount time a question whose answer changes every time the risk child starts
// or stops.
func installRiskScorer(lg luxlog.Logger) {
	cloud.SetRiskScorer(func(ctx context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return scoreOverPlane(ctx, lg, org, q)
	})
	// THE UNARMED AXIS IS ANNOUNCED, ONCE, AT BOOT. A rule half that cannot fire
	// because nothing states its axis is indistinguishable from a rule half that
	// found nothing, and the second reads as a clean bill of health. Said here, where
	// the client is published, so it is on the record before any payment needs it —
	// which is the same reason apps/risk announces a jurisdiction listing it cannot
	// use at mount rather than on the first decision that wanted one.
	lg.Info("credit endpoint risk axes", "armed", paymentAxes, "unarmed", paymentUnarmed,
		"doors", []string{"/v1/billing/topup/token"},
		"why", "no device fingerprint reaches this binary from a card payment")
}

// paymentAxes is exactly what a credit endpoint STATES, and [paymentUnarmed] exactly
// what it does not. They are stated rather than left to be inferred from
// [paymentSignals] because the difference decides which halves of the risk rule can
// fire at these endpoints, and a half that cannot fire must never be mistaken for a
// half that found nothing. A test holds [paymentSignals] to these two lists, so the
// declaration cannot drift from the endpoints.
//
// ONE LIST FOR BOTH ENDPOINTS, because both send the same body: the browser's top-up
// and the agent's typed payment each carry a card token, an amount and a currency under
// the SAME field names, so [paymentSignals] reads either one and the axes it can
// state do not depend on which address was called.
var paymentAxes = []string{plane.SignalNano, plane.SignalCountry, plane.SignalPeer}

// paymentUnarmed is the DEVICE axis, and the reason is a fact about the payment path
// rather than a decision of this file's.
//
// No device fingerprint reaches this binary from a card payment at either endpoint. The
// request body is a card token, an amount and a currency; the card is tokenised in the
// browser and its number never arrives here; the browser does not run the payment SDK's
// buyer-verification step, so there is no verification token either; and no header
// carries a device id. An agent calling the typed endpoint has less still. There is
// nothing to state.
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
// THE BUDGET IS THE CLIENT'S. cloud.Decide answers at RiskBudget whatever this
// returns, so a hop bounded any longer would only hold one of the client's 256
// slots past the point where its answer could still be used.
func scoreOverPlane(ctx context.Context, lg luxlog.Logger, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
	// NOT LISTENING IS NOT AN OUTAGE, and telling the two apart is the whole
	// reason this is a probe and not just a call.
	//
	// The fleet starts 106 of its apps lazily: an app is brought up by a request
	// reaching its prefix, and nothing reaches risk's. So the steady state of a
	// fresh pod is a scorer that is not there yet — which, asked synchronously,
	// costs a child's whole startup inside a 150ms budget, times out, and (being
	// privileged) REFUSES THE TOP-UP. Every cold start would take the credit endpoint
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
		// The judge's socket is THERE and this process cannot use it. An outage, handed
		// to the client as one — [cloud.Decide] renders it RefusalError, and the fail
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
// for the router's own start path, for the same reason.
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
func wakeScorer(lg luxlog.Logger) {
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

// payment is what a credit endpoint OBSERVED about one payment: who is paying, where
// the mint was reached, and the facts the model reads.
//
// It is ONE value because the screen and the record must be about the SAME payment.
// The screen reads a subject's history and the record writes it, so an endpoint that
// resolved those separately would judge one key and teach another — leaving the
// judged one's velocity empty forever, however many payments settled. Resolved once
// per payment by [seen] and handed to both halves.
//
// TWO ORGS, NEVER ONE. A card top-up names two organisations and they are the same
// string for every caller but one, which is exactly why holding a single value here
// was wrong: the charge is WRITTEN in one org's books ([chargedOrg]) and the balance
// it funds belongs to another's ([payerOrg]). A platform SuperAdmin acting inside a
// customer's org is the caller that separates them — commerce writes the receipt
// under the org being acted in, and the spend gate reads the admin's own wallet — so
// one value doing both jobs read the receipt out of a namespace it was never in.
// They are named the way [principal] names them: `org` is the data namespace, and
// `ledger` is the billing key.
//
// TWO NAMES IS FOR READING, NOT FOR MINTING. Every read here is right to hold both —
// the receipt comes out of `org` and the model judges `ledger` — but a CREDIT has one
// address and both names claim it, so a payment whose two names differ is refused rather
// than landed at one of them. A card charged on one org's merchant account cannot fund
// another org's wallet whichever way round it is spelled.
//
// AND IT IS REFUSED BEFORE THE CARD MOVES. Both names are resolved HERE, by [seen], off a
// request that has not charged anything yet — so [screen.decide] asks the question at the
// only point where the answer is still free. [screen.settle] asks it again at the ledger
// boundary, where it is a backstop rather than the control: a charge that reaches a mint
// through any path this screen does not compose still refuses.
type payment struct {
	// org is the organisation the charge is WRITTEN under — the commerce namespace
	// holding the receipt the money core produced ([chargedOrg], the EFFECTIVE org).
	// It is what both endpoints' handlers charge through: the browser route's org comes
	// from iammiddleware.IAMTokenRequired and the typed op's from [orgOf], and
	// both are that same effective org.
	org string
	// ledger is the organisation whose BALANCE this credits, and therefore the one
	// whose model judges it ([payerOrg]). It is org for every caller except a
	// masquerading SuperAdmin, which spends its own books.
	ledger string
	// subject is the payer's wallet key inside that LEDGER ([principal.Subject]).
	subject string
	// path is the ADDRESS of the mint this payment was taken at — the op's own path,
	// not the transport's. One screen answers for two endpoints and a record that does
	// not say which cannot be read back against the traffic that produced it.
	path string
	// via is the path THIS PROCESS ACTUALLY SERVED, which for a typed op is not the
	// endpoint: the same handler answers an agent's tools/call at /mcp and a sibling's
	// by-name call on zip's op plane. The pair is the only way an operator can see
	// that a payment arrived through the agent plane at all.
	via string
	// cents and currency are the amount the endpoint was asked to move, in that
	// currency's minor unit — the pair both mints declare under the same two names
	// ([bodyAmount], PaymentIn) because both are projections of one core.
	//
	// They are kept as themselves rather than read back out of facts, which holds the
	// SCORER's vocabulary: there the amount is nano-USD, converted, and absent
	// entirely for a currency this process cannot state in USD ([paymentFacts]). That
	// is the right value for a model of one organisation's behaviour and the wrong one
	// for a sale, which happened in the money the customer actually paid.
	cents    int64
	currency string
	// facts are the observations in the scorer's vocabulary ([paymentSignals]).
	facts map[string]string
}

// diverged reports whether this payment's two organisations are TWO organisations: the
// charge written under one org's books and the credit addressed to another's.
//
// It is the ONE reading of that fact. Both the pre-charge refusal ([screen.decide]) and
// the ledger boundary ([screen.settle]) ask it through this method rather than spelling
// `p.org != p.ledger` twice, because a rule written at two call sites is a rule that can
// be fixed at one of them — and these two must never disagree about which payments may
// mint.
//
// IT IS FALSE FOR EVERY CALLER THAT PAYS FOR ITSELF, which is what makes refusing on it
// safe at an endpoint. [principal] answers ONE string for both names for an ordinary
// member, for a trusted service token ([serviceOrg] answers both), and for a SuperAdmin
// at home (Owner == Org) — and answers "" for both where the payer does not resolve at
// all. The only identity the two names can differ for is a SuperAdmin acting inside
// another organisation.
func (p payment) diverged() bool { return p.org != p.ledger }

// screen is THE credit screen — one VALUE, resolved once at the composition root
// (mount.go) and composed onto the HANDLER of every endpoint that mints.
//
// IT IS A VALUE AND NOT A MIDDLEWARE, and the difference is the whole of this
// control's reach. A typed op is registered as a CONTRACT rather than as a chain, and
// zip records it ONCE: the REST route, the MCP tool, the by-name call plane and the
// CLI are four projections of that single record and all four dispatch to the op's
// HANDLER. Router middleware — the app.With(screen) this used to be — is composed
// around the REST route's fiber handler and around nothing else, so it guarded one
// projection of four and the tool an agent already holds went straight to the money.
// A value that both registrations WRAP THEIR HANDLER WITH is the one composition
// point every projection has to run through.
//
// It holds the logger and one read, because the decision holds no state: the model is
// the risk app's, the fail policy is [cloud.Decide]'s, and what an endpoint observed is
// a [payment]. The read is [receiptOf] — what a payment that cleared actually was — and
// it is a field for the reason [teach] is a variable: an endpoint test can state a
// settlement without standing up a commerce datastore to hold one. It is set once, by
// [riskGate], and production never varies it.
type screen struct {
	lg      luxlog.Logger
	receipt func(ctx context.Context, org, id string) (settlement, error)
}

// riskGate resolves the screen. It is called ONCE, at the composition root, and the
// value is handed to each registration — a screen fetched independently at each
// endpoint is a screen that can be fetched at one of them, which is the state this
// closed.
func riskGate(lg luxlog.Logger) screen { return screen{lg: lg, receipt: receiptOf} }

// route composes the screen onto a RAW route's handler.
//
// A raw route takes a handler LIST, so the screen could sit in that list ahead of the
// handler and continue with c.Next(). It wraps the handler instead, for the same
// reason [screen.op] must: the composition point is the HANDLER, uniformly, and a
// control that is spelled one way here and another way there is a control whose reach
// has to be re-derived at every endpoint. Here the two spellings happen to be
// equivalent — a raw route has exactly one projection — and saying it once is what
// makes the structural check readable.
//
// WHERE IT SITS IS STILL PART OF WHAT IT IS. On the browser endpoint the chain ahead of
// this handler ends in PinBillingSubject, which is what makes the subject it judges
// the subject the charge will credit: that middleware refuses every caller who is
// neither a validated customer nor the trusted service token, and pins the credited
// subject to the caller's own. A screen ahead of it would judge a value a later
// middleware could still change, which is a control on a value rather than on an act.
//
// The raw route also stays registered on cloud's own Router rather than on a
// With-decorated one, and that is a zipdoc constraint rather than a routing one:
// registering a raw money route through a resolvable wrapper makes zipdoc lift the
// FIRST HANDLER's doc comment as the route's description, so the top-up's published
// prose became RequireCSRF's. A gate must not be able to rewrite the document.
func (s screen) route(next zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		cents, currency := bodyAmount(c)
		p := seen(c, c.Path(), cents, currency)
		if err := s.decide(c.Context(), p); err != nil {
			return err
		}
		if err := next(c); err != nil {
			return err
		}
		// AND WHAT ACTUALLY HAPPENED GOES BACK TO THE MODEL. The screen reads history;
		// this is the only thing in the fleet that WRITES any. It runs after the handler
		// because a settlement is a fact about the past, and only the handler's own
		// answer says whether there was one.
		//
		// THE HANDLER'S OWN ANSWER IS THAT FACT. A non-2xx is a payment that did not
		// credit — declined, refused, or broken — and teaching from one would tell the
		// model money moved when none did, which is a velocity bound a caller fills by
		// sending payments that fail.
		res := c.Fiber().Response()
		if res.StatusCode() < 200 || res.StatusCode() > 299 {
			return nil
		}
		ref, receipt := settlementOf(res.Body())
		return s.record(c.Context(), p, ref, receipt)
	}
}

// record is what an endpoint does with a charge that CLEARED: the money, then the
// model, then the sale.
//
// THE MONEY GOES FIRST, and unlike the model it can refuse the payment: commerce's card
// core credits its own transaction store, which nothing in this binary spends from, so
// [screen.settle] is where the settlement reaches the ledger every gate actually reads
// (settle.go). A 2xx already written by the handler is overwritten by that error,
// because answering success to a customer whose money went nowhere is the defect, not
// the report of it.
//
// THE MODEL LEARNS EITHER WAY, and that is the ordering this function exists to hold.
// Teaching is telemetry about a payment that HAPPENED — the card cleared, whatever this
// process managed to do about it afterwards — so gating it on the credit inverted the
// rule that a settled charge is never dropped from the accrual: a top-up refused for a
// currency this ledger does not hold, or for a receipt it could not read, was a real
// payment that vanished from the payer's velocity, and a caller who could provoke the
// refusal could pay all day without accruing anything. [screen.learn] cannot fail the
// payment (it is detached, bounded and dropped under pressure), so nothing about
// running it on the refusal path can reach the customer.
//
// AND THE SALE IS STATED ON THE SAME TERMS THE MODEL IS TAUGHT ON. [screen.emit] tells
// the event plane that an order completed, which is what puts a REAL purchase in front
// of the org's connected ad platforms (apps/destinations forwards it as a server-side
// conversion). It sits here for [screen.learn]'s reason and shares its posture: the
// customer's card cleared, so the sale is a fact about the past whatever this process
// managed to do with the credit afterwards, and a conversion that went missing whenever
// the deposit refused would under-report exactly the payments an operator is already
// chasing. It cannot fail the payment either.
//
// It is ONE function because both endpoints run the same three halves in the same order
// and the order is the whole point: a second call site is a second chance to write them
// the other way round.
func (s screen) record(ctx context.Context, p payment, ref, id string) error {
	credit := s.settle(ctx, p, ref, id)
	s.learn(p, ref)
	s.emit(p, ref)
	return credit
}

// screened composes the screen onto ANY typed mint op's handler, and it is the
// ONE composition point for all of them.
//
// [screen.op] is this function with its projections fixed to one op's types, and
// that method stood alone only while there was one typed mint. There are four
// now — the agent's payment, the browser top-up, the saved-card top-up and the
// card subscription — and four copies of the composition is precisely the state
// that made the first screen a bound on one entrance with others beside it.
//
// It takes the two facts an endpoint cannot state for itself, and takes them as
// VALUES rather than reading the wire:
//
//	WHAT THE INPUT IS WORTH, off the DECODED input. Over MCP the body is a
//	JSON-RPC envelope with the payment inside `arguments`, so a body reader
//	states no value on exactly the endpoint an agent calls — the sharpest axis a
//	credit endpoint has, blind on the agent plane and fine on the browser's.
//
//	WHAT THE ANSWER SETTLED, off the RETURNED receipt. Over MCP the answer is
//	wrapped in a tools/call result and no HTTP response exists when the handler
//	returns, so a response reader would watch an agent's payment settle and
//	teach nothing.
//
// WHAT A PROJECTION CANNOT SUPPLY IS OMITTED, NEVER SKIPPED. The payer, the
// address and the jurisdiction come off the request the op is served over
// ([cloud.Request], parked by the app-wide Bridge, which runs for /mcp and the
// op plane exactly as for a REST route). A call with NO request at all — the
// CLI's local invoke — resolves no payer: that is the same state a request
// naming no organisation is in, it is screened AS that state rather than
// exempted from it, and the handler's own gate refuses it after ([orgOf]).
//
// THE ORDER IS THE CONTROL. decide runs before the core, so a refusal costs no
// card authorization; record runs after it and only on an answer, so the model
// learns from payments that happened and nothing else. A credit that cannot be
// posted refuses the op and the receipt is withheld, so a caller is never told a
// balance exists that does not.
func screened[In, Out any](
	s screen,
	path string,
	worth func(*In) (cents int64, currency string),
	receipt func(*Out) (ref, id string),
	core zip.TypedHandler[In, Out],
) zip.TypedHandler[In, Out] {
	return func(ctx context.Context, in *In) (*Out, error) {
		// The endpoint is the op's OWN address, stated rather than read off the
		// request: over MCP the request path is /mcp, which is the transport and
		// not the mint.
		p := payment{path: path}
		if c, ok := cloud.Request(ctx); ok {
			cents, currency := worth(in)
			p = seen(c, path, cents, currency)
		}
		if err := s.decide(ctx, p); err != nil {
			return nil, err
		}
		out, err := core(ctx, in)
		if err != nil || out == nil {
			return out, err
		}
		ref, id := receipt(out)
		// The same two halves in the same order as the raw endpoint, through the same
		// value ([screen.record]): the ledger the gate reads, then the model.
		if err := s.record(ctx, p, ref, id); err != nil {
			return nil, err
		}
		return out, nil
	}
}

// decide is THE decision — one screen, every endpoint, every projection.
//
// It refuses in THREE different sentences because they are three different facts, and
// a customer can act on only two of them:
//
//	the payment CANNOT BE CREDITED AT ALL — 409, naming the endpoint that can. The
//	charge and the credit name two organisations, so there is no address to land
//	the money at whatever the model says. Said first and said here, because this
//	is the last point at which no card has moved.
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
// It is never a 402. Out of funds is what a 402 means at this endpoint and this is
// not that — the whole point of the endpoint is that the caller has no funds yet.
func (s screen) decide(ctx context.Context, p payment) error {
	// A PAYMENT THAT CANNOT MINT IS REFUSED BEFORE THE CARD IS CHARGED, and WHERE that
	// is asked is the whole of the property.
	//
	// The mint has always refused a payment whose two names are two names — but it
	// refused from BEHIND the handler that charges the card ([screen.settle], which
	// [screen.record] runs after the money core has already taken the money). So the one
	// caller the rule exists for got the worst of both: the customer's merchant account
	// was charged, the credit had nowhere to land, and the endpoint answered 500 over real
	// money that was now permanently uncreditable — and chargeable again on the next
	// attempt, because a fresh idempotency key takes a fresh card authorisation.
	//
	// Both names are already resolved here ([seen]) off a request that has moved no
	// money, so this is the EARLIEST point the fact exists and the LAST one at which
	// refusing it costs nobody anything. The boundary keeps its own check as a backstop.
	//
	// IT IS NOT A RISK VERDICT and does not wear one's clothes: nothing was scored, the
	// scorer is not asked, and the answer is a 409 rather than the screen's own 403 so an
	// operator reading the endpoint's refusals never has to guess which of the two this
	// was. The sentence names the endpoint that CAN fund another organisation, because a
	// platform operator meaning to fund a customer is not doing anything wrong — they are
	// at the wrong endpoint.
	if p.diverged() {
		s.lg.Warn("credit endpoint refused a payment whose charge and credit are two organisations",
			"door", p.path, "via", p.via, "org", p.org, "ledger", p.ledger, "subject", p.subject)
		return zip.Errorf(http.StatusConflict,
			"a card charged in %q cannot fund the balance of %q — fund another organisation "+
				"with POST /v1/admin/grants", p.org, p.ledger)
	}
	// THE TENANT IS THE LEDGER, not the namespace the charge is written in: the model
	// that judges a payment is the model of the organisation whose balance it funds,
	// which is the same key the accrual [screen.learn] writes is filed under. A
	// masquerading SuperAdmin is screened against its OWN history because it is its own
	// books being credited.
	v := cloud.Decide(ctx, p.ledger, cloud.RiskQuery{
		Stage: cloud.StagePayment,
		// THE PAYER, not the account. They are different populations and this endpoint
		// judges the first: an account's learned history is its metered inference
		// spend, so screening a payment as an account compares money moving IN
		// against a distribution of money spent OUT — and the windowed value bounds
		// the aggregate rule reads are a PAYMENTS appetite accruing on the very same
		// key. A customer with a large inference bill was examined for it. See
		// [plane.KindPayer].
		Subject: cloud.RiskSubject{Kind: plane.KindPayer, ID: p.subject},
		// STATED, never derived. cloud.Privileged() does not match either of the
		// two routes this gate holds and the default is fail-open, so an unset bit
		// here is a scorer outage minting balance.
		Privileged: true,
		Signals:    cloud.Facts(p.facts),
	})
	// EVERY decision is recorded, including the allows — an unscored allow and a clean
	// one are different rows, and the refusal is what tells them apart. The ENDPOINT is on
	// the row because one screen answers for two of them, and the PLANE beside it
	// because one endpoint is now reached over three: a mint reached at /mcp is the fact
	// this record exists to make visible.
	s.lg.Info("credit endpoint screened",
		"door", p.path, "via", p.via, "org", p.org,
		"ledger", p.ledger, "action", v.Action, "scored", v.Scored(), "refusal", v.Refusal,
		"cause", v.Cause, "score", v.Score, "shape", v.Shape, "policy", v.Policy)
	if v.Allowed() {
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
	// Block, challenge and restrict all land here. Neither endpoint has a way to
	// present a challenge and neither has a reduced ceiling to fall back to, so
	// anything short of "proceed" is a refusal — never a quiet proceed.
	return zip.ErrForbidden("this payment was not authorised")
}

// seen is what an endpoint OBSERVED, resolved ONCE per payment.
//
// THE PAYER RULE IS ONE RULE — [payerOrg] then [principal.Subject] — and this is the
// only place either is called. That is what makes an accrual reached through any
// endpoint or any projection land on one key: a second resolver written for a second
// entrance would be two payers wearing one name, and a burst split across them would
// halve every velocity bound with the screen still switched on.
//
// THE OTHER ORG IS RESOLVED HERE TOO, in the same one place and off the same request:
// where the charge is written ([chargedOrg]) is a different fact from whose balance it
// funds, and a settlement that had to re-derive either later would be deriving it from
// a request the endpoint has already answered.
//
// The AMOUNT is a parameter because it is the one fact the transports carry
// differently: the raw endpoint has bytes and reads them with [bodyAmount], the typed
// op is handed them decoded. Everything else comes off the request, which every
// projection with a connection behind it has.
func seen(c *zip.Ctx, path string, amountCents int64, currency string) payment {
	ledger := payerOrg(c)
	return payment{
		org:      chargedOrg(c),
		ledger:   ledger,
		subject:  principal.PayerIn(c, ledger).Subject(),
		path:     path,
		via:      c.Path(),
		cents:    amountCents,
		currency: currency,
		facts:    paymentSignals(c, amountCents, currency),
	}
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

// learn tells the risk plane that a card payment SETTLED, at whichever endpoint took it
// and over whichever plane it was reached.
//
// # This is the only thing that teaches the credit endpoint's rule anything
//
// The screen reads what a subject had already done; nothing was making that true.
// [plane.RiskDecide] records nothing by design, the published learn endpoint is an
// organisation calling itself, and at a self-serve credit endpoint the organisation IS
// the payer — so a fresh org's pace and fan-out read an empty history, which is
// precisely the subject those halves exist for. Five payments of eleven thousand
// looked like five first payments. After this, the fifth reads forty-four thousand of
// prior accrual and the sixth reads fifty-five.
//
// # Why the payer cannot steer it
//
//	IT IS DRIVEN BY THE SETTLEMENT, NOT THE REQUEST. Nothing is stated unless the
//	handler ANSWERED that money moved: the reference is the gateway's, carried out
//	of the answer this process produced rather than out of the body the customer
//	sent. A request that fails, is declined, or is refused upstream states no
//	reference and teaches nothing at all — the two callers are what enforce that,
//	because only they can see whether their endpoint answered ([screen.route] reads the
//	response status, [screen.op] the returned receipt).
//
//	THE AMOUNT IS THE AMOUNT THAT SETTLED. It is the same value the screen was given
//	— which is the body's, because that is what a card endpoint charges — but it is only
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
//	the public learn endpoint refuses outright.
//
// # Idempotent, on the settlement's own identifier — ACROSS BOTH ENDPOINTS
//
// Settlement is at-least-once: the endpoint retries, a webhook replays, an event is
// redelivered. The key is therefore the PROCESSOR's reference for the charge — the
// gateway's own payment id, which is the one identifier that is the same across every
// path that can credit one payment — falling back to the ledger receipt only where
// the processor stated none. Velocity that double-counted a retry would freeze a
// customer for paying once.
//
// THAT IS ALSO WHAT MAKES EVERY ENDPOINT ONE ACCRUAL. Both endpoints return the SAME
// core's answer, so a payment taken at either one — over any of the typed endpoint's
// projections — carries the same gateway payment id and keys the same observation: a
// burst split across them accumulates on one subject instead of hiding half of itself
// behind the entrance that was not screened, and one payment sent twice converges
// rather than counting twice. The receipt fallback converges too: it is the ledger
// transaction id, which the raw endpoint names `transactionId` on the wire
// ([settlementOf]) and the typed op carries as PaymentOut.ID, one value under two
// spellings read through one precedence ([firstRef]).
//
// # It can never fail the payment
//
// The money has moved and the customer has been answered by the time this runs. So it
// is detached, bounded, dropped under pressure and panic-guarded, and its errors are
// logged and discarded — the same discipline apps/risk applies to stating a decision
// on the event plane, for the same reason. A telemetry row is expendable; a settled
// payment is not.
func (s screen) learn(p payment, ref string) {
	// THE LEDGER IS THE KEY, matching [screen.decide]'s tenant exactly: an accrual filed
	// under a different org from the one the screen reads is a history the screen can
	// never see.
	if p.ledger == "" || p.subject == "" {
		return
	}
	if ref == "" {
		// The endpoint answered success and named nothing this process can key on. Say so
		// rather than minting a key: an observation under an invented id counts the same
		// money again on the next retry, which is worse than the one it did not record.
		s.lg.Warn("a settled payment could not be taught to the risk model: the answer named no settlement",
			"door", p.path, "via", p.via, "ledger", p.ledger)
		return
	}
	nano := p.facts[plane.SignalNano]
	if nano == "" {
		// No amount this endpoint could state in USD ([paymentSignals]). The event still
		// happened, so it is still taught — the value features read blind, which is a
		// different and honest fact from a payment of nothing.
		s.lg.Debug("teaching a settled payment with no stated value", "door", p.path, "ledger", p.ledger)
	}
	in := &plane.RiskObserveIn{
		Stage:      cloud.StagePayment,
		Kind:       plane.KindPayer,
		Subject:    p.subject,
		Settlement: ref,
		// THE SAME SIGNALS THE SCREEN WAS GIVEN, minus the ones that describe the
		// question rather than the event. The axes must match what [screen.decide]
		// asked about or the screen reads a history taught under different identifiers.
		// They are the SAME map, resolved once per payment, so they cannot differ.
		Signals: signalsOf(map[string]string{
			plane.SignalNano: nano,
			plane.SignalPeer: p.facts[plane.SignalPeer],
		}),
	}
	lg, org := s.lg, p.ledger
	select {
	case teaching <- struct{}{}:
	default:
		lg.Debug("a settled top-up was not taught to the risk model: teachings in flight are at the ceiling",
			"ceiling", maxTeaching)
		return
	}
	// The peer call is read HERE, on the request's own goroutine, so the detached
	// goroutine below holds a VALUE rather than reading a package variable while
	// something else writes it — a client read from a goroutine nobody joins is a client
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

// firstRef IS the settlement key's precedence, in one place, over whichever
// identifiers an endpoint can state.
//
// THE PROCESSOR'S REFERENCE FIRST, because it is the one identifier that is the same
// across every path that can credit ONE payment — both synchronous endpoints and a
// replayed webhook all carry the gateway's payment id, while each writes its own
// ledger row with its own receipt. Keyed on the reference, several paths crediting one
// payment teach ONE observation; keyed on the receipt they would teach one each, and
// the velocity bound would count money that arrived once as having arrived twice.
//
// The ledger receipt is the FALLBACK and not the default: a processor that states no
// reference still settled, and refusing to teach it would leave a real payment out of
// the accrual. Both are minted by us or by the gateway and neither is a field the
// paying customer can set.
//
// It is one function because the two endpoints hold the same two identifiers in
// different hands — the raw endpoint as JSON in its response bytes ([settlementOf]),
// the typed op as fields on the PaymentOut it returns — and a precedence written twice
// is a precedence that can disagree about what "the same payment" is.
//
// ok=false means the answer named none of them, which is a settlement this process
// cannot key an idempotent record on — reported to the caller rather than papered over
// with a generated id.
func firstRef(ids ...string) (string, bool) {
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			return id, true
		}
	}
	return "", false
}

// settlementOf reads the settlement's own identifiers out of a RAW endpoint's answer,
// which is the only place an endpoint states them as bytes: the typed op returns a
// PaymentOut and its fields are read directly ([screen.op]).
//
// The two names are one endpoint's two facts, not two endpoints' spellings: commerce's
// core returns TakePaymentOut and the top-up forwards that struct verbatim, so
// `processorRef` is the gateway's reference and `transactionId` the ledger receipt.
// [firstRef] is the precedence between them, and it is the same precedence the typed
// endpoint applies to the same two values under its own field names.
//
// It answers BOTH, because the two are used for different things and only one of them
// can stand in for the other. ref is the idempotency key — one payment, one identity,
// whichever endpoint or webhook credits it. receipt is the ROW, and it is what
// [screen.settle] reads the settled amount, currency and books back off: the key is
// enough to dedup a deposit and never enough to size one.
func settlementOf(body []byte) (ref, receipt string) {
	var out struct {
		ProcessorRef  string `json:"processorRef"`
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", ""
	}
	ref, _ = firstRef(out.ProcessorRef, out.TransactionID)
	return ref, out.TransactionID
}

// payerOrg is the org whose ledger this payment will credit, and therefore the
// organisation whose model judges it.
//
// The two lanes are exactly the two PinBillingSubject admits on the browser endpoint,
// and no others reach this gate:
//
//	a validated customer — principal.Ledger, the SELECTED org that pays. It is
//	the same key the balance read and the spend gate use.
//	the trusted service token — a verified COMMERCE_SERVICE_TOKEN naming its own
//	org. It carries no validated user, so there is no ledger to resolve and the
//	org it named is the one being credited.
//
// The typed endpoint admits the first lane only — it has no service-token branch, and
// its handler resolves the same validated tenant off the context (payments.go
// orgOf) — so ONE resolver answers for both addresses. That is the point: an
// accrual is only a bound if both endpoints key it the same way, and a second resolver
// written for the second endpoint would be two payers wearing one name.
//
// Anything else resolves to "" and the scorer refuses to mint a tenant for it, so
// a request that reaches a credit endpoint naming no organisation is denied by the
// privileged branch rather than screened against nobody.
func payerOrg(c *zip.Ctx) string {
	return principal.Ledger(c)
}

// chargedOrg is the org the CHARGE IS WRITTEN UNDER — the commerce namespace the money
// core's receipt lives in, which is [principal.Org], the EFFECTIVE org.
//
// It is [payerOrg]'s twin and the two differ in exactly one caller: a platform
// SuperAdmin acting inside a customer's org. Both endpoints charge through the
// effective org — the browser route resolves it in iammiddleware.IAMTokenRequired and
// the typed op in [orgOf], and both read the same validated X-Org-Id — while the
// balance a SuperAdmin funds is its OWN (principal.BillingOrg substitutes the home org
// for exactly that identity, because platform sudo is not a statement about who pays).
// Reading the receipt out of the payer's org therefore looked in the admin's books for
// a row commerce had written in the customer's, found nothing, and refused a charge
// that had already cleared — a card taken and a balance that could never be credited.
//
// Its fallback is the service token's, for [serviceOrg]'s reason.
func chargedOrg(c *zip.Ctx) string {
	org, _ := principal.Org(c)
	return org
}

// paymentSignals is what this gate SAW, in the scorer's own vocabulary.
//
// ONE READER FOR EVERY ENDPOINT, and the AMOUNT IS A PARAMETER because that is the one
// fact the transports carry differently while the endpoints carry it identically. The
// raw endpoint has only bytes and reads them with [bodyAmount]; the typed op is handed
// the same two values already decoded, which over MCP is the only place they exist at
// all — the request body there is a JSON-RPC envelope with the payment inside
// `arguments`, so a reader that went back to the wire would state no value on exactly
// the endpoint an agent calls. Everything else is read off the request, which every
// projection with a connection behind it has.
//
// The amount is the fact that matters at a credit endpoint — value velocity is the
// axis a stolen card moves — and it is the one signal the model reads as a
// coordinate. The rest are the gate's record of why it asked.
//
// THE COUNTRY IS THE ADDRESS'S, NOT THE PAYER'S, and that is a limitation this
// endpoint cannot fix from here. The jurisdiction worth judging is the account's
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
// silence is the state this endpoint was already in. Wiring the billing or KYC
// jurisdiction, when there is one to wire, replaces this signal at this one line.
// THE PEER IS THE ADDRESS, AND THAT IS WHAT ARMS THE FAN-OUT. Two of the rule's
// four halves read aggregation AXES rather than one event's numbers, and an axis a
// gate never states does not exist: with only ip, currency, country and nano stated,
// the counterparty and device axes were empty on every payment, so [risk.onFan] — the
// half that exists to see one actor behind twenty nominally unrelated accounts —
// could not fire at this endpoint for any input at all, and [risk.onPace] read one axis
// where it reads three. Account farming is unremarkable from every account taken by
// itself; the only place the pattern exists is in what the accounts SHARE.
//
// So the strongest shared identifier this endpoint actually has is stated on the peer
// axis: the address our own edge resolved, from the same trusted-hop path the country
// comes from ([cloud.ClientIP]). It is not a counterparty in the settlement sense —
// nothing at a card endpoint is — but it is exactly what that axis is for: an
// identifier several nominally unrelated subjects can be found behind. It is also
// observed by us rather than stated by the payer, so it can be evaded (a fresh address
// per account, which costs the attacker something) and cannot be forged into somebody
// else's finding.
//
// WHAT IT IS NOT is a fabricated device. See [paymentAxes].
// It SUPPLIES THE ARGUMENTS, and [paymentFacts] is the rule — the same split
// [cloud.ClientIP] makes over [cloud.clientAddr], for the same reason. What an
// endpoint states decides which halves of the risk rule can fire at it, and that is a
// property worth testing without a socket, a proxy set or a request.
func paymentSignals(c *zip.Ctx, amountCents int64, currency string) map[string]string {
	return paymentFacts(cloud.ClientIP(c), cloud.ClientCountry(c), amountCents, currency)
}

// bodyAmount is the amount a RAW endpoint was asked to move, read off its request body —
// the one endpoint that has no decoded input to be handed. commerce's top-up body and
// the typed op's PaymentIn declare the amount and the currency under the SAME field
// names (`amountCents`, `currency`), because both are projections of one core that
// takes one set of values, so what a screen can state does not depend on which was
// called.
//
// A body that does not decode is not the screen's refusal to make: the handler behind
// it validates its own wire and answers 400 in its own words. Here it simply means the
// amount was not observed.
func bodyAmount(c *zip.Ctx) (int64, string) {
	var body struct {
		AmountCents int64  `json:"amountCents"`
		Currency    string `json:"currency"`
	}
	_ = json.Unmarshal(c.Body(), &body)
	return body.AmountCents, body.Currency
}

// paymentFacts IS the rule, as a pure function of the four facts the endpoint observed:
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

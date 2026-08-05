package risk

// emit.go — the decision, ON THE SHARED EVENT PLANE.
//
// A decision that only the deciding process can see is not a control anyone can
// operate. Everything this app writes down today is its own: the observation
// record is what a tenant TAUGHT its model (ring.go), the policy record is what
// it STATED, and both are per-tenant files on this app's own shelf, readable
// through this app's own ops. Nothing joins them to the traffic the organisation
// was already measuring, so the questions an operator actually has — did the gate
// start refusing the hour we changed the signup flow, is the shadow regime
// alerting on the campaign's own traffic — cannot be asked at all.
//
// So each decision is ALSO stated on the plane the organisation already reads:
// event.fact, filled by POST /v1/event, read by /v1/insights, grouped by every
// product lens. One row per decide, in the same table as the pageview that led to
// it, which is what makes "risk decisions beside product analytics" a JOIN rather
// than a project.
//
// THE PLANE IS ASKED, NOT LINKED. analytics owns that table and the pod forks one
// process per app, so the write core is in another pid — the shape risk_rpc.go's
// own header names twice (cloud.SetRiskScorer, cloud.SetObsErrorIngest) and the
// answer is the one both landed on: a plane op on the owning app's socket
// (plane.EventCapture, apps/analytics/event_rpc.go). No new transport, no HTTP
// hop through the fleet's front door, no second gate.
//
// THREE RULES, and each is the difference between telemetry and a liability:
//
//	IT CANNOT FAIL THE DECISION. The emit is detached, bounded and dropped under
//	pressure. A gate asking this scorer is the CREDIT DOOR; a telemetry outage
//	that refused a payment would be a control firing on exactly the customers it
//	must not fire on. Nothing about the emit is on the 150ms budget's path — the
//	verdict is computed, then handed over, and the door returns.
//
//	IT CANNOT CROSS A TENANT. The row is stated for the tenant the decision was
//	REACHED under — the one [planeTenant] minted from the plane principal — never
//	from a body and never from the subject. analytics stamps that org into the
//	fact, and every read binds it positionally.
//
//	IT CANNOT CARRY WHAT THE PLANE MUST NOT HOLD. The subject is an identifier a
//	caller chose and may be an email; the amount is a payment. Neither travels.
//	See [digest] and [bracket].

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
	peer "github.com/hanzoai/cloud/plane/analytics"
)

// nameDecided is what the row is called, and it is a CONSTANT for the reason
// analytics' own resolveName refuses a caller-chosen name: `name` is the column
// every lens groups by, and one chosen per call is unbounded cardinality in the
// index. Verb-object, in the plane's own plain vocabulary — no `$` sentinel, no
// product jargon.
const nameDecided = "risk_decided"

// surface names the emitting SURFACE on every row: this app. It is the `product`
// column, which is what lets an operator ask the plane for the fleet's own facts
// without knowing which event names exist.
const surface = "risk"

// The attribute names, in ONE place, because a name that drifted between the
// emitter and a saved query is a lens that silently returns nothing.
//
// EVERY ONE OF THEM IS BOUNDED, and that is a property rather than a habit. The
// attributes map is Map(LowCardinality(String), String): a value a caller can
// choose freely becomes a dictionary entry per distinct spelling, in a SHARED
// table, from a peer nobody vouched for — which is the exact defect analytics'
// resolveName documents (60 KiB of chosen bytes in the indexed column). So each
// value below is either server-minted (action, cause, refusal, shape, policy,
// score, cut, posture), from a closed set this op already refuses outside of
// (stage, subject_kind), or narrowed to a shape before it travels (country, and
// the value's BRACKET rather than the amount).
const (
	attrStage   = "stage"        // the lifecycle moment judged
	attrKind    = "subject_kind" // the entity class judged, NOT the row's own kind column
	attrAction  = "action"       // what the gate was told to do
	attrCause   = "cause"        // the short reason, from the closed vocabulary
	attrRefusal = "refusal"      // why this is not a scored answer
	attrShape   = "shape"        // the model space the verdict was reached in
	attrPolicy  = "policy"       // the regime version in force
	attrScore   = "score"        // where the event sat, on a SCORED answer only
	attrCut     = "cut"          // the threshold it was held against, likewise
	attrValue   = "value"        // the BRACKET the amount fell in — never the amount
	attrCountry = "country"      // the stated jurisdiction, alpha-2 or absent
	attrPosture = "posture"      // whether the verdict was live or shadow
)

// The postures a verdict is reached under. Two values, spelled once: SHADOW is
// the whole reason this row is worth writing — it is what the gate WOULD have
// done, on real traffic, changing nothing — and a reader that cannot tell it from
// a live decision would read a shadow deployment as an armed one.
const (
	postureLive   = "live"
	postureShadow = "shadow"
)

// The value BRACKETS — the intervals the stated bounds cut the amount into. The
// amount itself never travels: a payment amount beside a per-subject key, in a
// table every lens in the organisation reads, is a financial record in a
// telemetry store. What an operator asks of this column is which STATED BOUND the
// value was past, so that is what it holds.
//
// The bounds are the determination's own ([freezeNano], [reviewNano]) rather than
// a scale invented here, so a bracket means exactly what the rule that read the
// same number meant by it, and restating the policy moves both together.
const (
	// bracketNone — no value was stated. It is not "zero": the value features read
	// BLIND on an absent amount, which is a different fact from a payment of
	// nothing (see [nanoOf]).
	bracketNone = "none"
	// bracketUnder — a value under every stated bound.
	bracketUnder = "under"
	// bracketFreeze — at or above the freeze bound.
	bracketFreeze = "freeze"
	// bracketReview — at or above the examining bound.
	bracketReview = "review"
)

// emitBudget bounds ONE emit end to end, including waking a lazy peer. It is
// generous where the decide budget is tight, and it costs the door nothing
// because it bounds a detached goroutine rather than the request: five seconds is
// thirty-three decide budgets, far past a healthy socket call, and enough for a
// cold analytics child to come up (plane.Reach single-flights the start, so an
// emit that gives up mid-wake still leaves the peer coming up for the next one).
const emitBudget = 5 * time.Second

// maxEmits is how many emits may be in flight at once, and it is the whole of the
// back-pressure: past it an emit is DROPPED rather than queued.
//
// A queue would be the wrong answer twice over. It defers the loss instead of
// bounding it — a peer that stops answering fills any buffer at the decide rate —
// and it makes the memory this process holds a function of how fast the fleet is
// being screened. Dropping says the true thing: a telemetry row is expendable and
// a decision is not.
const maxEmits = 64

// inflight is that ceiling, as a counting semaphore over a buffered channel — the
// same shape [cloud.Decide]'s own ceiling takes, for the same reason: a send that
// cannot proceed is an ANSWER (there is no room) rather than a place to block.
var inflight = make(chan struct{}, maxEmits)

// emit hands one stated occurrence to the app that owns the event plane,
// DETACHED and FAIL-SOFT.
//
// The context is deliberately not the caller's. planeDecide's ctx is cancelled
// the moment the door answers, so an emit carrying it would be racing the
// response it is describing and would lose on every fast decision. It is
// [context.WithoutCancel] — the request's values (its trace, its request id) are
// kept, its cancellation is not — with the emit's own budget over it and the
// tenant restated on it.
//
// THE ORG IS RESTATED FROM THE TENANT rather than inherited from the incoming
// caller. They are the same value today — planeTenant minted the tenant from that
// principal — and saying it here is what keeps them the same value: the row is
// filed under the org this verdict was REACHED for, which is the only org it
// describes.
func emit(ctx context.Context, log luxlog.Logger, t tenant, in *contract.EventIn) {
	if in == nil {
		return
	}
	// Detached HERE, on the caller's goroutine, so the value the emit runs under is
	// derived from the request that produced it and never from whatever context the
	// scheduler happens to hand the goroutine.
	carried := context.WithoutCancel(ctx)
	select {
	case inflight <- struct{}{}:
	default:
		// The plane is not keeping up. Say so once, cheaply, and keep deciding.
		if log != nil {
			log.Debug("risk decision not stated on the event plane: emits in flight are at the ceiling",
				"ceiling", maxEmits)
		}
		return
	}
	// The peer call is read HERE, on the caller's goroutine, so the detached
	// goroutine below holds a VALUE rather than reading a package variable while
	// something else writes it. It is the seam a test substitutes, and a seam read
	// from a goroutine nobody joins is a seam no test can put back.
	call := send
	go func() {
		defer func() { <-inflight }()
		// A panic in a telemetry hand-off must not take down the process that is
		// screening payments.
		defer func() {
			if r := recover(); r != nil && log != nil {
				log.Error("risk decision emit panicked", "err", r)
			}
		}()
		ask, cancel := context.WithTimeout(cloud.For(carried, t.org()), emitBudget)
		defer cancel()
		if _, err := call(ask, in); err != nil && log != nil {
			// EVERY failure is the same fact here and none of them is the decision's:
			// an absent peer, a refusing one and a timed-out one all mean the row was
			// not written. There is nothing for this process to do about it and
			// nothing it may do to the verdict, so it is logged and dropped.
			log.Debug("risk decision not stated on the event plane", "err", err)
		}
	}()
}

// send is the ONE plane call, held in a variable for the one thing a variable
// buys here: a test can observe what LEAVES this process without standing up a
// second process to receive it. It is never reassigned in production — the only
// writer is a test, and the compiler holds the signature to the generated
// client's.
var send = peer.EventCapture

// decision projects one verdict onto the occurrence the plane stores. PURE over
// the decision — no clock, no I/O, no tenant lookup — so a test asserts exactly
// what leaves this process, which is the only way "no raw subject, no raw amount"
// is a property rather than a promise.
//
// It reads BOTH the wire answer and the engine's own [decided]: the answer is
// what the gate acts on, and the cut is on the assessment and nowhere else — a
// score is only meaningful against the threshold it was held against, and a row
// carrying one without the other cannot be reconstructed after the appetite is
// restated.
//
// A REFUSAL CARRIES NEITHER NUMBER. This is [answer]'s rule, extended one step:
// the engine populates a score before it checks whether it has warmed, so the
// number on a refusal is arithmetic and not an opinion — and a cut printed beside
// an absent score reads as a comparison that was never made.
func decision(t tenant, in *contract.RiskDecideIn, nano int64, d decided, out *contract.RiskDecided) *contract.EventIn {
	posture := postureLive
	if d.A.Shadow {
		posture = postureShadow
	}
	attrs := []contract.Signal{
		{Name: attrStage, Value: in.Stage},
		{Name: attrKind, Value: in.Kind},
		{Name: attrAction, Value: out.Action},
		{Name: attrPosture, Value: posture},
		{Name: attrValue, Value: bracket(nano)},
		{Name: attrPolicy, Value: strconv.Itoa(out.Policy)},
	}
	add := func(name, value string) {
		if value != "" {
			attrs = append(attrs, contract.Signal{Name: name, Value: value})
		}
	}
	add(attrCause, out.Cause)
	add(attrRefusal, out.Refusal)
	add(attrShape, out.Shape)
	add(attrCountry, alpha2(signal(in.Signals, contract.SignalCountry)))
	if out.Refusal == "" {
		add(attrScore, ratio(out.Score))
		add(attrCut, ratio(d.A.Cut))
	}
	return &contract.EventIn{
		Name:       nameDecided,
		Product:    surface,
		Subject:    digest(t, in.Kind, in.Subject),
		Attributes: attrs,
	}
}

// digest is the subject as the SHARED plane may hold it: a tenant-scoped hash,
// and never the identifier itself.
//
// The subject is the one string the asking gate chooses, and gates choose real
// ones — an account id, a session, an email. This app's own record keeps it,
// because that record is the tenant's own and exists to be replayed into the
// tenant's own model. The event plane is a different store with different
// readers, so what lands there is a key that GROUPS without NAMING: every
// decision about one subject shares a value, and the value says nothing about who
// it is.
//
// SCOPED BY TENANT AND KIND, both of which are in the hash and neither of which
// is recoverable from it. The tenant makes one subject two digests under two
// organisations, so the column cannot be used to correlate across tenants; the
// kind is in it for the reason it namespaces a subject everywhere else — a person
// and an account sharing an identifier are two subjects.
//
// SAY WHAT THIS IS: pseudonymisation, not anonymisation. Subject ids are
// low-entropy, so a party that already holds the tenant key and a candidate list
// can confirm a guess. That party is the organisation itself, reading its own
// partition, which already holds the ids. What it stops is the plane becoming a
// SECOND copy of identifying data — which is the property that matters for a
// store this many lenses read.
//
// It is sha256 truncated to eight bytes, hex — the shape analytics' own
// [fingerprint] uses, so the two grouping keys in this plane read alike.
func digest(t tenant, kind, subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	h := sha256.New()
	// The separators are what keep the hash unambiguous: without them a tenant
	// ending in the kind's first letters and a subject beginning with the rest
	// would hash identically to a different triple.
	_, _ = h.Write([]byte(t))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSpace(kind)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(subject))
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// bracket is the amount as the plane may hold it: which stated bound it was past.
//
// A negative value is treated as no value at all, for [nanoOf]'s reason — a
// number this rule cannot place must read as absent rather than as the mildest
// bracket, which is the reading that silently makes everything ordinary.
func bracket(nano int64) string {
	switch {
	case nano <= 0:
		return bracketNone
	case nano >= reviewNano:
		return bracketReview
	case nano >= freezeNano:
		return bracketFreeze
	default:
		return bracketUnder
	}
}

// alpha2 narrows the stated jurisdiction to the shape the listing reads: two
// letters, upper-cased. Anything else is ABSENT rather than stored.
//
// The country is the one attribute here a CALLER supplies, so it is the one that
// could put a caller-chosen string into a LowCardinality dictionary in a shared
// table. Narrowing it to the 676 spellings an ISO 3166-1 alpha-2 code can take is
// what bounds that column — and a gate that cannot state a country omits it,
// which is already a different fact from stating an unremarkable one.
func alpha2(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	if len(v) != 2 || v[0] < 'A' || v[0] > 'Z' || v[1] < 'A' || v[1] > 'Z' {
		return ""
	}
	return v
}

// ratio renders a score or a cut. The store's attribute values are strings, so
// the number is spelled the shortest way that reads back exactly ('g' with -1
// precision), and a zero is rendered rather than omitted — a score of zero is a
// judgement and an absent score is not.
func ratio(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

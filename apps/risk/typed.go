package risk

// typed.go — the CONTRACT. Every operation of the model plane is a zip TYPED OP,
// so ONE registration is the whole surface: the REST route, the OpenAPI
// operation, the MCP tool, the CLI command and every generated SDK method all
// project from it. An untyped route appends nothing to that registry and is
// therefore invisible to all five — which is why the one route that is untyped
// here says so out loud and is held to a closed list by a test.
//
// TWO RULES THAT ARE NOT STYLE.
//
// The ORG IS NEVER AN In FIELD. A typed op receives only a context; the tenant
// arrives through cloud.Bridge, which parks the identity the gateway minted from
// a verified bearer. An In field is caller-supplied, so a tenant key read from
// one is a cross-tenant read the caller asserted for itself.
//
// EVERY SCHEMA NAME IS PREFIXED. A typed op's Go type name IS its schema name and
// the fleet's schema namespace is FLAT — the compose refuses one name with two
// shapes across apps, because a generated SDK binds whichever it read last. So
// there is no `ref`, no `state`, no `report` here; there is `riskRunRef`,
// `riskModelState`, `riskSearchReport`.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and off every In/Out FIELD into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — it has no parameter for the service
// — so the service arrives as a RECEIVER and every op is a METHOD VALUE, which is
// also the only bound form cmd/zipdoc can lift prose from: a closure returned by
// a helper is a call expression with nothing to read.
type ops struct{ s *cloud.Service[state] }

// plane returns the model plane, or the honest 503 that says why there is none.
// Every op opens with it, BEFORE the tenant gate, so an unbuilt plane is one
// answer for every caller rather than a different failure per route.
func (o ops) plane() (*plane, error) {
	if o.s.State.plane == nil {
		return nil, zip.Errorf(503, "risk: the model plane is unavailable: %s", o.s.State.gap)
	}
	return o.s.State.plane, nil
}

// tenantFor is the ONE tenant resolution every op takes. It reads the validated
// principal parked by cloud.Bridge and the deployment's own brand, and it fails
// closed off the HTTP path — a CLI invocation has no request, therefore no
// validated principal, therefore no tenant and no read.
func (o ops) tenantFor(ctx context.Context) (tenant, error) {
	return tenantOf(ctx, o.s.Brand)
}

// admit is the ONE door into the plane: it resolves the model plane, the caller's
// own tenant, and takes one of that tenant's in-flight slots. Pair it with the
// release it returns.
//
// EVERY op opens with it, and that is enforced rather than asked for:
// [TestOps_EveryOpIsAdmittedAndPriced] walks this file's syntax tree and fails on
// an op that reaches the plane without it. Half the surface used to — state,
// features, appetite, snapshot, restore and the search read were plane work,
// shelf work and, in features' case, up to 120 warehouse statements, with no slot
// and no price. A bound applied to the two ops a reviewer looks at is not a
// bound; the ops an abuser calls are the cheap ones nobody thought to gate.
func (o ops) admit(ctx context.Context) (*plane, tenant, func(), error) {
	p, err := o.plane()
	if err != nil {
		return nil, "", nil, err
	}
	t, err := o.tenantFor(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	if err := p.enter(t); err != nil {
		return nil, "", nil, err
	}
	return p, t, func() { p.leave(t) }, nil
}

// ── the shapes ───────────────────────────────────────────────────────────────

// riskEvent is one thing that happened, as a caller states it.
//
// It names a SUBJECT and never an organisation: whose model this lands in is
// decided by the caller's verified identity, not by anything in this body.
type riskEvent struct {
	// ID is the caller's own stable identifier for the event. It selects the
	// below-the-line review sample by hash, so a counter would make the sample
	// steerable — use the id the event already has.
	ID string `json:"id"`
	// Kind is whose behaviour this is: person, session or account. It namespaces
	// the subject, so a person and an account that share an identifier stay two
	// subjects.
	Kind string `json:"kind"`
	// Subject is the identifier on that kind.
	Subject string `json:"subject"`
	// Nano is the value moved, in nano-USD. Omit it for an event that moves no
	// money: the value features then read BLIND rather than being told the amount
	// was zero, and the difference is reported on the model state.
	Nano int64 `json:"nano,omitempty"`
	// Peer is the counterparty, if any. It is an aggregation axis of its own —
	// "unfamiliar" is a fact about a relationship and not about either party.
	Peer string `json:"peer,omitempty"`
	// Device is the device fingerprint, if any. It is the axis that surfaces
	// several nominally unrelated subjects acting as one.
	Device string `json:"device,omitempty"`
	// At is when it happened, RFC 3339. Empty means now. It must sit inside the
	// thirty-day window the aggregates keep and no more than two minutes ahead of
	// this plane's clock; anything outside that is REFUSED rather than quietly
	// accepted, because a future timestamp moves the aggregates' leading edge and
	// leaves every later event for that subject reading as though it never
	// happened. History older than the window is folded in from your own event
	// surface, not through this door.
	At string `json:"at,omitempty"`
}

// observation converts a wire event into the model's vocabulary, refusing a
// shape the model cannot place. Nano-USD on the wire becomes USD in the model:
// the wire carries integers because money is not a float, and the model carries
// a ratio because every feature is dimensionless.
func (e riskEvent) observation(now time.Time) (observation, error) {
	id := strings.TrimSpace(e.ID)
	if id == "" {
		id = eventID()
	}
	// THE RESERVED NAMESPACES ARE THIS PLANE'S OWN, and a caller may not write into
	// them. The record deduplicates on (tenant, id) and the FIRST writer of an id
	// wins, so an id this plane will later mint for itself is an id a caller can
	// claim in advance — after which the plane's own observation is silently inert.
	//
	// Two of them exist and both are load-bearing. A SETTLEMENT observation
	// ([planeObserve]) is the only thing teaching the aggregate rules any payment
	// velocity at all, so pre-empting one switches those rules off for that subject
	// — available to the paying party, which at a self-serve credit door is the
	// organisation itself. A FOLDED bucket ([bucketID]) is a piece of the
	// organisation's own history, and a claimed id makes the fold skip it.
	//
	// It is refused HERE, on the CALLER's shape, and nowhere else. This method is the
	// one that reads an id a caller chose; [riskEvent.under] is the conversion beneath
	// it, and the plane's own doors reach that one with an id they minted themselves.
	// A guard on the conversion would refuse the plane its own namespaces.
	if ns, taken := reservedOf(id); taken {
		return observation{}, zip.ErrBadRequest("'id' may not begin with " + ns +
			" — that namespace is this plane's own, and an event written into it would displace one of your organisation's records")
	}
	return e.under(id, now)
}

// under is the conversion itself, under an id its caller has already settled on. It
// holds the TIME bound — the one definition of it, [within] — so every door reaches
// that bound through here and none of them can be the door that forgot it.
func (e riskEvent) under(id string, now time.Time) (observation, error) {
	at := now
	if e.At != "" {
		parsed, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			return observation{}, zip.ErrBadRequest("'at' must be RFC 3339")
		}
		// BOUNDED, BOTH DIRECTIONS, AND REFUSED RATHER THAN ADJUSTED. Unbounded this
		// is a one-request detector evasion: a future stamp moves the aggregates'
		// leading edge, after which the subject's real activity is older than every
		// window and reads as nothing at all. See [within].
		if err := within(parsed, now, ringWindow); err != nil {
			return observation{}, err
		}
		at = parsed
	}
	return observe(id, actor{
		Kind:    strings.TrimSpace(e.Kind),
		Subject: strings.TrimSpace(e.Subject),
		Peer:    strings.TrimSpace(e.Peer),
		Device:  strings.TrimSpace(e.Device),
	}, float64(e.Nano)/1e9, at)
}

// eventID names an event whose caller did not.
//
// IT IS RANDOM, and that is the correction: it used to be minted from the kind,
// the subject and the stamp — and the stamp is truncated to the second, so forty
// events for one subject inside one second were forty events with ONE id. The
// record deduplicates on (tenant, id), so thirty-nine of them were silently
// dropped from the very record the rings are a projection of; that subject read
// as having acted once, on every rollout, for any caller not sending its own ids.
// Ordinary traffic, quietly disarmed.
//
// A caller that needs a retry to converge sends its OWN id, which is what the
// field is for and what the doc says. Without one there is nothing to converge
// on: two identical bodies a second apart are two events, and pretending
// otherwise is the defect above.
func eventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A minted id must be UNIQUE above all else. Time at nanosecond resolution
		// is the only other source here that cannot repeat within a process.
		return "evt_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "evt_" + hex.EncodeToString(b[:])
}

// riskScoreIn is one event to judge.
type riskScoreIn struct {
	// Event is the thing to judge. It is judged against the caller's OWN model
	// and nothing is learned from it.
	Event riskEvent `json:"event"`
}

// riskScoreOut is the model's verdict on one event.
type riskScoreOut struct {
	// Scored is false when the model declined, and Refusal says which refusal it
	// was: warming, unusable or unidentified. None of them is a clean bill of
	// health, which is why the refusal is stated rather than rendered as a score
	// of zero.
	Scored bool `json:"scored"`
	// Refusal names why the model declined, when it did.
	Refusal string `json:"refusal,omitempty"`
	// Score is where the event sits in the tenant's own density: 0 where its
	// recent behaviour is densest, 1 where there is none of it.
	Score float64 `json:"score"`
	// Cut is the threshold in force, derived from the stated appetite as a
	// quantile of the scores actually observed rather than fixed at a number.
	Cut float64 `json:"cut"`
	// Alert is whether this would become evidence. It is false in shadow however
	// high the score.
	Alert bool `json:"alert"`
	// Shadow is whether the model is testing rather than deciding — scoring,
	// learning and recording what it WOULD have alerted on, and changing no
	// outcome. It is the default for a model no one has reviewed yet.
	Shadow bool `json:"shadow"`
	// Shape is the model space this verdict was reached in, as `<family>:<digest>`:
	// the KIND of model, and that family's own digest over your organisation's feature
	// inventory in order and the detector's geometry parameters. It is what pins an
	// adverse decision to a model — a score is only meaningful against the space that
	// produced it, and without this the only answer to "which model decided this" was
	// "the one that was running", which is not an answer.
	//
	// The family leads it because everything after it is one family's arithmetic. Two
	// spaces are the same space only if they are the same family, so comparing this
	// with the `shape` on your model state or on a published value is a comparison
	// that holds ACROSS families and not only inside one.
	//
	// It names the SPACE, not the learned state, and that is deliberate. The masses
	// at the instant of a score are in-process counters somewhere between two
	// published values, so citing a published address here would claim that value
	// produced this score — true only for the score taken the instant after a
	// publication. This, the policy version and the event's own time are what IS
	// true, and the published history's clock (GET /v1/risk/state) brackets the
	// decision between two named values from there.
	Shape string `json:"shape,omitempty"`
	// Policy is the version of your organisation's decision regime this verdict
	// was reached under, from its own policy history (GET /v1/risk/policy). Cut is
	// derived from the appetite that version states, so it is the record that makes
	// this decision reconstructible after the appetite is restated. Zero means no
	// regime has ever been stated and the default posture — shadow — was in force.
	Policy int `json:"policy"`
	// Causes is the per-feature attribution, ordered by contribution. Each is a
	// COUNTERFACTUAL on the model that produced the score — the coordinate moved
	// to its neutral value and the event rescored — so the explanation is the
	// same arithmetic the score came from.
	Causes []riskCause `json:"causes,omitempty"`
	// Values is every coordinate, including the ones that contributed nothing, so
	// a reviewer sees what the model read and not only what it concluded.
	Values []riskValue `json:"values,omitempty"`
}

// riskCause is one feature's contribution to a score, and it is the whole
// defensibility of a model decision.
//
// It is a COUNTERFACTUAL on the model that produced the score, not a second
// story told about it: the coordinate is moved to its neutral value, the event is
// rescored, and the drop IS the contribution. There is no separate explainer
// model to disagree with the scorer.
type riskCause struct {
	// Feature is the dimension that contributed.
	Feature string `json:"feature"`
	// Typology is the laundering or abuse pattern this dimension detects.
	Typology string `json:"typology"`
	// Indicator is the supervisor's own words for the thing being looked for.
	Indicator string `json:"indicator"`
	// Citation is where those words come from, so the claim is checkable rather
	// than asserted — which is what a chargeback network or a regulator asks for.
	Citation string `json:"citation"`
	// Severity is how much weight this dimension carries.
	Severity string `json:"severity"`
	// Unit is how to read Observed, which is what turns a coordinate into a
	// sentence.
	Unit string `json:"unit"`
	// Observed is the raw number the coordinate was computed from.
	Observed float64 `json:"observed"`
	// Baseline is the number it was measured against — always this
	// organisation's own history, never a fixed limit and never another
	// organisation's.
	Baseline float64 `json:"baseline"`
	// Without is the score the same event would have received with this
	// coordinate at its neutral value — the counterfactual itself.
	Without float64 `json:"without"`
	// Share is this feature's part of the score, in [0,1]. Zero across every
	// cause means no single feature accounts for the alert and the combination
	// does; the causes are then ordered by how far each sits from unremarkable.
	Share float64 `json:"share"`
}

// riskValue is one coordinate as the model read it.
type riskValue struct {
	// Feature is the dimension.
	Feature string `json:"feature"`
	// X is the coordinate in the model space, always dimensionless.
	X float64 `json:"x"`
	// Observed is the raw number X was computed from, quoted so the coordinate
	// reads back as a sentence rather than a bare ratio.
	Observed float64 `json:"observed"`
	// Baseline is what Observed was measured against: this organisation's own
	// history for this subject.
	Baseline float64 `json:"baseline"`
	// Unit is how to read Observed.
	Unit string `json:"unit"`
	// Blind marks a coordinate that could not be computed and took its neutral
	// value. A model silently reading neutral for a dimension it never has data
	// for is indistinguishable from one reading a genuine absence of risk.
	Blind bool `json:"blind"`
}

// riskLearnIn is a batch of events the model should learn from.
type riskLearnIn struct {
	// Events are the things that happened, oldest first. An empty batch is
	// refused: learning nothing is not an operation.
	Events []riskEvent `json:"events"`
}

// riskLearnOut is what the batch did.
type riskLearnOut struct {
	// Learned is how many of the events the model actually learned from, and is
	// also what the call is metered at: one screen per event learned from. It is
	// the batch minus the events already in this organisation's record, so a
	// retried batch reports — and is charged — zero.
	Learned int `json:"learned"`
}

// riskStateIn takes nothing off the wire. The whole input is the caller's validated
// principal, which is what decides whose model is reported.
type riskStateIn struct{}

// riskModelState is the governance report: everything a review of one tenant's
// model reads, and nothing about any other tenant.
type riskModelState struct {
	// Tenant is the qualified key the model is held under — the brand whose
	// issuer vouched for the caller and the organisation it acts for. It is
	// echoed so a reader can see the answer is its own and not a parameter it
	// passed.
	Tenant string `json:"tenant"`
	// Shape is the model's identity, as `<family>:<digest>`: the KIND of model, and
	// that family's own digest over the inventory in order and the detector's geometry
	// parameters. It is what an auditor pins an alert to, because learned state is
	// only meaningful against the space that produced it — and the family leads it
	// because two families' masses are not fitted differently, they are different
	// kinds of number.
	Shape string `json:"shape"`
	// Live is false while the model is in shadow — scoring, learning and
	// recording what it WOULD have alerted on, and changing no outcome. Shadow is
	// the default for a new tenant.
	Live bool `json:"live"`
	// Policy is the version of the decision regime this model is deciding under,
	// from your organisation's own policy history (GET /v1/risk/policy). Every
	// score cites it, so it is the join between a past decision and the appetite
	// that produced its threshold. Zero means no regime has ever been stated and
	// the default posture — shadow — is in force.
	Policy int `json:"policy"`
	// Learned is how many events the model has learned from.
	Learned int64 `json:"learned"`
	// Warm is whether that is enough for the model to have an opinion at all.
	// Below it the model declines to score, which is an ordinary state and is not
	// a clean bill of health.
	Warm bool `json:"warm"`
	// Stated is the share of the stream this organisation said may be examined.
	Stated float64 `json:"stated"`
	// Realised is the share that actually was. Reading it beside Stated is what
	// makes the appetite a measured commitment rather than an intention.
	Realised float64 `json:"realised"`
	// Sample is the share of below-the-line events retained for review, which is
	// how the miss rate is measured rather than assumed.
	Sample float64 `json:"sample"`
	// Cut is the threshold in force, derived from Stated as a quantile of the
	// scores actually observed.
	Cut float64 `json:"cut"`
	// Saturated means no threshold can honour the stated appetite because too
	// much of the stream scores in the top bucket, so the model is alerting on
	// nothing — the one state that must never be mistaken for quiet.
	Saturated bool `json:"saturated"`
	// Refused counts events the model would not score, by reason. None of them
	// was examined; a refusal is counted, never silent.
	Refused map[string]int64 `json:"refused"`
	// Blind counts, per feature, how often it took its neutral value for want of
	// data. A feature blind on most traffic is not contributing whatever the
	// inventory claims for it.
	Blind map[string]int64 `json:"blind"`
	// Surface reports what of the tenant's OWN event surface has been folded in.
	Surface riskSurface `json:"surface"`
	// Aggregates reports the pressure on this organisation's own sliding
	// aggregates, and whether they have started forgetting subjects to stay inside
	// their bound.
	Aggregates riskAggregates `json:"aggregates"`
	// Values is your organisation's own published model values, newest first —
	// every state it deliberately named, each addressed by its own content and
	// immutable. This is what PUT /v1/risk/state/model names, so it is reported
	// HERE rather than behind an address of its own: they are part of what a review
	// of one model reads, and a list of names is a few hundred bytes.
	//
	// Compare each one's `shape` with the `shape` above: equal means adopting it
	// restores masses into the space this model already runs, and different means
	// adopting it REPLANTS the model into the space that value describes — which is how
	// the shape a search found becomes the shape you are running.
	//
	// The working model is NOT in it. Publication is a boundary somebody marked; the
	// state between two boundaries is in-process counters, and calling those a value
	// would be a claim about reproducibility that nothing could honour.
	Values []riskModelValue `json:"values,omitempty"`
	// Descends is the published value the working model grew out of: the newest one
	// whose mass count it has reached or passed. Empty when nothing has been
	// published yet.
	//
	// It is DERIVED from the count and never stored, so an instant rollback is right
	// for free — adopting an older value moves the count backward and this answers
	// with that older value, where a stored pointer would be a second fact to keep
	// in step. Read with Learned it is also the DRIFT: this model is Descends plus
	// however many events the two counts differ by.
	Descends string `json:"descends,omitempty"`
	// Disposed is how many published values retention has taken. It is DERIVED from
	// the lowest surviving sequence, so it cannot drift from what it describes, and
	// it is reported because a retention that binds is a fact an operator must be
	// able to read rather than a silence.
	Disposed int `json:"disposed,omitempty"`
}

// riskAggregates is how full this organisation's own sliding aggregates are, and
// what it has cost.
//
// IT IS REPORTED BECAUSE THE ALTERNATIVE IS SILENCE. The aggregates are bounded
// per organisation, and at the bound the least-recently-active subject is
// forgotten — after which that subject reads as having done nothing, scores as
// unremarkable, and nothing anywhere says so. A control that switches itself off
// quietly is worse than no control, so the bound, the fill and the count of
// forgotten subjects are all on the organisation's own state.
type riskAggregates struct {
	// Subjects is how many of this organisation's subjects the aggregates hold.
	Subjects int `json:"subjects"`
	// Bound is the most they can hold. It is a per-organisation bound: at it, this
	// organisation degrades and no other one notices.
	Bound int `json:"bound"`
	// Forgotten is how many of its own subjects have been dropped to stay inside
	// that bound. Each one reads as inactive until it is active again.
	Forgotten int64 `json:"forgotten"`
	// Saturated is whether the bound is binding right now. The two counts are its
	// evidence; this is the state to act on.
	Saturated bool `json:"saturated"`
}

// riskSurface is how much of the organisation's own first-party data the model has
// actually seen — the moat, measured.
type riskSurface struct {
	// Folded is how many buckets of the tenant's own feature surface were folded
	// into the model when it became resident.
	Folded int `json:"folded"`
	// Rolled is how many windows of this organisation's own source planes —
	// product events, captured failures, metered inference — were rolled up into
	// its feature surface before that fold. Zero with no gap means the surface was
	// already current, which is a different fact from the rollup never running.
	Rolled int `json:"rolled"`
	// Refused is how many buckets of this organisation's own surface the fold
	// could not fold, because a subject on them is longer than this plane's own
	// field bound. It is history the model does not have, said out loud.
	Refused int `json:"refused,omitempty"`
	// Replayed is how many of this organisation's own recorded observations
	// rebuilt its sliding aggregates when the model became resident. It is what
	// says a rollout was a rebuild rather than a blindness: the aggregates are a
	// projection of a durable record, so a restart costs a replay and not a
	// control.
	Replayed int `json:"replayed"`
	// Window is the lookback the fold covered.
	Window string `json:"window"`
	// Gap says why the fold did not happen or did not complete, when that is the
	// case. An empty surface and an unreachable warehouse are different facts and
	// a model must not report them as the same one.
	Gap string `json:"gap,omitempty"`
}

// riskPublishIn takes nothing off the wire. The whole input is the caller's
// validated principal, which is what decides whose model is published.
type riskPublishIn struct{}

// riskModelValue names one of the organisation's own published model values.
//
// IT CARRIES NO MASSES, and that is the point of it. The state itself — 466 KiB of
// mass counters, measured — stays on the organisation's own encrypted shelf and is
// referred to by content. What used to travel in both directions on this op and its
// pair was that state, which made the CALLER the custodian of the organisation's
// model: it had to hold it, transport it, and be trusted not to have shaped it. An
// address is the whole of what a caller needs to name a value, so the state has no
// reason to leave the store, and now does not.
type riskModelValue struct {
	// Address names this value by its own content: the model's shape, the geometry
	// seed, its position in the window, its threshold, its masses as IEEE-754 bits
	// and the fold watermark behind them. Nothing else — no clock, no counter and
	// deliberately NOT the organisation, so an identical model has one name and a
	// name is never an authority. Holding another organisation's address resolves
	// nothing.
	Address string `json:"address"`
	// Sequence is this value's place in YOUR organisation's own history, from 1 and
	// contiguous until retention disposes of the oldest.
	Sequence int64 `json:"sequence"`
	// Shape NAMES the model space the masses are only meaningful against, as
	// `<family>:<digest>` — the KIND of model, and that family's own digest over the
	// feature inventory in order and the detector's geometry parameters. Compare it
	// with the `shape` on your model state (GET /v1/risk/state): equal means adopting
	// this value restores masses into the space already running, and different means
	// adopting it REPLANTS the model into the space this value describes. That is what
	// makes a searched shape installable.
	//
	// A DIFFERENT FAMILY IS NOT ADOPTABLE AT ALL, and that is the one difference the
	// family term makes here: a different geometry in the same family is a replant, and
	// a different family is a refusal naming both — its masses do not describe your
	// model in any space.
	Shape string `json:"shape"`
	// Learned is how many events are behind the masses.
	Learned int64 `json:"learned"`
	// Warmed is how far your own event surface had been folded in when this value
	// was published, RFC 3339. It is part of the address because two models with
	// identical masses reached by different routes disagree about what is left to
	// fold, and one of them will re-teach history the other will not.
	Warmed string `json:"warmed,omitempty"`
	// At is when it was published, RFC 3339, on the server clock. You do not supply
	// it: a record whose date the audited party chose is not a record.
	At string `json:"at"`
}

// riskPublishOut is the value this call published, and whether it minted one.
type riskPublishOut struct {
	// Tenant is whose history it entered.
	Tenant string `json:"tenant"`
	// Value is the published value: its name and what it is, never its masses.
	Value riskModelValue `json:"value"`
	// Minted is false when your model was ALREADY published under this name and
	// nothing was written. Publication is idempotent on the value itself, which is
	// what a content address is for — publishing at every boundary costs nothing
	// rather than being the cheapest way to fill a disk.
	Minted bool `json:"minted"`
}

// riskAdoptIn puts one of your own published values in force, by name.
type riskAdoptIn struct {
	// Address is one of YOUR organisation's own published values (GET
	// /v1/risk/state reports them, and a search reports the one it fitted for you).
	// An address your organisation has not published is NOT FOUND — including one
	// another organisation published, because an address names a value and never
	// authorises reading it.
	Address string `json:"address"`
}

// riskCatalogIn narrows the feature catalogue to a window of the caller's own
// surface.
type riskCatalogIn struct {
	// Days is how far back to measure the organisation's own coverage, 1 to 400.
	// Zero takes thirty.
	Days int `json:"days,omitempty"`
}

// riskCatalog is the feature catalogue in its two honest lenses: what the MODEL
// reads, and what THIS organisation's surface actually carries.
type riskCatalog struct {
	// Tenant is whose surface was measured.
	Tenant string `json:"tenant"`
	// Model is the governed inventory: one entry per dimension of the model
	// space, each carrying the typology it serves and the published standard that
	// asks for it. It is the same for every organisation, because it is the
	// model's shape.
	Model []riskModelFeature `json:"model"`
	// Surface is what this organisation's own event surface carries, per
	// dimension, measured over the window. A dimension present in no bucket is
	// blind here — the model reads its neutral value and a reviewer has to be able
	// to see that.
	Surface []riskOrgFeature `json:"surface"`
	// Network is the published cross-organisation baseline over the same window,
	// so the surface above has something to be read AGAINST. It is the same for
	// every caller and it names nobody.
	//
	// It carries no tenant and cannot be made to: the table it reads has no org
	// column, every figure is a quantile over at least kAnonOrgs organisations
	// weighted one vote each, and a band that does not meet that floor is dropped
	// on the way out.
	Network []riskBand `json:"network,omitempty"`
	// Gap says why a lens could not be measured, when that is the case. Each
	// reason names its own lens, because "the surface is unreadable" and "the
	// network baseline is unreadable" are different facts.
	Gap string `json:"gap,omitempty"`
}

// riskBand is one published day of the network baseline for one dimension: what
// the network's own days look like, in the same unit the surface above reports.
//
// The levels are INTERPOLATED rather than exact, so a published figure lies
// between two organisations' values and is therefore nobody's. No extreme level
// is published: at this floor a 99th percentile is the maximum however it is
// estimated, and a maximum is one organisation's number by definition.
type riskBand struct {
	// Day is the day the band covers.
	Day time.Time `json:"day"`
	// Kind is the subject kind it was computed over.
	Kind string `json:"kind"`
	// Dim is the dimension, named as this API publishes it.
	Dim string `json:"dim"`
	// Q10 is the quiet end of the network's day: a tenth of contributing
	// organisations sit at or below it.
	Q10 float64 `json:"q10"`
	// Q50 is the network's median day.
	Q50 float64 `json:"q50"`
	// Q90 is the busy end: a tenth of contributing organisations sit at or above
	// it. It is the highest level published.
	Q90 float64 `json:"q90"`
	// Orgs is how many organisations contributed, each weighted exactly one vote
	// whatever its size. It is published so a reader can judge the band rather
	// than trust it.
	Orgs uint32 `json:"orgs"`
	// N is how many subject-days went into it.
	N uint64 `json:"n"`
}

// riskModelFeature is one dimension of the model space and the obligation it serves.
type riskModelFeature struct {
	// Name is the dimension.
	Name string `json:"name"`
	// Window is the sliding aggregate it reads.
	Window string `json:"window,omitempty"`
	// Typology is the pattern this dimension detects.
	Typology string `json:"typology"`
	// Indicator is the supervisor's own words for the thing being looked for.
	Indicator string `json:"indicator"`
	// Citation is where those words come from, so the claim is checkable rather
	// than asserted.
	Citation string `json:"citation"`
	// Severity is how much weight an alert on it carries.
	Severity string `json:"severity"`
	// Unit is how to read the raw number, which is what turns a coordinate into a
	// sentence an investigator can put in a file.
	Unit string `json:"unit"`
	// Neutral is the value the coordinate takes when the data cannot support it.
	Neutral float64 `json:"neutral"`
	// Blind is how often this dimension took that neutral value for THIS
	// organisation.
	Blind int64 `json:"blind"`
}

// riskOrgFeature is one column of the organisation's own feature surface, measured.
type riskOrgFeature struct {
	// Name is the dimension as this API publishes it.
	Name string `json:"name"`
	// Source names the plane it is rolled up from, so a dimension that reads zero
	// everywhere traces to a plane the organisation does not use rather than to a
	// defect.
	Source string `json:"source"`
	// Unit is how to read the numbers below.
	Unit string `json:"unit"`
	// Buckets is how many five-minute buckets of this organisation's surface were
	// measured.
	Buckets int `json:"buckets"`
	// Present is in how many of them the dimension carried a value at all.
	Present int `json:"present"`
	// Mean is the dimension's average where it was present.
	Mean float64 `json:"mean"`
	// Max is the largest value it reached in the window.
	Max float64 `json:"max"`
	// Blind is true when the dimension is present in no bucket at all: this
	// organisation's surface does not carry it, and saying so is the difference
	// between no risk and no data.
	Blind bool `json:"blind"`
}

// riskSearchIn starts an exhaustive search for the model shape that best fits this
// organisation's own history.
type riskSearchIn struct {
	// Days is how much of the organisation's own history to replay, 1 to 400.
	// Zero takes thirty.
	Days int `json:"days,omitempty"`
}

// riskSearchRun is the accepted run.
type riskSearchRun struct {
	// ID addresses the run. Read the result back with it.
	ID string `json:"id"`
	// Events is how much of the organisation's own history the run will replay.
	Events int `json:"events"`
	// Candidates is how many model shapes will be tried.
	Candidates int `json:"candidates"`
}

// riskRunRef addresses one search run by id.
type riskRunRef struct {
	// ID is the run, taken from the path. A run another organisation started is
	// simply not there — the same answer an unknown id gives.
	ID string `json:"id"`
}

// riskSearchReport is what a completed search found.
type riskSearchReport struct {
	// ID is the run.
	ID string `json:"id"`
	// Done is false while the run is still going; the trials below are then the
	// ones finished so far.
	Done bool `json:"done"`
	// Started is when the run was accepted, RFC 3339.
	Started string `json:"started"`
	// Ended is when it finished, RFC 3339. Absent while it is still going.
	Ended string `json:"ended,omitempty"`
	// Events is how much of this organisation's history was replayed.
	Events int `json:"events"`
	// Trials is every shape tried, best first.
	Trials []riskTrial `json:"trials"`
	// Winner is the best-fitting shape, absent when nothing fit.
	Winner *riskTrial `json:"winner,omitempty"`
	// Fitted is the winning shape FITTED over your own history and published as one of
	// your organisation's own model values. Name its address on PUT
	// /v1/risk/state/model and the winning shape becomes the model you are running.
	//
	// It is why this op answers something you can act on. A trial keeps counts and not
	// the model that produced them, so a report without this named a shape nobody could
	// install — and the adoption path refused a shape change besides. Fitting the winner
	// once is a sixty-fifth pass over the same history; keeping all sixty-four fitted
	// models resident instead would cost a measured 21 MiB per run for sixty-three
	// shapes nobody adopts.
	//
	// Two things about it are worth knowing before you adopt it. Its realised rate can
	// differ from the winner's above, because the ranking measures every candidate under
	// one fixed reference geometry so the comparison is a comparison, while this is
	// fitted under YOUR geometry — the one an outsider cannot predict. And it has
	// learned the window this search replayed and nothing older, so adopting it trades
	// history for fit.
	Fitted *riskModelValue `json:"fitted,omitempty"`
	// Refusal says why the run proves nothing, when it does. An empty history is
	// REFUSED rather than reported as zero alerts: "no alerts" is exactly what a
	// quiet model looks like, and choosing a shape on the strength of an empty
	// replay is the failure a sandbox exists to prevent.
	Refusal string `json:"refusal,omitempty"`
	// Gap says why the winning shape could not be fitted into an adoptable value, when
	// it could not. It is separate from Refusal because they are different facts: a
	// refusal means the ranking below proves nothing, a gap means the ranking stands and
	// only the value is missing.
	Gap string `json:"gap,omitempty"`
}

// riskTopology is one candidate shape of the detector.
type riskTopology struct {
	// Family is the KIND of model this candidate is: `halfspace` is an ensemble of
	// half-space trees whose masses are counters, and it is the family this search
	// grid ranks. The parameters below are that family's own — a family that does not
	// partition space with trees has different ones — so read them against this.
	Family string `json:"family"`
	// Trees is how many half-space trees the ensemble holds.
	Trees int `json:"trees"`
	// Depth is how deep each tree is. With Trees it sets how finely the space is
	// partitioned, and therefore how much history it takes to fill.
	Depth int `json:"depth"`
	// Window is how many events make one reference window.
	Window int `json:"window"`
	// Blend is how much of a closing window folds into the reference: 1 replaces
	// it outright, less makes the reference expensive to move.
	Blend float64 `json:"blend"`
	// Review is the appetite this shape was tried at.
	Review float64 `json:"review"`
}

// riskTrial is what one shape did over this organisation's own history.
type riskTrial struct {
	// Topology is the shape.
	Topology riskTopology `json:"topology"`
	// Learned is how many events the shape learned from during the replay.
	Learned int64 `json:"learned"`
	// Scored is how many it was able to score.
	Scored int64 `json:"scored"`
	// Alerted is how many of those it would have raised.
	Alerted int64 `json:"alerted"`
	// Stated is the appetite the shape was tried at.
	Stated float64 `json:"stated"`
	// Realised is what that appetite actually produced. The distance between the
	// two is what the search is searching over.
	Realised float64 `json:"realised"`
	// Warm is whether the shape learned enough to have an opinion at all over
	// this organisation's whole history.
	Warm bool `json:"warm"`
	// Saturated is whether the appetite could not be honoured by any threshold,
	// which is a shape that alerts on nothing and reads like a quiet one.
	Saturated bool `json:"saturated"`
	// Curve is the realised alert rate over successive tenths of the history —
	// the learning curve, which says whether the shape settled or is still moving.
	Curve []float64 `json:"curve"`
	// Fit ranks the shape, smaller being better: the relative miss of the stated
	// appetite, plus flat penalties for never warming and for saturating, plus the
	// share of coordinates that were blind.
	Fit float64 `json:"fit"`
}

// ── the ops ──────────────────────────────────────────────────────────────────

// Score judges one event against the caller organisation's OWN model and learns
// nothing from it. It is how a candidate is tried against real behaviour before
// anything depends on the answer, and it is the model's analogue of testing a
// rule.
//
// Because it records nothing, the aggregates it reads do not include the event:
// the numbers are the organisation's history as it stands. A model still warming
// declines with a reason rather than answering zero, because silence must never
// read as a clean result.
//
// Example: {"event":{"id":"tx_9","kind":"account","subject":"u_412","nano":420000000}}
func (o ops) score(ctx context.Context, in *riskScoreIn) (*riskScoreOut, error) {
	obs, err := in.Event.observation(time.Now())
	if err != nil {
		return nil, err
	}
	pay, err := o.gate(ctx, "score", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	d, err := p.score(t, obs)
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out := verdict(d)
	return &out, nil
}

// Learn records a batch of events into the caller organisation's own aggregates
// and lets its model learn from them. It answers how many it learned from.
//
// IT DOES NOT SCORE, AND THAT IS THE POINT. An observation is a value you record;
// learning is a transformation over observations; a verdict is a query against the
// result. This op is the first two. [ops.score] is the third, it is pure, and it
// is the ONE door to a verdict. They were one call, which meant you could not
// record without training and could not train without being answered — and the
// model ran twice over every event to produce a verdict the response carried and
// no caller read.
//
// TO OBSERVE AND JUDGE, COMPOSE THE TWO, and mind the order. Score FIRST, then
// learn: the score is then the model's opinion of an event it has not yet learned
// from, which is the question worth asking. The other order answers for a model
// that has already absorbed the event it is judging.
//
// This is the training path, and there is no job behind it: the model IS a set of
// mass counters over half-space trees, so learning is an increment and the model
// is current the instant the last event lands. Nothing from any other
// organisation is in it, and nothing from this organisation leaves it.
//
// A RETRY IS INERT. The record deduplicates on the event id you send, and an event
// already in it moves nothing, costs nothing and is not counted — so a client that
// timed out can send the same batch again and its model holds what it holds.
// Without an id of your own there is nothing to converge on: two identical bodies
// are two events.
//
// Example: {"events":[{"id":"tx_9","kind":"account","subject":"u_412","nano":420000000}]}
func (o ops) learn(ctx context.Context, in *riskLearnIn) (*riskLearnOut, error) {
	if len(in.Events) == 0 {
		return nil, zip.ErrBadRequest("'events' is required — learning nothing is not an operation")
	}
	if len(in.Events) > maxBatch {
		return nil, zip.Errorf(413, "at most %d events per batch", maxBatch)
	}
	// Validate the WHOLE batch before any of it is charged for or learned from: a
	// batch that is half applied and then refused leaves the caller unable to say
	// what its model holds.
	now := time.Now()
	obs := make([]observation, 0, len(in.Events))
	for i := range in.Events {
		one, err := in.Events[i].observation(now)
		if err != nil {
			return nil, err
		}
		obs = append(obs, one)
	}
	pay, err := o.gate(ctx, "learn", len(obs))
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	learned, err := p.learn(t, obs...)
	if err != nil {
		return nil, wrap(err)
	}
	// METERED ON WHAT WAS DONE, which is this app's own stated rule for the pair
	// ([ops.gate]) and is now the truth rather than an approximation of it. The
	// gate above still bounds the batch the caller stated, because how many of it
	// is new is not knowable until the record has been written.
	pay(learned)
	return &riskLearnOut{Learned: learned}, nil
}

// State reports the caller organisation's own model: what it has learned, whether
// it is live or still in shadow, the threshold in force, the appetite it stated
// beside the share it actually realised, every refusal by reason, every feature
// that read blind, and how much of the organisation's own event surface has been
// folded in.
//
// It covers ONE organisation. A caller cannot learn another's volumes, alert rate
// or behaviour from it, because the state is read out of a model that holds only
// its own.
func (o ops) state(ctx context.Context, _ *riskStateIn) (*riskModelState, error) {
	pay, err := o.gate(ctx, "state", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	st, agg, err := p.state(t)
	if err != nil {
		return nil, wrap(err)
	}
	ver, err := p.regimeNow(t)
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out, err := p.review(t, st, agg, ver)
	if err != nil {
		return nil, wrap(err)
	}
	return &out, nil
}

// PublishModel publishes your organisation's model as a NAMED VALUE, so a decision
// taken today can be reconstructed tomorrow and a change made today can be undone.
//
// It answers with a NAME and not with the state. The masses stay on your
// organisation's own encrypted store and are referred to by an address computed
// from their own content: the shape, the geometry seed, the position in the window,
// the threshold, the masses themselves as IEEE-754 bits, and the fold watermark
// behind them. That is what makes the value nameable without making the caller its
// custodian.
//
// IT IS IDEMPOTENT ON THE VALUE. A model that has not changed publishes to the name
// it already has and mints nothing, reporting minted=false — so publishing at every
// boundary that matters is free. Ten values are retained per organisation, bounded
// in BYTES rather than in rows, and the oldest is disposed of past that.
//
// A model that has learned nothing is refused: planted is not learned, and a value
// that reproduces nothing is not a value.
//
// It is POST and PUT on one address because they are one plane's two verbs over one
// kind of thing: POST mints a value from the model in force, PUT puts a value in
// force. They were /v1/risk/state/snapshot and /v1/risk/state/restore — two addresses
// named after the operation rather than after the thing, which is how a reader ends
// up asking what the difference between a snapshot and a value is.
func (o ops) publish(ctx context.Context, _ *riskPublishIn) (*riskPublishOut, error) {
	pay, err := o.gate(ctx, "publishModel", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	v, minted, err := p.publish(t)
	if err != nil {
		return nil, wrap(err)
	}
	if v.Address == "" {
		return nil, zip.ErrNotFound("this organisation's model has learned nothing yet, so there is no value to publish")
	}
	pay(1)
	return &riskPublishOut{Tenant: string(t), Value: modelValueOf(v), Minted: minted}, nil
}

// AdoptModel puts one of your organisation's OWN PUBLISHED VALUES in force, by name —
// which is what an instant rollback is, what promoting a challenger is, and what
// installing the shape a search found is.
//
// IT TAKES AN ADDRESS AND NEVER STATE. The masses are read from your own store, so
// nothing about your model has to be held by whatever is making this call. That
// closes the sharpest edge the previous shape had: a body of counters is something
// a caller can COMPOSE, and a region filled until activity inside it reads as
// ordinary is a model that has been shaped rather than learned. The engine's mass
// invariant was the only thing standing between a composed body and the model; with
// an address there is no body to compose.
//
// IT ADOPTS THE SHAPE, NOT ONLY THE MASSES. A value records the model space its
// masses were taken in, and a value whose space differs from the one in force
// REPLANTS your model into that space before restoring them. That is what makes
// POST /v1/risk/search actionable: a search answers with the shape that fits your own
// history best and publishes it fitted, and its address is what you name here. Before
// this, a winning shape was advice nobody could take — the adoption path refused every
// shape change, and a winner is a different shape by definition.
//
// WHAT ADOPTING A SEARCHED SHAPE COSTS, SAID PLAINLY: the value a search fits has
// learned the window the search replayed and nothing older, so installing it trades
// history for fit. Your appetite is untouched — that is your policy record's, with its
// own versions — and so is the geometry, which stays your own.
//
// An address your organisation has not published is NOT FOUND. That includes one
// another organisation published, and it is not a lookup that failed: the store is
// per organisation and the address is a name, never an authority.
func (o ops) adopt(ctx context.Context, in *riskAdoptIn) (*riskModelState, error) {
	pay, err := o.gate(ctx, "adoptModel", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	// AFTER the principal is resolved, not before. A malformed body answered ahead of
	// the identity tells an unauthenticated caller which shapes this op accepts, and
	// it makes the refusal order depend on which field happened to be wrong. Same
	// ordering the money plane was corrected to.
	addr, err := admitAddress(in.Address)
	if err != nil {
		return nil, err
	}
	st, agg, err := p.adopt(t, addr)
	if err != nil {
		return nil, wrap(err)
	}
	ver, err := p.regimeNow(t)
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out, err := p.review(t, st, agg, ver)
	if err != nil {
		return nil, wrap(err)
	}
	return &out, nil
}

// modelValueOf projects a published value onto the wire. It is the ONE direction
// this conversion is written — there is no inverse, because nothing off the wire
// ever becomes a value: a value is minted from the model this process holds and
// named by its content.
func modelValueOf(v value) riskModelValue {
	out := riskModelValue{
		Address: v.Address, Sequence: v.Seq, Shape: v.Shape, Learned: v.Learned,
		At: v.At.Format(time.RFC3339),
	}
	if !v.Warmed.IsZero() {
		out.Warmed = v.Warmed.Format(time.RFC3339)
	}
	return out
}

// Features is the feature catalogue in its two honest lenses.
//
// The MODEL lens is the governed inventory: one entry per dimension of the model
// space, each carrying the typology it serves, the supervisor's own words for the
// indicator, and the published standard those words come from — so a coverage
// claim is checkable rather than asserted. It is the same for every organisation.
//
// The SURFACE lens is what THIS organisation's own event surface actually carries,
// measured over the window: how many of its buckets carry each dimension at all,
// and what the dimension reads where it is present. A dimension present in no
// bucket is BLIND, and saying so is the difference between no risk and no data.
//
// Example: {"days":30}
func (o ops) features(ctx context.Context, in *riskCatalogIn) (*riskCatalog, error) {
	days, err := window(in.Days)
	if err != nil {
		return nil, err
	}
	// PRICED FROM THE WINDOW, because that is what it costs. This op rolls up to
	// four source planes into the tenant's own surface — up to 120 bounded
	// INSERT..SELECT statements against the single warehouse pod — and then reads
	// the window back. It was free, and it is the most expensive read here.
	pay, err := o.gate(ctx, "features", windowScreens(days))
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	st, _, err := p.state(t)
	if err != nil {
		return nil, wrap(err)
	}
	out := riskCatalog{Tenant: string(t)}
	for _, f := range anomaly.Inventory() {
		out.Model = append(out.Model, riskModelFeature{
			Name: f.Name, Window: f.Window, Typology: f.Typology, Indicator: f.Indicator,
			Citation: f.Citation, Severity: f.Severity, Unit: f.Unit, Neutral: f.Neutral,
			Blind: st.Blind[f.Name],
		})
	}
	// A SURFACE READ ROLLS FIRST, the same rule the search follows: the dictionary
	// answers what this organisation's surface actually carries, and a surface
	// nobody rolled carries nothing — which reads as "every dimension blind" and is
	// the wrong answer rather than a missing one. Idempotent under the watermark and
	// aligned to the surface's own grain, so a current surface issues no statement.
	end := time.Now().UTC()
	if _, err := p.roll(ctx, t); err != nil {
		out.Gap = err.Error()
	}
	cov, err := dictionary(ctx, t, end.Add(-days), end)
	if err != nil {
		// METERED ANYWAY. The roll above already ran against the warehouse, and the
		// meter's contract is what was DONE — a read that reached the surface and
		// then could not be summarised still cost the window it rolled.
		pay(windowScreens(days))
		out.Gap = err.Error()
		out.Surface = []riskOrgFeature{}
		return &out, nil
	}
	for _, c := range cov {
		out.Surface = append(out.Surface, riskOrgFeature{
			Name: c.Dim.Name, Source: c.Dim.Source, Unit: c.Dim.Unit,
			Buckets: c.Buckets, Present: c.Present, Mean: c.Mean, Max: c.Max, Blind: c.Blind(),
		})
	}
	// THE THIRD LENS, and the reason the baseline is computed at all.
	//
	// The daily recompute writes [baselineTable] whether or not anything ever
	// reads it, and until this nothing did: a warehouse cost paid every day and
	// collected on never, which is the same "declared but unwired" defect as a
	// reader with no writer, inverted. One published table, one reader, so the
	// disclosure argument has exactly one place to hold.
	out.Network, err = bands(ctx, end.Add(-days), end)
	if err != nil {
		out.gap(err)
	}
	pay(windowScreens(days))
	return &out, nil
}

// bands reads the published network baseline for a window and renders it. It
// narrows by no dim, because the catalogue reports every dimension.
func bands(ctx context.Context, start, end time.Time) ([]riskBand, error) {
	bs, err := baseline(ctx, "", start, end)
	if err != nil {
		return nil, err
	}
	out := make([]riskBand, 0, len(bs))
	for _, b := range bs {
		out = append(out, riskBand{
			Day: b.Day, Kind: b.Kind, Dim: b.Dim,
			Q10: b.Q10, Q50: b.Q50, Q90: b.Q90, Orgs: b.Orgs, N: b.N,
		})
	}
	return out, nil
}

// gap records why a lens is missing WITHOUT losing one already recorded. Two
// lenses can fail in one response — an unreachable warehouse fails both — and a
// field that only ever holds the last writer's message reports one outage as the
// other's.
func (c *riskCatalog) gap(err error) {
	switch {
	case err == nil:
		return
	case c.Gap == "":
		c.Gap = err.Error()
	case c.Gap != err.Error():
		c.Gap += "; " + err.Error()
	}
}

// Search runs an exhaustive search for the model shape that best fits the caller
// organisation's own history, and answers 202 with the run to read back.
//
// Every candidate is replayed over that organisation's OWN feature surface in its
// own sandbox — its own aggregates, its own model, neither of them the live one —
// so a run cannot move a live threshold and cannot see another organisation's
// data. The result is the learning curve for each shape and the one that fit
// best, ranked on how closely it honoured the stated appetite, whether it warmed
// at all, whether it saturated, and how much of the coordinate space it left
// blind.
//
// An empty history is REFUSED rather than reported as zero alerts, because "no
// alerts" is exactly what a quiet model looks like.
//
// Example: {"days":30}
func (o ops) search(ctx context.Context, in *riskSearchIn) (*riskSearchRun, error) {
	days, err := window(in.Days)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	// A search is real compute over real history and it is GATED before any of it
	// runs, on the caller's OWN ledger, failing closed: a commerce that cannot be
	// reached refuses rather than admits.
	//
	// BOTH HALVES, EACH BEFORE ITS OWN WORK. [plane.begin] prices the surface read
	// from the window and the grid from the measured history, and meters each on
	// what it actually did — see there for why one price would be wrong in both
	// directions. This op hands it the client and nothing else: the plane never
	// reaches the request, the ledger or the rate.
	//
	// AND THE GRID'S METER RUNS IN THE RUN, not here. This op answers 202 and the
	// grid runs behind it; metering at accept would charge for every candidate over
	// every event the moment the run was ADMITTED, and a rollout — which this
	// binary does at one replica, stopping the old pod first — cancels the run
	// partway with the debit already taken.
	run, err := p.begin(ctx, t, days, func(kind string, n int) (func(int), error) {
		return o.gate(ctx, kind, n)
	})
	if err != nil {
		return nil, wrap(err)
	}
	return &riskSearchRun{ID: run.ID, Events: run.Events, Candidates: len(candidates())}, nil
}

// SearchResult reads back one search run: every shape tried over this
// organisation's own history, best first, and the one that fit.
//
// A run another organisation started is simply not there — the same 404 an
// unknown id gives, so the read is not a probe oracle.
//
// Example: {"id":"srch_2f6a1c"}
func (o ops) result(ctx context.Context, in *riskRunRef) (*riskSearchReport, error) {
	pay, err := o.gate(ctx, "searchResult", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	pay(1)
	id := strings.TrimSpace(in.ID)
	rep, err := p.run(t, id)
	if err != nil {
		return nil, wrap(err)
	}
	if rep == nil {
		if live, running := p.pending(t, id); running {
			out := searchReport(live, false)
			return &out, nil
		}
		return nil, zip.ErrNotFound("no such search run")
	}
	out := searchReport(*rep, true)
	return &out, nil
}

// ── projections ──────────────────────────────────────────────────────────────

// maxBatch bounds one learn call. A batch is a loop over the model's lock, so an
// unbounded one is a way to hold every other tenant's request behind this one.
const maxBatch = 1000

// ── the money ────────────────────────────────────────────────────────────────
//
// THE BILLABLE UNIT IS A SCREEN: one event judged against an organisation's own
// model. Scoring one event is one screen, learning from a batch is one per event,
// and a search is one per candidate per event of the history it replays — which
// is why the search is priced from its measured size rather than as a flat fee.
//
// Every one of them is GATED before the work and METERED after it, on the
// caller's OWN ledger, in every environment. This surface used to gate only the
// search: score and learn — the two an abuser would actually call, in a loop —
// were free, unbounded compute against a per-tenant model and a per-tenant disk
// write. Free unbounded compute is a denial of service and lost revenue at the
// same time, and which of the two it is depends only on who found it first.

// defaultScreenUUSD is the fallback price of ONE screen in micro-USD when no
// published one. It is the FLOOR, not the price: the meter authority answers
// what a screen costs and this is what a screen cost before the authority
// existed, so an unreadable authority keeps charging exactly what it charged
// yesterday. Zero would make screens free and therefore un-gated, mirroring the
// edge gate's price==0 pass-through.
const defaultScreenUUSD int64 = 100

// screenRate resolves the per-screen price from the meter authority, in
// micro-USD, falling back to the compiled floor.
//
// THE ENV OVERRIDE IS GONE. CLOUD_RISK_PRICE_UUSD_PER_SCREEN priced this before
// and was set by no deployment; an env var keeps no history, so nothing could
// say what we charged in March or who changed it. The price is a row now, edited
// at admin.hanzo.ai with an audit trail, and the constant above is the floor
// beneath it — two answers, in a stated order, instead of three.
// Indirected through a var so a test can price a screen at ZERO, which is a legal
// price that makes the surface free — and the property that a free surface is
// still not an ANONYMOUS one is the thing gate_order_test exists to hold. The env
// var used to be that client by accident; this is the same client on purpose, and the
// same one callProvider uses a repo over.
var screenRate = func(ctx context.Context) int64 {
	return cloud.RateMicros(ctx, "risk", "screen", defaultScreenUUSD)
}

// windowScreens prices a WINDOW of the warehouse: one screen per day rolled up
// and read back. Bringing an organisation's feature surface current is up to 120
// bounded INSERT..SELECT statements against the one warehouse pod plus a read of
// the window, and its size is the window and nothing else.
//
// It is one function because two surfaces do that work — the feature catalogue
// and a search's setup — and a unit spelled twice is a unit that eventually
// differs in one of the places.
func windowScreens(window time.Duration) int { return int(window / (24 * time.Hour)) }

// screenMicros is what n screens cost, in micro-USD.
func screenMicros(ctx context.Context, n int) int64 {
	rate := screenRate(ctx)
	if n <= 0 || rate <= 0 {
		return 0
	}
	return int64(n) * rate
}

// gate checks the caller's own balance for n screens and returns the debit to run
// once the work has actually happened.
//
// TWO HALVES, ONE DEFINITION, and the split is the point: the gate runs on the
// UPPER BOUND of the work before any of it starts, and the meter runs on what was
// DONE. A caller that is refused pays nothing; a caller whose batch was truncated
// pays for the part that landed.
//
// It fails closed — a commerce that cannot be reached refuses rather than admits
// — and it refuses in the fleet's own money contract via [cloud.Denied], so a 402
// from this surface reads exactly like a 402 from every other one. Off the HTTP
// path (an in-process CLI or MCP invocation) there is no ledger to charge and the
// pair is a no-op, which is the same rule the rest of the fleet applies.
// THE DEBIT HOLDS VALUES, NEVER THE REQUEST. Everything the meter needs is read
// off the request HERE, while it is live, and copied into the closure. The search
// meters from a background goroutine — the run outlives the 202 — and a request
// context is recycled the moment its handler returns, so a closure that kept `c`
// and read c.User() later reads a freed arena or another tenant's request on the
// same connection. Same defect as retaining a zero-copy header view, one level up.
func (o ops) gate(ctx context.Context, kind string, n int) (func(done int), error) {
	c, onHTTP := cloud.Request(ctx)
	if !onHTTP {
		return func(int) {}, nil
	}
	ledger := principal.Ledger(c)
	// NO LEDGER IS AN IDENTITY REFUSAL, and [cloud.ResourceMeter.Gate] is where it
	// is answered — above both of its branches, for every caller of the money door,
	// as [cloud.ErrNoLedger]. [cloud.denial] renders that as 403 "no validated
	// principal" and [cloud.DenyEnvelope] writes it in the fleet's own nested
	// {"error":{"code","message"}}, so this surface refuses an unidentified caller
	// in the same bytes as every other one.
	//
	// This op used to hold its own copy of that rule, from before the fleet door
	// had one. Both answered 403 with the identical sentence, so the only thing the
	// copy still decided was the SHAPE — flat {"status","code","error"} from zip
	// instead of the nested envelope — which made /v1/risk the one surface where a
	// client reading error.code found nothing. Measured, both ways, on this
	// package's own priced ops before it was removed.
	//
	// [TestPricedOps_RefuseAnUnidentifiedCallerInTheFleetsOwnEnvelope] is what holds
	// the remaining answer, and it fails if this file grows a second one back.
	project, validated := principal.ValidatedProject(c)
	if err := o.s.Bill.Gate(ctx, ledger, project, validated, kind, cloud.MicrosToGateCents(screenMicros(ctx, n))); err != nil {
		return nil, cloud.Denied(err)
	}
	who := metering.Usage{
		Model: kind, Project: project, Actor: c.User(),
		RequestID: c.RequestID(), ClientIP: cloud.ClientIP(c),
	}
	bill := o.s.Bill
	return func(done int) {
		use := who
		use.AmountMicros = screenMicros(ctx, done)
		bill.MeterUsage(ledger, kind, use)
	}, nil
}

// caller is the identity a policy change is recorded against.
//
// It comes from the VALIDATED principal on the request and never from a body: an
// attributable record whose attribution the caller chose is not attributable. Off
// the HTTP path there is no request and so no identity, and the honest answer is
// the empty one — [plane.enact] refuses it rather than recording an anonymous
// change.
//
// It lives HERE, beside [ops.gate], because a package reaches for the raw request
// in ONE file or it reaches for it in as many as nobody is counting. gate already
// reads this exact header for the meter's actor, so a second file calling
// cloud.Request for the same fact would be the same escape hatch under a second
// justification — which is what allowedRequestUses (typed_request_gate_test.go)
// exists to stop. Two functions, one client, one pin.
func caller(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return c.User()
}

// admitArming refuses to take an organisation's model LIVE for a caller who is not
// an admin of it.
//
// ARMING IS NOT TUNING, and only one of the two is self-service. Restating the
// appetite moves how much of an organisation's own stream it examines; arming
// decides whether the model may change an OUTCOME at all — a payment frozen, a
// grant refused — for every customer that organisation has. That is a governance
// act, and before this the whole regime was one write behind [ops.gate]'s billing
// check and [ops.admit]'s tenant guard, neither of which asks anything about
// authority: any member of an org could PUT {"live":true} and arm it.
//
// ORG-ADMIN, AND DELIBERATELY NOT SUPERADMIN. The organisation is arming ITSELF,
// so its own admin is exactly the right authority and requiring platform sudo
// would make every customer's governance decision Hanzo's to take. [cloud.Admin]
// is that scope — the org's own admin, with SuperAdmin as the stated superset —
// read through the platform's ONE predicate set rather than a fourth spelling of
// it.
//
// DISARMING IS LEFT SELF-SERVICE, which is the scope of the finding and not an
// oversight worth hiding: returning a model to shadow cannot freeze a payment, and
// today no organisation is armed at all.
//
// Off the HTTP path there is no principal, so there is no authority and no arming
// — the same fail-closed answer [caller] gives, for the same reason. It lives
// beside [ops.gate] and [caller] because this package reaches for the raw request
// in ONE file or in as many as nobody is counting.
func admitArming(ctx context.Context, live bool) error {
	if !live {
		return nil
	}
	c, ok := cloud.Request(ctx)
	if !ok || !cloud.Admin.Admits(cloud.AuthorityOf(c)) {
		return zip.ErrForbidden("taking this organisation's model live is an act for an admin of " +
			"this organisation; stating the appetite is not")
	}
	return nil
}

// window validates a day count and returns it as a duration. 400 days is the
// surface's own retention, so asking for more asks for rows that do not exist.
func window(days int) (time.Duration, error) {
	switch {
	case days == 0:
		return 30 * 24 * time.Hour, nil
	case days < 0 || days > 400:
		return 0, zip.ErrBadRequest("'days' must be between 1 and 400 — the surface keeps 400 days")
	default:
		return time.Duration(days) * 24 * time.Hour, nil
	}
}

// wrap turns an internal failure into an honest HTTP one, preserving a refusal
// that is already an HTTP error (the tenant gate's 403, a validation 400).
func wrap(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*zip.HTTPError](err); ok {
		return err
	}
	return zip.Errorf(500, "%v", err)
}

func verdict(d decided) riskScoreOut {
	a := d.A
	out := riskScoreOut{
		Scored: a.Scored, Refusal: a.Reason, Score: a.Score,
		Cut: a.Cut, Alert: a.Alert, Shadow: a.Shadow, Policy: d.Version, Shape: d.Shape,
	}
	for _, c := range a.Causes {
		out.Causes = append(out.Causes, riskCause{
			Feature: c.Feature, Typology: c.Typology, Indicator: c.Indicator,
			Citation: c.Citation, Severity: c.Severity, Unit: c.Unit,
			Observed: c.Observed, Baseline: c.Baseline, Without: c.Without, Share: c.Share,
		})
	}
	for _, v := range a.Values {
		out.Values = append(out.Values, riskValue{
			Feature: v.Feature, X: v.X, Observed: v.Observed,
			Baseline: v.Baseline, Unit: v.Unit, Blind: v.Blind,
		})
	}
	return out
}

// review is the whole governance answer for one organisation: what its model is
// right now, and every value it has published.
//
// ONE DOOR. The three ops that answer this question — reading the state, restating
// the appetite and adopting a value — all come through here, so none of them can
// answer it a different way. [modelState] stays a pure projection of engine state
// beneath it; this is what adds the organisation's own history to it.
//
// A history that cannot be read is an ERROR and not an empty list. The shelf is the
// same file the model was just read from, so a read that fails here is a fault, and
// reporting "no published values" for it would be indistinguishable from an
// organisation that has published none.
func (p *plane) review(t tenant, st anomaly.State, agg strain, policy int) (riskModelState, error) {
	out := modelState(t, st, p.surface(t), agg, policy)
	vs, disposed, err := p.values(t, 0)
	if err != nil {
		return riskModelState{}, err
	}
	out.Disposed = disposed
	for _, v := range vs {
		out.Values = append(out.Values, modelValueOf(v))
	}
	d, ok, err := p.descends(t, st.Learned)
	if err != nil {
		return riskModelState{}, err
	}
	if ok {
		out.Descends = d.Address
	}
	return out, nil
}

func modelState(t tenant, st anomaly.State, f fold, s strain, policy int) riskModelState {
	return riskModelState{
		Tenant: string(t), Shape: st.Digest, Live: !st.Config.Shadow, Policy: policy,
		Learned: st.Learned, Warm: st.Warm,
		Stated: st.Config.Appetite.Review, Realised: st.Realised, Sample: st.Config.Appetite.Sample,
		Cut: st.Cut, Saturated: st.Saturated,
		Refused: st.Refused, Blind: st.Blind,
		Surface: riskSurface{
			Folded: f.Folded, Rolled: f.Rolled, Replayed: f.Replayed, Refused: f.Refused,
			Window: f.Window.String(), Gap: f.Gap,
		},
		Aggregates: riskAggregates{
			Subjects: s.Subjects, Bound: s.Bound, Forgotten: s.Forgotten, Saturated: s.Saturated,
		},
	}
}

func searchReport(r report, done bool) riskSearchReport {
	out := riskSearchReport{
		ID: r.ID, Done: done, Started: r.Started.Format(time.RFC3339),
		Events: r.Events, Refusal: r.Refusal, Gap: r.Gap,
		Trials: make([]riskTrial, 0, len(r.Trials)),
	}
	if !r.Ended.IsZero() {
		out.Ended = r.Ended.Format(time.RFC3339)
	}
	for _, tr := range r.Trials {
		out.Trials = append(out.Trials, riskTrialOf(tr))
	}
	if r.Winner != nil {
		w := riskTrialOf(*r.Winner)
		out.Winner = &w
	}
	// THE SAME PROJECTION every other published value goes through, so the address a
	// search hands back and the address a state read lists are one shape and cannot
	// drift into two.
	if r.Fitted != nil {
		f := modelValueOf(*r.Fitted)
		out.Fitted = &f
	}
	return out
}

// riskTrialOf publishes one trial. The candidate's FAMILY is published beside its
// parameters, because the parameters only mean anything against it: `trees` is a
// half-space number and a family that does not partition space with trees would
// publish its own fields here.
func riskTrialOf(t trial) riskTrial {
	return riskTrial{
		Topology: riskTopology{Family: string(t.Topology.family()),
			Trees: t.Topology.Trees, Depth: t.Topology.Depth,
			Window: t.Topology.Window, Blend: t.Topology.Blend, Review: t.Topology.Review},
		Learned: t.Learned, Scored: t.Scored, Alerted: t.Alerted,
		Stated: t.Stated, Realised: t.Realised, Warm: t.Warm,
		Saturated: t.Saturated, Curve: t.Curve, Fit: t.Fit,
	}
}

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
// the fleet's schema namespace is FLAT — the weave refuses one name with two
// shapes across apps, because a generated SDK binds whichever it read last. So
// there is no `ref`, no `state`, no `report` here; there is `riskRunRef`,
// `riskModelState`, `riskSearchReport`.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
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
	id := strings.TrimSpace(e.ID)
	if id == "" {
		id = eventID()
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
	// Learned is how many events the model learned from, which is the batch, and
	// is also what the call is metered at: one screen per event.
	Learned int `json:"learned"`
	// Verdicts is the model's verdict on each event, in the order given, so a
	// caller that is both teaching and deciding needs one round trip.
	Verdicts []riskScoreOut `json:"verdicts"`
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
	// Shape is the model's identity: the inventory in order and the detector's
	// geometry parameters. It is what an auditor pins an alert to, because
	// learned state is only meaningful against the shape that produced it.
	Shape string `json:"shape"`
	// Live is false while the model is in shadow — scoring, learning and
	// recording what it WOULD have alerted on, and changing no outcome. Shadow is
	// the default for a new tenant.
	Live bool `json:"live"`
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

// riskAppetiteIn restates the risk appetite, which is the decision the model is not
// permitted to make for itself.
type riskAppetiteIn struct {
	// Review is the share of the stream that may be sent for examination, in
	// (0, 0.5]. The alert threshold is derived from it as a quantile of the scores
	// actually observed, so the level is governed rather than tuned.
	Review float64 `json:"review"`
	// Sample is the share of below-the-line events retained for review, in
	// [0, 1]. It is the instrument that measures what the model missed; there are
	// no labels, so nothing else can.
	Sample float64 `json:"sample"`
	// Live turns the model out of shadow. It defaults to FALSE on every call, so
	// going live is always an explicit act and never a side effect of changing a
	// number.
	Live bool `json:"live"`
}

// riskSnapshotIn takes nothing off the wire.
type riskSnapshotIn struct{}

// riskSnapshotOut is a pinned copy of the tenant's learned state.
type riskSnapshotOut struct {
	// Tenant is whose state this is. A snapshot naming another organisation is
	// refused on restore.
	Tenant string `json:"tenant"`
	// Shape is the model identity the state is only meaningful against.
	Shape string `json:"shape"`
	// Learned is how many events are behind it.
	Learned int64 `json:"learned"`
	// Body is the state itself: the masses and the seed, never the geometry.
	// Geometry is a pure function of the seed, so it is regenerated on restore and
	// cannot be supplied — which removes the sharpest edge a persisted model has,
	// state that describes WHERE the regions are rather than only how full.
	Body riskSnapshotBody `json:"body"`
}

// riskSnapshotBody is one organisation's learned state on the wire.
//
// It is DECLARED HERE rather than published straight off the engine's own type,
// for two reasons that are both about the contract. A dependency's struct tags
// are not our API, and letting them be means an upstream rename silently changes
// what every generated SDK sends. And the fleet's schema namespace is flat, so a
// type called `Snapshot` is a name another app will reach for next.
type riskSnapshotBody struct {
	// Version is the layout of the state. State from another version is rejected
	// rather than reinterpreted.
	Version int `json:"version"`
	// Shape is the model identity this state is only meaningful against: the
	// feature inventory in order and the detector's geometry parameters.
	Shape string `json:"shape"`
	// Tenant is the organisation the state belongs to. A restore under any other
	// organisation is refused.
	Tenant string `json:"tenant"`
	// Seed is what the tree geometry is generated FROM. Carrying the seed rather
	// than the trees is what keeps the state from describing where the regions
	// are.
	Seed uint64 `json:"seed"`
	// Learned is how many events are behind the masses, and Seen how far into the
	// open reference window they are.
	Learned int64 `json:"learned"`
	// Seen is the position within the open window.
	Seen int `json:"seen"`
	// Cut is the alert threshold that was in force.
	Cut float64 `json:"cut"`
	// Ref is the reference window's masses, per tree; Cur is the open window's.
	// A region's mass is the sum of its two halves — an array that fails that
	// invariant was not produced by this algorithm and is refused.
	Ref [][]float64 `json:"ref"`
	// Cur is the open window's masses, per tree.
	Cur [][]float64 `json:"cur"`
	// Hist is the score distribution the threshold is cut from.
	Hist []float64 `json:"hist"`
}

// snapshotBody and modelSnapshot are the two halves of one conversion, written
// beside each other so neither can drift from the other.
func snapshotBody(s anomaly.Snapshot) riskSnapshotBody {
	return riskSnapshotBody{
		Version: s.Version, Shape: s.Digest, Tenant: s.OrgID, Seed: s.Seed,
		Learned: s.Learned, Seen: s.Seen, Cut: s.Cut,
		Ref: s.Ref, Cur: s.Cur, Hist: s.Hist,
	}
}

func modelSnapshot(b riskSnapshotBody) anomaly.Snapshot {
	return anomaly.Snapshot{
		Version: b.Version, Digest: b.Shape, OrgID: b.Tenant, Seed: b.Seed,
		Learned: b.Learned, Seen: b.Seen, Cut: b.Cut,
		Ref: b.Ref, Cur: b.Cur, Hist: b.Hist,
	}
}

// riskRestoreIn installs previously pinned state.
type riskRestoreIn struct {
	// Body is a snapshot this organisation took. One naming another organisation
	// is refused: it is that organisation's learned behaviour, and installing it
	// would put one tenant's activity inside another's model.
	Body riskSnapshotBody `json:"body"`
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
	// Gap says why the surface could not be measured, when that is the case.
	Gap string `json:"gap,omitempty"`
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
	// Refusal says why the run proves nothing, when it does. An empty history is
	// REFUSED rather than reported as zero alerts: "no alerts" is exactly what a
	// quiet model looks like, and choosing a shape on the strength of an empty
	// replay is the failure a sandbox exists to prevent.
	Refusal string `json:"refusal,omitempty"`
}

// riskTopology is one candidate shape of the detector.
type riskTopology struct {
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
	a, err := p.score(t, obs)
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out := verdict(a)
	return &out, nil
}

// Learn records a batch of events into the caller organisation's own aggregates
// and lets its model learn from them, answering the model's verdict on each.
//
// This is the training path, and there is no job behind it: the model IS a set of
// mass counters over half-space trees, so learning is an increment and the model
// is current the instant the last event lands. Nothing from any other
// organisation is in it, and nothing from this organisation leaves it.
//
// Events are recorded FIRST and judged after, which is deliberate: the numbers an
// alert quotes are then the same ones an investigator sees when they look at the
// subject, and every baseline has the event removed from it arithmetically so
// nothing is measured against itself.
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
	verdicts, err := p.learn(t, obs...)
	if err != nil {
		return nil, wrap(err)
	}
	pay(len(verdicts))
	out := riskLearnOut{Learned: len(verdicts), Verdicts: make([]riskScoreOut, 0, len(verdicts))}
	for _, a := range verdicts {
		out.Verdicts = append(out.Verdicts, verdict(a))
	}
	return &out, nil
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
	pay(1)
	out := modelState(t, st, p.surface(t), agg)
	return &out, nil
}

// SetAppetite restates how much of the stream the caller organisation's model may
// send for examination, and whether it is live.
//
// The appetite is the decision a model is not permitted to make for itself: its
// output is a probability, so how likely it is to MISS something is a matter of
// policy that has to be stated, measured and reviewed rather than absorbed into a
// constant. The alert threshold is then derived from it as a quantile of the
// scores actually observed, which is what keeps its meaning as the distribution
// drifts.
//
// Learned state survives the change. The model's identity covers its SHAPE — the
// inventory and the geometry — and not its appetite, so restating policy unlearns
// nothing.
//
// Example: {"review":0.01,"sample":0.001,"live":false}
func (o ops) appetite(ctx context.Context, in *riskAppetiteIn) (*riskModelState, error) {
	switch {
	case in.Review <= 0 || in.Review > 0.5:
		return nil, zip.ErrBadRequest("'review' must be in (0, 0.5] — a share of the stream, not a count")
	case in.Sample < 0 || in.Sample > 1:
		return nil, zip.ErrBadRequest("'sample' must be in [0, 1]")
	}
	pay, err := o.gate(ctx, "appetite", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	st, agg, err := p.appetite(t, in.Review, in.Sample, in.Live)
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out := modelState(t, st, p.surface(t), agg)
	return &out, nil
}

// Snapshot pins the caller organisation's learned state so a decision taken today
// can be reproduced tomorrow.
//
// It carries the masses and the seed and never the geometry: geometry is a pure
// function of the seed, so it is regenerated on restore and cannot be supplied.
// That removes the sharpest edge a persisted model has — state that says where
// the regions are rather than only how full they are.
func (o ops) snapshot(ctx context.Context, _ *riskSnapshotIn) (*riskSnapshotOut, error) {
	pay, err := o.gate(ctx, "snapshot", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	snap, ok, err := p.pin(t)
	if err != nil {
		return nil, wrap(err)
	}
	if !ok {
		return nil, zip.ErrNotFound("this organisation's model has learned nothing yet, so there is no state to pin")
	}
	pay(1)
	return &riskSnapshotOut{Tenant: string(t), Shape: snap.Digest, Learned: snap.Learned, Body: snapshotBody(snap)}, nil
}

// Restore installs previously pinned state into the caller organisation's model,
// replacing whatever it holds.
//
// A snapshot naming another organisation is REFUSED. The engine checks the shape,
// the version and the mass invariant — an array that fails the invariant was not
// produced by this algorithm — but it does not check whose state it is, because
// in its own deployment the caller IS the tenant. Here the caller is a request,
// so the check is made here: another organisation's learned state inside this
// model is that organisation's activity, disclosed.
func (o ops) restore(ctx context.Context, in *riskRestoreIn) (*riskModelState, error) {
	pay, err := o.gate(ctx, "restore", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	st, agg, err := p.adopt(t, modelSnapshot(in.Body))
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out := modelState(t, st, p.surface(t), agg)
	return &out, nil
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
	pay, err := o.gate(ctx, "features", int(days/(24*time.Hour)))
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
		pay(int(days / (24 * time.Hour)))
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
	pay(int(days / (24 * time.Hour)))
	return &out, nil
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
	// A search is real compute over real history, so it is GATED before any of it
	// runs, on the caller's OWN ledger, and it fails closed: a commerce that
	// cannot be reached refuses rather than admits. It is priced as the screens it
	// will actually perform — every candidate over every event — so the biggest
	// operation on this surface is not also the cheapest.
	//
	// The gate runs where the SIZE is known: [plane.begin] measures the history
	// first and admits the run second, so the balance check is against the work
	// actually about to happen rather than against a flat fee that is wrong in both
	// directions.
	//
	// AND THE METER RUNS IN THE RUN, not here. This op answers 202 and the grid
	// runs behind it; metering at accept would charge for every candidate over
	// every event the moment the run was ADMITTED, and a rollout — which this
	// binary does at one replica, stopping the old pod first — cancels the run
	// partway with the debit already taken. What is charged is what the run
	// actually replayed, booked by [plane.begin] when the grid ends, however it
	// ends. The gate still runs on the upper bound, which is the contract this file
	// states: gate on what MIGHT happen, meter on what DID.
	var pay func(int)
	run, err := p.begin(ctx, t, days, func(events int) error {
		var err error
		pay, err = o.gate(ctx, "search", events*len(candidates()))
		return err
	}, func(screens int) {
		if pay != nil {
			pay(screens)
		}
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
// operator override is set. A clearly-named, configurable POLICY default — never
// a fabricated market price; ops sets the real number per deployment via
// CLOUD_RISK_PRICE_UUSD_PER_SCREEN. Zero makes screens free and therefore
// un-gated, mirroring the edge gate's price==0 pass-through.
const defaultScreenUUSD int64 = 100

// screenRate resolves the per-screen price. A negative or unparseable value falls
// through to the default, so a typo can never silently zero out billing.
func screenRate() int64 {
	s := strings.TrimSpace(os.Getenv("CLOUD_RISK_PRICE_UUSD_PER_SCREEN"))
	if s == "" {
		return defaultScreenUUSD
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return defaultScreenUUSD
	}
	return n
}

// screenMicros is what n screens cost, in micro-USD.
func screenMicros(n int) int64 {
	rate := screenRate()
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
	project, validated := principal.ValidatedProject(c)
	if err := o.s.Bill.Gate(ctx, ledger, project, validated, kind, cloud.MicrosToGateCents(screenMicros(n))); err != nil {
		return nil, cloud.Denied(err)
	}
	who := metering.Usage{
		Model: kind, Project: project, Actor: c.User(),
		RequestID: c.RequestID(), ClientIP: cloud.ClientIP(c),
	}
	bill := o.s.Bill
	return func(done int) {
		use := who
		use.AmountMicros = screenMicros(done)
		bill.MeterUsage(ledger, kind, use)
	}, nil
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
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return err
	}
	return zip.Errorf(500, "%v", err)
}

func verdict(a anomaly.Assessment) riskScoreOut {
	out := riskScoreOut{
		Scored: a.Scored, Refusal: a.Reason, Score: a.Score,
		Cut: a.Cut, Alert: a.Alert, Shadow: a.Shadow,
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

func modelState(t tenant, st anomaly.State, f fold, s strain) riskModelState {
	return riskModelState{
		Tenant: string(t), Shape: st.Digest, Live: !st.Config.Shadow,
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
		Events: r.Events, Refusal: r.Refusal, Trials: make([]riskTrial, 0, len(r.Trials)),
	}
	if !r.Ended.IsZero() {
		out.Ended = r.Ended.Format(time.RFC3339)
	}
	for _, tr := range r.Trials {
		out.Trials = append(out.Trials, wireTrial(tr))
	}
	if r.Winner != nil {
		w := wireTrial(*r.Winner)
		out.Winner = &w
	}
	return out
}

func wireTrial(t trial) riskTrial {
	return riskTrial{
		Topology: riskTopology{Trees: t.Topology.Trees, Depth: t.Topology.Depth,
			Window: t.Topology.Window, Blend: t.Topology.Blend, Review: t.Topology.Review},
		Learned: t.Learned, Scored: t.Scored, Alerted: t.Alerted,
		Stated: t.Stated, Realised: t.Realised, Warm: t.Warm,
		Saturated: t.Saturated, Curve: t.Curve, Fit: t.Fit,
	}
}

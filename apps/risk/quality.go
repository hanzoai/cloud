package risk

// quality.go is the CONTRACT for scoring quality: what a score means, why a
// decision went the way it did, what the organisation asked for at that
// probability, how good any of it is, and what a change would have done.
//
// Every operation here is a zip typed op, so one declaration is the REST route,
// the OpenAPI operation, the MCP tool, the CLI command and every generated SDK
// method. Nothing below is a raw handler and nothing below is written twice.
//
// THE GROUPS ARE RE-DECLARED IN THIS FILE ON PURPOSE. cmd/zipdoc resolves a
// group's prefix by reading the exact form `g := <router>.Group("/prefix")` IN
// THE FILE the ops are declared in, and files a doc comment under the path it
// resolves. A group arriving as a parameter is a prefix it cannot see, and it
// fails the generate rather than filing prose under the wrong path — so each
// file that declares ops declares its own groups. fiber matches middleware by
// path prefix in registration order, and mount installs cloud.Bridge on both
// prefixes before it calls mountQuality, so these ops sit behind the same tenant
// gate as every other one. tenant_test.go proves it rather than assuming it.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/calibrate"
	// The package is aml's `evaluate`; it is bound to `quality` here because
	// rule.go already declares a function called evaluate — the rule evaluator —
	// and one name cannot mean two things in one package.
	quality "github.com/luxfi/aml/pkg/evaluate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/reason"
	"github.com/zap-proto/zip"
)

// mountQuality registers the scoring-quality surface. Called from mount, last,
// so the tenant gate is already installed on both prefixes.
func mountQuality(s *stateService, app cloud.Router) {
	// ONE `g := <router>.Group("/prefix")` per line — see the file comment.
	g := app.Group("/v1/risk")
	gml := app.Group("/v1/ml")
	o := ops{s: s}

	// ── why ─────────────────────────────────────────────────────────────────
	zip.Get(g, "/reasons", o.reasons,
		zip.WithOperationID("riskReasons"),
		zip.WithSummary("Read the closed vocabulary of reason codes a decision can cite"),
		zip.WithTags("risk"))

	// ── the thresholds, as a governed record ────────────────────────────────
	zip.Get(g, "/policy", o.policy,
		zip.WithOperationID("riskPolicy"),
		zip.WithSummary("Read the score bands in force"),
		zip.WithTags("risk"))
	zip.Put(g, "/policy", o.setPolicy,
		zip.WithOperationID("riskSetPolicy"),
		zip.WithSummary("Set the score bands, as a new recorded version"),
		zip.WithTags("risk"))
	zip.Get(g, "/policy/versions", o.policyVersions,
		zip.WithOperationID("riskPolicyVersions"),
		zip.WithSummary("Read every version of a stage's bands, newest first"),
		zip.WithTags("risk"))

	// ── what a score means ──────────────────────────────────────────────────
	zip.Post(gml, "/calibrate", o.calibrate,
		zip.WithOperationID("mlCalibrate"),
		zip.WithSummary("Fit a calibration so a score reads as a probability"),
		zip.WithStatus(201),
		zip.WithTags("ml"))
	zip.Get(gml, "/calibration", o.calibration,
		zip.WithOperationID("mlCalibration"),
		zip.WithSummary("Read the calibration in force and how well it holds"),
		zip.WithTags("ml"))

	// ── how good it is ──────────────────────────────────────────────────────
	zip.Post(gml, "/evaluate", o.evaluate,
		zip.WithOperationID("mlEvaluate"),
		zip.WithSummary("Measure the decision plane against what actually happened"),
		zip.WithTags("ml"))
	zip.Get(gml, "/learning", o.learning,
		zip.WithOperationID("mlLearning"),
		zip.WithSummary("Read the learning curve: quality against how much history was seen"),
		zip.WithTags("ml"))

	// ── what a change would have done ───────────────────────────────────────
	zip.Post(gml, "/replay", o.replay,
		zip.WithOperationID("mlReplay"),
		zip.WithSummary("Replay a candidate policy over this tenant's own history"),
		zip.WithStatus(201),
		zip.WithTags("ml"))
	zip.Get(gml, "/replays/:id", o.replayReport,
		zip.WithOperationID("mlReplayReport"),
		zip.WithSummary("Read a replay report"),
		zip.WithTags("ml"))
}

// qualityCents is the floor price of one measurement: the bounded scan of the
// tenant's own file, before the arithmetic over what it returned.
//
// Every op below that walks the decision log is priced the same way because they
// are the same work, and pricing them differently would invite a caller to reach
// for the cheap one.
const qualityCents = 2

// perRows is how many rows one further cent buys.
//
// A flat price is a lie about a scan whose cost is linear in the rows it reads:
// a fit over 40 judged decisions and a twenty-step learning curve over fifty
// thousand cost the same two cents, and the second one is the one a caller
// loops. Gate on the CEILING the request could reach and meter on what it
// actually read, which is the standard shape — the ledger is debited before the
// work and settled by it.
const perRows = 5_000

// measureCents prices a read of n rows.
func measureCents(rows int) int64 {
	if rows < 0 {
		rows = 0
	}
	return qualityCents + int64(rows/perRows)
}

// inflight is the per-tenant bound on measurement.
//
// ONE measurement per tenant at a time, and the bound is PER TENANT by
// construction — there is no fleet-wide number to exhaust, so a tenant that
// hammers this surface degrades itself and nobody else. That is also the truth
// about the resource: these ops hold the tenant's single-writer file, so a
// second concurrent measurement for the same tenant is already queued behind the
// first at the connection pool, silently and without a deadline. Refusing it is
// the honest form of the same thing, and it leaves the tenant's own decide path
// its connection.
type inflight struct {
	mu   sync.Mutex
	busy map[Tenant]bool
}

func newInflight() *inflight { return &inflight{busy: map[Tenant]bool{}} }

// claim takes the tenant's measurement slot, or refuses. The release is the
// returned func and it is idempotent.
func (f *inflight) claim(t Tenant) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy[t] {
		return nil, zip.Errorf(429, "a measurement is already running for this organisation; "+
			"these read the same single-writer file and a second one would queue behind the first "+
			"holding the connection its own decisions need")
	}
	f.busy[t] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.busy, t)
			f.mu.Unlock()
		})
	}, nil
}

// measureDeadline bounds one measurement's reads.
//
// The arithmetic over the rows is pure and bounded by maxHistory; the READ is
// what can hang, on a file another request holds. Every statement below runs
// under this, so a caller that walks away, or a lock that is not released, ends
// as a refusal rather than as a goroutine holding a connection for the life of
// the process.
const measureDeadline = 60 * time.Second

// measuring establishes the whole preamble every measurement shares: the tenant,
// its file, its one slot, the deadline and the current scoring shape. One
// function, so the five ops cannot disagree about what a measurement is allowed
// to do.
func (o ops) measuring(ctx context.Context, kind string, days, rows int) (
	sc scope, db *sql.DB, shape string, mctx context.Context, done func(), err error,
) {
	sc, db, err = tenantState(ctx, o.s)
	if err != nil {
		return
	}
	if err = horizon(days); err != nil {
		return
	}
	// Gated on the CEILING this request could read, before any of it happens.
	if err = o.s.State.bill.Gate(ctx, sc.org, sc.project, sc.validate, kind, measureCents(bounded(rows))); err != nil {
		err = zip.Errorf(402, "%s", err.Error())
		return
	}
	release, err := o.s.State.inflight.claim(sc.tenant)
	if err != nil {
		return
	}
	shape, err = scoringShape(db, o.s.State.model.Digest())
	if err != nil {
		release()
		return
	}
	deadline, cancel := context.WithTimeout(ctx, measureDeadline)
	return sc, db, shape, deadline, func() { cancel(); release() }, nil
}

// bounded resolves a caller's row request to what the read will actually take.
func bounded(rows int) int {
	if rows <= 0 || rows > maxHistory {
		return maxHistory
	}
	return rows
}

// maxHorizon bounds the maturity horizon, in days.
//
// Ten years, which is past every retention window this plane has, and the point
// of the bound is not the ten years: a horizon is a caller-supplied integer that
// reaches a date computation, and an unbounded one walks the clock out of the
// range a stored timestamp can express. The comparison then still runs and still
// returns rows, which is the kind of wrong that produces a report rather than an
// error.
const maxHorizon = 3650

// horizon validates the one field every measurement here takes. One function, so
// the five ops cannot disagree about what a horizon is.
func horizon(days int) error {
	switch {
	case days < 0:
		return zip.ErrBadRequest("a maturity horizon cannot run backwards")
	case days > maxHorizon:
		return zip.Errorf(400, "a maturity horizon of %d days is beyond the %d this plane measures over", days, maxHorizon)
	}
	return nil
}

// ── the shapes ──────────────────────────────────────────────────────────────

// riskReasonCode is one entry of the closed vocabulary.
type riskReasonCode struct {
	// Code is the identifier a decision cites and a report counts. It is part of
	// the published contract: renaming one invalidates every stored decision
	// that cites it.
	Code string `json:"code"`
	// Source is where a reason with this code comes from — "model" for a feature
	// the detector reads, "rule" for a rule this organisation wrote.
	Source string `json:"source"`
	// Says is the sentence a person is shown, with no numbers in it. A decision
	// fills its own numbers in.
	Says string `json:"says"`
	// Typology is the pattern the feature expresses.
	Typology string `json:"typology,omitempty"`
	// Indicator is the supervisor's own words for what is being looked for.
	Indicator string `json:"indicator,omitempty"`
	// Citation is where those words come from, so the claim is checkable.
	Citation string `json:"citation,omitempty"`
	// Severity is the grading a reason with this code carries.
	Severity string `json:"severity"`
}

// riskReasonBook is the whole vocabulary.
type riskReasonBook struct {
	// Items is every code, model codes first in the model's own coordinate
	// order.
	Items []riskReasonCode `json:"items"`
	// Model is the digest of the detector shape these codes were derived from.
	// The vocabulary is a function of the feature inventory, so this pins which
	// inventory produced it.
	Model string `json:"model"`
}

// riskPolicyIn narrows the read to one stage.
type riskPolicyIn struct {
	// Stage is signup, payment, session, usage or payout. Empty reads every
	// stage this organisation has set bands for.
	Stage string `json:"stage,omitempty"`
}

// riskBand is one rung of the ladder: at or above this probability, this action.
type riskBand struct {
	// At is the boundary, in [0,1]. It is a PROBABILITY and not a raw score —
	// a band over an uncalibrated score is a band over units that mean nothing,
	// and the cost arithmetic beneath it would be meaningless too.
	At float64 `json:"at"`
	// Action is what to do at or above it: challenge, review, restrict or block.
	Action string `json:"action"`
}

// riskCost is what being wrong is worth, in nano-units of this organisation's
// accounting currency. Integers, because this is money and it is multiplied by
// counts; a float here is a rounding error waiting to be argued about.
type riskCost struct {
	// Miss is the price of letting through what should have been stopped — on a
	// payment lane, typically the disputed amount plus the network's fee.
	Miss int64 `json:"miss"`
	// Alarm is the price of stopping something that was fine: an abandoned
	// purchase, a support contact, and the share of those customers who do not
	// come back.
	Alarm int64 `json:"alarm"`
}

// riskPolicyBands is one stage's ladder as a record.
type riskPolicyBands struct {
	// Stage is the lifecycle moment this governs. One ladder per stage: the
	// price of a false decline at signup and at payout are different numbers
	// about different things.
	Stage string `json:"stage"`
	// Version is monotone within the stage. A change mints the next one and
	// nothing is edited, so what was in force on a given day is a lookup rather
	// than a reconstruction.
	Version int `json:"version"`
	// At is when it took effect, RFC 3339 in UTC.
	At string `json:"at"`
	// By is who decided. A control that changed and cannot say who changed it is
	// a control nobody owns.
	By string `json:"by"`
	// Reason is why it changed. Required on a write: a threshold moved without a
	// stated reason is a decision with no author.
	Reason string `json:"reason,omitempty"`
	// Floor is the action below the first band.
	Floor string `json:"floor"`
	// Bands is the ladder, ascending.
	Bands []riskBand `json:"bands"`
	// Shadow observes without acting: every band is computed and recorded and
	// the action returned is always the floor. It is a second gate in series
	// with the tenant's live/shadow mode, so one flag flipped by mistake still
	// cannot make an organisation act.
	Shadow bool `json:"shadow"`
	// Cost is what this organisation says being wrong is worth. It is what makes
	// a threshold defensible rather than chosen, and it is what the
	// cost-weighted metric is computed from.
	Cost riskCost `json:"cost"`
	// Digest identifies what this ladder DECIDES — not who wrote it or when — so
	// a re-save that changed nothing keeps its identity and a moved threshold
	// does not.
	Digest string `json:"digest"`
}

// riskPolicyBook is every stage's bands.
type riskPolicyBook struct {
	// Items is one entry per stage, in stage order.
	Items []riskPolicyBands `json:"items"`
}

// riskPolicyVersionsIn addresses one stage's history.
type riskPolicyVersionsIn struct {
	// Stage is the lifecycle moment. Required: a version history across stages
	// would interleave unrelated decisions.
	Stage string `json:"stage"`
	// Limit bounds the page, 1..200, default 50.
	Limit int `json:"limit,omitempty"`
}

// mlCalibrateIn asks for a fit over this organisation's own judged decisions.
type mlCalibrateIn struct {
	// Method is isotonic or platt. Isotonic assumes only that a higher score is
	// never less likely to be productive, which is the one assumption the
	// detector guarantees, and needs data. Platt has two parameters and survives
	// a small sample at the cost of assuming a shape. Empty takes isotonic.
	Method string `json:"method,omitempty"`
	// Horizon is how many days a decision must have aged before its outcome
	// counts as settled. It is the single most consequential field here: an
	// analyst judges within hours and a card dispute lands 30 to 120 days later,
	// so measuring over everything strips the recent tail of exactly the
	// chargebacks that make the base rate what it is. 120 for a payment lane, 14
	// for signup abuse, 0 only where every label genuinely arrives at once.
	// Between 0 and 3650, which is past every retention window here.
	Horizon int `json:"horizon"`
	// Rows bounds the history read, 1..50000. Zero takes the bound.
	Rows int `json:"rows,omitempty"`
}

// mlBin is one bucket of the reliability report: what was predicted here, and
// what actually happened.
type mlBin struct {
	// From is the lower bound of the predicted probability, inclusive.
	From float64 `json:"from"`
	// To is the upper bound, exclusive.
	To float64 `json:"to"`
	// Rows is how many decisions fell in the bin.
	Rows int `json:"rows"`
	// Predicted is the mean probability the map gave them. ABSENT for an empty
	// bin: a point drawn at zero would claim perfect confidence in innocence.
	Predicted *float64 `json:"predicted,omitempty"`
	// Observed is the share of them that turned out productive. A calibrated map
	// has it equal to Predicted, and the gap between the two is the whole report.
	// Absent for an empty bin, for the same reason.
	Observed *float64 `json:"observed,omitempty"`
}

// mlCalibrationView is the map in force and how well it holds.
type mlCalibrationView struct {
	// Fitted is false when this organisation has no calibration. Everything
	// below is then empty and the decision plane reports probabilities as
	// absent rather than inventing them.
	Fitted bool `json:"fitted"`
	// Version is monotone; a fit never replaces an earlier one in place.
	Version int `json:"version,omitempty"`
	// At is when it was fitted, RFC 3339 in UTC.
	At string `json:"at,omitempty"`
	// Method is isotonic or platt.
	Method string `json:"method,omitempty"`
	// Digest identifies this fit exactly, so a decision can name the map that
	// produced its probability.
	Digest string `json:"digest,omitempty"`
	// Shape is the scoring shape it was fitted under: the detector's geometry and
	// feature inventory folded with this organisation's enabled rule set.
	Shape string `json:"shape,omitempty"`
	// Current says whether that shape is still the one in force. When it is not,
	// the map REFUSES to answer and every decision reports its probability as
	// absent — which is the training-serving skew control working, and the field
	// to read first.
	Current bool `json:"current"`
	// Rows is how many judged decisions the fit was taken over.
	Rows int `json:"rows,omitempty"`
	// Productive is how many of them turned out to be worth acting on.
	Productive int `json:"productive,omitempty"`
	// Unproductive is how many turned out fine. Both classes are reported so the
	// imbalance is visible before anyone reads a probability computed over it.
	Unproductive int `json:"unproductive,omitempty"`
	// Prevalence is the productive share of that sample. A probability read
	// without it is uninterpretable: 0.4 is alarming at a base rate of one in a
	// thousand and unremarkable at one in three.
	Prevalence float64 `json:"prevalence,omitempty"`
	// Brier is the mean squared error on the fitting sample — the in-sample
	// number, and therefore optimistic. The honest one is on the learning
	// curve's held-out arm.
	Brier float64 `json:"brier,omitempty"`
	// Horizon is the maturity horizon the fit was taken under, in days.
	Horizon int `json:"horizon,omitempty"`
	// Immature is how many labelled decisions that horizon excluded for being too
	// young. A horizon that quietly drops most of the evidence is the difference
	// between a thin answer and a wrong one, so it is reported beside every fit.
	Immature int `json:"immature,omitempty"`
	// Superseded is how many mature judged decisions were excluded because they
	// were scored under a DIFFERENT scoring shape. A fit reads one coordinate
	// system only — stamping today's shape on yesterday's scores is exactly the
	// skew the shape gate refuses — so this is the evidence a refit cannot
	// honestly use, and the number that says why a fit went thin after a governed
	// change.
	Superseded int `json:"superseded,omitempty"`
	// Truncated says the bounded read cut the history: there are older mature
	// decisions under this shape that nothing here was computed from. The read
	// takes the MOST RECENT rows, so what is missing is the oldest.
	Truncated bool `json:"truncated,omitempty"`
	// Reliability is the fit against reality, in bins — the diagonal a console
	// draws. ABSENT when the map refuses to answer: a reliability report is the
	// map applied to every row, so a chart beside a refusal draws what the
	// refusal just said does not exist.
	Reliability []mlBin `json:"reliability,omitempty"`
	// Refusal names why there is no fit, when there is none.
	Refusal string `json:"refusal,omitempty"`
}

// mlMeasureIn asks for a measurement over this organisation's own history.
type mlMeasureIn struct {
	// Stage selects whose bands supply the operating point and the prices.
	// Empty measures at the cost-minimising threshold with no stated price,
	// which reports separation and nothing about money.
	Stage string `json:"stage,omitempty"`
	// Horizon is the maturity horizon in days, 0 to 3650 — see mlCalibrate, where
	// the same field decides the same thing and for the same reason.
	Horizon int `json:"horizon"`
	// Rows bounds the history read, 1..50000. Zero takes the bound.
	Rows int `json:"rows,omitempty"`
}

// mlConfusion is the four counts at the operating point.
type mlConfusion struct {
	// TP is productive and flagged: caught.
	TP float64 `json:"tp"`
	// FN is productive and let through — the misses, which the price of a miss
	// values.
	FN float64 `json:"fn"`
	// FP is unproductive and flagged — the false alarms, which the price of an
	// alarm values.
	FP float64 `json:"fp"`
	// TN is unproductive and let through: correctly left alone.
	TN float64 `json:"tn"`
}

// mlMetrics is the report. Accuracy is deliberately absent: on a stream with one
// productive event in a thousand, saying "fine" to everything is 99.9% accurate,
// so accuracy is not a weak metric here but an actively misleading one.
//
// Every rate is a POINTER. An unmeasured proportion reported as 0.0 reads as a
// perfect model, and on a plane where most decisions are unjudged that is the
// default state.
type mlMetrics struct {
	// Rows is every decision looked at.
	Rows int `json:"rows"`
	// Judged is how many of those carry an outcome. The gap between it and Rows
	// is the honest limit on everything below, which is why the pair is reported
	// first.
	Judged int `json:"judged"`
	// Productive is how many judged decisions were worth acting on.
	Productive int `json:"productive"`
	// Unproductive is how many were not. Both classes are reported so the
	// imbalance is visible before anyone reads a rate computed over it.
	Unproductive int `json:"unproductive"`
	// Prevalence is the productive share of the judged set.
	Prevalence *float64 `json:"prevalence,omitempty"`
	// ROC is the area under the receiver-operating curve, ties counted as half a
	// win. Insensitive to the base rate, which makes it comparable across
	// organisations and optimistic-looking on a rare-event stream.
	ROC *float64 `json:"roc,omitempty"`
	// PR is average precision — the area under precision-recall by the step
	// definition, never the trapezoid. This is the number that moves when a
	// rare-event model gets better.
	PR *float64 `json:"pr,omitempty"`
	// Threshold is the score everything below is measured at.
	Threshold *float64 `json:"threshold,omitempty"`
	// Precision is the productive share of what was flagged at that point — the
	// number an investigator experiences as the alert queue's hit rate.
	Precision *float64 `json:"precision,omitempty"`
	// Recall is the flagged share of what was productive: how much of the harm
	// was caught.
	Recall *float64 `json:"recall,omitempty"`
	// F1 is their harmonic mean. It is reported for continuity with what people
	// expect and it hides the trade-off the two prices make explicit; read the
	// cost instead when one is stated.
	F1 *float64 `json:"f1,omitempty"`
	// Alarm is the share of the judged stream flagged there — the realised
	// review rate an operations team is resourced against.
	Alarm *float64 `json:"alarm,omitempty"`
	// Lift is precision over prevalence: how much better than chance the flagged
	// set is. One means the plane is choosing at random.
	Lift *float64 `json:"lift,omitempty"`
	// Confusion is the four counts there.
	Confusion *mlConfusion `json:"confusion,omitempty"`
	// CostNano is what being wrong cost at the operating point, in nano-units of
	// the organisation's accounting currency. Absent when the stage's bands state
	// no price, because a cost computed from unstated prices is a number with a
	// decimal point and no meaning.
	CostNano *int64 `json:"cost_nano,omitempty"`
	// BestNano is the lowest cost reachable anywhere on the sweep. Absent for the
	// same reason.
	BestNano *int64 `json:"best_nano,omitempty"`
	// BestThreshold is where that lowest cost sits. The gap between it and
	// Threshold is what a threshold review is about.
	BestThreshold *float64 `json:"best_threshold,omitempty"`
	// Brier is the calibration error. A well-ranked, badly-calibrated plane
	// makes every band mean something other than what it says, and neither ROC
	// nor PR can see that: both are rank statistics and a monotone calibration
	// cannot move either.
	Brier *float64 `json:"brier,omitempty"`
	// Refusal names why a report is thin when it is.
	Refusal string `json:"refusal,omitempty"`
}

// mlPoint is one operating point of the sweep — one per distinct score, because
// two decisions that scored the same cannot be separated by any threshold.
type mlPoint struct {
	// Threshold is the score at or above which a decision is flagged.
	Threshold float64 `json:"threshold"`
	// TruePositive is recall at this point — the ROC vertical axis, and the PR
	// horizontal one.
	TruePositive float64 `json:"tpr"`
	// FalsePositive is fall-out: the flagged share of what was unproductive. The
	// ROC horizontal axis.
	FalsePositive float64 `json:"fpr"`
	// Precision is the productive share of what was flagged. The PR vertical
	// axis, against TruePositive as recall.
	Precision float64 `json:"precision"`
	// CostNano is what this point costs under the stated prices, zero when none
	// are stated.
	CostNano int64 `json:"cost_nano"`
}

// mlMeasurement is the whole answer: the metrics, the curve a console plots, and
// the two numbers that bound how much either can be trusted.
type mlMeasurement struct {
	// Metrics is the report at the operating point.
	Metrics mlMetrics `json:"metrics"`
	// Curve is the sweep — ROC, precision-recall and cost, three charts from one
	// pass because they are three projections of it.
	Curve []mlPoint `json:"curve,omitempty"`
	// Horizon is the maturity horizon applied, in days.
	Horizon int `json:"horizon"`
	// Immature is how many labelled decisions that horizon excluded for being too
	// young for their outcome to have arrived.
	Immature int `json:"immature"`
	// Superseded is how many mature judged decisions were scored under a
	// different scoring shape and therefore measured nowhere here.
	Superseded int `json:"superseded,omitempty"`
	// Truncated says the bounded read cut the history at its oldest end.
	Truncated bool `json:"truncated,omitempty"`
	// Calibrated says whether a probability was available at all. When it is
	// false, Brier is absent and the operating point is the raw score — the
	// SAME state the decide path is in, which is the point: this op must not
	// describe a world the live plane refuses to enter.
	Calibrated bool `json:"calibrated"`
	// Refusal names why there was no probability, when there was none.
	Refusal string `json:"refusal,omitempty"`
	// Unacted is the share of the judged evidence that came from decisions the
	// plane LET THROUGH. It is the honest limit on every number above: a blocked
	// decision has no outcome, so a judged set made entirely of decisions the
	// incumbent chose to act on measures agreement with the incumbent rather than
	// accuracy. Near zero means exactly that.
	Unacted float64 `json:"unacted"`
	// Stage is whose bands supplied the operating point and the prices, when one
	// did.
	Stage string `json:"stage,omitempty"`
}

// mlLearningIn asks for the learning curve.
type mlLearningIn struct {
	// Method is isotonic or platt, as for a fit.
	Method string `json:"method,omitempty"`
	// Stage supplies the prices, so the cost arm of the curve means something.
	Stage string `json:"stage,omitempty"`
	// Horizon is the maturity horizon in days, 0 to 3650.
	Horizon int `json:"horizon"`
	// Steps is how many points the curve has, 2..20, default 10.
	Steps int `json:"steps,omitempty"`
	// Rows bounds the history read, 1..50000. Zero takes the bound.
	Rows int `json:"rows,omitempty"`
}

// mlLearningStep is one point of the curve.
type mlLearningStep struct {
	// Rows is how many decisions the fit was given.
	Rows int `json:"rows"`
	// Judged is how many of those carried an outcome — the number that actually
	// constrains the fit.
	Judged int `json:"judged"`
	// Productive is how many of the judged were positive. A step with two
	// positives produces a number and not evidence, and this is how a reader
	// tells which.
	Productive int `json:"productive"`
	// Held is the size of the fixed later window this step was measured against.
	// It is the same at every step, which is the whole reason two steps are
	// comparable.
	Held int `json:"held"`
	// Train is the report on the rows the fit saw, and is optimistic by
	// construction.
	Train mlMetrics `json:"train"`
	// Ahead is the report on the held window, strictly later than anything the
	// fit saw. Two arms, because one alone cannot tell "more labels would help"
	// from "the fit has started memorising".
	Ahead mlMetrics `json:"ahead"`
	// Refusal names why a step produced no fit — almost always too little of one
	// class by that point. Stated rather than skipped, so the curve shows where
	// the plane became able to answer at all.
	Refusal string `json:"refusal,omitempty"`
}

// mlLearningCurve is quality against how much history the fit had seen.
type mlLearningCurve struct {
	// Steps is the curve, oldest prefix first.
	Steps []mlLearningStep `json:"steps"`
	// Horizon is the maturity horizon applied, in days.
	Horizon int `json:"horizon"`
	// Immature is how many labelled decisions that horizon excluded.
	Immature int `json:"immature"`
	// Superseded is how many mature judged decisions were scored under a
	// different scoring shape and therefore appear at no step.
	Superseded int `json:"superseded,omitempty"`
	// Truncated says the bounded read cut the history at its oldest end.
	Truncated bool `json:"truncated,omitempty"`
	// Refusal names why the curve is empty when it is.
	Refusal string `json:"refusal,omitempty"`
}

// mlReplayIn is a candidate to try against what already happened.
type mlReplayIn struct {
	// Stage is which lifecycle moment's decisions to replay. Required: replaying
	// a payout ladder over signup decisions answers a question nobody asked.
	Stage string `json:"stage"`
	// Floor is the action below the first band. Empty takes the floor the stage's
	// current ladder states.
	Floor string `json:"floor,omitempty"`
	// Bands is the candidate ladder. Empty replays the ladder currently in force,
	// which is how a change of maturity horizon or of window is compared on its
	// own.
	Bands []riskBand `json:"bands,omitempty"`
	// Cost is what being wrong is worth under the candidate. Empty takes the
	// prices the stage's current bands state.
	Cost *riskCost `json:"cost,omitempty"`
	// Horizon is the maturity horizon in days, 0 to 3650.
	Horizon int `json:"horizon"`
	// Rows bounds the history read, 1..50000. Zero takes the bound.
	Rows int `json:"rows,omitempty"`
}

// mlTally is one action and how often it was reached. A list and not a map: a
// map's iteration order is randomised per process, so a report built from one
// could not have a stable identity and two runs could not be compared.
type mlTally struct {
	// Action is the action reached.
	Action string `json:"action"`
	// Count is how many decisions reached it.
	Count int `json:"count"`
}

// mlMove is one decision the candidate would have decided differently.
type mlMove struct {
	// ID is the decision this move is about.
	ID string `json:"id"`
	// At is when it was decided, RFC 3339 in UTC.
	At string `json:"at"`
	// Was is what the plane actually did.
	Was string `json:"was"`
	// Would is what the candidate would have done instead.
	Would string `json:"would"`
	// Score is what the model recorded at the time. It is replayed, never
	// recomputed: the rings have moved on and the detector has since learned from
	// this very event.
	Score float64 `json:"score"`
	// Probability is what the calibration makes of that score. Absent when no
	// calibration is in force or the one on file was fitted under another shape.
	Probability *float64 `json:"probability,omitempty"`
	// Outcome is what turned out to be true, when anyone judged it. It is what
	// turns a list of differences into a list of improvements or of regressions.
	Outcome string `json:"outcome,omitempty"`
}

// mlReplayReport is what a candidate would have done.
type mlReplayReport struct {
	// ID identifies this report for the whole of its life. A threshold change is
	// justified by one, so the justification has an address.
	ID string `json:"id"`
	// At is when it was run, RFC 3339 in UTC.
	At string `json:"at"`
	// Rows is how many decisions were replayed.
	Rows int `json:"rows"`
	// From is the earliest decision in the period, RFC 3339 in UTC.
	From string `json:"from,omitempty"`
	// To is the latest.
	To string `json:"to,omitempty"`
	// Was is the action distribution the plane actually produced, ascending by
	// action.
	Was []mlTally `json:"was"`
	// Would is the distribution the candidate would have produced.
	Would []mlTally `json:"would"`
	// Changed is how many decisions would differ, exactly, and is never cut.
	Changed int `json:"changed"`
	// Tightened is how many of those the candidate would act harder on.
	Tightened int `json:"tightened"`
	// Loosened is how many it would let through that the plane did not. That
	// direction is the one a review has to justify.
	Loosened int `json:"loosened"`
	// Moved lists the differences, oldest first, cut at a bound. Changed is
	// never cut.
	Moved []mlMove `json:"moved,omitempty"`
	// Metrics is the candidate measured against the judged subset at the
	// operating point its FIRST band implies — the point where it stops doing
	// nothing.
	Metrics mlMetrics `json:"metrics"`
	// Explore is the share of the judged evidence that came from the
	// below-the-line sample rather than from something the plane chose to look
	// at. Near zero means the report is measuring agreement with the incumbent,
	// which is a property of the evidence and not of the candidate.
	Explore float64 `json:"explore"`
	// Policy is the digest of the candidate ladder that was replayed.
	Policy string `json:"policy"`
	// Calibration is the digest of the map that turned each recorded score into
	// the probability the ladder read. Empty when none was in force.
	Calibration string `json:"calibration,omitempty"`
	// Digest is the identity of this report. Two replays of one candidate over
	// one history agree on it; if they do not, the inputs moved. It is what makes
	// a replay evidence rather than an anecdote.
	Digest string `json:"digest"`
	// Refusal names why a report is empty when it is. An empty history and a
	// candidate that changes nothing render identically otherwise.
	Refusal string `json:"refusal,omitempty"`
}

// ── the operations ──────────────────────────────────────────────────────────

// Reasons is the closed vocabulary of reason codes a decision can cite.
//
// It is DERIVED from the detector's own feature inventory rather than declared
// beside it, so it cannot name a feature the model does not read and cannot omit
// one it does. Every model code carries the typology, the supervisory indicator
// and the published citation those words come from, so a reason shown to a
// person is checkable rather than asserted.
//
// A free-text reason cannot be counted, cannot be tested and cannot be compared
// between two decisions. Every regime that lets someone question an automated
// decision asks for the principal reasons specifically — which is a ranked list
// over a fixed vocabulary, and this is the vocabulary.
func (o ops) reasons(ctx context.Context, _ *riskNoInput) (*riskReasonBook, error) {
	if _, _, err := tenantState(ctx, o.s); err != nil {
		return nil, err
	}
	codes := reason.Codes()
	book := &riskReasonBook{
		Items: make([]riskReasonCode, 0, len(codes)),
		Model: o.s.State.model.Digest(),
	}
	for _, c := range codes {
		book.Items = append(book.Items, riskReasonCode{
			Code: c.Code, Source: c.Source, Says: c.Says,
			Typology: c.Typology, Indicator: c.Indicator,
			Citation: c.Citation, Severity: c.Severity,
		})
	}
	return book, nil
}

// Policy reads the score bands in force, per lifecycle stage.
//
// A band maps a calibrated PROBABILITY to an action, which is why it is a
// governed value and not a constant: where the line sits between letting a
// customer through and stopping them is a business decision with a price on
// either side, and it belongs to the organisation making it. A threshold
// compiled into the scoring path is a decision nobody signed and cannot be shown
// to have been in force on the day a particular customer was declined.
func (o ops) policy(ctx context.Context, in *riskPolicyIn) (*riskPolicyBook, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	var wanted []string
	if s := strings.ToLower(strings.TrimSpace(in.Stage)); s != "" {
		if !stages[s] {
			return nil, zip.Errorf(400, "%q is not a lifecycle stage", in.Stage)
		}
		wanted = []string{s}
	} else if wanted, err = policyStages(db); err != nil {
		return nil, err
	}

	book := &riskPolicyBook{Items: make([]riskPolicyBands, 0, len(wanted))}
	for _, s := range wanted {
		p, ok, err := currentPolicy(db, s)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		book.Items = append(book.Items, wireBands(p))
	}
	return book, nil
}

// SetPolicy records a new version of one stage's bands.
//
// It is an INSERT and never an update. A policy change is a governance decision,
// so the previous version stays exactly as it was and "what was in force when
// this customer was declined" is a lookup rather than a reconstruction. The
// version is minted here, the author comes from the validated principal rather
// than from the body, and the reason is required — a threshold that moved with
// no stated reason is a decision with no author.
//
// Every refusal below is a real way a ladder silently stops being a risk policy:
// a band above one is an action nothing can reach, bands out of order mean a
// higher probability asks for less, two bands at one probability leave which
// applies undecided, and a band repeating the action beneath it is dead weight
// that makes the ladder look like it has more steps than it has.
func (o ops) setPolicy(ctx context.Context, in *riskPolicyBands) (*riskPolicyBands, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	stage := strings.ToLower(strings.TrimSpace(in.Stage))
	if !stages[stage] {
		return nil, zip.Errorf(400, "%q is not a lifecycle stage", in.Stage)
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, zip.ErrBadRequest("a change of thresholds is a recorded decision and needs a stated reason")
	}
	if !actions[strings.ToLower(strings.TrimSpace(in.Floor))] {
		return nil, zip.Errorf(400, "%q is not an action", in.Floor)
	}
	rungs := make([]policy.Rung, 0, len(in.Bands))
	for _, b := range in.Bands {
		a := strings.ToLower(strings.TrimSpace(b.Action))
		if !actions[a] {
			return nil, zip.Errorf(400, "%q is not an action", b.Action)
		}
		rungs = append(rungs, policy.Rung{At: b.At, Action: a})
	}

	// The AUTHOR is the person, from the validated principal — the same helper the
	// suppression and control records already use. "The org set this threshold" is
	// not an answer to who signed a governance change.
	saved, err := putPolicy(db, policy.Policy{
		Stage: stage, By: by(ctx, sc), Reason: strings.TrimSpace(in.Reason),
		Floor: strings.ToLower(strings.TrimSpace(in.Floor)),
		Rungs: rungs, Shadow: in.Shadow,
		Cost: policy.Cost{Miss: in.Cost.Miss, Alarm: in.Cost.Alarm},
	})
	if err != nil {
		return nil, err
	}
	emit(sc.org, "risk.policy", map[string]any{
		"stage": saved.Stage, "version": saved.Version, "digest": saved.Digest, "shadow": saved.Shadow,
	})
	out := wireBands(saved)
	return &out, nil
}

// PolicyVersions is the audit trail: every version of one stage's bands, newest
// first, each with who set it, when and why.
//
// Nothing here was ever edited, so this is the whole history and not a
// reconstruction of one.
func (o ops) policyVersions(ctx context.Context, in *riskPolicyVersionsIn) (*riskPolicyBook, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	stage := strings.ToLower(strings.TrimSpace(in.Stage))
	if !stages[stage] {
		return nil, zip.Errorf(400, "%q is not a lifecycle stage", in.Stage)
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	versions, err := policyVersions(db, stage, limit)
	if err != nil {
		return nil, err
	}
	book := &riskPolicyBook{Items: make([]riskPolicyBands, 0, len(versions))}
	for _, p := range versions {
		book.Items = append(book.Items, wireBands(p))
	}
	return book, nil
}

// Calibrate fits the map that turns this organisation's scores into
// probabilities, from its own judged decisions.
//
// A detector's score is a density — how isolated an event is among the mass this
// organisation's recent traffic laid down. It ranks well and it means nothing on
// its own: 0.8 is not "80% likely", and two organisations' 0.8s are not
// comparable. Every band set on such a score is a number someone liked and every
// cost calculation over it is arithmetic on the wrong units.
//
// The fit is refused rather than approximated when the evidence cannot support
// one — too few judged decisions, or all of them one class — because a
// calibration fitted on one class is a constant, and a constant reported as a
// probability is a lie with a decimal point on it.
//
// It records the SCORING SHAPE it was fitted under: the detector's geometry and
// feature inventory folded with this organisation's enabled rule set. If either
// moves, the map refuses to answer and the decision plane reports probabilities
// as absent — which is the training-serving skew control, and the reason it
// cannot fail silently.
func (o ops) calibrate(ctx context.Context, in *mlCalibrateIn) (*mlCalibrationView, error) {
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "" {
		method = calibrate.Isotonic
	}
	if method != calibrate.Isotonic && method != calibrate.Platt {
		return nil, zip.Errorf(400, "%q is not a calibration method", in.Method)
	}
	sc, db, shape, mctx, done, err := o.measuring(ctx, "calibrate", in.Horizon, in.Rows)
	if err != nil {
		return nil, err
	}
	defer done()

	w, err := recorded(mctx, db, in.Horizon, shape, in.Rows)
	if err != nil {
		return nil, err
	}
	young, err := immature(mctx, db, in.Horizon)
	if err != nil {
		return nil, err
	}
	o.s.State.bill.Meter(sc.org, sc.project, "calibrate", measureCents(len(w.history)), sc.request, sc.clientIP)

	m, err := calibrate.Fit(samples(w.history), method, shape)
	if err != nil {
		// A refusal is the answer, not a failure: the caller needs to know the
		// plane cannot yet say what a score means, and why. When the evidence is
		// thin only because the shape moved, say THAT — it is a different fact with
		// a different remedy, and the remedy is time rather than another POST.
		out := &mlCalibrationView{
			Horizon: in.Horizon, Immature: young, Superseded: w.superseded, Refusal: err.Error(),
		}
		if w.superseded > 0 {
			out.Refusal += fmt.Sprintf("; %d further judged decisions were scored under a different "+
				"scoring shape and cannot be fitted under this one — a fit over them would stamp "+
				"today's coordinates on yesterday's scores, which is the skew this plane refuses",
				w.superseded)
		}
		return out, nil
	}
	// The AUTHOR is the person, resolved from the validated principal. A
	// calibration is a governance record and "the org changed it" names nobody.
	version, at, err := putCalibration(db, m, by(ctx, sc), in.Horizon)
	if err != nil {
		return nil, err
	}
	emit(sc.org, "risk.calibration", map[string]any{
		"version": version, "method": m.Method, "rows": m.Rows, "brier": m.Brier,
	})
	return view(m, version, at, shape, in.Horizon, young, w), nil
}

// Calibration reads the map in force and how well it holds against reality.
//
// The reliability report is the diagonal a console draws: bins of predicted
// probability against the share that actually turned out productive. An empty
// bin reports absence rather than zero, because a point drawn at zero on that
// chart claims perfect confidence in innocence.
//
// Current is the field to read first. When it is false the scoring shape has
// moved since the fit — a feature changed, or a rule was written, retired or
// reweighted — and the map refuses to answer, so every decision since reports
// its probability as absent. The remedy is a new fit, and until then the bands
// cannot act.
func (o ops) calibration(ctx context.Context, in *mlMeasureIn) (*mlCalibrationView, error) {
	// It is GATED, METERED and BOUNDED like the other measurements, because it IS
	// one: the reliability report walks the same bounded scan of the same file and
	// does the same arithmetic over it. An op priced at nothing beside four that
	// are priced is the one a caller loops.
	sc, db, shape, mctx, done, err := o.measuring(ctx, "calibration", in.Horizon, in.Rows)
	if err != nil {
		return nil, err
	}
	defer done()

	m, version, at, ok, err := currentCalibration(db)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &mlCalibrationView{
			Refusal: "no calibration is fitted for this organisation, so a score is a rank and not a probability",
		}, nil
	}
	w, err := recorded(mctx, db, in.Horizon, shape, in.Rows)
	if err != nil {
		return nil, err
	}
	young, err := immature(mctx, db, in.Horizon)
	if err != nil {
		return nil, err
	}
	o.s.State.bill.Meter(sc.org, sc.project, "calibration", measureCents(len(w.history)), sc.request, sc.clientIP)
	return view(m, version, at, shape, in.Horizon, young, w), nil
}

// Evaluate measures the decision plane against what actually happened.
//
// The metrics suit imbalanced data because fraud is rare and the usual ones do
// not: accuracy is not computed at all, ROC-AUC is reported with the prevalence
// beside it because it is uninterpretable without one, and average precision is
// computed by the step definition rather than the trapezoid — interpolating
// between operating points on a precision-recall curve credits a plane with
// points it cannot reach, and flatters exactly the rare-event case the metric
// exists for.
//
// The cost-weighted number is the one anybody outside the team cares about. A
// miss and a false alarm have different prices, both stated on the stage's own
// bands, both integers of nano-units so the arithmetic is exact. The
// cost-minimising threshold is reported beside the one in force, and the gap
// between them is what a threshold review is about.
//
// Unacted bounds the trust in all of it: a blocked decision has no outcome, so a
// judged set drawn only from decisions the plane already acted on measures
// agreement with the incumbent rather than accuracy.
func (o ops) evaluate(ctx context.Context, in *mlMeasureIn) (*mlMeasurement, error) {
	sc, db, shape, mctx, done, err := o.measuring(ctx, "evaluate", in.Horizon, in.Rows)
	if err != nil {
		return nil, err
	}
	defer done()

	stage, pol, err := stagePolicy(db, in.Stage)
	if err != nil {
		return nil, err
	}
	w, err := recorded(mctx, db, in.Horizon, shape, in.Rows)
	if err != nil {
		return nil, err
	}
	young, err := immature(mctx, db, in.Horizon)
	if err != nil {
		return nil, err
	}
	// The calibration is BOUND to the shape in force before a single number is
	// computed from it. Unbound, this op measured under whatever map was on file
	// and reported a Brier the decide path refuses to stand behind — the same
	// coordinates, the same rows, and the opposite answer to the one the live
	// plane gives. A bind that refuses leaves the zero Reader, and every
	// probability-shaped number below is then absent rather than invented.
	cal, calRefusal := reader(db, shape)
	o.s.State.bill.Meter(sc.org, sc.project, "evaluate", measureCents(len(w.history)), sc.request, sc.clientIP)

	obs := make([]quality.Observation, 0, len(w.history))
	for _, h := range w.history {
		obs = append(obs, h.Observation)
	}
	// The operating point is where the FIRST band starts acting; with no bands
	// the sweep is reported and the point is the cost-minimising one, which is
	// zero when no price is stated and is then simply "flag everything", stated
	// as such by the confusion counts rather than presented as a recommendation.
	at := 0.0
	if len(pol.Rungs) > 0 && cal.Fitted() {
		at = scoreFor(cal, pol.Rungs[0].At)
	}
	share, _ := unacted(w.history)
	m := quality.Measure(obs, at, pol.Cost, cal)
	out := &mlMeasurement{
		Metrics: wireMetrics(m), Horizon: in.Horizon, Immature: young,
		Superseded: w.superseded, Truncated: w.truncated,
		Unacted: share, Stage: stage, Calibrated: cal.Fitted(),
	}
	if !cal.Fitted() {
		out.Refusal = calRefusal
	}
	for _, p := range quality.Curve(obs, pol.Cost) {
		out.Curve = append(out.Curve, mlPoint{
			Threshold: p.Threshold, TruePositive: p.TruePositive,
			FalsePositive: p.FalsePositive, Precision: p.Precision, CostNano: p.CostNano,
		})
	}
	return out, nil
}

// Learning is the curve an operator reads before spending money on more labels:
// quality as a function of how much history the fit had.
//
// It has TWO arms and the pair is the point. The train arm is the metric on the
// rows the fit saw, optimistic by construction. The ahead arm is the metric on a
// FIXED later window — the same rows at every step, all of them after everything
// any step trained on. Both still falling means more labels would help; the
// train arm falling while the ahead arm has stopped means the fit has started
// memorising the sample and the next thing to change is the method, not the
// volume.
//
// The split is temporal and only temporal, never random. A random split lets a
// fit see the future — fraud arrives in campaigns, and half a campaign on each
// side of the line scores brilliantly on a pattern the fit has already been
// shown — and lets one device or one card appear on both sides, which rewards
// memorising an identifier and is invisible in every metric.
//
// The separation arm and the calibration arm say different things: a monotone
// calibration cannot reorder anything, so ROC and PR are identical before and
// after fitting one. ROC and PR moving along the curve is the DETECTOR getting
// better as it learns; Brier moving is the CALIBRATION getting better as labels
// accrue.
func (o ops) learning(ctx context.Context, in *mlLearningIn) (*mlLearningCurve, error) {
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "" {
		method = calibrate.Isotonic
	}
	if method != calibrate.Isotonic && method != calibrate.Platt {
		return nil, zip.Errorf(400, "%q is not a calibration method", in.Method)
	}
	steps := in.Steps
	if steps <= 0 {
		steps = 10
	}
	if steps < 2 || steps > 20 {
		return nil, zip.ErrBadRequest("a curve has between 2 and 20 steps")
	}
	sc, db, shape, mctx, done, err := o.measuring(ctx, "learning", in.Horizon, in.Rows)
	if err != nil {
		return nil, err
	}
	defer done()

	_, pol, err := stagePolicy(db, in.Stage)
	if err != nil {
		return nil, err
	}
	w, err := recorded(mctx, db, in.Horizon, shape, in.Rows)
	if err != nil {
		return nil, err
	}
	young, err := immature(mctx, db, in.Horizon)
	if err != nil {
		return nil, err
	}
	o.s.State.bill.Meter(sc.org, sc.project, "learning", measureCents(len(w.history)), sc.request, sc.clientIP)

	curve := &mlLearningCurve{
		Horizon: in.Horizon, Immature: young,
		Superseded: w.superseded, Truncated: w.truncated,
	}
	if len(w.history) < 2 {
		curve.Refusal = errNoHistory.Error()
		return curve, nil
	}
	obs := make([]quality.Observation, 0, len(w.history))
	for _, h := range w.history {
		obs = append(obs, h.Observation)
	}
	for _, s := range quality.Learning(obs, method, shape, steps, pol.Cost) {
		curve.Steps = append(curve.Steps, mlLearningStep{
			Rows: s.Rows, Judged: s.Judged, Productive: s.Productive, Held: s.Held,
			Train: wireMetrics(s.Train), Ahead: wireMetrics(s.Ahead), Refusal: s.Refusal,
		})
	}
	return curve, nil
}

// Replay answers what a candidate policy WOULD have decided, over the decisions
// this organisation already made.
//
// The RECORDED score is replayed and the model is not re-run, for two reasons
// that are the difference between a backtest that means something and one that
// does not. A score states how unusual an event was against the traffic at that
// moment, so recomputing it against today's aggregates answers a different
// question under the same name. And the detector has since learned from these
// very events, so re-scoring them through it is the purest form of knowing the
// future — and it produces a beautiful number.
//
// So the score is a fact and the candidate is a pure function of it, which is
// what makes the report DETERMINISTIC: no clock, no random source, no model
// state, and nothing that reaches the output is a map. Two runs over the same
// rows return the same digest, which is what lets a replay be evidence.
//
// It is written down. A replay is the justification attached to a threshold
// change, so it is a record with an identifier and not a number that scrolled
// past in a console.
func (o ops) replay(ctx context.Context, in *mlReplayIn) (*mlReplayReport, error) {
	stage := strings.ToLower(strings.TrimSpace(in.Stage))
	if !stages[stage] {
		return nil, zip.Errorf(400, "%q is not a lifecycle stage", in.Stage)
	}
	sc, db, shape, mctx, done, err := o.measuring(ctx, "replay", in.Horizon, in.Rows)
	if err != nil {
		return nil, err
	}
	defer done()

	current, _, err := currentPolicy(db, stage)
	if err != nil {
		return nil, err
	}
	cand := current
	cand.Stage = stage
	if len(in.Bands) > 0 {
		cand.Rungs = make([]policy.Rung, 0, len(in.Bands))
		for _, b := range in.Bands {
			a := strings.ToLower(strings.TrimSpace(b.Action))
			if !actions[a] {
				return nil, zip.Errorf(400, "%q is not an action", b.Action)
			}
			cand.Rungs = append(cand.Rungs, policy.Rung{At: b.At, Action: a})
		}
	}
	if f := strings.ToLower(strings.TrimSpace(in.Floor)); f != "" {
		if !actions[f] {
			return nil, zip.Errorf(400, "%q is not an action", in.Floor)
		}
		cand.Floor = f
	}
	if cand.Floor == "" {
		cand.Floor = ActionAllow
	}
	if in.Cost != nil {
		cand.Cost = policy.Cost{Miss: in.Cost.Miss, Alarm: in.Cost.Alarm}
	}
	// A candidate is replayed as a SEALED ladder or not at all: replaying one
	// that could never be put in force describes a policy nobody can adopt.
	// Seal's refusal travels to the caller as the report's own refusal, so the
	// answer says which property failed.
	sealed, sealErr := policy.Seal(cand)
	if sealErr == nil {
		cand = sealed
	}

	w, err := recorded(mctx, db, in.Horizon, shape, in.Rows)
	if err != nil {
		return nil, err
	}
	// BOUND, like every other read of the map. A backtest run under a calibration
	// the live plane refuses describes a world that cannot happen — every rung of
	// the candidate reachable, every Would action acted on — and it is then
	// written down as the durable justification for moving a threshold. Unbound,
	// the zero Reader makes every row take the candidate's floor, and Replay says
	// so in its own refusal.
	cal, _ := reader(db, shape)
	o.s.State.bill.Meter(sc.org, sc.project, "replay", measureCents(len(w.history)), sc.request, sc.clientIP)

	rep := quality.Replay(w.history, cal, cand, actionRank)

	id, at := newID("replay"), time.Now().UTC()
	// The AUTHOR is the person. A replay is the evidence attached to a threshold
	// change, and evidence whose author is "the org" names nobody.
	if err := putReplay(db, id, at, by(ctx, sc), rep); err != nil {
		return nil, err
	}
	return wireReplay(id, at, rep), nil
}

// ReplayReport reads a replay back. A threshold change is justified by one, so
// the justification has to still be there when somebody asks about the change.
func (o ops) replayReport(ctx context.Context, in *mlRef) (*mlReplayReport, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	rep, at, ok, err := getReplay(db, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("no such replay")
	}
	return wireReplay(strings.TrimSpace(in.ID), at, rep), nil
}

// ── rendering ───────────────────────────────────────────────────────────────

// actions is the closed set a band may name. It is derived from actionRank
// rather than restated, so a sixth action added there cannot be missing here.
var actions = func() map[string]bool {
	m := map[string]bool{}
	for _, a := range []string{ActionAllow, ActionChallenge, ActionReview, ActionRestrict, ActionBlock} {
		m[a] = true
	}
	return m
}()

// stagePolicy resolves the stage whose bands supply the operating point and the
// prices. An empty stage is not an error: it measures separation and reports
// nothing about money, which is the honest answer when nobody has stated a
// price.
func stagePolicy(db *sql.DB, want string) (string, policy.Policy, error) {
	stage := strings.ToLower(strings.TrimSpace(want))
	if stage == "" {
		return "", policy.Policy{}, nil
	}
	if !stages[stage] {
		return "", policy.Policy{}, zip.Errorf(400, "%q is not a lifecycle stage", want)
	}
	p, _, err := currentPolicy(db, stage)
	return stage, p, err
}

// reader binds this tenant's calibration to the scoring shape in force. It is
// the ONE door every measurement here reads a probability through, and it takes
// the shape as an argument the CALLER computed from the live scorer rather than
// reading it off the map — which is the whole point: a map cannot vouch for its
// own applicability.
//
// It returns the zero Reader and a sentence, never an error. Every op below
// treats "no calibration" and "the shape has moved" the same way — by not
// stating a probability — and both are facts about the tenant rather than
// failures of the request.
func reader(db *sql.DB, shape string) (calibrate.Reader, string) {
	m, _, _, ok, err := currentCalibration(db)
	switch {
	case err != nil:
		return calibrate.Reader{}, "the calibration could not be read, so no probability was applied"
	case !ok:
		return calibrate.Reader{}, "no calibration is fitted for this organisation, so a score is a rank and not a probability"
	}
	r, err := m.Under(shape)
	if err != nil {
		return calibrate.Reader{}, "the calibration on file was fitted under a different scoring shape, " +
			"so it refuses to answer and nothing here is stated as a probability: " + err.Error()
	}
	return r, ""
}

// scoreFor inverts a monotone calibration: the lowest score whose probability
// reaches p. Binary search over the unit interval rather than over the knots, so
// isotonic and platt take one implementation.
func scoreFor(cal calibrate.Reader, p float64) float64 {
	if !cal.Fitted() {
		return 0
	}
	lo, hi := 0.0, 1.0
	if cal.P(hi) < p {
		return 1.0000001
	}
	for range 40 {
		mid := (lo + hi) / 2
		if cal.P(mid) >= p {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

func wireBands(p policy.Policy) riskPolicyBands {
	out := riskPolicyBands{
		Stage: p.Stage, Version: p.Version, By: p.By, Reason: p.Reason,
		Floor: p.Floor, Shadow: p.Shadow, Digest: p.Digest,
		Cost:  riskCost{Miss: p.Cost.Miss, Alarm: p.Cost.Alarm},
		Bands: make([]riskBand, 0, len(p.Rungs)),
	}
	if !p.At.IsZero() {
		out.At = p.At.UTC().Format(time.RFC3339)
	}
	for _, r := range p.Rungs {
		out.Bands = append(out.Bands, riskBand{At: r.At, Action: r.Action})
	}
	return out
}

func wireMetrics(m quality.Metrics) mlMetrics {
	out := mlMetrics{
		Rows: m.Rows, Judged: m.Judged, Productive: m.Productive, Unproductive: m.Unproductive,
		Prevalence: m.Prevalence, ROC: m.ROC, PR: m.PR, Threshold: m.Threshold,
		Precision: m.Precision, Recall: m.Recall, F1: m.F1, Alarm: m.Alarm, Lift: m.Lift,
		CostNano: m.CostNano, BestNano: m.BestNano, BestThreshold: m.BestThreshold,
		Brier: m.Brier, Refusal: m.Refusal,
	}
	if m.Confusion != nil {
		out.Confusion = &mlConfusion{TP: m.Confusion.TP, FN: m.Confusion.FN, FP: m.Confusion.FP, TN: m.Confusion.TN}
	}
	return out
}

func wireReplay(id string, at time.Time, r quality.Report) *mlReplayReport {
	out := &mlReplayReport{
		ID: id, At: at.UTC().Format(time.RFC3339), Rows: r.Rows,
		Changed: r.Changed, Tightened: r.Tightened, Loosened: r.Loosened,
		Metrics: wireMetrics(r.Metrics), Explore: r.Explore,
		Policy: r.Policy, Calibration: r.Calibration, Digest: r.Digest, Refusal: r.Refusal,
		Was: make([]mlTally, 0, len(r.Was)), Would: make([]mlTally, 0, len(r.Would)),
	}
	if !r.From.IsZero() {
		out.From = r.From.UTC().Format(time.RFC3339)
	}
	if !r.To.IsZero() {
		out.To = r.To.UTC().Format(time.RFC3339)
	}
	for _, t := range r.Was {
		out.Was = append(out.Was, mlTally{Action: t.Action, Count: t.Count})
	}
	for _, t := range r.Would {
		out.Would = append(out.Would, mlTally{Action: t.Action, Count: t.Count})
	}
	for _, m := range r.Moved {
		out.Moved = append(out.Moved, mlMove{
			ID: m.ID, At: m.At.UTC().Format(time.RFC3339), Was: m.Was, Would: m.Would,
			Score: m.Score, Probability: m.Probability, Outcome: string(m.Disposition),
		})
	}
	return out
}

// view renders a calibration and, when it can still answer and there is history
// to check it against, the reliability report that says whether it holds.
//
// THE CHART IS BOUND LIKE EVERY OTHER READ. A map whose shape has moved refuses
// to state a probability, and a reliability report is that map applied to every
// row — so drawing one beside the refusal would put on a chart exactly what the
// prose has just said does not exist. Binding first makes that unrepresentable:
// an unbound reader draws nothing.
func view(m calibrate.Map, version int, at time.Time, shape string, horizon, young int, w evidence) *mlCalibrationView {
	out := &mlCalibrationView{
		Fitted: true, Version: version, Method: m.Method,
		Digest: m.Digest, Shape: m.Shape,
		Rows: m.Rows, Productive: m.Positive, Unproductive: m.Negative,
		Prevalence: m.Prevalence, Brier: m.Brier,
		Horizon: horizon, Immature: young,
		Superseded: w.superseded, Truncated: w.truncated,
	}
	if !at.IsZero() {
		out.At = at.UTC().Format(time.RFC3339)
	}
	read, err := m.Under(shape)
	out.Current = err == nil
	if err != nil {
		out.Refusal = "the scoring shape has moved since this fit — a feature changed, or a rule was written, " +
			"retired, reweighted or muted for every subject — so the map refuses to answer and every decision " +
			"reports its probability as absent"
		return out
	}
	for _, b := range calibrate.Reliability(samples(w.history), read, 10) {
		out.Reliability = append(out.Reliability, mlBin{
			From: b.From, To: b.To, Rows: b.Rows, Predicted: b.Predicted, Observed: b.Observed,
		})
	}
	return out
}

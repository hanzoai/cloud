package risk

// grade.go is the record plane and the arithmetic behind SCORING QUALITY: what a
// score means, why a decision went the way it did, and what the organisation
// asked for at that probability.
//
// FOUR RECORDS, ALL IN THE TENANT'S OWN FILE. A policy version, a calibration,
// the verdict attached to one decision and a replay report are each something an
// auditor, a regulator or a declined customer can ask about, so none of them
// lives in memory. They ride the same cek-encrypted per-org SQLite file the
// decision log already uses, which is what makes them survive the
// Recreate-at-one-replica rollout that drops every in-memory model.
//
// THAT FILE IS OPENED WITH cloud.OrgDB AND IS THEREFORE LOCAL. OrgDB is the
// encrypted open and nothing more; the ship-before-ack path is cloud.OrgStore
// configured WithDurable, which this package does not use — so a write here is
// as durable as the pod's volume and no more. Every record in this file has that
// property, the decision log included, and it predates this plane. Named here
// rather than claimed away: the two sibling durable planes (apps/research
// compose.go, apps/books books.go) ship after every commit and treat an unacked
// ship as an error, and moving the shelf onto OrgStore is the change that would
// give these records the same guarantee.
//
// NOTHING IS CACHED, DELIBERATELY. The decide path already reads this tenant's
// rules, lists and suppressions from its own file on every decision; reading the
// policy and the calibration the same way adds one idiom rather than a second.
// It also removes an entire class of bug — a cache with no invalidation path
// that keeps deciding under a policy an operator has already replaced — and it
// is why a rollout needs no rehydration step here: the record IS the state.
//
// THE ESCALATION RULE, ONCE. The evidence (rules and the model) proposes an
// action, and the policy proposes another from the calibrated probability. The
// decision takes the STRONGER of the two, capped at what the evidence can
// justify. A deny-list rule must not be overruled by a low probability — it is
// the tenant's explicit instruction — and a high probability must not be talked
// down by quiet evidence. But the probability is a pure function of the same
// score the model's own ceiling already capped, so an uncapped ladder would void
// that ceiling by arithmetic: see ceiling() below. Two authorities for one
// decision would otherwise be a question with two answers.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/calibrate"
	// The package is aml's `evaluate`; it is bound to `quality` here because
	// rule.go already declares a function called evaluate — the rule evaluator —
	// and one name cannot mean two things in one package.
	quality "github.com/luxfi/aml/pkg/evaluate"
	"github.com/luxfi/aml/pkg/policy"
	"github.com/luxfi/aml/pkg/reason"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/zap-proto/zip"
)

// qualitySchema is applied beside the decision plane's own schema, on the same
// open, in the same file. Separate constants rather than one, because these are
// a different concern with a different owner; the same file, because a verdict
// that could outlive or precede the decision it grades is not a record.
//
// NO TTL and no expiry anywhere here. These are records: a policy version says
// what was in force on the day someone was declined, and a verdict says why.
// Only the retention plane decides when a tenant's records go, per tenant.
const qualitySchema = `
CREATE TABLE IF NOT EXISTS policy (
	stage   TEXT NOT NULL,
	version INTEGER NOT NULL,
	at      TEXT NOT NULL,
	by      TEXT NOT NULL DEFAULT '',
	reason  TEXT NOT NULL DEFAULT '',
	digest  TEXT NOT NULL,
	body    TEXT NOT NULL,
	PRIMARY KEY (stage, version)
);

CREATE TABLE IF NOT EXISTS calibration (
	version INTEGER PRIMARY KEY,
	at      TEXT NOT NULL,
	by      TEXT NOT NULL DEFAULT '',
	shape   TEXT NOT NULL,
	horizon INTEGER NOT NULL DEFAULT 0,
	digest  TEXT NOT NULL,
	body    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS verdict (
	decision    TEXT PRIMARY KEY,
	at          TEXT NOT NULL,
	probability REAL,
	calibration TEXT NOT NULL DEFAULT '',
	policy      TEXT NOT NULL DEFAULT '',
	reasons     TEXT NOT NULL DEFAULT '[]',
	refusal     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS replay (
	id     TEXT PRIMARY KEY,
	at     TEXT NOT NULL,
	by     TEXT NOT NULL DEFAULT '',
	digest TEXT NOT NULL,
	body   TEXT NOT NULL
);
`

// topReasons is how many principal reasons a decision cites.
//
// Four, from Reg B: a creditor taking adverse action need not describe more than
// four reasons, and a list of every contributing feature is not an explanation
// anybody can act on. The model has nine coordinates and almost always moves on
// two or three of them.
const topReasons = 4

// verdict is the defensibility layer over one outcome: what the score meant,
// what moved it, and which ladder read it.
type verdict struct {
	// probability is the calibrated likelihood. Absent — a nil pointer, not a
	// zero — when no calibration is in force or the one on file was fitted under
	// a different scoring shape. A density reported as a probability is a number
	// that lies.
	probability *float64
	// reasons are the principal reasons, strongest first.
	reasons []reason.Reason
	// calibration and policy are the digests of what was applied, so the
	// decision can be replayed against exactly them.
	calibration string
	policy      string
	// action is what the policy asked for, before the escalation rule. Empty
	// when no ladder was reachable.
	action string
	// refusal names why there is no probability, when there is none. Silence and
	// a low probability render the same on a screen and are opposite facts.
	refusal string
}

// dispositions maps the decision log's verdict vocabulary onto the engine's.
//
// It is a CLOSED map with no default arm: a verdict this table does not name is
// unjudged, which is the honest reading of a word nobody has defined the meaning
// of. fraud, chargeback and abuse are all "the decision was right to be
// worried"; only legitimate is the other way; unknown says a human looked and
// could not tell, which is not evidence for either side.
var dispositions = map[string]replay.Disposition{
	"fraud":      replay.Productive,
	"chargeback": replay.Productive,
	"abuse":      replay.Productive,
	"legitimate": replay.Unproductive,
}

// scoringShape is the fingerprint everything a calibration depends on must agree
// on, and it is the training-serving skew control.
//
// A calibration maps THE DECISION SCORE to a probability, and that score is the
// combination of the model's own score with whatever this tenant's rules
// contributed. So the shape covers BOTH: the detector's geometry and feature
// inventory (anomaly.Store.Digest, which already folds the inventory in order
// with its neutral values), and this tenant's enabled rule set with the weights
// and actions each rule carries. Change either and the score distribution moves;
// the map fitted before the change then refuses to answer rather than quietly
// reporting probabilities for coordinates that no longer mean what they meant.
//
// A RULE-WIDE SUPPRESSION IS IN IT. Muting a rule is the day-to-day tuning knob
// and retiring one is the rare act, but combine() sums only the hits that were
// not suppressed — so a mute moves every score that rule touched EXACTLY as
// retiring it would. A control that noticed the rare change and not the common
// one would be a control nobody could rely on.
//
// LISTS AND SUBJECT-SCOPED SUPPRESSIONS ARE DELIBERATELY NOT IN IT, by the same
// argument in both directions. A list entry is data a rule reads, and a
// suppression naming one subject is a statement about that subject — neither
// changes what the scorer computes for the population, and both change
// constantly in normal operation. A plane whose calibration expired every time a
// stolen card was added to a deny list, or every time one merchant was excused,
// would never have a calibration at all. The boundary is stated rather than
// assumed: what moves the DISTRIBUTION invalidates, operational data about one
// row does not.
func scoringShape(db *sql.DB, model string) (string, error) {
	rules, err := loadRules(db)
	if err != nil {
		return "", err
	}
	sups, err := loadSuppressions(db)
	if err != nil {
		return "", err
	}
	return shapeOf(model, rules, sups, time.Now()), nil
}

// shapeOf folds the coordinate system from the values that produce a score.
//
// It takes the values rather than the store so the DECIDE path can fold the
// exact rules and suppressions it just scored under, instead of re-reading them
// and recording a shape a concurrent write may already have moved. scoringShape
// is the same fold over a fresh read, for the callers that hold neither.
func shapeOf(model string, rules []rule, sups []suppression, now time.Time) string {
	// Sorted by id so the fingerprint is a property of the SET and not of the
	// order rows happened to come back in.
	rules = append([]rule(nil), rules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	muted := make([]string, 0, len(sups))
	for _, s := range sups {
		// Expired mutes nothing — `until` in the past is the store's own expiry and
		// the decide path already reads it that way, so a shape that still counted
		// one would describe a scorer that no longer exists.
		if !s.Until.IsZero() && now.After(s.Until) {
			continue
		}
		// A mute that names a subject is about that subject. A mute that names a
		// rule and no subject silences it for the whole population, which is a move
		// of the distribution.
		if s.Rule == "" || s.Subject != "" {
			continue
		}
		muted = append(muted, s.Rule+":"+s.Kind)
	}
	sort.Strings(muted)

	h := sha256.New()
	fmt.Fprintf(h, "shape/v2|%s|", model)
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		fmt.Fprintf(h, "%s:%s:%g:%s|", r.ID, r.Stage, r.Weight, r.Action)
	}
	fmt.Fprint(h, "muted|")
	for _, m := range muted {
		fmt.Fprintf(h, "%s|", m)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// grade turns an outcome into a defensible one.
//
// It reads the tenant's own calibration and its ladder for this stage, maps the
// recorded score to a probability, ranks the reasons out of the model's exact
// counterfactual attribution and the rules that actually fired, and returns the
// action the policy asks for. It writes nothing: the caller records the verdict
// in the same place and at the same moment it records the decision, because a
// decision whose reasons landed separately could exist without them.
func grade(db *sql.DB, shape string, stage string, out outcome) verdict {
	v := verdict{reasons: reasonsOf(out)}

	cal, _, _, ok, err := currentCalibration(db)
	switch {
	case err != nil:
		v.refusal = "the calibration could not be read, so this score has no probability"
		return v
	case !ok:
		v.refusal = "no calibration is fitted for this organisation, so a score is a rank and not a probability"
		return v
	}
	read, err := cal.Under(shape)
	if err != nil {
		// The commonest cause by far, and the one worth naming: the model, the rule
		// set or a rule-wide mute moved since the fit.
		v.refusal = "the calibration on file was fitted under a different scoring shape: " + err.Error()
		return v
	}
	p := read.P(out.score)
	v.probability, v.calibration = &p, read.Digest()

	pol, ok, err := currentPolicy(db, stage)
	switch {
	case err != nil:
		v.refusal = "the policy could not be read, so no threshold was applied"
		return v
	case !ok:
		v.refusal = "no policy is set for this stage, so the probability was computed and nothing was decided from it"
		return v
	}
	v.policy, v.action = pol.Digest, pol.Action(p)
	return v
}

// escalate is the ONE place two authorities become one decision: the stronger of
// what the evidence asked for and what the policy asked for, with the policy
// held to what the evidence behind it can justify.
func escalate(evidence, policy string, out outcome) string {
	policy = atMost(policy, ceiling(out))
	if actionRank(policy) > actionRank(evidence) {
		return policy
	}
	return evidence
}

// ceiling is the strongest action the evidence behind one outcome can carry, or
// empty when it carries no ceiling at all.
//
// A decision the MODEL alone moved can put a transaction in front of a person
// and cannot decline one: an unexplainable refusal is not a decision anybody can
// defend to the customer or to a chargeback network, which is why decide caps
// the model's own hit at modelCeiling (decide.go, rule.go). The policy ladder is
// not a second, independent authority for that decline — it reads the calibrated
// probability, and the probability is a pure function of the SAME score — so
// applying the ladder over the cap would void the cap by arithmetic and the
// documented invariant would hold only until somebody set a band.
//
// A RULE the organisation wrote is different in kind. It is an explicit
// instruction with a name, a weight and a sentence a person can read, so a
// decline it reaches is explainable and no ceiling applies. A suppressed hit is
// not evidence: it contributed zero weight and it is not cited as a reason, so
// citing it here would let a mute both silence a rule and license a decline.
func ceiling(out outcome) string {
	for _, h := range out.hits {
		if h.Suppressed || h.Rule == modelRuleID {
			continue
		}
		return ""
	}
	return modelCeiling
}

// atMost caps an action at a ceiling. An empty ceiling caps nothing.
func atMost(action, ceiling string) string {
	if ceiling == "" || actionRank(action) <= actionRank(ceiling) {
		return action
	}
	return ceiling
}

// reasonsOf ranks the principal reasons behind one outcome.
//
// The model's contribution comes from its own per-feature counterfactual — move
// one coordinate to neutral, rescore on the same trees — so it is a measurement
// on the model that decided rather than a surrogate fitted afterwards. Each
// rule that fired contributes one reason naming itself.
//
// A SUPPRESSED HIT IS NOT A REASON. It is recorded, it is visible, and it
// contributed zero weight to the action — so citing it as a reason for the
// action would be false. That is the same argument the suppression path already
// makes for keeping the hit in the record.
func reasonsOf(out outcome) []reason.Reason {
	rs := reason.Rank(out.causes, 0)
	for _, h := range out.hits {
		if h.Suppressed || h.Rule == modelRuleID {
			continue
		}
		rs = append(rs, reason.OfRule(h.Rule, h.Name, h.Severity, h.Weight))
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Weight > rs[j].Weight })
	if len(rs) > topReasons {
		rs = rs[:topReasons]
	}
	return rs
}

// modelRuleID is the identifier the engine's own detector files its hit under.
// Its contribution is already expressed feature by feature in the causes, so
// citing the hit as well would count one piece of evidence twice.
const modelRuleID = "anomaly"

// ── the policy record ───────────────────────────────────────────────────────

// currentPolicy reads the ladder in force for one stage: the highest version.
func currentPolicy(db *sql.DB, stage string) (policy.Policy, bool, error) {
	var body string
	err := db.QueryRow(`SELECT body FROM policy WHERE stage = ? ORDER BY version DESC LIMIT 1`, stage).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Policy{}, false, nil
	}
	if err != nil {
		return policy.Policy{}, false, err
	}
	var p policy.Policy
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return policy.Policy{}, false, err
	}
	return p, true, nil
}

// putPolicy mints the next version of a stage's ladder.
//
// Nothing is ever updated in place. A policy change is a governance decision and
// the question "what was in force when this customer was declined" has to be a
// lookup rather than a reconstruction — so the row is an insert, the version is
// the successor of whatever is there, and the previous version stays exactly as
// it was.
func putPolicy(db *sql.DB, p policy.Policy) (policy.Policy, error) {
	sealed, err := policy.Seal(p)
	if err != nil {
		return policy.Policy{}, zip.ErrBadRequest(err.Error())
	}
	tx, err := db.Begin()
	if err != nil {
		return policy.Policy{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var last sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM policy WHERE stage = ?`, sealed.Stage).Scan(&last); err != nil {
		return policy.Policy{}, err
	}
	sealed.Version = int(last.Int64) + 1
	sealed.At = time.Now().UTC()
	// Digest is over what the ladder DECIDES, so it does not move with the
	// version or the timestamp. Re-sealing after stamping those would be a no-op
	// and is not done, which is what keeps two identical ladders comparable.

	body, err := json.Marshal(sealed)
	if err != nil {
		return policy.Policy{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO policy (stage, version, at, by, reason, digest, body) VALUES (?,?,?,?,?,?,?)`,
		sealed.Stage, sealed.Version, stamp(sealed.At), sealed.By, sealed.Reason, sealed.Digest, string(body),
	); err != nil {
		return policy.Policy{}, err
	}
	if err := tx.Commit(); err != nil {
		return policy.Policy{}, err
	}
	return sealed, nil
}

// policyVersions is the audit trail for one stage, newest first.
func policyVersions(db *sql.DB, stage string, limit int) ([]policy.Policy, error) {
	rows, err := db.Query(
		`SELECT body FROM policy WHERE stage = ? ORDER BY version DESC LIMIT ?`, stage, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []policy.Policy
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var p policy.Policy
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// policyStages lists the stages this tenant has ever set a ladder for.
func policyStages(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT stage FROM policy ORDER BY stage`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ── the calibration record ──────────────────────────────────────────────────

// currentCalibration reads the map in force: the highest version. One
// calibration per tenant rather than one per stage — the map turns THIS model's
// score into a probability and the model is per tenant, so a per-stage split
// would divide the same evidence into thinner samples of the same relationship.
func currentCalibration(db *sql.DB) (calibrate.Map, int, time.Time, bool, error) {
	var version, horizon int
	var at, body string
	err := db.QueryRow(
		`SELECT version, at, horizon, body FROM calibration ORDER BY version DESC LIMIT 1`,
	).Scan(&version, &at, &horizon, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return calibrate.Map{}, 0, time.Time{}, false, nil
	}
	if err != nil {
		return calibrate.Map{}, 0, time.Time{}, false, err
	}
	var m calibrate.Map
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return calibrate.Map{}, 0, time.Time{}, false, err
	}
	return m, version, unstamp(at), true, nil
}

// putCalibration records a fit as the next version.
func putCalibration(db *sql.DB, m calibrate.Map, by string, horizon int) (int, time.Time, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return 0, time.Time{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var last sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM calibration`).Scan(&last); err != nil {
		return 0, time.Time{}, err
	}
	version := int(last.Int64) + 1
	at := time.Now().UTC()
	if _, err := tx.Exec(
		`INSERT INTO calibration (version, at, by, shape, horizon, digest, body) VALUES (?,?,?,?,?,?,?)`,
		version, stamp(at), by, m.Shape, horizon, m.Digest, string(body),
	); err != nil {
		return 0, time.Time{}, err
	}
	return version, at, tx.Commit()
}

// ── the verdict record ──────────────────────────────────────────────────────

// getVerdict reads one decision's grading back, so a repeat of an idempotent
// decide returns the same reasons rather than a decision with none.
func getVerdict(db *sql.DB, id string) (verdict, bool, error) {
	var p sql.NullFloat64
	var cal, pol, reasons, refusal string
	err := db.QueryRow(
		`SELECT probability, calibration, policy, reasons, refusal FROM verdict WHERE decision = ?`, id,
	).Scan(&p, &cal, &pol, &reasons, &refusal)
	if errors.Is(err, sql.ErrNoRows) {
		return verdict{}, false, nil
	}
	if err != nil {
		return verdict{}, false, err
	}
	v := verdict{calibration: cal, policy: pol, refusal: refusal}
	if p.Valid {
		val := p.Float64
		v.probability = &val
	}
	if err := json.Unmarshal([]byte(reasons), &v.reasons); err != nil {
		return verdict{}, false, err
	}
	return v, true, nil
}

// graded is how every READ path asks for a decision's grading: the verdict, or a
// verdict that SAYS there is none.
//
// The not-found used to be discarded with `_`, which made an ungraded decision
// indistinguishable from a graded one with nothing to say — so a block could be
// served on the dispute-packet surface with no probability, no principal reasons
// and no refusal. The rows are written in one transaction now, so this should be
// unreachable; a record plane that answered "nothing to say" for a state it
// believes impossible would be hiding exactly the corruption worth knowing about.
func graded(db *sql.DB, id string) (verdict, error) {
	v, ok, err := getVerdict(db, id)
	if err != nil {
		return verdict{}, err
	}
	if !ok {
		return verdict{refusal: "this decision has no recorded grading, so there is no probability, " +
			"no principal reason and nothing this plane can defend it with"}, nil
	}
	return v, nil
}

// ── the replay record ───────────────────────────────────────────────────────

func putReplay(db *sql.DB, id string, at time.Time, by string, rep quality.Report) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO replay (id, at, by, digest, body) VALUES (?,?,?,?,?)`,
		id, stamp(at), by, rep.Digest, string(body))
	return err
}

func getReplay(db *sql.DB, id string) (quality.Report, time.Time, bool, error) {
	var at, body string
	err := db.QueryRow(`SELECT at, body FROM replay WHERE id = ?`, id).Scan(&at, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return quality.Report{}, time.Time{}, false, nil
	}
	if err != nil {
		return quality.Report{}, time.Time{}, false, err
	}
	var rep quality.Report
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		return quality.Report{}, time.Time{}, false, err
	}
	return rep, unstamp(at), true, nil
}

// ── reading the history back ────────────────────────────────────────────────

// maxHistory bounds every read of the decision log for measurement.
//
// The same bound the search sandbox uses, and for the same reason: this runs
// in-request against a single-writer file, so an unbounded scan is a tenant
// holding the only connection its own decisions need.
const maxHistory = 50_000

// evidence is one bounded read of the decision log: the rows a measurement was
// computed from, and the two facts that say what the bounds left out. Both are
// reported on every answer, because a measurement is only as good as the sample
// it saw and a sample silently cut reads as a complete one.
type evidence struct {
	// history is the rows, OLDEST FIRST, ready to measure over.
	history []quality.Recorded
	// truncated says the bound cut the read: there are older mature decisions
	// under this shape that no number here was computed from.
	truncated bool
	// superseded counts the mature judged decisions excluded because they were
	// scored under a DIFFERENT coordinate system. It is the evidence a refit
	// cannot honestly use, and the number that says why a fit went thin after a
	// governed change.
	superseded int
}

// recorded reads this tenant's decision log as measurable observations, under
// ONE scoring shape, restricted to decisions old enough for their outcome to
// have arrived and bounded to the most recent of them.
//
// THE MATURITY HORIZON IS THE WHOLE OF WHY THIS TAKES A PARAMETER. Analysts
// judge within hours; a card network dispute lands 30 to 120 days after the
// transaction it disputes. Measure over everything and the recent tail is
// enriched with analyst clearances and stripped of the chargebacks that have not
// arrived yet, so the prevalence is understated and every threshold derived from
// it sits too high. Excluding decisions younger than the horizon is what makes
// the judged set representative rather than merely recent.
//
// THE SHAPE IS THE OTHER FILTER AND IT IS NOT OPTIONAL. A score is a coordinate.
// Rows scored before a rule was written, retired, reweighted or muted are
// coordinates in a system that no longer exists, and a fit taken over them and
// stamped with today's shape is precisely the training–serving skew the shape
// gate exists to refuse — arrived at from the inside, by the remedy the gate's
// own message recommends. Filtering here means a refit after a governed change
// finds thin evidence and SAYS SO, instead of re-blessing history it cannot read.
//
// THE BOUND TAKES THE MOST RECENT ROWS. Ascending plus LIMIT takes the oldest,
// which past the bound freezes every fit, every measurement and every replay on
// the dawn of the tenant's log forever. The read is descending and the slice is
// reversed once, so what comes back is the LATEST maxHistory decisions in time
// order — and truncated says when there were more.
//
// The ordering is on the second-truncated timestamp and then the id, which is a
// TOTAL order: the stored form is RFC 3339 with a variable-length fraction, so a
// plain string comparison sorts "…:00.5Z" before "…:00Z". At the day scale a
// horizon works on that is immaterial, but a learning curve that split its
// history on it would not be reproducible, and reproducibility is the property
// being sold.
func recorded(ctx context.Context, db *sql.DB, horizon int, shape string, limit int) (evidence, error) {
	if limit <= 0 || limit > maxHistory {
		limit = maxHistory
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -horizon).Format("2006-01-02T15:04:05")

	// The scan is its own scope, and that is load-bearing rather than tidy: the
	// org file is opened with ONE connection, so an open *sql.Rows holds the only
	// one there is. Reading limit+1 and stopping early leaves it open, and the
	// count below would then wait for a connection this function is itself
	// holding — a deadlock, not an error, bounded by nothing.
	scan := func() ([]quality.Recorded, bool, error) {
		// limit+1 answers "was there more" exactly, and for free — a COUNT over the
		// same predicate is a second scan of the same rows to learn one bit.
		rows, err := db.QueryContext(ctx,
			`SELECT id, at, action, score, label FROM decision
			 WHERE substr(at,1,19) <= ? AND shape = ?
			 ORDER BY substr(at,1,19) DESC, id DESC LIMIT ?`, cutoff, shape, limit+1)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = rows.Close() }()

		out := make([]quality.Recorded, 0, limit)
		truncated := false
		for rows.Next() {
			if len(out) == limit {
				truncated = true
				break
			}
			var id, at, action, label string
			var score float64
			if err := rows.Scan(&id, &at, &action, &score, &label); err != nil {
				return nil, false, err
			}
			out = append(out, quality.Recorded{
				Observation: quality.Observation{
					ID: id, At: unstamp(at), Score: score, Action: action,
					Disposition: dispositions[strings.ToLower(label)],
				},
				// Every judgement this plane holds today came from a person reviewing
				// a decision. The below-the-line sample arm the engine already selects
				// is not yet wired to a label source, so the honest exploration share
				// is zero and Replay reports it as such rather than assuming it away.
				Source: "review",
			})
		}
		return out, truncated, rows.Err()
	}

	out, truncated, err := scan()
	if err != nil {
		return evidence{}, err
	}
	// Back into time order: every consumer of a history is temporal — the
	// learning curve splits it, the replay walks it, the report bounds it.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	w := evidence{history: out, truncated: truncated}

	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM decision WHERE label != '' AND substr(at,1,19) <= ? AND shape != ?`,
		cutoff, shape).Scan(&w.superseded); err != nil {
		return evidence{}, err
	}
	return w, nil
}

// immature counts the labelled decisions the horizon excluded. It is reported
// beside every fit and every measurement, because a horizon that quietly drops
// most of the evidence is the difference between a thin answer and a wrong one.
func immature(ctx context.Context, db *sql.DB, horizon int) (int, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -horizon).Format("2006-01-02T15:04:05")
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM decision WHERE label != '' AND substr(at,1,19) > ?`, cutoff).Scan(&n)
	return n, err
}

// samples reduces recorded observations to what a calibration fit reads.
// Unjudged rows are dropped rather than counted as unproductive: a fit over
// "unknown means fine" is a fit against the incumbent policy, not against the
// world.
func samples(history []quality.Recorded) []calibrate.Sample {
	out := make([]calibrate.Sample, 0, len(history))
	for _, h := range history {
		switch h.Disposition {
		case replay.Productive:
			out = append(out, calibrate.Sample{Score: h.Score, Productive: true})
		case replay.Unproductive:
			out = append(out, calibrate.Sample{Score: h.Score})
		}
	}
	return out
}

// unacted is the share of the judged evidence that came from decisions the
// policy LET THROUGH.
//
// It is the honest limit on every number measured from this log. A blocked
// decision has no outcome — the counterfactual is unobserved and no arithmetic
// recovers it — so a judged set made entirely of decisions the incumbent chose
// to act on measures agreement with the incumbent rather than accuracy. Near
// zero here means exactly that, and it is reported rather than assumed away.
func unacted(history []quality.Recorded) (float64, int) {
	var judged, allowed int
	for _, h := range history {
		if h.Disposition != replay.Productive && h.Disposition != replay.Unproductive {
			continue
		}
		judged++
		if h.Action == ActionAllow {
			allowed++
		}
	}
	if judged == 0 {
		return 0, 0
	}
	return round4(float64(allowed) / float64(judged)), judged
}

package risk

// grade.go is the record plane and the arithmetic behind SCORING QUALITY: what a
// score means, why a decision went the way it did, and what the organisation
// asked for at that probability.
//
// FOUR RECORDS, ALL DURABLE IN THE TENANT'S OWN FILE. A policy version, a
// calibration, the verdict attached to one decision and a replay report are each
// something an auditor, a regulator or a declined customer can ask about, so
// none of them lives in memory. They ride the same cek-encrypted, HA-shipped
// per-org SQLite file the decision log already uses (cloud.OrgDB → internal/org),
// which is also what makes them survive the Recreate-at-one-replica rollout that
// drops every in-memory model.
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
// decision takes the STRONGER of the two. A deny-list rule must not be overruled
// by a low probability — it is the tenant's explicit instruction — and a high
// probability must not be talked down by quiet evidence. Two authorities for one
// decision would otherwise be a question with two answers.

import (
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
// LISTS ARE DELIBERATELY NOT IN IT. A list entry is data a rule reads, not a
// change to what the rule computes, and lists change constantly in normal
// operation — a plane whose calibration expired every time a stolen card was
// added to a deny list would never have a calibration at all. The boundary is
// stated rather than assumed: governed changes invalidate, operational data does
// not.
func scoringShape(db *sql.DB, model string) (string, error) {
	rules, err := loadRules(db)
	if err != nil {
		return "", err
	}
	// Sorted by id so the fingerprint is a property of the rule SET and not of
	// the order rows happened to come back in.
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	h := sha256.New()
	fmt.Fprintf(h, "shape/v1|%s|", model)
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		fmt.Fprintf(h, "%s:%s:%g:%s|", r.ID, r.Stage, r.Weight, r.Action)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
	p, err := cal.P(out.score, shape)
	if err != nil {
		// The commonest cause by far, and the one worth naming: the model or the
		// rule set moved since the fit.
		v.refusal = "the calibration on file was fitted under a different scoring shape: " + err.Error()
		return v
	}
	v.probability, v.calibration = &p, cal.Digest

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
// what the evidence asked for and what the policy asked for.
func escalate(evidence, policy string) string {
	if actionRank(policy) > actionRank(evidence) {
		return policy
	}
	return evidence
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

// putVerdict records the grading of one decision. Called inside the decide path
// immediately after the decision row lands, so a decision and the reasons for it
// arrive together or not at all.
func putVerdict(db *sql.DB, id string, at time.Time, v verdict) error {
	body, err := json.Marshal(v.reasons)
	if err != nil {
		return err
	}
	var p any
	if v.probability != nil {
		p = *v.probability
	}
	_, err = db.Exec(
		`INSERT OR REPLACE INTO verdict (decision, at, probability, calibration, policy, reasons, refusal)
		 VALUES (?,?,?,?,?,?,?)`,
		id, stamp(at), p, v.calibration, v.policy, string(body), v.refusal)
	return err
}

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

// recorded reads this tenant's decision log as measurable observations, oldest
// first, restricted to decisions old enough for their outcome to have arrived.
//
// THE MATURITY HORIZON IS THE WHOLE OF WHY THIS TAKES A PARAMETER. Analysts
// judge within hours; a card network dispute lands 30 to 120 days after the
// transaction it disputes. Measure over everything and the recent tail is
// enriched with analyst clearances and stripped of the chargebacks that have not
// arrived yet, so the prevalence is understated and every threshold derived from
// it sits too high. Excluding decisions younger than the horizon is what makes
// the judged set representative rather than merely recent.
//
// The ordering is on the second-truncated timestamp and then the id, which is a
// TOTAL order: the stored form is RFC 3339 with a variable-length fraction, so a
// plain string comparison sorts "…:00.5Z" before "…:00Z". At the day scale a
// horizon works on that is immaterial, but a learning curve that split its
// history on it would not be reproducible, and reproducibility is the property
// being sold.
func recorded(db *sql.DB, horizon int, limit int) ([]quality.Recorded, error) {
	if limit <= 0 || limit > maxHistory {
		limit = maxHistory
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -horizon).Format("2006-01-02T15:04:05")
	rows, err := db.Query(
		`SELECT id, at, action, score, label FROM decision
		 WHERE substr(at,1,19) <= ?
		 ORDER BY substr(at,1,19) ASC, id ASC LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []quality.Recorded
	for rows.Next() {
		var id, at, action, label string
		var score float64
		if err := rows.Scan(&id, &at, &action, &score, &label); err != nil {
			return nil, err
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
	return out, rows.Err()
}

// immature counts the labelled decisions the horizon excluded. It is reported
// beside every fit and every measurement, because a horizon that quietly drops
// most of the evidence is the difference between a thin answer and a wrong one.
func immature(db *sql.DB, horizon int) (int, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -horizon).Format("2006-01-02T15:04:05")
	var n int
	err := db.QueryRow(
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

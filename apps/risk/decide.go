package risk

// decide.go is the hot path: one call, one decision, an answer that says what to
// do and why.
//
// ORDER, and why it is this order:
//
//  1. RECORD the observation into the in-memory velocity rings. Everything after
//     reads them, and the numbers quoted in the decision must be the numbers an
//     investigator sees when they look at the subject.
//  2. CLASSIFY agency — agent, human, bot or unknown — from facts only we hold.
//  3. SCORE the model. It learns online and its evidence is capped at review.
//  4. EVALUATE the rules over the recorded aggregates plus the model's score.
//  5. SUPPRESS. A suppressed hit is RECORDED with suppressed=true, never dropped:
//     silence must never read as a clean result.
//  6. COMBINE into one action, and never let statistical evidence exceed the
//     ceiling.
//
// NOTHING HERE READS THE WAREHOUSE. The rings are in memory, the rules and lists
// are the tenant's own SQLite file, the model is in process. That is deliberate:
// the warehouse is one pod and this call sits inside a card processor's
// authorization window.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

// Agency is what kind of actor this is. It is the differentiator: we run the
// agents, so we can ask the org's OWN registry whether the agent a decision
// names is one it registered — a fact nobody who does not run agents holds. See
// agency.go, which is where it is derived and where the registry is asked.
const (
	// AgencyAgent is an agent the org registered: the reference on the
	// observation RESOLVED in this org's own agent registry.
	AgencyAgent = "agent"
	// AgencyHuman is a live session bound to a validated user, naming no agent.
	AgencyHuman = "human"
	// AgencyBot is undeclared automation: something named an agent reference
	// this org's registry does not know. A claim the registry can disprove is
	// the strongest signal available here.
	AgencyBot = "bot"
	// AgencyUnknown names nothing to check, or nothing checkable. Scored
	// normally — an honest "we cannot tell" is worth more than a guess in either
	// direction.
	AgencyUnknown = "unknown"
)

// Refusals. A refusal is COUNTED and NAMED, never silent, because a control that
// declined to answer and a control that found nothing are the same bytes on the
// wire and opposite facts about the system.
const (
	// RefusalWarming means the model has not learned enough of this tenant's
	// behaviour for its scores to mean anything. Rules still ran.
	RefusalWarming = "warming"
	// RefusalDisarmed means this tenant HAD learned state and this process does
	// not have it: the snapshot was unreadable, or the engine refused it. It is
	// warming's opposite, not its synonym — warming is a control coming up, and
	// this is a control that is off. Named separately because reporting a
	// disarmed model as "warming" is exactly how one stays off unnoticed.
	RefusalDisarmed = "disarmed"
	// RefusalUnverified means the actor's claimed agency could not be checked
	// against the org's registry, because the registry could not be reached. The
	// classification on this decision is a gap, not a verdict.
	RefusalUnverified = "unverified"
	// RefusalUnidentified means the observation named no subject the aggregates
	// can be keyed on.
	RefusalUnidentified = "unidentified"
	// RefusalShadow means the tenant is in shadow: everything was computed and
	// recorded, and nothing acts.
	RefusalShadow = "shadow"
)

// hit is one piece of evidence.
type hit struct {
	// Rule is the identifier of the rule or model that produced this evidence.
	Rule string `json:"rule"`
	// Name is the human-readable detection name.
	Name string `json:"name"`
	// Action is what this evidence alone asks for.
	Action string `json:"action"`
	// Weight is how much this evidence contributes, in [0,1].
	Weight float64 `json:"weight"`
	// Severity is the reviewer-facing grading.
	Severity string `json:"severity,omitempty"`
	// Suppressed marks evidence a tenant suppression muted. It still contributes
	// nothing to the action and is still recorded, because a muted control that
	// leaves no trace is indistinguishable from one that was never running.
	Suppressed bool `json:"suppressed,omitempty"`
}

// observation is everything the decide path was given plus everything it derived
// before scoring. It is the value the rules read and the value the record plane
// stores, so what fired and what was stored cannot disagree.
type observation struct {
	id        string
	at        time.Time
	stage     string
	kind      string
	subject   string
	agency    string
	amount    int64
	currency  string
	direction string
	signals   map[string]string
	agent     string
	session   string
	// refusal is what was already short BEFORE scoring — today, an agency the
	// registry could not confirm. It rides the observation rather than being a
	// parameter of its own because it is derived from the observation, in the
	// same call that derives agency.
	refusal string
}

// outcome is the decision, before it is rendered onto the wire.
type outcome struct {
	id      string
	action  string
	score   float64
	agency  string
	hits    []hit
	causes  []types.Cause
	shadow  bool
	refusal string
}

// axisOf maps a velocity axis to the observation's value on that axis. It is a
// closed map because a rule may only name an axis this returns something for,
// and admitTerm holds it to the same set.
func (o observation) axisOf(axis string) string {
	switch axis {
	case "account":
		return o.subject
	case "device":
		return o.signals["device"]
	case "ip":
		return o.signals["ip"]
	case "email":
		return o.signals["email"]
	case "bin":
		return o.signals["bin"]
	case "pair":
		if cp := o.signals["counterparty"]; cp != "" {
			return o.subject + "\x1f" + cp
		}
	}
	return ""
}

// record writes the observation onto every ring it has a value for. The rings
// are keyed {OrgID, Kind, Value} with the tenant leading, so two tenants naming
// the same device or the same address never share a counter.
func record(vel *velocity.Store, t Tenant, o observation) {
	usd := nanoUSD(o.amount)
	for axis := range velocityAxes {
		v := o.axisOf(axis)
		if v == "" {
			continue
		}
		vel.Record(velocity.Key{OrgID: t.String(), Kind: axis, Value: v}, o.at, usd, structuringThreshold)
	}
}

// structuringThreshold is the reporting threshold the "just under" counters are
// measured against — the standard USD 10,000 figure. It is a constant because a
// per-tenant threshold is a per-tenant policy and this is the statutory one.
const structuringThreshold = 10_000

// nanoUSD converts the wire's integer nano-units to the float the aggregates and
// the model read. Money is carried as an integer on the wire so no rounding
// happens between the caller and the ledger; the aggregate is a statistic and a
// float is the right shape for it.
func nanoUSD(nano int64) float64 { return float64(nano) / 1e9 }

// observe builds the fact set the rules read. Every velocity number the closed
// vocabulary can name is materialised here, once, so a rule set of any size
// reads the rings a bounded number of times.
func observe(vel *velocity.Store, t Tenant, o observation, modelScore float64, warming bool) factSet {
	f := factSet{
		scalar: map[string]string{
			"stage":            o.stage,
			"subject.kind":     o.kind,
			"subject.id":       o.subject,
			"agency":           o.agency,
			"amount.currency":  o.currency,
			"amount.direction": o.direction,
			"actor.agent":      o.agent,
			"actor.session":    o.session,
			"model.warming":    strconv.FormatBool(warming),
		},
		number: map[string]float64{
			"amount.nano": float64(o.amount),
			"model.score": modelScore,
		},
	}
	for k, v := range o.signals {
		f.scalar["signal."+strings.ToLower(k)] = v
	}
	for axis := range velocityAxes {
		v := o.axisOf(axis)
		if v == "" {
			continue
		}
		for _, obs := range vel.Observe(velocity.Key{OrgID: t.String(), Kind: axis, Value: v}) {
			p := "velocity." + axis + "." + obs.Window + "."
			f.number[p+"count"] = float64(obs.Count)
			f.number[p+"sum"] = obs.Sum
			f.number[p+"near"] = float64(obs.Near)
			f.number[p+"days"] = float64(obs.Days)
		}
	}
	return f
}

// bench is everything a decision is made AGAINST: the tenant's two in-memory
// planes, its governance, and how to grade the model's own refusal.
//
// It is a VALUE, so decide can be exercised without a store — which is what
// makes the ordering above testable rather than assertable — and it is ONE
// value, so the next thing a decision must read arrives as a named field rather
// than as a tenth positional argument nobody reads at the call site.
type bench struct {
	// t is the tenant. It leads every velocity key and every model entity, so it
	// is what makes two tenants' counters disjoint.
	t Tenant
	// vel and model are THIS tenant's own aggregates and forest. Nothing here is
	// shared with another tenant; see bound.go.
	vel   *velocity.Store
	model *anomaly.Store
	// rules is this tenant's rule set, already loaded.
	rules []rule
	// lists answers whether a value is in one of this tenant's named lists.
	lists func(name, value string) bool
	// mute answers whether a suppression covers this hit. A muted hit is still
	// recorded; see step 5.
	mute func(h hit, o observation) bool
	// shadow is the tenant observing rather than acting.
	shadow bool
	// grade turns the model's own refusal into the word that is true of it —
	// `warming` for a control coming up, `disarmed` for one that is off. Nil
	// grades nothing, which is what a test without a store wants.
	grade func(reason string) string
}

// gradeOf applies the bench's grader, if it has one.
func (b bench) gradeOf(reason string) string {
	if b.grade == nil {
		return reason
	}
	return b.grade(reason)
}

// decide is the whole path.
func decide(ctx context.Context, b bench, o observation) outcome {
	_ = ctx

	out := outcome{id: o.id, shadow: b.shadow, agency: o.agency, action: ActionAllow, refusal: o.refusal}
	if strings.TrimSpace(o.subject) == "" {
		out.refusal = RefusalUnidentified
		return out
	}

	// 1. Record first: everything after reads these rings, and the numbers in the
	// decision must be the ones an investigator sees on the subject.
	record(b.vel, b.t, o)

	// 2. Score the model. Assess LEARNS; the tenant's own traffic is its training
	// set and there is no separate training pass.
	tx := types.Transaction{
		ID:                o.id,
		OrgID:             b.t.String(),
		UserID:            o.subject,
		AccountID:         o.subject,
		Currency:          o.currency,
		Direction:         o.direction,
		IPAddress:         o.signals["ip"],
		DeviceFingerprint: o.signals["device"],
		Counterparty:      o.signals["counterparty"],
		Timestamp:         o.at,
		USD:               nanoUSD(o.amount),
	}
	entity := types.Entity{ID: o.subject, OrgID: b.t.String()}
	assessment := b.model.Inspect(tx, entity)
	var modelHit *hit
	if mh, ok := b.model.Assess(tx, entity); ok {
		action := mh.Rule.Action
		if actionRank(action) > actionRank(modelCeiling) {
			action = modelCeiling
		}
		modelHit = &hit{
			Rule: mh.Rule.ID, Name: mh.Rule.Name, Action: action,
			Weight: mh.Rule.Weight, Severity: mh.Rule.Severity,
		}
		out.causes = mh.Causes
	}
	// GRADED HERE, where the model's own reason is produced and still the only
	// thing being named. Grading the merged word instead would let any
	// higher-ranked reason hide a model that is off.
	if !assessment.Scored && assessment.Reason != "" {
		out.refusal = worse(out.refusal, b.gradeOf(assessment.Reason))
	}

	// 3. Rules, over the recorded aggregates plus the model's score.
	f := observe(b.vel, b.t, o, assessment.Score, !assessment.Scored)
	f.lists = b.lists
	hits := evaluate(b.rules, o.stage, f)
	if modelHit != nil {
		hits = append(hits, *modelHit)
	}

	// 4. Suppression. A suppressed hit is kept, marked and given zero weight —
	// it appears in the record and contributes nothing to the action.
	scoring := make([]hit, 0, len(hits))
	for i := range hits {
		if b.mute != nil && b.mute(hits[i], o) {
			hits[i].Suppressed = true
			continue
		}
		scoring = append(scoring, hits[i])
	}

	// 5. Combine. Sorting again puts the model's hit in its place among the rest.
	sort.SliceStable(hits, func(i, j int) bool {
		if a, b := actionRank(hits[i].Action), actionRank(hits[j].Action); a != b {
			return a > b
		}
		return hits[i].Weight > hits[j].Weight
	})
	score, action := combine(scoring)
	out.hits, out.score = hits, round4(score)

	// 6. Shadow. Everything above ran, everything is recorded, nothing acts.
	if b.shadow {
		out.action = ActionAllow
		out.refusal = worse(out.refusal, RefusalShadow)
		return out
	}
	out.action = action
	return out
}

// worse keeps the refusal a reader most needs to see.
//
// A decision can be short of more than one thing at once and the wire carries ONE
// word, so the word is chosen by a stated precedence rather than by whichever
// line happened to run last.
//
// THE ORDER IS "WHAT WILL NOT FIX ITSELF", most consequential first:
//
//	unidentified  the observation named nobody, so there was nothing to key on.
//	disarmed      a control that WAS on is off. Nothing this tenant does next
//	              turns it back on; an operator has to.
//	unverified    a check that was supposed to happen did not, because another
//	              process could not answer. Also nobody's own traffic to fix.
//	unusable      the engine says its own output is not usable yet.
//	warming       the model is coming up. ORDINARY — it is the state of every new
//	              tenant, and the tenant's own next requests resolve it.
//	shadow        the tenant chose to observe. Not a shortfall at all, and also
//	              published as its own boolean.
//
// warming ranking BELOW unverified is deliberate and was a defect the other way
// round: warming is the common case, so letting it win meant a registry outage
// was reported as a model that is merely young, on the majority of decisions.
//
// A refusal this table does not name still outranks silence: an unnamed reason
// is one the engine added and this file has not met, and dropping it would be
// exactly the silent gap the whole vocabulary exists to prevent.
func worse(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case rank(b) > rank(a):
		return b
	default:
		return a
	}
}

func rank(refusal string) int {
	if r, named := refusalRank[refusal]; named {
		return r
	}
	return refusalRank[anomaly.ReasonUnusable]
}

var refusalRank = map[string]int{
	RefusalShadow:          1,
	RefusalWarming:         2,
	anomaly.ReasonUnusable: 3,
	RefusalUnverified:      4,
	RefusalDisarmed:        5,
	RefusalUnidentified:    6,
}

// round4 trims a score to four places. A score is a judgement, not a
// measurement, and rendering seventeen digits of float noise invites a client to
// compare two scores that differ in the fifteenth.
func round4(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*1e4) / 1e4
}

// newID mints a decision identifier. Random rather than sequential: a sequential
// id over a shared surface is a volume oracle — a tenant can count another
// tenant's decisions by watching its own ids skip.
func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

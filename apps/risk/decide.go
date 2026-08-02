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
// agents, so we can tell a declared, credentialed, metered agent from an
// anonymous script, and nobody who does not run agents can compute it.
const (
	// AgencyAgent is a declared agent: it named an agent reference, it
	// authenticated with a principal-bearing credential, and its traffic is
	// metered on this org's own ledger.
	AgencyAgent = "agent"
	// AgencyHuman is a browser session bound to a validated user.
	AgencyHuman = "human"
	// AgencyBot is undeclared automation: no agent reference, and either no
	// credential or a publishable key, which by construction identifies a tenant
	// and authenticates nobody.
	AgencyBot = "bot"
	// AgencyUnknown is credentialed but undeclared. Scored normally — an honest
	// "we cannot tell" is worth more than a guess in either direction.
	AgencyUnknown = "unknown"
)

// Refusals. A refusal is COUNTED and NAMED, never silent, because a control that
// declined to answer and a control that found nothing are the same bytes on the
// wire and opposite facts about the system.
const (
	// RefusalWarming means the model has not learned enough of this tenant's
	// behaviour for its scores to mean anything. Rules still ran.
	RefusalWarming = "warming"
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

// classify derives agency from four facts cloud already holds, and from NO
// user-agent string. A user agent is a caller-supplied claim, so sniffing it
// classifies whoever is honest and misses whoever is not.
//
//	credential  a publishable key resolves an ORG and no principal, so it cannot
//	            be an agent; a secret key or a bearer resolves a principal.
//	declared    does actor.agentRef resolve in THIS org's agent registry?
//	session     is there a live agent session for it?
//	metered     does this account have priced rows on the org's own ledger?
//
// declared AND credentialed => agent, and the org's agent policy applies.
// undeclared AND anonymous  => bot, and the anonymous lane's bounds apply.
// Anything else is unknown and is scored normally.
func classify(credentialed, publishable, declared, humanSession bool) string {
	switch {
	case declared && credentialed && !publishable:
		return AgencyAgent
	case !declared && (!credentialed || publishable):
		return AgencyBot
	case humanSession && !declared:
		return AgencyHuman
	default:
		return AgencyUnknown
	}
}

// decide is the whole path. It takes the tenant's rules, lists and suppressions
// as values so it can be exercised without a store, which is what makes the
// ordering above testable rather than assertable.
func decide(
	ctx context.Context,
	vel *velocity.Store,
	model *anomaly.Store,
	t Tenant,
	o observation,
	rules []rule,
	lists func(name, value string) bool,
	suppressed func(h hit, o observation) bool,
	shadow bool,
) outcome {
	_ = ctx

	out := outcome{id: o.id, shadow: shadow, agency: o.agency, action: ActionAllow}
	if strings.TrimSpace(o.subject) == "" {
		out.refusal = RefusalUnidentified
		return out
	}

	// 1. Record first: everything after reads these rings, and the numbers in the
	// decision must be the ones an investigator sees on the subject.
	record(vel, t, o)

	// 2. Score the model. Assess LEARNS; the tenant's own traffic is its training
	// set and there is no separate training pass.
	tx := types.Transaction{
		ID:                o.id,
		OrgID:             t.String(),
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
	// NO MODEL IS A MODEL THAT REFUSES, NOT A PANIC. The champion's geometry can
	// fail to be housed, and the caller's answer to that is a nil store — so the
	// rules still run, the decision is still recorded, and it says WARMING: the
	// same word the engine uses for a store that holds nothing, because two
	// vocabularies for "the model has no memory" would eventually disagree.
	var assessment anomaly.Assessment
	var modelHit *hit
	if model == nil {
		assessment.Reason = RefusalWarming
	} else {
		assessment = model.Inspect(tx, types.Entity{ID: o.subject, OrgID: t.String()})
		if mh, ok := model.Assess(tx, types.Entity{ID: o.subject, OrgID: t.String()}); ok {
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
	}
	if !assessment.Scored && assessment.Reason != "" {
		out.refusal = assessment.Reason
	}

	// 3. Rules, over the recorded aggregates plus the model's score.
	f := observe(vel, t, o, assessment.Score, !assessment.Scored)
	f.lists = lists
	hits := evaluate(rules, o.stage, f)
	if modelHit != nil {
		hits = append(hits, *modelHit)
	}

	// 4. Suppression. A suppressed hit is kept, marked and given zero weight —
	// it appears in the record and contributes nothing to the action.
	scoring := make([]hit, 0, len(hits))
	for i := range hits {
		if suppressed != nil && suppressed(hits[i], o) {
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
	if shadow {
		out.action = ActionAllow
		if out.refusal == "" {
			out.refusal = RefusalShadow
		}
		return out
	}
	out.action = action
	return out
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

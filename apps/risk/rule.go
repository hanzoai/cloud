package risk

// rule.go is the risk rule algebra: a CLOSED set of fields and a CLOSED set of
// comparisons, evaluated against one decision's facts.
//
// WHY IT IS NOT luxfi/aml's expr-lang evaluator, stated with the measurement.
// The design this app was built from claimed pkg/engine is base-free. It is not.
// Measured with `go list -deps`:
//
//	pkg/engine -> pkg/history -> pkg/store -> github.com/hanzoai/base/core
//	                                       -> github.com/hanzoai/tasks (Temporal SDK)
//	                                       -> github.com/hanzoai/csqlite, hanzoai/sqlite
//
// A grep for `hanzoai/base` in pkg/engine's own files finds nothing, which is
// how the claim was arrived at; the import is TRANSITIVE, through the measures
// in scope.go. Linking it would pull the whole Base application framework and a
// workflow engine into a binary whose job is to answer a payment-authorization
// call in single-digit milliseconds — and cloud's image carries a SQLITE-GATE
// that fails the BUILD, not the tests, when a per-app binary drags a second
// sqlite driver in under CGO.
//
// So the risk plane brings in only the transitively base-free packages
// (types, velocity, anomaly, replay, standard) and states its rules as data.
// That is the better shape here anyway: a risk rule is a conjunction of
// comparisons over a fixed vocabulary, which is a closed algebra, not a
// language. A closed algebra is injection-safe by construction, is a TYPED
// wire shape (so it reaches the SDKs, the CLI and the MCP tools as a schema
// instead of as an opaque string), and can be replayed without an interpreter.
//
// The right long-term fix is upstream and is named in the handoff: split
// pkg/history's Base-backed store into its own package so pkg/engine is
// base-free in fact as well as in intent.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Actions, strongest last. `challenge` and `restrict` are risk's own: the
// compliance vocabulary (allow/flag/review/block) has no word for "ask the human
// to prove they are one" or "let the money in but not out", and both are the
// normal answer at signup and at payout.
const (
	ActionAllow     = "allow"
	ActionChallenge = "challenge"
	ActionReview    = "review"
	ActionRestrict  = "restrict"
	ActionBlock     = "block"
)

// actionRank orders actions by how much they demand, so "the strongest of these"
// and "no stronger than this" are one comparison rather than two tables. An
// action nobody defined ranks lowest: it must never outrank one that was.
func actionRank(a string) int {
	switch a {
	case ActionAllow:
		return 1
	case ActionChallenge:
		return 2
	case ActionReview:
		return 3
	case ActionRestrict:
		return 4
	case ActionBlock:
		return 5
	default:
		return 0
	}
}

// modelCeiling is the strongest action STATISTICAL evidence may reach on its
// own. It is luxfi/aml's types.ActionCeiling and it is not weakened for the
// payment stage: a model can put a transaction in front of a person; it cannot
// decline one, because an unexplainable refusal is not a decision anybody can
// defend to the customer or to a chargeback network.
const modelCeiling = ActionReview

// Stages are the lifecycle moments a decision can be asked about. The stage
// selects the feature window and the rule set; it never selects a different
// tenant gate.
const (
	StageSignup  = "signup"
	StagePayment = "payment"
	StageSession = "session"
	StageUsage   = "usage"
	StagePayout  = "payout"
	StageDispute = "dispute"
)

var stages = map[string]bool{
	StageSignup: true, StagePayment: true, StageSession: true,
	StageUsage: true, StagePayout: true, StageDispute: true,
}

// Operators, closed.
const (
	OpEq       = "eq"
	OpNe       = "ne"
	OpGt       = "gt"
	OpGte      = "gte"
	OpLt       = "lt"
	OpLte      = "lte"
	OpIn       = "in"
	OpNotIn    = "notin"
	OpInList   = "inlist"
	OpNotList  = "notinlist"
	OpExists   = "exists"
	OpAbsent   = "absent"
	OpContains = "contains"
	OpPrefix   = "prefix"
	OpSuffix   = "suffix"
)

var operators = map[string]bool{
	OpEq: true, OpNe: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true,
	OpIn: true, OpNotIn: true, OpInList: true, OpNotList: true,
	OpExists: true, OpAbsent: true, OpContains: true, OpPrefix: true, OpSuffix: true,
}

// facts is the CLOSED vocabulary of scalar fields a term may name. Two families
// are open by shape and closed by source: `signal.<name>` reads the caller's own
// signal map (the caller's data, compared in memory, never spelled into SQL),
// and `velocity.<axis>.<window>.<stat>` reads the in-memory rings through an
// allowlisted axis and window.
var facts = map[string]bool{
	"stage":            true,
	"subject.kind":     true,
	"subject.id":       true,
	"agency":           true,
	"amount.nano":      true,
	"amount.currency":  true,
	"amount.direction": true,
	"model.score":      true,
	"model.warming":    true,
	"actor.agent":      true,
	"actor.session":    true,
}

// velocityStats is the closed set of numbers a rule may read off a ring.
var velocityStats = map[string]bool{"count": true, "sum": true, "near": true, "days": true}

// term is one comparison. Value carries strings, Number carries numerics and
// Values carries a set — three fields rather than one `any`, because the wire
// shape is a published schema and `any` publishes as "anything".
type term struct {
	// Field is the fact to read. One of the closed vocabulary, or
	// `signal.<name>`, or `velocity.<axis>.<window>.<stat>`.
	Field string `json:"field"`
	// Op is the comparison. One of the closed operator set.
	Op string `json:"op"`
	// Value is the string operand, for the textual comparisons.
	Value string `json:"value,omitempty"`
	// Number is the numeric operand, for the ordered comparisons.
	Number float64 `json:"number,omitempty"`
	// Values is the set operand, for `in` and `notin`.
	Values []string `json:"values,omitempty"`
}

// rule is one detection. All terms must hold — a conjunction, deliberately: a
// disjunction is two rules, and two rules are two things a reviewer can judge,
// retire and measure separately. Nothing is lost and the report gets sharper.
type rule struct {
	// ID is the rule's stable identifier within the tenant.
	ID string `json:"id"`
	// Name is what a reviewer reads in an alert.
	Name string `json:"name"`
	// Stage narrows the rule to one lifecycle moment; empty means every stage.
	Stage string `json:"stage,omitempty"`
	// Action is what the rule asks for when it holds.
	Action string `json:"action"`
	// Weight is how much evidence a hit contributes, in [0,1].
	Weight float64 `json:"weight"`
	// Severity is the reviewer-facing grading: low, medium, high or critical.
	Severity string `json:"severity"`
	// Enabled governs the live path. A disabled rule still replays, because the
	// question a simulation asks is what happens ON activation.
	Enabled bool `json:"enabled"`
	// All is the conjunction. An empty conjunction is refused at admission: a
	// rule that holds on everything is not a detection.
	All []term `json:"all"`
}

// admit validates a rule before anything depends on it. Every refusal here is
// one that would otherwise become a rule that fires on everything, on nothing,
// or on a field that does not exist — all three read as a working control.
func admit(r rule) error {
	switch {
	case strings.TrimSpace(r.Name) == "":
		return fmt.Errorf("risk: a rule with no name cannot be read back in an alert")
	case r.Stage != "" && !stages[r.Stage]:
		return fmt.Errorf("risk: %q is not a lifecycle stage", r.Stage)
	case actionRank(r.Action) == 0:
		return fmt.Errorf("risk: %q is not an action", r.Action)
	case r.Weight < 0 || r.Weight > 1:
		return fmt.Errorf("risk: weight %v is outside [0,1]", r.Weight)
	case len(r.All) == 0:
		return fmt.Errorf("risk: a rule with no terms holds on everything, which is not a detection")
	}
	if r.Severity != "" && !severities[r.Severity] {
		return fmt.Errorf("risk: %q is not a severity", r.Severity)
	}
	for i, t := range r.All {
		if err := admitTerm(t); err != nil {
			return fmt.Errorf("risk: term %d: %w", i, err)
		}
	}
	return nil
}

var severities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}

// admitTerm holds the field and operator vocabularies. It is the ONE gate: a
// field is either in the closed set or matches one of the two structured
// families, and an unknown field is an error rather than a silent miss.
func admitTerm(t term) error {
	if !operators[t.Op] {
		return fmt.Errorf("%q is not an operator", t.Op)
	}
	switch {
	case facts[t.Field]:
	case strings.HasPrefix(t.Field, "signal."):
		if strings.TrimPrefix(t.Field, "signal.") == "" {
			return fmt.Errorf("signal. names no signal")
		}
	case strings.HasPrefix(t.Field, "velocity."):
		parts := strings.Split(t.Field, ".")
		if len(parts) != 4 {
			return fmt.Errorf("%q is not velocity.<axis>.<window>.<stat>", t.Field)
		}
		if !velocityAxes[parts[1]] {
			return fmt.Errorf("%q is not a velocity axis", parts[1])
		}
		if !velocityWindows[parts[2]] {
			return fmt.Errorf("%q is not a velocity window", parts[2])
		}
		if !velocityStats[parts[3]] {
			return fmt.Errorf("%q is not a velocity statistic", parts[3])
		}
	default:
		return fmt.Errorf("%q is not a fact this vocabulary carries", t.Field)
	}
	if (t.Op == OpIn || t.Op == OpNotIn) && len(t.Values) == 0 {
		return fmt.Errorf("%s with an empty set holds on nothing", t.Op)
	}
	if (t.Op == OpInList || t.Op == OpNotList) && strings.TrimSpace(t.Value) == "" {
		return fmt.Errorf("%s names no list", t.Op)
	}
	return nil
}

// velocityAxes and velocityWindows mirror the rings the decide path keeps. They
// are declared here because this is where a caller's spelling is checked against
// them; decide.go records on exactly these.
var velocityAxes = map[string]bool{"account": true, "device": true, "ip": true, "pair": true, "email": true, "bin": true}

var velocityWindows = map[string]bool{"1h": true, "24h": true, "7d": true, "30d": true}

// facts is what a term reads: the decision's own inputs plus everything the
// scoring path has computed by the time rules run.
type factSet struct {
	scalar map[string]string
	number map[string]float64
	lists  func(name, value string) bool
}

func (f factSet) str(field string) (string, bool) {
	v, ok := f.scalar[field]
	return v, ok
}

func (f factSet) num(field string) (float64, bool) {
	v, ok := f.number[field]
	if ok {
		return v, true
	}
	// A scalar that parses as a number is comparable as one: `signal.bin` is a
	// string on the wire and an ordered value in a rule.
	if s, ok := f.scalar[field]; ok {
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// holds evaluates one term. An unreadable field is FALSE, never an error that
// aborts the decision: a rule over a signal this caller did not send has not
// matched, and the alternative — failing the whole decision — turns one
// mis-specified rule into an outage on the authorization path.
func (f factSet) holds(t term) bool {
	switch t.Op {
	case OpExists:
		if _, ok := f.str(t.Field); ok {
			return true
		}
		_, ok := f.num(t.Field)
		return ok
	case OpAbsent:
		if _, ok := f.str(t.Field); ok {
			return false
		}
		_, ok := f.num(t.Field)
		return !ok
	case OpGt, OpGte, OpLt, OpLte:
		v, ok := f.num(t.Field)
		if !ok {
			return false
		}
		switch t.Op {
		case OpGt:
			return v > t.Number
		case OpGte:
			return v >= t.Number
		case OpLt:
			return v < t.Number
		default:
			return v <= t.Number
		}
	case OpInList, OpNotList:
		v, ok := f.str(t.Field)
		if !ok || f.lists == nil {
			return t.Op == OpNotList
		}
		in := f.lists(t.Value, v)
		return in == (t.Op == OpInList)
	}

	v, ok := f.str(t.Field)
	if !ok {
		if n, isNum := f.num(t.Field); isNum {
			v, ok = strconv.FormatFloat(n, 'f', -1, 64), true
		}
	}
	if !ok {
		return t.Op == OpNe || t.Op == OpNotIn
	}
	switch t.Op {
	case OpEq:
		return v == t.Value
	case OpNe:
		return v != t.Value
	case OpContains:
		return strings.Contains(v, t.Value)
	case OpPrefix:
		return strings.HasPrefix(v, t.Value)
	case OpSuffix:
		return strings.HasSuffix(v, t.Value)
	case OpIn:
		return contains(t.Values, v)
	case OpNotIn:
		return !contains(t.Values, v)
	}
	return false
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// evaluate runs the enabled rules for a stage and returns the hits in a stable
// order (strongest action first, then by weight, then by id) so two identical
// decisions render identically.
func evaluate(rules []rule, stage string, f factSet) []hit {
	var hits []hit
	for _, r := range rules {
		if !r.Enabled || (r.Stage != "" && r.Stage != stage) {
			continue
		}
		matched := true
		for _, t := range r.All {
			if !f.holds(t) {
				matched = false
				break
			}
		}
		if matched {
			hits = append(hits, hit{Rule: r.ID, Name: r.Name, Action: r.Action, Weight: r.Weight, Severity: r.Severity})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if a, b := actionRank(hits[i].Action), actionRank(hits[j].Action); a != b {
			return a > b
		}
		if hits[i].Weight != hits[j].Weight {
			return hits[i].Weight > hits[j].Weight
		}
		return hits[i].Rule < hits[j].Rule
	})
	return hits
}

// combine turns hits into a score and an action.
//
// The score is weight-of-evidence: 1 - prod(1-w). Two independent weak signals
// compound and no single one saturates, which is the property a summed score
// does not have (three 0.4 rules sum past 1 and clamp, losing the fourth).
//
// The action is the strongest any hit asked for, and the model's own hit is
// capped at the ceiling BEFORE it gets here, so no arrangement of weights lets
// statistical evidence decline anything on its own.
func combine(hits []hit) (float64, string) {
	remain, action := 1.0, ActionAllow
	for _, h := range hits {
		w := h.Weight
		if w < 0 {
			w = 0
		}
		if w > 1 {
			w = 1
		}
		remain *= 1 - w
		if actionRank(h.Action) > actionRank(action) {
			action = h.Action
		}
	}
	return 1 - remain, action
}

// starter is the rule set a tenant gets on its first decision. Every one of
// these is a lifecycle-stage detection the product spec names, expressed in the
// vocabulary above so a tenant can read, copy and retire them. They are seeded
// ENABLED but the tenant starts in SHADOW, so nothing acts until an operator
// says so and can see what would have happened.
func starter() []rule {
	return []rule{{
		ID: "signup-burst-ip", Name: "Many signups from one address", Stage: StageSignup,
		Action: ActionChallenge, Weight: 0.4, Severity: "medium", Enabled: true,
		All: []term{{Field: "velocity.ip.1h.count", Op: OpGte, Number: 5}},
	}, {
		ID: "signup-shared-device", Name: "One device onboarding several accounts", Stage: StageSignup,
		Action: ActionReview, Weight: 0.5, Severity: "high", Enabled: true,
		All: []term{{Field: "velocity.device.7d.count", Op: OpGte, Number: 4}},
	}, {
		ID: "signup-disposable-email", Name: "Disposable email domain", Stage: StageSignup,
		Action: ActionChallenge, Weight: 0.35, Severity: "medium", Enabled: true,
		All: []term{{Field: "signal.emaildomain", Op: OpInList, Value: "email-deny"}},
	}, {
		ID: "payment-card-testing", Name: "Card testing: a burst of small charges", Stage: StagePayment,
		Action: ActionBlock, Weight: 0.7, Severity: "critical", Enabled: true,
		All: []term{
			{Field: "velocity.bin.1h.count", Op: OpGte, Number: 10},
			{Field: "amount.nano", Op: OpLte, Number: 5_000_000_000},
		},
	}, {
		ID: "payment-denied-ip", Name: "Payment from a denied address", Stage: StagePayment,
		Action: ActionBlock, Weight: 0.9, Severity: "critical", Enabled: true,
		All: []term{{Field: "signal.ip", Op: OpInList, Value: "ip-deny"}},
	}, {
		ID: "usage-spend-spike", Name: "Pay-as-you-go spend far above this account's own shape", Stage: StageUsage,
		Action: ActionRestrict, Weight: 0.5, Severity: "high", Enabled: true,
		All: []term{{Field: "velocity.account.24h.sum", Op: OpGte, Number: 100_000_000_000_000}},
	}, {
		ID: "payout-velocity", Name: "Payouts leaving faster than they arrived", Stage: StagePayout,
		Action: ActionRestrict, Weight: 0.6, Severity: "high", Enabled: true,
		All: []term{
			{Field: "amount.direction", Op: OpEq, Value: "out"},
			{Field: "velocity.account.24h.count", Op: OpGte, Number: 5},
		},
	}, {
		ID: "bot-anonymous-burst", Name: "Undeclared automation at machine cadence", Stage: StageSession,
		Action: ActionChallenge, Weight: 0.45, Severity: "medium", Enabled: true,
		All: []term{
			{Field: "agency", Op: OpEq, Value: AgencyBot},
			{Field: "velocity.ip.1h.count", Op: OpGte, Number: 60},
		},
	}}
}

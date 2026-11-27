package train

// policy.go — an Enso router policy: the enso.policy/1 file the engine's tuner
// writes (hanzo-inc/engine retrain `tune`, policy::cascade::PolicyFile), what of it
// production can serve, and the tier routes it compiles to.
//
// The file is per benchmark: the pool of arms, the sealed lockbox, and per tier a
// cascade with its budget and its in-sample, held-out and lockbox scores. The
// engine serves one model a request (hanzo-inc/engine tests/serve.rs
// exactly_one_upstream_inference_call_per_request), so a tier production can
// serve asks one arm in one stage, the same arm on every benchmark, and that arm
// names the upstream it was measured as. Tier t serves the Enso model enso-<t>.
//
// What a policy sets is the lead of each of its tiers' routes. Promoting it puts
// that upstream first on the tier's route and keeps the rest behind it, in order,
// so the tier still fails over as it did; every other model and field of the
// catalog stays as it was.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// policySchema is the one schema the registry takes (policy::cascade::SCHEMA).
const policySchema = "enso.policy/1"

// policyMax bounds a policy file: a few benchmarks of a few thousand questions,
// each tier's decisions included, are a few megabytes.
const policyMax = 8 << 20

// tierName is a tier's name in a policy file: what its Enso model is named after.
var tierName = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)

// skuOf is the Enso model a policy tier serves.
func skuOf(tier string) string { return "enso-" + tier }

// PolicyScore is one score in a policy file: over n questions, the share answered
// right (no answer counts wrong), dollars per thousand questions and latency.
type PolicyScore struct {
	N          int     `json:"n"`
	Accuracy   float64 `json:"accuracy"`
	USDPer1k   float64 `json:"usd_per_1k"`
	LatencyP50 float64 `json:"latency_p50_s"`
	LatencyP95 float64 `json:"latency_p95_s"`
}

type policyStage struct {
	Arms []string `json:"arms"`
}

type policyCascade struct {
	Stages []policyStage `json:"stages"`
	Agree  int           `json:"agree"`
}

type policyTier struct {
	Budget  float64         `json:"budget_usd_per_1k"`
	Front   json.RawMessage `json:"front,omitempty"`
	Cascade policyCascade   `json:"cascade"`
	Train   PolicyScore     `json:"train"`
	Heldout PolicyScore     `json:"heldout"`
	Lockbox PolicyScore     `json:"lockbox"`
}

type policyBench struct {
	Questions  int                   `json:"questions"`
	Pool       []string              `json:"pool"`
	Lockbox    []string              `json:"lockbox"`
	Ceiling    PolicyScore           `json:"ceiling"`
	BestSingle PolicyScore           `json:"best_single"`
	Tiers      map[string]policyTier `json:"tiers"`
}

type policyModel struct {
	PriceIn  float64            `json:"price_in"`
	PriceOut float64            `json:"price_out"`
	Accuracy map[string]float64 `json:"accuracy"`
	Answered map[string]int     `json:"answered"`
	// Upstream is the model id the arm was asked as through the gateway, which is
	// the upstream an Enso route names.
	Upstream string `json:"upstream,omitempty"`
}

type policySource struct {
	Attempts string `json:"attempts"`
	Digest   string `json:"digest"`
	Folds    int    `json:"folds"`
	Lockbox  string `json:"lockbox"`
}

// policyDoc is what the registry reads of a policy file; the rest (each tier's
// decisions, the frontier, a front's rules) is kept as written.
type policyDoc struct {
	Schema  string                 `json:"schema"`
	Source  policySource           `json:"source"`
	Models  map[string]policyModel `json:"models"`
	Benches map[string]policyBench `json:"benches"`
}

// canonical is a JSON document re-encoded with its object keys sorted, each
// number in one spelling (a whole number as an integer, any other as the shortest
// decimal that reads back as the same float64) and no insignificant space: two
// files that say the same thing, however a tuner or a browser spelled them, are
// one sequence of bytes, and so one digest.
func canonical(raw []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("more than one JSON value")
	}
	v, err := spelled(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// spelled is v with every number in its one spelling.
func spelled(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("number %s out of range", x)
		}
		if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return json.Number(strconv.FormatInt(int64(f), 10)), nil
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	case map[string]any:
		for k, e := range x {
			n, err := spelled(e)
			if err != nil {
				return nil, err
			}
			x[k] = n
		}
	case []any:
		for i, e := range x {
			n, err := spelled(e)
			if err != nil {
				return nil, err
			}
			x[i] = n
		}
	}
	return v, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readPolicy validates a policy file and answers it canonical, its digest and
// what the registry reads of it. Every problem is named at once.
func readPolicy(raw []byte) ([]byte, string, policyDoc, error) {
	var doc policyDoc
	if len(raw) == 0 {
		return nil, "", doc, fmt.Errorf("document: a policy file is required")
	}
	if len(raw) > policyMax {
		return nil, "", doc, fmt.Errorf("document: %d bytes, past %d", len(raw), policyMax)
	}
	canon, err := canonical(raw)
	if err != nil {
		return nil, "", doc, fmt.Errorf("document: not JSON: %v", err)
	}
	if err := json.Unmarshal(canon, &doc); err != nil {
		return nil, "", doc, fmt.Errorf("document: %v", err)
	}
	var bad []string
	if doc.Schema != policySchema {
		bad = append(bad, fmt.Sprintf("schema is %q, not %s", doc.Schema, policySchema))
	}
	if strings.TrimSpace(doc.Source.Digest) == "" {
		bad = append(bad, "source.digest is empty: a policy names the answers it was tuned on")
	}
	if doc.Source.Folds < 2 || strings.TrimSpace(doc.Source.Lockbox) == "" {
		bad = append(bad, "source must state its folds (2 or more) and its lockbox rule")
	}
	for arm, m := range doc.Models {
		if m.PriceIn < 0 || m.PriceOut < 0 || math.IsNaN(m.PriceIn) || math.IsNaN(m.PriceOut) {
			bad = append(bad, fmt.Sprintf("model %s: a price below zero", arm))
		}
	}
	if len(doc.Benches) == 0 {
		bad = append(bad, "it holds no benchmark")
	}
	for _, bench := range sorted(doc.Benches) {
		b := doc.Benches[bench]
		if b.Questions <= 0 {
			bad = append(bad, fmt.Sprintf("%s: no questions", bench))
		}
		if len(b.Tiers) == 0 {
			bad = append(bad, fmt.Sprintf("%s: no tier", bench))
		}
		for _, s := range []PolicyScore{b.Ceiling, b.BestSingle} {
			bad = append(bad, scoreProblems(bench, s)...)
		}
		for _, t := range sorted(b.Tiers) {
			tier := b.Tiers[t]
			if !tierName.MatchString(t) {
				bad = append(bad, fmt.Sprintf("%s: tier %q is not a tier name: lowercase letters and digits", bench, t))
			}
			if tier.Budget < 0 || math.IsNaN(tier.Budget) {
				bad = append(bad, fmt.Sprintf("%s/%s: a budget below zero", bench, t))
			}
			if len(tier.Cascade.Stages) == 0 {
				bad = append(bad, fmt.Sprintf("%s/%s: its cascade asks no arm", bench, t))
			}
			for _, st := range tier.Cascade.Stages {
				if len(st.Arms) == 0 {
					bad = append(bad, fmt.Sprintf("%s/%s: a stage asks no arm", bench, t))
				}
				for _, arm := range st.Arms {
					if _, ok := doc.Models[arm]; !ok {
						bad = append(bad, fmt.Sprintf("%s/%s: arm %s is not among its models", bench, t, arm))
					}
				}
			}
			for _, s := range []PolicyScore{tier.Train, tier.Heldout, tier.Lockbox} {
				bad = append(bad, scoreProblems(bench+"/"+t, s)...)
			}
		}
	}
	if len(bad) > 0 {
		return nil, "", doc, fmt.Errorf("document: %s", strings.Join(bad, "; "))
	}
	return canon, sum(canon), doc, nil
}

func scoreProblems(where string, s PolicyScore) []string {
	if s.N < 0 || s.Accuracy < 0 || s.Accuracy > 1 || s.USDPer1k < 0 || math.IsNaN(s.Accuracy) || math.IsNaN(s.USDPer1k) {
		return []string{where + ": a score out of range"}
	}
	return nil
}

func sorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// plan is what of a policy production can serve: each tier's one arm and the
// upstream it names, and why any tier cannot be served whatever the catalog.
type plan struct {
	Tiers   map[string]PolicyLead `json:"tiers"`
	Reasons []string              `json:"reasons"`
}

// PolicyLead is the one arm a tier is served by, and the upstream it names.
type PolicyLead struct {
	Arm      string `json:"arm"`
	Upstream string `json:"upstream"`
}

// planOf reads the routes a policy sets. A tier is served by one model a request,
// so a tier asking more than one arm, or in more than one stage, cannot be; neither
// can one whose benchmarks choose different arms, nor an arm naming no upstream.
func planOf(doc policyDoc) plan {
	p := plan{Tiers: map[string]PolicyLead{}, Reasons: []string{}}
	chosen := map[string]map[string]string{} // tier -> bench -> arm
	for _, bench := range sorted(doc.Benches) {
		for _, t := range sorted(doc.Benches[bench].Tiers) {
			c := doc.Benches[bench].Tiers[t].Cascade
			n := 0
			for _, s := range c.Stages {
				n += len(s.Arms)
			}
			if len(c.Stages) != 1 || n != 1 {
				p.Reasons = append(p.Reasons, fmt.Sprintf("tier %s on %s asks %d models in %d stages; a tier is served by one model a request", t, bench, n, len(c.Stages)))
				continue
			}
			if chosen[t] == nil {
				chosen[t] = map[string]string{}
			}
			chosen[t][bench] = c.Stages[0].Arms[0]
		}
	}
	for _, t := range sorted(chosen) {
		arms := map[string]bool{}
		for _, a := range chosen[t] {
			arms[a] = true
		}
		if len(arms) > 1 {
			var says []string
			for _, bench := range sorted(chosen[t]) {
				says = append(says, bench+" chooses "+chosen[t][bench])
			}
			p.Reasons = append(p.Reasons, fmt.Sprintf("tier %s: %s; a tier serves one model", t, strings.Join(says, ", ")))
			continue
		}
		var arm string
		for a := range arms {
			arm = a
		}
		up := strings.TrimSpace(doc.Models[arm].Upstream)
		if up == "" {
			p.Reasons = append(p.Reasons, fmt.Sprintf("tier %s: arm %s names no upstream, so no route can serve it", t, arm))
			continue
		}
		p.Tiers[t] = PolicyLead{Arm: arm, Upstream: up}
	}
	if len(p.Tiers) == 0 && len(p.Reasons) == 0 {
		p.Reasons = append(p.Reasons, "it sets no tier")
	}
	return p
}

// ensoCatalog is an Enso admin catalog as the engine writes it (hanzo-inc/engine
// admin.rs), held as JSON so every field it carries goes back as it came.
type ensoCatalog struct {
	root   map[string]any
	models map[string]any
}

func readEnso(raw []byte) (*ensoCatalog, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var root map[string]any
	if err := d.Decode(&root); err != nil {
		return nil, fmt.Errorf("the Enso catalog is not JSON: %v", err)
	}
	models, _ := root["models"].(map[string]any)
	if models == nil {
		return nil, fmt.Errorf("the Enso catalog lists no models")
	}
	return &ensoCatalog{root: root, models: models}, nil
}

// model is one of the catalog's models, nil when it lists none of that name.
func (c *ensoCatalog) model(id string) map[string]any {
	m, _ := c.models[id].(map[string]any)
	return m
}

// route is a model's rungs, in order.
func (c *ensoCatalog) route(id string) []map[string]any {
	m := c.model(id)
	if m == nil {
		return nil
	}
	rs, _ := m["route"].([]any)
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		if rung, ok := r.(map[string]any); ok {
			out = append(out, rung)
		}
	}
	return out
}

func upstreamOf(rung map[string]any) string {
	s, _ := rung["upstream"].(string)
	return strings.TrimSpace(s)
}

// upstreams is a model's route as the upstreams it names, in order.
func (c *ensoCatalog) upstreams(id string) []string {
	var out []string
	for _, r := range c.route(id) {
		out = append(out, upstreamOf(r))
	}
	return out
}

func off(m map[string]any) bool {
	b, _ := m["disabled"].(bool)
	return b
}

// paid is a model the engine funds from the prepaid lane: a tier the price guard
// holds to its price, and the only kind a policy routes.
func paid(m map[string]any) bool {
	f, _ := m["funding"].(string)
	return f == "prepaid"
}

// servable is why a policy's routes cannot be served on this catalog, every
// reason at once; none when they can. A tier's lead must be on its model's route
// already: promoting reorders a route and adds no rung, since a rung's long-prompt
// price and efforts are kept only where a route names it again.
func servable(p plan, c *ensoCatalog) []string {
	out := append([]string{}, p.Reasons...)
	for _, t := range sorted(p.Tiers) {
		pt := p.Tiers[t]
		sku := skuOf(t)
		m := c.model(sku)
		switch {
		case m == nil:
			out = append(out, fmt.Sprintf("tier %s: the Enso catalog serves no %s", t, sku))
		case off(m):
			out = append(out, fmt.Sprintf("tier %s: %s is disabled in the Enso catalog", t, sku))
		case !paid(m):
			out = append(out, fmt.Sprintf("tier %s: %s is not a paid tier; a policy routes the paid tiers", t, sku))
		case !slices.Contains(c.upstreams(sku), pt.Upstream):
			out = append(out, fmt.Sprintf("tier %s: %s (arm %s) is not on %s's route; add it there in Model routing first", t, pt.Upstream, pt.Arm, sku))
		}
	}
	return out
}

// PolicyChain is one tier's route as it serves now and as a change would leave it.
type PolicyChain struct {
	Tier   string   `json:"tier"`
	Model  string   `json:"model"`
	Before []string `json:"before"`
	After  []string `json:"after"`
}

// lead puts upstream first on model's route. The rest follow in order: the order
// they had before any policy where it names them, then any rung it does not, as
// they stand. A tier therefore fails over as it did before any policy, whichever
// versions were promoted between.
func (c *ensoCatalog) lead(model, upstream string, order []string) {
	m := c.model(model)
	if m == nil {
		return
	}
	rs := c.route(model)
	rank := func(r map[string]any) int {
		up := upstreamOf(r)
		if up == upstream {
			return -1
		}
		if i := slices.Index(order, up); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(rs, func(a, b map[string]any) int { return rank(a) - rank(b) })
	next := make([]any, 0, len(rs))
	for _, r := range rs {
		next = append(next, r)
	}
	m["route"] = next
}

// order puts model's route in the order given where it names a rung; any rung it
// does not name follows, as it stands.
func (c *ensoCatalog) order(model string, order []string) { c.lead(model, "", order) }

func (c *ensoCatalog) bytes() ([]byte, error) { return json.Marshal(c.root) }

// realize lays a registry's serving state onto the catalog as it serves, in
// place: each of p's tiers leads with its arm, and every other model a policy set
// goes back to the order its route had before any policy. Nothing but order moves:
// the rungs are the live ones, so a rung added, removed or repriced since stays as
// the catalog has it. It records in r the order before any policy of each model p
// sets that r does not hold yet, and answers every route it changed.
func realize(r *registry, p plan, c *ensoCatalog) []PolicyChain {
	for _, t := range sorted(p.Tiers) {
		if m := skuOf(t); r.origin[m] == nil {
			r.origin[m] = append([]string{}, c.upstreams(m)...)
		}
	}
	var out []PolicyChain
	for _, m := range sorted(r.origin) {
		if c.model(m) == nil {
			// A release of the catalog dropped the model: there is no route to put back.
			delete(r.origin, m)
			continue
		}
		before := c.upstreams(m)
		t := strings.TrimPrefix(m, "enso-")
		if pt, set := p.Tiers[t]; set {
			c.lead(m, pt.Upstream, r.origin[m])
		} else {
			c.order(m, r.origin[m])
		}
		if after := c.upstreams(m); !slices.Equal(before, after) {
			out = append(out, PolicyChain{Tier: t, Model: m, Before: before, After: after})
		}
	}
	return out
}

// PolicyScores is one score in two versions; either is absent where its version has
// none.
type PolicyScores struct {
	From *PolicyScore `json:"from"`
	To   *PolicyScore `json:"to"`
}

type PolicyStages struct {
	From [][]string `json:"from"`
	To   [][]string `json:"to"`
}

type PolicyNames struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// PolicyTierDiff is one tier on one benchmark across two versions.
type PolicyTierDiff struct {
	Tier    string       `json:"tier"`
	Arms    PolicyStages `json:"arms"`
	Budget  [2]float64   `json:"budget"`
	Heldout PolicyScores `json:"heldout"`
	Lockbox PolicyScores `json:"lockbox"`
}

type PolicyBenchDiff struct {
	Bench      string           `json:"bench"`
	Pool       PolicyNames      `json:"pool"`
	BestSingle PolicyScores     `json:"best_single"`
	Tiers      []PolicyTierDiff `json:"tiers"`
}

// PolicyDiff is what changed from one version to another.
type PolicyDiff struct {
	From    int               `json:"from"`
	To      int               `json:"to"`
	Source  [2]string         `json:"source"`
	Models  PolicyNames       `json:"models"`
	Benches []PolicyBenchDiff `json:"benches"`
}

func delta(a, b []string) PolicyNames {
	out := PolicyNames{Added: []string{}, Removed: []string{}}
	for _, x := range b {
		if !slices.Contains(a, x) {
			out.Added = append(out.Added, x)
		}
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			out.Removed = append(out.Removed, x)
		}
	}
	return out
}

func stages(c policyCascade) [][]string {
	out := [][]string{}
	for _, s := range c.Stages {
		out = append(out, s.Arms)
	}
	return out
}

func scoreOf(ok bool, s PolicyScore) *PolicyScore {
	if !ok {
		return nil
	}
	return &s
}

// diffPolicies is what changed from a to b: the evidence they were tuned on,
// the arms they know, and per benchmark its pool, best single arm and each tier's
// cascade, budget and scores on new questions (held out) and the sealed set
// (lockbox).
func diffPolicies(from, to int, a, b policyDoc) PolicyDiff {
	d := PolicyDiff{From: from, To: to, Source: [2]string{a.Source.Digest, b.Source.Digest},
		Models: delta(sorted(a.Models), sorted(b.Models)), Benches: []PolicyBenchDiff{}}
	benches := sorted(a.Benches)
	for _, k := range sorted(b.Benches) {
		if !slices.Contains(benches, k) {
			benches = append(benches, k)
		}
	}
	sort.Strings(benches)
	for _, bench := range benches {
		x, inA := a.Benches[bench]
		y, inB := b.Benches[bench]
		bd := PolicyBenchDiff{Bench: bench, Pool: delta(x.Pool, y.Pool),
			BestSingle: PolicyScores{scoreOf(inA, x.BestSingle), scoreOf(inB, y.BestSingle)}, Tiers: []PolicyTierDiff{}}
		tiers := sorted(x.Tiers)
		for _, k := range sorted(y.Tiers) {
			if !slices.Contains(tiers, k) {
				tiers = append(tiers, k)
			}
		}
		sort.Strings(tiers)
		for _, t := range tiers {
			p, inX := x.Tiers[t]
			q, inY := y.Tiers[t]
			td := PolicyTierDiff{Tier: t, Budget: [2]float64{p.Budget, q.Budget},
				Heldout: PolicyScores{scoreOf(inX, p.Heldout), scoreOf(inY, q.Heldout)},
				Lockbox: PolicyScores{scoreOf(inX, p.Lockbox), scoreOf(inY, q.Lockbox)}}
			if inX {
				td.Arms.From = stages(p.Cascade)
			}
			if inY {
				td.Arms.To = stages(q.Cascade)
			}
			bd.Tiers = append(bd.Tiers, td)
		}
		d.Benches = append(d.Benches, bd)
	}
	return d
}

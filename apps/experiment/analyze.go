package experiment

// analyze.go — the pure analysis core: fold per-subject analytics outcomes into
// per-variant samples (joined to flags assignment), then compute per-variant
// conversion rate, lift, and significance vs the control arm. Everything here is
// I/O-free and deterministic, so the tests drive it with in-memory samples — no
// analytics warehouse, no flags engine — and the significance math is pinned to
// reference values.

import (
	"math"
	"sort"
)

// defaultAlpha is the two-tailed significance threshold (95% confidence).
const defaultAlpha = 0.05

// sample is one variant's raw evidence: subjects Exposed (the denominator) and, of
// those, subjects who Converted (the numerator — fired the metric event).
type sample struct {
	exposed   int
	converted int
}

// Outcome is one variant's measured outcome and its comparison to the control arm.
// Lift/Z/PValue/Significant are 0/false for the control itself (it is its own
// baseline).
type Outcome struct {
	Arm         string  `json:"variant"`     // the arm this row measures
	Control     bool    `json:"control"`     // true on the baseline arm; its own lift and stats are zero
	Exposed     int     `json:"exposed"`     // subjects the arm enrolled — the denominator
	Converted   int     `json:"converted"`   // of those, how many fired the metric event
	Rate        float64 `json:"rate"`        // converted over exposed
	Lift        float64 `json:"lift"`        // relative to control: (rate-ctrl)/ctrl
	Z           float64 `json:"z"`           // two-proportion z vs control
	PValue      float64 `json:"pValue"`      // two-tailed p vs control
	Significant bool    `json:"significant"` // pValue < alpha
}

// Analysis is the experiment's full read: per-variant Outcomes plus the advisory
// Winner (the significant treatment with the highest rate that beats control, else
// "" when inconclusive). The Winner is advisory — decide takes an explicit choice.
type Analysis struct {
	Trial        string    `json:"experiment"`   // the experiment that was analysed
	Metric       string    `json:"metric"`       // the event a conversion is counted from
	Alpha        float64   `json:"alpha"`        // the two-tailed threshold significance was judged at
	Outcomes     []Outcome `json:"results"`      // one row per declared arm, control first
	Winner       string    `json:"winner"`       // ADVISORY: the significant, control-beating arm with the highest rate, else empty
	ExposedTotal int       `json:"exposedTotal"` // subjects enrolled across every arm
}

// foldOutcomes buckets per-subject analytics outcomes into per-variant samples by
// joining each exposed subject to its flags variant. assign is the deterministic
// subject -> variant function (flags.Assign in production, a fake in tests); a
// subject not enrolled in any arm (variant "") or not exposed is skipped, so the
// denominator is exactly "subjects the experiment assigned AND that saw the arm".
// An assign error on one subject drops that subject (fail-safe: a single
// unassignable subject never poisons the whole analysis).
func foldOutcomes(outcomes []MetricOutcome, assign func(subject string) (string, error)) map[string]*sample {
	samples := map[string]*sample{}
	for _, o := range outcomes {
		if !o.Exposed {
			continue
		}
		v, err := assign(o.Subject)
		if err != nil || v == "" {
			continue
		}
		s := samples[v]
		if s == nil {
			s = &sample{}
			samples[v] = s
		}
		s.exposed++
		if o.Converted {
			s.converted++
		}
	}
	return samples
}

// computeAnalysis is the pure statistics: per-variant rate, then lift + a
// two-proportion z-test vs the control arm, then the advisory winner. Arms with
// no samples still appear (Exposed 0), so the read is complete over the experiment's
// declared arms. Deterministic ordering: control first, then by variant key.
func computeAnalysis(exp Trial, samples map[string]*sample, alpha float64) Analysis {
	if alpha <= 0 || alpha >= 1 {
		alpha = defaultAlpha
	}
	control := exp.controlKey()
	cs := samples[control]
	cExposed, cConverted := 0, 0
	if cs != nil {
		cExposed, cConverted = cs.exposed, cs.converted
	}
	cRate := rate(cConverted, cExposed)

	a := Analysis{Trial: exp.ID, Metric: exp.MetricEvent, Alpha: alpha}
	for _, variant := range exp.Arms {
		s := samples[variant.Key]
		exposed, converted := 0, 0
		if s != nil {
			exposed, converted = s.exposed, s.converted
		}
		a.ExposedTotal += exposed
		r := Outcome{
			Arm:       variant.Key,
			Control:   variant.Key == control,
			Exposed:   exposed,
			Converted: converted,
			Rate:      round4(rate(converted, exposed)),
		}
		if variant.Key != control {
			z, p := twoProportionZ(cConverted, cExposed, converted, exposed)
			r.Z = round4(z)
			r.PValue = round4(p)
			r.Significant = p < alpha
			if cRate > 0 {
				r.Lift = round4((rate(converted, exposed) - cRate) / cRate)
			}
		}
		a.Outcomes = append(a.Outcomes, r)
	}
	sort.SliceStable(a.Outcomes, func(i, j int) bool {
		if a.Outcomes[i].Control != a.Outcomes[j].Control {
			return a.Outcomes[i].Control // control first
		}
		return a.Outcomes[i].Arm < a.Outcomes[j].Arm
	})
	a.Winner = pickWinner(a.Outcomes, cRate)
	return a
}

// pickWinner returns the significant, control-beating treatment with the highest
// conversion rate, else "" (inconclusive — no promotion recommended).
func pickWinner(results []Outcome, controlRate float64) string {
	winner, best := "", controlRate
	for _, r := range results {
		if r.Control || !r.Significant {
			continue
		}
		if r.Rate > best {
			winner, best = r.Arm, r.Rate
		}
	}
	return winner
}

// twoProportionZ computes the two-proportion z statistic and the two-tailed p-value
// for H0: p_treatment == p_control, using the pooled-variance estimator. The p-value
// is exact via the standard-normal survival function 2*(1-Phi(|z|)) = erfc(|z|/sqrt2)
// — stdlib math.Erfc, no dependency. Degenerate inputs (an empty arm, no variance)
// return (0, 1): not significant, never a divide-by-zero.
func twoProportionZ(cConverted, cExposed, tConverted, tExposed int) (z, p float64) {
	if cExposed == 0 || tExposed == 0 {
		return 0, 1
	}
	pc := float64(cConverted) / float64(cExposed)
	pt := float64(tConverted) / float64(tExposed)
	pool := float64(cConverted+tConverted) / float64(cExposed+tExposed)
	variance := pool * (1 - pool) * (1/float64(cExposed) + 1/float64(tExposed))
	if variance <= 0 {
		return 0, 1
	}
	z = (pt - pc) / math.Sqrt(variance)
	p = math.Erfc(math.Abs(z) / math.Sqrt2)
	return z, p
}

func rate(converted, exposed int) float64 {
	if exposed == 0 {
		return 0
	}
	return float64(converted) / float64(exposed)
}

func round4(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*1e4) / 1e4
}

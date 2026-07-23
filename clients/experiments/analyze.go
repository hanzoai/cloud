package experiments

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

// Result is one variant's measured outcome and its comparison to the control arm.
// Lift/Z/PValue/Significant are 0/false for the control itself (it is its own
// baseline).
type Result struct {
	Variant     string  `json:"variant"`
	Control     bool    `json:"control"`
	Exposed     int     `json:"exposed"`
	Converted   int     `json:"converted"`
	Rate        float64 `json:"rate"`
	Lift        float64 `json:"lift"`        // relative to control: (rate-ctrl)/ctrl
	Z           float64 `json:"z"`           // two-proportion z vs control
	PValue      float64 `json:"pValue"`      // two-tailed p vs control
	Significant bool    `json:"significant"` // pValue < alpha
}

// Analysis is the experiment's full read: per-variant Results plus the advisory
// Winner (the significant treatment with the highest rate that beats control, else
// "" when inconclusive). The Winner is advisory — decide takes an explicit choice.
type Analysis struct {
	Experiment   string   `json:"experiment"`
	Metric       string   `json:"metric"`
	Alpha        float64  `json:"alpha"`
	Results      []Result `json:"results"`
	Winner       string   `json:"winner"`
	ExposedTotal int      `json:"exposedTotal"`
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
// two-proportion z-test vs the control arm, then the advisory winner. Variants with
// no samples still appear (Exposed 0), so the read is complete over the experiment's
// declared arms. Deterministic ordering: control first, then by variant key.
func computeAnalysis(exp Experiment, samples map[string]*sample, alpha float64) Analysis {
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

	a := Analysis{Experiment: exp.ID, Metric: exp.MetricEvent, Alpha: alpha}
	for _, variant := range exp.Variants {
		s := samples[variant.Key]
		exposed, converted := 0, 0
		if s != nil {
			exposed, converted = s.exposed, s.converted
		}
		a.ExposedTotal += exposed
		r := Result{
			Variant:   variant.Key,
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
		a.Results = append(a.Results, r)
	}
	sort.SliceStable(a.Results, func(i, j int) bool {
		if a.Results[i].Control != a.Results[j].Control {
			return a.Results[i].Control // control first
		}
		return a.Results[i].Variant < a.Results[j].Variant
	})
	a.Winner = pickWinner(a.Results, cRate)
	return a
}

// pickWinner returns the significant, control-beating treatment with the highest
// conversion rate, else "" (inconclusive — no promotion recommended).
func pickWinner(results []Result, controlRate float64) string {
	winner, best := "", controlRate
	for _, r := range results {
		if r.Control || !r.Significant {
			continue
		}
		if r.Rate > best {
			winner, best = r.Variant, r.Rate
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

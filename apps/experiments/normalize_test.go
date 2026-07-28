package experiments

import (
	"encoding/json"
	"testing"
)

func twoVariants() []Variant {
	return []Variant{{Key: "control", Control: true, Weight: 50}, {Key: "treatment", Weight: 50}}
}

func TestNormalize_HappyPath(t *testing.T) {
	e, err := normalize(createBody{
		ID: "checkout", Name: "Checkout CTA", MetricEvent: "order_completed", Variants: twoVariants(),
	}, "web", "z@hanzo.ai")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if e.FlagKey != "exp_checkout" {
		t.Fatalf("default flagKey = %q want exp_checkout", e.FlagKey)
	}
	if e.SubjectKind != SubjectUser {
		t.Fatalf("default subjectKind = %q want user", e.SubjectKind)
	}
	if e.ExposureEvent != defaultExposureEvent {
		t.Fatalf("default exposure = %q", e.ExposureEvent)
	}
	if e.Status != StatusRunning || e.Project != "web" || e.CreatedBy != "z@hanzo.ai" {
		t.Fatalf("stamps wrong: %+v", e)
	}
}

func TestNormalize_Rejects(t *testing.T) {
	bad := []struct {
		name string
		body createBody
	}{
		{"empty id", createBody{ID: "", MetricEvent: "m", Variants: twoVariants()}},
		{"path id", createBody{ID: "../etc", MetricEvent: "m", Variants: twoVariants()}},
		{"no metric", createBody{ID: "x", MetricEvent: "", Variants: twoVariants()}},
		{"one variant", createBody{ID: "x", MetricEvent: "m", Variants: []Variant{{Key: "a", Weight: 100}}}},
		{"bad subjectKind", createBody{ID: "x", MetricEvent: "m", SubjectKind: "planet", Variants: twoVariants()}},
		{"dup variant", createBody{ID: "x", MetricEvent: "m", Variants: []Variant{{Key: "a", Weight: 50}, {Key: "a", Weight: 50}}}},
		{"two controls", createBody{ID: "x", MetricEvent: "m", Variants: []Variant{{Key: "a", Control: true, Weight: 50}, {Key: "b", Control: true, Weight: 50}}}},
		{"weights not 100", createBody{ID: "x", MetricEvent: "m", Variants: []Variant{{Key: "a", Weight: 30}, {Key: "b", Weight: 40}}}},
		{"bad variant slug", createBody{ID: "x", MetricEvent: "m", Variants: []Variant{{Key: "a/b", Weight: 50}, {Key: "c", Weight: 50}}}},
		{"bad flagKey", createBody{ID: "x", MetricEvent: "m", FlagKey: "../f", Variants: twoVariants()}},
	}
	for _, c := range bad {
		if _, err := normalize(c.body, "web", "u"); err == nil {
			t.Fatalf("%s: expected rejection", c.name)
		}
	}
}

// TestNormalizeVariants_EvenSplit: all-zero weights become an even split summing to
// 100 (deterministic default), so a caller may omit weights.
func TestNormalizeVariants_EvenSplit(t *testing.T) {
	vs, err := normalizeVariants([]Variant{{Key: "a"}, {Key: "b"}, {Key: "c"}, {Key: "d"}})
	if err != nil {
		t.Fatalf("even split: %v", err)
	}
	for _, v := range vs {
		if v.Weight != 25 {
			t.Fatalf("even split want 25 each, got %v for %s", v.Weight, v.Key)
		}
	}
}

// TestFlagDef_PostHogShape asserts create builds the exact multivariate shape the
// native evaluator consumes: active, a 100% group, weighted variants, per-variant
// payloads — and that the variant KIND is opaque (an ad-creative id rides untouched).
func TestFlagDef_PostHogShape(t *testing.T) {
	e := Experiment{
		FlagKey: "exp_banner",
		Variants: []Variant{
			{Key: "control", Weight: 50, Payload: json.RawMessage(`{"creative":"cre_a"}`)},
			{Key: "treatment", Weight: 50, Payload: json.RawMessage(`{"creative":"cre_b"}`)},
		},
	}
	raw, err := e.flagDef()
	if err != nil {
		t.Fatalf("flagDef: %v", err)
	}
	var def struct {
		Key     string `json:"key"`
		Active  bool   `json:"active"`
		Filters struct {
			Groups       []map[string]any `json:"groups"`
			Multivariate struct {
				Variants []struct {
					Key    string  `json:"key"`
					Weight float64 `json:"rollout_percentage"`
				} `json:"variants"`
			} `json:"multivariate"`
			Payloads map[string]json.RawMessage `json:"payloads"`
		} `json:"filters"`
	}
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("decode def: %v", err)
	}
	if def.Key != "exp_banner" || !def.Active {
		t.Fatalf("def head wrong: %s", raw)
	}
	if len(def.Filters.Groups) != 1 || def.Filters.Groups[0]["rollout_percentage"].(float64) != 100 {
		t.Fatalf("want one 100%% group: %s", raw)
	}
	if len(def.Filters.Multivariate.Variants) != 2 || def.Filters.Multivariate.Variants[0].Weight != 50 {
		t.Fatalf("variants wrong: %s", raw)
	}
	if string(def.Filters.Payloads["treatment"]) != `{"creative":"cre_b"}` {
		t.Fatalf("payload (ad-creative id) must ride untouched: %s", def.Filters.Payloads["treatment"])
	}
}

// TestPromoteVariant_RewritesWeights: promoting a winner sets it to 100% and every
// other arm to 0%, preserving payloads + groups (read-modify-write, no clobber).
func TestPromoteVariant_RewritesWeights(t *testing.T) {
	def := json.RawMessage(`{
		"key":"exp_x","active":true,
		"filters":{
			"groups":[{"properties":[],"rollout_percentage":100}],
			"multivariate":{"variants":[
				{"key":"control","rollout_percentage":50},
				{"key":"treatment","rollout_percentage":50}
			]},
			"payloads":{"treatment":{"cta":"buy"}}
		}
	}`)
	out, err := promoteVariant(def, "treatment")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	mv := m["filters"].(map[string]any)["multivariate"].(map[string]any)["variants"].([]any)
	for _, raw := range mv {
		v := raw.(map[string]any)
		want := 0.0
		if v["key"] == "treatment" {
			want = 100
		}
		if v["rollout_percentage"].(float64) != want {
			t.Fatalf("variant %v weight = %v want %v", v["key"], v["rollout_percentage"], want)
		}
	}
	// payload + group preserved.
	if m["filters"].(map[string]any)["payloads"].(map[string]any)["treatment"] == nil {
		t.Fatalf("promote must preserve payloads: %s", out)
	}
}

func TestPromoteVariant_UnknownWinnerErrors(t *testing.T) {
	def := json.RawMessage(`{"key":"x","filters":{"multivariate":{"variants":[{"key":"a","rollout_percentage":100}]}}}`)
	if _, err := promoteVariant(def, "ghost"); err == nil {
		t.Fatalf("promoting a non-variant must error")
	}
}

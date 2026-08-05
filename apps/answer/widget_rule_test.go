// Copyright © 2026 Hanzo AI. MIT License.

package answer

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// THE PROMPT TEACHES A FORMAT SOMETHING ELSE PARSES, AND THE TWO CANNOT BE CHECKED
// BY THE COMPILER.
//
// The renderer lives in the extension (packages/browser/src/answer/widget.ts): it
// validates every block and DROPS anything it does not recognise. So a typo here —
// a renamed field, a kind the client has no branch for, a number where a string
// belongs — does not fail anything. It produces answers whose widgets silently never
// appear, which looks exactly like a model that chose not to emit one.
//
// These assertions are the closest thing to a shared type. They hold the examples in
// the prompt to the shape the client actually accepts, so drift is red HERE, in the
// repo that can fix it.
func TestWidgetRuleTeachesShapesTheClientAccepts(t *testing.T) {
	// The kinds the client has a branch for, and the fields each one requires.
	required := map[string][]string{
		"comparison": {"columns", "rows"},
		"steps":      {"steps"},
		"stats":      {"stats"},
		"timeline":   {"events"},
		"entity":     {"title", "facts"},
		"definition": {"term", "meaning"},
	}

	objects := regexp.MustCompile(`\{"kind":.*`).FindAllString(widgetRule, -1)
	if len(objects) != len(required) {
		t.Fatalf("prompt carries %d example objects, want one per kind (%d): the model is "+
			"taught a set the client does not mirror", len(objects), len(required))
	}

	seen := map[string]bool{}
	for _, raw := range objects {
		var got map[string]any
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Errorf("example is not valid JSON, so it teaches a format nothing can parse: %v\n%s", err, raw)
			continue
		}
		kind, _ := got["kind"].(string)
		fields, ok := required[kind]
		if !ok {
			t.Errorf("example names kind %q, which the client has no branch for and will drop", kind)
			continue
		}
		seen[kind] = true
		for _, f := range fields {
			if _, present := got[f]; !present {
				t.Errorf("kind %q example omits required field %q — the client refuses a block without it", kind, f)
			}
		}
	}
	for kind := range required {
		if !seen[kind] {
			t.Errorf("kind %q is never shown to the model, so it will never be emitted", kind)
		}
	}
}

// A comparison row must have exactly as many cells as there are columns. The client
// refuses a ragged table rather than padding it (a padded grid renders broken), so an
// example that models raggedness would teach the one mistake guaranteed to be dropped.
func TestWidgetRuleComparisonExampleIsRectangular(t *testing.T) {
	raw := regexp.MustCompile(`\{"kind":"comparison".*`).FindString(widgetRule)
	if raw == "" {
		t.Fatal("no comparison example in the prompt")
	}
	var got struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("comparison example does not parse: %v", err)
	}
	for i, row := range got.Rows {
		if len(row) != len(got.Columns) {
			t.Errorf("example row %d has %d cells against %d columns — the client drops ragged tables",
				i, len(row), len(got.Columns))
		}
	}
}

// Both modes must carry the rule. They are separate constants, so appending it to
// one and not the other is a one-line omission that nothing else would notice —
// research would simply never produce a widget.
func TestEveryModeCarriesTheWidgetRule(t *testing.T) {
	for name, m := range modes {
		if !strings.Contains(m.system, "hanzo-widget") {
			t.Errorf("mode %q has a system prompt with no widget rule, so it can never emit one", name)
		}
	}
}

// The rule must say the prose stands alone. The client drops an invalid block and
// keeps the prose, so an answer that leaned on a widget to be complete would read as
// a hole exactly when validation refused one.
func TestWidgetRuleRequiresSelfSufficientProse(t *testing.T) {
	if !strings.Contains(widgetRule, "stand on its own") {
		t.Error("the rule does not tell the model the prose must stand alone; a dropped " +
			"widget would then leave the answer incomplete")
	}
	if !strings.Contains(widgetRule, "never HTML") {
		t.Error("the rule does not forbid HTML; the model reads the open web, so markup it " +
			"authored is an injection path into the client's origin")
	}
}

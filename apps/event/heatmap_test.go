// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"encoding/json"
	"reflect"
	"testing"
)

// clickFact is an @hanzo/observe $click as the plane holds it: every property a string.
func clickFact() message {
	m := pageFact()
	m.Kind = "track"
	m.Name = "$click"
	m.URL = "https://hanzo.ai/pricing"
	m.Path = "/pricing"
	m.Attributes = map[string]string{
		"$x": "403", "$y": "1880", "$el_x": "34", "$el_y": "12",
		"$target_fixed":   "false",
		"$viewport_width": "1440", "$viewport_height": "900",
		"$page_width": "1440", "$page_height": "4373",
		"$el": "main/plans/button[Get Pro]",
	}
	return m
}

// insightsProps is the properties map insights' plugin-server parses from the record.
func insightsProps(t *testing.T, m message) map[string]any {
	t.Helper()
	rec, err := insightsRecord(m, m.Time)
	if err != nil || rec == nil {
		t.Fatalf("record: %v %v", rec, err)
	}
	var env captured
	if err := json.Unmarshal(rec.Value, &env); err != nil {
		t.Fatal(err)
	}
	var data capturedData
	if err := json.Unmarshal([]byte(env.Data), &data); err != nil {
		t.Fatal(err)
	}
	return data.Properties
}

func TestClickCarriesTheHeatmapDataInsightsDrawsFrom(t *testing.T) {
	p := insightsProps(t, clickFact())
	want := map[string]any{"https://hanzo.ai/pricing": []any{map[string]any{
		"x": 403.0, "y": 1880.0, "target_fixed": false, "type": "click",
	}}}
	if !reflect.DeepEqual(p["$heatmap_data"], want) {
		t.Fatalf("$heatmap_data = %#v", p["$heatmap_data"])
	}
	// insights drops the whole event when a heat map rides a viewport that is not a number.
	for _, k := range []string{"$viewport_width", "$viewport_height", "$x", "$y", "$page_height"} {
		if _, ok := p[k].(float64); !ok {
			t.Errorf("%s = %#v, want a number", k, p[k])
		}
	}
	if p["$el"] != "main/plans/button[Get Pro]" {
		t.Errorf("$el = %v: a label is not a measurement", p["$el"])
	}
}

func TestFixedTargetAndRageClick(t *testing.T) {
	m := clickFact()
	m.Name = "$rageclick"
	m.Attributes["$target_fixed"] = "true"
	m.Attributes["$y"] = "32"
	items := insightsProps(t, m)["$heatmap_data"].(map[string]any)["https://hanzo.ai/pricing"].([]any)
	got := items[0].(map[string]any)
	if got["type"] != "rageclick" || got["target_fixed"] != true || got["y"] != 32.0 {
		t.Fatalf("item = %#v", got)
	}
}

func TestKeyboardClickIsInNoHeatmap(t *testing.T) {
	m := clickFact()
	delete(m.Attributes, "$x")
	delete(m.Attributes, "$y")
	if _, ok := insightsProps(t, m)["$heatmap_data"]; ok {
		t.Fatal("a click with no position was put on the heat map")
	}
}

func TestClickWithoutAViewportCarriesNoHeatmap(t *testing.T) {
	m := clickFact()
	delete(m.Attributes, "$viewport_width")
	if _, ok := insightsProps(t, m)["$heatmap_data"]; ok {
		t.Fatal("heat map data without a viewport would drop the event in insights")
	}
}

func TestPageLeaveCarriesItsScrollReach(t *testing.T) {
	m := clickFact()
	m.Name = "$pageleave"
	m.URL = "https://hanzo.ai/"
	m.Path = "/"
	m.Attributes = map[string]string{
		"$scroll_max": "5400", "$depth_max": "48", "$active_ms": "41250", "$seq": "0",
		"$viewport_width": "390", "$viewport_height": "844",
		"$sections": `{"hero":12000,"plans":3100}`,
	}
	p := insightsProps(t, m)
	if p["$prev_pageview_pathname"] != "/" || p["$prev_pageview_max_scroll"] != 5400.0 {
		t.Fatalf("scroll reach = %v %v", p["$prev_pageview_pathname"], p["$prev_pageview_max_scroll"])
	}
	if p["$active_ms"] != 41250.0 || p["$depth_max"] != 48.0 {
		t.Errorf("measurements = %v %v", p["$active_ms"], p["$depth_max"])
	}
	if s, ok := p["$sections"].(map[string]any); !ok || s["hero"] != 12000.0 {
		t.Errorf("$sections = %#v, want the object", p["$sections"])
	}
	if _, ok := p["$heatmap_data"]; ok {
		t.Error("a page leave is not a click")
	}
}

func TestOrdinaryEventsAreUntouched(t *testing.T) {
	p := insightsProps(t, pageFact())
	for _, k := range []string{"$heatmap_data", "$prev_pageview_pathname"} {
		if _, ok := p[k]; ok {
			t.Errorf("%s on a page view", k)
		}
	}
	m := pageFact()
	m.Attributes["plan"] = "0123"
	if insightsProps(t, m)["plan"] != "0123" {
		t.Error("a caller's numeric-looking string was turned into a number")
	}
}

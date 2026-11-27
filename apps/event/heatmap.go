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

// heatmap.go — @hanzo/observe's positions and reach, as insights' heat maps read them.
//
// insights draws a heat map from its `heatmaps` table, and its ingestion fills that
// table from two shapes on an event (nodejs extract-heatmap-data-step.ts):
//
//	$heatmap_data: {url: [{x, y, target_fixed, type}]}   a click or rage click
//	$prev_pageview_pathname + $prev_pageview_max_scroll  how far a page view scrolled
//
// beside `$viewport_width`, `$viewport_height` and `$session_id`. @hanzo/observe sends
// what both are made of under its own names — `$x`/`$y`/`$target_fixed` on every
// `$click` and `$rageclick`, `$scroll_max` on every `$pageleave` — so this is a
// rendering, like the rest of the bridge, and the client stays one wire.
//
// THE NUMBERS MUST BE NUMBERS. The plane stores every property as a string, and
// insights' extraction DROPS THE WHOLE EVENT — not only its heat map — when an event
// carrying heat map data has a viewport that is not a number. So the observe
// properties that are measurements go back to numbers here, on every event, before
// any heat map data is attached.

package event

import "strconv"

// measured are the observe properties that are measurements: pixel positions and
// sizes, durations in milliseconds, counts and ratios. They reach insights as
// numbers, so its lenses can sum and bucket them.
var measured = []string{
	"$x", "$y", "$el_x", "$el_y",
	"$viewport_width", "$viewport_height", "$page_width", "$page_height",
	"$scroll_max", "$depth_max", "$active_ms", "$elapsed_ms", "$seq",
	"$ttfb_ms", "$fcp_ms", "$lcp_ms", "$inp_ms", "$cls",
	"depth", "ratio",
}

// pointer names the observe events a heat map counts, and the type insights files
// each under.
var pointer = map[string]string{
	"$click":     "click",
	"$rageclick": "rageclick",
}

// heatmap renders the measurements as numbers and, for a pointer event or a page
// leave, the shapes insights' heat maps are filled from. url and path are the
// page the event was filed under.
func heatmap(name string, props map[string]any, url, path string) {
	for _, k := range measured {
		s, ok := props[k].(string)
		if !ok {
			continue
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			props[k] = f
		}
	}
	if url == "" {
		return
	}
	if _, ok := props["$viewport_width"].(float64); !ok {
		return
	}
	if _, ok := props["$viewport_height"].(float64); !ok {
		return
	}
	if kind, ok := pointer[name]; ok {
		x, okx := props["$x"].(float64)
		y, oky := props["$y"].(float64)
		if !okx || !oky {
			// A keyboard activation has no position, and is in no heat map.
			return
		}
		props["$heatmap_data"] = map[string][]map[string]any{url: {{
			"x":            x,
			"y":            y,
			"target_fixed": props["$target_fixed"] == "true",
			"type":         kind,
		}}}
		return
	}
	if name == "$pageleave" && path != "" {
		if scroll, ok := props["$scroll_max"].(float64); ok {
			props["$prev_pageview_pathname"] = path
			props["$prev_pageview_max_scroll"] = scroll
		}
	}
}

// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"testing"
	"time"
)

// TestToCapture_PreservesUTMAttribution proves the PostHog-wire adapter carries the
// BARE utm_* campaign params (what PostHog SDKs emit) through to the native
// CaptureEvent — and thence, via normalize, into the attributes['utm_*'] entries
// event.event stores. Regression guard: these were previously dropped, so
// every campaign-attributed pageview lost its source/medium/campaign on the
// PostHog wire and the web/commerce lens could never attribute it.
func TestToCapture_PreservesUTMAttribution(t *testing.T) {
	e := insightsEvent{
		Event:      "$pageview",
		DistinctID: "visitor-1",
		Properties: map[string]any{
			"utm_source":   "newsletter",
			"utm_medium":   "email",
			"utm_campaign": "launch",
			"utm_term":     "analytics",
			"utm_content":  "hero-cta",
			"$current_url": "https://hanzo.ai/insights",
		},
	}
	cap := e.toCapture()
	if cap.UTM.Source != "newsletter" || cap.UTM.Medium != "email" ||
		cap.UTM.Campaign != "launch" || cap.UTM.Term != "analytics" || cap.UTM.Content != "hero-cta" {
		t.Fatalf("UTM not mapped from PostHog wire: %+v", cap.UTM)
	}
	// End-to-end through the normalizer into the attributes the sink binds.
	f, ok := normalize("acme", time.Now(), cap)
	if !ok {
		t.Fatal("want ok")
	}
	a := f.attributes
	if a["utm_source"] != "newsletter" || a["utm_medium"] != "email" ||
		a["utm_campaign"] != "launch" || a["utm_term"] != "analytics" || a["utm_content"] != "hero-cta" {
		t.Fatalf("UTM lost before the fact: %v", a)
	}
}

// TestToCapture_IdempotencyID proves the client event id (PostHog top-level `uuid`,
// or the `$insert_id` property fallback) is preserved as the stable fact id — the
// ReplacingMergeTree key that makes a retried batch collapse instead of duplicate —
// while an absent id still falls back to a server-minted one.
func TestToCapture_IdempotencyID(t *testing.T) {
	// top-level uuid wins
	f, _ := normalize("acme", time.Now(),
		insightsEvent{Event: "signup", DistinctID: "u1", UUID: "evt-abc"}.toCapture())
	if f.id != "evt-abc" {
		t.Fatalf("top-level uuid not preserved as fact id, got %q", f.id)
	}
	// $insert_id property fallback when no top-level uuid
	f2, _ := normalize("acme", time.Now(),
		insightsEvent{Event: "signup", DistinctID: "u1", Properties: map[string]any{"$insert_id": "ins-9"}}.toCapture())
	if f2.id != "ins-9" {
		t.Fatalf("$insert_id fallback not preserved, got %q", f2.id)
	}
	// absent → server still mints a non-empty id
	f3, _ := normalize("acme", time.Now(),
		insightsEvent{Event: "signup", DistinctID: "u1"}.toCapture())
	if f3.id == "" {
		t.Fatal("server must still mint an id when the client sends none")
	}
}

// TestToCapture_MapsCoreFields guards that the pre-existing $-property mappings
// still hold alongside the new UTM/idempotency mappings (no regression).
func TestToCapture_MapsCoreFields(t *testing.T) {
	cap := insightsEvent{
		Event:      "$pageview",
		DistinctID: "v1",
		Properties: map[string]any{
			"$session_id":  "s1",
			"$current_url": "https://hanzo.ai/x",
			"$pathname":    "/x",
			"$referrer":    "https://news.ycombinator.com/",
			"$lib":         "insights-go",
			"$lib_version": "1.2.3",
			"product":      "console",
		},
	}.toCapture()
	if cap.Type != "pageview" || cap.SessionID != "s1" || cap.URL != "https://hanzo.ai/x" ||
		cap.Path != "/x" || cap.Referrer != "https://news.ycombinator.com/" ||
		cap.Library != "insights-go" || cap.LibraryVer != "1.2.3" || cap.Product != "console" {
		t.Fatalf("core PostHog-wire mapping regressed: %+v", cap)
	}
}

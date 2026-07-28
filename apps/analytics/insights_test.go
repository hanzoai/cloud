// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"testing"
	"time"
)

// TestToCapture_PreservesUTMAttribution proves the PostHog-wire adapter carries the
// BARE utm_* campaign params (what PostHog SDKs emit) through to the native
// CaptureEvent — and thence, via normalizeEvent, into the hanzo.events utm_*
// columns the INSERT binds. Regression guard: these were previously dropped, so
// every campaign-attributed pageview lost its source/medium/campaign on the
// /v1/insights/e front door and the web/commerce lens could never attribute it.
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
	// End-to-end through the normalizer into the positional row the INSERT binds.
	row, ok := normalizeEvent("acme", time.Now(), cap)
	if !ok {
		t.Fatal("want ok")
	}
	if row.utmSource != "newsletter" || row.utmMedium != "email" ||
		row.utmCampaign != "launch" || row.utmTerm != "analytics" || row.utmContent != "hero-cta" {
		t.Fatalf("UTM lost before the events row: src=%q med=%q camp=%q term=%q content=%q",
			row.utmSource, row.utmMedium, row.utmCampaign, row.utmTerm, row.utmContent)
	}
}

// TestToCapture_IdempotencyID proves the client event id (PostHog top-level `uuid`,
// or the `$insert_id` property fallback) is preserved as the stable row id, so a
// retried batch does not mint a fresh id per attempt — while an absent id still
// falls back to a server-minted one (existing behavior unchanged).
func TestToCapture_IdempotencyID(t *testing.T) {
	// top-level uuid wins
	row, _ := normalizeEvent("acme", time.Now(),
		insightsEvent{Event: "signup", DistinctID: "u1", UUID: "evt-abc"}.toCapture())
	if row.id != "evt-abc" {
		t.Fatalf("top-level uuid not preserved as row id, got %q", row.id)
	}
	// $insert_id property fallback when no top-level uuid
	row2, _ := normalizeEvent("acme", time.Now(),
		insightsEvent{Event: "signup", DistinctID: "u1", Properties: map[string]any{"$insert_id": "ins-9"}}.toCapture())
	if row2.id != "ins-9" {
		t.Fatalf("$insert_id fallback not preserved, got %q", row2.id)
	}
	// absent → server still mints a non-empty id
	row3, _ := normalizeEvent("acme", time.Now(),
		insightsEvent{Event: "signup", DistinctID: "u1"}.toCapture())
	if row3.id == "" {
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

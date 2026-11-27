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
	"bytes"
	"image/gif"
	"testing"
	"time"
)

// TestPixelIsAOnePixelGIF: the image decodes, and it is 1x1.
func TestPixelIsAOnePixelGIF(t *testing.T) {
	img, err := gif.Decode(bytes.NewReader(pixelGIF))
	if err != nil {
		t.Fatalf("pixel does not decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 1 || b.Dy() != 1 {
		t.Fatalf("pixel is %dx%d", b.Dx(), b.Dy())
	}
}

// TestPixelViewNamesThePage: the page is ?u= or else the Referer, the campaign is
// read off the page, and the visitor is a daily hash that names no cookie.
func TestPixelViewNamesThePage(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	ev := pixelView("", "https://hanzo.ai/pricing?utm_source=mail&utm_campaign=fall", "pk-a", "203.0.113.9", "Mozilla/5.0", now)
	if ev.Type != "pageview" || ev.URL != "https://hanzo.ai/pricing?utm_source=mail&utm_campaign=fall" || ev.Path != "/pricing" {
		t.Fatalf("view = %+v", ev)
	}
	if ev.Referrer != "" {
		t.Fatalf("a Referer that is the page is not also its referrer: %q", ev.Referrer)
	}
	if ev.UTM.Source != "mail" || ev.UTM.Campaign != "fall" {
		t.Fatalf("utm = %+v", ev.UTM)
	}
	if len(ev.AnonymousID) != 35 || ev.AnonymousID[:3] != "px-" {
		t.Fatalf("visitor = %q", ev.AnonymousID)
	}

	stated := pixelView("https://hanzo.ai/blog/x", "https://mail.example/inbox", "pk-a", "203.0.113.9", "Mozilla/5.0", now)
	if stated.URL != "https://hanzo.ai/blog/x" || stated.Referrer != "https://mail.example/inbox" {
		t.Fatalf("?u= names the page and the Referer is where it came from: %+v", stated)
	}

	same := pixelView("https://hanzo.ai/", "", "pk-a", "203.0.113.9", "Mozilla/5.0", now.Add(time.Hour))
	next := pixelView("https://hanzo.ai/", "", "pk-a", "203.0.113.9", "Mozilla/5.0", now.Add(24*time.Hour))
	other := pixelView("https://hanzo.ai/", "", "pk-b", "203.0.113.9", "Mozilla/5.0", now)
	if same.AnonymousID != ev.AnonymousID {
		t.Fatal("the same browser is one visitor within a day")
	}
	if next.AnonymousID == ev.AnonymousID || other.AnonymousID == ev.AnonymousID {
		t.Fatal("a visitor must not carry across days or across projects")
	}

	for _, page := range []string{"", "not a url", "/relative", "javascript:alert(1)", "ftp://hanzo.ai/"} {
		if got := pixelView(page, "", "pk-a", "", "", now); got.URL != "" {
			t.Fatalf("page %q must not be recorded: %+v", page, got)
		}
	}
}

// TestPixelViewIsFirstParty: every view is kept; the choice the embedding page
// states is only what it may be forwarded under.
func TestPixelViewIsFirstParty(t *testing.T) {
	now := time.Now()
	ev := pixelView("https://hanzo.ai/blog/x", "", "pk-a", "203.0.113.9", "Mozilla/5.0", now)
	if got, _ := stamp([]CaptureEvent{ev}, visit{refused: true}); len(got) != 1 || got[0].Properties["consent"] != "" {
		t.Fatalf("a pixel view under GPC: %v", got)
	}
	ev.Properties = map[string]any{"consent": "analytics"}
	if got, _ := stamp([]CaptureEvent{ev}, visit{}); len(got) != 1 || got[0].Properties["consent"] != "analytics" {
		t.Fatalf("a pixel view under a stated choice: %v", got)
	}
}

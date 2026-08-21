// Copyright © 2026 Hanzo AI. MIT License.

package event

// browsertag.go — which platforms the hosted tag can inject a pixel for.
//
// It lives beside tag.js because that is the file with the injectors: the map says
// which INJECT entry a platform dispatches to, and a name here with no entry there is
// a site told it is tracking and is not. Two readers derive from it and neither keeps
// a copy — apps/projects answers GET /v1/tags with it, apps/destinations marks each
// catalog row so a console can offer a pixel input for exactly these platforms.
//
// A platform absent here forwards SERVER-SIDE ONLY, which is the honest state for our
// own sinks (Hanzo Insights, Hanzo Analytics): they need no third-party tag.

// BrowserTags maps a destination platform id to the tag.js injector it dispatches on.
// The key is the platform's own Destination.ID(), which is not always the obvious slug
// — Google Ads is "google-ads".
var BrowserTags = map[string]string{
	"ga4":        "ga",
	"google-ads": "gads",
	"linkedin":   "linkedin",
	"meta":       "meta",
	"pinterest":  "pinterest",
	"reddit":     "reddit",
	"tiktok":     "tiktok",
	"x":          "x",
}

// HasPixel reports whether the hosted tag injects a browser pixel for this platform.
func HasPixel(platform string) bool { _, ok := BrowserTags[platform]; return ok }

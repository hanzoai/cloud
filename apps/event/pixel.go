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

// pixel.go — GET /v1/event/pixel/{key}.gif, a site's default pixel.
//
//	<img src="https://api.hanzo.ai/v1/event/pixel/pk-….gif" alt="" width="1" height="1">
//
// Every project has one, because every project has a publishable key: the pixel is
// the key in an image address. It is the install path for a page that runs no
// script at all — an email, a static page, a host that forbids a tag — and it
// records exactly one thing, a page view, onto the SAME write core every other
// ingest reaches: the key admits it to its project, the visitor is stamped from the
// request (visitor.go), and a developer's own machine is dropped.
//
// The page is `?u=` when the embedder says so, else the Referer the browser sent
// with the image. The visitor is a daily hash of the key, address and agent: a
// pixel sets no cookie and reads none, so the same browser is one visitor for a
// day and nobody after it.
//
// The view is first party, so it is recorded for every visitor (consent.go). An image
// cannot ask, so the embedder states what the visitor allowed, `?consent=analytics`
// (the categories the cookie spells), and that is what the view may be forwarded
// under; a view stating none is forwarded nowhere.
//
// It ALWAYS answers the image, with no-store, whatever happened to the view. A
// broken-image icon on somebody's page is the embedder's cost for our refusal, and
// the answer must not tell a stranger which keys are live.

package event

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// pixelGIF is a transparent 1x1 GIF89a.
var pixelGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
}

// pixelSource is the wire a pixel view arrived on, as the write core records it.
const pixelSource = "pixel"

// pixelPath is the pixel's address, in the router's spelling the document keys on.
const pixelPath = "/v1/event/pixel/:key"

// The pixel declares itself beside itself, as the tag does: an image response under
// the media type servePixel sets, and the line a reader pastes.
func init() {
	spec.Register(pixelPath, http.MethodGet, nil, spec.Bytes{Type: "image/gif"})
	spec.Describe(pixelPath, http.MethodGet,
		"A site's default pixel — a page view from an image, for a page that runs no script",
		"Answers a transparent 1x1 GIF and records one page view for the project whose "+
			"publishable key names the address:\n\n"+
			"    <img src=\"https://api.hanzo.ai/v1/event/pixel/pk-….gif\" alt=\"\" width=\"1\" height=\"1\">\n\n"+
			"The page is `?u=` when given, else the Referer. The visitor is stamped from the "+
			"request like every event, is one visitor for a day, and carries no cookie. "+
			"Every view is recorded: it is the site's own first-party analytics. `?consent=analytics`, "+
			"the visitor's choice as the embedding page knows it, is what it may be forwarded to "+
			"Google Analytics under; Do-not-track and Global Privacy Control forward nothing, and a "+
			"developer's own machine records nothing. The image is answered whatever happened to the "+
			"view, with no-store.")
}

// servePixel records the view, when there is one to record, and answers the image.
func servePixel(c *zip.Ctx) error {
	key := strings.TrimSuffix(c.Param("key"), ".gif")
	if cloud.IsPublishableKey(key) {
		if a, ok := keyAdmission(c, key); ok && a.project != "" {
			ev := pixelView(c.Query("u"), c.Header("Referer"), key,
				strings.TrimSpace(geo.IP(c)), strings.TrimSpace(c.Header("User-Agent")), time.Now())
			// An image grants no more than Analytics, whatever its address says: no
			// view it records is ever an ad platform's.
			if g, _ := ParseGrants(c.Query("consent")); g.Analytics {
				ev.Properties = map[string]any{"consent": Grants{Analytics: true}.String()}
			}
			if ev.URL != "" {
				evs := attributeProject([]CaptureEvent{ev}, a.project, a.key)
				evs[0].keepLocal = a.keepLocal
				evs[0].networks = a.networks
				// A same-site page sends its session cookie with the image, so a view an
				// operator made acting as someone else is marked like any other event.
				evs[0].signer = signerOf(zip.CallerOf(c.Forward()))
				evs, _ = visitors(c, a.org, evs)
				if _, err := ingestEvents(c.Context(), a.org, pixelSource, evs); err != nil {
					c.Log().Warn("pixel view not recorded", "project", a.project, "err", err)
				}
			}
		}
	}
	c.SetHeader("Content-Type", "image/gif")
	c.SetHeader("Cache-Control", "no-store, max-age=0")
	c.SetHeader("Access-Control-Allow-Origin", "*")
	return c.Bytes(http.StatusOK, pixelGIF)
}

// pixelView is the page view one image request states. page is `?u=`, falling back
// to the Referer; a page that is not an absolute http(s) address yields an event
// with no URL, which the caller does not record. The referrer is the Referer only
// when the page came from `?u=` and the two differ, since otherwise the Referer IS
// the page.
func pixelView(page, referer, key, ip, ua string, now time.Time) CaptureEvent {
	page, referer = strings.TrimSpace(page), strings.TrimSpace(referer)
	from := ""
	if page == "" {
		page = referer
	} else if referer != page {
		from = referer
	}
	u, err := url.Parse(page)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return CaptureEvent{}
	}
	q := u.Query()
	sum := sha256.Sum256([]byte(key + "|" + ip + "|" + ua + "|" + now.UTC().Format(time.DateOnly)))
	return CaptureEvent{
		Type:        "pageview",
		Timestamp:   now.UTC().Format(time.RFC3339Nano),
		AnonymousID: "px-" + hex.EncodeToString(sum[:16]),
		URL:         u.String(),
		Path:        u.EscapedPath(),
		Referrer:    from,
		UTM: UTM{
			Source:   q.Get("utm_source"),
			Medium:   q.Get("utm_medium"),
			Campaign: q.Get("utm_campaign"),
			Term:     q.Get("utm_term"),
			Content:  q.Get("utm_content"),
		},
		Library: pixelSource,
	}
}

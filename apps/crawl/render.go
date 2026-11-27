package crawl

// render.go — a page as a browser draws it.
//
// Read answers what a page says. A design starts from what it looks like, and
// that is a browser's to say: the first screen as a picture, the DOM it ends in
// once its scripts have run, its words and its links. This is the one op that
// asks the renderer (browser.go) for all four, through the same gate and the same
// fee as an escalation, because it is the same act: one render.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/hanzoai/outbound"
	"github.com/zap-proto/zip"
)

const (
	// maxDOM caps the DOM an answer carries. It is read to see how a page is put
	// together, and past a few hundred kilobytes it is repetition of that.
	maxDOM = 256 << 10
	// maxLinks caps the links an answer carries, first come first kept.
	maxLinks = 100
)

// shot is what a render asks of the renderer beyond the markdown a read gets: the
// first screen as a PNG, drawn without the consent banner or overlay that would
// cover it. The renderer's own defaults decide the rest, so a render here is the
// browser a read escalates to.
var shot = map[string]any{
	"screenshot":                true,
	"force_viewport_screenshot": true,
	"remove_consent_popups":     true,
	"remove_overlay_elements":   true,
}

// Link is one anchor of a rendered page.
type Link struct {
	Href string `json:"href"`
	Text string `json:"text"`
}

// Rendered is one page as the browser drew it.
type Rendered struct {
	// Page is the address the browser landed on, the title, the markdown of the
	// rendered document and what it said of itself.
	Page
	// Screenshot is the first screen, a PNG.
	Screenshot []byte
	// DOM is the document the page ended in, scripts and styles removed, cut at
	// maxDOM. Cut says whether it was.
	DOM string
	Cut bool
	// Links are the page's anchors, internal ones first, at most maxLinks.
	Links []Link
}

// Render draws one URL in the browser.
//
// The address is judged as Read judges it. A GET through the guarded client runs
// first and follows the redirect chain through the dialer, so a public URL that
// bounces to a private address is refused at the hop that matters, with the same
// error; a page that merely cannot be read statically goes on to the browser,
// which can read what a plain GET cannot. The browser itself is bounded by its
// pod's egress policy (see browser.go).
func Render(ctx context.Context, raw string) (*Rendered, error) {
	var landed []string
	resp, _, err := open(ctx, raw)
	switch {
	case errors.Is(err, outbound.ErrRefused):
		return nil, err
	case err == nil:
		landed = append(landed, resp.Request.URL.String())
		resp.Body.Close()
	}

	r, err := visit(ctx, raw, shot, func(r *browserResult) error {
		if r.Screenshot == "" {
			return fmt.Errorf("crawl: browser drew no screenshot of %s", raw)
		}
		return nil
	}, landed...)
	if err != nil {
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(r.Screenshot)
	if err != nil {
		return nil, fmt.Errorf("crawl: the screenshot of %s is not a picture: %w", raw, err)
	}

	meta := map[string]any{}
	maps.Copy(meta, r.Metadata)
	meta["renderer"] = "browser"
	title, _ := meta["title"].(string)
	out := &Rendered{
		Page:       Page{URL: raw, Title: title, Markdown: strings.TrimSpace(string(r.Markdown)), Metadata: meta},
		Screenshot: png,
		DOM:        r.CleanedHTML,
		Links:      links(r.Links),
	}
	if r.URL != "" {
		out.URL = r.URL
		meta["sourceURL"] = r.URL
	}
	if len(out.DOM) > maxDOM {
		out.DOM, out.Cut = strings.ToValidUTF8(out.DOM[:maxDOM], ""), true
	}
	return out, nil
}

// links reads the renderer's `links` — anchors under `internal` and `external` —
// and answers them in that order, once each. A shape it does not know is no links:
// they are a courtesy of the render, never a reason for it to fail.
func links(raw json.RawMessage) []Link {
	var in struct {
		Internal []Link `json:"internal"`
		External []Link `json:"external"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var out []Link
	seen := map[string]bool{}
	for _, l := range append(in.Internal, in.External...) {
		if l.Href == "" || seen[l.Href] {
			continue
		}
		seen[l.Href] = true
		out = append(out, l)
		if len(out) == maxLinks {
			break
		}
	}
	return out
}

// RenderPath is the address of the render op.
const RenderPath = "/v1/crawl/render"

// renderRequest is the /v1/crawl/render body.
type renderRequest struct {
	// URL is the page to draw, absolute and http or https, judged exactly as
	// read_page judges it. Empty is a 400 with success false.
	URL string `json:"url"`
}

// renderResult is the /v1/crawl/render response, shaped as read_page's is: a page
// that could not be drawn is a normal answer, not a fault.
type renderResult struct {
	// Success is whether the page was drawn. FALSE with an Error is a complete
	// answer, not a fault — check this before reading Data.
	Success bool `json:"success"`
	// Data is the page, present exactly when Success.
	Data *renderDocument `json:"data,omitempty"`
	// Error says what stopped the render: the host was refused, unreachable, the
	// browser could not draw it, or the caller cannot cover the fee.
	Error string `json:"error,omitempty"`
}

// StatusCode states the one non-2xx this answer carries as its own body, as
// [crawlResult.StatusCode] does: a request with no url is 400.
func (r *renderResult) StatusCode() int {
	if !r.Success && r.Error == missingURL {
		return http.StatusBadRequest
	}
	return http.StatusOK
}

// renderDocument is the drawn page.
type renderDocument struct {
	// URL is the address the browser landed on, after redirects.
	URL string `json:"url"`
	// Title is the document's title, when it carried one.
	Title string `json:"title,omitempty"`
	// Screenshot is the first screen of the page as a PNG, base64 on the wire.
	Screenshot []byte `json:"screenshot"`
	// DOM is the page's HTML after its scripts ran, with scripts and styles
	// removed and cut at 256 KiB. It is how the page is put together.
	DOM string `json:"dom"`
	// Cut is true when the DOM was cut at that size.
	Cut bool `json:"cut,omitempty"`
	// Markdown is the page's text, as read_page reads it.
	Markdown string `json:"markdown"`
	// Links are the page's anchors, internal ones first, at most 100.
	Links []renderLink `json:"links,omitempty"`
	// Metadata is whatever the document said about itself, as read_page carries it.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// renderLink is one anchor of the drawn page.
type renderLink struct {
	// Href is where it points, as the page wrote it.
	Href string `json:"href"`
	// Text is what it says.
	Text string `json:"text,omitempty"`
}

// draw answers one URL as a browser draws it: its screenshot, its DOM, its text
// and its links.
//
// It is the second half of read_page. read_page fetches and answers markdown,
// escalating to a browser only for a page too thin to read; this always draws,
// and costs a render each time (CRAWL_FEE_CENTS), debited only when the browser
// answered with a picture.
//
// A PAGE THAT COULD NOT BE DRAWN IS A NORMAL ANSWER, as it is there. A refused
// address, an unreachable host, a browser that could not draw it and a caller who
// cannot cover the render all answer 200 with `success:false` and the reason in
// `error`. Non-2xx is reserved for a caller problem: 400 with the same body when
// there is no url, 401 when the caller has no validated principal. Check
// `success` before reading `data`.
//
// The URL is caller-supplied and reached from INSIDE the cluster, so the
// protections are read_page's: only http and https, every address the guarded
// client dials public unicast and every redirect hop judged, the first host judged
// again before the browser is asked, and the browser's pod bounded by its egress
// policy. Nothing is kept: a drawn page is not archived.
func draw(ctx context.Context, in *renderRequest) (*renderResult, error) {
	if _, err := scopeOf(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.URL) == "" {
		return &renderResult{Error: missingURL}, nil
	}
	r, err := Render(ctx, in.URL)
	if err != nil {
		return &renderResult{Error: err.Error()}, nil
	}
	meta, err := json.Marshal(r.Metadata)
	if err != nil {
		return nil, zip.ErrInternal("crawl: the page's metadata will not encode")
	}
	doc := &renderDocument{
		URL:        r.URL,
		Title:      r.Title,
		Screenshot: r.Screenshot,
		DOM:        r.DOM,
		Cut:        r.Cut,
		Markdown:   r.Markdown,
		Metadata:   meta,
	}
	for _, l := range r.Links {
		doc.Links = append(doc.Links, renderLink{Href: l.Href, Text: l.Text})
	}
	return &renderResult{Success: true, Data: doc}, nil
}

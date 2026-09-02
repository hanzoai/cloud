package projects

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// A project's picture is a PICTURE OF THE PROJECT.
//
// The card that lists these sites needs something to show, and the honest
// something is the site itself. It used to draw a tinted arrangement of
// rectangles derived from the slug — a placeholder that looks like a design and
// is not one, which reads as a mockup somebody invented rather than as the thing
// it is standing in for. A drawing that varies by slug is still a drawing.
//
// So this screenshots the live URL, through Hanzo Crawl (headless Chromium,
// already in-cluster and already the renderer websearch falls back to). One
// browser in the fleet, not a second one bolted onto this service.
//
// WHAT IT WILL NOT DO IS INVENT ONE. A site that has never deployed, a capture
// that fails, a crawl service that is down — every one of them answers 404, and
// the card renders no picture at all. An empty frame says "there is nothing to
// show yet", which is true; a generated one says "this is what it looks like",
// which is not.

// shotTTL is how long a capture stands. A deployed site changes when someone
// deploys it, which is not often, and a screenshot is decoration on a list —
// paying for a headless page load per card per view would make opening the list
// cost more than the list is worth.
const shotTTL = 6 * time.Hour

// shotTimeout bounds one capture. Crawl renders a real page; a slow site must
// not hold the request open behind it.
const shotTimeout = 20 * time.Second

// shotViewport is what the page is rendered at — a desktop window, because that
// is what these sites are designed for and a phone-width capture of a desktop
// layout is a picture of a different site.
const (
	shotWidth  = 1280
	shotHeight = 800
)

type shot struct {
	png []byte
	at  time.Time
}

// shots is the in-process cache. Deliberately in-process and deliberately
// small-minded: a screenshot is regenerable from the live site at any moment, so
// losing the cache on a restart costs one page load and nothing else. Putting it
// in the object store would mean inventing an invalidation story for a value
// that already expires on a clock.
var (
	shotsMu sync.Mutex
	shots   = map[string]shot{}
)

func cachedShot(key string) ([]byte, bool) {
	shotsMu.Lock()
	defer shotsMu.Unlock()
	s, ok := shots[key]
	if !ok || time.Since(s.at) > shotTTL {
		return nil, false
	}
	return s.png, true
}

func cacheShot(key string, png []byte) {
	shotsMu.Lock()
	defer shotsMu.Unlock()
	// A cache with no ceiling is a leak with a schedule. This one holds a
	// screenshot per project per deployment; a deployment that keeps rolling
	// would otherwise grow it forever.
	if len(shots) > 512 {
		shots = map[string]shot{}
	}
	shots[key] = shot{png: png, at: time.Now()}
}

func crawlURL() string {
	if v := environ.Or("CRAWL_URL", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://crawl.hanzo.svc:11235"
}

var shotClient = &http.Client{Timeout: shotTimeout}

// capture asks Crawl for one PNG of a URL. It returns nil when the service
// cannot answer — the caller turns that into a 404, never into a placeholder.
func capture(ctx context.Context, url string) []byte {
	body, err := json.Marshal(map[string]any{
		"urls": []string{url},
		"crawler_config": map[string]any{
			"screenshot": true,
			// The page is a real site with real fonts and images; give it a
			// moment to settle so the capture is not a half-painted frame.
			"wait_until":          "networkidle",
			"screenshot_wait_for": 1.0,
			"viewport_width":      shotWidth,
			"viewport_height":     shotHeight,
		},
	})
	if err != nil {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, crawlURL()+"/crawl", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	// The same KMS-sourced token every other reader of this service sends — one
	// service, one credential, read from one env (see apps/websearch/render.go).
	if tok := environ.Or("CRAWL_API_TOKEN", ""); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := shotClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil
	}
	// Bounded: a screenshot is a few hundred KB, and this is a base64 payload
	// from a service that could be having a bad day.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if err != nil {
		return nil
	}

	var out struct {
		Results []struct {
			Screenshot string `json:"screenshot"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Results) == 0 {
		return nil
	}
	png, err := base64.StdEncoding.DecodeString(out.Results[0].Screenshot)
	if err != nil || len(png) == 0 {
		return nil
	}
	return png
}

// shotOf serves GET /v1/projects/:slug/shot — a PNG of the live site, or 404.
//
// It is UNTYPED because it answers image bytes: a typed op declares one JSON Out
// and would have to describe a PNG as one, which is a schema that lies about what
// comes back.
func shotOf(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	p, err := loadProject(s, c.Context(), org, slugParam(c))
	if err != nil {
		return err
	}

	// Nothing deployed means nothing to photograph. That is a 404 about the
	// PICTURE, not about the project — the project is right there in the list.
	live := siteURL(s, org, p.Slug)
	if p.Status == "" || live == "" {
		return zip.ErrNotFound("no live site to capture")
	}

	// Keyed by the deployment, so a redeploy invalidates by construction rather
	// than by anyone remembering to clear anything.
	key := org + "/" + p.Slug + "@" + p.CurrentDeploy
	if png, ok := cachedShot(key); ok {
		return sendPNG(c, png)
	}

	ctx, cancel := context.WithTimeout(c.Context(), shotTimeout)
	defer cancel()
	png := capture(ctx, live)
	if png == nil {
		return zip.ErrNotFound("no capture available")
	}
	cacheShot(key, png)
	return sendPNG(c, png)
}

func sendPNG(c *zip.Ctx, png []byte) error {
	c.SetHeader("Content-Type", "image/png")
	// The browser may hold it for an hour. Shorter than shotTTL on purpose: the
	// server's copy is the one that decides freshness, and a client cache that
	// outlived it would show a picture the server had already replaced.
	c.SetHeader("Cache-Control", "private, max-age=3600")
	c.Status(http.StatusOK)
	return c.SendStream(bytes.NewReader(png))
}

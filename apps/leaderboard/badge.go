// badge.go — THE PUBLIC LEADERBOARD RANK BADGE.
//
//	GET /v1/leaderboard/badge/:handle    public: <handle>.svg, ?theme=dark|light&style=flat|card&fields=&label=
//
// Renders a listed org's standing rank and points as an SVG emblem for GitHub READMEs
// (reusing github.com/hanzoai/reward/emblem so profile and rank badges share one visual
// grammar, option parser, escaping and ETag format).
//
// PRIVACY. Only orgs that opted into public listing (PUT /v1/leaderboard/optin or
// PUT /v1/leaderboard/optin/org) resolve. An unlisted org, a withdrawn org, a held org
// and an unknown handle all answer the same 404.
//
// CACHING. Each rendered SVG is cached in-process for one hour (bounded LRU/TTL, serving
// stale on transient warehouse blips) and served with hourly browser/CDN caching plus
// 24h stale-while-revalidate and ETag/304 validation, so README traffic never recomputes
// badges continuously. Opt-in changes clear the cache immediately.

package leaderboard

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/reward/emblem"
	"github.com/zap-proto/zip"
)

const (
	// badgeTTL is how long a rendered leaderboard badge SVG is reused in-process
	// before recomputing from the standing line.
	badgeTTL = time.Hour

	// badgeCacheMax caps distinct rendered badge entries held in memory so
	// arbitrary query/handle combinations cannot grow memory without bound.
	badgeCacheMax = 4096

	// badgeCacheControl lets browsers and CDN edges cache each badge for an hour
	// and serve stale for a day while revalidating in the background.
	badgeCacheControl = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"
)

func init() {
	spec.Register("/v1/leaderboard/badge/:handle", http.MethodGet, nil, spec.Bytes{Type: "image/svg+xml"})
	spec.Describe("/v1/leaderboard/badge/:handle", http.MethodGet,
		"The README badge of a listed org's leaderboard rank, as an SVG",
		"Public once the org opts into the public leaderboard (PUT /v1/leaderboard/optin or PUT /v1/leaderboard/optin/org). "+
			"Name the handle or org slug with the .svg extension. theme=dark|light, style=flat|card, fields= and label= (at most 24 characters) choose the look. "+
			"Cached hourly in-process and at the edge (stale-while-revalidate 24h) and answers 304 to a matching ETag; every word on it is escaped. "+
			"An unlisted org and an unknown handle are the same 404. No credential is needed.")
	spec.Open("/v1/leaderboard/badge/:handle", http.MethodGet)
}

type badgeEntry struct {
	svg  []byte
	etag string
	at   time.Time
}

type badgeCache struct {
	mu      sync.Mutex
	entries map[string]badgeEntry
}

var badges badgeCache

func (c *badgeCache) get(key string) (badgeEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return badgeEntry{}, false
	}
	e, ok := c.entries[key]
	return e, ok
}

func (c *badgeCache) set(key string, svg []byte, etag string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]badgeEntry)
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= badgeCacheMax {
		// Evict expired entries first; if still full, drop the oldest entry.
		for k, v := range c.entries {
			if at.Sub(v.at) >= badgeTTL {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= badgeCacheMax {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range c.entries {
				if oldestKey == "" || v.at.Before(oldestAt) {
					oldestKey, oldestAt = k, v.at
				}
			}
			if oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
	}
	c.entries[key] = badgeEntry{svg: svg, etag: etag, at: at}
}

func (c *badgeCache) del(key string) {
	c.mu.Lock()
	if c.entries != nil {
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

func (c *badgeCache) reset() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// badge serves GET /v1/leaderboard/badge/:handle.
func badge(s *cloud.Service[state], c *zip.Ctx) error {
	handle, ok := strings.CutSuffix(c.Param("handle"), ".svg")
	handle = strings.TrimSpace(handle)
	if !ok || handle == "" {
		return badgeErr(c, http.StatusNotFound, "not found")
	}

	// Validate options before handle lookup so an invalid option is 400 for every
	// handle and reveals nothing about whether the handle exists.
	q := url.Values{}
	c.Fiber().RequestCtx().QueryArgs().VisitAll(func(k, v []byte) { q.Add(string(k), string(v)) })
	o, err := emblem.Parse(q)
	if err != nil {
		return badgeErr(c, http.StatusBadRequest, err.Error())
	}

	key := strings.ToLower(handle) + "\x00" + o.Theme + "\x00" + o.Style + "\x00" + strings.Join(o.Fields, ",") + "\x00" + o.Label
	now := nowFn()

	cached, haveCached := badges.get(key)
	if haveCached && now.Sub(cached.at) < badgeTTL {
		return writeBadge(c, cached.svg, cached.etag)
	}

	l, err := current(c.Context(), s.State.store)
	if err != nil {
		if haveCached {
			return writeBadge(c, cached.svg, cached.etag)
		}
		s.Log.Warn("leaderboard badge: standing unavailable", "err", err)
		return badgeErr(c, http.StatusServiceUnavailable, "leaderboard unavailable")
	}
	choices, err := s.State.store.Choices(c.Context())
	if err != nil {
		if haveCached {
			return writeBadge(c, cached.svg, cached.etag)
		}
		s.Log.Warn("leaderboard badge: opt-in choices unreadable", "err", err)
		return badgeErr(c, http.StatusServiceUnavailable, "leaderboard unavailable")
	}

	rank, e, display, found := findListed(l, choices, handle)
	if !found {
		if haveCached {
			badges.del(key)
		}
		return badgeErr(c, http.StatusNotFound, "not found")
	}

	card := emblem.Card{
		Handle: display,
		Level:  fmt.Sprintf("#%d", rank),
		Points: int64(e.points),
		Tier:   compactTokens(e.src.Tokens),
	}
	svg := emblem.Render(card, o)
	tag := emblem.ETag(svg)
	badges.set(key, svg, tag, now)
	return writeBadge(c, svg, tag)
}

// findListed resolves handle strictly against orgs that opted into public listing.
// An exact case-insensitive org slug match takes precedence over a display-handle
// match; among display-handle matches, the highest-ranked org wins deterministically.
func findListed(l *line, choices map[string]choice, handle string) (rank int, match entry, display string, ok bool) {
	handleRank := 0
	var handleEntry entry
	handleDisplay := ""

	for i, e := range l.ranked {
		ch, has := choices[e.org]
		if !has || !ch.Listed {
			continue
		}
		name := strings.TrimSpace(ch.Handle)
		if name == "" {
			name = e.org
		}
		if strings.EqualFold(e.org, handle) {
			return i + 1, e, name, true
		}
		if handleRank == 0 && strings.EqualFold(name, handle) {
			handleRank, handleEntry, handleDisplay = i+1, e, name
		}
	}
	if handleRank > 0 {
		return handleRank, handleEntry, handleDisplay, true
	}
	return 0, entry{}, "", false
}

func writeBadge(c *zip.Ctx, svg []byte, etag string) error {
	c.SetHeader("Content-Type", "image/svg+xml; charset=utf-8")
	c.SetHeader("Cache-Control", badgeCacheControl)
	c.SetHeader("ETag", etag)
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.SetHeader("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	if badgeETagMatches(c.Header("If-None-Match"), etag) {
		return c.Bytes(http.StatusNotModified, nil)
	}
	return c.Bytes(http.StatusOK, svg)
}

func badgeErr(c *zip.Ctx, status int, msg string) error {
	return c.JSON(status, map[string]any{"error": map[string]string{"type": "api-error", "message": msg}})
}

func badgeETagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for tag := range strings.SplitSeq(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(tag), "W/") == want {
			return true
		}
	}
	return false
}

func compactTokens(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1_000_000_000_000:
		return fmt.Sprintf("%.1fT tokens", float64(n)/1e12)
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB tokens", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM tokens", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK tokens", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d tokens", n)
	}
}

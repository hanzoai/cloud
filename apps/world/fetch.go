package world

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UPSTREAM FETCHERS — the Go port of the world edge functions:
//   - fetchGDELT ← world/api/gdelt-doc.js  (GDELT 2.0 Doc API, artlist mode)
//   - fetchRSS   ← world/api/rss-proxy.js  (host-allowlisted RSS/Atom proxy)
//
// Both normalize their upstream into NewsItem and cache the result in an
// in-memory TTL map. The cache key is the UPSTREAM identity (feed URL / GDELT
// query), NOT the tenant: an upstream feed's content is a public value identical
// for every org, and tenant NARROWING (the per-project filters) happens AFTER the
// cache read, per-request — so caching globally is both correct and DRY. No
// cross-tenant leak is possible: the cached value is public news; only the
// filtered projection differs per request.

const (
	gdeltDefaultBase = "https://api.gdeltproject.org/api/v2/doc/doc"
	gdeltHost        = "api.gdeltproject.org"
	gdeltMaxRecords  = 20
	gdeltMinQueryLen = 2

	rssMaxBody  = 4 << 20 // 4 MiB cap on a feed body (OOM / abuse guard)
	rssMaxItems = 15      // items taken per feed

	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	maxCacheEntries = 512
)

// ── TTL cache ──────────────────────────────────────────────────────────────

type cacheEntry struct {
	items []NewsItem
	at    time.Time
}

// feedCache is the in-memory, TTL-bounded upstream cache (the Go twin of the
// frontend rss.ts feedCache). Growth is bounded: at the cap, expired entries are
// evicted before a new one is admitted.
type feedCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cacheEntry
}

func newFeedCache(ttl time.Duration) *feedCache {
	return &feedCache{ttl: ttl, m: map[string]cacheEntry{}}
}

func (fc *feedCache) get(key string) ([]NewsItem, bool) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	e, ok := fc.m[key]
	if !ok || time.Since(e.at) > fc.ttl {
		return nil, false
	}
	return e.items, true
}

func (fc *feedCache) put(key string, items []NewsItem) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.m) >= maxCacheEntries {
		now := time.Now()
		for k, e := range fc.m {
			if now.Sub(e.at) > fc.ttl {
				delete(fc.m, k)
			}
		}
	}
	fc.m[key] = cacheEntry{items: items, at: time.Now()}
}

// ── GDELT ──────────────────────────────────────────────────────────────────

type gdeltArticle struct {
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	Domain      string      `json:"domain"`
	SeenDate    string      `json:"seendate"`
	SocialImage string      `json:"socialimage"`
	Language    string      `json:"language"`
	Tone        json.Number `json:"tone"`
}

// fetchGDELT queries the GDELT 2.0 Doc API (artlist mode, date-sorted) for one
// keyword and normalizes each article to a NewsItem. Cached by (timespan, query).
func (s *service) fetchGDELT(ctx context.Context, query, timespan string, max int) ([]NewsItem, error) {
	query = strings.TrimSpace(query)
	if len(query) < gdeltMinQueryLen {
		return nil, fmt.Errorf("gdelt: query too short")
	}
	if max <= 0 || max > gdeltMaxRecords {
		max = gdeltMaxRecords
	}
	if timespan == "" {
		timespan = "72h"
	}
	key := "gdelt:" + timespan + ":" + query
	if items, ok := s.cache.get(key); ok {
		return items, nil
	}

	u, err := url.Parse(s.gdeltBase)
	if err != nil {
		return nil, fmt.Errorf("gdelt: base url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("mode", "artlist")
	q.Set("sort", "date")
	q.Set("format", "json")
	q.Set("timespan", timespan)
	q.Set("maxrecords", strconv.Itoa(max))
	u.RawQuery = q.Encode()

	body, err := s.httpGet(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("gdelt: %w", err)
	}
	var payload struct {
		Articles []gdeltArticle `json:"articles"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("gdelt: decode: %w", err)
	}
	items := make([]NewsItem, 0, len(payload.Articles))
	for _, a := range payload.Articles {
		if a.URL == "" || a.Title == "" {
			continue
		}
		items = append(items, NewsItem{
			Source:  a.Domain,
			Title:   cleanText(a.Title),
			Link:    a.URL,
			PubDate: normalizeGDELTDate(a.SeenDate),
			Lang:    a.Language,
			Image:   a.SocialImage,
			Tone:    strings.TrimSpace(a.Tone.String()),
		})
	}
	s.cache.put(key, items)
	return items, nil
}

// normalizeGDELTDate converts GDELT's compact "20060102T150405Z" seendate into an
// RFC3339 UTC string. An unparseable value yields "" (sorts to the bottom).
func normalizeGDELTDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// ── RSS / Atom ─────────────────────────────────────────────────────────────

// fetchRSS fetches one allowlisted RSS/Atom feed and normalizes its items. The
// host allowlist is the SSRF guard (ported from rss-proxy.js): a non-allowlisted
// host — including a redirect target (enforced by the client CheckRedirect) — is
// refused before any request leaves the pod. Cached by feed URL.
func (s *service) fetchRSS(ctx context.Context, feedURL string) ([]NewsItem, error) {
	feedURL = strings.TrimSpace(feedURL)
	u, err := url.Parse(feedURL)
	if err != nil {
		return nil, fmt.Errorf("rss: parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("rss: scheme %q not allowed", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if !s.rssAllowed(host) {
		return nil, fmt.Errorf("rss: host %q not allowlisted", host)
	}
	key := "rss:" + feedURL
	if items, ok := s.cache.get(key); ok {
		return items, nil
	}

	body, err := s.httpGet(ctx, feedURL, map[string]string{
		"User-Agent":      browserUA,
		"Accept":          "application/rss+xml, application/xml, text/xml, */*",
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		return nil, fmt.Errorf("rss: %w", err)
	}
	items, err := parseFeed(body)
	if err != nil {
		return nil, fmt.Errorf("rss: parse %s: %w", host, err)
	}
	if len(items) > rssMaxItems {
		items = items[:rssMaxItems]
	}
	for i := range items {
		if items[i].Source == "" {
			items[i].Source = host
		}
	}
	s.cache.put(key, items)
	return items, nil
}

func (s *service) rssAllowed(host string) bool {
	_, ok := s.rssAllow[host]
	return ok
}

// rssDoc / atomDoc capture the RSS 2.0 and Atom shapes the world feeds emit. The
// XMLName field is left UNconstrained (no `xml:"rss"` tag) so encoding/xml never
// errors on a root-name mismatch — parseFeed selects the shape by inspecting the
// decoded root local name.
type rssDoc struct {
	XMLName xml.Name
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
}

type atomDoc struct {
	XMLName xml.Name
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// parseFeed decodes an RSS 2.0 or Atom document into NewsItems. RSS is tried
// first (the common case); Atom is the fallback.
func parseFeed(body []byte) ([]NewsItem, error) {
	var rss rssDoc
	if err := xml.Unmarshal(body, &rss); err == nil &&
		(strings.EqualFold(rss.XMLName.Local, "rss") || len(rss.Channel.Items) > 0) {
		items := make([]NewsItem, 0, len(rss.Channel.Items))
		for _, it := range rss.Channel.Items {
			title := cleanText(it.Title)
			link := strings.TrimSpace(it.Link)
			if title == "" && link == "" {
				continue
			}
			items = append(items, NewsItem{
				Source:  cleanText(rss.Channel.Title),
				Title:   title,
				Link:    link,
				PubDate: normalizeRSSDate(it.PubDate),
			})
		}
		return items, nil
	}

	var atom atomDoc
	if err := xml.Unmarshal(body, &atom); err == nil &&
		(strings.EqualFold(atom.XMLName.Local, "feed") || len(atom.Entries) > 0) {
		items := make([]NewsItem, 0, len(atom.Entries))
		for _, e := range atom.Entries {
			title := cleanText(e.Title)
			link := pickAtomLink(e.Links)
			if title == "" && link == "" {
				continue
			}
			pub := e.Published
			if pub == "" {
				pub = e.Updated
			}
			items = append(items, NewsItem{
				Source:  cleanText(atom.Title),
				Title:   title,
				Link:    link,
				PubDate: normalizeRSSDate(pub),
			})
		}
		return items, nil
	}

	return nil, fmt.Errorf("unrecognized feed format")
}

func pickAtomLink(links []atomLink) string {
	for _, l := range links {
		if (l.Rel == "alternate" || l.Rel == "") && l.Href != "" {
			return strings.TrimSpace(l.Href)
		}
	}
	for _, l := range links {
		if l.Href != "" {
			return strings.TrimSpace(l.Href)
		}
	}
	return ""
}

// rssDateLayouts covers the RFC-822/1123 variants RSS emits plus the RFC-3339
// forms Atom emits. An unparseable date yields "" (sorts to the bottom) rather
// than a fabricated "now" that would falsely rank a dateless item as freshest.
var rssDateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	time.RFC822Z,
	time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05",
}

func normalizeRSSDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range rssDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// cleanText trims and collapses interior whitespace runs to single spaces.
func cleanText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// ── HTTP ───────────────────────────────────────────────────────────────────

// httpGet performs a bounded GET against an already-validated URL: the body read
// is capped at rssMaxBody and a non-2xx status is an error. Redirect targets are
// re-validated by the client's CheckRedirect (set in Mount) so a redirect can
// never escape the host allowlist.
func (s *service) httpGet(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, rssMaxBody))
}

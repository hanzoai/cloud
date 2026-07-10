package world

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	maxNewsItems     = 50
	defaultGDELTSpan = "72h"
	fetchConcurrency = 8
	newsFetchTimeout = 20 * time.Second

	maxFeeds       = 64
	maxFilterTerms = 64
)

// NewsItem is the normalized, source-agnostic shape every upstream (GDELT, RSS,
// Atom) is projected into and the wire contract for GET /v1/world/news.
type NewsItem struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	Link    string `json:"link"`
	PubDate string `json:"pubDate"` // RFC3339 UTC, or "" when the upstream gave no parseable date
	Lang    string `json:"lang,omitempty"`
	Image   string `json:"image,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

type newsResponse struct {
	Items []NewsItem `json:"items"`
}

// defaultPipeline is the (org,project) fallback when none is configured: a few
// reputable world-news feeds and NO keyword narrowing, so a project with no
// pipeline still gets a live, sensible feed. Every host is allowlisted.
func defaultPipeline() Pipeline {
	return Pipeline{
		Feeds: []string{
			"https://feeds.bbci.co.uk/news/world/rss.xml",
			"https://feeds.npr.org/1001/rss.xml",
			"https://www.theguardian.com/world/rss",
		},
	}
}

// scope resolves the (org, project) tenant tuple for a request. The org gates on
// a VALIDATED principal (principal.Org → 403 otherwise); the project is the
// org sub-scope. A ?project query, when present, MUST equal the authoritative
// project claim, else 400 — a client cannot widen its own scope via the query.
func scope(c *zip.Ctx) (org, project string, err error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	project = principal.Project(c)
	if q := strings.TrimSpace(c.Query("project")); q != "" && q != project {
		return "", "", zip.ErrBadRequest("project query does not match the authenticated project scope")
	}
	return org, project, nil
}

// getNews serves the merged, filtered, freshest-first news feed for the caller's
// (org, project). It loads the pipeline (or the default), fans out to GDELT (per
// keyword) + RSS (per feed) concurrently, applies the project filters, dedupes,
// sorts by recency, caps the result, and publishes a live refresh to the SSE bus.
func (s *service) getNews(c *zip.Ctx) error {
	org, project, err := scope(c)
	if err != nil {
		return err
	}
	pipe, gerr := s.store.Get(c.Context(), org, project)
	if errors.Is(gerr, errNotFound) {
		pipe = defaultPipeline()
	} else if gerr != nil {
		return zip.Errorf(http.StatusInternalServerError, "load pipeline: %v", gerr)
	}

	ctx, cancel := context.WithTimeout(c.Context(), newsFetchTimeout)
	defer cancel()

	items := s.collect(ctx, pipe)
	items = applyFilters(items, pipe.Filters)
	items = dedupeByLink(items)
	sortByPubDateDesc(items)
	if len(items) > maxNewsItems {
		items = items[:maxNewsItems]
	}
	if items == nil {
		items = []NewsItem{}
	}

	if s.bus != nil {
		s.bus.publish(streamUpdate{Org: org, Project: project, Items: items})
	}
	return c.JSON(http.StatusOK, newsResponse{Items: items})
}

// collect fans out to every upstream (one GDELT query per keyword, one fetchRSS
// per feed) concurrently, bounded by fetchConcurrency. A source failure is logged
// and skipped — the feed degrades to honest partial results, never a 5xx.
func (s *service) collect(ctx context.Context, pipe Pipeline) []NewsItem {
	type job struct {
		gdelt bool
		arg   string
	}
	var jobs []job
	for _, kw := range pipe.Filters.Keywords {
		if kw = strings.TrimSpace(kw); len(kw) >= gdeltMinQueryLen {
			jobs = append(jobs, job{gdelt: true, arg: kw})
		}
	}
	for _, f := range pipe.Feeds {
		if f = strings.TrimSpace(f); f != "" {
			jobs = append(jobs, job{gdelt: false, arg: f})
		}
	}
	if len(jobs) == 0 {
		return nil
	}

	var (
		mu  sync.Mutex
		out []NewsItem
		wg  sync.WaitGroup
		sem = make(chan struct{}, fetchConcurrency)
	)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			var (
				items []NewsItem
				err   error
			)
			if j.gdelt {
				items, err = s.fetchGDELT(ctx, j.arg, defaultGDELTSpan, gdeltMaxRecords)
			} else {
				items, err = s.fetchRSS(ctx, j.arg)
			}
			if err != nil {
				s.log.Debug("world: source fetch failed", "gdelt", j.gdelt, "arg", j.arg, "err", err)
				return
			}
			mu.Lock()
			out = append(out, items...)
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	return out
}

// applyFilters narrows items by the project Filters. Each non-empty axis is an
// AND predicate; within an axis the terms are OR'd (case-insensitive substring).
// Keywords/regions match the title; sources match the item source.
func applyFilters(items []NewsItem, f Filters) []NewsItem {
	kw := lowerNonEmpty(f.Keywords)
	regions := lowerNonEmpty(f.Regions)
	sources := lowerNonEmpty(f.Sources)
	if len(kw) == 0 && len(regions) == 0 && len(sources) == 0 {
		return items
	}
	var out []NewsItem
	for _, it := range items {
		title := strings.ToLower(it.Title)
		if len(kw) > 0 && !containsAny(title, kw) {
			continue
		}
		if len(regions) > 0 && !containsAny(title, regions) {
			continue
		}
		if len(sources) > 0 && !containsAny(strings.ToLower(it.Source), sources) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// dedupeByLink drops duplicate items (same feed can surface via GDELT + RSS),
// keyed by link (falling back to title when a link is absent).
func dedupeByLink(items []NewsItem) []NewsItem {
	seen := make(map[string]struct{}, len(items))
	out := make([]NewsItem, 0, len(items))
	for _, it := range items {
		k := it.Link
		if k == "" {
			k = it.Title
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, it)
	}
	return out
}

// sortByPubDateDesc orders items freshest-first. PubDate is RFC3339 UTC (or ""),
// so a lexical descending compare is a chronological descending sort; "" sorts last.
func sortByPubDateDesc(items []NewsItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].PubDate > items[j].PubDate
	})
}

// ── pipeline handlers ──────────────────────────────────────────────────────

type pipelineView struct {
	Org       string   `json:"org"`
	Project   string   `json:"project"`
	Feeds     []string `json:"feeds"`
	Filters   Filters  `json:"filters"`
	Default   bool     `json:"default"` // true when no stored pipeline (default feeds returned)
	CreatedAt string   `json:"createdAt,omitempty"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

func (s *service) getPipeline(c *zip.Ctx) error {
	org, project, err := scope(c)
	if err != nil {
		return err
	}
	pipe, gerr := s.store.Get(c.Context(), org, project)
	if errors.Is(gerr, errNotFound) {
		return c.JSON(http.StatusOK, toPipelineView(org, project, defaultPipeline(), true))
	}
	if gerr != nil {
		return zip.Errorf(http.StatusInternalServerError, "load pipeline: %v", gerr)
	}
	return c.JSON(http.StatusOK, toPipelineView(org, project, pipe, false))
}

type pipelineReq struct {
	Feeds   []string `json:"feeds"`
	Filters Filters  `json:"filters"`
}

func (s *service) putPipeline(c *zip.Ctx) error {
	org, project, err := scope(c)
	if err != nil {
		return err
	}
	var body pipelineReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	feeds, err := s.sanitizeFeeds(body.Feeds)
	if err != nil {
		return err
	}
	pipe, perr := s.store.Put(c.Context(), Pipeline{
		Org:       org,
		Project:   project,
		Feeds:     feeds,
		Filters:   sanitizeFilters(body.Filters),
		UpdatedAt: time.Now().Unix(),
	})
	if perr != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist pipeline: %v", perr)
	}
	return c.JSON(http.StatusOK, toPipelineView(org, project, pipe, false))
}

// sanitizeFeeds validates the requested feed URLs at the WRITE boundary: each
// must be an http(s) URL whose host is allowlisted (the SSRF guard applied once,
// up front, so a stored pipeline can never carry an un-fetchable/hostile feed).
func (s *service) sanitizeFeeds(feeds []string) ([]string, error) {
	if len(feeds) > maxFeeds {
		return nil, zip.ErrBadRequest("too many feeds (max 64)")
	}
	out := make([]string, 0, len(feeds))
	seen := map[string]struct{}{}
	for _, raw := range feeds {
		f := strings.TrimSpace(raw)
		if f == "" {
			continue
		}
		u, err := url.Parse(f)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, zip.ErrBadRequest("feed must be an http(s) URL: " + f)
		}
		host := strings.ToLower(u.Hostname())
		if !s.rssAllowed(host) {
			return nil, zip.ErrBadRequest("feed host not allowlisted: " + host)
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out, nil
}

func sanitizeFilters(f Filters) Filters {
	return Filters{
		Regions:  cleanTerms(f.Regions),
		Keywords: cleanTerms(f.Keywords),
		Sources:  cleanTerms(f.Sources),
	}
}

func cleanTerms(xs []string) []string {
	out := make([]string, 0, len(xs))
	seen := map[string]struct{}{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if len(out) >= maxFilterTerms {
			break
		}
		key := strings.ToLower(x)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, x)
	}
	return out
}

func toPipelineView(org, project string, p Pipeline, isDefault bool) pipelineView {
	return pipelineView{
		Org:     org,
		Project: project,
		Feeds:   orEmptySlice(p.Feeds),
		Filters: Filters{
			Regions:  orEmptySlice(p.Filters.Regions),
			Keywords: orEmptySlice(p.Filters.Keywords),
			Sources:  orEmptySlice(p.Filters.Sources),
		},
		Default:   isDefault,
		CreatedAt: rfc3339(p.CreatedAt),
		UpdatedAt: rfc3339(p.UpdatedAt),
	}
}

// ── shared helpers ─────────────────────────────────────────────────────────

func lowerNonEmpty(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func containsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

func orEmptySlice(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

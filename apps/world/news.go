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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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
	// Source names the upstream the item came from: the RSS channel title, or
	// the GDELT article's domain.
	Source string `json:"source"`
	// Title is the headline; it is also the text keyword and region filters match on.
	Title string `json:"title"`
	// Link is the article URL.
	Link string `json:"link"`
	// PubDate is RFC3339 UTC, or "" when the upstream gave no parseable date.
	// Items sort freshest-first and a dateless item sorts last.
	PubDate string `json:"pubDate"`
	// Lang is the upstream's language name, when it named one.
	Lang string `json:"lang,omitempty"`
	// Image is a lead-image URL, when the upstream carried one.
	Image string `json:"image,omitempty"`
	// Tone is GDELT's sentiment score as text; absent for RSS/Atom items.
	Tone string `json:"tone,omitempty"`
}

type newsResponse struct {
	// Items is the merged, filtered, deduped feed, freshest first, capped at 50.
	// Empty (never null) when no upstream answered.
	Items []NewsItem `json:"items"`
}

// scopeRef is the input of a read that takes nothing but its tenant scope. The
// (org, project) pair is resolved SERVER-SIDE from the validated principal;
// Project here only ASSERTS the caller's own scope and can never widen it.
type scopeRef struct {
	// Project, when given, must equal the caller's authenticated project scope;
	// a mismatch is refused. Omit it to read the authenticated project.
	Project string `json:"project"`
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
// org sub-scope. `requested`, when non-empty, MUST equal the authoritative
// project claim, else 400 — a client cannot widen its own scope by asking.
func scope(c *zip.Ctx, requested string) (org, project string, err error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	project = principal.Project(c)
	if q := strings.TrimSpace(requested); q != "" && q != project {
		return "", "", zip.ErrBadRequest("project query does not match the authenticated project scope")
	}
	return org, project, nil
}

// scopeOf is scope for a TYPED op, which receives a context and its decoded In
// and never the request. Off the HTTP path there is no request and so no
// validated principal, and the op refuses.
func scopeOf(ctx context.Context, requested string) (org, project string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	return scope(c, requested)
}

// getNews serves the merged, filtered, freshest-first news feed for the caller's
// (org, project). It loads the pipeline (or the default), fans out to GDELT (per
// keyword) + RSS (per feed) concurrently, applies the project filters, dedupes,
// sorts by recency, caps the result at 50, and publishes a live refresh to the
// SSE stream. A source that fails is skipped, so the feed degrades to honest
// partial results rather than an error.
//
// Response: {"items": [{"source": "BBC News", "title": "Border conflict escalates", "link": "https://example.com/a", "pubDate": "2026-07-07T12:00:00Z"}]}
func (s *service) getNews(ctx context.Context, in *scopeRef) (*newsResponse, error) {
	org, project, err := scopeOf(ctx, in.Project)
	if err != nil {
		return nil, err
	}
	pipe, gerr := s.store.Get(ctx, org, project)
	if errors.Is(gerr, errNotFound) {
		pipe = defaultPipeline()
	} else if gerr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load pipeline: %v", gerr)
	}

	fetch, cancel := context.WithTimeout(ctx, newsFetchTimeout)
	defer cancel()

	items := s.collect(fetch, pipe)
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
	return &newsResponse{Items: items}, nil
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
	// Org and Project are the tenant scope the pipeline belongs to, echoed from
	// the validated principal — never from the request.
	Org     string `json:"org"`
	Project string `json:"project"`
	// Feeds are the RSS/Atom feed URLs this project pulls, all allowlisted hosts.
	Feeds []string `json:"feeds"`
	// Filters are the keyword/region/source narrowings applied to the merged feed.
	Filters Filters `json:"filters"`
	// Default is true when no pipeline is stored and these are the built-in feeds.
	Default bool `json:"default"`
	// CreatedAt and UpdatedAt are RFC3339 UTC, absent on a default pipeline.
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// getPipeline reads the feed + filter configuration for the caller's (org,
// project). A project that has saved none gets the built-in world-news feeds
// with default:true, so a first read is never empty.
//
// Response: {"org": "acme", "project": "default", "feeds": ["https://feeds.bbci.co.uk/news/world/rss.xml"], "filters": {"regions": [], "keywords": [], "sources": []}, "default": true}
func (s *service) getPipeline(ctx context.Context, in *scopeRef) (*pipelineView, error) {
	org, project, err := scopeOf(ctx, in.Project)
	if err != nil {
		return nil, err
	}
	pipe, gerr := s.store.Get(ctx, org, project)
	if errors.Is(gerr, errNotFound) {
		v := toPipelineView(org, project, defaultPipeline(), true)
		return &v, nil
	}
	if gerr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load pipeline: %v", gerr)
	}
	v := toPipelineView(org, project, pipe, false)
	return &v, nil
}

type pipelineReq struct {
	// Feeds are the RSS/Atom feed URLs to pull. Each must be an http(s) URL whose
	// host is on the server's allowlist; max 64. An empty list clears the feeds.
	Feeds []string `json:"feeds"`
	// Filters narrow the merged feed. Each axis is AND'd, terms within an axis are
	// OR'd, matching is case-insensitive substring; max 64 terms per axis.
	Filters Filters `json:"filters"`
}

// putPipeline replaces the feed + filter configuration for the caller's (org,
// project). Feed hosts are checked against the allowlist at this write boundary,
// so a stored pipeline can never carry an unfetchable or hostile feed. The
// stored pipeline is returned.
//
// Example: {"feeds": ["https://feeds.bbci.co.uk/news/world/rss.xml"], "filters": {"keywords": ["conflict"]}}
func (s *service) putPipeline(ctx context.Context, in *pipelineReq) (*pipelineView, error) {
	// The optional ?project= cross-check rides the URL, which a method with a body
	// does not declare in the document; it is read here so the guard behaves the
	// same on every route.
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	org, project, err := scope(c, c.Query("project"))
	if err != nil {
		return nil, err
	}
	feeds, err := s.sanitizeFeeds(in.Feeds)
	if err != nil {
		return nil, err
	}
	pipe, perr := s.store.Put(ctx, Pipeline{
		Org:       org,
		Project:   project,
		Feeds:     feeds,
		Filters:   sanitizeFilters(in.Filters),
		UpdatedAt: time.Now().Unix(),
	})
	if perr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist pipeline: %v", perr)
	}
	v := toPipelineView(org, project, pipe, false)
	return &v, nil
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

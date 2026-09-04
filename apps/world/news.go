package world

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/internal/stamp"
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
	// Source is the outlet the item came from, as the upstream named it.
	Source string `json:"source"`
	// Title is the headline.
	Title string `json:"title"`
	// Link is the article's URL at the outlet.
	Link string `json:"link"`
	// PubDate is when the outlet published it, RFC3339 UTC. Empty when the
	// upstream gave no date this could parse — items with no date sort last.
	PubDate string `json:"pubDate"`
	// Lang is the article's language code when the upstream reported one.
	Lang string `json:"lang,omitempty"`
	// Image is a lead-image URL when the upstream carried one.
	Image string `json:"image,omitempty"`
	// Tone is GDELT's own sentiment score for the article, as text. Only GDELT
	// items carry it.
	Tone string `json:"tone,omitempty"`
}

// newsResponse is one read of the merged feed.
type newsResponse struct {
	// Items is the merged, filtered, deduped feed, freshest first and capped at
	// 50. A source that failed is skipped rather than failing the read, so this
	// can be shorter than the pipeline's reach — it is never an error.
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
		return "", "", principal.Refused(c)
	}
	project = principal.Project(c)
	if q := strings.TrimSpace(c.Query("project")); q != "" && q != project {
		return "", "", zip.ErrBadRequest("project query does not match the authenticated project scope")
	}
	return org, project, nil
}

// scopeOf is scope() for a typed op, which receives only a context.
//
// It reaches the REQUEST rather than taking either value from an In field, and both
// halves of that are deliberate. The PROJECT is a claim the identity boundary minted
// (X-Project-Id), which principal.OrgFrom does not carry, and it is a tenant key: an
// In field is caller-supplied, so reading it from one would be a cross-scope read
// the caller asserted for itself. The ?project QUERY is only ever cross-CHECKED
// against that claim, and it cannot be an In field either — zip binds an In field
// from the BODY as well as the URL, so PUT /v1/world/pipeline would start rejecting
// a body that named a project, a wire this route has never had.
//
// Off the HTTP path there is no request and no principal, so it fails closed and
// every op refuses — the handler's own gate, with no second gate to keep in sync.
func scopeOf(ctx context.Context) (org, project string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", principal.RefusedFrom(ctx)
	}
	return scope(c)
}

// newsQuery is the news read's input. It carries nothing: the feed a caller gets is
// entirely the (org, project) pipeline's, resolved server-side.
type newsQuery struct{}

// news returns the caller's merged world-news feed: every source their project's
// pipeline names — GDELT once per keyword, plus each allowlisted RSS or Atom feed —
// fetched concurrently, narrowed by the pipeline's keyword/region/source filters,
// deduplicated by link and sorted freshest first, capped at 50 items.
//
// A project with no stored pipeline gets a sensible default set of world feeds
// rather than an empty answer. A source that fails or times out is SKIPPED: the feed
// degrades to honest partial results and never 5xxs because one outlet was down.
// Reading also publishes the result to the /v1/world/stream subscribers of the same
// (org, project), so a dashboard's own refresh updates every open tab.
func (s *service) news(ctx context.Context, _ *newsQuery) (*newsResponse, error) {
	org, project, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	pipe, gerr := s.store.Get(ctx, org, project)
	if errors.Is(gerr, errNotFound) {
		pipe = defaultPipeline()
	} else if gerr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load pipeline: %v", gerr)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, newsFetchTimeout)
	defer cancel()

	items := s.collect(fetchCtx, pipe)
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

// pipelineView is one project's configured news pipeline.
type pipelineView struct {
	// Org is the tenant the pipeline belongs to, resolved server-side from the
	// validated principal.
	Org string `json:"org"`
	// Project is the org sub-scope the pipeline belongs to.
	Project string `json:"project"`
	// Feeds is the RSS/Atom feed URLs the pipeline reads. Every host is on the
	// server's allowlist — a URL that is not cannot be stored.
	Feeds []string `json:"feeds"`
	// Filters narrows the merged feed.
	Filters Filters `json:"filters"`
	// Default is true when no pipeline is stored for this project and these are
	// the built-in world feeds. Writing one turns it false.
	Default bool `json:"default"`
	// CreatedAt is when the pipeline was first stored, RFC3339 UTC. Absent on the
	// default.
	CreatedAt string `json:"createdAt,omitempty"`
	// UpdatedAt is when it was last written, RFC3339 UTC. Absent on the default.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// pipelineQuery is the pipeline read's input. It carries nothing: which pipeline is
// read is entirely the caller's validated (org, project).
type pipelineQuery struct{}

// pipeline returns the caller project's news pipeline: which feeds it reads and how
// the merged result is filtered. A project that has never written one is answered
// with the built-in world feeds and `default: true`, so a fresh project sees the
// same feed /v1/world/news would actually serve rather than an empty configuration.
func (s *service) pipeline(ctx context.Context, _ *pipelineQuery) (*pipelineView, error) {
	org, project, err := scopeOf(ctx)
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

// pipelineReq replaces the caller project's pipeline WHOLE. It is not a patch: a
// field left out is stored empty, so sending only feeds clears the filters.
type pipelineReq struct {
	// Feeds is the RSS/Atom feed URLs to read, at most 64. Each must be an
	// http(s) URL whose host is on the server's allowlist — the SSRF guard is
	// applied here, at the write, so a stored pipeline can never name a host the
	// fetcher would refuse. Blank entries are dropped and duplicates collapse.
	Feeds []string `json:"feeds"`
	// Filters narrows the merged feed. Terms are trimmed, de-duplicated
	// case-insensitively, and capped at 64 per axis.
	Filters Filters `json:"filters"`
}

// setPipeline replaces the caller project's news pipeline and returns what was
// stored. It is a WHOLE replacement, not a patch: a field the request leaves out is
// stored empty, so sending only feeds clears the filters.
//
// Every feed URL is validated HERE, at the write boundary — http(s) only, and the
// host must be on the server's allowlist — so a stored pipeline can never name a
// host the fetcher would later refuse, and the allowlist is one decision in one
// place rather than a check at each fetch.
func (s *service) setPipeline(ctx context.Context, in *pipelineReq) (*pipelineView, error) {
	org, project, err := scopeOf(ctx)
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
		CreatedAt: stamp.Unix(p.CreatedAt),
		UpdatedAt: stamp.Unix(p.UpdatedAt),
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

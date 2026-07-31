// Package catalog is the CROSS-ORG discovery lens: one place to find every
// project, app and site the fleet has built, whichever org built it.
//
// It owns no store. The corpus lives in the lexical index (clients/index) — the
// same store the Meilisearch dialect serves — so relevance, paging, persistence
// and encryption at rest are the ones the platform already runs. What this
// package adds is the ONE thing the index cannot express on its own: a corpus
// that spans orgs.
//
// # How that stays safe
//
// The index pins every row to an org and every query to one org. Cross-org
// discovery is therefore not a weaker filter — it is a SECOND corpus:
//
//	PublicOrg ("~catalog")   the published, world-readable catalog. Every caller
//	                         reads it. Nobody can write it: an org id is minted
//	                         from a validated IAM owner claim and IAM org slugs
//	                         begin with an alphanumeric, so no principal can ever
//	                         BE "~catalog".
//	the caller's own org     their private projects, read with principal.Org and
//	                         nothing else — never a request field (HIP-0026).
//
// A customer's private project is a row in their own org's `catalog` index. It
// cannot appear in another tenant's results because the query that would return
// it is never run for them. Nothing PUBLISHES over HTTP either: the published
// corpus is reconciled in-process from sources that are public by construction
// (sync.go), so no credential exists that could promote a tenant row into it.
//
// Surface:
//
//	GET /v1/catalog   search + browse: ?q= &org= &kind= &archetype= &language=
//	                  &origin=template|community|third-party|product
//	                  &template=<parent id>   (lineage: what was forked from it)
//	                  &forkable=true|false &official=true|false
//	                  (absent = both; see filter)
//
// origin is the axis the two hanzo.app lanes are cut on — /templates browses
// origin=template, /community browses origin=community — so they are TWO VIEWS
// of this one corpus and not two catalogs that can disagree.
//
// There is no write route. The corpus reconciles itself (sync.go), which is why
// there is no credential that could publish into the published catalog at all.
package catalog

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/projects"
	"github.com/zap-proto/zip"
)

const (
	// PublicOrg owns the published cross-org corpus. The leading "~" is what
	// makes it unforgeable: IAM org slugs start with an alphanumeric, so this is
	// a name SanitizeIdentity can never mint from a bearer token.
	PublicOrg = "~catalog"

	// uid is the index every catalog row lives in, in BOTH corpora — so an org's
	// private entries and the published ones are the same shape read the same way.
	uid = "catalog"

	// pk is the index's primary key: "<org>/<name>", stable across syncs so a
	// re-published entry updates in place instead of accumulating duplicates.
	pk = "id"

	// scan bounds how many rows one request pulls out of the index before
	// faceting. Facet counts must be computed over the whole matching set, not
	// the returned page, or the browse rail would renumber itself as you page.
	scan = 5000

	// maxLimit bounds a page.
	maxLimit = 200
)

// CatalogEntry is one thing the fleet built. The fields are deliberately the four axes
// discovery is done along (org, archetype, language, forkable) plus the two links
// that make a hit actionable (URL to see it, Repo to read it).
//
// The name is the flattened `catalog.Entry`: the OpenAPI component namespace is
// flat across every app, and a bare `Entry` there already means another app's row.
type CatalogEntry struct {
	// ID is "<org>/<name>", the stable key a re-publish updates in place.
	ID string `json:"id"`
	// Org is the account that built it: hanzo | lux | zoo.
	Org string `json:"org"`
	// Name is the project name within its org.
	Name string `json:"name"`
	// Title is the human name, when the source carried one.
	Title string `json:"title,omitempty"`
	// Kind is repo | site.
	Kind string `json:"kind"`
	// Origin is WHAT THIS IS TO YOU: template | community | third-party | product
	// (origin.go owns the four nouns and derives them). Not omitempty, for the
	// same reason Forkable is not: every row has an answer, and a missing one is
	// exactly the flattening this field exists to end.
	Origin string `json:"origin"`
	// Archetype is the shape of the thing (app, site, library, …).
	Archetype string `json:"archetype,omitempty"`
	// Language is the primary language, as the source reported it.
	Language string `json:"language,omitempty"`
	// Description is the one-line summary from the source.
	Description string `json:"description,omitempty"`
	// URL is where it is live, when it is deployed.
	URL string `json:"url,omitempty"`
	// Repo is where the source is read.
	Repo string `json:"repo,omitempty"`
	// Template is the parent entry id this was forked from, when it was.
	Template string `json:"template,omitempty"`
	// Forkable is NOT omitempty: false is an answer here, not a missing field.
	// Omitted, a client could not tell "you cannot fork this" from "nobody said".
	Forkable bool `json:"forkable"`
	// Stars is the upstream star count, when the source reported one.
	Stars int `json:"stars,omitempty"`
	// Updated is when the source last changed, and the key the page sorts on
	// (freshest first).
	Updated string `json:"updated,omitempty"`
	// Upstream/License credit the third-party work an entry was published from:
	// the difference between "this org built it" and "somebody else built it and
	// we are showing it to you".
	//
	// WHO built it is Org, above — the account that paid for the project. There
	// was once a separate admin-gated `official` boolean here claiming the same
	// thing, and because it was gated it disagreed: apps Hanzo wrote and hosts
	// were published by a script holding an ordinary org token, so it stayed
	// false on all of them and this directory filed our own work as somebody
	// else's. A field that restates an unforgeable fact can only ever be the
	// wrong copy of it.
	Upstream string `json:"upstream,omitempty"`
	License  string `json:"license,omitempty"`
	// Scope is provenance, not storage: "public" for a row from the published
	// corpus, "org" for one only this caller can see. A UI that cannot tell them
	// apart cannot warn before sharing a link.
	Scope string `json:"scope"`
	// Note is why a row is NOT in the published catalog, set by the admission gate
	// (gate.go) on the sites it holds back. It is the difference between a demo
	// that silently vanished from the public lens and one whose owner can read the
	// reason and fix it. A published row never carries one.
	Note string `json:"note,omitempty"`
}

// CatalogView is the ONE result shape. Facets ship with every response because
// browse and search are the same request here — a query with no q is a browse.
//
// The name is the flattened `catalog.Response`, for the reason on CatalogEntry.
type CatalogView struct {
	// Data is ONE page of the matching set, freshest first.
	Data []CatalogEntry `json:"data"`
	// Total is how many entries matched, which is the whole set and not this page.
	Total int `json:"total"`
	// Facets counts the WHOLE matching set along every browse axis (org,
	// archetype, language, kind, origin, template, forkable), so a rail renders
	// the choices that actually have results behind them.
	Facets map[string]counts `json:"facets"`
}

type counts map[string]int

type state struct{}

// Mount wires the lens and starts the corpus reconcile. No store, no DataDir:
// the corpus is the index's, and the sync is a goroutine, not an endpoint.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app != nil && cloud.ZipApp(app) == nil {
		return fmt.Errorf("catalog.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	return cloud.Mount(app, deps, "catalog", build, routes)
}

func build(b cloud.Base) (state, error) {
	go loop(b)
	return state{}, nil
}

func routes(app cloud.Router, _ *cloud.Service[state]) {
	// The typed-op bridge FIRST — fiber runs middleware in registration order, so
	// one installed after the leaf would never run, and browse reads the caller's
	// validated org off the context it parks. Bounded to this subsystem's own
	// path. Serve installs one app-wide too; nesting is harmless, and this is what
	// makes the surface testable on a bare app.
	app.Group("/v1/catalog").Use(cloud.Bridge())
	zip.Get(cloud.ZipApp(app), "/v1/catalog", browse)
}

// loop reconciles the corpus on a timer, first pass delayed so a boot never waits
// on the network. Failures are logged and retried at the next tick: a stale
// catalog is a far better answer than an empty one.
func loop(b cloud.Base) {
	for t := time.NewTimer(firstAfter); ; t.Reset(every) {
		<-t.C
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		kept, pruned, err := run(ctx)
		cancel()
		if err != nil {
			b.Log.Warn("catalog sync", "err", err, "published", kept)
			continue
		}
		b.Log.Info("catalog synced", "published", kept, "pruned", pruned)
	}
}

// Query is the whole /v1/catalog request: the free-text q the index scores, the
// exact-match axes the browse rails are cut on, and the page bounds. Every axis
// is optional and an absent one is not a filter.
type Query struct {
	// Q is the free-text query the lexical index scores. Empty browses everything.
	Q string `json:"q"`
	// Org narrows to one publishing org (hanzo | lux | zoo), case-insensitive
	// exact match. It is a browse axis over the PUBLISHED corpus, not a tenant
	// key: which corpora are read is decided by the validated principal alone.
	Org string `json:"org"`
	// Kind narrows to repo or site.
	Kind string `json:"kind"`
	// Origin narrows to one lane: template | community | third-party | product.
	Origin string `json:"origin"`
	// Template narrows to one lineage — everything forked from that parent entry id.
	Template string `json:"template"`
	// Archetype narrows to one archetype.
	Archetype string `json:"archetype"`
	// Language narrows to one language.
	Language string `json:"language"`
	// Forkable is TRI-state: "true" keeps only forkable entries, "false" only the
	// complement, and anything else (including absent) asks nothing.
	Forkable string `json:"forkable"`
	// Limit caps the page; 0 means 50 and nothing above 200 is honoured.
	Limit int `json:"limit"`
	// Offset skips that many rows of the result, which is sorted freshest-first.
	Offset int `json:"offset"`
}

// browse answers search AND browse: it reads the published cross-org corpus plus
// the caller's own private one, narrows both by the exact-match axes, and returns
// one page together with facet counts over the WHOLE matching set. An anonymous
// caller sees only the published corpus. The lexical index does relevance over
// the free-text q; the axes are applied here because they are exact-match
// dimensions, and asking a term index to express "language = Go" as a term match
// would let "go" in a description score as a language.
//
// Example: {"q": "blockchain", "language": "Go", "limit": 20}
// CatalogView: {"data": [{"id": "lux/node", "org": "lux", "name": "node", "kind": "repo", "origin": "product", "language": "Go", "forkable": false, "scope": "public"}], "total": 1, "facets": {"language": {"Go": 1}}}
func browse(ctx context.Context, in *Query) (*CatalogView, error) {
	if !index.Ready() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "catalog: index not mounted")
	}
	q := strings.TrimSpace(in.Q)
	rows, err := read(ctx, PublicOrg, q, "public")
	if err != nil {
		return nil, err
	}
	// The caller's OWN corpus, read with the validated principal and nothing else.
	// Anonymous callers simply get the published one.
	if org, ok := principal.OrgFrom(ctx); ok && org != PublicOrg {
		own, err := read(ctx, org, q, "org")
		if err != nil {
			return nil, err
		}
		rows = append(rows, own...)
	}

	rows = filter(rows, in)
	facets := facet(rows)
	total := len(rows)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Updated > rows[j].Updated })
	return &CatalogView{Data: page(rows, in), Total: total, Facets: facets}, nil
}

// read pulls one corpus out of the index and stamps its scope.
func read(ctx context.Context, org, q, scope string) ([]CatalogEntry, error) {
	raw, err := index.Query(ctx, org, uid, q, scan, 0)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	out := make([]CatalogEntry, 0, len(raw))
	for _, r := range raw {
		var e CatalogEntry
		if json.Unmarshal(r, &e) != nil || e.ID == "" {
			continue
		}
		e.Scope = scope
		out = append(out, e)
	}
	return out, nil
}

// filter applies the exact-match browse axes. An absent param is not a filter.
// Every dimension `facet` counts is filterable here and vice versa: a facet a
// caller can see but cannot act on is a rail that lies about being clickable.
//
// forkable is TRI-state for that reason. Read as `== "true"` it could only ever
// narrow, never select the complement, so `?forkable=false` silently meant "no
// filter" — a boolean axis whose negative case is unaskable is a label, not a
// filter.
func filter(in []CatalogEntry, q *Query) []CatalogEntry {
	org, arch := strings.ToLower(q.Org), strings.ToLower(q.Archetype)
	lang, kind := strings.ToLower(q.Language), strings.ToLower(q.Kind)
	// origin cuts the corpus into the lanes a person actually browses; parent
	// narrows a lane to one lineage ("everything built from folio"), which is what
	// turns the community lane from a pile into something you can read.
	orig, parent := strings.ToLower(q.Origin), strings.ToLower(q.Template)
	fork, forkSet := tribool(q.Forkable)
	out := in[:0]
	for _, e := range in {
		switch {
		case org != "" && strings.ToLower(e.Org) != org,
			arch != "" && strings.ToLower(e.Archetype) != arch,
			lang != "" && strings.ToLower(e.Language) != lang,
			kind != "" && strings.ToLower(e.Kind) != kind,
			orig != "" && strings.ToLower(e.Origin) != orig,
			parent != "" && strings.ToLower(e.Template) != parent,
			forkSet && e.Forkable != fork:
			continue
		}
		out = append(out, e)
	}
	return out
}

// tribool reads a flag that has three answers, not two: yes, no, and unasked.
func tribool(s string) (v, ok bool) {
	b, err := strconv.ParseBool(strings.TrimSpace(s))
	return b, err == nil
}

// facet counts the matching set along every browse axis, so the rail a client
// renders is the rail that actually has results behind it. forkable is counted
// on BOTH sides by the same rule as every other dimension — counting only the
// trues rendered {true: everything} and told a caller there was a choice where
// there was none.
func facet(in []CatalogEntry) map[string]counts {
	f := map[string]counts{"org": {}, "archetype": {}, "language": {}, "kind": {},
		"origin": {}, "template": {}, "forkable": {}}
	for _, e := range in {
		for dim, v := range map[string]string{
			"org": e.Org, "archetype": e.Archetype, "language": e.Language,
			"kind": e.Kind, "origin": e.Origin, "template": e.Template,
			"forkable": strconv.FormatBool(e.Forkable),
		} {
			if v != "" {
				f[dim][v]++
			}
		}
	}
	return f
}

// page cuts one window out of the sorted result. A limit that was never given
// (or was given as something the URL could not carry as a positive number) is 50,
// and 200 is the ceiling; an offset past the end is an empty page, not an error.
func page(in []CatalogEntry, q *Query) []CatalogEntry {
	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = 50
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(in) {
		return []CatalogEntry{}
	}
	if end := offset + limit; end < len(in) {
		return in[offset:end]
	}
	return in[offset:]
}

// The two seams sync writes and reads through, as package vars so the reconcile
// is testable without a live GitHub and the site source without a store.
var (
	reconcile = func(ctx context.Context, org string, rows []CatalogEntry) (int, int, error) {
		docs := make([]map[string]any, 0, len(rows))
		for _, e := range rows {
			if e.ID == "" || e.Org == "" {
				continue // an unkeyed row is one the next swap could never prune
			}
			e.Scope = "" // provenance is stamped on READ; storing it would freeze it
			var doc map[string]any
			raw, _ := json.Marshal(e)
			_ = json.Unmarshal(raw, &doc)
			docs = append(docs, doc)
		}
		return index.Reconcile(ctx, org, uid, pk, docs)
	}
	// The corpus's two sources, one seam each: what we BUILT and what is LIVE.
	fromOrgs  = orgRepos
	liveSites = projects.LiveSites
)

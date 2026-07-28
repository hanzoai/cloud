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
//	PublicOrg ("~catalog")   the published, world-readable catalog. Every
//	                         authenticated caller reads it. Nobody writes it over
//	                         HTTP: an org id is minted from a validated IAM owner
//	                         claim, IAM org slugs begin with an alphanumeric, so
//	                         no principal can ever BE "~catalog".
//	the caller's own org     their private projects, read with principal.Org and
//	                         nothing else — never a request field (HIP-0026).
//
// A customer's private project is a row in their own org's `catalog` index. It
// cannot appear in another tenant's results because the query that would return
// it is never run for them. Publishing to the public corpus is a SuperAdmin
// write (PUT /v1/catalog), so a tenant cannot promote their own row into it.
//
// Surface:
//
//	GET /v1/catalog   search + browse: ?q= &org= &archetype= &language= &forkable=
//	PUT /v1/catalog   replace the published corpus (SuperAdmin) — full swap, prunes
package catalog

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/index"
	"github.com/hanzoai/cloud/clients/principal"
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

// Entry is one thing the fleet built. The fields are deliberately the four axes
// discovery is done along (org, archetype, language, forkable) plus the two links
// that make a hit actionable (URL to see it, Repo to read it).
type Entry struct {
	ID          string `json:"id"`
	Org         string `json:"org"` // hanzo | lux | zoo
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Kind        string `json:"kind"` // repo | site
	Archetype   string `json:"archetype,omitempty"`
	Language    string `json:"language,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`      // live, if it is deployed
	Repo        string `json:"repo,omitempty"`     // source
	Template    string `json:"template,omitempty"` // lineage, if forked from one
	Forkable    bool   `json:"forkable,omitempty"`
	Stars       int    `json:"stars,omitempty"`
	Updated     string `json:"updated,omitempty"`
	// Scope is provenance, not storage: "public" for a row from the published
	// corpus, "org" for one only this caller can see. A UI that cannot tell them
	// apart cannot warn before sharing a link.
	Scope string `json:"scope"`
}

// Response is the ONE result shape. Facets ship with every response because
// browse and search are the same request here — a query with no q is a browse.
type Response struct {
	Data   []Entry           `json:"data"`
	Total  int               `json:"total"`
	Facets map[string]counts `json:"facets"`
}

type counts map[string]int

type state struct{}

// Mount wires the lens. No store, no DataDir: the corpus is the index's.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "catalog", func(cloud.Base) (state, error) { return state{}, nil }, routes)
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/catalog", cloud.Handle(s, browse))
	app.Put("/v1/catalog", cloud.Handle(s, publish))
}

// browse answers search AND browse. The lexical index does relevance over the
// free-text q; the facet filters are applied here because they are exact-match
// dimensions, and asking a term index to express "language = Go" as a term match
// would let "go" in a description score as a language.
func browse(s *cloud.Service[state], c *zip.Ctx) error {
	if !index.Ready() {
		return zip.Errorf(http.StatusServiceUnavailable, "catalog: index not mounted")
	}
	q := strings.TrimSpace(c.Query("q"))
	rows, err := read(c, PublicOrg, q, "public")
	if err != nil {
		return err
	}
	// The caller's OWN corpus, read with the validated principal and nothing else.
	// Anonymous callers simply get the published one.
	if org, ok := principal.Org(c); ok && org != PublicOrg {
		own, err := read(c, org, q, "org")
		if err != nil {
			return err
		}
		rows = append(rows, own...)
	}

	rows = filter(rows, c)
	facets := facet(rows)
	total := len(rows)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Updated > rows[j].Updated })
	return c.JSON(http.StatusOK, Response{Data: page(rows, c), Total: total, Facets: facets})
}

// read pulls one corpus out of the index and stamps its scope.
func read(c *zip.Ctx, org, q, scope string) ([]Entry, error) {
	raw, err := index.Query(c.Context(), org, uid, q, scan, 0)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	out := make([]Entry, 0, len(raw))
	for _, r := range raw {
		var e Entry
		if json.Unmarshal(r, &e) != nil || e.ID == "" {
			continue
		}
		e.Scope = scope
		out = append(out, e)
	}
	return out, nil
}

// filter applies the exact-match browse axes. An absent param is not a filter.
func filter(in []Entry, c *zip.Ctx) []Entry {
	org, arch := strings.ToLower(c.Query("org")), strings.ToLower(c.Query("archetype"))
	lang, fork := strings.ToLower(c.Query("language")), c.Query("forkable") == "true"
	out := in[:0]
	for _, e := range in {
		switch {
		case org != "" && strings.ToLower(e.Org) != org,
			arch != "" && strings.ToLower(e.Archetype) != arch,
			lang != "" && strings.ToLower(e.Language) != lang,
			fork && !e.Forkable:
			continue
		}
		out = append(out, e)
	}
	return out
}

// facet counts the matching set along every browse axis, so the rail a client
// renders is the rail that actually has results behind it.
func facet(in []Entry) map[string]counts {
	f := map[string]counts{"org": {}, "archetype": {}, "language": {}, "kind": {}}
	for _, e := range in {
		for dim, v := range map[string]string{
			"org": e.Org, "archetype": e.Archetype, "language": e.Language, "kind": e.Kind,
		} {
			if v != "" {
				f[dim][v]++
			}
		}
		if e.Forkable {
			f["forkable"] = counts{"true": f["forkable"]["true"] + 1}
		}
	}
	return f
}

func page(in []Entry, c *zip.Ctx) []Entry {
	limit, offset := intQuery(c, "limit", 50), intQuery(c, "offset", 0)
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset >= len(in) {
		return []Entry{}
	}
	if end := offset + limit; end < len(in) {
		return in[offset:end]
	}
	return in[offset:]
}

func intQuery(c *zip.Ctx, name string, def int) int {
	n, err := strconv.Atoi(c.Query(name))
	if err != nil || n < 0 {
		return def
	}
	return n
}

// publish REPLACES the published corpus. SuperAdmin only, and a full swap: the
// catalog mirrors upstream sources (a git forge, the sites table), so a sync must
// converge and a deleted project must leave. An org publishing its OWN private
// entries does not come here — it writes its own org's `catalog` index through
// the Meilisearch dialect, which is already org-scoped.
func publish(s *cloud.Service[state], c *zip.Ctx) error {
	if !principal.IsSuperAdmin(c) {
		return zip.ErrForbidden("catalog: publishing the cross-org catalog is a platform operation")
	}
	if !index.Ready() {
		return zip.Errorf(http.StatusServiceUnavailable, "catalog: index not mounted")
	}
	var in struct {
		Entries []Entry `json:"entries"`
	}
	if err := c.Bind(&in); err != nil {
		return zip.ErrBadRequest("catalog: invalid body")
	}
	docs := make([]map[string]any, 0, len(in.Entries))
	for _, e := range in.Entries {
		if e.ID == "" || e.Org == "" {
			continue // an unkeyed row would be a document the swap can never prune
		}
		e.Scope = ""
		var doc map[string]any
		raw, _ := json.Marshal(e)
		_ = json.Unmarshal(raw, &doc)
		docs = append(docs, doc)
	}
	kept, removed, err := index.Reconcile(c.Context(), PublicOrg, uid, pk, docs)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	s.Log.Info("catalog published", "entries", kept, "pruned", removed)
	return c.JSON(http.StatusOK, map[string]any{"published": kept, "pruned": removed})
}

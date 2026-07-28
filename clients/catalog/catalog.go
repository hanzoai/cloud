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
//	                  &forkable=true|false &official=true|false
//	                  (absent = both; see filter)
//
// There is no write route. The corpus reconciles itself (sync.go), which is why
// there is no credential that could publish into the published catalog at all.
package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/index"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/projects"
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
	// Forkable is NOT omitempty: false is an answer here, not a missing field.
	// Omitted, a client could not tell "you cannot fork this" from "nobody said".
	Forkable bool   `json:"forkable"`
	Stars    int    `json:"stars,omitempty"`
	Updated  string `json:"updated,omitempty"`
	// Official and Upstream/License are AUTHORSHIP. Official is the platform-gated
	// first-party marker (projects.Project.Official, raised only by an admin);
	// Upstream/License credit the third-party work an entry was published from.
	// Together they are the difference between "we built this" and "somebody else
	// built this and we are showing it to you" — which a directory titled with our
	// own three orgs has no business leaving to the reader.
	//
	// Official follows Forkable in NOT being omitempty, for the same reason: false
	// is an answer, and omitted it could not be told from "nobody said".
	Official bool   `json:"official"`
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

// Response is the ONE result shape. Facets ship with every response because
// browse and search are the same request here — a query with no q is a browse.
type Response struct {
	Data   []Entry           `json:"data"`
	Total  int               `json:"total"`
	Facets map[string]counts `json:"facets"`
}

type counts map[string]int

type state struct{}

// Mount wires the lens and starts the corpus reconcile. No store, no DataDir:
// the corpus is the index's, and the sync is a goroutine, not an endpoint.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "catalog", build, routes)
}

func build(b cloud.Base) (state, error) {
	go loop(b)
	return state{}, nil
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/catalog", cloud.Handle(s, browse))
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
// Every dimension `facet` counts is filterable here and vice versa: a facet a
// caller can see but cannot act on is a rail that lies about being clickable.
//
// forkable is TRI-state for that reason. Read as `== "true"` it could only ever
// narrow, never select the complement, so `?forkable=false` silently meant "no
// filter" — a boolean axis whose negative case is unaskable is a label, not a
// filter.
func filter(in []Entry, c *zip.Ctx) []Entry {
	org, arch := strings.ToLower(c.Query("org")), strings.ToLower(c.Query("archetype"))
	lang, kind := strings.ToLower(c.Query("language")), strings.ToLower(c.Query("kind"))
	fork, forkSet := boolQuery(c, "forkable")
	first, firstSet := boolQuery(c, "official")
	out := in[:0]
	for _, e := range in {
		switch {
		case org != "" && strings.ToLower(e.Org) != org,
			arch != "" && strings.ToLower(e.Archetype) != arch,
			lang != "" && strings.ToLower(e.Language) != lang,
			kind != "" && strings.ToLower(e.Kind) != kind,
			forkSet && e.Forkable != fork,
			firstSet && e.Official != first:
			continue
		}
		out = append(out, e)
	}
	return out
}

// boolQuery reads a flag that has three answers, not two: yes, no, and unasked.
func boolQuery(c *zip.Ctx, name string) (v, ok bool) {
	b, err := strconv.ParseBool(strings.TrimSpace(c.Query(name)))
	return b, err == nil
}

// facet counts the matching set along every browse axis, so the rail a client
// renders is the rail that actually has results behind it. forkable is counted
// on BOTH sides by the same rule as every other dimension — counting only the
// trues rendered {true: everything} and told a caller there was a choice where
// there was none.
func facet(in []Entry) map[string]counts {
	f := map[string]counts{"org": {}, "archetype": {}, "language": {}, "kind": {}, "forkable": {}, "official": {}}
	for _, e := range in {
		for dim, v := range map[string]string{
			"org": e.Org, "archetype": e.Archetype, "language": e.Language,
			"kind": e.Kind, "forkable": strconv.FormatBool(e.Forkable),
			"official": strconv.FormatBool(e.Official),
		} {
			if v != "" {
				f[dim][v]++
			}
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

// The two seams sync writes and reads through, as package vars so the reconcile
// is testable without a live GitHub and the site source without a store.
var (
	reconcile = func(ctx context.Context, org string, rows []Entry) (int, int, error) {
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

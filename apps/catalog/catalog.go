// Package catalog is one place to browse every project, app and site built here.
//
// It is the CROSS-ORG discovery lens: the corpus spans orgs, so a project is
// findable whichever org built it.
//
// It owns no store. The corpus lives in the lexical index (apps/index) — the
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
// corpus is reconciled from sources that are public by construction (sync.go),
// so no credential exists that could promote a tenant row into it. The swap
// itself is a call on the internal plane — a socket the edge router does not
// carry, reachable only from inside this deployment — so "no write route" stays
// literally true of every surface a caller can reach.
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
	"github.com/hanzoai/cloud/plane"
	// The GENERATED client for the index peer — the one typed way to call it, with
	// the app name, the op and the In/Out pair already fixed to each other. Aliased
	// because the app package this file also imports is the SAME word: one is the
	// index in this process, the other is how to reach it in another.
	indexpeer "github.com/hanzoai/cloud/plane/index"
	projectspeer "github.com/hanzoai/cloud/plane/project"
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
	// ID is "<org>/<name>" and is the corpus's primary key: a re-published entry
	// updates in place under it rather than accumulating duplicates, so it is the
	// one handle stable enough to link to or to name in a `template` filter. Two
	// orgs can spell the same id, and `canonical` picks which one keeps it.
	ID  string `json:"id"`
	Org string `json:"org"` // hanzo | lux | zoo
	// Name is the short identifier inside the org — the repository's name, or the
	// site's slug — and is the half of ID after the slash. Not a display name;
	// Title is.
	Name string `json:"name"`
	// Title is what to SHOW. A site's human name wins where it has one; a repo row
	// falls back to the repository name, so on a repo this usually just repeats
	// Name. Absent only for a site whose project was never named — render Name.
	Title string `json:"title,omitempty"`
	Kind  string `json:"kind"` // repo | site
	// Origin is WHAT THIS IS TO YOU: template | community | third-party | product
	// (origin.go owns the four nouns and derives them). Not omitempty, for the
	// same reason Forkable is not: every row has an answer, and a missing one is
	// exactly the flattening this field exists to end.
	Origin string `json:"origin"`
	// Archetype is WHAT KIND OF THING this is, from a closed and ordered list —
	// model | contract | chain | sdk | template | infra | site | app — derived from
	// the repository's own topics, name and description, first match winning, and
	// always `site` for a deployed site. It is DERIVED, never guessed by a model,
	// because a wrong archetype hides a row from the browse rail more thoroughly
	// than a missing one does. Empty when no topic matched: unclassified, not
	// uncategorisable.
	Archetype string `json:"archetype,omitempty"`
	// Language is the repository's primary implementation language as GitHub
	// computes it ("Go", "TypeScript"), and the case is GitHub's. Empty for a site
	// with no source half and for a repository GitHub could not classify.
	Language string `json:"language,omitempty"`
	// Description is the repository's own one-line GitHub description, carried
	// verbatim. It comes from the SOURCE half of a row, so a site that was never
	// matched to a repository has none, and nothing here is written by us.
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`      // live, if it is deployed
	Repo        string `json:"repo,omitempty"`     // source
	Template    string `json:"template,omitempty"` // lineage, if forked from one
	// Forkable is NOT omitempty: false is an answer here, not a missing field.
	// Omitted, a client could not tell "you cannot fork this" from "nobody said".
	Forkable bool `json:"forkable"`
	// Stars is GitHub's stargazer count for the source repository, read at the last
	// sync and never accumulated here. It is not a ranking — the page sorts on
	// Updated — but it is the tiebreak when two orgs claim one ID. Absent for a
	// site with no repository behind it, and for a repository nobody has starred.
	Stars int `json:"stars,omitempty"`
	// Updated is when the thing last MOVED, as RFC 3339 in UTC: a repository's last
	// push, or a site's last deploy. The page is ordered on it, most recent first,
	// by comparing these strings — so the format is load-bearing and not cosmetic.
	// Absent means the source reported no timestamp, and such a row sorts last.
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
	// License is the terms that upstream work carries, in whichever form the half
	// that credited it had: an SPDX id ("MIT", "Apache-2.0") on a GitHub fork,
	// free text on a site whose publisher declared it. GitHub's NOASSERTION — "we
	// could not identify it" — reads as none rather than as a licence by that name.
	// So empty means UNDECLARED and never unencumbered, and Upstream is what says
	// whether the question applies at all.
	License string `json:"license,omitempty"`
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

// browseQuery is the GET /v1/catalog request: a free-text q plus the exact-match
// browse axes, every one of them optional and every one of them a query
// parameter.
//
// The axes are STRINGS rather than the bools and ints they read as, and that is
// deliberate: this surface has always answered an unparseable value by NOT
// applying that filter, and zip's URL binder leaves an unparseable value at the
// field's ZERO — so `?limit=0` and `?limit=abc` would arrive identically through
// an int, while the wire distinguishes them (0 is a page of nothing; abc is the
// default 50). Same for forkable, which is TRI-state here: yes, no, and unasked.
type browseQuery struct {
	// Q is the free-text query the lexical index scores relevance on. Empty is a
	// browse rather than a search — the same request either way.
	Q string `json:"q"`
	// Org narrows to one builder org: hanzo | lux | zoo. Case-insensitive.
	Org string `json:"org"`
	// Kind narrows to repo | site. Case-insensitive.
	Kind string `json:"kind"`
	// Origin narrows to what a row IS to you: template | community | third-party |
	// product. This is the axis the two hanzo.app lanes are cut on.
	Origin string `json:"origin"`
	// Archetype narrows to one project archetype. Case-insensitive.
	Archetype string `json:"archetype"`
	// Language narrows to one implementation language. Case-insensitive.
	Language string `json:"language"`
	// Template narrows a lane to ONE lineage: the id of the parent everything
	// returned was forked from.
	Template string `json:"template"`
	// Forkable is tri-state: "true" selects the forkable rows, "false" selects the
	// rest, and anything else — including absent — applies no filter at all.
	Forkable string `json:"forkable"`
	// Limit caps the page at 200, default 50. A value that is not a non-negative
	// integer falls back to the default.
	Limit string `json:"limit"`
	// Offset is where the page starts, default 0, with the same tolerance.
	Offset string `json:"offset"`
}

// catalogPage is the ONE result shape. Facets ship with every response because
// browse and search are the same request here — a query with no q is a browse.
//
// It is named for its product rather than called `Response`: a typed op's Go type
// name IS its schema name across the WHOLE fleet, the namespace is flat, and
// openapi.Compose refuses one name with two shapes.
type catalogPage struct {
	// Data is the page of matching entries, most recently updated first.
	Data []Entry `json:"data"`
	// Total is how many entries matched BEFORE paging — what a pager sizes itself on.
	Total int `json:"total"`
	// Facets counts the whole matching set along every browse axis, so a rail a
	// client renders is a rail that has results behind it. Keyed axis → value → count.
	Facets map[string]counts `json:"facets"`
}

type counts map[string]int

type state struct{}

// ops binds the subsystem to its typed handler: a TypedHandler has no parameter
// for the service, so it arrives as a RECEIVER and the op is a method value —
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// Mount wires the lens and starts the corpus reconcile. No store, no DataDir:
// the corpus is the index's, and the sync is a goroutine, not an endpoint.
func Use(app cloud.Router, deps cloud.Deps) error {
	// The typed-op registry lives on the App: it is what makes the browse a
	// document operation, an MCP tool, a CLI command and an SDK method rather than
	// only a route. A Router that cannot reach it must fail the mount rather than
	// serve a route no projection knows about.
	if app != nil && cloud.ZipApp(app) == nil {
		return fmt.Errorf("catalog.Use:  router carries no typed-op registry")
	}
	return cloud.Use(app, deps, "catalog", build, routes)
}

func build(b cloud.Base) (state, error) {
	go loop(b)
	return state{}, nil
}

// routes registers the lens.
//
// A typed op receives only a context, so the validated org reaches it by being
// parked there — never as an In field, which is caller-supplied and would be a
// cross-tenant read the caller asserted for itself. cloud.Bridge parks it, and
// the COMPOSER installs it, not this subsystem: the fused host once at its root
// (serve.go), and a plugin program's constructor likewise. The install this
// subsystem used to make sat on a group with no routes beneath it, a program zip
// refuses to compose.
//
// The op is declared on the App with its WHOLE path, not on a group with an
// empty leaf: joining "/v1/catalog" with "" yields "/v1/catalog/", a different
// path from the one this API has always served.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	zip.Get(cloud.ZipApp(app), "/v1/catalog", o.browse)
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

// Browse searches AND browses the cross-org catalog: every project, app and site
// the fleet has built, whichever org built it.
//
// It reads TWO corpora and returns them as one page — the published,
// world-readable catalog that every caller sees, plus the caller's OWN org's
// private entries when the request carries a validated principal. Each row says
// which it came from in `scope`, so a client can warn before sharing a link. An
// anonymous caller simply gets the published one; no filter can ever widen a
// caller into another tenant's corpus, because the query that would return it is
// never run for them.
//
// A request with no q is a browse rather than a search, and both answer the same
// shape: the page, the total before paging, and the facet counts over the whole
// matching set.
//
// Example: {"origin":"template","language":"typescript","forkable":"true","limit":"20"}
func (o ops) browse(ctx context.Context, in *browseQuery) (*catalogPage, error) {
	q := strings.TrimSpace(in.Q)
	rows, err := read(ctx, PublicOrg, q, "public")
	if err != nil {
		return nil, err
	}
	// The caller's OWN corpus, read with the validated principal and nothing else.
	// Anonymous callers simply get the published one. The org is the one Bridge
	// parked, never a request field: an In field is caller-supplied, so an org read
	// from one is a cross-tenant read the caller asserted for itself.
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
	return &catalogPage{Data: page(rows, in), Total: total, Facets: facets}, nil
}

// read pulls one corpus out of the index and stamps its scope.
func read(ctx context.Context, org, q, scope string) ([]Entry, error) {
	raw, err := lexical(ctx, org, q)
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

// lexical reads the corpus out of the index, wherever the index happens to be.
//
// IN-PROCESS FIRST, then the plane. Both legs are real: a fused binary that
// mounted both apps has the index right here and a call over the wire would be
// a pointless hop, while the deployed fleet runs one process per app and the
// in-process global is nil for good.
//
// This used to be `index.Query` alone, guarded by `index.Ready()` — and since
// Ready() answers "is the index in THIS binary", the guard was false forever
// once catalog and index became separate plugin rows. Every /v1/catalog request
// answered 503 "index not mounted", which is what hanzo.app's Community page
// rendered as "ERROR: CATALOG: 503": a page that drew perfectly and listed
// nothing, on a fleet where nothing was actually down.
func lexical(ctx context.Context, org, q string) ([]json.RawMessage, error) {
	if index.Ready() {
		return index.Query(ctx, org, uid, q, nil, scan, 0)
	}
	// WHICH TENANT THE CALL IS MADE FOR, and why it is not For().
	//
	// A typed handler's ctx carries the in-flight request, and zip's
	// forwardIdentity says an inbound request ALWAYS wins over a stated caller —
	// so For() is silently ignored here and the call goes out as whoever asked.
	// For an anonymous visitor that is nobody, which is exactly what shipped:
	// 500 "index: no org on the call" on the public browse.
	//
	// As() re-points the tenant on a context with NO request behind it, which is
	// the one place zip reads what we stated. The caller's authority still
	// travels whole; only the tenant is re-pointed — which is the whole point,
	// because the published corpus is read as PublicOrg by everyone, signed in
	// or not.
	call := cloud.For(ctx, org)
	if c, ok := cloud.Request(ctx); ok {
		call = cloud.As(c, org)
	}
	out, err := indexpeer.IndexQuery(call, &plane.IndexQueryIn{UID: uid, Q: q, Limit: scan})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return out.Rows, nil
}

// write hands the assembled corpus to the index, wherever the index happens to
// be. It is the exact mirror of lexical, and it was missing for the exact reason
// lexical needed writing: Reconcile serves out of the index's own process-level
// global, so in THIS process it has always answered "index: not mounted".
//
// That is why the catalog was empty. Not a wiped store, not an expired GitHub
// token, not a sync that never ran — the sync ran every hour, read both sources
// correctly, assembled the whole corpus, and then had nowhere to put it. When
// catalog and index became two plugin rows the READ was given a plane op and the
// write was deliberately left in-process ("one writer, in the process that owns
// the file"), which is the right property and the wrong conclusion: the writer is
// still one and still the index's, whether the corpus reaches it through a
// function call or a socket.
//
// Fixing the read alone turned a 503 into {"data":[],"total":0} — it began
// succeeding against a store nothing had ever written to. A page of nothing is a
// worse bug than an error, because it looks like an answer.
//
// IN-PROCESS FIRST, then the plane, both legs real, for the same reason lexical
// takes them in that order: a fused binary that mounted both apps has the index
// right here, and the deployed fleet does not.
func write(ctx context.Context, org string, docs []json.RawMessage) (int, int, error) {
	if index.Ready() {
		rows := make([]map[string]any, 0, len(docs))
		for _, raw := range docs {
			var d map[string]any
			if err := json.Unmarshal(raw, &d); err != nil {
				continue
			}
			rows = append(rows, d)
		}
		return index.Reconcile(ctx, org, uid, pk, rows)
	}
	// WHICH TENANT THE CORPUS IS WRITTEN AS, and why For() is right here where
	// lexical needs As().
	//
	// lexical runs inside a request, and zip's forwardIdentity says an inbound
	// request always wins over a stated caller — so it has an identity to displace.
	// This runs in the sync goroutine off a background context: there is no request
	// to lose to, and the tenant is simply stated. That is the form plane's own
	// contract names for a background job, and the published corpus is written as
	// PublicOrg by exactly this call.
	out, err := indexpeer.IndexReconcile(cloud.For(ctx, org),
		&plane.IndexReconcileIn{UID: uid, PrimaryKey: pk, Docs: docs})
	if err != nil {
		return 0, 0, err
	}
	if out == nil {
		return 0, 0, nil
	}
	return out.Kept, out.Removed, nil
}

// filter applies the exact-match browse axes. An absent param is not a filter.
// Every dimension `facet` counts is filterable here and vice versa: a facet a
// caller can see but cannot act on is a rail that lies about being clickable.
//
// forkable is TRI-state for that reason. Read as `== "true"` it could only ever
// narrow, never select the complement, so `?forkable=false` silently meant "no
// filter" — a boolean axis whose negative case is unaskable is a label, not a
// filter.
func filter(rows []Entry, in *browseQuery) []Entry {
	org, arch := strings.ToLower(in.Org), strings.ToLower(in.Archetype)
	lang, kind := strings.ToLower(in.Language), strings.ToLower(in.Kind)
	// origin cuts the corpus into the lanes a person actually browses; parent
	// narrows a lane to one lineage ("everything built from folio"), which is what
	// turns the community lane from a pile into something you can read.
	orig, parent := strings.ToLower(in.Origin), strings.ToLower(in.Template)
	fork, forkSet := boolQuery(in.Forkable)
	out := rows[:0]
	for _, e := range rows {
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

// boolQuery reads a flag that has three answers, not two: yes, no, and unasked.
//
// It takes the raw STRING rather than a *bool because the two disagree on the
// wire: zip's URL binder reads a bare `?forkable` (no value at all) as TRUE,
// while this surface has always read it as unasked — ParseBool refuses an empty
// string. A bool In would therefore return a different set of rows for a URL that
// has not changed.
func boolQuery(raw string) (v, ok bool) {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	return b, err == nil
}

// facet counts the matching set along every browse axis, so the rail a client
// renders is the rail that actually has results behind it. forkable is counted
// on BOTH sides by the same rule as every other dimension — counting only the
// trues rendered {true: everything} and told a caller there was a choice where
// there was none.
func facet(in []Entry) map[string]counts {
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

func page(rows []Entry, in *browseQuery) []Entry {
	limit, offset := intQuery(in.Limit, 50), intQuery(in.Offset, 0)
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset >= len(rows) {
		return []Entry{}
	}
	if end := offset + limit; end < len(rows) {
		return rows[offset:end]
	}
	return rows[offset:]
}

// intQuery reads a paging bound from its raw string, falling back to def for
// every value that is not a non-negative integer.
//
// It takes the STRING rather than an int field for the same reason boolQuery
// does: zip's URL binder leaves an unparseable value at the field's ZERO, so an
// int In could not tell `?limit=0` (a page of nothing, which this surface
// serves) from `?limit=abc` (unset, which it answers with 50).
func intQuery(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// The two clients sync writes and reads through, as package vars so the reconcile
// is testable without a live GitHub and the site source without a store.
var (
	reconcile = func(ctx context.Context, org string, rows []Entry) (int, int, error) {
		docs := make([]json.RawMessage, 0, len(rows))
		for _, e := range rows {
			if e.ID == "" || e.Org == "" {
				continue // an unkeyed row is one the next swap could never prune
			}
			e.Scope = "" // provenance is stamped on READ; storing it would freeze it
			raw, err := json.Marshal(e)
			if err != nil {
				continue
			}
			docs = append(docs, raw)
		}
		return write(ctx, org, docs)
	}
	// The corpus's two sources, one client each: what we BUILT and what is LIVE.
	fromOrgs  = orgRepos
	liveSites = serving
)

// serving is what is LIVE, wherever the projects store happens to be — the same
// two legs as lexical and write, for the third source that was reaching for an
// in-process global across a process boundary.
//
// This one failed the most quietly of the three. projects.LiveSites reports nil
// when its package is unmounted, because a deployment that hosts no sites is not
// an error — true of a deployment, and false of a PROCESS. In the catalog process
// it meant "you asked the wrong half of the fleet", and nil and empty are the
// same answer, so the corpus simply had no sites in it and nothing anywhere said
// so. That is the whole `site` kind, every demo URL, and the deployed starters
// the template lane is mostly made of.
func serving(ctx context.Context) ([]projects.LiveSite, error) {
	if projects.Ready() {
		return projects.LiveSites(ctx)
	}
	out, err := projectspeer.SitesLive(ctx, &plane.LiveSitesIn{})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	live := make([]projects.LiveSite, 0, len(out.Sites))
	for _, s := range out.Sites {
		live = append(live, projects.LiveSite{
			Org: s.Org, Slug: s.Slug, Name: s.Name, URL: s.URL,
			Repo: s.Repo, ForkedFrom: s.ForkedFrom, UpdatedAt: s.UpdatedAt,
			Upstream: s.Upstream, License: s.License,
		})
	}
	return live, nil
}

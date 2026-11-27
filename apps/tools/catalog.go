package tools

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// The CATALOG: our canonical copy of what the public MCP registries publish.
//
// It is not a second external-server registry. An org's SERVERS are connections
// it owns (external_mcp.go) — a URL, a credential, a lifecycle. The catalog is
// the world's LISTINGS: what exists, who publishes it, where it can be reached.
// Enabling a listing does not create a parallel record, it creates the SAME
// MCPServer row with its listing recorded, so there is one thing an org can have and
// one place it lives, whether it was typed in by hand or picked off a shelf.
//
// The copy is canonical because the upstream is not ours: registry.modelcontextprotocol.io
// can be slow, rate-limited or down, and a storefront that goes blank when a third
// party does is not a storefront. It also cannot hold what only we know — which
// listings we hide, feature, or vouch for — so the row carries both halves: the
// upstream fields, replaced wholesale on every sync, and the CURATION fields,
// which a sync never touches.
//
// One store, the shared discipline: one SQLite file under DataDir, born encrypted
// under its own derived key. Unlike every other store
// in this package it has NO org column, and that is the point — a catalog is the
// same for everyone, which is exactly why an org's own enablement of an entry
// lives in the org-scoped store instead.

// upstream is the public MCP registry we sync from. It is a constant and not a
// setting: the whole value of a canonical copy is that every deployment holds the
// same one, and a per-deployment source would make "official" mean something
// different in each. CLOUD_TOOLS_REGISTRY overrides it for a test or an air-gapped
// mirror — see registryURL.
const upstream = "https://registry.modelcontextprotocol.io"

const (
	// syncPage is how many listings one upstream request asks for. 100 is the
	// registry's documented maximum, so this is the fewest round trips it allows.
	syncPage = 100
	// syncPages is the walk's backstop. The public registry held 19,321 servers
	// the first time this ran, so a cap chosen to be "comfortably past the whole
	// registry" is a cap that starts SILENTLY TRUNCATING the catalog one good
	// quarter from now — which is the failure it was written to prevent, arrived
	// at quietly. So it sits two orders of magnitude out, and hitting it is an
	// ERROR: a short catalog that says so beats a short catalog that does not.
	//
	// It is not the loop guard either. A cursor that never ends is a cursor that
	// REPEATS, and Sync catches that directly (see seen) — which is both the real
	// adversarial shape and immediate, rather than 5000 requests later.
	syncPages = 5000
	// syncBody bounds one upstream response.
	syncBody = 8 << 20
)

// MCPListing is one upstream MCP server as we hold it: what the registry published,
// plus what we decided about it.
type MCPListing struct {
	// ID addresses the listing in a URL. It is the reverse-DNS NAME with its one
	// slash written as an underscore — reversible, because a namespace never
	// contains an underscore — so the id is readable and stable rather than a
	// hash that means nothing to whoever reads a link.
	ID string `json:"id"`
	// Name is the publisher's reverse-DNS name, e.g. "com.stripe/mcp".
	Name string `json:"name"`
	// Vendor is the namespace half of Name — the publisher, e.g. "com.stripe".
	Vendor string `json:"vendor"`
	// Title is the human-readable display name, when the entry carries one.
	Title string `json:"title,omitempty"`
	// Description is the publisher's one-line summary.
	Description string `json:"description"`
	// Repo is the source repository URL, when the entry names one.
	Repo string `json:"repo,omitempty"`
	// Site is the project's homepage, when the entry names one.
	Site string `json:"site,omitempty"`
	// Version is the published version of this listing.
	Version string `json:"version"`
	// Transports are the distinct transports this server can be reached over,
	// sorted: some of "stdio", "streamable-http", "sse". A listing with
	// "streamable-http" is one an org can enable here and now; a listing that is
	// only "stdio" needs a process to run it.
	Transports []string `json:"transports"`
	// Packages are the runnable package forms — npm, pypi, oci — each with the
	// runtime that launches it and the transport it then speaks.
	Packages []MCPPackage `json:"packages,omitempty"`
	// Remotes are the hosted endpoints the publisher serves the server at.
	Remotes []MCPRemote `json:"remotes,omitempty"`
	// Registry is the upstream this row was synced from.
	Registry string `json:"registry"`
	// Synced is when this row was last confirmed against upstream, Unix seconds.
	Synced int64 `json:"synced"`
	// Hidden keeps the listing out of the org-visible catalog. Curation: a sync
	// never changes it. Only a SuperAdmin sets it, and only a SuperAdmin sees a
	// hidden entry listed.
	Hidden bool `json:"hidden"`
	// Featured puts the listing on the front of the shelf. Curation.
	Featured bool `json:"featured"`
	// Official is whether this is the vendor's OWN server rather than someone
	// else's copy of it. Derived on every sync (see isOfficial) until a
	// SuperAdmin sets it explicitly, after which the admin's answer stands.
	Official bool `json:"official"`
	// Logo is the brand mark to render for the listing — the publisher's icon when
	// the entry carries one, or the one an admin set. Curation.
	Logo string `json:"logo,omitempty"`
}

// MCPPackage is one runnable form of a server: what to fetch, what runs it, and what
// it speaks once running.
type MCPPackage struct {
	// Registry is where the package is fetched from: npm, pypi, oci, nuget, mcpb.
	Registry string `json:"registry"`
	// Identifier is the package name or download URL.
	Identifier string `json:"identifier"`
	// Version is the exact published package version.
	Version string `json:"version,omitempty"`
	// Runtime is the publisher's hint for what launches it: npx, uvx, docker.
	Runtime string `json:"runtime,omitempty"`
	// Transport is what the launched process speaks: usually "stdio".
	Transport string `json:"transport"`
}

// MCPRemote is one hosted endpoint the publisher serves the server at.
type MCPRemote struct {
	// Transport is "streamable-http" or "sse".
	Transport string `json:"transport"`
	// URL is the endpoint.
	URL string `json:"url"`
}

// Endpoint is the listing's streamable-http URL, or "" when it has none.
// It is the ONE question enablement asks of a listing: an org can enable what can
// be reached, and a package that has to be RUN first cannot be until there is
// somewhere to run it (see apps/tools/LLM.md).
func (l MCPListing) Endpoint() string {
	for _, r := range l.Remotes {
		if r.Transport == "streamable-http" {
			return r.URL
		}
	}
	return ""
}

// CatalogStore is the canonical copy of the public registries. One SQLite file,
// no org column: a catalog is the same for every tenant, and what a tenant does
// with an entry is a row in the org-scoped server store instead.
type CatalogStore struct {
	db   *sql.DB
	http *http.Client
}

// OpenCatalogStore opens (and migrates) the catalog under dir.
func OpenCatalogStore(dir string) (*CatalogStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "tools-catalog", dir, func(db *sql.DB) (*CatalogStore, error) {
		ctx := context.Background()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS catalog (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  vendor      TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  repo        TEXT NOT NULL DEFAULT '',
  site        TEXT NOT NULL DEFAULT '',
  version     TEXT NOT NULL DEFAULT '',
  transports  TEXT NOT NULL DEFAULT '[]',
  packages    TEXT NOT NULL DEFAULT '[]',
  remotes     TEXT NOT NULL DEFAULT '[]',
  registry    TEXT NOT NULL DEFAULT '',
  digest      TEXT NOT NULL DEFAULT '',
  synced      BIGINT NOT NULL DEFAULT 0,
  hidden      BIGINT NOT NULL DEFAULT 0,
  featured    BIGINT NOT NULL DEFAULT 0,
  official    BIGINT NOT NULL DEFAULT 0,
  curated     BIGINT NOT NULL DEFAULT 0,
  logo        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS catalog_vendor ON catalog (vendor);
CREATE TABLE IF NOT EXISTS catalog_pass (
  id       BIGINT PRIMARY KEY CHECK (id = 1),
  finished BIGINT NOT NULL
);`)
			return err
		}); err != nil {
			return nil, fmt.Errorf("tools: catalog migrate: %w", err)
		}
		// A PLAIN client, deliberately: the SSRF guard on the server registry exists
		// because an ORG supplies that URL, and this one comes from the deployment's
		// own environment. Guarding it would protect against nobody and would refuse
		// the in-cluster mirror an air-gapped deployment must point at.
		return &CatalogStore{db: db, http: &http.Client{Timeout: 30 * time.Second}}, nil
	})
}

// Serves reports whether a listing on the shelf, hidden ones included, serves the
// server at host and dir, as locate reads a URL: a remote on the same host whose
// scope holds dir. A server the public registry lists is the shelf's, whoever
// types its URL in by hand and however it spells the host; a host that serves
// many servers under paths of their own — server.smithery.ai/@owner/name/mcp —
// is the shelf's only where the shelf lists one; and a remote the registry
// publishes as a template — https://{region}.api.example.com/t-{tenant}/mcp —
// serves every value of its variables on the domain it names.
func (s *CatalogStore) Serves(ctx context.Context, host, dir string) (bool, error) {
	if host == "" {
		return false, nil
	}
	// Every remote that could serve host names its registrable domain literally:
	// a template whose variables reach into it serves nobody (pattern).
	domain := registrable(host)
	uni, err := hosts.ToUnicode(domain)
	if err != nil {
		uni = domain
	}
	// A prefilter: a row is kept if it names the domain either way it may be spelled,
	// and pattern decides below. LIKE is a substring test, so the terms are escaped
	// (likeEscape), and both sides are lowered because LIKE folds case on SQLite and
	// not on PostgreSQL.
	rows, err := s.db.QueryContext(ctx, `SELECT remotes FROM catalog
WHERE lower(remotes) LIKE lower($1) ESCAPE '\' OR lower(remotes) LIKE lower($2) ESCAPE '\'`,
		"%"+likeEscape(domain)+"%", "%"+likeEscape(uni)+"%")
	if err != nil {
		return false, fmt.Errorf("tools: catalog serves %s: %w", host, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, fmt.Errorf("tools: catalog serves %s: %w", host, err)
		}
		var remotes []MCPRemote
		if json.Unmarshal([]byte(raw), &remotes) != nil {
			continue
		}
		for _, r := range remotes {
			if p, ok := pattern(r.URL); ok && p.serves(host, dir) {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// registrable is the part of a host its owner registered — example.com of
// mcp.example.com — or the host itself when it has none: an address, a suffix.
func registrable(host string) string {
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// shape is a listed remote as a pattern: its host's labels and its scope's
// segments, each literal text with any text where a template's variable stood.
type shape struct{ labels, scope []*regexp.Regexp }

// variable stands for a template's variable — {tenant_id}, {server.region} — while
// a listed remote's URL is read; variables finds them as the registry declares
// them, anything but braces between braces.
const variable = "0var0"

var variables = regexp.MustCompile(`\{[^{}]*\}`)

// pattern reads a listed remote's URL, a template's included, into what it
// serves. ok is false for a URL no resolver reads, and for a template whose
// variables reach into the registrable domain — mcp.{company}.com, {a}.{b} — which
// names every company's host: the shelf's claim on it would be a claim on
// everyone's own server.
func pattern(raw string) (shape, bool) {
	host, dir, ok := locate(variables.ReplaceAllString(raw, variable))
	if !ok || strings.Contains(registrable(host), variable) {
		return shape{}, false
	}
	var r shape
	for _, l := range strings.Split(host, ".") {
		r.labels = append(r.labels, part(l, "[^.]+"))
	}
	for _, g := range segments(scope(dir)) {
		r.scope = append(r.scope, part(g, "[^/]+"))
	}
	return r, true
}

// part is one label or segment as a pattern: its literal text, and any text
// within it (any) where a variable stood.
func part(s, any string) *regexp.Regexp {
	pieces := strings.Split(s, variable)
	for i := range pieces {
		pieces[i] = regexp.QuoteMeta(pieces[i])
	}
	return regexp.MustCompile("^" + strings.Join(pieces, any) + "$")
}

// serves reports whether the shape serves the server at host and dir: the same
// labels, and dir inside its scope.
func (r shape) serves(host, dir string) bool {
	labels, segs := strings.Split(host, "."), segments(dir)
	if len(labels) != len(r.labels) || len(segs) < len(r.scope) {
		return false
	}
	for i, re := range r.labels {
		if !re.MatchString(labels[i]) {
			return false
		}
	}
	for i, re := range r.scope {
		if !re.MatchString(segs[i]) {
			return false
		}
	}
	return true
}

// segments splits a dir, as locate or scope reads one, into its segments.
func segments(dir string) []string {
	if d := strings.Trim(dir, "/"); d != "" {
		return strings.Split(d, "/")
	}
	return nil
}

// hosts reads a host name as a resolver does (UTS #46), allowing the characters a
// registered name may carry beyond a strict domain name's.
var hosts = idna.New(idna.MapForLookup(), idna.StrictDomainName(false))

// locate reads a server URL the way a resolver reads it: the host in its ASCII
// form, lowercased, without a port or a trailing dot — "MCP.Stripe.COM.:443" and
// "mcp。stripe。com" are both mcp.stripe.com — and the path cleaned and
// lowercased, ending in "/". ok is false for a URL no resolver reads.
func locate(raw string) (host, dir string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", false
	}
	host, err = hosts.ToASCII(u.Hostname())
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if err != nil || host == "" {
		return "", "", false
	}
	dir = strings.ToLower(path.Clean("/" + u.Path))
	if dir != "/" {
		dir += "/"
	}
	return host, dir, true
}

// scope is where on its host a listed remote serves: the directory its path is in
// (dir, as locate reads it). A remote at the root or one step below it —
// mcp.stripe.com, api.githubcopilot.com/mcp/ — serves the whole host.
func scope(dir string) string {
	d := strings.TrimSuffix(dir, "/")
	return d[:strings.LastIndex(d, "/")+1]
}

// Close closes the underlying database.
func (s *CatalogStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Query narrows a catalog listing. The zero value lists everything visible.
type Query struct {
	// Text matches the name, title or description, case-insensitively.
	Text string
	// Featured, Official keep only listings with that flag when true.
	Featured, Official bool
	// Hidden includes the hidden listings. Only a SuperAdmin ever passes true —
	// the org-visible view is the same query with it false, so "what an org sees"
	// and "what an admin sees" are ONE query and cannot drift apart.
	Hidden bool
	// Limit bounds the page. Zero or less means catalogPage; more than catalogMax
	// is clamped to it.
	Limit int
	// Offset skips that many rows.
	Offset int
}

const (
	// catalogPage is a page of listings, and catalogMax is the most one request
	// may ask for. They are not decoration: the public registry held 19,321
	// servers the first time this synced, so an unbounded list is a twenty-megabyte
	// response nobody asked for and a storefront that renders in a minute.
	catalogPage = 50
	catalogMax  = 200
)

// page is the bounded (limit, offset) a query resolves to. A limit that is not a
// positive integer reads as the default, which is what an absent one is.
func (q Query) page() (int, int) {
	limit := q.Limit
	if limit <= 0 {
		limit = catalogPage
	}
	if limit > catalogMax {
		limit = catalogMax
	}
	return limit, max(q.Offset, 0)
}

// List returns one page of the matching listings, featured first then name, with
// the total the filter matched — which is what lets a storefront say "showing 50
// of 9,216" rather than implying the shelf ends where the page does.
func (s *CatalogStore) List(ctx context.Context, q Query) ([]MCPListing, int, error) {
	where := []string{}
	args := []any{}
	if !q.Hidden {
		where = append(where, "hidden=0")
	}
	if q.Featured {
		where = append(where, "featured=1")
	}
	if q.Official {
		where = append(where, "official=1")
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		if err := dialect.Text(t); err != nil {
			return nil, 0, fmt.Errorf("tools: search catalog: %w", err)
		}
		// ESCAPE, because % and _ are wildcards and a caller searching for the
		// literal "create_payment" means the underscore. Both sides are lowered,
		// because LIKE folds case on SQLite and not on PostgreSQL.
		where = append(where, `(lower(name) LIKE lower($1) ESCAPE '\' OR lower(title) LIKE lower($2) ESCAPE '\' OR lower(description) LIKE lower($3) ESCAPE '\')`)
		like := "%" + likeEscape(t) + "%"
		args = append(args, like, like, like)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("tools: count catalog: %w", err)
	}
	limit, offset := q.page()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+listingCols+` FROM catalog`+clause+` ORDER BY featured DESC, name LIMIT `+holder(len(args)+1)+` OFFSET `+holder(len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("tools: list catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []MCPListing{}
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// holder is the placeholder for the n'th argument.
func holder(n int) string { return "$" + strconv.Itoa(n) }

// likeEscape makes a search term literal: the LIKE wildcards, and the escape
// character itself, stop being operators.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// Get returns one listing by id. A hidden listing is returned — the caller
// decides whether it may be shown, because a detail page and a curation edit
// address the same row.
func (s *CatalogStore) Get(ctx context.Context, id string) (MCPListing, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+listingCols+` FROM catalog WHERE id=$1`, id)
	return scanListing(row)
}

// Curation is the set of decisions a SuperAdmin makes about a listing. Every
// field is a POINTER because this is a patch: nil leaves what is there, which is
// what lets "feature this" not silently un-hide something.
type Curation struct {
	Hidden   *bool
	Featured *bool
	Official *bool
	Logo     *string
}

// Curate applies a patch to one listing and returns the result. Setting Official
// marks the row curated, so no later sync re-derives over the admin's answer —
// which is the whole difference between a default and a decision.
func (s *CatalogStore) Curate(ctx context.Context, id string, c Curation) (MCPListing, error) {
	sets, args := []string{}, []any{}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, column+"="+holder(len(args)))
	}
	if c.Hidden != nil {
		set("hidden", boolInt(*c.Hidden))
	}
	if c.Featured != nil {
		set("featured", boolInt(*c.Featured))
	}
	if c.Official != nil {
		set("official", boolInt(*c.Official))
		sets = append(sets, "curated=1")
	}
	if c.Logo != nil {
		logo := strings.TrimSpace(*c.Logo)
		if err := dialect.Text(logo); err != nil {
			return MCPListing{}, fmt.Errorf("tools: curate catalog: %w", err)
		}
		set("logo", logo)
	}
	if len(sets) == 0 {
		return s.Get(ctx, id)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE catalog SET `+strings.Join(sets, ", ")+` WHERE id=`+holder(len(args)+1), append(args, id)...)
	if err != nil {
		return MCPListing{}, fmt.Errorf("tools: curate catalog: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return MCPListing{}, ErrUnknownTool
	}
	return s.Get(ctx, id)
}

// Count is how many listings the canonical copy holds.
func (s *CatalogStore) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog`).Scan(&n); err != nil {
		return 0, fmt.Errorf("tools: count catalog: %w", err)
	}
	return n, nil
}

// ── sync ────────────────────────────────────────────────────────────────────────

// Sync walks the upstream registry and writes what it publishes into the
// canonical copy, returning how many rows were new and how many changed.
//
// It is IDEMPOTENT by construction: the id is the publisher's own name, so a
// second pass over an unchanged registry updates the same rows in place and
// reports added=0, updated=0. Nothing is ever deleted here — a listing that
// vanishes upstream is a listing an org may already have enabled, and removing
// its description would not remove its server.
//
// The CURATION columns are not in the write list at all. That is not a rule to
// remember, it is the statement: a sync cannot un-hide, un-feature or re-brand
// anything, because it never names those columns.
func (s *CatalogStore) Sync(ctx context.Context) (added, updated int, err error) {
	have, err := s.digests(ctx)
	if err != nil {
		return 0, 0, err
	}
	base := registryURL()
	cursor, seen := "", map[string]bool{}
	for page := 0; ; page++ {
		batch, next, err := s.fetch(ctx, base, cursor)
		if err != nil {
			return added, updated, err
		}
		// One transaction per PAGE. The store is one connection, so a row per
		// commit is a page's worth of fsyncs for a page's worth of rows — nineteen
		// thousand of them on a first sync, inside a request someone is waiting on.
		// Per page rather than per sync, because a walk over a third party's cursor
		// can end in an error at any point and the pages that DID read are worth
		// keeping.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return added, updated, fmt.Errorf("tools: begin catalog write: %w", err)
		}
		for _, l := range batch {
			was, held := have[l.ID]
			if err := s.put(ctx, tx, l); err != nil {
				_ = tx.Rollback()
				return added, updated, err
			}
			switch {
			case !held:
				added++
			case was != l.digest():
				updated++
			}
			have[l.ID] = l.digest()
		}
		if err := tx.Commit(); err != nil {
			return added, updated, fmt.Errorf("tools: commit catalog page: %w", err)
		}
		if next == "" || len(batch) == 0 {
			return added, updated, s.finish(ctx)
		}
		// The cursor is a third party's, so the walk must not depend on it ending.
		// A cursor already followed is a loop, and one more request would be the
		// same request.
		if seen[next] {
			return added, updated, fmt.Errorf("tools: registry cursor %q repeated after %d pages", next, page+1)
		}
		seen[next] = true
		if page+1 >= syncPages {
			return added, updated, fmt.Errorf("tools: registry did not end after %d pages", syncPages)
		}
		cursor = next
	}
}

// finish records that a pass walked the registry to its last page. Only a
// finished pass counts as fresh (catalog_keep.go): a walk cut short by an
// upstream error or a stopped process keeps the rows it wrote, and leaves the
// copy stale so the next read walks it again.
func (s *CatalogStore) finish(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO catalog_pass (id, finished) VALUES (1, $1)
ON CONFLICT(id) DO UPDATE SET finished=excluded.finished`, time.Now().Unix()); err != nil {
		return fmt.Errorf("tools: record catalog pass: %w", err)
	}
	return nil
}

// Finished is when a pass last walked the registry to its end; zero when none has.
func (s *CatalogStore) Finished(ctx context.Context) (time.Time, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT finished FROM catalog_pass WHERE id = 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("tools: read catalog pass: %w", err)
	}
	return time.Unix(at, 0), nil
}

// registryURL is the upstream to sync from: the public registry, or the override
// an air-gapped deployment (or a test) points at a mirror with.
func registryURL() string {
	if v := env.Or("CLOUD_TOOLS_REGISTRY", ""); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return upstream
}

// digests reads the id → content-digest of everything already held, so a sync can
// report what actually CHANGED rather than restating its own row count.
func (s *CatalogStore) digests(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, digest FROM catalog`)
	if err != nil {
		return nil, fmt.Errorf("tools: read catalog digests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, d string
		if err := rows.Scan(&id, &d); err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, rows.Err()
}

// put writes one upstream listing. The curation columns are absent from the
// update list, so they survive; official is written only while the row is
// UNCURATED, so the derivation is a default and an admin's answer is final.
func (s *CatalogStore) put(ctx context.Context, tx *sql.Tx, l MCPListing) error {
	transports, _ := json.Marshal(l.Transports)
	packages, _ := json.Marshal(l.Packages)
	remotes, _ := json.Marshal(l.Remotes)
	_, err := tx.ExecContext(ctx, `
INSERT INTO catalog (id, name, vendor, title, description, repo, site, version,
                     transports, packages, remotes, registry, digest, synced, official, logo)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, vendor=excluded.vendor, title=excluded.title,
  description=excluded.description, repo=excluded.repo, site=excluded.site,
  version=excluded.version, transports=excluded.transports,
  packages=excluded.packages, remotes=excluded.remotes,
  registry=excluded.registry, digest=excluded.digest, synced=excluded.synced,
  official=CASE WHEN catalog.curated=1 THEN catalog.official ELSE excluded.official END,
  logo=CASE WHEN catalog.logo='' THEN excluded.logo ELSE catalog.logo END`,
		l.ID, l.Name, l.Vendor, l.Title, l.Description, l.Repo, l.Site, l.Version,
		string(transports), string(packages), string(remotes), l.Registry, l.digest(),
		time.Now().Unix(), boolInt(l.Official), l.Logo)
	if err != nil {
		return fmt.Errorf("tools: put catalog listing %q: %w", l.ID, err)
	}
	return nil
}

// scrub drops the NUL bytes a feed's text may carry, which PostgreSQL refuses and
// which one listing must not be able to stop a sync over. The feed's JSON decoder
// has already made its text valid UTF-8.
func scrub(s string) string { return strings.ReplaceAll(s, "\x00", "") }

// fetch reads one upstream page and shapes it into listings. Only the LATEST
// version of each server is asked for: the registry keeps every published version
// and a catalog of a product's history is not a catalog of products.
func (s *CatalogStore) fetch(ctx context.Context, base, cursor string) ([]MCPListing, string, error) {
	u := fmt.Sprintf("%s/v0/servers?version=latest&limit=%d", base, syncPage)
	if cursor != "" {
		u += "&cursor=" + url.QueryEscape(cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", fmt.Errorf("tools: build registry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("tools: read registry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, syncBody))
	if err != nil {
		return nil, "", fmt.Errorf("tools: read registry body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("tools: registry returned %d", resp.StatusCode)
	}
	var page struct {
		Servers []struct {
			Server struct {
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Version     string `json:"version"`
				WebsiteURL  string `json:"websiteUrl"`
				Repository  struct {
					URL string `json:"url"`
				} `json:"repository"`
				Icons []struct {
					Src string `json:"src"`
				} `json:"icons"`
				Packages []struct {
					RegistryType string `json:"registryType"`
					Identifier   string `json:"identifier"`
					Version      string `json:"version"`
					RuntimeHint  string `json:"runtimeHint"`
					Transport    struct {
						Type string `json:"type"`
					} `json:"transport"`
				} `json:"packages"`
				Remotes []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"remotes"`
			} `json:"server"`
		} `json:"servers"`
		Metadata struct {
			NextCursor string `json:"nextCursor"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", fmt.Errorf("tools: registry shape not recognised: %w", err)
	}
	// The cursor is echoed back on the next request and held for the loop check,
	// so an upstream could otherwise hand us a megabyte of it, 5000 times.
	if len(page.Metadata.NextCursor) > 512 {
		return nil, "", fmt.Errorf("tools: registry cursor is %d bytes", len(page.Metadata.NextCursor))
	}
	out := make([]MCPListing, 0, len(page.Servers))
	for _, e := range page.Servers {
		v := e.Server
		vendor, _, ok := strings.Cut(v.Name, "/")
		if !ok || !upstreamName.MatchString(v.Name) {
			continue // not a name we can address, and not one we will invent an id for
		}
		l := MCPListing{
			ID: listingID(v.Name), Name: v.Name, Vendor: vendor, Title: scrub(v.Title),
			Description: scrub(v.Description), Repo: webURL(v.Repository.URL), Site: webURL(v.WebsiteURL),
			Version: scrub(v.Version), Registry: base,
		}
		if len(v.Icons) > 0 {
			l.Logo = webURL(v.Icons[0].Src)
		}
		kinds := map[string]bool{}
		for _, p := range v.Packages {
			l.Packages = append(l.Packages, MCPPackage{
				Registry: p.RegistryType, Identifier: p.Identifier, Version: p.Version,
				Runtime: p.RuntimeHint, Transport: p.Transport.Type,
			})
			kinds[p.Transport.Type] = true
		}
		for _, r := range v.Remotes {
			l.Remotes = append(l.Remotes, MCPRemote{Transport: r.Type, URL: r.URL})
			kinds[r.Type] = true
		}
		for k := range kinds {
			if k != "" {
				l.Transports = append(l.Transports, k)
			}
		}
		sort.Strings(l.Transports)
		l.Official = isOfficial(l)
		out = append(out, l)
	}
	return out, page.Metadata.NextCursor, nil
}

// digest fingerprints the UPSTREAM half of a listing, so a sync can tell a row
// that changed from one that was merely seen again. Curation is deliberately not
// in it: an admin featuring something is not upstream news.
func (l MCPListing) digest() string {
	b, _ := json.Marshal([]any{l.Name, l.Title, l.Description, l.Repo, l.Site, l.Version,
		l.Transports, l.Packages, l.Remotes})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// upstreamName is the registry's OWN name grammar: exactly one slash, a namespace
// of [a-zA-Z0-9.-] and a remainder of [a-zA-Z0-9._-].
//
// It is enforced here rather than assumed, because listingID's reversibility rests
// on it and the id is the catalog's PRIMARY KEY. A namespace containing an
// underscore would make "com.foo_bar/mcp" and "com.foo/bar_mcp" the same row —
// and put's conflict resolution PRESERVES curation, so the second would inherit
// the first's featured, admin-vouched standing and replace its endpoint. An
// upstream that publishes a name outside its own grammar is skipped.
var upstreamName = regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`)

// webURL keeps a third party's URL only when it is one a browser may follow.
// Every one of these is RENDERED — the logo on a branding page, the repository and
// site as links — and "src" from an untrusted registry is exactly where a
// javascript: or data: URI would arrive. An unusable value is dropped rather than
// stored, so no reader has to remember to re-check it.
func webURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.String()
}

// listingID is the URL form of a reverse-DNS name: the one slash written as an
// underscore. It is reversible — a NAMESPACE is [a-zA-Z0-9.-] and can never
// contain an underscore — so the id reads as the name it is and no lookup table
// is needed to get back.
func listingID(name string) string {
	ns, rest, ok := strings.Cut(name, "/")
	if !ok {
		return name
	}
	return ns + "_" + rest
}

// isOfficial answers the one question a shelf full of copies makes urgent: is
// this the VENDOR'S server, or someone else's wrapper around it?
//
// The registry attests the publisher, not the product. A namespace like
// "com.stripe" is issued only against proof of the domain stripe.com, so the
// namespace IS the vendor's identity — but "io.github.alice" attests an account
// on a code forge, not a domain, and a re-hosting service publishes hundreds of
// other people's servers under its own perfectly-verified namespace.
//
// So two conditions, both mechanical:
//
//  1. the namespace names a DOMAIN, not an account on a forge.
//  2. that domain SERVES this listing — its endpoint, its site or its repository
//     is on the vendor's own host.
//
// stripe.com publishing mcp.stripe.com is official. A proxy on someone's
// workers.dev is not, and neither is a listing whose namespace is a forge
// account. What the data cannot settle — a re-hoster's own-brand wrapper — is
// what a SuperAdmin overrides, and Curate makes that answer final.
func isOfficial(l MCPListing) bool {
	labels := strings.Split(l.Vendor, ".")
	if len(labels) < 2 {
		return false
	}
	for _, forge := range []string{"io.github.", "io.gitlab."} {
		if strings.HasPrefix(l.Vendor+".", forge) {
			return false
		}
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	domain := strings.ToLower(strings.Join(labels, "."))

	serves := make([]string, 0, len(l.Remotes)+2)
	for _, r := range l.Remotes {
		serves = append(serves, r.URL)
	}
	if len(serves) == 0 {
		serves = append(serves, l.Site, l.Repo)
	}
	for _, raw := range serves {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// ── scanning ────────────────────────────────────────────────────────────────────

const listingCols = `id, name, vendor, title, description, repo, site, version,
  transports, packages, remotes, registry, synced, hidden, featured, official, logo`

func scanListing(r rowScanner) (MCPListing, error) {
	var l MCPListing
	var transports, packages, remotes string
	var hidden, featured, official int
	if err := r.Scan(&l.ID, &l.Name, &l.Vendor, &l.Title, &l.Description, &l.Repo, &l.Site,
		&l.Version, &transports, &packages, &remotes, &l.Registry, &l.Synced,
		&hidden, &featured, &official, &l.Logo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MCPListing{}, ErrUnknownTool
		}
		return MCPListing{}, err
	}
	_ = json.Unmarshal([]byte(transports), &l.Transports)
	_ = json.Unmarshal([]byte(packages), &l.Packages)
	_ = json.Unmarshal([]byte(remotes), &l.Remotes)
	if l.Transports == nil {
		l.Transports = []string{}
	}
	l.Hidden, l.Featured, l.Official = hidden != 0, featured != 0, official != 0
	return l, nil
}

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
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
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
// One store, the shared discipline: one SQLite file under DataDir, opened through
// cek (encrypted at rest wherever the build can encrypt). Unlike every other store
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
	// Name is the publisher's reverse-DNS name, e.g. "com.acme/mcp".
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

// OpenCatalogStore opens (and migrates) the catalog at path.
func OpenCatalogStore(path string) (*CatalogStore, error) {
	db, err := cek.Open(cek.Global, path)
	if err != nil {
		return nil, fmt.Errorf("tools: open catalog store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tools: catalog pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`
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
  synced      INTEGER NOT NULL DEFAULT 0,
  hidden      INTEGER NOT NULL DEFAULT 0,
  featured    INTEGER NOT NULL DEFAULT 0,
  official    INTEGER NOT NULL DEFAULT 0,
  curated     INTEGER NOT NULL DEFAULT 0,
  logo        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS catalog_vendor ON catalog (vendor);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tools: catalog migrate: %w", err)
	}
	// A PLAIN client, deliberately: the SSRF guard on the server registry exists
	// because an ORG supplies that URL, and this one comes from the deployment's
	// own environment. Guarding it would protect against nobody and would refuse
	// the in-cluster mirror an air-gapped deployment must point at.
	return &CatalogStore{db: db, http: &http.Client{Timeout: 30 * time.Second}}, nil
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
}

// List returns the matching listings: featured first, then name.
func (s *CatalogStore) List(ctx context.Context, q Query) ([]MCPListing, error) {
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
		where = append(where, "(name LIKE ? OR title LIKE ? OR description LIKE ?)")
		like := "%" + t + "%"
		args = append(args, like, like, like)
	}
	stmt := `SELECT ` + listingCols + ` FROM catalog`
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY featured DESC, name"
	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("tools: list catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []MCPListing{}
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get returns one listing by id. A hidden listing is returned — the caller
// decides whether it may be shown, because a detail page and a curation edit
// address the same row.
func (s *CatalogStore) Get(ctx context.Context, id string) (MCPListing, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+listingCols+` FROM catalog WHERE id=?`, id)
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
	if c.Hidden != nil {
		sets, args = append(sets, "hidden=?"), append(args, boolInt(*c.Hidden))
	}
	if c.Featured != nil {
		sets, args = append(sets, "featured=?"), append(args, boolInt(*c.Featured))
	}
	if c.Official != nil {
		sets, args = append(sets, "official=?", "curated=1"), append(args, boolInt(*c.Official))
	}
	if c.Logo != nil {
		sets, args = append(sets, "logo=?"), append(args, strings.TrimSpace(*c.Logo))
	}
	if len(sets) == 0 {
		return s.Get(ctx, id)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE catalog SET `+strings.Join(sets, ", ")+` WHERE id=?`, append(args, id)...)
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
		for _, l := range batch {
			was, held := have[l.ID]
			if err := s.put(ctx, l); err != nil {
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
		if next == "" || len(batch) == 0 {
			return added, updated, nil
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

// registryURL is the upstream to sync from: the public registry, or the override
// an air-gapped deployment (or a test) points at a mirror with.
func registryURL() string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_TOOLS_REGISTRY")); v != "" {
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
func (s *CatalogStore) put(ctx context.Context, l MCPListing) error {
	transports, _ := json.Marshal(l.Transports)
	packages, _ := json.Marshal(l.Packages)
	remotes, _ := json.Marshal(l.Remotes)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO catalog (id, name, vendor, title, description, repo, site, version,
                     transports, packages, remotes, registry, digest, synced, official, logo)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
	out := make([]MCPListing, 0, len(page.Servers))
	for _, e := range page.Servers {
		v := e.Server
		vendor, _, ok := strings.Cut(v.Name, "/")
		if !ok || vendor == "" {
			continue // not a reverse-DNS name; not something we can address
		}
		l := MCPListing{
			ID: listingID(v.Name), Name: v.Name, Vendor: vendor, Title: v.Title,
			Description: v.Description, Repo: v.Repository.URL, Site: v.WebsiteURL,
			Version: v.Version, Registry: base,
		}
		if len(v.Icons) > 0 {
			l.Logo = v.Icons[0].Src
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
	serves = append(serves, l.Site, l.Repo)
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

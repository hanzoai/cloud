package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/outbound"
	_ "github.com/hanzoai/sqlite"
)

// External MCP servers (task 2). An org registers its OWN outside MCP server —
// a URL plus, optionally, an auth secret. The secret VALUE lives ONLY in KMS
// (per-org ref); SQLite keeps the URL, the header NAME to inject, and whether a
// secret is set. The server's tools surface through the SAME registry (SourceMCP,
// lowest precedence — an external tool can never shadow a native one) and dispatch
// through the SAME per-principal plane (activation-gated, priced, metered).

// maxMCPResponse bounds an external server's response body (DoS guard).
const maxMCPResponse = 4 << 20 // 4 MiB

// maxServers is how many external MCP servers one org may register. Every one is
// asked for its tools, and remembered, on the org's behalf, so the number an org
// can point this plane at is bounded where the rows are written.
const maxServers = 25

// ErrTooManyServers is a registration past maxServers.
var ErrTooManyServers = fmt.Errorf("tools: an org registers at most %d MCP servers", maxServers)

// MCPServer is one org-registered external MCP server. AuthHeader/HasSecret record
// how to authenticate; the secret itself is in KMS at authRef(org, id).
type MCPServer struct {
	// ID is the server's id within the org. It also PREFIXES every tool name the
	// server contributes, which is what keeps two servers' "search" apart.
	ID string `json:"id"`
	// Org is the org that registered the server — the validated caller's.
	Org string `json:"org"`
	// Name is the org's label for the server.
	Name string `json:"name"`
	// URL is the server's JSON-RPC endpoint. Always a public http(s) host: the
	// registration boundary and the dialer both refuse anything else.
	URL string `json:"url"`
	// AuthHeader is the request header the KMS-held credential is injected into,
	// e.g. "Authorization". Absent when the server needs no credential.
	AuthHeader string `json:"authHeader,omitempty"`
	// HasSecret is whether a credential is sealed in KMS for this server. The
	// VALUE is never returned by any route.
	HasSecret bool `json:"hasSecret"`
	// Listing is the catalog entry this server was enabled from, when it was.
	// Empty means the org typed the URL in itself.
	Listing string `json:"listing,omitempty"`
	// Source is where the registration came from: "catalog" when it was enabled
	// off the shelf, "org" when the org registered the URL itself. It is DERIVED
	// from Listing rather than stored, because two columns for one fact is two
	// chances to disagree.
	Source string `json:"source"`
	// Admitted is whether an admin of the org, or a SuperAdmin, registered the
	// server. Only an admitted server is carried into the org's agent runs: a
	// server registered before registering one took an admin is not, until an
	// admin registers it again.
	Admitted bool `json:"admitted"`
	// CreatedAt is when the server was registered, Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// Status is whether the server's tools could be listed when the org's
	// servers were read: "ok", "unreachable", "refused" or "error". Only the
	// listing answers it; a registration does not.
	Status string `json:"status,omitempty"`
	// Reason says why a server that is not "ok" is not, in words any member may
	// be shown: never the server's address.
	Reason string `json:"reason,omitempty"`
	// Tools is how many tools the server listed, when it listed them.
	Tools int `json:"tools,omitempty"`

	// urlRef is what the row holds in place of URL: its path in KMS.
	urlRef string
}

// source is where a registration came from. One fact, read one way.
func (s MCPServer) source() string {
	if s.Listing != "" {
		return "catalog"
	}
	return "org"
}

// MCPServerStore is the per-org registry of external MCP servers (one SQLite file,
// org column, the shared cloud store discipline). Isolation is a mandatory
// `WHERE org=?` on every statement; org is the validated principal value.
//
// mcp_servers.url is a KMS value (internal/escrow), kept at
// orgs/<org>/tools-mcp/<id>/url@default beside the credential at orgs/<org>/tools-mcp/<id>,
// and the column holds the path: a server's address may carry its credential in the query
// (a key in a query is a credential, which is why a member is shown the origin and not the
// URL). No WHERE or ORDER BY reads it. A read keeps the address for a minute, as a
// credential is kept (listedFor), and a revision or a delete drops it at once. Left plain:
// ids, the name the org gave, the header's NAME (the credential injected into it is in KMS),
// the listing it was enabled from and the flags.
//
// A file from before holds the address in plaintext or sealed as a field (internal/org),
// bound to the row's (org, id). The open writes each to KMS and replaces the column with
// its path; a column that holds a path is left alone, so a second open does nothing.
type MCPServerStore struct {
	db     *sql.DB
	fields *tenure.Fields // opens an address a file held before KMS; nil on a process with no data-plane master
	esc    *escrow.Escrow // keeps the addresses
	urls   sync.Map       // (org, id) -> *address, the address as KMS last answered it
}

// address is one server's URL as KMS last answered it.
type address struct {
	at  time.Time
	url string
}

// mcpSubsystem names the store: the system namespace's "tools-mcp". The database and the
// sealer are both made from it, so a column's key and its schema cannot name two things.
const mcpSubsystem = "tools-mcp"

// OpenMCPServerStore opens (and migrates) the external-server store under dir and moves
// what it held before KMS into KMS. master is cloud.Deps.MasterKey and opens only that old
// value; k is cloud.Deps.KMS and keeps every address. With no KMS an address is neither
// kept nor read.
func OpenMCPServerStore(dir string, master []byte, k escrow.KMS) (*MCPServerStore, error) {
	var fields *tenure.Fields
	if c, err := tenure.NewCipher(master); err == nil {
		if fields, err = c.Fields(namespace.System(), mcpSubsystem); err != nil {
			return nil, fmt.Errorf("tools: mcp-server store: %w", err)
		}
	}
	return locus.Use(context.Background(), nil, namespace.System(), mcpSubsystem, dir, func(db *sql.DB) (*MCPServerStore, error) {
		s := &MCPServerStore{db: db, fields: fields, esc: escrow.New(k, "tools-mcp")}
		ctx := context.Background()
		d := dialect.Of(db)
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS mcp_servers (
  id          TEXT NOT NULL,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  url         TEXT NOT NULL,
  auth_header TEXT NOT NULL DEFAULT '',
  has_secret  BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  listing     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org, id)
);`); err != nil {
				return err
			}
			// A column arrived after the table did, so a deployment that already has rows
			// gets it HERE — and before the index that reads it, or the index would be
			// the statement that fails on exactly the databases this exists for. listing
			// came first; admitted arrived after it, and a row that predates it was
			// written when any member could register a server, so it says no.
			for _, c := range []struct{ name, ddl string }{
				{"listing", "TEXT NOT NULL DEFAULT ''"},
				{"admitted", "BIGINT NOT NULL DEFAULT 0"},
			} {
				has, err := d.HasColumn(ctx, tx, "mcp_servers", c.name)
				if err != nil {
					return err
				}
				if has {
					continue
				}
				if _, err := tx.ExecContext(ctx, `ALTER TABLE mcp_servers ADD COLUMN `+c.name+` `+c.ddl); err != nil {
					return fmt.Errorf("%s: %w", c.name, err)
				}
			}
			// One enablement per (org, listing), enforced by the store rather than
			// remembered by a caller: enabling the same shelf entry twice is one server.
			if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS mcp_servers_listing
  ON mcp_servers (org, listing) WHERE listing <> ''`); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("tools: mcp-server migrate: %w", err)
		}
		if err := s.moveToKMS(ctx); err != nil {
			return nil, err
		}
		return s, nil
	})
}

// moveToKMS writes, once, the addresses a file holds from before KMS (plain, or sealed as a
// field) to KMS and replaces each column with its path. It runs outside the schema's
// transaction, because it calls KMS; it compares what it read, so another replica that
// moved the row first leaves nothing to change.
func (s *MCPServerStore) moveToKMS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org, id, url FROM mcp_servers WHERE url <> '' AND substr(url, 1, 4) <> 'kms:'`)
	if err != nil {
		return fmt.Errorf("tools: mcp-server migrate: %w", err)
	}
	type held struct{ org, id, url string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.org, &h.id, &h.url); err != nil {
			_ = rows.Close()
			return fmt.Errorf("tools: mcp-server migrate: %w", err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("tools: mcp-server migrate: %w", err)
	}
	for _, h := range found {
		url := h.url
		if strings.HasPrefix(url, "seal:") {
			if s.fields == nil {
				return fmt.Errorf("tools: mcp-server migrate: server %s: no data-plane master on this process, so an address the file held before KMS cannot be opened", h.id)
			}
			r := s.fields.Row("mcp_servers", h.org, h.id)
			if url = r.Open("url", url); r.Err() != nil {
				return fmt.Errorf("tools: mcp-server migrate: server %s: %w", h.id, r.Err())
			}
		}
		ref, err := s.esc.Put(ctx, h.org, h.id, "url", url)
		if err != nil {
			return fmt.Errorf("tools: mcp-server migrate: server %s: %w", h.id, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE mcp_servers SET url = $1 WHERE org = $2 AND id = $3 AND url = $4`,
			ref, h.org, h.id, h.url); err != nil {
			return fmt.Errorf("tools: mcp-server migrate: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *MCPServerStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Resolve answers what id a registration WOULD get, without writing anything.
//
// It exists because a registration is two writes — the row and the credential —
// and the credential's KMS ref is keyed on the id. Resolving first lets the seal
// happen BEFORE the row, which is the only ordering with no undo in it: a failed
// seal leaves the store exactly as it was, rather than leaving a row asserting a
// credential that was never stored. (It did leave one. A row with has_secret=1 and
// nothing in KMS makes every dispatch fail inside listRemote, which mcpProvider
// skips — so the server's tools SILENTLY vanish from the org's plane with no error
// anywhere. That is worse than the refusal it should have been.)
//
// want is a PREFERRED id, not a demand. Empty gets a random handle, which is what
// a hand-registered server has always had. A catalog enablement asks for the
// vendor's own handle instead, because the id PREFIXES every tool name the server
// contributes. Taken names are suffixed: two servers from one vendor is a thing an
// org is allowed to have. Re-enabling a listing the org already has resolves to
// THAT row's id, so the tool names an agent already learned do not move under it.
func (s *MCPServerStore) Resolve(ctx context.Context, org, listing, want string) (id string, fresh bool, err error) {
	if listing != "" {
		if cur, err := s.byListing(ctx, org, listing); err == nil {
			return cur.ID, false, nil
		}
	}
	id, err = s.handle(ctx, org, want)
	return id, true, err
}

// Write stores one registration at an id Resolve produced. It is the ONE place a
// registration comes into being — typed in or enabled off the shelf — and it
// touches no KMS: by the time it runs, the credential is already sealed.
//
// It reads, then writes, in the store's one write transaction: the cap is counted
// and the row inserted with no other registration between them, so two racing for
// the last place cannot both take it, and the second of two enabling the SAME
// listing — a double-clicked button is the ordinary way that happens — finds the
// row the first made and becomes the revise it would have been had it arrived a
// moment later, rather than a 500 the caller cannot act on.
func (s *MCPServerStore) Write(ctx context.Context, srv MCPServer, fresh bool) (MCPServer, error) {
	srv.Source = srv.source()
	if err := dialect.Text(srv.ID, srv.Org, srv.Name, srv.URL, srv.AuthHeader, srv.Listing); err != nil {
		return MCPServer{}, fmt.Errorf("tools: write mcp server: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return MCPServer{}, fmt.Errorf("tools: write mcp server: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if fresh && srv.Listing != "" {
		if cur, err := s.scanServer(tx.QueryRowContext(ctx,
			`SELECT `+serverCols+` FROM mcp_servers WHERE org=$1 AND listing=$2`, srv.Org, srv.Listing)); err == nil {
			srv.ID, fresh = cur.ID, false
		} else if !errors.Is(err, ErrUnknownTool) {
			return MCPServer{}, fmt.Errorf("tools: create mcp server: %w", err)
		}
	}
	if !fresh {
		cur, err := s.scanServer(tx.QueryRowContext(ctx,
			`SELECT `+serverCols+` FROM mcp_servers WHERE org=$1 AND id=$2`, srv.Org, srv.ID))
		if err != nil {
			return MCPServer{}, err
		}
		srv.CreatedAt = cur.CreatedAt
		// The address is kept in KMS inside the write lock, because only here is the id
		// final: a double-clicked enable lands on the row the first made.
		ref, err := s.esc.Put(ctx, srv.Org, srv.ID, "url", srv.URL)
		if err != nil {
			return MCPServer{}, fmt.Errorf("tools: revise mcp server: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE mcp_servers SET name=$1, url=$2, auth_header=$3, has_secret=$4, listing=$5, admitted=$6 WHERE org=$7 AND id=$8`,
			srv.Name, ref, srv.AuthHeader, boolInt(srv.HasSecret), srv.Listing, boolInt(srv.Admitted), srv.Org, srv.ID); err != nil {
			return MCPServer{}, fmt.Errorf("tools: revise mcp server: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return MCPServer{}, err
		}
		s.remember(srv)
		return srv, nil
	}
	srv.CreatedAt = time.Now().Unix()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_servers WHERE org=$1`, srv.Org).Scan(&n); err != nil {
		return MCPServer{}, fmt.Errorf("tools: create mcp server: %w", err)
	}
	if n >= maxServers {
		return MCPServer{}, ErrTooManyServers
	}
	ref, err := s.esc.Put(ctx, srv.Org, srv.ID, "url", srv.URL)
	if err != nil {
		return MCPServer{}, fmt.Errorf("tools: create mcp server: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, org, name, url, auth_header, has_secret, created_at, listing, admitted)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		srv.ID, srv.Org, srv.Name, ref, srv.AuthHeader, boolInt(srv.HasSecret), srv.CreatedAt, srv.Listing, boolInt(srv.Admitted)); err != nil {
		_ = s.esc.Drop(ctx, srv.Org, ref)
		return MCPServer{}, fmt.Errorf("tools: create mcp server: %w", err)
	}
	if err := tx.Commit(); err != nil {
		_ = s.esc.Drop(ctx, srv.Org, ref)
		return MCPServer{}, err
	}
	s.remember(srv)
	return srv, nil
}

// handle resolves a preferred id to a free one within the org: the preference
// itself when nothing holds it, then "-2", "-3", and a random handle when even
// those are taken or nothing was preferred.
func (s *MCPServerStore) handle(ctx context.Context, org, want string) (string, error) {
	want = sanitize(want)
	for i := 0; want != "" && i < 16; i++ {
		id := want
		if i > 0 {
			id = fmt.Sprintf("%s-%d", want, i+1)
		}
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM mcp_servers WHERE org=$1 AND id=$2`, org, id).Scan(&n); err != nil {
			return "", fmt.Errorf("tools: check mcp server id: %w", err)
		}
		if n == 0 {
			return id, nil
		}
	}
	return "m" + randHex(6), nil
}

// byListing is the org's server for one catalog listing, or ErrUnknownTool.
func (s *MCPServerStore) byListing(ctx context.Context, org, listing string) (MCPServer, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+serverCols+` FROM mcp_servers WHERE org=$1 AND listing=$2`, org, listing)
	return s.scanServer(row)
}

// sanitize reduces a preferred id to what a tool name may carry: lowercase
// letters, digits and dashes. An UNDERSCORE is dropped rather than mapped,
// because "<id>_<tool>" is cut on the FIRST underscore — an id containing one
// would take a bite out of every tool name it prefixes.
func sanitize(want string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(want)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

// brand is the handle an enabled listing's tools are prefixed with: the
// publisher's DOMAIN, read back out of the reverse-DNS namespace, dots to dashes.
// "com.stripe" → stripe-com. "io.github.alice" → alice-github-io.
//
// It names the whole domain and not the memorable label inside it, and that is the
// entire point. A namespace is issued against proof of the domain, so anyone may
// hold "sh.stripe" by owning stripe.sh — and a handle taken from one label would
// render that vendor's tools as "stripe_create_payment_link" on the very screen
// where an org pastes its real Stripe key. stripe-sh and stripe-com are two
// different things and say so. Longer, and true.
func brand(vendor string) string {
	labels := strings.Split(vendor, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, "-")
}

// Count is how many servers an org has registered.
func (s *MCPServerStore) Count(ctx context.Context, org string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_servers WHERE org=$1`, org).Scan(&n); err != nil {
		return 0, fmt.Errorf("tools: count mcp servers: %w", err)
	}
	return n, nil
}

// Get returns one server for (org, id), with its address.
func (s *MCPServerStore) Get(ctx context.Context, org, id string) (MCPServer, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+serverCols+` FROM mcp_servers WHERE org=$1 AND id=$2`, org, id)
	srv, err := s.scanServer(row)
	if err != nil {
		return MCPServer{}, err
	}
	return srv, s.reveal(ctx, &srv)
}

// List returns an org's registered servers, sorted by name.
func (s *MCPServerStore) List(ctx context.Context, org string) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serverCols+` FROM mcp_servers WHERE org=$1 ORDER BY name`, org)
	if err != nil {
		return nil, fmt.Errorf("tools: list mcp servers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MCPServer
	for rows.Next() {
		srv, err := s.scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Revealed once the cursor is closed: a file's one connection serves one query at a
	// time, and the reveal may wait on KMS.
	_ = rows.Close()
	for i := range out {
		if err := s.reveal(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Delete removes a server for (org, id) and asks KMS to forget its address. Returns
// whether a row was removed.
func (s *MCPServerStore) Delete(ctx context.Context, org, id string) (bool, error) {
	var ref string
	err := s.db.QueryRowContext(ctx, `SELECT url FROM mcp_servers WHERE org=$1 AND id=$2`, org, id).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("tools: delete mcp server: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("tools: delete mcp server: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	s.urls.Delete(org + "\x00" + id)
	if err := s.esc.Drop(ctx, org, ref); err != nil {
		return true, fmt.Errorf("tools: delete mcp server: the server is gone but KMS still holds its address: %w", err)
	}
	return true, nil
}

type rowScanner interface{ Scan(dest ...any) error }

const serverCols = `id, org, name, url, auth_header, has_secret, created_at, listing, admitted`

// scanServer reads a row. It names the address and does not read it; reveal does.
func (s *MCPServerStore) scanServer(r rowScanner) (MCPServer, error) {
	var srv MCPServer
	var hasSecret, admitted int
	if err := r.Scan(&srv.ID, &srv.Org, &srv.Name, &srv.urlRef, &srv.AuthHeader, &hasSecret,
		&srv.CreatedAt, &srv.Listing, &admitted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MCPServer{}, ErrUnknownTool
		}
		return MCPServer{}, err
	}
	srv.HasSecret = hasSecret != 0
	srv.Admitted = admitted != 0
	srv.Source = srv.source()
	return srv, nil
}

// reveal fills in the address srv's row names in KMS. It is kept for a minute, as a
// credential is (secret), so the registry that lists every server on every dispatch asks
// KMS once a minute, not once a call; a revision or a delete drops it at once.
func (s *MCPServerStore) reveal(ctx context.Context, srv *MCPServer) error {
	key := srv.Org + "\x00" + srv.ID
	if v, ok := s.urls.Load(key); ok {
		if a := v.(*address); time.Since(a.at) < listedFor {
			srv.URL = a.url
			return nil
		}
	}
	url, err := s.esc.Get(ctx, srv.Org, srv.urlRef)
	if err != nil {
		return fmt.Errorf("tools: mcp server %s: %w", srv.ID, err)
	}
	srv.URL = url
	s.urls.Store(key, &address{at: time.Now(), url: url})
	return nil
}

// remember keeps the address a write has just put in KMS, so the read that follows it is not
// a round trip.
func (s *MCPServerStore) remember(srv MCPServer) {
	s.urls.Store(srv.Org+"\x00"+srv.ID, &address{at: time.Now(), url: srv.URL})
}

// authRef is the KMS ref an external server's auth secret is sealed at, per org.
func authRef(org, id string) string { return "orgs/" + org + "/tools-mcp/" + id }

// mcpProvider surfaces every org-registered external MCP server's tools through the
// registry. It is one Provider; it holds the server store + a KMS client (auth) +
// an SSRF-guarded HTTP client. Tool wire names are "<serverID>_<remoteName>" so a
// dispatch resolves its server by the leading id with no per-call fan-out.
type mcpProvider struct {
	store    *MCPServerStore
	kms      secret.Store
	http     *http.Client
	seen     sync.Map     // (org, server, url) -> *listed
	sessions sync.Map     // (org, server, url) -> *shaken
	checked  sync.Map     // (org, server, url) -> checked
	secrets  sync.Map     // (org, server) -> *opened
	swept    atomic.Int64 // Unix seconds of the last eviction pass
}

func newMCPProvider(store *MCPServerStore, kms secret.Store) *mcpProvider {
	return &mcpProvider{store: store, kms: kms, http: guardedHTTPClient()}
}

// listedFor is how long one server's tool list is reused. A remote's tool set
// changes on the vendor's deploys, not on our requests, so a minute is far inside
// the window where re-asking teaches us nothing.
//
// Without it the cost was quadratic in the wrong place: EVERY dispatch resolves
// through the registry, which lists every provider, which asked every one of the
// org's servers over the network — so an org that enabled thirty listings turned
// each of its tool calls into thirty outbound HTTP requests, each with a 20s
// timeout. The MCP server lists on the same path, and tools/list is the method a
// client calls constantly.
const listedFor = time.Minute

// staleAfter is when a remembered list stops being worth the memory it sits in.
// A server that has not been asked about in an hour is one whose org deregistered
// it, or renamed it, or never comes back — and each entry holds a whole tool set,
// so a fleet that churns servers would otherwise accumulate them forever.
const staleAfter = time.Hour

// listed is one server's remembered tool list.
type listed struct {
	mu    sync.Mutex
	at    time.Time
	tools []remoteTool
	err   error
}

// opened is one server's credential as KMS last answered it.
type opened struct {
	mu    sync.Mutex
	at    time.Time
	value []byte
}

// secret is the server's credential, asked of KMS at most once per listedFor.
// A run relays every tools/call it makes, and each asked the store to open the
// same sealed value again. A refusal is not kept, so a credential sealed a moment
// later is read at once; a credential replaced or removed is dropped at once
// (drop), so the window only ever reuses the value the org last set.
func (p *mcpProvider) secret(ctx context.Context, srv MCPServer) ([]byte, error) {
	v, _ := p.secrets.LoadOrStore(srv.Org+"\x00"+srv.ID, &opened{})
	e := v.(*opened)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.at.IsZero() && time.Since(e.at) < listedFor {
		return e.value, nil
	}
	b, err := p.kms.GetSecret(ctx, authRef(srv.Org, srv.ID))
	if err != nil {
		e.value, e.at = nil, time.Time{}
		return nil, err
	}
	e.value, e.at = b, time.Now()
	return b, nil
}

// drop forgets a server's kept credential, for the handler that just replaced or
// removed it.
func (p *mcpProvider) drop(org, id string) {
	if p != nil {
		p.secrets.Delete(org + "\x00" + id)
	}
}

// sweep drops what has gone stale, at most once per staleAfter. It runs off the
// listing path rather than a goroutine: a provider with no traffic has nothing to
// forget, and a timer that outlives the process's interest in the data is one more
// thing to shut down.
func (p *mcpProvider) sweep() {
	last := p.swept.Load()
	now := time.Now()
	if now.Sub(time.Unix(last, 0)) < staleAfter || !p.swept.CompareAndSwap(last, now.Unix()) {
		return
	}
	p.seen.Range(func(k, v any) bool {
		e := v.(*listed)
		e.mu.Lock()
		stale := !e.at.IsZero() && now.Sub(e.at) > staleAfter
		e.mu.Unlock()
		if stale {
			p.seen.Delete(k)
		}
		return true
	})
	// A health reading past its window is taken again before it is shown.
	p.checked.Range(func(k, v any) bool {
		if now.Sub(v.(checked).at) >= listedFor {
			p.checked.Delete(k)
		}
		return true
	})
	// A session past its window is shaken again before it is used, so it is not
	// kept either.
	p.sessions.Range(func(k, v any) bool {
		e := v.(*shaken)
		e.mu.Lock()
		stale := now.Sub(e.at) >= sessionFor
		e.mu.Unlock()
		if stale {
			p.sessions.Delete(k)
		}
		return true
	})
	// A credential past its window is never read again as it is, so it is not
	// kept in memory until someone next asks.
	p.secrets.Range(func(k, v any) bool {
		e := v.(*opened)
		e.mu.Lock()
		stale := now.Sub(e.at) >= listedFor
		e.mu.Unlock()
		if stale {
			p.secrets.Delete(k)
		}
		return true
	})
}

func (p *mcpProvider) Source() Source { return SourceMCP }

// List fans out tools/list to each of the org's registered servers and returns
// their tools prefixed by server id. A server that errors is skipped — one bad
// external server never blanks the plane.
func (p *mcpProvider) List(ctx context.Context, scope Scope) ([]Tool, error) {
	if p.store == nil || scope.Org == "" {
		return nil, nil
	}
	servers, err := p.store.List(ctx, scope.Org)
	if err != nil {
		return nil, err
	}
	p.sweep()
	var out []Tool
	for _, srv := range servers {
		remote, err := p.tools(ctx, srv)
		if err != nil {
			continue
		}
		for _, rt := range remote {
			out = append(out, Tool{
				Name:         srv.ID + "_" + rt.Name,
				Source:       SourceMCP,
				Description:  rt.Description,
				Schema:       rt.InputSchema,
				Dispatchable: true,
			})
		}
	}
	return out, nil
}

// Dispatch resolves the server by the tool name's leading id and calls tools/call
// on it with the KMS-sourced auth header. The principal's org gates the lookup, so
// a caller can only ever reach its OWN registered servers.
func (p *mcpProvider) Dispatch(ctx context.Context, pr Principal, name string, args map[string]any) (any, error) {
	id, remoteName, ok := strings.Cut(name, "_")
	if !ok || p.store == nil || pr.Org == "" {
		return nil, ErrUnknownTool
	}
	srv, err := p.store.Get(ctx, pr.Org, id)
	if err != nil {
		return nil, ErrUnknownTool
	}
	return p.callRemote(ctx, srv, remoteName, args)
}

// remoteTool is a tool as returned by an external server's tools/list.
type remoteTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// tools is listRemote behind the per-server window. Keyed on the URL as well as
// the id, so re-pointing a server at a different endpoint is asked about at once
// rather than a minute later.
func (p *mcpProvider) tools(ctx context.Context, srv MCPServer) ([]remoteTool, error) {
	key := srv.Org + "\x00" + srv.ID + "\x00" + srv.URL
	v, _ := p.seen.LoadOrStore(key, &listed{})
	e := v.(*listed)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.at.IsZero() && time.Since(e.at) < listedFor {
		return e.tools, e.err
	}
	tools, err := p.listRemote(ctx, srv)
	if err != nil && ctx.Err() != nil {
		// The caller stopped waiting, which says nothing about the server, so it is
		// not remembered as the server's answer.
		return nil, err
	}
	e.tools, e.err, e.at = tools, err, time.Now()
	return e.tools, e.err
}

func (p *mcpProvider) listRemote(ctx context.Context, srv MCPServer) ([]remoteTool, error) {
	var all []remoteTool
	size := 0
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		var res struct {
			Tools      []remoteTool `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := p.rpc(ctx, srv, "tools/list", params, &res); err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			// A name the plane could not carry never reaches a model's list: the
			// prefixed name is the tool's whole identity from here on.
			if t.Name == "" || !validToolName(srv.ID+"_"+t.Name) {
				continue
			}
			if size += len(t.Name) + len(t.Description) + len(t.InputSchema); size > maxListBytes {
				return nil, errTooManyTools
			}
			all = append(all, t)
		}
		if res.NextCursor == "" || res.NextCursor == cursor {
			return all, nil
		}
		cursor = res.NextCursor
	}
	return all, nil
}

func (p *mcpProvider) callRemote(ctx context.Context, srv MCPServer, name string, args map[string]any) (any, error) {
	var res json.RawMessage
	if err := p.rpc(ctx, srv, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// ── Streamable HTTP ──────────────────────────────────────────────────────────────
//
// An external server is spoken to as the MCP Streamable HTTP transport specifies
// (2025-06-18, and the 2025-03-26 servers before it): every POST accepts JSON or
// an event stream, the first exchange is initialize, the session the server hands
// back rides every later request, and an answer may arrive as one JSON body or as
// server-sent events. A server that knows no initialize is spoken to as before, one
// bare JSON-RPC POST at a time.

// protocolVersion is the MCP revision this client asks for.
const protocolVersion = "2025-06-18"

// sessionFor is how long one handshake is reused. A server may forget a session
// sooner, which it says with a 404, and that is answered by shaking hands again.
const sessionFor = 10 * time.Minute

// maxListPages bounds a tools/list that pages: a server whose cursor never ends
// cannot hold a listing open forever.
const maxListPages = 20

// maxListBytes bounds one server's tool list — names, descriptions and schemas
// across every page — because the list is remembered for as long as the server
// is asked about. Far past any real server; 25 of them is 25 MiB at most.
const maxListBytes = 1 << 20 // 1 MiB

// errTooManyTools is a tool list past maxListBytes.
var errTooManyTools = errors.New("tools: the server's tool list is larger than this plane keeps")

// maxSessionID bounds an Mcp-Session-Id the server hands back before it is
// echoed into every later request.
const maxSessionID = 256

// revision is the shape of an MCP protocol revision: a date.
var revision = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// session is what initialize established with one server.
type session struct {
	id      string // Mcp-Session-Id; empty when the server keeps none
	version string // the negotiated revision; empty for a server that knows no initialize
}

// shaken is one server's session as the provider keeps it.
type shaken struct {
	mu sync.Mutex
	at time.Time // zero until a handshake has answered
	session
}

// errNothing is a server that answered a request with an empty body.
var errNothing = errors.New("tools: decode mcp response: the server answered nothing")

// statusErr is a server that answered with an HTTP status outside 2xx.
type statusErr struct{ code int }

func (e statusErr) Error() string { return fmt.Sprintf("tools: mcp server returned %d", e.code) }

// rpc performs one JSON-RPC 2.0 call to an external server over its session,
// injecting the KMS auth secret into srv.AuthHeader when set. It bounds the
// response, honors ctx, and surfaces a JSON-RPC error object as a Go error (fail
// closed — never a fake ok). A session the server has forgotten is established
// again once and the call retried once.
func (p *mcpProvider) rpc(ctx context.Context, srv MCPServer, method string, params any, out any) error {
	sess, err := p.session(ctx, srv)
	if err != nil {
		return err
	}
	res, err := p.send(ctx, srv, sess, method, params)
	var se statusErr
	if errors.As(err, &se) && se.code == http.StatusNotFound && sess.id != "" {
		p.forget(srv)
		if sess, err = p.session(ctx, srv); err != nil {
			return err
		}
		res, err = p.send(ctx, srv, sess, method, params)
	}
	if err != nil {
		return err
	}
	if out != nil && len(res) > 0 {
		if err := json.Unmarshal(res, out); err != nil {
			return fmt.Errorf("tools: decode mcp result: %w", err)
		}
	}
	return nil
}

// sessionKey names one server's session the way tools() names its list: a server
// re-pointed at another endpoint shakes hands with the new one.
func sessionKey(srv MCPServer) string { return srv.Org + "\x00" + srv.ID + "\x00" + srv.URL }

// session answers the server's live session, shaking hands when there is none.
func (p *mcpProvider) session(ctx context.Context, srv MCPServer) (session, error) {
	v, _ := p.sessions.LoadOrStore(sessionKey(srv), &shaken{})
	e := v.(*shaken)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.at.IsZero() && time.Since(e.at) < sessionFor {
		return e.session, nil
	}
	id, version, err := p.initialize(ctx, srv)
	if err != nil {
		return session{}, err
	}
	e.at, e.session = time.Now(), session{id: id, version: version}
	return e.session, nil
}

// forget drops a server's session, for the call it just failed.
func (p *mcpProvider) forget(srv MCPServer) { p.sessions.Delete(sessionKey(srv)) }

// initialize is the handshake: initialize, then notifications/initialized. A
// server that knows no initialize — it answers method-not-found, 400, 404 or 405,
// or nothing at all — is one that takes bare JSON-RPC, and is spoken to without a
// session.
func (p *mcpProvider) initialize(ctx context.Context, srv MCPServer) (id, version string, err error) {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "hanzo-cloud", "version": "1"},
	}
	res, hdr, err := p.post(ctx, srv, session{}, "initialize", params, false)
	var se statusErr
	var re rpcErr
	switch {
	case errors.As(err, &se) && (se.code == http.StatusBadRequest || se.code == http.StatusNotFound || se.code == http.StatusMethodNotAllowed),
		errors.As(err, &re) && re.code == -32601,
		errors.Is(err, errNothing):
		return "", "", nil
	case err != nil:
		return "", "", err
	}
	var ok struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(res, &ok)
	// Both are echoed into a header on every later request, so neither is taken
	// unless it has the shape the transport gives it: a revision is a date, and a
	// session id is visible ASCII. A malformed revision is answered with the one
	// asked for; a malformed session id is no session.
	version = ok.ProtocolVersion
	if !revision.MatchString(version) {
		version = protocolVersion
	}
	sess := session{id: hdr.Get("Mcp-Session-Id"), version: version}
	if !visible(sess.id) {
		sess.id = ""
	}
	if _, _, err := p.post(ctx, srv, sess, "notifications/initialized", nil, true); err != nil &&
		!errors.As(err, &se) {
		return "", "", err
	}
	return sess.id, sess.version, nil
}

// visible reports whether a session id is one to echo: 1 to maxSessionID
// characters of visible ASCII, as the transport specifies.
func visible(id string) bool {
	if id == "" || len(id) > maxSessionID {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

// send is one request over an established session.
func (p *mcpProvider) send(ctx context.Context, srv MCPServer, sess session, method string, params any) (json.RawMessage, error) {
	res, _, err := p.post(ctx, srv, sess, method, params, false)
	return res, err
}

// requestID numbers this process's requests, so an event stream's answer is told
// apart from anything else the server sends on it.
var requestID atomic.Int64

// post sends one JSON-RPC message and reads the answer to it. A notification
// carries no id and expects no answer.
func (p *mcpProvider) post(ctx context.Context, srv MCPServer, sess session, method string, params any, notify bool) (json.RawMessage, http.Header, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	id := requestID.Add(1)
	if !notify {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("tools: the server's address does not parse")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sess.id != "" {
		req.Header.Set("Mcp-Session-Id", sess.id)
	}
	if sess.version != "" {
		req.Header.Set("MCP-Protocol-Version", sess.version)
	}
	secret, err := p.authorize(ctx, srv, req)
	if err != nil {
		return nil, nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("tools: mcp %s: %w", method, unreachable{err})
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.Header, statusErr{resp.StatusCode}
	}
	if notify {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.Header, nil
	}
	var raw []byte
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		raw, err = readEvents(resp.Body, id)
	} else {
		raw, err = io.ReadAll(resp.Body)
		if err != nil {
			err = fmt.Errorf("tools: read mcp response: %w", err)
		}
	}
	if err != nil {
		return nil, resp.Header, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, resp.Header, errNothing
	}
	// The credential was sent to this server and nowhere else; an answer that
	// carries it back — in a result or in an error message — carries it to
	// whoever reads the answer, so it is struck out before anything reads it.
	raw = redact(raw, secret)
	res, err := answer(raw, id)
	return res, resp.Header, err
}

// authorize injects the server's KMS-held credential, when it has one, and
// answers the value it injected.
func (p *mcpProvider) authorize(ctx context.Context, srv MCPServer, req *http.Request) ([]byte, error) {
	if srv.AuthHeader == "" || !srv.HasSecret {
		return nil, nil
	}
	if p.kms == nil {
		return nil, errors.New("tools: KMS not configured; refusing to dispatch to authenticated MCP server")
	}
	secret, err := p.secret(ctx, srv)
	if err != nil {
		return nil, fmt.Errorf("tools: read mcp auth secret: %w", err)
	}
	req.Header.Set(srv.AuthHeader, string(secret))
	return secret, nil
}

// minRedact is the shortest credential struck out of an answer. Anything shorter
// is not a credential worth the name, and striking it would mangle ordinary text.
const minRedact = 6

// redact strikes the credential out of a server's answer: the value as injected,
// its token alone when it carries a scheme ("Bearer sk-…" → "sk-…"), and each as
// JSON would escape it.
func redact(raw, secret []byte) []byte {
	if len(secret) < minRedact {
		return raw
	}
	forms := [][]byte{secret}
	if i := bytes.IndexByte(secret, ' '); i > 0 && len(secret)-i-1 >= minRedact {
		forms = append(forms, secret[i+1:])
	}
	for _, f := range forms {
		raw = bytes.ReplaceAll(raw, f, []byte("[redacted]"))
		if esc, err := json.Marshal(string(f)); err == nil && len(esc) > 2 && !bytes.Equal(esc[1:len(esc)-1], f) {
			raw = bytes.ReplaceAll(raw, esc[1:len(esc)-1], []byte("[redacted]"))
		}
	}
	return raw
}

// rpcErr is a JSON-RPC error object the server answered with.
type rpcErr struct {
	code    int
	message string
}

func (e rpcErr) Error() string { return "tools: mcp server error: " + e.message }

// envelope is one JSON-RPC message as a server sends it.
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// isAnswer reports whether m answers a request rather than asking one or notifying.
func (m envelope) isAnswer() bool { return m.Result != nil || m.Error != nil }

// matches reports whether m answers request id: the number as sent, or the same
// digits quoted by a server that echoes ids as strings.
func (m envelope) matches(id int64) bool {
	n := strconv.FormatInt(id, 10)
	s := string(bytes.TrimSpace(m.ID))
	return s == n || s == `"`+n+`"`
}

// answer reads the result out of one JSON body: a single message, or a batch in
// which the answer to id is found.
//
// A single message is taken as the answer whatever id it carries. That is safe
// because nothing is multiplexed on a JSON body: it is the whole response to one
// POST this process sent, on its own request, so it cannot be anyone else's
// answer — and servers that echo ids loosely (a number as a string, or a fixed 1)
// are common. A batch or an event stream can carry several messages, and there
// only the answer that names this request is taken.
func answer(raw []byte, id int64) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	var msg envelope
	if len(raw) > 0 && raw[0] == '[' {
		var batch []envelope
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("tools: decode mcp response: %w", err)
		}
		found := false
		for _, m := range batch {
			if m.isAnswer() && m.matches(id) {
				msg, found = m, true
				break
			}
		}
		if !found {
			return nil, errors.New("tools: decode mcp response: the batch carries no answer")
		}
	} else if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("tools: decode mcp response: %w", err)
	}
	if msg.Error != nil {
		return nil, rpcErr{msg.Error.Code, msg.Error.Message}
	}
	return msg.Result, nil
}

// readEvents reads a server-sent event stream until the answer to request id
// arrives. Comments, other event types, the server's own requests and
// notifications, and answers to any other request are passed over; a stream that
// ends without the answer is an error.
func readEvents(r io.Reader, id int64) ([]byte, error) {
	br := bufio.NewReader(r)
	var data []string
	event := ""
	dispatch := func() []byte {
		defer func() { data, event = nil, "" }()
		if len(data) == 0 || (event != "" && event != "message") {
			return nil
		}
		raw := []byte(strings.Join(data, "\n"))
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var batch []envelope
			if json.Unmarshal(trimmed, &batch) == nil {
				for _, m := range batch {
					if m.isAnswer() && m.matches(id) {
						return raw
					}
				}
			}
			return nil
		}
		var m envelope
		if json.Unmarshal(trimmed, &m) != nil || !m.isAnswer() || !m.matches(id) {
			return nil
		}
		return raw
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("tools: read mcp response: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if got := dispatch(); got != nil {
				return got, nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if errors.Is(err, io.EOF) {
			if got := dispatch(); got != nil {
				return got, nil
			}
			return nil, errors.New("tools: the mcp server's event stream ended without an answer")
		}
	}
}

// healthWorkers is how many of an org's servers are asked at once when its
// servers are listed, and healthWait how long each is given. With maxServers,
// a listing waits at most ceil(25/8) × 3s = 12s, and only for servers not asked
// in the last minute.
const healthWorkers = 8

var healthWait = 3 * time.Second

// checked is one server's health as a listing last found it.
type checked struct {
	at     time.Time
	status string
	reason string
	tools  int
}

// health says, for each server, whether its tools could be listed: "ok" with how
// many, or why not. A server is asked at most once per listedFor on a listing's
// behalf, whatever it answered, by a bounded pool with a deadline of its own; an
// answer it gave in time also fills the tool cache the plane lists from, and one
// it did not give in time leaves that cache alone.
func (p *mcpProvider) health(ctx context.Context, servers []MCPServer) {
	if p == nil || len(servers) == 0 {
		return
	}
	jobs := make(chan *MCPServer)
	var wg sync.WaitGroup
	for w := 0; w < min(healthWorkers, len(servers)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for srv := range jobs {
				key := sessionKey(*srv)
				if v, ok := p.checked.Load(key); ok && time.Since(v.(checked).at) < listedFor {
					c := v.(checked)
					srv.Status, srv.Reason, srv.Tools = c.status, c.reason, c.tools
					continue
				}
				probe, cancel := context.WithTimeout(ctx, healthWait)
				tools, err := p.tools(probe, *srv)
				timedOut := probe.Err() != nil && ctx.Err() == nil
				cancel()
				if ctx.Err() != nil {
					continue // the caller left; nothing learned about the server
				}
				c := checked{at: time.Now()}
				c.status, c.reason = classify(err)
				if timedOut && err != nil {
					c.status, c.reason = "unreachable", "the server did not answer in time"
				}
				if err == nil {
					c.tools = len(tools)
				}
				p.checked.Store(key, c)
				srv.Status, srv.Reason, srv.Tools = c.status, c.reason, c.tools
			}
		}()
	}
	for i := range servers {
		jobs <- &servers[i]
	}
	close(jobs)
	wg.Wait()
}

// classify reduces a listing's error to a status and one of this plane's own
// sentences. No reason ever carries text the server wrote: a server is sent the
// org's credential, and anything it says back would otherwise be shown to every
// member of the org — the credential itself, or a message dressed as ours.
func classify(err error) (status, reason string) {
	var se statusErr
	var re rpcErr
	var un unreachable
	switch {
	case err == nil:
		return "ok", ""
	case errors.As(err, &un), errors.Is(err, context.DeadlineExceeded):
		return "unreachable", "the server did not answer"
	case errors.As(err, &se) && (se.code == http.StatusUnauthorized || se.code == http.StatusForbidden):
		return "refused", fmt.Sprintf("the server refused the credential (%d)", se.code)
	case errors.As(err, &se) && se.code == http.StatusNotAcceptable:
		return "refused", "the server refused the request's format (406)"
	case errors.As(err, &se) && se.code == http.StatusNotFound:
		return "error", "the server has no MCP endpoint at that address (404)"
	case errors.As(err, &se):
		return "error", fmt.Sprintf("the server answered %d", se.code)
	case errors.As(err, &re):
		return "error", "the server answered its tool list with an error"
	case errors.Is(err, errTooManyTools):
		return "error", fmt.Sprintf("the server's tool list is larger than %d KiB", maxListBytes>>10)
	case strings.Contains(err.Error(), "auth secret"), strings.Contains(err.Error(), "KMS"):
		return "error", "the server's credential could not be read"
	default:
		return "error", "the server's answer could not be read"
	}
}

// unreachable is a request to an org's server that got no answer, said without
// the address it was sent to. The transport's own error spells the whole URL, and
// an org may have registered one with a key in its query; this error reaches
// whoever called the tool — a member, or a run's harness in a sandbox its person
// can open. The cause stays reachable to errors.Is, never to the text, but for
// the guard's refusal, whose sentinel names no address and tells an attack from
// an outage.
type unreachable struct{ cause error }

func (e unreachable) Error() string {
	if errors.Is(e.cause, outbound.ErrRefused) {
		return "upstream unreachable: " + outbound.ErrRefused.Error()
	}
	return "upstream unreachable"
}

func (e unreachable) Unwrap() error { return e.cause }

// origin is a server's URL as a member is shown it: its scheme and host. The
// path and query are the admins' — a key in a query is a credential — and the
// scheme and host say which server it is.
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ── SSRF guard + URL validation ─────────────────────────────────────────────────

// validateServerURL enforces the registration boundary: only http/https, only a
// public host, and no credential in the address. It blocks an org from pointing a
// "tool" at cloud metadata / internal services (169.254.169.254, 10/8, localhost).
// Dial-time re-checks defend against DNS rebinding (guardedHTTPClient).
//
// A credential belongs in the secret, which is sealed in KMS and injected at the
// dial. One written into the URL as user:password@ is stored in the clear beside
// it and rides every request the transport logs, so it is refused here.
func validateServerURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must be http or https")
	}
	if u.User != nil {
		return errors.New("url must not carry a credential; give it as the secret, which is sealed in KMS")
	}
	if u.Host == "" {
		return errors.New("url must have a host")
	}
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return errors.New("url host is not permitted")
	}
	if a, err := netip.ParseAddr(host); err == nil && !outbound.Public(a) {
		return errors.New("url host resolves to a non-public address")
	}
	return nil
}

// guardedHTTPClient returns the outbound client: its dialer refuses any connection
// to a non-public IP — the SSRF defense that also catches DNS rebinding (the check
// runs on the resolved address at dial time, not the hostname at registration) —
// and a response past maxMCPResponse is an error.
//
// It follows NO redirect. A credential is injected into an ARBITRARY header name
// here, and one client serves every server, so no fixed list of secret headers
// can name it — an org that used X-Api-Key would have it replayed verbatim to
// wherever the server sent us. An MCP JSON-RPC endpoint has no reason to
// redirect, and the endpoint may come from a third party's catalog entry rather
// than from the org itself.
func guardedHTTPClient() *http.Client {
	return outbound.Client(outbound.Options{Timeout: 20 * time.Second, Limit: maxMCPResponse})
}

// maxSecret bounds a credential. Generous for a bearer token or an API key, and
// far under anything that is a mistake or an attempt to fill a store.
const maxSecret = 8 << 10

// validHeader is the HTTP field-name grammar (RFC 9110 token). Checked at the
// REGISTRATION boundary rather than at dispatch: net/http refuses a bad name when
// the request is built, which is hours later, in a background listing, where the
// symptom is a server whose tools quietly never appear.
var validHeader = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "000000000000"[:n*2]
	}
	return hex.EncodeToString(b)
}

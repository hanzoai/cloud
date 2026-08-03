package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/namespace"
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
	// CreatedAt is when the server was registered, Unix seconds.
	CreatedAt int64 `json:"createdAt"`
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
type MCPServerStore struct {
	db *sql.DB
}

// OpenMCPServerStore opens (and migrates) the external-server store under dir.
func OpenMCPServerStore(dir string) (*MCPServerStore, error) {
	db, err := cek.Open(namespace.System(), "tools-mcp", dir)
	if err != nil {
		return nil, fmt.Errorf("tools: open mcp-server store: %w", err)
	}
	sqlpool.Single(db)
	s := &MCPServerStore{db: db}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS mcp_servers (
  id          TEXT NOT NULL,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  url         TEXT NOT NULL,
  auth_header TEXT NOT NULL DEFAULT '',
  has_secret  INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  listing     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org, id)
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tools: mcp-server migrate: %w", err)
	}
	// The listing column arrived after the table did, so a deployment that already
	// has rows gets it HERE — and before the index that reads it, or the index
	// would be the statement that fails on exactly the databases this exists for.
	// "duplicate column" is the migration having already run, which is the only
	// outcome after the first boot; every other error is real and refuses the open.
	if _, err := db.Exec(`ALTER TABLE mcp_servers ADD COLUMN listing TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		_ = db.Close()
		return nil, fmt.Errorf("tools: mcp-server migrate listing: %w", err)
	}
	// One enablement per (org, listing), enforced by the store rather than
	// remembered by a caller: enabling the same shelf entry twice is one server.
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS mcp_servers_listing
  ON mcp_servers (org, listing) WHERE listing <> ''`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tools: mcp-server listing index: %w", err)
	}
	return s, nil
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
func (s *MCPServerStore) Write(ctx context.Context, srv MCPServer, fresh bool) (MCPServer, error) {
	srv.Source = srv.source()
	if !fresh {
		cur, err := s.Get(ctx, srv.Org, srv.ID)
		if err != nil {
			return MCPServer{}, err
		}
		srv.CreatedAt = cur.CreatedAt
		if _, err := s.db.ExecContext(ctx,
			`UPDATE mcp_servers SET name=?, url=?, auth_header=?, has_secret=?, listing=? WHERE org=? AND id=?`,
			srv.Name, srv.URL, srv.AuthHeader, boolInt(srv.HasSecret), srv.Listing, srv.Org, srv.ID); err != nil {
			return MCPServer{}, fmt.Errorf("tools: revise mcp server: %w", err)
		}
		return srv, nil
	}
	srv.CreatedAt = time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, org, name, url, auth_header, has_secret, created_at, listing) VALUES (?,?,?,?,?,?,?,?)`,
		srv.ID, srv.Org, srv.Name, srv.URL, srv.AuthHeader, boolInt(srv.HasSecret), srv.CreatedAt, srv.Listing); err != nil {
		// Resolve read, then this writes, and between the two another request may
		// have enabled the SAME listing — a double-clicked button is the ordinary
		// way that happens. The row it created is the row this one was going to
		// create, so this becomes the revise it would have been had it arrived a
		// moment later, rather than a 500 the caller cannot act on. Both unique
		// constraints land here: the (org, id) key and the (org, listing) index.
		if srv.Listing != "" && strings.Contains(err.Error(), "UNIQUE constraint") {
			if cur, gErr := s.byListing(ctx, srv.Org, srv.Listing); gErr == nil {
				srv.ID = cur.ID
				return s.Write(ctx, srv, false)
			}
		}
		return MCPServer{}, fmt.Errorf("tools: create mcp server: %w", err)
	}
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
			`SELECT COUNT(*) FROM mcp_servers WHERE org=? AND id=?`, org, id).Scan(&n); err != nil {
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
		`SELECT `+serverCols+` FROM mcp_servers WHERE org=? AND listing=?`, org, listing)
	return scanServer(row)
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

// Get returns one server for (org, id).
func (s *MCPServerStore) Get(ctx context.Context, org, id string) (MCPServer, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+serverCols+` FROM mcp_servers WHERE org=? AND id=?`, org, id)
	return scanServer(row)
}

// List returns an org's registered servers, sorted by name.
func (s *MCPServerStore) List(ctx context.Context, org string) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serverCols+` FROM mcp_servers WHERE org=? ORDER BY name`, org)
	if err != nil {
		return nil, fmt.Errorf("tools: list mcp servers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MCPServer
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// Delete removes a server for (org, id). Returns whether a row was removed.
func (s *MCPServerStore) Delete(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("tools: delete mcp server: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

type rowScanner interface{ Scan(dest ...any) error }

const serverCols = `id, org, name, url, auth_header, has_secret, created_at, listing`

func scanServer(r rowScanner) (MCPServer, error) {
	var srv MCPServer
	var hasSecret int
	if err := r.Scan(&srv.ID, &srv.Org, &srv.Name, &srv.URL, &srv.AuthHeader, &hasSecret,
		&srv.CreatedAt, &srv.Listing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MCPServer{}, ErrUnknownTool
		}
		return MCPServer{}, err
	}
	srv.HasSecret = hasSecret != 0
	srv.Source = srv.source()
	return srv, nil
}

// authRef is the KMS ref an external server's auth secret is sealed at, per org.
func authRef(org, id string) string { return "orgs/" + org + "/tools-mcp/" + id }

// mcpProvider surfaces every org-registered external MCP server's tools through the
// registry. It is one Provider; it holds the server store + a KMS client (auth) +
// an SSRF-guarded HTTP client. Tool wire names are "<serverID>_<remoteName>" so a
// dispatch resolves its server by the leading id with no per-call fan-out.
type mcpProvider struct {
	store *MCPServerStore
	kms   types.KMSClient
	http  *http.Client
	seen  sync.Map     // (org, server, url) -> *listed
	swept atomic.Int64 // Unix seconds of the last eviction pass
}

func newMCPProvider(store *MCPServerStore, kms types.KMSClient) *mcpProvider {
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
// timeout. The MCP door lists on the same path, and tools/list is the method a
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
	e.tools, e.err = p.listRemote(ctx, srv)
	e.at = time.Now()
	return e.tools, e.err
}

func (p *mcpProvider) listRemote(ctx context.Context, srv MCPServer) ([]remoteTool, error) {
	var res struct {
		Tools []remoteTool `json:"tools"`
	}
	if err := p.rpc(ctx, srv, "tools/list", nil, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (p *mcpProvider) callRemote(ctx context.Context, srv MCPServer, name string, args map[string]any) (any, error) {
	var res json.RawMessage
	if err := p.rpc(ctx, srv, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// rpc performs one JSON-RPC 2.0 call to an external server, injecting the KMS auth
// secret into srv.AuthHeader when set. It bounds the response, honors ctx, and
// surfaces a JSON-RPC error object as a Go error (fail closed — never a fake ok).
func (p *mcpProvider) rpc(ctx context.Context, srv MCPServer, method string, params any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("tools: build mcp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if srv.AuthHeader != "" && srv.HasSecret {
		if p.kms == nil {
			return errors.New("tools: KMS not configured; refusing to dispatch to authenticated MCP server")
		}
		secret, err := p.kms.GetSecret(ctx, authRef(srv.Org, srv.ID))
		if err != nil {
			return fmt.Errorf("tools: read mcp auth secret: %w", err)
		}
		req.Header.Set(srv.AuthHeader, string(secret))
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("tools: mcp %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponse))
	if err != nil {
		return fmt.Errorf("tools: read mcp response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tools: mcp server returned %d", resp.StatusCode)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("tools: decode mcp response: %w", err)
	}
	if env.Error != nil {
		return fmt.Errorf("tools: mcp server error: %s", env.Error.Message)
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("tools: decode mcp result: %w", err)
		}
	}
	return nil
}

// ── SSRF guard + URL validation ─────────────────────────────────────────────────

// validateServerURL enforces the registration boundary: only http/https, only a
// public host. It blocks an org from pointing a "tool" at cloud metadata / internal
// services (169.254.169.254, 10/8, localhost). Dial-time re-checks defend against
// DNS rebinding (guardedHTTPClient).
func validateServerURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must be http or https")
	}
	if u.Host == "" {
		return errors.New("url must have a host")
	}
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return errors.New("url host is not permitted")
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return errors.New("url host resolves to a non-public address")
	}
	return nil
}

// guardedHTTPClient returns an HTTP client whose dialer rejects any connection to a
// non-public IP — the SSRF defense that also catches DNS rebinding (the check runs
// on the resolved address at dial time, not the hostname at registration).
func guardedHTTPClient() *http.Client {
	base := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: 20 * time.Second,
		// A credential is injected into an ARBITRARY header name here, and Go
		// strips only Authorization and Cookie across a redirect — so an org that
		// used X-Api-Key would have it replayed verbatim to wherever the server
		// sent us. An MCP JSON-RPC endpoint has no reason to redirect, and the
		// endpoint may now come from a third party's catalog entry rather than
		// from the org itself.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if !isPublicIP(ip) {
						return nil, fmt.Errorf("tools: refusing to dial non-public address %s", ip)
					}
				}
				return base.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
			},
			MaxIdleConns:        16,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
}

// isPublicIP reports whether ip is a globally-routable unicast address (not
// loopback, private, link-local, multicast, or unspecified).
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
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

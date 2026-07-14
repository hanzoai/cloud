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
	"strings"
	"time"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/types"
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
	ID         string `json:"id"`
	Org        string `json:"org"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	AuthHeader string `json:"authHeader,omitempty"` // header to inject the KMS secret into (e.g. "Authorization").
	HasSecret  bool   `json:"hasSecret"`
	CreatedAt  int64  `json:"createdAt"`
}

// MCPServerStore is the per-org registry of external MCP servers (one SQLite file,
// org column, the shared cloud store discipline). Isolation is a mandatory
// `WHERE org=?` on every statement; org is the validated principal value.
type MCPServerStore struct {
	db *sql.DB
}

// OpenMCPServerStore opens (and migrates) the external-server store at path.
func OpenMCPServerStore(path string) (*MCPServerStore, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("tools: open mcp-server store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tools: mcp-server pragma %q: %w", pragma, err)
		}
	}
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
  PRIMARY KEY (org, id)
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tools: mcp-server migrate: %w", err)
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

// Create inserts a server row (id generated). It does NOT touch KMS — the handler
// stores the secret first, then records has_secret here.
func (s *MCPServerStore) Create(ctx context.Context, srv MCPServer) (MCPServer, error) {
	if srv.ID == "" {
		srv.ID = "m" + randHex(6)
	}
	srv.CreatedAt = time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, org, name, url, auth_header, has_secret, created_at) VALUES (?,?,?,?,?,?,?)`,
		srv.ID, srv.Org, srv.Name, srv.URL, srv.AuthHeader, boolInt(srv.HasSecret), srv.CreatedAt); err != nil {
		return MCPServer{}, fmt.Errorf("tools: create mcp server: %w", err)
	}
	return srv, nil
}

// Get returns one server for (org, id).
func (s *MCPServerStore) Get(ctx context.Context, org, id string) (MCPServer, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, org, name, url, auth_header, has_secret, created_at FROM mcp_servers WHERE org=? AND id=?`, org, id)
	return scanServer(row)
}

// List returns an org's registered servers, sorted by name.
func (s *MCPServerStore) List(ctx context.Context, org string) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, name, url, auth_header, has_secret, created_at FROM mcp_servers WHERE org=? ORDER BY name`, org)
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

func scanServer(r rowScanner) (MCPServer, error) {
	var srv MCPServer
	var hasSecret int
	if err := r.Scan(&srv.ID, &srv.Org, &srv.Name, &srv.URL, &srv.AuthHeader, &hasSecret, &srv.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MCPServer{}, ErrUnknownTool
		}
		return MCPServer{}, err
	}
	srv.HasSecret = hasSecret != 0
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
}

func newMCPProvider(store *MCPServerStore, kms types.KMSClient) *mcpProvider {
	return &mcpProvider{store: store, kms: kms, http: guardedHTTPClient()}
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
	var out []Tool
	for _, srv := range servers {
		remote, err := p.listRemote(ctx, srv)
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
	if !ok {
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

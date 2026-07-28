package wallets

// mpcclient.go is the thin typed client to the DEPLOYED luxfi/mpc ring's
// INTERNAL threshold API (github.com/luxfi/mpc cmd/mpcd/main.go, served on the
// node's --api port, :9800). This is the canonical server-to-server custody
// seam — the exact wire contract luxfi/mpc's own /sign handler documents for a
// custody adapter. cloud speaks the ring's real HTTP protocol WITHOUT importing
// github.com/luxfi/mpc (which would drag chi/Postgres/HSM/webauthn into the hot
// binary): the clients/mpc precedent applied to signing.
//
// SURFACE. The internal API is the raw t-of-n threshold primitive; org_id is the
// tenant scope on every call (no vault indirection — vaults/treasury-governance
// are a dashboard-API product surface, not the custody sign path):
//
//	POST /keygen {org_id, wallet_id?}
//	   → {wallet_id, result_type, ecdsa_pub_key, eddsa_pub_key, evm_address, ...}
//	POST /sign   {org_id, wallet_id, key_type, chain_id, payload_hash, idempotency_key}
//	   → {session_id, signature, status}   (200 + {status:"rejected",error} on refusal)
//	GET  /keys   → [key ...]
//
// AUTH. Every mutating route is gated on a STATIC bearer token — the ring's
// MPC_INTERNAL_API_KEY, shared across all nodes and sourced from KMS. No JWT, no
// jwt library, no extra dependency. The token is opaque: cloud never mints or
// derives it, it only presents the KMS-resolved value.
//
// A nil client, no nodes, or no key ⇒ not configured ⇒ every op fails CLOSED
// (ErrMPCNotConfigured); a signature is NEVER fabricated.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// mpcHTTPTimeout bounds each cluster call. Keygen can take up to ~60s
	// (distributed CGGMP21 rounds), so the ceiling is generous.
	mpcHTTPTimeout = 90 * time.Second
)

// Canonical cloud→mpc internal-API routes.
const (
	pathKeygen = "/keygen"
	pathSign   = "/sign"
	pathKeys   = "/keys"
)

// mpcClient is a failover client over one or more mpc node base URLs (the ring's
// per-pod :9800 endpoints). A nil client, no nodes, or no key ⇒ fails closed.
type mpcClient struct {
	nodes []string // base URLs, e.g. ["http://mpc-node-0.mpc-node-headless.hanzo-mpc.svc.cluster.local:9800", ...]
	key   []byte   // MPC_INTERNAL_API_KEY bearer token (from KMS)
	http  *http.Client
}

func newMPCClient(nodes []string, key []byte) *mpcClient {
	return &mpcClient{nodes: nodes, key: key, http: &http.Client{Timeout: mpcHTTPTimeout}}
}

// configured reports whether at least one node URL AND the bearer key are
// present. An address without a key still fails closed.
func (m *mpcClient) configured() bool {
	return m != nil && len(m.nodes) > 0 && len(m.key) > 0
}

// ── typed operations (the ring's internal threshold contract) ────────────────

// keygen → POST /keygen {org_id, wallet_id?} → {wallet_id, evm_address, ...}.
// An empty walletID lets the ring mint a deterministic one. Returns the ring's
// wallet id (the Sign handle) and the derived EVM address.
func (m *mpcClient) keygen(ctx context.Context, org, walletID string) (string, string, error) {
	body := map[string]any{"org_id": org}
	if walletID != "" {
		body["wallet_id"] = walletID
	}
	var resp keygenResp
	if err := m.do(ctx, http.MethodPost, pathKeygen, body, &resp); err != nil {
		return "", "", err
	}
	if resp.ResultType != "" && !strings.EqualFold(resp.ResultType, "success") {
		return "", "", fmt.Errorf("mpc: keygen %s: %s", resp.ResultType, resp.Error)
	}
	if resp.WalletID == "" || resp.EVMAddress == "" {
		return "", "", fmt.Errorf("mpc: keygen returned no wallet id/address")
	}
	return resp.WalletID, resp.EVMAddress, nil
}

// sign → POST /sign → {signature}. digest is the 32-byte hash to sign. chainID
// is the wallet's EVM chain id (0 when chain-agnostic). The idempotency key is
// derived deterministically from (org, walletID, digest) so a retried request
// returns the SAME signature and never triggers a second threshold round.
func (m *mpcClient) sign(ctx context.Context, org, walletID string, chainID int, digest []byte) ([]byte, error) {
	if walletID == "" {
		return nil, fmt.Errorf("mpc: sign requires a wallet id")
	}
	if len(digest) != 32 {
		return nil, fmt.Errorf("mpc: sign requires a 32-byte digest, got %d", len(digest))
	}
	hexHash := hex.EncodeToString(digest)
	body := map[string]any{
		"org_id":          org,
		"wallet_id":       walletID,
		"key_type":        "ecdsa",
		"chain_id":        chainID,
		"payload_hash":    hexHash,
		"idempotency_key": idempotencyKey(org, walletID, hexHash),
	}
	var resp signResp
	if err := m.do(ctx, http.MethodPost, pathSign, body, &resp); err != nil {
		return nil, err
	}
	// The ring returns 200 with status "rejected" + error on a sign refusal
	// (timeout / protocol error) — surface it rather than decode an empty sig.
	if resp.Status != "" && !strings.EqualFold(resp.Status, "success") && !strings.EqualFold(resp.Status, "signed") {
		return nil, fmt.Errorf("mpc: sign %s: %s", resp.Status, resp.Error)
	}
	return decodeSig(resp.Signature)
}

// keygenResp / signResp mirror the ring's internal-API JSON.
type keygenResp struct {
	WalletID   string `json:"wallet_id"`
	ResultType string `json:"result_type"`
	EVMAddress string `json:"evm_address"`
	Error      string `json:"error"`
}

type signResp struct {
	SessionID string `json:"session_id"`
	Signature string `json:"signature"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}

// idempotencyKey is a deterministic, org-scoped key for one (wallet, digest)
// sign — identical retries collapse to one threshold round (the ring caches on
// this key). Distinct digests never collide.
func idempotencyKey(org, walletID, hexHash string) string {
	h := sha256.Sum256([]byte(org + "|" + walletID + "|" + hexHash))
	return hex.EncodeToString(h[:])
}

// decodeSig parses a hex (optionally 0x-prefixed) signature into bytes.
func decodeSig(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("mpc: empty signature in response")
	}
	b, err := hex.DecodeString(trim0x(s))
	if err != nil {
		return nil, fmt.Errorf("mpc: signature not hex: %w", err)
	}
	return b, nil
}

// ── transport ────────────────────────────────────────────────────────────────

// do sends reqBody as JSON to the first node that answers 2xx, presenting the
// static bearer key, and decodes a 2xx body into out (out may be nil). Nodes are
// tried in order for failover.
func (m *mpcClient) do(ctx context.Context, method, path string, reqBody, out any) error {
	if !m.configured() {
		return ErrMPCNotConfigured
	}
	var payload []byte
	if reqBody != nil {
		var err error
		if payload, err = json.Marshal(reqBody); err != nil {
			return fmt.Errorf("mpc: marshal request: %w", err)
		}
	}
	var lastErr error
	for _, node := range m.nodes {
		u := strings.TrimRight(node, "/") + path
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+string(m.key))
		resp, err := m.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("mpc %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
			continue
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("mpc %s %s: decode response: %w", method, path, err)
		}
		return nil
	}
	return fmt.Errorf("mpc request failed: %w", lastErr)
}

// trim0x strips a leading 0x/0X.
func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

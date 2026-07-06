package wallets

// mpcclient.go is the thin typed REST client to the DEPLOYED luxfi/mpc cluster.
// It is the clients/mpcseal precedent applied to the wallet/vault/sign surface:
// cloud speaks the real service's HTTP contract WITHOUT importing
// github.com/luxfi/mpc (which would pull chi/Postgres/HSM/webauthn into the hot
// binary). The request/response JSON mirrors luxfi/mpc's REST handlers
// (pkg/api/handlers_{vaults,wallets,treasury}.go) so cloud is a faithful client.
//
// AUTH. luxfi/mpc gates every route on a JWT (issuer "mpc.lux.network", audience
// "mpc-api", claim org_id — see pkg/api/auth.go). We mint that HS256 token with
// the stdlib (crypto/hmac) — NO jwt library, NO extra dependency — from a shared
// secret pulled from KMS. The org_id claim carries the caller's tenant, so the
// cluster scopes the vault/wallet to the same org cloud validated.
//
// PATHS. The routes below are the canonical wallet-custody surface of the mpc
// REST API. The vault + wallet lifecycle maps to /v1/vaults + /v1/vaults/{id}/
// wallets verbatim; signing and treasury are the mpc spec's wallet-scoped sign
// and named-signer-governance routes. Divergences from the current server tree
// (which routes some signing via /v1/transactions and nests treasury under
// /v1/mpc/treasury) are intentional: this is the DECIDED cloud→mpc contract, and
// the only remaining step to go live is a running cluster at CLOUD_WALLETS_MPC_ADDR.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// mpcJWTIssuer / mpcJWTAudience match luxfi/mpc's validateJWT (pkg/api/auth.go).
	mpcJWTIssuer   = "mpc.lux.network"
	mpcJWTAudience = "mpc-api"
	// mpcJWTTTL bounds a minted token; each request mints a fresh short-lived one.
	mpcJWTTTL = 5 * time.Minute
	// mpcHTTPTimeout bounds each cluster call.
	mpcHTTPTimeout = 30 * time.Second
)

// Canonical cloud→mpc route templates (%s = url path segment).
const (
	pathVaults        = "/v1/vaults"
	pathVaultWallets  = "/v1/vaults/%s/wallets"
	pathWalletSign    = "/v1/wallets/%s/sign"
	pathWalletReshare = "/v1/wallets/%s/reshare"
	pathTreasury      = "/v1/treasury/wallets"
	pathTreasurySign  = "/v1/treasury/sign"
	pathTreasuryRotate = "/v1/treasury/wallets/%s/rotate-signer"
)

// mpcClient is a failover REST client over one or more mpc node base URLs. A
// nil client, no nodes, or no secret ⇒ not configured ⇒ every op fails closed.
type mpcClient struct {
	nodes  []string // base URLs, e.g. ["https://mpc.hanzo.ai"]
	secret []byte   // HS256 JWT signing secret (from KMS)
	http   *http.Client
}

func newMPCClient(nodes []string, secret []byte) *mpcClient {
	return &mpcClient{nodes: nodes, secret: secret, http: &http.Client{Timeout: mpcHTTPTimeout}}
}

// configured reports whether the cluster address AND the JWT secret are present.
// Both are required — an address without a secret still fails closed.
func (m *mpcClient) configured() bool {
	return m != nil && len(m.nodes) > 0 && len(m.secret) > 0
}

// ── typed operations (faithful subset of the mpc REST contract) ──────────────

// createVault → POST /v1/vaults {name} → {id}.
func (m *mpcClient) createVault(ctx context.Context, org, name string) (string, error) {
	var resp struct {
		ID string `json:"id"`
	}
	if err := m.do(ctx, org, http.MethodPost, pathVaults, map[string]any{"name": name}, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", fmt.Errorf("mpc: create vault returned empty id")
	}
	return resp.ID, nil
}

// createWallet → POST /v1/vaults/{id}/wallets {name,key_type,protocol} →
// {id, walletId, address|evmAddress}. Returns (walletID, address).
func (m *mpcClient) createWallet(ctx context.Context, org, vaultID, name string) (string, string, error) {
	body := map[string]any{"name": name, "key_type": "secp256k1", "protocol": "cggmp21"}
	var resp walletResp
	if err := m.do(ctx, org, http.MethodPost, fmt.Sprintf(pathVaultWallets, vaultID), body, &resp); err != nil {
		return "", "", err
	}
	addr := resp.address()
	if addr == "" {
		return "", "", fmt.Errorf("mpc: create wallet returned no address")
	}
	return resp.walletID(), addr, nil
}

// sign → POST /v1/wallets/{id}/sign {digest} → {signature}. digest is 32 bytes.
func (m *mpcClient) sign(ctx context.Context, org, walletID string, digest []byte) ([]byte, error) {
	if walletID == "" {
		return nil, fmt.Errorf("mpc: sign requires a wallet id")
	}
	body := map[string]any{"digest": "0x" + hex.EncodeToString(digest)}
	var resp struct {
		Signature string `json:"signature"`
	}
	if err := m.do(ctx, org, http.MethodPost, fmt.Sprintf(pathWalletSign, walletID), body, &resp); err != nil {
		return nil, err
	}
	return decodeSig(resp.Signature)
}

// reshare → POST /v1/wallets/{id}/reshare. Rolls shares; address unchanged.
func (m *mpcClient) reshare(ctx context.Context, org, walletID string) error {
	return m.do(ctx, org, http.MethodPost, fmt.Sprintf(pathWalletReshare, walletID), map[string]any{}, nil)
}

// createTreasury → POST /v1/treasury/wallets {name,chain} → {walletId, address}.
func (m *mpcClient) createTreasury(ctx context.Context, org, name, chain string) (string, string, error) {
	if chain == "" {
		chain = "evm"
	}
	body := map[string]any{"name": name, "chain": chain}
	var resp walletResp
	if err := m.do(ctx, org, http.MethodPost, pathTreasury, body, &resp); err != nil {
		return "", "", err
	}
	addr := resp.address()
	if addr == "" {
		return "", "", fmt.Errorf("mpc: create treasury wallet returned no address")
	}
	return resp.walletID(), addr, nil
}

// treasurySign → POST /v1/treasury/sign {walletId,digest} → {signature}.
func (m *mpcClient) treasurySign(ctx context.Context, org, walletID string, digest []byte) ([]byte, error) {
	body := map[string]any{"walletId": walletID, "digest": "0x" + hex.EncodeToString(digest)}
	var resp struct {
		Signature string `json:"signature"`
	}
	if err := m.do(ctx, org, http.MethodPost, pathTreasurySign, body, &resp); err != nil {
		return nil, err
	}
	return decodeSig(resp.Signature)
}

// rotateTreasurySigner → POST /v1/treasury/wallets/{id}/rotate-signer.
func (m *mpcClient) rotateTreasurySigner(ctx context.Context, org, walletID string) error {
	return m.do(ctx, org, http.MethodPost, fmt.Sprintf(pathTreasuryRotate, walletID), map[string]any{}, nil)
}

// walletResp accepts either the mpc-native evmAddress or the spec-named address,
// so the client works against the real cluster AND a faithful stub.
type walletResp struct {
	ID         string `json:"id"`
	WalletID   string `json:"walletId"`
	Address    string `json:"address"`
	EVMAddress string `json:"evmAddress"`
}

func (w walletResp) address() string {
	if w.Address != "" {
		return w.Address
	}
	return w.EVMAddress
}

func (w walletResp) walletID() string {
	if w.WalletID != "" {
		return w.WalletID
	}
	return w.ID
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

// do POSTs (or GETs) reqBody as JSON to the first node that answers 2xx, minting
// a fresh org-scoped JWT per attempt, and decodes a 2xx body into out (out may
// be nil to ignore the body). Nodes are tried in order for failover.
func (m *mpcClient) do(ctx context.Context, org, method, path string, reqBody, out any) error {
	if !m.configured() {
		return ErrMPCNotConfigured
	}
	token, err := mintMPCJWT(m.secret, org)
	if err != nil {
		return fmt.Errorf("mpc: mint jwt: %w", err)
	}
	var payload []byte
	if reqBody != nil {
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
		req.Header.Set("Authorization", "Bearer "+token)
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

// mintMPCJWT mints the HS256 token luxfi/mpc validates (pkg/api/auth.go): header
// {alg:HS256}, claims {org_id, role, iss, aud, iat, exp}. Hand-rolled on the
// stdlib so no jwt dependency enters go.mod. role "admin" satisfies the cluster's
// owner/admin route gates.
func mintMPCJWT(secret []byte, org string) (string, error) {
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"user_id": "cloud-wallets",
		"org_id":  org,
		"role":    "admin",
		"iss":     mpcJWTIssuer,
		"aud":     mpcJWTAudience,
		"iat":     now.Unix(),
		"exp":     now.Add(mpcJWTTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	signingInput := header + "." + b64url(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + b64url(mac.Sum(nil)), nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// trim0x strips a leading 0x/0X.
func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

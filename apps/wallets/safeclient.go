package wallets

// safeclient.go is the thin typed client to the DEPLOYED luxfi/mpc ring's
// DASHBOARD/product API (github.com/luxfi/mpc pkg/api, served on the node's
// dashboard port :8081, ClusterIP mpc-api-svc) — specifically its Safe / ERC-4337
// smart-wallet surface (pkg/api/handlers_smart_wallets.go). It is the custody seam
// for KindSafe: cloud composes the ring's Gnosis-Safe-style smart-wallet product
// WITHOUT importing github.com/luxfi/mpc (same rule as mpcclient.go).
//
// TWO AUTH PLANES, ONE RING. The :9800 INTERNAL threshold API (mpcclient.go) gates
// on a static bearer (MPC_INTERNAL_API_KEY). The :8081 PRODUCT API instead
// authenticates a JWT: HS256 over the ring's MPC_JWT_SECRET, iss=mpc.lux.network,
// aud=[mpc-api], carrying {user_id, org_id, role}. The Safe DEPLOY route is gated
// on role owner|admin (server.go), so cloud mints a SHORT-LIVED role="admin" token
// scoped to the tenant org. cloud never persists a ring token; it mints one per
// call. The HMAC secret is resolved from cloud's OWN in-process KMS
// (CLOUD_WALLETS_MPC_JWT_SECRET_REF) — NEVER a plaintext env value.
//
// ROUTES (server.go, under /v1):
//
//	POST /v1/wallets/{id}/smart-wallet {chain, wallet_type, owners, threshold}
//	     → deploy a counterfactual Safe owned by the MPC EOA (CREATE2 predicted addr)
//	GET  /v1/smart-wallets/{id}
//	     → the deployed Safe record
//	POST /v1/smart-wallets/{id}/propose {to,value,data,operation,chain_id,nonce}
//	     → the ring MPC-signs the EIP-712 Safe transaction hash (owner approval)
//
// A nil client, no base URL, or no secret ⇒ not configured ⇒ every op fails CLOSED
// (ErrMPCNotConfigured); a Safe/address/signature is NEVER fabricated.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// safeHTTPTimeout bounds each product-API call. Deploy/propose fold in a
// threshold sign (propose triggers an MPC round), so the ceiling is generous.
const safeHTTPTimeout = 90 * time.Second

// The ring's product-API JWT contract (pkg/api/auth.go). role="admin" clears the
// deploy gate (requireRole owner|admin) and every propose gate.
const (
	ringJWTIssuer   = "mpc.lux.network"
	ringJWTAudience = "mpc-api"
	ringJWTRole     = "admin"
	ringJWTSubject  = "cloud-wallets" // the user_id cloud presents as (server-to-server)
	ringJWTTTL      = 5 * time.Minute // short-lived: minted per request, never stored
)

// Canonical cloud→ring product-API routes.
const (
	pathDeploySmartWallet = "/v1/wallets/%s/smart-wallet" // %s = ring MPC wallet id
	pathGetSmartWallet    = "/v1/smart-wallets/%s"        // %s = smart wallet id
	pathProposeSafeTx     = "/v1/smart-wallets/%s/propose" // %s = smart wallet id
)

// safeClient talks to ONE ring product-API base URL. A nil client, empty base, or
// empty secret ⇒ fails closed.
type safeClient struct {
	base   string // e.g. http://mpc-api-svc.hanzo-mpc.svc.cluster.local:8081
	secret []byte // MPC_JWT_SECRET (HS256), resolved from KMS
	http   *http.Client
}

func newSafeClient(base string, secret []byte) *safeClient {
	return &safeClient{
		base:   strings.TrimRight(strings.TrimSpace(base), "/"),
		secret: secret,
		http:   &http.Client{Timeout: safeHTTPTimeout},
	}
}

// configured reports whether a base URL AND the HMAC secret are both present.
func (c *safeClient) configured() bool {
	return c != nil && c.base != "" && len(c.secret) > 0
}

// ── typed operations (the ring's Safe smart-wallet contract) ─────────────────

// safeWallet mirrors the ring's db.SmartWallet JSON (the subset cloud consumes).
type safeWallet struct {
	ID              string   `json:"id"`
	WalletID        string   `json:"walletId"`
	ContractAddress string   `json:"contractAddress"`
	Chain           string   `json:"chain"`
	WalletType      string   `json:"walletType"`
	Owners          []string `json:"owners"`
	Threshold       int      `json:"threshold"`
	Status          string   `json:"status"`
}

// SafeTx is the Safe transaction to propose (and MPC-sign the EIP-712 hash of).
type SafeTx struct {
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
	ChainID int64  `json:"chainId"`
	Nonce   int    `json:"nonce"`
}

// SafeTxResult is the outcome of a propose: the EIP-712 Safe-tx hash the ring
// computed and the threshold ECDSA signature (r,s) its MPC produced over it.
type SafeTxResult struct {
	SafeTxHash string `json:"safeTxHash"`
	R          string `json:"r"`
	S          string `json:"s"`
}

// mpcWallet mirrors the ring's db.Wallet JSON (the subset cloud consumes). ID is
// the DB primary key — the handle the Safe deploy route resolves (orm.Get); it is
// DISTINCT from WalletID, the internal threshold-key id used to threshold-sign.
type mpcWallet struct {
	ID         string `json:"id"`         // db.Wallet primary key — the Safe deploy handle
	WalletID   string `json:"walletId"`   // internal threshold-key id — the sign handle
	EVMAddress string `json:"evmAddress"` // the MPC EOA (the Safe owner)
}

// createVault creates a ring vault (the wallet's parent scope on the product API)
// and returns its id. The ring's Safe surface is vault-scoped: a wallet that a Safe
// can be deployed for MUST be created under a vault (POST /v1/vaults/{id}/wallets),
// which is the ONLY create path that persists a db.Wallet row AND returns its id.
func (c *safeClient) createVault(ctx context.Context, org, name string) (string, error) {
	var v struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/vaults", org, map[string]any{"name": name}, &v); err != nil {
		return "", err
	}
	if v.ID == "" {
		return "", fmt.Errorf("safe: create vault returned no id")
	}
	return v.ID, nil
}

// createWallet threshold-keygens a secp256k1 MPC wallet under vaultID and returns
// its db id (the Safe deploy handle), internal WalletID (the sign handle), and EOA.
func (c *safeClient) createWallet(ctx context.Context, org, vaultID, name string) (*mpcWallet, error) {
	body := map[string]any{"name": name, "key_type": "secp256k1", "protocol": "cggmp21"}
	var wl mpcWallet
	// Read-after-write race: the ring commits the vault AFTER it writes the
	// createVault 201, so this back-to-back createWallet can read-miss it.
	if err := c.doRetryNotFound(ctx, http.MethodPost, "/v1/vaults/"+vaultID+"/wallets", org, body, &wl, "vault not found"); err != nil {
		return nil, err
	}
	if wl.ID == "" || wl.WalletID == "" || wl.EVMAddress == "" {
		return nil, fmt.Errorf("safe: create wallet returned an incomplete wallet (id/walletId/evmAddress)")
	}
	return &wl, nil
}

// doRetryNotFound wraps do() with a bounded retry on the ring's read-after-write
// race: it commits a newly-created record (vault, wallet) AFTER writing the 201
// response, so cloud's back-to-back next call — fired microseconds apart on one
// keep-alive connection — can 404 on the just-created record. A slower client
// (curl, separate processes) never observes the gap, which is why manual repro
// succeeds. Retry ONLY that specific "<record> not found" 404 with short linear
// backoff (ctx-aware); every other error fails fast. do() unmarshals into out
// only on 2xx, so out is untouched across the retried 404s.
func (c *safeClient) doRetryNotFound(ctx context.Context, method, path, org string, reqBody, out any, notFound string) error {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 250 * time.Millisecond):
			}
		}
		lastErr = c.do(ctx, method, path, org, reqBody, out)
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(strings.ToLower(lastErr.Error()), notFound) {
			return lastErr // not the race — fail fast
		}
	}
	return lastErr
}

// deploySafe deploys a counterfactual Safe on chain, owned by the MPC EOA. The
// ring guarantees the MPC EOA is always an owner and returns the CREATE2-predicted
// contract address. threshold is the Safe m-of-n (default 1 = the single MPC EOA).
// walletID is the db.Wallet PRIMARY KEY (mpcWallet.ID), not the internal WalletID.
func (c *safeClient) deploySafe(ctx context.Context, org, walletID, chain string, owners []string, threshold int) (*safeWallet, error) {
	if walletID == "" {
		return nil, fmt.Errorf("safe: deploy requires the ring MPC wallet id")
	}
	if strings.TrimSpace(chain) == "" {
		return nil, fmt.Errorf("safe: deploy requires an EVM chain")
	}
	if threshold < 1 {
		threshold = 1
	}
	body := map[string]any{
		"chain":       chain,
		"wallet_type": "safe",
		"owners":      owners,
		"threshold":   threshold,
	}
	var sw safeWallet
	// Same read-after-write race one step later: the ring commits the db.Wallet
	// (createWallet) AFTER its 201, so this deploy can 404 "wallet not found" on it.
	if err := c.doRetryNotFound(ctx, http.MethodPost, fmt.Sprintf(pathDeploySmartWallet, walletID), org, body, &sw, "wallet not found"); err != nil {
		return nil, err
	}
	if sw.ID == "" || sw.ContractAddress == "" {
		return nil, fmt.Errorf("safe: deploy returned no id/address")
	}
	return &sw, nil
}

// getSafe reads a deployed Safe record (org-scoped by the ring).
func (c *safeClient) getSafe(ctx context.Context, org, swID string) (*safeWallet, error) {
	if swID == "" {
		return nil, fmt.Errorf("safe: get requires a smart wallet id")
	}
	var sw safeWallet
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf(pathGetSmartWallet, swID), org, nil, &sw); err != nil {
		return nil, err
	}
	return &sw, nil
}

// proposeSafeTx proposes a Safe transaction: the ring computes the EIP-712 Safe-tx
// hash (bound to the Safe contract + chain_id) and MPC-signs it, returning the
// hash + the threshold signature (r,s). operation is always 0 (Call); a Safe
// DelegateCall is deliberately not exposed here.
func (c *safeClient) proposeSafeTx(ctx context.Context, org, swID string, tx SafeTx) (*SafeTxResult, error) {
	if swID == "" {
		return nil, fmt.Errorf("safe: propose requires a smart wallet id")
	}
	if strings.TrimSpace(tx.To) == "" {
		return nil, fmt.Errorf("safe: propose requires a `to` address")
	}
	if tx.ChainID == 0 {
		return nil, fmt.Errorf("safe: propose requires a non-zero chain_id (EIP-712 domain)")
	}
	value := tx.Value
	if value == "" {
		value = "0"
	}
	body := map[string]any{
		"to":        tx.To,
		"value":     value,
		"data":      tx.Data,
		"operation": 0,
		"chain_id":  tx.ChainID,
		"nonce":     tx.Nonce,
	}
	// The ring returns {transaction, safe_tx_hash, signature_r, signature_s}.
	var resp struct {
		SafeTxHash string `json:"safe_tx_hash"`
		R          string `json:"signature_r"`
		S          string `json:"signature_s"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf(pathProposeSafeTx, swID), org, body, &resp); err != nil {
		return nil, err
	}
	if resp.SafeTxHash == "" {
		return nil, fmt.Errorf("safe: propose returned no safe_tx_hash")
	}
	return &SafeTxResult{SafeTxHash: resp.SafeTxHash, R: resp.R, S: resp.S}, nil
}

// ── transport ────────────────────────────────────────────────────────────────

// do mints a fresh org-scoped admin JWT, sends reqBody as JSON (nil for GET),
// and decodes a 2xx body into out (out may be nil).
func (c *safeClient) do(ctx context.Context, method, path, org string, reqBody, out any) error {
	if !c.configured() {
		return ErrMPCNotConfigured
	}
	token, err := c.mintJWT(org)
	if err != nil {
		return err
	}
	var payload []byte
	if reqBody != nil {
		if payload, err = json.Marshal(reqBody); err != nil {
			return fmt.Errorf("safe: marshal request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("safe %s %s: %w", method, path, err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("safe %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("safe %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// mintJWT builds a short-lived HS256 ring product-API token scoped to org with
// role=admin. Hand-rolled (crypto/hmac + base64url) so cloud takes on NO JWT
// dependency — the exact wire the ring's jwt.ParseWithClaims validates:
// iss=mpc.lux.network, aud=[mpc-api], HS256 over MPC_JWT_SECRET.
func (c *safeClient) mintJWT(org string) (string, error) {
	if strings.TrimSpace(org) == "" {
		return "", fmt.Errorf("safe: mint jwt requires an org")
	}
	now := time.Now()
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	claims := map[string]any{
		"user_id": ringJWTSubject,
		"org_id":  org,
		"role":    ringJWTRole,
		"iss":     ringJWTIssuer,
		"aud":     []string{ringJWTAudience},
		"iat":     now.Unix(),
		"exp":     now.Add(ringJWTTTL).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("safe: marshal jwt header: %w", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("safe: marshal jwt claims: %w", err)
	}
	signingInput := b64url(hb) + "." + b64url(cb)
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(signingInput))
	return signingInput + "." + b64url(mac.Sum(nil)), nil
}

// b64url is JWT base64url (no padding).
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

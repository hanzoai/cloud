// topup.go ports console's app/billing/v1/topup/wallet/route.ts into the unified
// binary at POST /v1/console/topup/wallet (task #41). It is the verify-and-record
// seam for an HUSD wallet top-up: the browser sends an HUSD ERC-20 transfer to the
// treasury and posts the tx hash here; this handler reads the receipt from the Hanzo
// EVM, confirms it is a mined, successful HUSD Transfer(from → treasury, value),
// derives USD cents from the (18-decimal, USD-pegged) on-chain value, records it to
// commerce as an `husd` crypto payment, and returns the credited amount + balance.
//
// THE CREDITED AMOUNT IS THE ON-CHAIN VALUE, never a client number — which is exactly
// why this MUST be a server handler and cannot collapse to a same-origin call. Two
// hardenings over the Node route:
//
//   - IDOR-safe: the credit lands on the VALIDATED caller's own org/user (the
//     gateway-verified X-Org-Id/X-User-Id), never a client-supplied `userId`.
//   - S2S to commerce: recorded with the admin COMMERCE_SERVICE_TOKEN + the caller's
//     X-Org-Id (the same service-to-service pattern clients/admin reads balances on),
//     not by forwarding a browser cookie.
//
// The EVM receipt is read over plain JSON-RPC (eth_getTransactionReceipt) — one
// well-known call + one well-known event, so the stdlib is sufficient and no EVM
// client dependency is pulled in.
//
// Honest failure (no fabricated credit, ever): HUSD/treasury unconfigured
// (greenfield — HUSD not yet deployed) → 501; a missing/failed/non-HUSD-to-treasury
// tx → 400; the chain or commerce unreachable → 502.
package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/zap-proto/zip"
)

// transferTopic is keccak256("Transfer(address,address,uint256)") — the ERC-20
// Transfer event signature, topics[0] of every transfer log. A universally-fixed
// constant (no need to hash at runtime).
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// husdCentDivisor: HUSD is an 18-decimal, USD-pegged stablecoin, so 1e16 base units
// = 1 cent. Mirrors route.ts's `value / 10n ** 16n`.
var husdCentDivisor = new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil)

var (
	addrRe   = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	txHashRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

func isAddr(a string) bool { return addrRe.MatchString(a) }

// topupConfig is the deployment's HUSD + commerce wiring, resolved from server-only
// env (sourced from KMS by the deployment, never a browser value).
type topupConfig struct {
	husd     string // HUSD ERC-20 contract address
	treasury string // the top-up treasury address
	rpcURL   string // Hanzo EVM JSON-RPC endpoint
	chainID  int64
	commerce string // commerce base (e.g. http://commerce.hanzo.svc.cluster.local:8001)
	token    string // admin S2S bearer for commerce (COMMERCE_SERVICE_TOKEN; never logged)
}

func loadTopupConfig() topupConfig {
	chainID := int64(36963)
	if v := strings.TrimSpace(os.Getenv("HANZO_CHAIN_ID")); v != "" {
		if n, ok := new(big.Int).SetString(v, 10); ok {
			chainID = n.Int64()
		}
	}
	return topupConfig{
		husd:     strings.TrimSpace(os.Getenv("HANZO_HUSD_ADDRESS")),
		treasury: strings.TrimSpace(os.Getenv("HANZO_HUSD_TREASURY")),
		rpcURL:   strings.TrimRight(getenv("HANZO_RPC_URL", "https://rpc.hanzo.network"), "/"),
		chainID:  chainID,
		commerce: strings.TrimRight(getenv("COMMERCE_URL", "https://api.hanzo.ai"), "/"),
		token:    strings.TrimSpace(os.Getenv("COMMERCE_SERVICE_TOKEN")),
	}
}

// configured reports whether the greenfield gate is satisfied — a valid HUSD contract
// AND treasury. Until HUSD is deployed both are unset and the surface is honestly 501.
func (t topupConfig) configured() bool { return isAddr(t.husd) && isAddr(t.treasury) }

type walletTopupReq struct {
	TxHash      string `json:"txHash"`
	FromAddress string `json:"fromAddress"`
	// A client-supplied `userId` is intentionally NOT read — the credit lands on the
	// validated caller (no IDOR).
}

type walletTopupResp struct {
	CreditedCents int64  `json:"creditedCents"`
	Balance       int64  `json:"balance"`
	TxHash        string `json:"txHash"`
	Status        string `json:"status"`
}

// walletTopup verifies a sent HUSD transfer on-chain and credits the caller's org.
// Mirrors POST app/billing/v1/topup/wallet/route.ts.
func (s *svc) walletTopup(c *zip.Ctx) error {
	cfg := loadTopupConfig()
	// Greenfield gate: no HUSD contract / treasury ⇒ honest "not configured yet".
	if !cfg.configured() {
		return zip.Errorf(http.StatusNotImplemented, "HUSD top-up is not configured yet (HUSD is not deployed on Hanzo Mainnet)")
	}

	// The credit lands on the VALIDATED caller's own org (X-Org-Id) — require it.
	cr, ok := resolveCaller(c, true)
	if !ok {
		return zip.ErrForbidden("sign in to top up your balance")
	}

	var body walletTopupReq
	if err := c.Bind(&body); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	txHash := strings.TrimSpace(body.TxHash)
	if !txHashRe.MatchString(txHash) {
		return zip.ErrBadRequest("a valid transaction hash is required")
	}

	// ── 1. Verify the HUSD transfer on-chain ─────────────────────────────────────
	cents, verifiedFrom, herr := verifyHusdTransfer(c.Context(), cfg, txHash, strings.TrimSpace(body.FromAddress))
	if herr != nil {
		return herr
	}

	// ── 2. Record to commerce as an HUSD crypto payment (S2S) ────────────────────
	status, herr := recordHusdPayment(c.Context(), cfg, cr, txHash, verifiedFrom, cents)
	if herr != nil {
		return herr
	}

	// New USD-ledger balance — best-effort; the credit already landed.
	balance := commerceBalanceCents(c.Context(), cfg, cr)

	return c.JSON(http.StatusOK, walletTopupResp{CreditedCents: cents, Balance: balance, TxHash: txHash, Status: status})
}

// ── on-chain verification (plain JSON-RPC) ───────────────────────────────────────

// rpcReceipt is the subset of an eth_getTransactionReceipt result we read.
type rpcReceipt struct {
	Status string   `json:"status"` // "0x1" success, "0x0" failed
	Logs   []rpcLog `json:"logs"`
}

type rpcLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// verifyHusdTransfer reads the receipt and confirms exactly one mined, successful
// HUSD Transfer to the treasury, returning the credited cents and the sender. Any
// non-conforming tx is an honest 400; an unreachable chain is a 502.
func verifyHusdTransfer(ctx context.Context, cfg topupConfig, txHash, wantFrom string) (int64, string, error) {
	rcpt, err := getReceipt(ctx, cfg.rpcURL, txHash)
	if err != nil {
		return 0, "", zip.Errorf(http.StatusBadGateway, "could not verify the transaction on Hanzo Mainnet: %v", err)
	}
	if rcpt == nil {
		return 0, "", zip.ErrBadRequest("transaction not found or not yet mined")
	}
	if strings.ToLower(rcpt.Status) != "0x1" {
		return 0, "", zip.ErrBadRequest("transaction failed on-chain")
	}

	husd := strings.ToLower(cfg.husd)
	treasuryTopic := addrToTopic(cfg.treasury)
	for _, lg := range rcpt.Logs {
		if strings.ToLower(lg.Address) != husd {
			continue
		}
		if len(lg.Topics) < 3 || strings.ToLower(lg.Topics[0]) != transferTopic {
			continue
		}
		if strings.ToLower(lg.Topics[2]) != treasuryTopic { // indexed `to`
			continue
		}
		value, ok := new(big.Int).SetString(strings.TrimPrefix(lg.Data, "0x"), 16)
		if !ok {
			continue
		}
		cents := new(big.Int).Div(value, husdCentDivisor)
		if cents.Sign() <= 0 || !cents.IsInt64() {
			return 0, "", zip.ErrBadRequest("transferred amount is below the minimum (1 cent)")
		}
		from := topicToAddr(lg.Topics[1]) // indexed `from`
		if wantFrom != "" && isAddr(wantFrom) && !strings.EqualFold(from, wantFrom) {
			return 0, "", zip.ErrBadRequest("transfer sender does not match the connected wallet")
		}
		return cents.Int64(), from, nil
	}
	return 0, "", zip.ErrBadRequest("no HUSD transfer to the treasury was found in this transaction")
}

// getReceipt calls eth_getTransactionReceipt over JSON-RPC. A null result (not mined)
// returns (nil,nil); a transport / RPC error propagates.
func getReceipt(ctx context.Context, rpcURL, txHash string) (*rpcReceipt, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_getTransactionReceipt", "params": []string{txHash},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpc unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rpc status %d", resp.StatusCode)
	}
	var env struct {
		Result *rpcReceipt `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("rpc decode: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", env.Error.Message)
	}
	return env.Result, nil // Result==nil ⇒ not found / not yet mined
}

// addrToTopic left-pads a 20-byte address to a 32-byte indexed topic (lowercase).
func addrToTopic(addr string) string {
	h := strings.ToLower(strings.TrimPrefix(addr, "0x"))
	return "0x" + strings.Repeat("0", 64-len(h)) + h
}

// topicToAddr extracts the 20-byte address from a 32-byte indexed topic (lowercase,
// 0x-prefixed).
func topicToAddr(topic string) string {
	h := strings.TrimPrefix(topic, "0x")
	if len(h) < 40 {
		return "0x" + h
	}
	return "0x" + h[len(h)-40:]
}

// ── commerce (S2S) ───────────────────────────────────────────────────────────────

// recordHusdPayment records the verified credit to commerce as an `husd` crypto
// payment, scoped to the caller's org via the S2S service token + X-Org-Id.
func recordHusdPayment(ctx context.Context, cfg topupConfig, cr caller, txHash, from string, cents int64) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"method":      "crypto",
		"network":     "hanzo",
		"chainId":     cfg.chainID,
		"currency":    "husd",
		"amount":      cents,
		"txHash":      txHash,
		"fromAddress": from,
		"toAddress":   cfg.treasury,
		"userId":      cr.id, // the VALIDATED caller — never a client-supplied id
	})
	raw, status, err := commerceDo(ctx, cfg.commerce, cfg.token, http.MethodPost, "/v1/billing/payment", nil, cr.owner, payload)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "could not reach commerce to record the payment: %v", err)
	}
	if status < 200 || status >= 300 {
		return "", zip.Errorf(http.StatusBadGateway, "commerce rejected the payment (HTTP %d): %s", status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Status == "" {
		out.Status = "recorded"
	}
	return out.Status, nil
}

// commerceBalanceCents reads the caller's USD-ledger balance (cents). Best-effort:
// the credit is already recorded, so a read error degrades to 0, not a failure.
func commerceBalanceCents(ctx context.Context, cfg topupConfig, cr caller) int64 {
	q := url.Values{"user": {cr.id}, "currency": {"usd"}}
	raw, status, err := commerceDo(ctx, cfg.commerce, cfg.token, http.MethodGet, "/v1/billing/balance", q, cr.owner, nil)
	if err != nil || status < 200 || status >= 300 {
		return 0
	}
	var b struct {
		Balance   int64 `json:"balance"`
		Available int64 `json:"available"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return 0
	}
	if b.Balance != 0 {
		return b.Balance
	}
	return b.Available
}

// commerceDo performs one S2S commerce request: admin bearer + X-Org-Id (commerce's
// EdgeAuth trusts the org header ONLY behind the service token). Returns the raw
// body + status. Mirrors clients/admin/commerce.go's auth. Takes (base, token) rather
// than the HUSD topupConfig so both the wallet top-up AND the /v1/billing/* data bridge
// (billing.go) share this ONE S2S transport.
func commerceDo(ctx context.Context, base, token, method, path string, q url.Values, org string, body []byte) ([]byte, int, error) {
	if base == "" {
		return nil, 0, fmt.Errorf("commerce not configured")
	}
	u := base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

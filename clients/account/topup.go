// topup.go is the verify-and-record seam for a crypto wallet top-up: the browser
// sends a USD-pegged ERC-20 transfer to our treasury and posts the tx hash here;
// this handler reads the receipt from that chain, confirms it is a mined, successful
// Transfer(from → treasury, value), derives USD cents from the on-chain value using
// the TOKEN'S OWN decimals, records it to commerce, and returns the credited amount
// plus the new balance.
//
// A rail is one accepted (chain, token, treasury) triple, configured as data in
// TOPUP_RAILS and discoverable at GET /v1/commerce/topup/rails. This replaced a
// single hardcoded HUSD-on-Hanzo-Mainnet pair: HUSD is not deployed, so the surface
// was permanently 501 — complete, correct and unable to take a cent. Customers
// already hold USDC on Base/Ethereum/Polygon, so accepting the assets they have is
// what makes this earn.
//
// THE CREDITED AMOUNT IS THE ON-CHAIN VALUE, never a client number — which is exactly
// why this MUST be a server handler and cannot collapse to a same-origin call. Three
// properties worth keeping:
//
//   - IDOR-safe: the credit lands on the VALIDATED caller's own org/user (the
//     gateway-verified X-Org-Id/X-User-Id), never a client-supplied `userId`.
//   - S2S to commerce: recorded with the admin COMMERCE_SERVICE_TOKEN + the caller's
//     X-Org-Id (the same service-to-service pattern clients/admin reads balances on),
//     not by forwarding a browser cookie.
//   - Per-rail decimals: cents come from 10^(decimals-2), so a 6-decimal USDC and an
//     18-decimal token cannot be priced with one another's divisor.
//
// The EVM receipt is read over plain JSON-RPC (eth_getTransactionReceipt) — one
// well-known call + one well-known event, so the stdlib is sufficient and no EVM
// client dependency is pulled in. That also means a new chain costs no new code.
//
// Honest failure (no fabricated credit, ever): no rail configured → 501; an unknown
// rail, or a missing/failed/non-matching tx → 400; the chain or commerce unreachable
// → 502.
package account

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
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/transport"
	"github.com/zap-proto/zip"
)

// httpClient is the shared outbound client for the account subsystem's plain-HTTP seams
// (EVM JSON-RPC, commerce billing/store S2S). Small JSON envelopes, bounded reads; 15s
// is generous for an in-cluster / same-region hop. (Owned here — the S2S transport home
// — since the former waitlist.go was retired with the /v1/console namespace.)
var httpClient = &http.Client{Timeout: 15 * time.Second}

// commerceHTTP is the client for the commerce S2S seam ONLY (commerceDo). Separate
// from httpClient (which also dials EVM JSON-RPC) so that — when commerce is folded
// in-process (task #111) — commerce calls dispatch to the in-process handler via
// the commerce transport's self-routing dispatch (no socket to the standalone), while the
// HUSD chain RPC keeps going over the real network. Off the co-resident path it is a
// plain HTTP client, exactly like before.
var commerceHTTP = transport.Client(15 * time.Second)

// transferTopic is keccak256("Transfer(address,address,uint256)") — the ERC-20
// Transfer event signature, topics[0] of every transfer log. A universally-fixed
// constant (no need to hash at runtime).
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// centDivisor: base units per cent for a `decimals`-place USD-pegged token, i.e.
// 10^(decimals-2). This MUST be per-token, not a constant: HUSD has 18 decimals
// (1e16 per cent) while USDC has 6 (1e4 per cent). Sharing one divisor across both
// would misprice a credit by 10^12 — the difference between a $10 top-up and a
// $10,000,000,000 one — so the token's own decimals are carried on the rail and
// used here.
func centDivisor(decimals int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-2)), nil)
}

var (
	addrRe   = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	txHashRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

func isAddr(a string) bool { return addrRe.MatchString(a) }

// rail is ONE accepted way to pay: a USD-pegged ERC-20 on a specific chain, sent to
// a treasury address we control there. Everything a receipt check needs is on the
// rail, so accepting a new chain or token is data, never code.
//
// This replaced a single hardcoded (HUSD, treasury) pair. That pair could only ever
// describe HUSD on Hanzo Mainnet, and since HUSD is not deployed the whole surface
// was permanently 501 — architecturally complete and earning nothing. Customers
// already hold USDC on Base/Ethereum/Polygon, so the rail set is what makes the
// path able to take money at all.
type rail struct {
	// Stable id the client names when submitting, e.g. "base-usdc".
	ID string `json:"id"`
	// Human chain name for the UI, e.g. "Base".
	Chain string `json:"chain"`
	// EIP-155 chain id — the wallet must be on this chain.
	ChainID int64 `json:"chainId"`
	// JSON-RPC endpoint used to read the receipt. Carried in the CONFIG json (this
	// struct is what TOPUP_RAILS decodes into) but never published — the public
	// listing is a separate view type, because an input model and an output model
	// are different things and collapsing them once made this field unsettable.
	RPCURL string `json:"rpcUrl"`
	// The ERC-20 contract.
	Token string `json:"token"`
	// Display symbol, e.g. "USDC".
	Symbol string `json:"symbol"`
	// Token decimals. USDC is 6; an 18-decimal token must say 18.
	Decimals int `json:"decimals"`
	// Where the customer sends funds on this chain. Public by nature.
	Treasury string `json:"treasury"`
}

// ok reports whether a rail is usable. A malformed rail is DROPPED rather than
// rejected at request time, so one bad entry cannot take the whole surface down —
// and a rail with impossible decimals cannot silently misprice a credit.
func (r rail) ok() bool {
	return r.ID != "" && isAddr(r.Token) && isAddr(r.Treasury) && r.RPCURL != "" &&
		r.Decimals >= 2 && r.Decimals <= 36
}

// topupConfig is the deployment's accepted rails + commerce wiring, resolved from
// server-only env (sourced from KMS by the deployment, never a browser value).
type topupConfig struct {
	rails    []rail
	commerce string // commerce base (e.g. http://commerce.hanzo.svc.cluster.local:8001)
	token    string // admin S2S bearer for commerce (COMMERCE_SERVICE_TOKEN; never logged)
}

// TOPUP_RAILS is a JSON array of rails — ONE variable describing the whole accepted
// set, rather than a family of per-token env names that would have to be invented
// again for every chain. Unparseable or malformed entries are dropped.
func loadTopupConfig() topupConfig {
	var rails []rail
	if raw := strings.TrimSpace(os.Getenv("TOPUP_RAILS")); raw != "" {
		var parsed []rail
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			for _, r := range parsed {
				r.RPCURL = strings.TrimRight(strings.TrimSpace(r.RPCURL), "/")
				if r.ok() {
					rails = append(rails, r)
				}
			}
		}
	}
	return topupConfig{
		rails:    rails,
		commerce: strings.TrimRight(getenv("COMMERCE_URL", "https://api.hanzo.ai"), "/"),
		token:    strings.TrimSpace(os.Getenv("COMMERCE_SERVICE_TOKEN")),
	}
}

// configured reports whether ANY rail is accepted. With none the surface is honestly
// 501 rather than pretending to take money it cannot verify.
func (t topupConfig) configured() bool { return len(t.rails) > 0 }

// find returns the named rail. An unknown id is a client error, not a server one.
func (t topupConfig) find(id string) (rail, bool) {
	for _, r := range t.rails {
		if strings.EqualFold(r.ID, id) {
			return r, true
		}
	}
	return rail{}, false
}

type walletTopupReq struct {
	// Which accepted rail the transfer was sent on, e.g. "base-usdc". The client
	// names it rather than the server guessing from the tx: the same address can
	// exist on several chains, so inferring would risk crediting against the wrong
	// treasury.
	Rail        string `json:"rail"`
	TxHash      string `json:"txHash"`
	FromAddress string `json:"fromAddress"`
	// A client-supplied `userId` is intentionally NOT read — the credit lands on the
	// validated caller (no IDOR). Neither is any amount: the credit is the ON-CHAIN
	// value, so a client number could never inflate it.
}

type walletTopupResp struct {
	CreditedCents int64  `json:"creditedCents"`
	Balance       int64  `json:"balance"`
	TxHash        string `json:"txHash"`
	Status        string `json:"status"`
}

// topupRails answers GET /v1/commerce/topup/rails: the accepted (chain, token,
// treasury) set, so the browser can render "send USDC here" WITHOUT the addresses
// being baked into its bundle.
//
// This exists because the console previously gated its top-up UI on
// NEXT_PUBLIC_HANZO_HUSD_ADDRESS/_TREASURY — build-time constants. Enabling a rail
// therefore meant rebuilding and redeploying the frontend, and with them unset the
// UI reported "not available yet" no matter what the server could actually accept.
// Serving the set at runtime keeps ONE source of truth (the server's config) and
// lets a rail be switched on without shipping a bundle.
//
// Everything here is public on-chain data; no secret is exposed.
func topupRails(s *cloud.Service[state], c *zip.Ctx) error {
	cfg := loadTopupConfig()
	// Encode as [] rather than null, so clients can just read .length.
	view := make([]railView, 0, len(cfg.rails))
	for _, r := range cfg.rails {
		view = append(view, railView{
			ID: r.ID, Chain: r.Chain, ChainID: r.ChainID,
			Token: r.Token, Symbol: r.Symbol, Decimals: r.Decimals, Treasury: r.Treasury,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"rails": view})
}

// railView is what a browser is told about a rail: everything needed to send funds
// and nothing else. Distinct from `rail` so that adding an operational field to the
// config (an RPC URL, a key reference, a provider credential) cannot leak by merely
// existing — a new field is published only if it is added here on purpose.
type railView struct {
	ID       string `json:"id"`
	Chain    string `json:"chain"`
	ChainID  int64  `json:"chainId"`
	Token    string `json:"token"`
	Symbol   string `json:"symbol"`
	Decimals int    `json:"decimals"`
	Treasury string `json:"treasury"`
}

// walletTopup verifies a sent stablecoin transfer on-chain and credits the caller's
// org. The credited amount is the ON-CHAIN value, never a client number.
func walletTopup(s *cloud.Service[state], c *zip.Ctx) error {
	cfg := loadTopupConfig()
	// No accepted rail ⇒ honest "not configured yet" rather than a fake credit.
	if !cfg.configured() {
		return zip.Errorf(http.StatusNotImplemented, "crypto top-up is not configured yet (no payment rail is enabled)")
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
	// With exactly one rail the client may omit it; naming it is required as soon as
	// there is a choice, so a transfer can never be checked against another chain's
	// treasury by default.
	railID := strings.TrimSpace(body.Rail)
	if railID == "" && len(cfg.rails) == 1 {
		railID = cfg.rails[0].ID
	}
	rl, ok := cfg.find(railID)
	if !ok {
		return zip.ErrBadRequest("unknown payment rail: name one from GET /v1/commerce/topup/rails")
	}

	// ── 1. Verify the transfer on-chain ──────────────────────────────────────────
	cents, verifiedFrom, herr := verifyTransfer(c.Context(), rl, txHash, strings.TrimSpace(body.FromAddress))
	if herr != nil {
		return herr
	}

	// ── 2. Record to commerce as a crypto payment on this rail (S2S) ─────────────
	status, herr := recordCryptoPayment(c.Context(), cfg, rl, cr, txHash, verifiedFrom, cents)
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

// verifyTransfer reads the receipt on the rail's chain and confirms a mined,
// successful Transfer of the rail's token to the rail's treasury, returning the
// credited cents and the sender. Any non-conforming tx is an honest 400; an
// unreachable chain is a 502. Nothing is credited that the chain did not confirm.
func verifyTransfer(ctx context.Context, rl rail, txHash, wantFrom string) (int64, string, error) {
	rcpt, err := getReceipt(ctx, rl.RPCURL, txHash)
	if err != nil {
		return 0, "", zip.Errorf(http.StatusBadGateway, "could not verify the transaction on %s: %v", rl.Chain, err)
	}
	if rcpt == nil {
		return 0, "", zip.ErrBadRequest("transaction not found or not yet mined")
	}
	if strings.ToLower(rcpt.Status) != "0x1" {
		return 0, "", zip.ErrBadRequest("transaction failed on-chain")
	}

	token := strings.ToLower(rl.Token)
	treasuryTopic := addrToTopic(rl.Treasury)
	div := centDivisor(rl.Decimals)
	for _, lg := range rcpt.Logs {
		if strings.ToLower(lg.Address) != token {
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
		cents := new(big.Int).Div(value, div)
		if cents.Sign() <= 0 || !cents.IsInt64() {
			return 0, "", zip.ErrBadRequest("transferred amount is below the minimum (1 cent)")
		}
		from := topicToAddr(lg.Topics[1]) // indexed `from`
		if wantFrom != "" && isAddr(wantFrom) && !strings.EqualFold(from, wantFrom) {
			return 0, "", zip.ErrBadRequest("transfer sender does not match the connected wallet")
		}
		return cents.Int64(), from, nil
	}
	return 0, "", zip.ErrBadRequest("no " + rl.Symbol + " transfer to the treasury was found in this transaction")
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

// recordCryptoPayment records the verified credit to commerce, scoped to the
// caller's org via the S2S service token + X-Org-Id. Network, chain, currency and
// destination all come from the RAIL the transfer was verified against, so the
// ledger row describes the payment that actually happened rather than a fixed
// assumption about which chain and token it was.
func recordCryptoPayment(ctx context.Context, cfg topupConfig, rl rail, cr caller, txHash, from string, cents int64) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"method":      "crypto",
		"network":     rl.Chain,
		"chainId":     rl.ChainID,
		"currency":    strings.ToLower(rl.Symbol),
		"amount":      cents,
		"txHash":      txHash,
		"fromAddress": from,
		"toAddress":   rl.Treasury,
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
	resp, err := commerceHTTP.Do(req)
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

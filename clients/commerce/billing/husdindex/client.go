// Package husdindex is Step 3 of the chain-backed credit ledger: it makes the
// on-chain HUSD balance the source of truth and the commerce DB a cache.
//
// It has two decomplected halves:
//   - client.go: a minimal JSON-RPC READ client (net/http, encoding/json) for
//     the Hanzo EVM — block height, ERC-20 balanceOf, and Transfer logs. No
//     geth, no cgo: reads only, so the indexer builds and the live read-proof
//     runs CGO-free.
//   - index.go: the Sync projector — scan Transfer events INTO org addresses and
//     project each, idempotently, into the ledger tagged by its off-chain
//     issuance bucket. Pure logic over small interfaces, unit-tested with fakes.
//
// The invariant it upholds: an org's indexed balance == its on-chain
// balanceOf(address), reconciled to the cent (Σ projected credits − debits).
package husdindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// TransferTopic0 is keccak256("Transfer(address,address,uint256)") — the ERC-20
// Transfer event signature topic. Verified against luxfi/crypto.Keccak256 in the
// tests so a typo can never silently mis-filter logs.
const TransferTopic0 = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// balanceOfSelector is the 4-byte selector for ERC-20 balanceOf(address).
const balanceOfSelector = "0x70a08231"

// Client reads HUSD state over JSON-RPC. It is safe for concurrent use.
type Client struct {
	rpcURL    string
	tokenAddr string
	http      *http.Client
	id        atomic.Int64
}

// NewClient builds a read client for the given RPC endpoint + HUSD token.
func NewClient(rpcURL, tokenAddr string) *Client {
	return &Client{
		rpcURL:    rpcURL,
		tokenAddr: strings.ToLower(tokenAddr),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: c.id.Add(1), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("husdindex: rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	var out rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("husdindex: rpc %s decode: %w", method, err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("husdindex: rpc %s error %d: %s", method, out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

// BlockNumber returns the current chain head.
func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	raw, err := c.call(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	return hexToUint64(rawString(raw))
}

// BalanceOf returns the token balance of addr in base units (wei).
func (c *Client) BalanceOf(ctx context.Context, addr string) (*big.Int, error) {
	data := balanceOfSelector + pad32(addr)
	raw, err := c.call(ctx, "eth_call", []any{
		map[string]string{"to": c.tokenAddr, "data": data}, "latest",
	})
	if err != nil {
		return nil, err
	}
	return hexToBig(rawString(raw))
}

// rawLog is one eth_getLogs entry.
type rawLog struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber string   `json:"blockNumber"`
	TxHash      string   `json:"transactionHash"`
	LogIndex    string   `json:"logIndex"`
	Removed     bool     `json:"removed"`
}

// TransfersTo returns HUSD Transfer events whose recipient (topic2) is one of
// addrs, between fromBlock and toBlock (inclusive). addrs are matched
// case-insensitively.
func (c *Client) TransfersTo(ctx context.Context, addrs []string, fromBlock, toBlock uint64) ([]Transfer, error) {
	if len(addrs) == 0 || toBlock < fromBlock {
		return nil, nil
	}
	toTopics := make([]any, 0, len(addrs))
	for _, a := range addrs {
		toTopics = append(toTopics, "0x"+pad32(a))
	}
	filter := map[string]any{
		"address":   c.tokenAddr,
		"fromBlock": uintToHex(fromBlock),
		"toBlock":   uintToHex(toBlock),
		// [Transfer, from=any, to ∈ addrs]
		"topics": []any{TransferTopic0, nil, toTopics},
	}
	raw, err := c.call(ctx, "eth_getLogs", []any{filter})
	if err != nil {
		return nil, err
	}
	var logs []rawLog
	if err := json.Unmarshal(raw, &logs); err != nil {
		return nil, fmt.Errorf("husdindex: decode logs: %w", err)
	}
	out := make([]Transfer, 0, len(logs))
	for _, l := range logs {
		if l.Removed {
			continue // reorged-out
		}
		tr, err := decodeTransfer(l)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

// TransfersInTx returns the HUSD Transfer events emitted by ONE mined tx whose
// recipient is one of addrs. It reads eth_getTransactionReceipt (not eth_getLogs)
// so a just-submitted mint can be projected the instant it is mined, without
// waiting for a block-range scan. addrs match case-insensitively; only logs from
// the HUSD token contract are considered. Returns nil if the tx is not yet mined
// (no receipt) — the caller falls back to the background Sync.
func (c *Client) TransfersInTx(ctx context.Context, txHash string, addrs []string) ([]Transfer, error) {
	want := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		want["0x"+pad32(a)] = true // 32-byte topic form, lowercased by pad32
	}
	raw, err := c.call(ctx, "eth_getTransactionReceipt", []any{txHash})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil // not mined yet
	}
	var receipt struct {
		Logs []rawLog `json:"logs"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("husdindex: decode receipt: %w", err)
	}
	out := make([]Transfer, 0, len(receipt.Logs))
	for _, l := range receipt.Logs {
		if l.Removed || strings.ToLower(l.Address) != c.tokenAddr {
			continue
		}
		if len(l.Topics) != 3 || strings.ToLower(l.Topics[0]) != TransferTopic0 {
			continue // not an ERC-20 Transfer
		}
		if !want[strings.ToLower(l.Topics[2])] {
			continue // not to one of our addresses
		}
		tr, err := decodeTransfer(l)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

// Transfer is a decoded ERC-20 Transfer event.
type Transfer struct {
	From     string // 0x, lowercased
	To       string // 0x, lowercased
	ValueWei *big.Int
	TxHash   string
	LogIndex uint64
	Block    uint64
}

// DedupKey is the idempotency key for projecting this transfer: unique per log.
func (t Transfer) DedupKey() string { return fmt.Sprintf("%s:%d", t.TxHash, t.LogIndex) }

func decodeTransfer(l rawLog) (Transfer, error) {
	if len(l.Topics) != 3 {
		return Transfer{}, fmt.Errorf("husdindex: transfer log has %d topics, want 3", len(l.Topics))
	}
	value, err := hexToBig(l.Data)
	if err != nil {
		return Transfer{}, fmt.Errorf("husdindex: transfer value: %w", err)
	}
	block, err := hexToUint64(l.BlockNumber)
	if err != nil {
		return Transfer{}, err
	}
	logIdx, err := hexToUint64(l.LogIndex)
	if err != nil {
		return Transfer{}, err
	}
	return Transfer{
		From:     topicToAddr(l.Topics[1]),
		To:       topicToAddr(l.Topics[2]),
		ValueWei: value,
		TxHash:   strings.ToLower(l.TxHash),
		LogIndex: logIdx,
		Block:    block,
	}, nil
}

// --- hex helpers (no external deps) ---

func rawString(r json.RawMessage) string { return strings.Trim(string(r), `"`) }

func hexToUint64(h string) (uint64, error) {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")
	if h == "" {
		return 0, nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(h, 16); !ok {
		return 0, fmt.Errorf("husdindex: bad hex uint %q", h)
	}
	return n.Uint64(), nil
}

func hexToBig(h string) (*big.Int, error) {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")
	if h == "" {
		return big.NewInt(0), nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(h, 16); !ok {
		return nil, fmt.Errorf("husdindex: bad hex bigint %q", h)
	}
	return n, nil
}

func uintToHex(n uint64) string { return "0x" + new(big.Int).SetUint64(n).Text(16) }

// pad32 left-pads a 20-byte address to a 32-byte (64-hex) word.
func pad32(addr string) string {
	a := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(addr, "0x"), "0X"))
	if len(a) < 64 {
		a = strings.Repeat("0", 64-len(a)) + a
	}
	return a
}

// topicToAddr extracts the low 20 bytes of a 32-byte topic word as a 0x address.
func topicToAddr(topic string) string {
	t := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(topic, "0x"), "0X"))
	if len(t) >= 40 {
		t = t[len(t)-40:]
	}
	return "0x" + t
}

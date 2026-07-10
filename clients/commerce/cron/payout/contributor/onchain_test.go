//go:build onchain

// Live on-chain proof of the OSS-contributor HUSD payout path against the
// Hanzo testnet (EVM chainId 36962). This exercises the EXACT production code
// path — executeCryptoPayout → util/blockchain.TransferToken →
// thirdparty/ethereum.transferToken (luxfi signing) → real signed ERC-20
// transfer — and verifies the resulting transaction on-chain.
//
// It is gated behind the `onchain` build tag so it never runs in normal CI;
// it needs a funded treasury and a reachable RPC. The blank import below
// registers the EVM ERC-20 transfer implementation (the same wiring the
// commerced daemon needs in production).
//
// Run:
//
//	SDKROOT=$(xcrun --show-sdk-path) CGO_ENABLED=1 \
//	HUSD_RPC_URL=http://localhost:19630/ext/bc/C/rpc \
//	HUSD_TREASURY_KEY=<hex> HUSD_RECIPIENT=0x... \
//	go test -tags onchain -run TestOnChainHUSDPayout_Testnet -v ./cron/payout/contributor
package contributor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	contribModel "github.com/hanzoai/cloud/clients/commerce/models/contributor"

	// Register the EVM ERC-20 transfer implementation into util/blockchain.
	// Without this blank import, blockchain.TransferToken returns
	// ErrNoTokenTransfer — this is exactly what cmd/commerced must import in
	// production to execute on-chain HUSD payouts.
	_ "github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
)

func TestOnChainHUSDPayout_Testnet(t *testing.T) {
	rpc := os.Getenv("HUSD_RPC_URL")
	key := os.Getenv("HUSD_TREASURY_KEY")
	recipient := os.Getenv("HUSD_RECIPIENT")
	if rpc == "" || key == "" || recipient == "" {
		t.Skip("set HUSD_RPC_URL, HUSD_TREASURY_KEY, HUSD_RECIPIENT to run the live testnet payout")
	}

	const (
		husdTestnet = "0xc57b7eCE2Ce2E74ef3Bc08Cfd5f5Fb41B6Ad4D66"
		chainID     = int64(36962)
		decimals    = 18
		amountCents = int64(2550) // $25.50 OSS dividend
	)

	cfg := Config{HUSD: HUSDConfig{
		ChainID:      chainID,
		RPCURL:       rpc,
		TokenAddress: husdTestnet,
		TreasuryKey:  key,
		Decimals:     decimals,
	}}

	c := &contribModel.Contributor{
		GitLogin:     "oss-tester",
		PayoutMethod: "crypto",
		PayoutTarget: recipient,
	}
	alloc := contribModel.PayoutAllocation{
		GitLogin:    "oss-tester",
		Component:   "@hanzo/commerce",
		AmountCents: amountCents,
	}

	balBefore := husdBalanceOf(t, rpc, husdTestnet, recipient)
	t.Logf("recipient HUSD before: %s", balBefore)

	txHash, err := executeCryptoPayout(context.Background(), c, alloc, cfg)
	if err != nil {
		t.Fatalf("executeCryptoPayout: %v", err)
	}
	if !txHashShape.MatchString(txHash) {
		t.Fatalf("returned txHash %q is not a 32-byte hash", txHash)
	}
	t.Logf("REAL on-chain HUSD payout tx: %s", txHash)

	// Poll the receipt until mined; assert success (status 0x1).
	status, blockNum := waitReceipt(t, rpc, txHash)
	if status != "0x1" {
		t.Fatalf("tx %s mined with non-success status %q", txHash, status)
	}
	t.Logf("tx mined OK status=%s block=%s", status, blockNum)

	// Confirm the recipient actually received the expected HUSD.
	balAfter := husdBalanceOf(t, rpc, husdTestnet, recipient)
	t.Logf("recipient HUSD after: %s", balAfter)
	want, _ := husdCentsToWei(amountCents, decimals)
	delta := new(big.Int).Sub(balAfter, balBefore)
	if delta.Cmp(want) != 0 {
		t.Fatalf("recipient delta=%s, want %s", delta, want)
	}
	t.Logf("PROVEN: contributor received %s HUSD base units ($%.2f) via the commerce payout executor", delta, float64(amountCents)/100.0)
}

// --- minimal JSON-RPC helpers (no extra deps) ---

func rpcCall(t *testing.T, url, method string, params []any) json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("rpc %s: %v", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("rpc %s decode: %v (%s)", method, err, raw)
	}
	if out.Error != nil {
		t.Fatalf("rpc %s error: %s", method, out.Error.Message)
	}
	return out.Result
}

func husdBalanceOf(t *testing.T, url, token, addr string) *big.Int {
	t.Helper()
	// balanceOf(address) selector 0x70a08231 + left-padded address
	data := "0x70a08231" + "000000000000000000000000" + strings.TrimPrefix(strings.ToLower(addr), "0x")
	var hexStr string
	_ = json.Unmarshal(rpcCall(t, url, "eth_call", []any{
		map[string]string{"to": token, "data": data}, "latest",
	}), &hexStr)
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(hexStr, "0x"), 16)
	return n
}

func waitReceipt(t *testing.T, url, txHash string) (status, blockNum string) {
	t.Helper()
	for i := 0; i < 60; i++ {
		res := rpcCall(t, url, "eth_getTransactionReceipt", []any{txHash})
		if string(res) != "null" {
			var r struct {
				Status      string `json:"status"`
				BlockNumber string `json:"blockNumber"`
			}
			if err := json.Unmarshal(res, &r); err == nil && r.Status != "" {
				return r.Status, r.BlockNumber
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("receipt for %s not found after 60s", txHash)
	return "", ""
}

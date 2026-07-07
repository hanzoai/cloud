package treasury

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethclient"
)

// anchorGasLimit is a generous fixed limit for a single bytes32 store — the
// TreasuryAnchor.anchor(bytes32) call (or the self-tx data write) costs far less, and
// a fixed limit avoids an eth_estimateGas round-trip against a fee-quirky L1.
const anchorGasLimit = 200_000

// anchorSelector is keccak256("anchor(bytes32)")[:4] == 0xeecdf927 — the calldata
// prefix for the TreasuryAnchor contract call.
var anchorSelector = crypto.Keccak256([]byte("anchor(bytes32)"))[:4]

// selfTxMagic tags a contract-less anchor: a 0-value self-tx whose data is
// "HZTA"+root, so an anchor is identifiable on-chain even without a deployed contract.
var selfTxMagic = []byte("HZTA")

// submit signs and sends the current ledger root to the Hanzo L1, then waits for the
// receipt. It uses the luxfi/geth EVM client; the signer key is provisioned from KMS
// (never plaintext in code/manifest). Prefers the TreasuryAnchor contract call when
// TREASURY_ANCHOR_CONTRACT is set; else a 0-value self-tx carrying the root. Only
// called when configured() is true (rpc + signer present).
func (a *anchorer) submit(ctx context.Context, b ledger.Backend) (*anchorRecord, error) {
	root, count, err := b.Root(ctx)
	if err != nil {
		return nil, fmt.Errorf("compute root: %w", err)
	}
	priv, err := crypto.HexToECDSA(trim0x(a.signerKeyHex()))
	if err != nil {
		return nil, fmt.Errorf("parse KMS-provisioned signer key: %w", err)
	}
	// crypto.PubkeyToAddress returns luxfi/crypto's own common.Address; the geth
	// client speaks luxfi/geth's common.Address. Both are [20]byte, so convert once.
	from := common.Address(crypto.PubkeyToAddress(priv.PublicKey))

	cl, err := ethclient.DialContext(ctx, a.rpcURL)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", a.rpcURL, err)
	}
	defer cl.Close()

	chainID, err := cl.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("chainID: %w", err)
	}
	nonce, err := cl.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	gasPrice, err := cl.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("gas price: %w", err)
	}

	var to common.Address
	var data []byte
	if a.contract != "" {
		to = common.HexToAddress(a.contract)
		data = append(append([]byte{}, anchorSelector...), root[:]...) // anchor(bytes32)
	} else {
		to = from // self-tx: the root lives in the data field of a 0-value tx to self
		data = append(append([]byte{}, selfTxMagic...), root[:]...)
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &to,
		Value:    big.NewInt(0),
		Gas:      anchorGasLimit,
		GasPrice: gasPrice,
		Data:     data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), priv)
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}
	if err := cl.SendTransaction(ctx, signed); err != nil {
		return nil, fmt.Errorf("send tx: %w", err)
	}

	rec := &anchorRecord{
		Root:    "0x" + hex.EncodeToString(root[:]),
		TxHash:  signed.Hash().Hex(),
		At:      time.Now().Unix(),
		Entries: count,
	}
	// Wait (bounded) for the receipt so the persisted record carries a real block.
	for i := 0; i < 30; i++ {
		if r, rerr := cl.TransactionReceipt(ctx, signed.Hash()); rerr == nil && r != nil {
			rec.Block = r.BlockNumber.Uint64()
			break
		}
		select {
		case <-ctx.Done():
			return rec, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	a.store(rec)
	a.log.Info("treasury: anchored ledger root", "root", rec.Root, "txHash", rec.TxHash, "block", rec.Block, "chainId", a.chainID)
	return rec, nil
}

// store sets the in-memory last record and persists it to DataDir (best-effort).
func (a *anchorer) store(rec *anchorRecord) {
	a.mu.Lock()
	a.last = rec
	a.mu.Unlock()
	if a.dataDir == "" {
		return
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(a.dataDir, "treasury_anchor.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		a.log.Warn("treasury: persist anchor record failed", "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		a.log.Warn("treasury: persist anchor record rename failed", "err", err)
	}
}

// loadRecord reads the persisted last anchor at boot (nil when none/unreadable).
func (a *anchorer) loadRecord() *anchorRecord {
	if a.dataDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(a.dataDir, "treasury_anchor.json"))
	if err != nil {
		return nil
	}
	var rec anchorRecord
	if err := json.Unmarshal(b, &rec); err != nil || rec.TxHash == "" {
		return nil
	}
	return &rec
}

func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

package treasury

import (
	"context"
	"crypto/ecdsa"
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

// txSigner is the anchor's signing seam: it yields the sender address and signs
// the 32-byte EVM signing hash. Two backends satisfy it — a local, KMS-provisioned
// key (keySigner, single-sig) and a quorum-gated treasury MPC wallet (mpcSigner,
// the reserve's 3-of-5, bound via BindAnchorSigner). submit() is agnostic to which
// one signs; the signature is applied identically.
type txSigner interface {
	address() common.Address
	signHash(ctx context.Context, hash []byte) ([]byte, error)
}

// keySigner signs with the anchor's own KMS-provisioned secp256k1 key (the
// single-signer path this refactor generalizes away from as the default).
type keySigner struct{ priv *ecdsa.PrivateKey }

func (k keySigner) address() common.Address {
	return common.Address(crypto.PubkeyToAddress(k.priv.PublicKey))
}
func (k keySigner) signHash(_ context.Context, hash []byte) ([]byte, error) {
	return crypto.Sign(hash, k.priv)
}

// mpcSigner delegates the signing hash to a quorum-gated custody backend — the
// reserve's treasury MPC wallet (3-of-5). The address is the wallet's fixed EVM
// address; the signature is produced by the threshold ring, never a local key.
type mpcSigner struct {
	addr common.Address
	sign func(ctx context.Context, hash []byte) ([]byte, error)
}

func (m mpcSigner) address() common.Address { return m.addr }
func (m mpcSigner) signHash(ctx context.Context, hash []byte) ([]byte, error) {
	return m.sign(ctx, hash)
}

// boundAnchorSigner, when set by the wallets/finance seam (BindAnchorSigner),
// makes the anchor commit with the reserve's treasury MPC wallet instead of a
// local key — quorum-gated. nil ⇒ fall back to the KMS-provisioned key.
var boundAnchorSigner txSigner

// BindAnchorSigner binds a quorum-gated treasury signer (the reserve's 3-of-5
// MPC wallet) as the anchor's signer: addr is the wallet's EVM address, and sign
// delegates the 32-byte signing hash to the threshold ring. A nil sign unbinds
// (revert to the local key). This is the finance seam — the treasury reserve
// wallet, not a single key, governs on-chain anchors.
func BindAnchorSigner(addr common.Address, sign func(ctx context.Context, hash []byte) ([]byte, error)) {
	if sign == nil {
		boundAnchorSigner = nil
		return
	}
	boundAnchorSigner = mpcSigner{addr: addr, sign: sign}
}

// signer resolves the anchor's signing backend: the bound quorum-gated treasury
// signer when present (the reserve MPC wallet), else the local KMS-provisioned
// key. Returns an error when neither is available (submit fails closed — a
// signature is never fabricated).
func (a *anchorer) signer() (txSigner, error) {
	if boundAnchorSigner != nil {
		return boundAnchorSigner, nil
	}
	keyHex := a.signerKeyHex()
	if keyHex == "" {
		return nil, fmt.Errorf("no anchor signer: bind a treasury MPC wallet (BindAnchorSigner) or provision TREASURY_ANCHOR_SIGNER_KEY from KMS")
	}
	priv, err := crypto.HexToECDSA(trim0x(keyHex))
	if err != nil {
		return nil, fmt.Errorf("parse KMS-provisioned signer key: %w", err)
	}
	return keySigner{priv: priv}, nil
}

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
	sgn, err := a.signer()
	if err != nil {
		return nil, err
	}
	// The sender is the signer's address — a local KMS key's pubkey address, or the
	// reserve treasury MPC wallet's fixed EVM address (quorum-gated). Both are
	// luxfi/geth [20]byte common.Address.
	from := sgn.address()

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
	// Sign through the seam: hash the tx for this chain, delegate the 32-byte
	// signing hash to the resolved backend (local key OR the reserve's 3-of-5 MPC
	// wallet), then apply the recoverable signature. Decouples the tx builder from
	// WHERE the private material lives.
	evmSigner := types.LatestSignerForChainID(chainID)
	sig, err := sgn.signHash(ctx, evmSigner.Hash(tx).Bytes())
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}
	signed, err := tx.WithSignature(evmSigner, sig)
	if err != nil {
		return nil, fmt.Errorf("apply signature: %w", err)
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

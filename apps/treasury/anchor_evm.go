package treasury

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/wallet"
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

// txSigner is the anchor's signing client: it yields the sender address and signs
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

// boundAnchorSigner, when set by the wallets/finance client (BindAnchorSigner),
// makes the anchor commit with the reserve's treasury MPC wallet instead of a
// local key — quorum-gated. nil ⇒ fall back to the KMS-provisioned key.
var boundAnchorSigner txSigner

// BindAnchorSigner binds a quorum-gated treasury signer (the reserve's 3-of-5
// MPC wallet) as the anchor's signer: addr is the wallet's EVM address, and sign
// delegates the 32-byte signing hash to the threshold ring. A nil sign unbinds
// (revert to the local key). This is the finance client — the treasury reserve
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
func (a *anchorer) signer(ctx context.Context) (txSigner, error) {
	if boundAnchorSigner != nil {
		return boundAnchorSigner, nil
	}
	// A signer bound through another replica is bound here too, from the org the book
	// remembers it for. Once the quorum wallet signs anchors, no replica falls back to a
	// lone key: an anchor from a different address than the last would be a different
	// witness, and nobody asked for one.
	if org := a.signerOrg(ctx); org != "" {
		addr, sign, ok := wallet.TreasuryAnchorSigner(ctx, org, "eip155:"+strconv.FormatInt(a.chainID, 10))
		if !ok {
			return nil, fmt.Errorf("the anchor is signed by org %s's treasury wallet and this replica cannot reach it", org)
		}
		BindAnchorSigner(common.HexToAddress(addr), sign)
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
	// ONE REPLICA SENDS AT A TIME. A transaction names the sender's nonce, and two sent
	// from one address together read the same pending nonce, so the chain would keep one
	// and the other would be refused after its root was computed and its signer asked.
	// The root is read under the lock too, so what is anchored is the journal as the
	// previous anchor left it.
	release, err := a.records.Anchoring(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	root, count, err := b.Root(ctx)
	if err != nil {
		return nil, fmt.Errorf("compute root: %w", err)
	}
	sgn, err := a.signer(ctx)
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
	// Sign through the client: hash the tx for this chain, delegate the 32-byte
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
	for range 30 {
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
	if err := a.records.SetAnchor(ctx, *rec); err != nil {
		// The root is on the chain and that is the witness; what is lost is the book's
		// memory of it, which the next anchor writes again.
		a.log.Error("treasury: the anchor landed and was not recorded", "txHash", rec.TxHash, "err", err)
	}
	a.log.Info("treasury: anchored ledger root", "root", rec.Root, "txHash", rec.TxHash, "block", rec.Block, "chainId", a.chainID)
	return rec, nil
}

func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

package company

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethclient"
	luxlog "github.com/luxfi/log"
)

// genesis.go records the cap-table equity genesis on-chain. It mirrors the treasury
// L1 anchor (clients/treasury/anchor_evm.go): compute a deterministic keccak root of
// the founding allocation, then commit that bytes32 to the Hanzo L1 via a KMS-signed
// transaction — either an EquityGenesis contract call or a 0-value self-tx carrying
// "HZEG"+root. The chain is the source of truth; the indexer (clients/graph) projects
// the anchored root for GraphQL reads. Until the RPC + signer are wired the root is
// computed and returned with an HONEST pending status — incorporation is never
// blocked on an unconfigured chain, and NO fabricated tx hash is recorded.

// defaultHanzoChainID is the Hanzo L1 EVM chain (same L1 the treasury anchors to).
const defaultHanzoChainID = 36963

// equityGenesisMagic tags a contract-less genesis anchor: a 0-value self-tx whose
// data is "HZEG"+root, identifiable on-chain even without a deployed contract.
var equityGenesisMagic = []byte("HZEG")

// anchorSelector is keccak256("recordGenesis(bytes32)")[:4] — the calldata prefix
// for the EquityGenesis contract call when COMPANY_GENESIS_CONTRACT is set.
var anchorSelector = crypto.Keccak256([]byte("recordGenesis(bytes32)"))[:4]

const anchorGasLimit = 200_000

// genesisAllocation is the canonical, deterministic pre-image of the equity-genesis
// root: the entity plus the founders' allocation, founders sorted by email so the
// root is independent of insertion order. Changing any field changes the root, so
// the on-chain value is an immutable witness to the founding cap table.
type genesisAllocation struct {
	Structure    Structure    `json:"structure"`
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	Name         string       `json:"name"`
	Founders     []Founder    `json:"founders"`
}

// genesisRoot computes the keccak256 root over the canonical JSON of the founding
// allocation. Founders are copied and sorted by email; KYC bookkeeping fields are
// zeroed so the root witnesses the ALLOCATION (who owns what), not verification
// state.
func genesisRoot(f *Formation) [32]byte {
	founders := make([]Founder, len(f.Founders))
	for i, fo := range f.Founders {
		founders[i] = Founder{Name: fo.Name, Email: fo.Email, EquityBps: fo.EquityBps}
	}
	sort.Slice(founders, func(i, j int) bool { return founders[i].Email < founders[j].Email })
	alloc := genesisAllocation{
		Structure:    f.Structure,
		Jurisdiction: f.Jurisdiction,
		Name:         f.Name,
		Founders:     founders,
	}
	b, _ := json.Marshal(alloc)
	var root [32]byte
	copy(root[:], crypto.Keccak256(b))
	return root
}

// evmAnchor commits the genesis root to the Hanzo L1. Config is operator-injected;
// the signer key is provisioned from KMS into the pod env (never a plaintext key in
// code or manifest), mirroring the treasury anchor's secrets discipline.
//
//	COMPANY_GENESIS_RPC_URL     Hanzo L1 C-chain RPC
//	COMPANY_GENESIS_CHAIN_ID    EVM chain id (default 36963)
//	COMPANY_GENESIS_CONTRACT    deployed EquityGenesis address (optional; absent →
//	                            self-tx carrying "HZEG"+root)
//	COMPANY_GENESIS_SIGNER_KEY  secp256k1 signer key, provisioned from KMS by the
//	                            operator's KMSSecret CRD (ref COMPANY_GENESIS_SIGNER_KMS_REF)
type evmAnchor struct {
	log luxlog.Logger
}

func newEVMAnchor(log luxlog.Logger) *evmAnchor { return &evmAnchor{log: log} }

func (a *evmAnchor) rpcURL() string { return strings.TrimSpace(os.Getenv("COMPANY_GENESIS_RPC_URL")) }
func (a *evmAnchor) contract() string {
	return strings.TrimSpace(os.Getenv("COMPANY_GENESIS_CONTRACT"))
}
func (a *evmAnchor) signerKeyHex() string {
	return strings.TrimSpace(os.Getenv("COMPANY_GENESIS_SIGNER_KEY"))
}

func (a *evmAnchor) chainID() int64 {
	if v := strings.TrimSpace(os.Getenv("COMPANY_GENESIS_CHAIN_ID")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultHanzoChainID
}

// Configured reports whether the on-chain submit path is fully wired (an RPC AND a
// signer key provisioned from KMS). Until both are present the anchor is
// compute-and-report only.
func (a *evmAnchor) Configured() bool {
	return a.rpcURL() != "" && a.signerKeyHex() != ""
}

// Anchor computes the genesis root and, when Configured(), commits it on-chain. The
// returned Genesis ALWAYS carries the root; TxHash/Block are set only on a real
// commit. An unwired chain yields an honest pending status and records nothing false.
func (a *evmAnchor) Anchor(ctx context.Context, f *Formation) (*Genesis, error) {
	root := genesisRoot(f)
	g := &Genesis{
		Root:    "0x" + hex.EncodeToString(root[:]),
		ChainID: a.chainID(),
		At:      time.Now().Unix(),
		Status:  "pending",
	}
	if !a.Configured() {
		g.Note = "genesis root computed; on-chain anchor pending wiring — set COMPANY_GENESIS_RPC_URL + provision COMPANY_GENESIS_SIGNER_KEY from KMS (never a plaintext key) to commit to Hanzo L1 " + strconv.FormatInt(g.ChainID, 10)
		return g, nil
	}
	if err := a.submit(ctx, root, g); err != nil {
		// A submit failure does not fabricate a tx; the root stands, the status is
		// honest, and the error is surfaced for retry.
		g.Note = "genesis submit failed: " + err.Error()
		return g, err
	}
	return g, nil
}

// submit signs and sends the genesis root to the Hanzo L1, then waits (bounded) for
// the receipt. Uses the luxfi/geth EVM client; the signer key is provisioned from
// KMS. Prefers the EquityGenesis contract call when COMPANY_GENESIS_CONTRACT is set,
// else a 0-value self-tx carrying the root.
func (a *evmAnchor) submit(ctx context.Context, root [32]byte, g *Genesis) error {
	priv, err := crypto.HexToECDSA(trim0x(a.signerKeyHex()))
	if err != nil {
		return fmt.Errorf("parse KMS-provisioned signer key: %w", err)
	}
	from := common.Address(crypto.PubkeyToAddress(priv.PublicKey))

	cl, err := ethclient.DialContext(ctx, a.rpcURL())
	if err != nil {
		return fmt.Errorf("dial %s: %w", a.rpcURL(), err)
	}
	defer cl.Close()

	chainID, err := cl.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("chainID: %w", err)
	}
	nonce, err := cl.PendingNonceAt(ctx, from)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	gasPrice, err := cl.SuggestGasPrice(ctx)
	if err != nil {
		return fmt.Errorf("gas price: %w", err)
	}

	var to common.Address
	var data []byte
	if c := a.contract(); c != "" {
		to = common.HexToAddress(c)
		data = append(append([]byte{}, anchorSelector...), root[:]...) // recordGenesis(bytes32)
	} else {
		to = from // self-tx: the root lives in the data field of a 0-value tx to self
		data = append(append([]byte{}, equityGenesisMagic...), root[:]...)
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce: nonce, To: &to, Value: big.NewInt(0),
		Gas: anchorGasLimit, GasPrice: gasPrice, Data: data,
	})
	signer := types.LatestSignerForChainID(chainID)
	sig, err := crypto.Sign(signer.Hash(tx).Bytes(), priv)
	if err != nil {
		return fmt.Errorf("sign tx: %w", err)
	}
	signed, err := tx.WithSignature(signer, sig)
	if err != nil {
		return fmt.Errorf("apply signature: %w", err)
	}
	if err := cl.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("send tx: %w", err)
	}
	g.TxHash = signed.Hash().Hex()
	g.Status = "anchored"
	for i := 0; i < 30; i++ {
		if r, rerr := cl.TransactionReceipt(ctx, signed.Hash()); rerr == nil && r != nil {
			g.Block = r.BlockNumber.Uint64()
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	a.log.Info("company: anchored equity genesis", "root", g.Root, "txHash", g.TxHash, "block", g.Block, "chainId", g.ChainID)
	return nil
}

func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

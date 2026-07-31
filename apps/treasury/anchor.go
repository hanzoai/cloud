package treasury

import (
	"context"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/treasury/ledger"
	luxlog "github.com/luxfi/log"
)

// The Hanzo L1 EVM anchor. The treasury's books live off-chain (Base/SQLite), so to
// make them tamper-evident and EVM-auditable we commit a deterministic root of the
// whole journal (ledger.Root) to the live Hanzo L1 (chain 36963). A change to any
// historical posting changes the root, so the on-chain value is an immutable witness
// to the off-chain ledger.
//
// Config (operator-injected; the signing key is a KMS reference, NEVER a plaintext
// key — HIP secrets rule):
//
//	TREASURY_ANCHOR_RPC_URL         Hanzo L1 C-chain RPC (the in-cluster
//	                                hanzod-rpc-internal .../v1/bc/<blockchainID>/rpc,
//	                                or https://api.hanzo.network/v1/bc/C/rpc)
//	TREASURY_ANCHOR_CHAIN_ID        EVM chain id (default 36963)
//	TREASURY_ANCHOR_CONTRACT        deployed TreasuryAnchor address (optional; absent
//	                                → anchor as a signed 0-value tx carrying the root
//	                                in the data field)
//	TREASURY_ANCHOR_SIGNER_KMS_REF  KMS secret ref for the anchor signer key
//
// Phase 1 ships the root computation, the config surface and an HONEST status: with
// no signer wired, POST /v1/admin/treasury/anchor returns the root that WOULD be
// committed plus the exact remaining step, and records nothing false. Phase 2 wires
// the luxfi/geth submit + on-chain record behind this same status/handler.
const defaultHanzoChainID = 36963

type anchorer struct {
	rpcURL    string
	chainID   int64
	contract  string
	signerRef string
	dataDir   string
	log       luxlog.Logger

	mu   sync.Mutex
	last *anchorRecord // most recent successful on-chain anchor (persisted)
}

// anchorRecord is one committed on-chain anchor — persisted to DataDir so the "last
// anchored" status survives a restart.
type anchorRecord struct {
	Root    string `json:"root"`   // 0x… ledger root committed
	TxHash  string `json:"txHash"` // the Hanzo L1 transaction hash
	Block   uint64 `json:"block"`  // block the anchor landed in
	At      int64  `json:"at"`     // unix seconds
	Entries int    `json:"entries"`
}

func newAnchorer(deps cloud.Deps, log luxlog.Logger) *anchorer {
	chainID := int64(defaultHanzoChainID)
	if v := strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_CHAIN_ID")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			chainID = n
		}
	}
	a := &anchorer{
		rpcURL:    strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_RPC_URL")),
		chainID:   chainID,
		contract:  strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_CONTRACT")),
		signerRef: strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_SIGNER_KMS_REF")),
		dataDir:   deps.DataDir,
		log:       log,
	}
	a.last = a.loadRecord()
	return a
}

// configured reports whether the on-chain submit path is fully wired (an RPC to
// reach the chain AND a signer key provisioned from KMS). Until both are present the
// anchor is compute-and-report only.
func (a *anchorer) configured() bool {
	return a != nil && a.rpcURL != "" && (boundAnchorSigner != nil || a.signerKeyHex() != "")
}

// signerKeyHex returns the anchor signer's private key, provisioned from KMS into the
// pod env by the operator's KMSSecret CRD (sourced from TREASURY_ANCHOR_SIGNER_KMS_REF)
// — NEVER committed, never in a manifest. Empty when not provisioned.
func (a *anchorer) signerKeyHex() string {
	return strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_SIGNER_KEY"))
}

// AnchorStatus is the anchor view embedded in GET /v1/admin/treasury and returned by
// POST /v1/admin/treasury/anchor.
type AnchorStatus struct {
	// ChainID is the Hanzo L1 chain the root is committed to.
	ChainID int64 `json:"chainId"`
	// RPCConfigured and SignerConfigured report which half of the chain path is wired.
	RPCConfigured    bool `json:"rpcConfigured"`
	SignerConfigured bool `json:"signerConfigured"`
	// Contract is the deployed anchor contract address, empty until deployed.
	Contract string `json:"contract,omitempty"`
	// CurrentRoot is the 0x… root of the journal as it stands now.
	CurrentRoot string `json:"currentRoot"`
	// EntryCount is how many journal entries the current root covers.
	EntryCount int `json:"entryCount"`
	// Status is pending, anchored or error.
	Status string `json:"status"`
	// Note states the remaining step, in plain words.
	Note string `json:"note"`
	// LastRoot is the last root committed on-chain; empty before the first submit.
	LastRoot string `json:"lastRoot,omitempty"`
	// LastTxHash is that commit's transaction hash.
	LastTxHash string `json:"lastTxHash,omitempty"`
	// LastBlock is the block it landed in.
	LastBlock uint64 `json:"lastBlock,omitempty"`
	// LastAt is when it landed, unix seconds.
	LastAt int64 `json:"lastAt,omitempty"`
	// Synced is true when the last anchored root equals the current root.
	Synced bool `json:"synced"`
}

// status computes the current ledger root and reports whether the chain path is
// wired + the last committed anchor. It never fabricates an anchored state. The root
// is computed over WHICHEVER backend is the ledger of record (native or Formance) via
// the shared ledger.Backend port, so the anchor is backend-agnostic.
func (a *anchorer) status(ctx context.Context, b ledger.Backend) AnchorStatus {
	root, count, err := b.Root(ctx)
	st := AnchorStatus{
		ChainID:          a.chainID,
		RPCConfigured:    a.rpcURL != "",
		SignerConfigured: a.signerKeyHex() != "",
		Contract:         a.contract,
		EntryCount:       count,
	}
	if err != nil {
		st.Status = "error"
		st.Note = "compute root: " + err.Error()
		return st
	}
	current := "0x" + hex.EncodeToString(root[:])
	st.CurrentRoot = current
	a.mu.Lock()
	last := a.last
	a.mu.Unlock()
	if last != nil {
		st.LastRoot, st.LastTxHash, st.LastBlock, st.LastAt = last.Root, last.TxHash, last.Block, last.At
		st.Synced = last.Root == current
	}
	if last != nil {
		st.Status = "anchored"
	} else {
		st.Status = "pending"
	}
	switch {
	case a.configured():
		st.Note = "chain wiring present; POST /v1/admin/treasury/anchor to commit the current root to Hanzo L1 (" + strconv.FormatInt(a.chainID, 10) + ")"
	default:
		st.Note = "anchor pending chain wiring — set TREASURY_ANCHOR_RPC_URL + provision TREASURY_ANCHOR_SIGNER_KEY from KMS (KMSSecret, ref TREASURY_ANCHOR_SIGNER_KMS_REF; never a plaintext key) + deploy contracts/TreasuryAnchor.sol on chain " + strconv.FormatInt(a.chainID, 10) + " (set TREASURY_ANCHOR_CONTRACT) to enable on-chain commits"
	}
	return st
}

// AnchorData wraps the anchor status a commit answers with.
type AnchorData struct {
	// Anchor is the anchor status after the attempt.
	Anchor AnchorStatus `json:"anchor"`
}

// AnchorOut is the admin envelope around an anchor commit.
type AnchorOut struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   *AnchorData `json:"data"`
}

// anchor commits the current ledger root to Hanzo L1. It answers with the anchor
// status. SuperAdmin only. When the chain path is wired it signs and submits the
// anchor transaction and records it; otherwise it reports the root that WOULD be
// committed plus the exact remaining step, and records nothing false. A failed submit
// answers status=error with the reason rather than a fabricated anchored state.
func (o ops) anchor(ctx context.Context, _ *struct{}) (*AnchorOut, error) {
	if _, err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	if s.State.anchor.configured() {
		rec, err := s.State.anchor.submit(ctx, s.State.record)
		if err != nil {
			s.Log.Error("treasury: anchor submit failed", "err", err)
			st := s.State.anchor.status(ctx, s.State.record)
			st.Status = "error"
			st.Note = "submit: " + err.Error()
			return &AnchorOut{Status: "ok", Data: &AnchorData{Anchor: st}}, nil
		}
		emitAudit(s, ctx, "treasury.anchor", "", rec.TxHash, map[string]any{
			"root": rec.Root, "txHash": rec.TxHash, "block": rec.Block, "chainId": s.State.anchor.chainID,
		})
	}
	return &AnchorOut{Status: "ok", Data: &AnchorData{Anchor: s.State.anchor.status(ctx, s.State.record)}}, nil
}

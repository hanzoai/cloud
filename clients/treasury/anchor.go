package treasury

import (
	"context"
	"encoding/hex"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/treasury/ledger"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
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
//	                                hanzod-rpc-internal .../ext/bc/<blockchainID>/rpc,
//	                                or https://api.hanzo.network/ext/bc/C/rpc)
//	TREASURY_ANCHOR_CHAIN_ID        EVM chain id (default 36963)
//	TREASURY_ANCHOR_CONTRACT        deployed TreasuryAnchor address (optional; absent
//	                                → anchor as a signed 0-value tx carrying the root
//	                                in the data field)
//	TREASURY_ANCHOR_SIGNER_KMS_REF  KMS secret ref for the anchor signer key
//
// Phase 1 ships the root computation, the config surface and an HONEST status: with
// no signer wired, POST /v1/admin/finance/anchor returns the root that WOULD be
// committed plus the exact remaining step, and records nothing false. Phase 2 wires
// the luxfi/geth submit + on-chain record behind this same status/handler.
const defaultHanzoChainID = 36963

type anchorer struct {
	rpcURL    string
	chainID   int64
	contract  string
	signerRef string
	log       luxlog.Logger
}

func newAnchorer(deps cloud.Deps, log luxlog.Logger) *anchorer {
	chainID := int64(defaultHanzoChainID)
	if v := strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_CHAIN_ID")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			chainID = n
		}
	}
	return &anchorer{
		rpcURL:    strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_RPC_URL")),
		chainID:   chainID,
		contract:  strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_CONTRACT")),
		signerRef: strings.TrimSpace(os.Getenv("TREASURY_ANCHOR_SIGNER_KMS_REF")),
		log:       log,
	}
}

// configured reports whether the on-chain submit path is fully wired (an RPC to
// reach the chain AND a KMS signer ref). Until both are present the anchor is
// compute-and-report only.
func (a *anchorer) configured() bool {
	return a != nil && a.rpcURL != "" && a.signerRef != ""
}

// anchorStatus is the anchor view embedded in GET /v1/admin/finance and returned by
// POST /v1/admin/finance/anchor.
type anchorStatus struct {
	ChainID          int64  `json:"chainId"`
	RPCConfigured    bool   `json:"rpcConfigured"`
	SignerConfigured bool   `json:"signerConfigured"`
	Contract         string `json:"contract,omitempty"`
	CurrentRoot      string `json:"currentRoot"` // 0x… root of the journal as it stands now
	EntryCount       int    `json:"entryCount"`
	Status           string `json:"status"` // pending | anchored | error
	Note             string `json:"note"`
}

// status computes the current ledger root and reports whether the chain path is
// wired. It never fabricates an anchored state. The root is computed over WHICHEVER
// backend is the ledger of record (native or Formance) via the shared ledger.Backend
// port, so the anchor is backend-agnostic.
func (a *anchorer) status(ctx context.Context, b ledger.Backend) anchorStatus {
	root, count, err := b.Root(ctx)
	st := anchorStatus{
		ChainID:          a.chainID,
		RPCConfigured:    a.rpcURL != "",
		SignerConfigured: a.signerRef != "",
		Contract:         a.contract,
		EntryCount:       count,
	}
	if err != nil {
		st.Status = "error"
		st.Note = "compute root: " + err.Error()
		return st
	}
	st.CurrentRoot = "0x" + hex.EncodeToString(root[:])
	st.Status = "pending"
	if a.configured() {
		st.Note = "chain wiring present; POST /v1/admin/finance/anchor to commit the current root to Hanzo L1"
	} else {
		st.Note = "anchor pending chain wiring — set TREASURY_ANCHOR_RPC_URL + TREASURY_ANCHOR_SIGNER_KMS_REF (KMS ref, never a plaintext key) to enable on-chain commits"
	}
	return st
}

// adminAnchor answers POST /v1/admin/finance/anchor — commit the current ledger
// root to Hanzo L1. Global-admin only. Phase 1 returns the root that WOULD be
// committed plus the exact remaining step (Phase 2 submits + records the tx).
func (s *svc) adminAnchor(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	return adminOK(c, map[string]any{"anchor": s.anchor.status(c.Context(), s.record)})
}

package treasury

import (
	"context"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
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
	kms       cloud.KMSClient
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
		kms:       deps.KMS,
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
	return a != nil && a.rpcURL != "" && (boundAnchorSigner != nil || a.hasSigner())
}

// hasSigner reports whether an anchor signer is provisioned, WITHOUT fetching it.
// Status answers this question; only the submit path needs the key itself, and a
// status endpoint should never have a reason to hold a private key.
func (a *anchorer) hasSigner() bool { return a != nil && a.signerRef != "" }

// signerKeyHex fetches the anchor signer's private key from KMS at the moment it
// is needed to sign. TREASURY_ANCHOR_SIGNER_KMS_REF names its location; the key
// itself never enters the pod env. Empty when not provisioned or unresolvable —
// callers treat empty as "cannot anchor", which is the safe direction.
func (a *anchorer) signerKeyHex() string {
	if a == nil || a.signerRef == "" || a.kms == nil {
		return ""
	}
	value, err := a.kms.GetSecret(context.Background(), a.signerRef)
	if err != nil {
		a.log.Warn("anchor signer key did not resolve from KMS", "ref", a.signerRef, "err", err)
		return ""
	}
	return strings.TrimSpace(string(value))
}

// anchorStatus is the anchor view embedded in GET /v1/admin/treasury and returned by
// POST /v1/admin/treasury/anchor.
type anchorStatus struct {
	ChainID          int64  `json:"chainId"`
	RPCConfigured    bool   `json:"rpcConfigured"`
	SignerConfigured bool   `json:"signerConfigured"`
	Contract         string `json:"contract,omitempty"`
	CurrentRoot      string `json:"currentRoot"` // 0x… root of the journal as it stands now
	EntryCount       int    `json:"entryCount"`
	Status           string `json:"status"` // pending | anchored | error
	Note             string `json:"note"`
	// The last committed on-chain anchor (nil-fields until the first successful submit).
	LastRoot   string `json:"lastRoot,omitempty"`
	LastTxHash string `json:"lastTxHash,omitempty"`
	LastBlock  uint64 `json:"lastBlock,omitempty"`
	LastAt     int64  `json:"lastAt,omitempty"`
	Synced     bool   `json:"synced"` // true when the last anchored root == the current root
}

type rooted interface {
	// Root is the Merkle root over the journal, plus the entry count it covers.
	Root(ctx context.Context) ([32]byte, int, error)
}

// rooted is the ONE thing the anchor needs of a ledger: the Merkle root over its
// journal, and how many entries went into it.
//
// It is declared HERE, by the consumer, and it is one method — where this used to
// name ledger.Backend, all ELEVEN methods of it, to call Root and nothing else.
// The parameter said the anchor could accrue revenue, seed the reserve, debit a
// program and rewrite the revenue-share policy; it can do none of those, and now
// the type says so. Both backends still satisfy it without adding a line, because
// a Go interface is satisfied structurally by whoever already has the method —
// which is exactly why the consumer is the right place to state what it wants.
func (a *anchorer) status(ctx context.Context, b rooted) anchorStatus {
	root, count, err := b.Root(ctx)
	st := anchorStatus{
		ChainID:          a.chainID,
		RPCConfigured:    a.rpcURL != "",
		SignerConfigured: a.hasSigner(),
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

// AnchorTreasury commits the current ledger root to Hanzo L1, making the books
// tamper-evident on chain, and returns the anchoring status. When the chain path
// is wired it signs and submits the anchor transaction and records it; when it is
// not, it returns the root that WOULD be committed plus the exact remaining
// wiring step and records nothing false. A submit that fails still answers 200
// with the anchor's own status set to "error" — the attempt is the product.
// SuperAdmin only.
func (o ops) adminAnchor(ctx context.Context, _ *cloud.Unit) (*anchorOut, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	if o.s.State.anchor.configured() {
		rec, err := o.s.State.anchor.submit(ctx, o.s.State.record)
		if err != nil {
			o.s.Log.Error("treasury: anchor submit failed", "err", err)
			st := o.s.State.anchor.status(ctx, o.s.State.record)
			st.Status = "error"
			st.Note = "submit: " + err.Error()
			return &anchorOut{Status: "ok", Data: anchorData{Anchor: st}}, nil
		}
		emitAudit(o.s, ctx, "treasury.anchor", "", rec.TxHash, map[string]any{
			"root": rec.Root, "txHash": rec.TxHash, "block": rec.Block, "chainId": o.s.State.anchor.chainID,
		})
	}
	return &anchorOut{Status: "ok", Data: anchorData{Anchor: o.s.State.anchor.status(ctx, o.s.State.record)}}, nil
}

// anchorData carries the anchoring status.
type anchorData struct {
	// Anchor is the Hanzo L1 anchoring status of the ledger root after this call.
	Anchor anchorStatus `json:"anchor"`
}

// anchorOut is anchorData in the admin envelope.
type anchorOut struct {
	// Status is "ok" on success. A submit that failed still answers ok with the
	// anchor's own status set to "error" — the attempt is the product.
	Status string `json:"status"`
	// Msg carries an operator-facing note; empty on success.
	Msg string `json:"msg"`
	// Data is the anchoring status.
	Data anchorData `json:"data"`
}

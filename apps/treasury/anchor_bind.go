package treasury

import (
	"context"
	"net/http"
	"strconv"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/wallets"
	"github.com/luxfi/geth/common"
	"github.com/zap-proto/zip"
)

// signerData is the wallet that will sign the next anchor.
type signerData struct {
	// BoundAnchorSigner is the EVM address now signing anchors. Fund it for gas.
	BoundAnchorSigner string `json:"boundAnchorSigner"`
	// ChainID is the EVM chain the signer is bound for.
	ChainID int64 `json:"chainId"`
	// Org is the org whose treasury wallet was resolved.
	Org string `json:"org"`
}

// signerOut is signerData in the admin envelope.
type signerOut struct {
	// Status is "ok" on success.
	Status string `json:"status"`
	// Msg carries an operator-facing note; empty on success.
	Msg string `json:"msg"`
	// Data is the bound signer.
	Data signerData `json:"data"`
}

// adminSetAnchorSigner installs the reserve's threshold MPC wallet as the signer
// for on-chain anchors, and returns its EVM address so an operator can fund it
// for gas. It provisions-or-resolves the caller org's treasury wallet on the
// deployed MPC ring and installs it, so every later anchor commits the ledger
// root SIGNED BY THE QUORUM WALLET instead of a lone KMS key. Idempotent — a
// repeat resolves the same wallet, which is why the address is a PUT. SuperAdmin
// only.
func (o ops) adminSetAnchorSigner(ctx context.Context, _ *noInput) (*signerOut, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	chain := "eip155:" + strconv.FormatInt(o.s.State.anchor.chainID, 10)
	addr, sign, ok := wallets.TreasuryAnchorSigner(ctx, org, chain)
	if !ok {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"treasury MPC custody not configured (deploy the ring + set CLOUD_WALLETS_MPC_ADDR)")
	}
	BindAnchorSigner(common.HexToAddress(addr), sign)
	o.s.Log.Info("treasury: bound MPC anchor signer", "org", org, "address", addr, "chainId", o.s.State.anchor.chainID)
	return &signerOut{Status: "ok", Data: signerData{
		BoundAnchorSigner: addr,
		ChainID:           o.s.State.anchor.chainID,
		Org:               org,
	}}, nil
}

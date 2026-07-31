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

// BoundSigner is the wallet now signing anchor commits.
type BoundSigner struct {
	// BoundAnchorSigner is the EVM address of the bound MPC wallet — fund it for gas.
	BoundAnchorSigner string `json:"boundAnchorSigner"`
	// ChainID is the Hanzo L1 chain the signer was bound for.
	ChainID int64 `json:"chainId"`
	// Org is the org whose treasury wallet was resolved.
	Org string `json:"org"`
}

// BoundSignerOut is the admin envelope around a signer binding.
type BoundSignerOut struct {
	Status string       `json:"status"`
	Msg    string       `json:"msg"`
	Data   *BoundSigner `json:"data"`
}

// bindAnchor binds the treasury MPC wallet as the on-chain anchor signer. Subsequent
// anchor commits are then signed BY THE QUORUM WALLET instead of the lone KMS key. It
// provisions-or-resolves the caller org's treasury wallet on the deployed MPC
// ring and installs it. SuperAdmin only, and idempotent — a repeat resolves the same
// wallet. Answers with the bound EVM address so the operator can fund it for gas.
func (o ops) bindAnchor(ctx context.Context, _ *struct{}) (*BoundSignerOut, error) {
	c, err := admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	org, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	chain := "eip155:" + strconv.FormatInt(s.State.anchor.chainID, 10)
	addr, sign, ok := wallets.TreasuryAnchorSigner(ctx, org, chain)
	if !ok {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"treasury MPC custody not configured (deploy the ring + set CLOUD_WALLETS_MPC_ADDR)")
	}
	BindAnchorSigner(common.HexToAddress(addr), sign)
	s.Log.Info("treasury: bound MPC anchor signer", "org", org, "address", addr, "chainId", s.State.anchor.chainID)
	return &BoundSignerOut{Status: "ok", Data: &BoundSigner{
		BoundAnchorSigner: addr,
		ChainID:           s.State.anchor.chainID,
		Org:               org,
	}}, nil
}

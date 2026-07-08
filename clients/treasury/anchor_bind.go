package treasury

import (
	"net/http"
	"strconv"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/wallets"
	"github.com/luxfi/geth/common"
	"github.com/zap-proto/zip"
)

// adminBindAnchor binds the reserve's treasury MPC wallet as the on-chain anchor
// signer (POST /v1/admin/treasury/bind-anchor). It provisions-or-resolves the
// caller-org's KindTreasury wallet on the deployed luxfi/mpc ring and installs it
// via BindAnchorSigner, so subsequent POST /v1/admin/treasury/anchor commits the
// ledger root SIGNED BY THE QUORUM WALLET (the reserve's threshold MPC wallet)
// instead of the lone KMS key. Global-admin only; idempotent (a repeat resolves
// the same wallet). Returns the bound EVM address so the operator can fund it for
// gas on the Hanzo L1.
func (s *svc) adminBindAnchor(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	chain := "eip155:" + strconv.FormatInt(s.anchor.chainID, 10)
	addr, sign, ok := wallets.TreasuryAnchorSigner(c.Context(), org, chain)
	if !ok {
		return zip.Errorf(http.StatusServiceUnavailable,
			"treasury MPC custody not configured (deploy the ring + set CLOUD_WALLETS_MPC_ADDR)")
	}
	BindAnchorSigner(common.HexToAddress(addr), sign)
	s.log.Info("treasury: bound MPC anchor signer", "org", org, "address", addr, "chainId", s.anchor.chainID)
	return adminOK(c, map[string]any{
		"boundAnchorSigner": addr,
		"chainId":           s.anchor.chainID,
		"org":               org,
	})
}

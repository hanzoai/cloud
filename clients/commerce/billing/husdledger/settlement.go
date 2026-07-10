package husdledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/husdsettlement"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// Settlement (Step 5). Metered usage debits the ledger OFF chain (a Withdraw row),
// but the org's on-chain HUSD balance still reflects only its mints — so the chain
// OVER-states the balance by the amount consumed (plus any reclaimed/expired
// grants). Settlement sweeps that drift back to the treasury with an org→treasury
// HUSD transfer signed by the ORG's derived key, driving balanceOf(orgAddr) down
// to the off-chain ledger balance. It is self-correcting (a re-run computes a
// fresh, ~zero drift) and per-threshold (dust never churns gas).

// SettleResult is the per-org outcome of a settlement pass.
type SettleResult struct {
	Org            string `json:"org"`
	OrgAddress     string `json:"orgAddress"`
	OnChainCents   int64  `json:"onChainCents"`
	SpendableCents int64  `json:"spendableCents"`
	DriftCents     int64  `json:"driftCents"`
	Settled        bool   `json:"settled"`
	TxHash         string `json:"txHash,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Settle sweeps every org whose on-chain balance has drifted above its ledger
// balance by at least the threshold. Idempotent: a second pass finds ~zero drift.
// Driven by an external CronJob (mirrors the OSS/contributor payout endpoints).
func (s *Service) Settle(ctx context.Context) ([]SettleResult, error) {
	if !s.Enabled() {
		return nil, husd.ErrNotConfigured
	}
	db := datastore.New(context.Background())
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(db).GetAll(&orgs); err != nil {
		return nil, err
	}
	out := make([]SettleResult, 0, len(orgs))
	for _, o := range orgs {
		if strings.TrimSpace(o.Name) == "" {
			continue
		}
		r, err := s.SettleOrg(ctx, o.Name)
		if err != nil {
			r.Reason = "error: " + err.Error()
			log.Warn("husdledger: settle org %s: %v", o.Name, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// SettleOrg computes one org's drift and, if it clears the threshold, sweeps it
// org→treasury (signed by the org's derived key) and records an audit line.
func (s *Service) SettleOrg(ctx context.Context, org string) (SettleResult, error) {
	res := SettleResult{Org: org}
	acct, err := treasury.DeriveAccount(s.seed, org)
	if err != nil {
		return res, err
	}
	res.OrgAddress = acct.Address
	test := s.settleTest()

	onchainCents, err := husdindex.OnChainBalanceCents(ctx, s.balanceReader, acct.Address, s.cfg.Decimals)
	if err != nil {
		return res, fmt.Errorf("husdledger: settle balanceOf %s: %w", org, err)
	}
	spendable, err := orgLedgerSpendableCents(org, test)
	if err != nil {
		return res, err
	}
	drift, settle := husd.SettlementDrift(onchainCents, spendable, s.settleThreshold())
	res.OnChainCents = onchainCents
	res.SpendableCents = spendable
	res.DriftCents = drift
	if !settle {
		res.Reason = "below_threshold"
		return res, nil
	}

	driftWei, err := husd.CentsToWei(drift, s.cfg.Decimals)
	if err != nil {
		return res, err
	}
	txHash, err := s.settleTransfer(ctx, blockchain.TokenTransfer{
		ChainID:      s.cfg.ChainID,
		RPCURL:       s.cfg.RPCURL,
		TokenAddress: s.cfg.TokenAddress,
		// SIGNER = the org's OWN derived key, so the transfer's `from` is the org
		// address (NOT the treasury). The field is named TreasuryKey on the seam
		// but it is simply "the signing key".
		TreasuryKey: acct.PrivateKeyHex(),
		To:          s.treasuryAddr,
		AmountWei:   driftWei,
		GasLimit:    s.cfg.GasLimit,
	})
	if err != nil {
		return res, fmt.Errorf("husdledger: settle transfer %s: %w", org, err)
	}
	res.Settled = true
	res.TxHash = txHash
	s.recordSettlement(org, acct.Address, drift, test, txHash)
	return res, nil
}

// settleTest reports which ledger Test partition this chain settles: a chain only
// ever hosts ONE partition (the testnet chain holds only test mints, mainnet only
// live), so the partition is a pure function of the configured chain id.
func (s *Service) settleTest() bool { return s.cfg.ChainID != husd.DefaultChainID }

func (s *Service) settleThreshold() int64 {
	if v := os.Getenv("HUSD_SETTLE_THRESHOLD_CENTS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func (s *Service) recordSettlement(org, addr string, cents int64, test bool, txHash string) {
	db := datastore.New(nscontext.WithNamespace(context.Background(), systemNamespace))
	rec := husdsettlement.New(db)
	sum := sha256.Sum256([]byte("husd-settlement\x00" + strings.ToLower(txHash)))
	rec.SetId(hex.EncodeToString(sum[:16]))
	rec.OrgID = org
	rec.OrgAddress = addr
	rec.AmountCents = cents
	rec.Test = test
	rec.ChainID = s.cfg.ChainID
	rec.TxHash = strings.ToLower(txHash)
	rec.SettledAt = time.Now().UTC()
	if err := rec.Put(); err != nil {
		log.Warn("husdledger: settlement audit record failed for %s (tx=%s): %v", org, txHash, err)
	}
}

// orgLedgerSpendableCents sums an org's whole off-chain ledger (all subjects) in
// the given Test partition: Σ non-expired Deposits − Σ Withdraws, usd. This is
// what the org's on-chain balance should equal AFTER settlement — the chain's
// balanceOf is exactly Σ mints (deposits), so the gap to this is what usage +
// expiry consumed. In-org Transfers net to zero org-wide and Holds are
// reservations, so both are excluded (matching TallyTransactions' balance math).
func orgLedgerSpendableCents(org string, test bool) (int64, error) {
	db := datastore.New(nscontext.WithNamespace(context.Background(), org))
	root := db.NewKey("synckey", "", 1, nil)
	transs := make([]*transaction.Transaction, 0)
	if _, err := transaction.Query(db).Ancestor(root).
		Filter("Test=", test).Filter("Currency=", currency.Type("usd")).GetAll(&transs); err != nil {
		return 0, err
	}
	now := time.Now()
	var sum int64
	for _, t := range transs {
		switch t.Type {
		case transaction.Deposit:
			if !t.ExpiresAt.IsZero() && t.ExpiresAt.Before(now) {
				continue // expired grant — excluded (drift reclaims it to treasury)
			}
			sum += int64(t.Amount)
		case transaction.Withdraw:
			sum -= int64(t.Amount)
		}
	}
	return sum, nil
}

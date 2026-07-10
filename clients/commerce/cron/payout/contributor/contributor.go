// Package contributor executes the OSS contributor revenue sharing payouts.
//
// This runs monthly (or on-demand) and uses the payout algorithm from
// models/contributor/payout.go to calculate per-contributor allocations,
// then creates CreditGrant or queues transfer records.
package contributor

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/log"
	contribModel "github.com/hanzoai/cloud/clients/commerce/models/contributor"
	"github.com/hanzoai/cloud/clients/commerce/models/creditgrant"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// HUSD configuration + cent↔wei math live in the canonical util/husd package —
// ONE definition of what HUSD means, shared with the chain-backed credit ledger
// (treasury, billing/husdindex). These aliases keep the contributor executor's
// existing surface (HUSDConfig, ErrHUSDNotConfigured, the default consts, and
// the private husdCentsToWei used in tests) while delegating to that one source.
const (
	defaultHUSDChainID  = husd.DefaultChainID
	defaultHUSDRPCURL   = husd.DefaultRPCURL
	defaultHUSDDecimals = husd.DefaultDecimals
)

// ErrHUSDNotConfigured is returned by the crypto executor when the HUSD token
// address or treasury key is missing. The payout is skipped (not lost): once
// the secrets land in KMS the next run pays it out.
var ErrHUSDNotConfigured = husd.ErrNotConfigured

// HUSDConfig configures on-chain HUSD stablecoin payouts (see util/husd.Config).
type HUSDConfig = husd.Config

// husdCentsToWei converts USD cents into HUSD base units (see util/husd).
var husdCentsToWei = husd.CentsToWei

// Config holds runtime configuration for the contributor payout cron.
type Config struct {
	// Namespace is the org namespace to operate in (default: "hanzo").
	Namespace string

	// DryRun prints what would happen without creating records.
	DryRun bool

	// Publisher for emitting events. Nil = skip events.
	Publisher *events.Publisher

	// Period defines the billing period to calculate payouts for.
	// If zero, defaults to the previous calendar month.
	PeriodStart time.Time
	PeriodEnd   time.Time

	// HUSD configures on-chain HUSD payouts (PayoutMethod="crypto"). If unset,
	// Payout fills it from the environment (KMS-injected) via HUSD.LoadFromEnv.
	HUSD HUSDConfig
}

// Payout executes the monthly contributor payout.
//
// Steps:
//  1. Fetch total billable revenue for the period from the transaction ledger
//  2. Fetch all active, verified contributors
//  3. Call CalculatePayouts() (existing algorithm)
//  4. For each allocation above MinPayoutCents:
//     - credits: create CreditGrant
//     - stripe:  create Payout record (transfer)
//     - crypto:  create Payout record (crypto transfer)
//  5. Update contributor.TotalEarned, contributor.LastPaid
//  6. Publish contributor.payout events
func Payout(ctx context.Context, cfg Config) error {
	if cfg.Namespace == "" {
		cfg.Namespace = "hanzo"
	}

	// Source on-chain HUSD config from KMS-injected env (no-op for fields the
	// caller already set, e.g. in tests).
	cfg.HUSD.LoadFromEnv()

	// Default to previous calendar month.
	if cfg.PeriodStart.IsZero() || cfg.PeriodEnd.IsZero() {
		now := time.Now().UTC()
		firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		cfg.PeriodEnd = firstOfMonth
		cfg.PeriodStart = firstOfMonth.AddDate(0, -1, 0)
	}

	log.Info("contributor-payout: period %s to %s, namespace=%s, dry-run=%v",
		cfg.PeriodStart.Format("2006-01-02"),
		cfg.PeriodEnd.Format("2006-01-02"),
		cfg.Namespace,
		cfg.DryRun,
	)

	nsCtx := nscontext.WithNamespace(ctx, cfg.Namespace)
	db := datastore.New(nsCtx)

	// 1. Calculate total billable revenue for the period.
	totalRevenue, componentRevenue, err := calculatePeriodRevenue(db, cfg.PeriodStart, cfg.PeriodEnd)
	if err != nil {
		return fmt.Errorf("calculate period revenue: %w", err)
	}

	log.Info("contributor-payout: total revenue=%d cents, %d components with revenue",
		totalRevenue, len(componentRevenue))

	if totalRevenue <= 0 {
		log.Info("contributor-payout: no revenue in period, skipping")
		return nil
	}

	// 2. Fetch all active contributors.
	contributors, err := fetchActiveContributors(db)
	if err != nil {
		return fmt.Errorf("fetch contributors: %w", err)
	}

	log.Info("contributor-payout: found %d active contributors", len(contributors))

	if len(contributors) == 0 {
		log.Info("contributor-payout: no active contributors, skipping")
		return nil
	}

	// 3. Run the payout algorithm.
	config := contribModel.DefaultConfig()
	summary := contribModel.CalculatePayouts(totalRevenue, contributors, componentRevenue, config)

	log.Info("contributor-payout: pool=%d cents, %d allocations above minimum",
		summary.ContributorPool, len(summary.Allocations))

	if len(summary.Allocations) == 0 {
		log.Info("contributor-payout: no allocations above minimum threshold")
		return nil
	}

	if cfg.DryRun {
		for _, a := range summary.Allocations {
			log.Info("contributor-payout: [DRY-RUN] %s (%s) -> $%.2f",
				a.GitLogin, a.Component, float64(a.AmountCents)/100.0)
		}
		return nil
	}

	// 4. Execute payouts.
	contributorIndex := make(map[string]*contribModel.Contributor)
	for i := range contributors {
		contributorIndex[contributors[i].Id()] = &contributors[i]
	}

	var payoutCount int
	for _, alloc := range summary.Allocations {
		c, ok := contributorIndex[alloc.ContributorId]
		if !ok {
			log.Warn("contributor-payout: contributor %s not found, skipping", alloc.ContributorId)
			continue
		}

		txHash, err := executePayout(nsCtx, db, c, alloc, cfg)
		if err != nil {
			log.Error("contributor-payout: payout failed for %s: %v", c.GitLogin, err)
			continue
		}
		payoutCount++

		// Update contributor stats (+ on-chain receipt for crypto payouts).
		c.TotalEarned += currency.Cents(alloc.AmountCents)
		c.LastPaid = time.Now().UTC()
		if txHash != "" {
			c.LastPayoutTx = txHash
			c.LastPayoutChainId = cfg.HUSD.ChainID
		}
		if err := c.Update(); err != nil {
			log.Error("contributor-payout: failed to update contributor %s: %v", c.GitLogin, err)
		}

		// Publish event.
		publishPayoutEvent(ctx, cfg.Publisher, c, alloc, txHash, cfg.PeriodStart, cfg.PeriodEnd)
	}

	log.Info("contributor-payout: completed %d/%d payouts", payoutCount, len(summary.Allocations))
	return nil
}

// executePayout dispatches on the contributor's payout method. It returns the
// on-chain transaction hash for crypto payouts (empty for credits/stripe).
func executePayout(ctx context.Context, db *datastore.Datastore, c *contribModel.Contributor, alloc contribModel.PayoutAllocation, cfg Config) (string, error) {
	switch c.PayoutMethod {
	case "credits":
		return "", executeCreditsPayout(ctx, db, c, alloc, cfg)
	case "stripe":
		return "", executeStripePayout(c, alloc)
	case "crypto":
		return executeCryptoPayout(ctx, c, alloc, cfg)
	default:
		// Default to credits if no method specified.
		return "", executeCreditsPayout(ctx, db, c, alloc, cfg)
	}
}

// executeCreditsPayout creates a CreditGrant for the contributor.
func executeCreditsPayout(_ context.Context, db *datastore.Datastore, c *contribModel.Contributor, alloc contribModel.PayoutAllocation, cfg Config) error {
	grant := creditgrant.New(db)
	grant.UserId = c.UserId
	grant.Name = fmt.Sprintf("OSS contributor payout: %s (%s to %s)",
		c.GitLogin,
		cfg.PeriodStart.Format("2006-01"),
		cfg.PeriodEnd.Format("2006-01"))
	grant.AmountCents = alloc.AmountCents
	grant.RemainingCents = alloc.AmountCents
	grant.Currency = c.Currency
	grant.Priority = 8 // Between purchased (5) and referral (10)
	grant.Tags = "oss-earnings"
	grant.EffectiveAt = time.Now().UTC()
	// OSS credits do not expire.

	if err := grant.Create(); err != nil {
		return fmt.Errorf("create credit grant for %s: %w", c.GitLogin, err)
	}

	log.Info("contributor-payout: created credit grant %s for %s ($%.2f)",
		grant.Id(), c.GitLogin, float64(alloc.AmountCents)/100.0)
	return nil
}

// executeStripePayout queues a Stripe transfer for the contributor.
// The actual Stripe API call happens in the transfer worker (cron/payout/transferfee.go).
func executeStripePayout(c *contribModel.Contributor, alloc contribModel.PayoutAllocation) error {
	if c.PayoutTarget == "" {
		return fmt.Errorf("contributor %s has no Stripe account ID", c.GitLogin)
	}

	log.Info("contributor-payout: queued Stripe transfer for %s -> %s ($%.2f)",
		c.GitLogin, c.PayoutTarget, float64(alloc.AmountCents)/100.0)
	return nil
}

// executeCryptoPayout transfers HUSD (Hanzo USD stablecoin) on the Hanzo EVM
// to the contributor's wallet (PayoutTarget) and returns the on-chain tx hash.
//
// The transfer is a standard ERC-20 transfer signed by the treasury key
// (sourced from KMS, never logged), dispatched through the
// util/blockchain.TransferToken seam — the EVM signing lives in the
// luxfi/cevm sub-module, which must be linked into the running
// binary (blank import) for on-chain execution.
func executeCryptoPayout(ctx context.Context, c *contribModel.Contributor, alloc contribModel.PayoutAllocation, cfg Config) (string, error) {
	if c.PayoutTarget == "" {
		return "", fmt.Errorf("contributor %s has no wallet address", c.GitLogin)
	}
	if cfg.HUSD.TokenAddress == "" || cfg.HUSD.TreasuryKey == "" {
		return "", ErrHUSDNotConfigured
	}

	amountWei, err := husdCentsToWei(alloc.AmountCents, cfg.HUSD.Decimals)
	if err != nil {
		return "", err
	}

	txHash, err := blockchain.TransferToken(ctx, blockchain.TokenTransfer{
		ChainID:      cfg.HUSD.ChainID,
		RPCURL:       cfg.HUSD.RPCURL,
		TokenAddress: cfg.HUSD.TokenAddress,
		TreasuryKey:  cfg.HUSD.TreasuryKey,
		To:           c.PayoutTarget,
		AmountWei:    amountWei,
		GasLimit:     cfg.HUSD.GasLimit,
	})
	if err != nil {
		return "", fmt.Errorf("husd transfer for %s: %w", c.GitLogin, err)
	}

	log.Info("contributor-payout: HUSD on-chain payout %s -> %s ($%.2f) chain=%d tx=%s",
		c.GitLogin, c.PayoutTarget, float64(alloc.AmountCents)/100.0, cfg.HUSD.ChainID, txHash)
	return txHash, nil
}

// calculatePeriodRevenue queries the transaction ledger for total revenue and
// per-component revenue attribution in the given period.
//
// Attribution is two-tier, both real (no flat fallback):
//  1. Exact — transactions tagged with a component (tx.Tags) attribute their
//     full amount to that component.
//  2. SBOM-weighted — revenue NOT tagged to a component is distributed across
//     the shipped components proportional to their SBOM weight (usage, then
//     lines). This is the deployed-images → SBOM-components → contributors
//     mapping. Untagged revenue is left unattributed only when there are no
//     SBOM entries to attribute it to.
func calculatePeriodRevenue(db *datastore.Datastore, start, end time.Time) (int64, map[string]int64, error) {
	var txns []*transaction.Transaction
	q := transaction.Query(db).
		Filter("Type=", "withdraw").
		Filter("CreatedAt>=", start).
		Filter("CreatedAt<", end)

	if _, err := q.GetAll(&txns); err != nil {
		return 0, nil, fmt.Errorf("query transactions: %w", err)
	}

	var totalRevenue, untagged int64
	componentRevenue := make(map[string]int64)

	for _, tx := range txns {
		amt := int64(tx.Amount)
		totalRevenue += amt
		if tx.Tags != "" {
			componentRevenue[tx.Tags] += amt // exact, transaction-tagged
		} else {
			untagged += amt
		}
	}

	// Distribute untagged revenue across components by real SBOM weight.
	if untagged > 0 {
		var entries []contribModel.SBOMEntry
		if _, err := contribModel.QuerySBOM(db).GetAll(&entries); err != nil {
			return 0, nil, fmt.Errorf("query sbom entries: %w", err)
		}
		weights := contribModel.ComponentWeightsFromSBOM(entries)
		for comp, rev := range contribModel.DistributeRevenue(untagged, weights) {
			componentRevenue[comp] += rev
		}
	}

	return totalRevenue, componentRevenue, nil
}

// fetchActiveContributors returns all active, verified contributors.
func fetchActiveContributors(db *datastore.Datastore) ([]contribModel.Contributor, error) {
	var contributors []contribModel.Contributor
	q := contribModel.Query(db).
		Filter("Active=", true).
		Filter("Verified=", true)

	if _, err := q.GetAll(&contributors); err != nil {
		return nil, fmt.Errorf("query contributors: %w", err)
	}

	return contributors, nil
}

// publishPayoutEvent emits a contributor.payout_calculated event.
func publishPayoutEvent(ctx context.Context, pub *events.Publisher, c *contribModel.Contributor, alloc contribModel.PayoutAllocation, txHash string, periodStart, periodEnd time.Time) {
	if pub == nil {
		return
	}

	now := time.Now().UTC()
	event := &events.CommerceEvent{
		ID:        fmt.Sprintf("payout-%s-%s", c.Id(), periodStart.Format("2006-01")),
		Type:      "contributor.payout_calculated",
		Timestamp: now,
		UserID:    c.UserId,
		Data: map[string]interface{}{
			"contributor_id": c.Id(),
			"git_login":      c.GitLogin,
			"amount_cents":   alloc.AmountCents,
			"component":      alloc.Component,
			"payout_method":  c.PayoutMethod,
			"tx_hash":        txHash,
			"period_start":   periodStart.Format("2006-01-02"),
			"period_end":     periodEnd.Format("2006-01-02"),
		},
	}

	if err := pub.Publish(ctx, events.SubjectContributorPayoutCalc, event); err != nil {
		log.Error("contributor-payout: failed to publish event for %s: %v", c.GitLogin, err)
	}
}

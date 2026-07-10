package billing

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/bucket"
	"github.com/hanzoai/cloud/clients/commerce/billing/husdledger"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// This file is the Step-4 rewire seam: the billing money-in handlers (deposit,
// welcome/starter grant, top-up) call chainMintCredit INSTEAD of writing a DB
// credit row WHEN the chain-backed ledger is enabled. A credit then exists only
// as HUSD minted by the treasury key (which commerce does not hold) and projected
// back into the ledger the balance endpoint reads. When the chain path is not
// configured, chainCreditEnabled() is false and each handler keeps its existing
// DB write — so a deploy without HUSD configured is byte-for-byte unchanged, and
// the migration (Step 6) flips it deliberately.

// chainCreditEnabled reports whether the on-chain HUSD credit path is live.
func chainCreditEnabled() bool { return husdledger.Default().Enabled() }

// bucketForTags maps a deposit's ledger tags to the on-chain mint bucket, using
// the SAME classifier the balance split uses (so the mint bucket and the read
// bucket can never disagree): a grant tag → Credit, anything else → Prepaid.
func bucketForTags(tags string) treasury.Bucket {
	if bucket.DepositKind(tags) == bucket.Credit {
		return treasury.BucketCredit
	}
	return treasury.BucketPrepaid
}

// chainMintCredit performs the authorized on-chain HUSD mint for a credit and
// returns the receipt. The caller MUST already have established mint authority
// (a PlatformOnly route, a settled payment, or a server-fixed grant) — exactly
// as before a DB deposit — so wrapping the ctx authorized here is faithful: it is
// the SAME capability that satisfies the ledger sink on the DB path.
func chainMintCredit(c *gin.Context, org *organization.Organization, subject string, cents int64, b treasury.Bucket, reason, idemKey string) (*treasury.Receipt, error) {
	ctx := mintauth.WithAuthorized(org.Namespaced(c))
	return husdledger.Default().MintCredit(ctx, treasury.MintRequest{
		OrgID:       org.Namespace(),
		Subject:     subject,
		AmountCents: cents,
		Bucket:      b,
		Reason:      reason,
		IdemKey:     idemKey,
		Test:        org.TestMode(),
	})
}

// welcomeMintKey is the DETERMINISTIC idempotency key for a subject's one-time
// welcome/starter credit. It is shared by GrantStarterCredit (/credit),
// PostMyWelcome (/me/welcome), and GrantStarter (/grant-starter) so the welcome
// is minted AT MOST ONCE per subject across ALL three endpoints, regardless of
// which fires first — the on-chain analogue of the starter-credit tag dedupe.
func welcomeMintKey(org *organization.Organization, subject string) string {
	return "welcome:" + org.Namespace() + ":" + subject
}

// randomIdemKey returns a fresh idempotency key for a mint that carries no
// caller-supplied key, so distinct deposits (e.g. repeated settlements to the
// same user) each mint exactly once and never collapse onto one on-chain tx.
func randomIdemKey(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// SyncHUSD runs one indexer pass — the backfill + reconcile safety net behind the
// synchronous mint-time projection. Driven by an external CronJob (mirrors the
// contributor-payout execute endpoint). Platform-only (touches the money ledger).
//
//	POST /v1/billing/husd/sync
func SyncHUSD(c *gin.Context) {
	svc := husdledger.Default()
	if !svc.Enabled() {
		http.Fail(c, 503, "chain-backed credit ledger not enabled", nil)
		return
	}
	n, err := svc.SyncOnce(middleware.GetContext(c))
	if err != nil {
		http.Fail(c, 500, "husd indexer sync failed", err)
		return
	}
	c.JSON(200, gin.H{"projected": n})
}

// SettleHUSD sweeps every org's on-chain drift back to the treasury (metered
// usage settled org→treasury). Platform-only, CronJob-driven. Idempotent
// (self-correcting drift). Returns the per-org settlement outcomes.
//
//	POST /v1/billing/husd/settle
func SettleHUSD(c *gin.Context) {
	svc := husdledger.Default()
	if !svc.Enabled() {
		http.Fail(c, 503, "chain-backed credit ledger not enabled", nil)
		return
	}
	results, err := svc.Settle(middleware.GetContext(c))
	if err != nil {
		http.Fail(c, 500, "husd settlement failed", err)
		return
	}
	settled := 0
	for _, r := range results {
		if r.Settled {
			settled++
		}
	}
	c.JSON(200, gin.H{"orgs": len(results), "settled": settled, "results": results})
}

// MigrateHUSD moves every org's existing DB-ledger balance onto chain with zero
// drift (one-time). Dry-run by default (reports the snapshot + reconcile without
// minting); pass ?execute=true to mint. Platform-only.
//
//	POST /v1/billing/husd/migrate?execute=true
func MigrateHUSD(c *gin.Context) {
	svc := husdledger.Default()
	if !svc.Enabled() {
		http.Fail(c, 503, "chain-backed credit ledger not enabled", nil)
		return
	}
	dryRun := c.Query("execute") != "true"
	results, err := svc.Migrate(middleware.GetContext(c), dryRun)
	if err != nil {
		http.Fail(c, 500, "husd migration failed", err)
		return
	}
	migrated, reconciled := 0, 0
	for _, r := range results {
		if !r.DryRun && r.MintedCents > 0 {
			migrated++
		}
		if r.Reconciled {
			reconciled++
		}
	}
	c.JSON(200, gin.H{"orgs": len(results), "dryRun": dryRun, "migrated": migrated, "reconciled": reconciled, "results": results})
}

// StatusHUSD reports the chain-ledger configuration + whether it is enabled — a
// read-only observability surface (no secrets: the treasury key is never exposed).
//
//	GET /v1/billing/husd/status
func StatusHUSD(c *gin.Context) {
	svc := husdledger.Default()
	cfg := svc.Config()
	c.JSON(200, gin.H{
		"enabled":  svc.Enabled(),
		"chainId":  cfg.ChainID,
		"token":    cfg.TokenAddress,
		"decimals": cfg.Decimals,
		"rpc":      cfg.RPCURL,
	})
}

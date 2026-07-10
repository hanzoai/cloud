// OSS developer payout accrual — the persistent counterpart to ossattr.
//
// AccrueOSSPayout is the fire-and-forget hook the usage path calls after it
// records an org's charge. It is structurally identical to TrackRevenueShare
// (revshare.go): same call site, same "compute a capped share of the charge
// and write accrual records" shape, same fail-soft posture (errors logged,
// never propagated to the billing path). The difference is the recipient — not
// a referrer, but the upstream OSS packages the org's deployment depends on,
// resolved from the SBOMs emitted on deploy.
package engine

import (
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/ossaccrual"
	"github.com/hanzoai/cloud/clients/commerce/models/sbomrecord"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/ossattr"
	"github.com/hanzoai/cloud/clients/commerce/ossfunding"
)

// platformDB returns a datastore for the global platform/system namespace where
// OSS accruals (a Hanzo->OSS liability aggregated across all paying orgs) live.
// SBOM records are likewise global (an image is built once, used by many orgs),
// so we read them from the same namespace. We derive it from the spend db's
// context but reset the namespace to the system namespace.
func platformDB(spendDB *datastore.Datastore) *datastore.Datastore {
	db := datastore.New(spendDB.Context)
	db.SetNamespace("system")
	return db
}

// attributableComponents returns the union of OSS components an org's spend
// should be attributed against. Default model: an org running on the Hanzo
// cloud platform benefits from ALL the OSS in the deployed platform images, so
// we union the components across every SBOMRecord. De-dup by PURL happens in
// ossattr.Attribute, so returning the raw union (with duplicates) is correct.
//
// This is the ONE place "what does an org run?" is defined; narrowing it to a
// per-org service subset later is a change here only — the math and ledger are
// untouched (separation of concerns).
func attributableComponents(pdb *datastore.Datastore) ([]ossattr.Package, error) {
	records := make([]*sbomrecord.SBOMRecord, 0, 64)
	rootKey := pdb.NewKey("synckey", "", 1, nil)
	if _, err := sbomrecord.Query(pdb).Ancestor(rootKey).GetAll(&records); err != nil {
		return nil, err
	}

	pkgs := make([]ossattr.Package, 0, 256)
	for _, rec := range records {
		for _, c := range rec.Components {
			scope := ossattr.ScopeTransitive
			if c.Scope == string(ossattr.ScopeDirect) {
				scope = ossattr.ScopeDirect
			}
			pkgs = append(pkgs, ossattr.Package{
				PURL:      c.PURL,
				Name:      c.Name,
				Ecosystem: c.Ecosystem,
				Version:   c.Version,
				Scope:     scope,
			})
		}
	}
	return pkgs, nil
}

// AccrueOSSPayout attributes a fraction (<=25%) of one org charge across the
// OSS packages the org's deployment depends on and writes one OSSAccrual line
// per package. Fire-and-forget: call as `go AccrueOSSPayout(...)`.
//
// Parameters mirror TrackRevenueShare:
//   - spendDB: datastore scoped to the spending org's namespace (for context).
//   - org, user: who incurred the charge.
//   - chargeAmount, cur: the usage charge.
//   - sourceTxnID: the withdraw transaction id (provenance + idempotency).
//   - test: org is not live -> mark accruals test (excluded from real payouts).
func AccrueOSSPayout(spendDB *datastore.Datastore, org, user string, chargeAmount currency.Cents, cur currency.Type, sourceTxnID string, test bool) {
	prog := config.GetOSSPayoutProgram()
	if prog == nil || !prog.Active {
		return
	}
	if chargeAmount <= 0 {
		return
	}

	pdb := platformDB(spendDB)

	pkgs, err := attributableComponents(pdb)
	if err != nil {
		log.Error("osspayout: failed to load SBOM components for org %s: %v", org, err)
		return
	}
	if len(pkgs) == 0 {
		// No SBOMs ingested yet — nothing to attribute. Not an error.
		return
	}

	result := ossattr.Attribute(int64(chargeAmount), pkgs, prog.Policy())
	if result.PoolCents <= 0 || len(result.Allocations) == 0 {
		return
	}

	resolver := ossfunding.PURLResolver{}
	written := 0
	for _, alloc := range result.Allocations {
		if alloc.Cents <= 0 {
			continue
		}

		idem := ossattr.AccrualID(org, sourceTxnID, alloc.PURL)

		// Idempotency: skip if this (org, txn, package) already accrued.
		existing := make([]*ossaccrual.OSSAccrual, 0, 1)
		dup, derr := ossaccrual.Query(pdb).
			Filter("IdempotencyKey=", idem).
			Limit(1).
			GetAll(&existing)
		if derr == nil && len(dup) > 0 {
			continue
		}

		target := resolver.Resolve(alloc.PURL)
		status := ossaccrual.Pending
		if target.Kind == ossfunding.KindUnresolved {
			status = ossaccrual.Held
		}

		ac := ossaccrual.New(pdb)
		ac.PURL = alloc.PURL
		ac.Name = alloc.Name
		ac.Ecosystem = alloc.Ecosystem
		ac.Version = alloc.Version
		ac.Scope = ossaccrual.Scope(alloc.Scope)
		ac.SpendOrg = org
		ac.SpendUser = user
		ac.SourceTxnID = sourceTxnID
		ac.SpendCents = chargeAmount
		ac.PoolFraction = result.PoolFraction
		ac.Weight = alloc.Weight
		ac.ShareRatio = alloc.ShareRatio
		ac.Currency = cur
		ac.Amount = currency.Cents(alloc.Cents)
		ac.Status = status
		ac.FundingTarget = target.String()
		ac.FundingKind = string(target.Kind)
		ac.IdempotencyKey = idem
		ac.Test = test

		if err := ac.Create(); err != nil {
			log.Error("osspayout: failed to write accrual for %s (amount %d): %v", alloc.PURL, alloc.Cents, err)
			continue
		}
		written++
	}

	log.Info("osspayout: accrued %d cents (%.0f%% pool) across %d packages for org %s txn %s",
		result.PoolCents, result.PoolFraction*100, written, org, sourceTxnID)
}

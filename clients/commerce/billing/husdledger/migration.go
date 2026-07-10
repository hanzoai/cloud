package husdledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/bucket"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// Migration (Step 6). Move an org's existing DB-ledger balance onto chain with
// ZERO drift: snapshot each subject's credit/prepaid split, one-time treasury
// MINT the equivalent HUSD (bucketed) to the org address, and NEUTRALIZE the
// legacy DB deposits with an idempotent offset withdraw so the balance read
// (which sums the whole ledger, unchanged) now reflects the chain-sourced credit
// instead of the legacy rows. Reconcile asserts chain balanceOf(org) equals the
// pre-migration DB balance, exact to the cent.
//
// Net per subject (bucket.Compute math): legacy(C credit, P prepaid) + chain
// mint(C, P) − offset(C+P, credits-first) = (C+P) unchanged, now carried by the
// chain mint. Idempotent: mints key on migrate-<bucket>:<org>:<subject>, the
// offset on a deterministic id — a re-run is a no-op and self-heals a partial run.

// MigrateResult is one org's migration outcome.
type MigrateResult struct {
	Org               string `json:"org"`
	Subjects          int    `json:"subjects"`
	DBBalanceCents    int64  `json:"dbBalanceCents"`    // pre-migration DB balance (org total)
	MintedCents       int64  `json:"mintedCents"`       // total minted on chain this run
	ChainBalanceCents int64  `json:"chainBalanceCents"` // post-migration on-chain balanceOf(org)
	PostDBCents       int64  `json:"postDbCents"`       // post-migration DB balance (must equal DBBalance)
	Reconciled        bool   `json:"reconciled"`        // chain == DB == postDB, exact to the cent
	DryRun            bool   `json:"dryRun"`
	Reason            string `json:"reason,omitempty"`
}

// Migrate migrates every org's ledger onto chain (dry-run reports the snapshot +
// current chain balance without minting). Platform/CronJob-driven, one-time.
func (s *Service) Migrate(ctx context.Context, dryRun bool) ([]MigrateResult, error) {
	if !s.Enabled() {
		return nil, husd.ErrNotConfigured
	}
	db := datastore.New(context.Background())
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(db).GetAll(&orgs); err != nil {
		return nil, err
	}
	out := make([]MigrateResult, 0, len(orgs))
	for _, o := range orgs {
		if o.Name == "" {
			continue
		}
		r, err := s.MigrateOrg(ctx, o.Name, dryRun)
		if err != nil {
			r.Reason = "error: " + err.Error()
			log.Warn("husdledger: migrate org %s: %v", o.Name, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// subjectSplit pairs a subject with its pre-migration bucket split.
type subjectSplit struct {
	Subject string
	Split   bucket.Split
}

// MigrateOrg migrates ONE org and reconciles chain==DB to the cent.
func (s *Service) MigrateOrg(ctx context.Context, org string, dryRun bool) (MigrateResult, error) {
	res := MigrateResult{Org: org, DryRun: dryRun}
	test := s.settleTest()

	splits, err := orgSubjectSplits(org, test)
	if err != nil {
		return res, err
	}
	var dbTotal int64
	for _, ss := range splits {
		dbTotal += int64(ss.Split.Balance)
	}
	res.Subjects = len(splits)
	res.DBBalanceCents = dbTotal

	acct, err := treasury.DeriveAccount(s.seed, org)
	if err != nil {
		return res, err
	}

	if dryRun {
		res.Reason = "dry_run"
		if chain, cErr := onChainCents(ctx, s, acct.Address); cErr == nil {
			res.ChainBalanceCents = chain
		}
		return res, nil
	}

	// Mint each subject's credit + prepaid to the org address, then offset legacy.
	for _, ss := range splits {
		if c := int64(ss.Split.CreditsRemaining); c > 0 {
			if _, mErr := s.MintCredit(mintauth.WithAuthorized(context.Background()), treasury.MintRequest{
				OrgID: org, Subject: ss.Subject, AmountCents: c, Bucket: treasury.BucketCredit,
				Reason: "migration", Test: test, IdemKey: "migrate-credit:" + org + ":" + ss.Subject,
			}); mErr != nil {
				return res, fmt.Errorf("migrate mint credit %s/%s: %w", org, ss.Subject, mErr)
			}
			res.MintedCents += c
		}
		if p := int64(ss.Split.PrepaidBalance); p > 0 {
			if _, mErr := s.MintCredit(mintauth.WithAuthorized(context.Background()), treasury.MintRequest{
				OrgID: org, Subject: ss.Subject, AmountCents: p, Bucket: treasury.BucketPrepaid,
				Reason: "migration", Test: test, IdemKey: "migrate-prepaid:" + org + ":" + ss.Subject,
			}); mErr != nil {
				return res, fmt.Errorf("migrate mint prepaid %s/%s: %w", org, ss.Subject, mErr)
			}
			res.MintedCents += p
		}
		// Neutralize the legacy deposits PER BUCKET so the split is preserved:
		// a non-GPU offset cancels legacy credit (bucket.Compute draws it
		// credits-first), a GPU-tagged offset cancels legacy prepaid (drawn
		// prepaid-only). Combined with the equal-amount chain mints, the net
		// per-bucket balance is unchanged — now carried by the chain credit.
		if err := writeMigrationOffset(org, ss.Subject, "credit", int64(ss.Split.CreditsRemaining), false, test); err != nil {
			return res, fmt.Errorf("migrate offset credit %s/%s: %w", org, ss.Subject, err)
		}
		if err := writeMigrationOffset(org, ss.Subject, "prepaid", int64(ss.Split.PrepaidBalance), true, test); err != nil {
			return res, fmt.Errorf("migrate offset prepaid %s/%s: %w", org, ss.Subject, err)
		}
	}

	// Reconcile: chain balanceOf(org) and the post-migration DB balance must BOTH
	// equal the pre-migration DB balance, exact to the cent.
	chainBal, err := onChainCents(ctx, s, acct.Address)
	if err != nil {
		return res, err
	}
	res.ChainBalanceCents = chainBal
	postDB, err := orgLedgerSpendableCents(org, test)
	if err != nil {
		return res, err
	}
	res.PostDBCents = postDB
	res.Reconciled = chainBal == dbTotal && postDB == dbTotal
	if !res.Reconciled {
		res.Reason = fmt.Sprintf("DRIFT: db=%d chain=%d postDb=%d", dbTotal, chainBal, postDB)
	}
	return res, nil
}

func onChainCents(ctx context.Context, s *Service, addr string) (int64, error) {
	wei, err := s.balanceReader.BalanceOf(ctx, addr)
	if err != nil {
		return 0, err
	}
	cents, _, err := husd.WeiToCents(wei, s.cfg.Decimals)
	return cents, err
}

// orgSubjectSplits snapshots every subject's bucket split in an org (Test
// partition). Subjects are the distinct iam-user destinations/sources in the
// ledger; the split is the SAME bucket.Compute the balance endpoint uses, so the
// migration mints exactly what the balance read reports — no drift by construction.
func orgSubjectSplits(org string, test bool) ([]subjectSplit, error) {
	nsCtx := nscontext.WithNamespace(context.Background(), org)
	db := datastore.New(nsCtx)
	root := db.NewKey("synckey", "", 1, nil)
	transs := make([]*transaction.Transaction, 0)
	if _, err := transaction.Query(db).Ancestor(root).
		Filter("Test=", test).Filter("Currency=", currency.Type("usd")).GetAll(&transs); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range transs {
		if t.DestinationKind == transaction.IAMUserKind && t.DestinationId != "" {
			seen[t.DestinationId] = true
		}
		if t.SourceKind == transaction.IAMUserKind && t.SourceId != "" {
			seen[t.SourceId] = true
		}
	}
	subjects := make([]string, 0, len(seen))
	for s := range seen {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects) // deterministic order (stable idempotency + reproducible)

	now := time.Now()
	out := make([]subjectSplit, 0, len(subjects))
	for _, subj := range subjects {
		raw, err := txutil.GetRawByCurrency(nsCtx, subj, transaction.IAMUserKind, "usd", test)
		if err != nil {
			return nil, err
		}
		split := bucket.Compute(raw, subj, now)
		if int64(split.Balance) <= 0 {
			continue // nothing to migrate for this subject
		}
		out = append(out, subjectSplit{Subject: subj, Split: split})
	}
	return out, nil
}

// writeMigrationOffset cancels a subject's LEGACY balance in ONE bucket with an
// idempotent Withdraw (deterministic id per (org,subject,bucket) → a re-run
// collapses onto one row), so after migration the balance is carried by the
// chain-sourced credit, not the legacy deposits. The prepaid offset is
// GPU-tagged so bucket.Compute draws it from prepaid ONLY (never credits); the
// credit offset is non-GPU (drawn credits-first). Non-mint (a withdraw), so it
// needs no mint authorization.
func writeMigrationOffset(org, subject, kind string, cents int64, gpu, test bool) error {
	if cents <= 0 {
		return nil
	}
	db := datastore.New(nscontext.WithNamespace(context.Background(), org))
	root := db.NewKey("synckey", "", 1, nil)
	sum := sha256.Sum256([]byte("husd-migrate-offset\x00" + org + "\x00" + subject + "\x00" + kind))
	key := db.NewKey("transaction", "migoff_"+hex.EncodeToString(sum[:16]), 0, root)

	existing := transaction.New(db)
	if err := existing.Get(key); err == nil {
		return nil // already offset — idempotent no-op
	} else if !errors.Is(err, datastore.ErrNoSuchEntity) {
		return fmt.Errorf("husdledger: check migration offset: %w", err)
	}

	tag := "migration:" + kind
	if gpu {
		tag = "gpu:migration" // draws prepaid-only in bucket.Compute
	}
	w := transaction.New(db)
	if err := w.SetKey(key); err != nil {
		return err
	}
	w.Type = transaction.Withdraw
	w.SourceId = subject
	w.SourceKind = transaction.IAMUserKind
	w.Currency = currency.Type("usd")
	w.Amount = currency.Cents(cents)
	w.Test = test
	w.Tags = tag
	w.Notes = "HUSD migration offset (" + kind + " bucket moved on-chain)"
	w.Metadata = types.Map{"source": "husd-migration", "bucket": kind}
	return w.Create()
}

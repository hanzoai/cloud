// Package husdledger wires the chain-backed credit ledger (treasury mint service
// + husdindex projector, Steps 2-3) to commerce's production per-org SQLite
// stores, and exposes the ONE mint entrypoint (Service.MintCredit) the billing
// handlers call in place of a direct DB credit write (Step 4).
//
// Decomplected from package treasury on purpose: treasury/husdindex stay free of
// the datastore (cgo) dependency so their logic is unit-tested + live-proved
// CGO-free; the datastore-touching adapters live here and are CI-tested.
package husdledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// ledgerStore is the production husdindex.Ledger: it projects one on-chain HUSD
// credit into the commerce transaction ledger as an idempotent, bucket-tagged
// Deposit — the SAME row shape (kind=transaction, destinationKind=iam-user,
// ancestor synckey) the existing GET /v1/billing/*/balance read already tallies,
// so the balance becomes chain-sourced with no read-path change.
type ledgerStore struct{}

var _ husdindex.Ledger = ledgerStore{}

// creditKey is the DETERMINISTIC storage key for a projected credit: a pure
// function of the transfer's dedup key (txHash:logIndex). Re-scanning the same
// block, or ProjectTx racing the background Sync, both land on this id, so the
// backend ON CONFLICT(id,kind,namespace) upsert collapses them onto ONE row —
// never a double credit. Parented under the synckey root so the ancestor-scoped
// balance query finds it.
func creditKey(db *datastore.Datastore, dedup string) datastore.Key {
	sum := sha256.Sum256([]byte("husd-credit\x00" + dedup))
	root := db.NewKey("synckey", "", 1, nil)
	return db.NewKey("transaction", "husd_"+hex.EncodeToString(sum[:16]), 0, root)
}

// Credit projects one on-chain HUSD Transfer into org-scoped ledger as a Deposit.
func (ledgerStore) Credit(_ context.Context, c husdindex.Credit) error {
	if c.OrgID == "" {
		return errors.New("husdledger: credit missing org")
	}
	subject := c.Subject
	if subject == "" {
		subject = c.OrgID // org-pooled billing
	}
	// Ungated BACKGROUND context (not an inbound HTTP principal), scoped to the
	// org namespace — the SAME central store + namespace-column scoping the
	// deposit write and the LLM gateway balance gate use (NOT NewNamespaced files).
	dctx := nscontext.WithNamespace(context.Background(), c.OrgID)
	db := datastore.New(dctx)
	key := creditKey(db, c.DedupKey)

	// Idempotent: if this transfer was already projected, do nothing (a true
	// no-op re-scan, not an overwrite).
	existing := transaction.New(db)
	if err := existing.Get(key); err == nil {
		return nil
	} else if !errors.Is(err, datastore.ErrNoSuchEntity) {
		return fmt.Errorf("husdledger: check existing credit: %w", err)
	}

	trans := transaction.New(db)
	if err := trans.SetKey(key); err != nil {
		return fmt.Errorf("husdledger: set credit key: %w", err)
	}
	trans.Type = transaction.Deposit
	trans.DestinationId = subject
	trans.DestinationKind = transaction.IAMUserKind
	trans.Currency = currency.Type("usd")
	trans.Amount = currency.Cents(c.AmountCents)
	trans.Tags = c.Tag
	trans.Test = c.Test
	trans.Notes = "HUSD on-chain credit " + c.TxHash
	trans.Metadata = types.Map{
		"husdTxHash":   c.TxHash,
		"husdLogIndex": c.LogIndex,
		"source":       "husd-onchain",
	}
	// The credit mirrors value ALREADY created on chain by the treasury key (a
	// real, settled mint), so it is authorized at the ledger sink. The context is
	// ungated anyway (background job) — this is explicit belt-and-suspenders.
	trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	return trans.Create()
}

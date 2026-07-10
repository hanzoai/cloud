// Package datastorestore is the production treasury.IssuanceStore: it persists
// HUSD mint issuances to the per-org SQLite ledger via models/husdissuance.
//
// It lives in a SUB-package (not package treasury) on purpose: package treasury
// stays free of the datastore/sqlite (cgo) dependency so its mint-logic unit
// tests and the live on-chain proof build CGO-free. Production wires
// datastorestore.New(db) as the treasury.IssuanceStore.
//
// Idempotency follows the proven money-safe pattern (models/idempotencykey): the
// issuance storage id is deterministic in the idempotency key, so concurrent
// duplicate creates collapse onto ONE row via the backend ON CONFLICT(id, kind,
// namespace) upsert. CreateIfAbsent reads by the EXACT kind-qualified key (never
// GetById, whose hashid decode drops the kind and breaks the Postgres lookup).
//
// LIMITATION (documented, same as models/idempotencykey): the read-then-create
// is not an atomic compare-and-swap (datastore.RunInTransaction is a no-op on
// this backend), so two callers racing a FIRST-EVER idempotency key can both see
// "absent" and both proceed to submit a chain tx. The deterministic id still
// guarantees ONE stored row, and the treasury account's sequential nonce rejects
// the second concurrent transfer, but the definitive backstop is the step-6
// reconcile job (chain balance == Σ issuances per org). Callers derive the
// idempotency key from a stable logical event (welcome:<org>, topup:<paymentId>)
// so identical-key races are pathological, not routine.
package datastorestore

import (
	"context"
	"errors"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/husdissuance"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
)

// Store implements treasury.IssuanceStore against a datastore.
type Store struct {
	db *datastore.Datastore
}

// New builds a datastore-backed issuance store. db must be namespaced to the
// org (or the system namespace for cross-org audit) by the caller.
func New(db *datastore.Datastore) *Store { return &Store{db: db} }

// compile-time: Store satisfies the interface package treasury depends on.
var _ treasury.IssuanceStore = (*Store)(nil)

func (s *Store) key(id string) datastore.Key {
	m := husdissuance.New(s.db)
	return s.db.NewKey(m.Kind(), id, 0, nil)
}

// CreateIfAbsent records iss iff its Id is absent, else returns the existing row.
func (s *Store) CreateIfAbsent(_ context.Context, iss *treasury.Issuance) (bool, *treasury.Issuance, error) {
	existing := husdissuance.New(s.db)
	if err := existing.Get(s.key(iss.Id)); err == nil {
		return false, toIssuance(existing), nil
	} else if !errors.Is(err, datastore.ErrNoSuchEntity) {
		return false, nil, err
	}

	m := husdissuance.New(s.db)
	m.SetId(iss.Id)
	fromIssuance(m, iss)
	if err := m.Create(); err != nil {
		return false, nil, err
	}
	return true, iss, nil
}

// Update writes status/txHash/mintedAt in place on the deterministic row.
func (s *Store) Update(_ context.Context, iss *treasury.Issuance) error {
	m := husdissuance.New(s.db)
	m.SetId(iss.Id)
	fromIssuance(m, iss)
	return m.Put()
}

// Get loads an issuance by id (nil,nil if absent).
func (s *Store) Get(_ context.Context, id string) (*treasury.Issuance, error) {
	m := husdissuance.New(s.db)
	if err := m.Get(s.key(id)); err != nil {
		if errors.Is(err, datastore.ErrNoSuchEntity) {
			return nil, nil
		}
		return nil, err
	}
	return toIssuance(m), nil
}

// ByTxHash resolves the issuance behind a mint tx (indexed on TxHash) so the
// indexer can tag a projected on-chain transfer with its off-chain bucket/subject.
// Returns nil (not an error) when no issuance minted that tx — an external payin.
// It makes *Store also satisfy billing/husdindex.IssuanceLookup, so the SAME
// system-namespace store is both the mint's write target and the indexer's
// read source (one issuance ledger, queryable by tx globally).
func (s *Store) ByTxHash(_ context.Context, txHash string) (*treasury.Issuance, error) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if txHash == "" {
		return nil, nil
	}
	found := make([]*husdissuance.HUSDIssuance, 0, 1)
	q := husdissuance.Query(s.db).Filter("TxHash=", txHash).Limit(1)
	if _, err := q.GetAll(&found); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return toIssuance(found[0]), nil
}

func fromIssuance(m *husdissuance.HUSDIssuance, iss *treasury.Issuance) {
	m.IdemKey = iss.IdemKey
	m.OrgID = iss.OrgID
	m.Subject = iss.Subject
	m.OrgAddress = iss.OrgAddress
	m.AmountCents = iss.AmountCents
	m.Bucket = string(iss.Bucket)
	m.Reason = iss.Reason
	m.Test = iss.Test
	m.ChainID = iss.ChainID
	m.TokenAddr = iss.TokenAddr
	m.TxHash = iss.TxHash
	m.Status = string(iss.Status)
	m.MintedAt = iss.MintedAt
}

func toIssuance(m *husdissuance.HUSDIssuance) *treasury.Issuance {
	return &treasury.Issuance{
		Id:          m.Id(),
		IdemKey:     m.IdemKey,
		OrgID:       m.OrgID,
		Subject:     m.Subject,
		OrgAddress:  m.OrgAddress,
		AmountCents: m.AmountCents,
		Bucket:      treasury.Bucket(m.Bucket),
		Reason:      m.Reason,
		Test:        m.Test,
		ChainID:     m.ChainID,
		TokenAddr:   m.TokenAddr,
		TxHash:      m.TxHash,
		Status:      treasury.IssuanceStatus(m.Status),
		CreatedAt:   m.GetCreatedAt(),
		MintedAt:    m.MintedAt,
	}
}

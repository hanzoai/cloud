package util

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

type TransactionData struct {
	Balance      currency.Cents             `json:"balance"`
	Holds        currency.Cents             `json:"holds"`
	Transactions []*transaction.Transaction `json:"transactions"`
}

type TransactionDatas struct {
	Id   string                             `json:"id"`
	Kind string                             `json:"kind"`
	Data map[currency.Type]*TransactionData `json:"data"`
}

func GetTransactions(ctx context.Context, id, kind string, test bool) (*TransactionDatas, error) {
	// ONE central commerce ledger, org-scoped by the namespace bound on ctx —
	// the SAME store the deposit write (api/billing.Deposit) and the LLM gateway
	// prepaid gate (tier/zap/usage) use. NOT NewNamespaced: that read the per-org
	// SQLite files while deposits/gate used the central store → balance $0 → 402.
	db := datastore.New(ctx)

	rootKey := db.NewKey("synckey", "", 1, nil)

	transs := make([]*transaction.Transaction, 0)
	if _, err := transaction.Query(db).Ancestor(rootKey).Filter("Test=", test).Filter("SourceKind=", kind).Filter("SourceId=", id).GetAll(&transs); err != nil {
		log.Error("ListSource Transaction Query Error '%v'", err, ctx)
		return nil, err
	}

	if _, err := transaction.Query(db).Ancestor(rootKey).Filter("Test=", test).Filter("DestinationKind=", kind).Filter("DestinationId=", id).GetAll(&transs); err != nil {
		log.Error("ListDestination Transaction Query Error '%v'", err, ctx)
		return nil, err
	}

	log.Info("GetTransactions (Test:%v) '%v/%v': %v", test, kind, id, json.Encode(transs), ctx)

	return TallyTransactions(ctx, id, kind, transs)
}

func GetTransactionsByCurrency(ctx context.Context, id, kind string, cur currency.Type, test bool) (*TransactionDatas, error) {
	// ONE central commerce ledger, org-scoped by the namespace bound on ctx — the
	// SAME store the deposit write and the LLM gateway prepaid gate use (see
	// GetTransactions). NOT NewNamespaced (per-org files) → that was the $0-balance bug.
	db := datastore.New(ctx)

	rootKey := db.NewKey("synckey", "", 1, nil)

	transs := make([]*transaction.Transaction, 0)
	if _, err := transaction.Query(db).Ancestor(rootKey).Filter("Test=", test).Filter("SourceKind=", kind).Filter("SourceId=", id).Filter("Currency=", cur).GetAll(&transs); err != nil {
		log.Error("ListSource Transaction Query Error '%v'", err, ctx)
		return nil, err
	}

	if _, err := transaction.Query(db).Ancestor(rootKey).Filter("Test=", test).Filter("DestinationKind=", kind).Filter("DestinationId=", id).Filter("Currency=", cur).GetAll(&transs); err != nil {
		log.Error("ListDestination Transaction Query Error '%v'", err, ctx)
		return nil, err
	}

	log.Info("GetTransactionsByCurrency (Test:%v) '%v/%v', '%v': %v", test, kind, id, cur, json.Encode(transs), ctx)

	return TallyTransactions(ctx, id, kind, transs)
}

// GetRawByCurrency returns the RAW source- and destination-side transactions for
// a subject in one currency — the same union GetTransactionsByCurrency tallies,
// but WITHOUT filtering expired deposits or rolling up a balance. The bucket
// split (billing/bucket) needs the raw ledger so it can count granted-but-expired
// credit separately from still-spendable credit. ONE central store, org-scoped by
// the namespace bound on ctx (identical to GetTransactionsByCurrency).
func GetRawByCurrency(ctx context.Context, id, kind string, cur currency.Type, test bool) ([]*transaction.Transaction, error) {
	db := datastore.New(ctx)
	rootKey := db.NewKey("synckey", "", 1, nil)

	transs := make([]*transaction.Transaction, 0)
	if _, err := transaction.Query(db).Ancestor(rootKey).Filter("Test=", test).Filter("SourceKind=", kind).Filter("SourceId=", id).Filter("Currency=", cur).GetAll(&transs); err != nil {
		log.Error("GetRawByCurrency source query error '%v'", err, ctx)
		return nil, err
	}
	if _, err := transaction.Query(db).Ancestor(rootKey).Filter("Test=", test).Filter("DestinationKind=", kind).Filter("DestinationId=", id).Filter("Currency=", cur).GetAll(&transs); err != nil {
		log.Error("GetRawByCurrency destination query error '%v'", err, ctx)
		return nil, err
	}
	return transs, nil
}

func TallyTransactions(ctx context.Context, id, kind string, transs []*transaction.Transaction) (*TransactionDatas, error) {
	datas := &TransactionDatas{
		Id:   id,
		Kind: kind,
		Data: make(map[currency.Type]*TransactionData),
	}

	now := time.Now()

	for _, trans := range transs {
		// NB: these transactions are query-loaded (via GetAll), so they carry no
		// datastore/db handle. Calling trans.Id() on them allocates a key against
		// a nil db (transaction's ParentFn closure) and panics — which surfaced as
		// a 500 on GET /v1/billing/balance for any ledger holding an expired
		// deposit. Log the already-loaded fields instead; never call trans.Id() here.
		if trans.SourceId == trans.DestinationId {
			log.Warn("Anomylous transaction to self detected: %v/%v type=%v amount=%v", trans.SourceKind, trans.SourceId, trans.Type, trans.Amount, ctx)
			continue
		}

		// Skip expired deposits — they no longer contribute to balance.
		if trans.Type == transaction.Deposit && !trans.ExpiresAt.IsZero() && trans.ExpiresAt.Before(now) {
			log.Info("Skipping expired deposit for %v/%v amount=%v %v (expired %v)", trans.DestinationKind, trans.DestinationId, trans.Amount, trans.Currency, trans.ExpiresAt, ctx)
			continue
		}

		if _, ok := datas.Data[trans.Currency]; !ok {
			datas.Data[trans.Currency] = &TransactionData{
				Transactions: make([]*transaction.Transaction, 0),
			}
		}

		data := datas.Data[trans.Currency]
		data.Transactions = append(data.Transactions, trans)

		switch trans.Type {
		case transaction.Deposit:
			data.Balance += trans.Amount
		case transaction.Withdraw:
			data.Balance -= trans.Amount
		case transaction.Transfer:
			if trans.DestinationId == id {
				data.Balance += trans.Amount
			} else if trans.SourceId == id {
				data.Balance -= trans.Amount
			} else {
				log.Panic("This should not be possible", ctx)
				return nil, nil
			}
		case transaction.Hold:
			data.Holds += trans.Amount
		case transaction.HoldRemoved:
		default:
			log.Panic("This should not be possible: '%v'", json.Encode(trans), ctx)
		}
	}

	for k, v := range datas.Data {
		if v.Holds < currency.Cents(0) {
			datas.Data[k].Holds = currency.Cents(0)
		}
	}

	return datas, nil
}

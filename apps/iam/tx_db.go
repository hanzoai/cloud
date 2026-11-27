package iam

import (
	"context"
	"errors"
	"strings"

	"github.com/hanzoai/orm"
)

// wrapTxSafe ensures transactional ById / IdExists / KeyExists lookups on a
// single-connection encrypted SQLite handle (cek.OpenAt + orm.AdaptSQL) read
// through the active transaction rather than acquiring a second pool connection
// from readDB (which self-deadlocks when MaxOpenConns == 1).
func wrapTxSafe(db orm.DB) orm.DB {
	if db == nil {
		return nil
	}
	return &txSafeRootDB{DB: db}
}

type txSafeRootDB struct {
	orm.DB
}

func (r *txSafeRootDB) RunInTransaction(ctx context.Context, fn func(tx orm.DB) error) error {
	return r.DB.RunInTransaction(ctx, func(tx orm.DB) error {
		return fn(&txBoundDB{DB: tx})
	})
}

func (r *txSafeRootDB) RunInTransactionWith(ctx context.Context, opts *orm.TxOptions, fn func(tx orm.DB) error) error {
	return r.DB.RunInTransactionWith(ctx, opts, func(tx orm.DB) error {
		return fn(&txBoundDB{DB: tx})
	})
}

type txBoundDB struct {
	orm.DB
}

func (t *txBoundDB) Query(kind string) orm.Query {
	return &txBoundQuery{Query: t.DB.Query(kind), tx: t.DB, kind: kind}
}

func (t *txBoundDB) RunInTransaction(_ context.Context, fn func(tx orm.DB) error) error {
	return fn(t)
}

func (t *txBoundDB) RunInTransactionWith(_ context.Context, _ *orm.TxOptions, fn func(tx orm.DB) error) error {
	return fn(t)
}

type txBoundQuery struct {
	orm.Query
	tx   orm.DB
	kind string
}

func (q *txBoundQuery) Filter(f string, v any) orm.Query {
	return &txBoundQuery{Query: q.Query.Filter(f, v), tx: q.tx, kind: q.kind}
}

func (q *txBoundQuery) Order(field string) orm.Query {
	return &txBoundQuery{Query: q.Query.Order(field), tx: q.tx, kind: q.kind}
}

func (q *txBoundQuery) Limit(limit int) orm.Query {
	return &txBoundQuery{Query: q.Query.Limit(limit), tx: q.tx, kind: q.kind}
}

func (q *txBoundQuery) Offset(offset int) orm.Query {
	return &txBoundQuery{Query: q.Query.Offset(offset), tx: q.tx, kind: q.kind}
}

func (q *txBoundQuery) Ancestor(ancestor orm.Key) orm.Query {
	return &txBoundQuery{Query: q.Query.Ancestor(ancestor), tx: q.tx, kind: q.kind}
}

func (q *txBoundQuery) KeysOnly() orm.Query {
	return &txBoundQuery{Query: q.Query.KeysOnly(), tx: q.tx, kind: q.kind}
}

func (q *txBoundQuery) ById(id string, dst any) (orm.Key, bool, error) {
	key := q.tx.NewKey(q.kind, id, 0, nil)
	if err := q.tx.Get(context.Background(), key, dst); err != nil {
		if isTxNoSuchEntity(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return key, true, nil
}

func (q *txBoundQuery) IdExists(id string) (orm.Key, bool, error) {
	var dummy map[string]any
	return q.ById(id, &dummy)
}

func (q *txBoundQuery) KeyExists(key orm.Key) (bool, error) {
	var dummy map[string]any
	if err := q.tx.Get(context.Background(), key, &dummy); err != nil {
		if isTxNoSuchEntity(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func isTxNoSuchEntity(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, orm.ErrNotFound) || strings.Contains(err.Error(), "no such entity")
}

package reward

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/locus"
)

// sqlAddrEnv names the hanzo-sql server (sql://host:port) the deployment keeps its
// shared state on. IAM's identity store is the first store that lives there, so the
// variable still carries IAM's name; the address is the server's, not IAM's.
const sqlAddrEnv = "IAM_SQL_ADDR"

// openStore opens the orm.DB the rules engine and this app's own rows live in.
//
// A deployment that has moved its shared identity store to hanzo-sql over ZAP
// (IAM_STORE_BACKEND=sql) keeps these rows there too, reached identically from every
// replica: the same call IAM's store makes (store.Connect), which writes nothing of
// IAM's into it. Everywhere else they are this app's store through the seam,
// locus.Open: a schema on hanzo-sql when CLOUD_SQL_DSN is set, else the encrypted
// SQLite file under the data dir. orm reads the engine off the pool, so the same
// records run on either. Which of the first two is decided by the variable the
// deployment already sets for its shared identity store, not by a second knob.
func openStore(dir string) (orm.DB, func() error, error) {
	if backend := env.Or(cloud.IAMStoreEnv, ""); backend == "sql" {
		db, err := store.Connect(backend, "", env.Or(sqlAddrEnv, ""))
		if err != nil {
			return nil, nil, fmt.Errorf("open hanzo-sql: %w", err)
		}
		return db, db.Close, nil
	}
	held, err := locus.Use(context.Background(), nil, namespace.System(), "reward", dir, func(raw *sql.DB) (*pool, error) {
		db, err := orm.AdaptSQL(raw)
		if err != nil {
			return nil, err
		}
		return &pool{DB: db, raw: raw}, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open reward store: %w", err)
	}
	return held.DB, held.raw.Close, nil
}

// pool is the orm.DB over a database handle, closed as the handle is: what
// locus.Use opens, so that it can move the store off its file first.
type pool struct {
	orm.DB
	raw *sql.DB
}

func (p *pool) Close() error { return p.raw.Close() }

// ready builds the indexes of this app's kinds, beside the engine's.
func ready(db orm.DB) error {
	if err := orm.Ready(db, postKind, sentKind, cursorKind); err != nil {
		return fmt.Errorf("reward: %w", err)
	}
	return nil
}

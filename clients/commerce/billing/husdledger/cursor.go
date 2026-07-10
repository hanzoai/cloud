package husdledger

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/husdcursor"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// systemNamespace holds cross-org indexer bookkeeping (cursor, and the issuance
// mint-audit ledger) — one place, not per tenant.
const systemNamespace = "system"

// cursorStore is the production husdindex.Cursor: the last fully-scanned block
// per chain, persisted as a singleton husd-cursor record in the system namespace.
type cursorStore struct{ chainID int64 }

var _ husdindex.Cursor = (*cursorStore)(nil)

func (cs *cursorStore) db() *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(context.Background(), systemNamespace))
}

// id is deterministic per chain so the cursor is a singleton (never duplicated).
func (cs *cursorStore) id() string {
	return "husd-cursor:" + itoa(cs.chainID)
}

func (cs *cursorStore) Last(context.Context) (uint64, error) {
	db := cs.db()
	c := husdcursor.New(db)
	if err := c.Get(db.NewKey(c.Kind(), cs.id(), 0, nil)); err != nil {
		if errors.Is(err, datastore.ErrNoSuchEntity) {
			return 0, nil
		}
		return 0, err
	}
	return c.LastBlock, nil
}

func (cs *cursorStore) Save(_ context.Context, block uint64) error {
	db := cs.db()
	c := husdcursor.New(db)
	c.SetId(cs.id())
	c.ChainID = cs.chainID
	c.LastBlock = block
	return c.Put()
}

// itoa formats a base-10 int64 without importing strconv at every call site.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

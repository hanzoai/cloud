package query

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/datastore/utils"
)

// defaultDB is the fallback database for queries created without an explicit db.DB.
var defaultDB db.DB

// SetDefaultDB sets the global default database used by New().
func SetDefaultDB(database db.DB) {
	defaultDB = database
}

type Id struct {
	Id_ string
}

// Query implements iface.Query using the new db package
type Query struct {
	ctx      context.Context
	dbQuery  db.Query
	kind     string
	database db.DB
	keysOnly bool

	// Query state for building
	filters     []filter
	orders      []string
	limit       int
	offset      int
	ancestor    iface.Key
	projections []string
	distinct    bool
	startCursor iface.Cursor
	endCursor   iface.Cursor
}

type filter struct {
	field string
	value interface{}
}

// New creates a new Query for the given kind.
// Uses the default database if one was set via SetDefaultDB.
func New(ctx context.Context, kind string) iface.Query {
	q := &Query{
		ctx:      ctx,
		kind:     kind,
		database: defaultDB,
	}
	if defaultDB != nil {
		q.dbQuery = defaultDB.Query(kind)
	}
	return q
}

// NewWithDB creates a new Query with an explicit database
func NewWithDB(ctx context.Context, kind string, database db.DB) iface.Query {
	q := &Query{
		ctx:      ctx,
		kind:     kind,
		database: database,
	}
	if database != nil {
		q.dbQuery = database.Query(kind)
	}
	return q
}

// clone creates a copy of the query
func (q *Query) clone() *Query {
	newQ := &Query{
		ctx:         q.ctx,
		database:    q.database,
		dbQuery:     q.dbQuery,
		kind:        q.kind,
		keysOnly:    q.keysOnly,
		filters:     append([]filter{}, q.filters...),
		orders:      append([]string{}, q.orders...),
		limit:       q.limit,
		offset:      q.offset,
		ancestor:    q.ancestor,
		projections: append([]string{}, q.projections...),
		distinct:    q.distinct,
		startCursor: q.startCursor,
		endCursor:   q.endCursor,
	}
	return newQ
}

// Wrap all query methods to implement iface.Query

func (q *Query) Ancestor(ancestor iface.Key) iface.Query {
	newQ := q.clone()
	newQ.ancestor = ancestor
	if newQ.dbQuery != nil {
		// Convert iface.Key to db.Key
		dbKey := &dbKeyWrapper{ancestor}
		newQ.dbQuery = newQ.dbQuery.Ancestor(dbKey)
	}
	return newQ
}

func (q *Query) Count() (int, error) {
	if q.dbQuery != nil {
		return q.dbQuery.Count(q.ctx)
	}
	// Without a database, we can't count
	return 0, errors.New("query: database not initialized")
}

func (q *Query) Distinct() iface.Query {
	newQ := q.clone()
	newQ.distinct = true
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Distinct()
	}
	return newQ
}

func (q *Query) EventualConsistency() iface.Query {
	// No-op in the new db package
	return q
}

func (q *Query) Filter(filterStr string, value interface{}) iface.Query {
	newQ := q.clone()
	newQ.filters = append(newQ.filters, filter{field: filterStr, value: value})
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Filter(filterStr, value)
	}
	return newQ
}

func (q *Query) KeysOnly() iface.Query {
	newQ := q.clone()
	newQ.keysOnly = true
	return newQ
}

func (q *Query) Limit(limit int) iface.Query {
	newQ := q.clone()
	newQ.limit = limit
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Limit(limit)
	}
	return newQ
}

func (q *Query) Offset(offset int) iface.Query {
	newQ := q.clone()
	newQ.offset = offset
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Offset(offset)
	}
	return newQ
}

func (q *Query) Order(fieldName string) iface.Query {
	newQ := q.clone()
	newQ.orders = append(newQ.orders, fieldName)
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Order(fieldName)
	}
	return newQ
}

func (q *Query) Project(fieldNames ...string) iface.Query {
	newQ := q.clone()
	newQ.projections = append(newQ.projections, fieldNames...)
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Project(fieldNames...)
	}
	return newQ
}

func (q *Query) Run() iface.Iterator {
	if q.dbQuery != nil {
		iter := q.dbQuery.Run(q.ctx)
		return &iteratorWrapper{iter: iter, kind: q.kind}
	}
	return &emptyIterator{}
}

func (q *Query) Start(c iface.Cursor) iface.Query {
	newQ := q.clone()
	newQ.startCursor = c
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.Start(&cursorWrapper{c})
	}
	return newQ
}

func (q *Query) End(c iface.Cursor) iface.Query {
	newQ := q.clone()
	newQ.endCursor = c
	if newQ.dbQuery != nil {
		newQ.dbQuery = newQ.dbQuery.End(&cursorWrapper{c})
	}
	return newQ
}

// Returns true if entity with key is persisted to datastore
func (q *Query) KeyExists(k iface.Key) (bool, error) {
	_, ok, err := q.KeysOnly().ByKey(k, nil)
	return ok, err
}

// Returns true if entity with key that encodes to id is persisted to datastore
func (q *Query) IdExists(id string) (iface.Key, bool, error) {
	return q.KeysOnly().ById(id, nil)
}

// Fetches first entity
func (q *Query) First(dst interface{}) (iface.Key, bool, error) {
	// Run query with iterator
	iter := q.Limit(1).Run()
	k, err := iter.Next(dst)

	// Nothing found
	if k == nil {
		return nil, false, nil
	}

	// Error trying run query
	if IgnoreFieldMismatch(err) != nil {
		return nil, false, err
	}

	// Found it
	return k, true, nil
}

// Fetches first key
func (q *Query) FirstKey() (iface.Key, bool, error) {
	return q.KeysOnly().First(nil)
}

// Fetches keys only
func (q *Query) GetKeys() ([]iface.Key, error) {
	return q.KeysOnly().GetAll(nil)
}

// Fetches entities. Dst must have type *[]S or *[]*S or *[]P, for some
// struct type S or some non- interface, non-pointer type P such that P
// or *P implements PropertyLoadSaver.
func (q *Query) GetAll(dst interface{}) ([]iface.Key, error) {
	if q.dbQuery == nil {
		return nil, errors.New("query: database not initialized")
	}

	v := reflect.ValueOf(dst)
	if dst != nil && !isPtrSlice(v) {
		return nil, fmt.Errorf("Expected dst to be a pointer to a slice or nil, got: %v", v.Kind())
	}

	dbKeys, err := q.dbQuery.GetAll(q.ctx, dst)
	err = IgnoreFieldMismatch(err)

	if err != nil {
		return nil, err
	}

	// Convert db.Keys to iface.Keys
	keys := make([]iface.Key, len(dbKeys))
	for i, k := range dbKeys {
		keys[i] = key.FromDBKey(k)
	}

	return keys, nil
}

func (q *Query) ByKey(k iface.Key, dst interface{}) (iface.Key, bool, error) {
	if k == nil {
		return nil, false, ErrInvalidKey
	}

	if dst == nil {
		dst = &Id{}
	}

	if q.database == nil {
		return nil, false, errors.New("query: database not initialized")
	}

	// Convert to db.Key
	dbKey := &dbKeyWrapper{k}

	err := q.database.Get(q.ctx, dbKey, dst)

	// Completely ignore field mismatch as we may be querying just for Id{}
	err = ReallyIgnoreFieldMismatch(err)

	// Not found
	if errors.Is(err, db.ErrNoSuchEntity) {
		return nil, false, nil
	}

	// Query failed for some reason
	if err != nil {
		log.Warn("Failed to query by key: %v", err)
		return nil, false, err
	}

	// Success
	return k, true, nil
}

// Query for entity by id
func (q *Query) ById(id string, dst interface{}) (iface.Key, bool, error) {
	// Assume encoded key
	k, err := key.Decode(q.ctx, id)

	// Try to fetch by key (can fail in rare edge cases)
	if err == nil {
		if result, ok, _ := q.ByKey(k, dst); ok {
			return result, true, nil
		}
	}

	// Try to find by filter
	filterField := ""

	// Use unique filter based on model type
	switch q.kind {
	case "store", "product", "collection", "plan", "catalog-entry":
		filterField = "Slug="
	case "variant":
		filterField = "SKU="
	case "form", "namespace", "organization", "site":
		filterField = "Name="
	case "aggregate":
		filterField = "Instance="
	case "user":
		if strings.Contains(id, "@") {
			filterField = "Email="
		} else {
			filterField = "Username="
		}
	case "referrer":
		filterField = "Code="
	case "coupon":
		return q.couponFromId(id, dst)
	case "order":
		return q.orderFromId(id, dst)
	case "affiliate", "cart", "discount", "subscription", "referral", "submission",
		"subscriber", "webhook", "meter", "credit-grant", "token", "payment",
		"transaction", "oauthtoken", "app", "wallet", "blockaddress", "blocktransaction",
		"tokentransaction", "paymentmethod", "fee", "transfer", "reversal", "partner",
		"referralprogram", "review", "return", "note", "bundle", "campaign",
		"funnel", "deploy", "adcampaign", "adconfig", "adset", "ad",
		"copy", "media", "block", "taxrates", "shippingrates",
		"billing-pricing-rule", "billing-payout", "billing-event", "billing-invoice",
		"spend-alert", "bank-transfer-instruction", "payment-intent", "meter-event",
		"credit-note", "balance-transaction", "customer-balance", "webhook-endpoint",
		"network-token", "crypto-payment-intent", "usage-watermark", "setup-intent",
		"refund", "dispute", "crypto-balance",
		// Model[T] (github.com/hanzoai/orm) kinds. All are hashid-keyed only —
		// none carry a slug/name/SKU secondary lookup — so a failed key decode
		// is a genuine not-found (HTTP 404), never an unregistered-kind 500.
		"apipermission", "applicationmethod", "auto-recharge", "campaignbudget",
		"contributor", "customergroup", "customergroupmembership", "fulfillment",
		"fulfillmentprovider", "fulfillmentset", "geozone", "inventoryitem",
		"inventorylevel", "movie", "notification", "oss-accrual", "price",
		"pricelist", "pricepreference", "pricerule", "priceset", "promotion",
		"promotionrule", "publishableapikey", "redemption", "region", "reservation",
		"role", "saleschannel", "sbom-entry", "sbom-record", "servicezone",
		"shippingoption", "shippingoptionrule", "shippingprofile", "stocklocation",
		"taxprovider", "taxrate", "taxraterule", "taxregion", "variantinventorylink",
		"tokensale", "watchlist",
		// B2B + gift cards (added for Medusa parity — hashid-keyed only).
		"company", "employee", "quote", "quote-message", "approval",
		"gift-card", "gift-card-redemption", "exchange", "idempotency-key",
		"product-option", "product-option-value", "product-category",
		"product-tag", "product-type", "return-reason", "refund-reason":
		// These kinds are always identified by hashid-encoded keys only.
		// If the key decode already failed, return not-found rather than error.
		return nil, false, nil
	default:
		return nil, false, errors.New(fmt.Sprintf("Not a valid kind for query: '%s'\nDecode error: '%s'", q.kind, err))
	}

	// Query by filter last
	return q.Filter(filterField, id).First(dst)
}

// dbKeyWrapper wraps iface.Key to implement db.Key
type dbKeyWrapper struct {
	key iface.Key
}

func (w *dbKeyWrapper) Kind() string      { return w.key.Kind() }
func (w *dbKeyWrapper) StringID() string  { return w.key.StringID() }
func (w *dbKeyWrapper) IntID() int64      { return w.key.IntID() }
func (w *dbKeyWrapper) Namespace() string { return w.key.Namespace() }
func (w *dbKeyWrapper) Incomplete() bool  { return w.key.Incomplete() }
func (w *dbKeyWrapper) Encode() string    { return w.key.Encode() }

func (w *dbKeyWrapper) Parent() db.Key {
	p := w.key.Parent()
	if p == nil {
		return nil
	}
	return &dbKeyWrapper{p}
}

func (w *dbKeyWrapper) Equal(other db.Key) bool {
	if other == nil {
		return false
	}
	return w.Kind() == other.Kind() && w.Encode() == other.Encode()
}

// iteratorWrapper wraps db.Iterator to implement iface.Iterator
type iteratorWrapper struct {
	iter db.Iterator
	kind string
}

func (w *iteratorWrapper) Next(dst interface{}) (iface.Key, error) {
	dbKey, err := w.iter.Next(dst)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, Done
		}
		return nil, err
	}
	if dbKey == nil {
		return nil, Done
	}
	return key.FromDBKey(dbKey), nil
}

func (w *iteratorWrapper) Cursor() (iface.Cursor, error) {
	cursor, err := w.iter.Cursor()
	if err != nil {
		return nil, err
	}
	return &cursorWrapperDB{cursor}, nil
}

// cursorWrapper wraps iface.Cursor to implement db.Cursor
type cursorWrapper struct {
	cursor iface.Cursor
}

func (w *cursorWrapper) String() string {
	return w.cursor.String()
}

// cursorWrapperDB wraps db.Cursor to implement iface.Cursor
type cursorWrapperDB struct {
	cursor db.Cursor
}

func (w *cursorWrapperDB) String() string {
	return w.cursor.String()
}

// emptyIterator is returned when no database is configured
type emptyIterator struct{}

func (e *emptyIterator) Next(dst interface{}) (iface.Key, error) {
	return nil, Done
}

func (e *emptyIterator) Cursor() (iface.Cursor, error) {
	return &emptyCursor{}, nil
}

type emptyCursor struct{}

func (c *emptyCursor) String() string {
	return ""
}

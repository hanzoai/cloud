package iam

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// storeIndexes are the lookups IAM makes on every request, indexed on the JSON
// fields the ORM filters and sorts by.
//
// The ORM keeps every entity in one table, `_entities (kind, data JSON)`, and
// indexes only kind. A filter on a field is `json_extract(data, '$.<field>') = ?`,
// so without these every one of them reads every row of its kind and parses its
// JSON: the users of one org, the org directory, a person by email, and the token
// a bearer credential names, which is every API request that carries one. On the
// production store (49 MB, SQLCipher) those reads cost tens to hundreds of
// milliseconds each and queue behind one another, so a directory page that asks
// one count per org waits seconds.
//
// Each expression is written exactly as the ORM writes it (hanzoai/orm
// db/sqlite.go buildWhere/buildOrderBy): SQLite uses an expression index only for
// a query whose expression is the same one. kind leads every index because every
// query the ORM builds binds it.
var storeIndexes = []struct{ name, cols string }{
	// A person or an org by its natural key, and one org's members in name order.
	{"idx_entities_owner_name", "kind, json_extract(data, '$.owner'), json_extract(data, '$.name')"},
	// The whole directory in name order, across orgs.
	{"idx_entities_name", "kind, json_extract(data, '$.name')"},
	// Sign-in by address.
	{"idx_entities_email", "kind, json_extract(data, '$.email')"},
	// A presented bearer or refresh credential, by its hash.
	{"idx_entities_access_token", "kind, json_extract(data, '$.accessTokenHash')"},
	{"idx_entities_refresh_token", "kind, json_extract(data, '$.refreshTokenHash')"},
	// Everything a user holds: their tokens, sessions and memberships.
	{"idx_entities_user", "kind, json_extract(data, '$.user')"},
}

// ensureIndexes creates any of storeIndexes the store lacks. It is idempotent and
// additive: an existing index is left as it is, and no row changes. The first
// open of a store without them builds them, once, before IAM serves.
func ensureIndexes(ctx context.Context, conn *sql.DB) (time.Duration, error) {
	start := time.Now()
	for _, ix := range storeIndexes {
		stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON _entities(%s)", ix.name, ix.cols)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return time.Since(start), fmt.Errorf("iam: index %s: %w", ix.name, err)
		}
	}
	return time.Since(start), nil
}

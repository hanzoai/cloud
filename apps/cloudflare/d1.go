package cloudflare

// d1.go — Cloudflare D1, WIRED (account-scoped; upstream path is singular
// /accounts/{id}/d1/database, our route is plural /d1/databases). List is a read;
// create/delete are mutations (authWrite → org admin); a query is a mutation too — a
// D1 query can INSERT/UPDATE/DROP, so it takes the write gate, not the read gate.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// d1Path is the upstream account-scoped D1 base (singular "database", per the CF API).
func d1Path(acct string) string { return "/accounts/" + acct + "/d1/database" }

// databasesIn pages and filters the database list. Every field is optional and
// rides the query string; each is forwarded to Cloudflare under the same name.
type databasesIn struct {
	// Page is the 1-based page of databases to return.
	Page string `json:"page"`
	// PerPage is how many databases one page holds.
	PerPage string `json:"per_page"`
	// Name filters to the database with this name.
	Name string `json:"name"`
}

// D1DatabaseList lists the D1 databases on the org's Cloudflare account. Any org
// member may read.
func (o ops) d1DatabaseList(ctx context.Context, in *databasesIn) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	q := forward(map[string]string{"page": in.Page, "per_page": in.PerPage, "name": in.Name})
	return cl.relay(ctx, http.MethodGet, d1Path(acct)+q, nil)
}

// databaseCreateIn names a new D1 database.
type databaseCreateIn struct {
	// Name is the database name to create.
	Name string `json:"name"`
}

// D1DatabaseCreate creates a D1 database on the org's Cloudflare account.
// Requires org admin.
//
// Example: {"name": "orders"}
func (o ops) d1DatabaseCreate(ctx context.Context, in *databaseCreateIn) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("database name is invalid")
	}
	return cl.relay(ctx, http.MethodPost, d1Path(acct), map[string]string{"name": name})
}

// databaseRef addresses one D1 database, from the path.
type databaseRef struct {
	// Database is the Cloudflare D1 database id or name.
	Database string `json:"database"`
}

// D1DatabaseDelete deletes a D1 database and everything stored in it. Requires
// org admin.
//
// Example: {"database": "orders"}
func (o ops) d1DatabaseDelete(ctx context.Context, in *databaseRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	db, err := seg("database", in.Database, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, d1Path(acct)+"/"+db, nil)
}

// d1Query runs a SQL statement against a database. The body ({sql, params}) is
// validated for a non-empty sql then forwarded VERBATIM (preserving params and any
// batch fields), so the full CF query shape reaches D1 without field loss.
//
// NOT a typed op: that verbatim forward is the point. A typed In decodes the body
// into a Go struct and re-encodes it, which drops every field the struct does not
// model — starting with params, which is where the query's bound values live.
func (o ops) d1Query(c *zip.Ctx) error {
	ctx := c.Context()
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return err
	}
	db, err := pathSeg(c, "database", nameRE)
	if err != nil {
		return err
	}
	var in struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	if strings.TrimSpace(in.SQL) == "" {
		return zip.ErrBadRequest("sql is required")
	}
	return cl.pass(c, http.MethodPost, d1Path(acct)+"/"+db+"/query", json.RawMessage(c.Body()))
}

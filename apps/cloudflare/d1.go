package cloudflare

// d1.go — Cloudflare D1, WIRED (account-scoped; upstream path is singular
// /accounts/{id}/d1/database, our route is plural /d1/databases). List is a read;
// create/delete are mutations (authWrite → org admin); a query is a mutation too — a
// D1 query can INSERT/UPDATE/DROP, so it takes the write gate, not the read gate.

import (
	"bytes"
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

// D1Query is a statement to run against one database: the database from the path,
// the statement and its bound values from the body — plus the body's OWN BYTES,
// kept as the caller wrote them so the forward to D1 stays verbatim.
//
// Naming two fields is not a claim that D1 takes only two. The schema is an OPEN
// object and the bytes are what travel, so a field D1 accepts that is not named
// here reaches D1 unchanged; `sql` and `params` are named because they are what a
// caller has to know.
//
// Params is `[]json.RawMessage` rather than `[]any` because the ELEMENT is what its
// schema has to say. A type that marshals itself states its own wire form and
// publishes `{}` — any JSON, which is true — while `any` falls through zip's
// schemaOf to `{"type": "object"}`, an assertion the `[42]` in this route's own
// example refutes. Nothing decodes through it either way: the bytes are what travel.
type D1Query struct {
	// Database is the D1 database to run against, from the path. The URL is the
	// addressing authority: no body field can redirect a statement to another
	// database.
	Database string `json:"-" url:"database"`
	// SQL is the statement to run. Blank (or absent) is refused before anything
	// reaches D1.
	SQL string `json:"sql" url:"-"`
	// Params are the statement's bound values, in the order its `?` placeholders
	// appear — a string, a number, a boolean or null, whatever the column takes.
	// Absent means the statement carries no placeholders; bind values here rather
	// than interpolating them into the statement.
	Params []json.RawMessage `json:"params,omitempty" url:"-"`

	// body is the caller's own bytes, unexported so it reaches no schema and no
	// caller can send one. It is what the forward carries.
	body json.RawMessage
}

// UnmarshalJSON keeps the caller's bytes and fills the two named fields, and NEVER
// refuses the body.
//
// Both halves are the wire. The bytes are what D1 receives, so decoding and
// re-encoding a Go struct would drop every field this type does not model —
// starting with `params`, where the query's bound values live. And zip decodes a
// typed op's body BEFORE the handler runs (typed.go op.invoke), so an error
// returned here is a 400 that outranks this plane's own 403 for a caller who is
// not an org admin; recording the outcome instead lets the handler re-decide in
// the order it always used — admin, then the path, then the statement.
//
// Dropping the error also reproduces what this route always accepted: it used to
// decode into a struct carrying `sql` alone, so a `params` D1 would reject reached
// D1 and D1 answered. It is apps/goja's SizedIn.Fill one plane over. What it
// cannot carry back is a body that is not JSON at all — encoding/json validates
// the whole document before it calls any Unmarshaler — so those few bytes are the
// one thing answered 400 where a non-admin used to read 403.
func (q *D1Query) UnmarshalJSON(b []byte) error {
	type body D1Query // no methods, so no recursion
	var in body
	_ = json.Unmarshal(b, &in) // the dropped error IS the wire; see above
	*q = D1Query(in)
	q.body = bytes.Clone(b)
	return nil
}

// D1Query runs one SQL statement against a D1 database. It executes on the org's
// OWN Cloudflare account and relays D1's result set. The body is checked for a
// non-empty `sql` and then forwarded VERBATIM, so every field D1 accepts reaches D1
// even though only two are named here.
//
// Requires ORG ADMIN — a statement may INSERT, UPDATE or DROP, so a query takes the
// write gate rather than the read one — and a caller who is only an org member is
// refused 403. A missing `sql` is 400; 503 if the org has never connected a
// Cloudflare token.
//
// Example: {"sql": "SELECT * FROM orders WHERE id = ?", "params": [42]}
func (o ops) d1Query(ctx context.Context, in *D1Query) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	db, err := seg("database", in.Database, nameRE)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.SQL) == "" {
		return nil, zip.ErrBadRequest("sql is required")
	}
	return cl.relay(ctx, http.MethodPost, d1Path(acct)+"/"+db+"/query", in.body)
}

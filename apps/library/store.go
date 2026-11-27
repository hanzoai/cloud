package library

// store.go — the one table every product's artifacts are listed in.
//
// ONE TABLE ON THE ONE hanzo-sql, AND TENANCY IS A COLUMN (locus.Tenants). The
// org a statement acts for comes from its context and nowhere else: a request's
// validated org, or the org a sibling stated on the plane.
//
// WITHIN AN ORG, A ROW IS ITS OWNER'S. The owner is the person the identity
// boundary vouched for (principal.SelfFrom); every write refuses a call that names
// nobody, so no row is written without one. Another member reads a row only when
// its owner shared it, and changes it never. A row whose owner is empty is
// nobody's — visible and mine both require an owner — which is the rule the
// conversation leak was closed with: the absence of an owner is not everyone.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/orm/tenant"
)

// orgKey parks the org a statement acts for. Unexported: only scoped sets it.
type orgKey struct{}

// scoped binds ctx to org for every statement made with it.
func scoped(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, orgKey{}, org)
}

// tenantOf is the resolver orm/tenant asks.
func tenantOf(ctx context.Context) (string, bool) {
	org, ok := ctx.Value(orgKey{}).(string)
	return org, ok && org != ""
}

// Item states. A reserved item waits for its bytes and is listed to nobody.
const (
	stateReady   = "ready"
	statePending = "pending"
)

var tables = []tenant.Table{{
	Name: "items",
	Fields: []tenant.Field{
		{Name: "id", Type: tenant.Text},
		{Name: "owner", Type: tenant.Text},
		// by is how a reader names the owner, kept as the boundary said it then.
		{Name: "by", Type: tenant.Text, Default: ""},
		{Name: "shared", Type: tenant.Bool, Default: false},
		{Name: "state", Type: tenant.Text, Default: stateReady},
		{Name: "kind", Type: tenant.Text},
		{Name: "source", Type: tenant.Text},
		{Name: "name", Type: tenant.Text},
		{Name: "mime", Type: tenant.Text},
		{Name: "bytes", Type: tenant.Int, Default: 0},
		{Name: "sha256", Type: tenant.Text, Default: ""},
		{Name: "width", Type: tenant.Int, Default: 0},
		{Name: "height", Type: tenant.Int, Default: 0},
		{Name: "seconds", Type: tenant.Real, Default: 0.0},
		{Name: "pages", Type: tenant.Int, Default: 0},
		{Name: "project", Type: tenant.Text, Default: ""},
		{Name: "ref", Type: tenant.Text, Default: ""},
		{Name: "origin", Type: tenant.Text, Default: ""},
		// key makes a write idempotent: unique per owner and source. An item
		// written with none takes its own id, which is unique already.
		{Name: "key", Type: tenant.Text},
		{Name: "object", Type: tenant.Text},
		{Name: "still", Type: tenant.Text, Default: ""},
		{Name: "created", Type: tenant.Int},
		{Name: "updated", Type: tenant.Int},
	},
	Key:    []string{"id"},
	Unique: [][]string{{"owner", "source", "key"}},
	Index:  [][]string{{"owner", "created"}, {"shared", "created"}, {"ref"}},
}}

// row is an item as the table holds it.
type row struct {
	ID      string  `db:"id"`
	Owner   string  `db:"owner"`
	By      string  `db:"by"`
	Shared  bool    `db:"shared"`
	State   string  `db:"state"`
	Kind    string  `db:"kind"`
	Source  string  `db:"source"`
	Name    string  `db:"name"`
	Mime    string  `db:"mime"`
	Bytes   int64   `db:"bytes"`
	Sha256  string  `db:"sha256"`
	Width   int64   `db:"width"`
	Height  int64   `db:"height"`
	Seconds float64 `db:"seconds"`
	Pages   int64   `db:"pages"`
	Project string  `db:"project"`
	Ref     string  `db:"ref"`
	Origin  string  `db:"origin"`
	Key     string  `db:"key"`
	Object  string  `db:"object"`
	Still   string  `db:"still"`
	Created int64   `db:"created"`
	Updated int64   `db:"updated"`
}

// visible is what one person may read in their org: their own ready rows and
// the rows their org's members shared. A person with no subject reads nothing.
func visible(me string) tenant.Cond {
	return tenant.And(
		tenant.Ne("owner", ""),
		tenant.Eq("state", stateReady),
		tenant.Or(tenant.Eq("owner", me), tenant.Eq("shared", true)),
	)
}

// mine is what one person may change: their own rows, never nobody's.
func mine(me string) tenant.Cond {
	return tenant.And(tenant.Ne("owner", ""), tenant.Eq("owner", me))
}

// store is the table's reads and writes, each in the org its context names.
type store struct{ db *tenant.DB }

var errNoItem = errors.New("library: no such item")

func (s store) insert(ctx context.Context, r row) error {
	return s.db.Insert(ctx, "items", r)
}

// one reads a row the caller may see, or errNoItem.
func (s store) one(ctx context.Context, me, id string) (row, error) {
	r, ok, err := s.db.Select[row]("items").Where(tenant.And(tenant.Eq("id", id), visible(me))).First(ctx)
	if err != nil {
		return row{}, err
	}
	if !ok {
		return row{}, errNoItem
	}
	return r, nil
}

// owned reads a row of the caller's own in any state, or errNoItem.
func (s store) owned(ctx context.Context, me, id string) (row, error) {
	r, ok, err := s.db.Select[row]("items").Where(tenant.And(tenant.Eq("id", id), mine(me))).First(ctx)
	if err != nil {
		return row{}, err
	}
	if !ok {
		return row{}, errNoItem
	}
	return r, nil
}

// keyed reads the caller's item made under (source, key), if any.
func (s store) keyed(ctx context.Context, me, source, key string) (row, bool, error) {
	return s.db.Select[row]("items").Where(tenant.And(mine(me), tenant.Eq("source", source), tenant.Eq("key", key))).First(ctx)
}

// filter is a list's narrowing.
type filter struct {
	me, scope, kind, source, ref, project, q string
	since, until                             int64
	after                                    *cursor
	limit                                    int
}

// cursor is where a page ended: the last row's created stamp and id.
type cursor struct {
	created int64
	id      string
}

func (c cursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.created, 10) + "." + c.id))
}

func parseCursor(s string) (*cursor, bool) {
	if s == "" {
		return nil, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	at, id, ok := strings.Cut(string(raw), ".")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" || len(id) > 64 {
		return nil, false
	}
	return &cursor{created: n, id: id}, true
}

// list reads a page newest first, and the cursor of the next page ("" at the end).
func (s store) list(ctx context.Context, f filter) ([]row, string, error) {
	conds := []tenant.Cond{visible(f.me)}
	switch f.scope {
	case "mine":
		conds = append(conds, tenant.Eq("owner", f.me))
	case "shared":
		conds = append(conds, tenant.Ne("owner", f.me))
	}
	for col, v := range map[string]string{"kind": f.kind, "source": f.source, "ref": f.ref, "project": f.project} {
		if v != "" {
			conds = append(conds, tenant.Eq(col, v))
		}
	}
	if f.q != "" {
		conds = append(conds, tenant.Like("name", "%"+tenant.Escape(f.q)+"%"))
	}
	if f.since > 0 {
		conds = append(conds, tenant.Ge("created", f.since))
	}
	if f.until > 0 {
		conds = append(conds, tenant.Lt("created", f.until))
	}
	if c := f.after; c != nil {
		conds = append(conds, tenant.Or(
			tenant.Lt("created", c.created),
			tenant.And(tenant.Eq("created", c.created), tenant.Lt("id", c.id)),
		))
	}
	rows, err := s.db.Select[row]("items").Where(tenant.And(conds...)).OrderBy("created DESC", "id DESC").Limit(f.limit + 1).All(ctx)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > f.limit {
		rows = rows[:f.limit]
		last := rows[len(rows)-1]
		next = cursor{created: last.Created, id: last.ID}.String()
	}
	return rows, next, nil
}

// change sets columns on one of the caller's own rows.
func (s store) change(ctx context.Context, me, id string, set tenant.Row) error {
	set["updated"] = now()
	n, err := s.db.Update(ctx, "items", set, tenant.And(tenant.Eq("id", id), mine(me)))
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoItem
	}
	return nil
}

// remove deletes one of the caller's own rows.
func (s store) remove(ctx context.Context, me, id string) error {
	n, err := s.db.Delete(ctx, "items", tenant.And(tenant.Eq("id", id), mine(me)))
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoItem
	}
	return nil
}

// unowned reads the rows of the org that name no owner, for the SuperAdmin's
// read-only view.
func (s store) unowned(ctx context.Context, limit int) ([]row, error) {
	return s.db.Select[row]("items").Where(tenant.Eq("owner", "")).OrderBy("created DESC", "id DESC").Limit(limit).All(ctx)
}

// stale reads, across every org, reservations whose bytes never came: the
// platform scope, entered for the sweep alone.
func (s store) stale(ctx context.Context, before int64, limit int) ([]staleRow, error) {
	p, err := s.db.Platform(ctx, "library: sweep reservations whose upload never came")
	if err != nil {
		return nil, err
	}
	return p.Select[staleRow]("items").Where(tenant.And(tenant.Eq("state", statePending), tenant.Lt("created", before))).Limit(limit).All(ctx)
}

// staleRow is a reservation the sweep removes, with the org it is in.
type staleRow struct {
	Org    string `db:"org_id"`
	ID     string `db:"id"`
	Owner  string `db:"owner"`
	Object string `db:"object"`
}

// now is the clock rows are stamped with, unix milliseconds.
func now() int64 { return time.Now().UnixMilli() }

// stamp renders a stored time for the wire.
func stamp(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

// noRows reports a read that found nothing.
func noRows(err error) bool { return errors.Is(err, sql.ErrNoRows) || errors.Is(err, errNoItem) }

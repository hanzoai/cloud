package studio

// store.go — where Studio keeps what it knows: runs and their steps, the events a
// run's stream replays, the saved things (templates, flows, documents,
// brands), chat threads, and the idempotency keys that make a retried create one
// run.
//
// ONE DATABASE ON THE ONE hanzo-sql, AND TENANCY IS A COLUMN. Every table leads
// with org_id (hanzoai/orm/tenant): it leads every key and every index, the org a
// statement acts for comes from the context and nowhere else, and on PostgreSQL
// row-level security is forced on every table with a policy that holds org_id to
// the transaction's app.org. Two guards, and the second assumes the first failed:
// a query this package wrote wrong still cannot read another org's rows, because
// the login it runs as can see none outside the org its transaction names.
//
// The org reaches a statement only through scoped (below): a request's validated
// org, or the org a run's workflow was started for. Nothing a caller sends names
// one.
//
// WITHIN AN ORG, A ROW IS ITS CREATOR'S. The creator is the person the identity
// boundary vouched for (principal.SelfFrom); a row other members may read says
// shared. A row with no creator is nobody's — it is never shown — which is the rule
// the conversation leak was closed with: absence of an owner is not everyone.

import (
	"context"
	"time"

	"github.com/hanzoai/orm/tenant"

	"github.com/hanzoai/cloud/sdk/locus"
)

// orgKey parks the org a statement acts for. Unexported, so only this package can
// set one, and only scoped does.
type orgKey struct{}

// scoped binds ctx to org for every statement made with it.
func scoped(ctx context.Context, org string) context.Context {
	return context.WithValue(ctx, orgKey{}, org)
}

// tenantOf is the resolver orm/tenant asks: the org scoped parked, or none.
func tenantOf(ctx context.Context) (string, bool) {
	org, ok := ctx.Value(orgKey{}).(string)
	return org, ok && org != ""
}

// The tables. Each leads with org_id, which orm/tenant adds and never lets a row
// move off.
var tables = []tenant.Table{
	{
		Name: "runs",
		Fields: []tenant.Field{
			{Name: "id", Type: tenant.Text},
			{Name: "creator", Type: tenant.Text},
			{Name: "shared", Type: tenant.Bool, Default: false},
			{Name: "kind", Type: tenant.Text},
			{Name: "title", Type: tenant.Text},
			{Name: "status", Type: tenant.Text},
			{Name: "template", Type: tenant.Text, Default: ""},
			{Name: "flow", Type: tenant.Text, Default: ""},
			{Name: "brand", Type: tenant.Text, Default: ""},
			{Name: "graph", Type: tenant.Text},
			{Name: "input", Type: tenant.Text},
			{Name: "tokens", Type: tenant.Text, Default: "{}"},
			{Name: "version", Type: tenant.Int},
			{Name: "parent", Type: tenant.Text, Default: ""},
			{Name: "root", Type: tenant.Text},
			{Name: "estimate", Type: tenant.Int, Default: 0},
			{Name: "charged", Type: tenant.Int, Default: 0},
			{Name: "fault", Type: tenant.Text, Default: ""},
			{Name: "payer", Type: tenant.Text, Default: "{}"},
			{Name: "attempt", Type: tenant.Int, Default: 0},
			{Name: "text", Type: tenant.Text, Default: ""},
			{Name: "created", Type: tenant.Int},
			{Name: "started", Type: tenant.Int, Default: 0},
			{Name: "finished", Type: tenant.Int, Default: 0},
		},
		Key:   []string{"id"},
		Index: [][]string{{"creator", "created"}, {"root"}, {"status"}},
	},
	{
		Name: "steps",
		Fields: []tenant.Field{
			{Name: "run", Type: tenant.Text},
			{Name: "id", Type: tenant.Text},
			{Name: "seq", Type: tenant.Int},
			{Name: "op", Type: tenant.Text},
			{Name: "title", Type: tenant.Text, Default: ""},
			{Name: "status", Type: tenant.Text},
			{Name: "attempt", Type: tenant.Int, Default: 0},
			{Name: "progress", Type: tenant.Real, Default: 0.0},
			{Name: "message", Type: tenant.Text, Default: ""},
			{Name: "items", Type: tenant.Int, Default: 0},
			{Name: "outputs", Type: tenant.Text, Default: "[]"},
			{Name: "text", Type: tenant.Text, Default: ""},
			{Name: "json", Type: tenant.Text, Default: ""},
			{Name: "cost", Type: tenant.Int, Default: 0},
			{Name: "fault", Type: tenant.Text, Default: ""},
			{Name: "started", Type: tenant.Int, Default: 0},
			{Name: "finished", Type: tenant.Int, Default: 0},
		},
		Key: []string{"run", "id"},
	},
	{
		// parts are the units a step runs as: one, or one per item of its `each`.
		// Each is durable on its own, so a fan-out killed half way keeps the half
		// that finished.
		Name: "parts",
		Fields: []tenant.Field{
			{Name: "run", Type: tenant.Text},
			{Name: "step", Type: tenant.Text},
			{Name: "item", Type: tenant.Int},
			{Name: "status", Type: tenant.Text},
			{Name: "attempt", Type: tenant.Int, Default: 0},
			{Name: "job", Type: tenant.Text, Default: ""},
			{Name: "outputs", Type: tenant.Text, Default: "[]"},
			{Name: "text", Type: tenant.Text, Default: ""},
			{Name: "json", Type: tenant.Text, Default: ""},
			{Name: "cost", Type: tenant.Int, Default: 0},
			{Name: "fault", Type: tenant.Text, Default: ""},
		},
		Key: []string{"run", "step", "item"},
	},
	{
		Name: "events",
		Fields: []tenant.Field{
			{Name: "run", Type: tenant.Text},
			{Name: "seq", Type: tenant.Int},
			{Name: "kind", Type: tenant.Text},
			{Name: "data", Type: tenant.Text},
			{Name: "at", Type: tenant.Int},
		},
		Key: []string{"run", "seq"},
	},
	{
		// docs holds the four saved things — templates, flows, documents and
		// brands — as their wire value, with the columns a list filters on.
		Name: "docs",
		Fields: []tenant.Field{
			{Name: "kind", Type: tenant.Text},
			{Name: "id", Type: tenant.Text},
			{Name: "creator", Type: tenant.Text},
			{Name: "shared", Type: tenant.Bool, Default: false},
			{Name: "sort", Type: tenant.Text, Default: ""},
			{Name: "body", Type: tenant.Text},
			{Name: "created", Type: tenant.Int},
			{Name: "updated", Type: tenant.Int},
		},
		Key:   []string{"kind", "id"},
		Index: [][]string{{"kind", "updated"}},
	},
	{
		Name: "threads",
		Fields: []tenant.Field{
			{Name: "id", Type: tenant.Text},
			{Name: "creator", Type: tenant.Text},
			{Name: "title", Type: tenant.Text},
			{Name: "created", Type: tenant.Int},
			{Name: "updated", Type: tenant.Int},
		},
		Key:   []string{"id"},
		Index: [][]string{{"creator", "updated"}},
	},
	{
		Name: "messages",
		Fields: []tenant.Field{
			{Name: "thread", Type: tenant.Text},
			{Name: "id", Type: tenant.Text},
			{Name: "role", Type: tenant.Text},
			{Name: "text", Type: tenant.Text},
			{Name: "assets", Type: tenant.Text, Default: "[]"},
			{Name: "runs", Type: tenant.Text, Default: "[]"},
			{Name: "created", Type: tenant.Int},
		},
		Key: []string{"thread", "id"},
	},
	{
		// keys are Idempotency-Key values: one person's key names one run for a day.
		Name: "keys",
		Fields: []tenant.Field{
			{Name: "creator", Type: tenant.Text},
			{Name: "key", Type: tenant.Text},
			{Name: "run", Type: tenant.Text},
			{Name: "created", Type: tenant.Int},
		},
		Key: []string{"creator", "key"},
	},
}

// The rows, as the scanner maps them (db tags name the columns).

type runRow struct {
	ID       string `db:"id"`
	Creator  string `db:"creator"`
	Shared   bool   `db:"shared"`
	Kind     string `db:"kind"`
	Title    string `db:"title"`
	Status   string `db:"status"`
	Template string `db:"template"`
	Flow     string `db:"flow"`
	Brand    string `db:"brand"`
	Graph    string `db:"graph"`
	Input    string `db:"input"`
	Tokens   string `db:"tokens"`
	Version  int64  `db:"version"`
	Parent   string `db:"parent"`
	Root     string `db:"root"`
	Estimate int64  `db:"estimate"`
	Charged  int64  `db:"charged"`
	Fault    string `db:"fault"`
	Payer    string `db:"payer"`
	Attempt  int64  `db:"attempt"`
	Text     string `db:"text"`
	Created  int64  `db:"created"`
	Started  int64  `db:"started"`
	Finished int64  `db:"finished"`
}

type stepRow struct {
	Run      string  `db:"run"`
	ID       string  `db:"id"`
	Seq      int64   `db:"seq"`
	Op       string  `db:"op"`
	Title    string  `db:"title"`
	Status   string  `db:"status"`
	Attempt  int64   `db:"attempt"`
	Progress float64 `db:"progress"`
	Message  string  `db:"message"`
	Items    int64   `db:"items"`
	Outputs  string  `db:"outputs"`
	Text     string  `db:"text"`
	JSON     string  `db:"json"`
	Cost     int64   `db:"cost"`
	Fault    string  `db:"fault"`
	Started  int64   `db:"started"`
	Finished int64   `db:"finished"`
}

type partRow struct {
	Run     string `db:"run"`
	Step    string `db:"step"`
	Item    int64  `db:"item"`
	Status  string `db:"status"`
	Attempt int64  `db:"attempt"`
	Job     string `db:"job"`
	Outputs string `db:"outputs"`
	Text    string `db:"text"`
	JSON    string `db:"json"`
	Cost    int64  `db:"cost"`
	Fault   string `db:"fault"`
}

type eventRow struct {
	Run  string `db:"run"`
	Seq  int64  `db:"seq"`
	Kind string `db:"kind"`
	Data string `db:"data"`
	At   int64  `db:"at"`
}

type docRow struct {
	Kind    string `db:"kind"`
	ID      string `db:"id"`
	Creator string `db:"creator"`
	Shared  bool   `db:"shared"`
	Sort    string `db:"sort"`
	Body    string `db:"body"`
	Created int64  `db:"created"`
	Updated int64  `db:"updated"`
}

type threadRow struct {
	ID      string `db:"id"`
	Creator string `db:"creator"`
	Title   string `db:"title"`
	Created int64  `db:"created"`
	Updated int64  `db:"updated"`
}

type messageRow struct {
	Thread  string `db:"thread"`
	ID      string `db:"id"`
	Role    string `db:"role"`
	Text    string `db:"text"`
	Assets  string `db:"assets"`
	Runs    string `db:"runs"`
	Created int64  `db:"created"`
}

type keyRow struct {
	Creator string `db:"creator"`
	Key     string `db:"key"`
	Run     string `db:"run"`
	Created int64  `db:"created"`
}

// visible is what one person may read in their org: their own rows, and the rows
// the org shares. A row with no creator matches neither half unless shared, and a
// person with no subject reads only what is shared.
func visible(me string) tenant.Cond {
	if me == "" {
		return tenant.Eq("shared", true)
	}
	return tenant.Or(tenant.Eq("creator", me), tenant.Eq("shared", true))
}

// mine is what one person may change: their own rows, never nobody's.
func mine(me string) tenant.Cond {
	return tenant.And(tenant.Ne("creator", ""), tenant.Eq("creator", me))
}

// openDB opens the store on the one shared opener: PostgreSQL on hanzo-sql when the
// deployment names it, else the encrypted SQLite file a laptop and the tests use.
func openDB(ctx context.Context, dataDir string) (*tenant.DB, func() error, error) {
	return locus.Tenants(ctx, locus.Tenancy{Name: "studio", DataDir: dataDir, Tables: tables, Tenant: tenantOf})
}

// now is the clock every row is stamped with, in unix nanoseconds.
func now() int64 { return time.Now().UnixNano() }

// stamp renders a stored time for the wire; zero is absent.
func stamp(ns int64) string {
	if ns == 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}

package base

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// TestMain hands cek a throwaway master key. The store opens through cek, which
// refuses to open a data plane unencrypted, so these tests run the REAL
// encrypted path — the same code a deployment runs, not a way around it.
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}

// store builds the subsystem over a fresh directory, exactly as Mount does.
func store(t *testing.T) *cloud.Service[state] {
	t.Helper()
	st, err := build(cloud.Base{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return &cloud.Service[state]{State: st}
}

// as returns a context carrying a validated caller in org, the way the identity
// boundary hands one to a typed handler.
func as(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u@" + org})
}

// status is the HTTP status an error carries. Asserting on it is asserting on
// the contract: 400 and 404 are what a caller sees and branches on.
func status(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// A collection name is a storage key the caller picks, so the pattern is the
// whole of what stands between the store and a name it has no business holding.
func TestCollectionName(t *testing.T) {
	s, ctx := store(t), as("acme")
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"users", true},
		{"a", true},
		{"user_events_2", true},
		{strings.Repeat("c", 64), true},

		{"", false},
		{"Users", false},                     // upper case
		{"1users", false},                    // leading digit
		{"users-2", false},                   // hyphen
		{"users.json", false},                // dot
		{"../../etc/passwd", false},          // traversal
		{"users; DROP TABLE records", false}, // injection
		{"users records", false},             // space
		{strings.Repeat("c", 65), false},     // one over the limit
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := create(s)(ctx, &CreateIn{Collection: tc.name, Doc: json.RawMessage(`{}`)})
			switch {
			case tc.ok && err != nil:
				t.Fatalf("create(%q) = %v, want it accepted", tc.name, err)
			case !tc.ok && status(err) != 400:
				t.Fatalf("create(%q) = %v, want 400", tc.name, err)
			}
		})
	}
}

// A record is whatever JSON the caller sends. "Whatever JSON" still has to BE
// JSON, and it has to fit.
func TestDoc(t *testing.T) {
	s, ctx := store(t), as("acme")
	for _, tc := range []struct {
		name string
		doc  json.RawMessage
		ok   bool
	}{
		{"object", json.RawMessage(`{"a":1,"b":[2,3]}`), true},
		{"array", json.RawMessage(`[1,2,3]`), true},
		{"string", json.RawMessage(`"hello"`), true},
		{"number", json.RawMessage(`42`), true},
		{"null", json.RawMessage(`null`), true},
		{"at the limit", json.RawMessage(`"` + strings.Repeat("x", MaxDoc-2) + `"`), true},

		{"absent", nil, false},
		{"empty", json.RawMessage(``), false},
		{"truncated", json.RawMessage(`{"a":`), false},
		{"two values", json.RawMessage(`{} {}`), false},
		{"not json at all", json.RawMessage(`hello`), false},
		{"one byte over", json.RawMessage(`"` + strings.Repeat("x", MaxDoc) + `"`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := create(s)(ctx, &CreateIn{Collection: "docs", Doc: tc.doc})
			switch {
			case tc.ok && err != nil:
				t.Fatalf("create = %v, want it accepted", err)
			case !tc.ok && status(err) != 400:
				t.Fatalf("create = %v, want 400", err)
			}
		})
	}
}

// The whole life of a record, in the order a caller lives it.
func TestLifecycle(t *testing.T) {
	s, ctx := store(t), as("acme")

	made, err := create(s)(ctx, &CreateIn{Collection: "notes", Doc: json.RawMessage(`{"t":"one"}`)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if made.ID == "" {
		t.Fatal("create returned no id; ids are the server's to mint")
	}

	got, err := get(s)(ctx, &GetIn{Collection: "notes", ID: made.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Doc) != `{"t":"one"}` {
		t.Fatalf("get doc = %s, want the bytes that went in", got.Doc)
	}
	if !got.Created.Equal(made.Created) || !got.Updated.Equal(made.Updated) {
		t.Fatalf("create said %v/%v, get says %v/%v; a read must agree with the write",
			made.Created, made.Updated, got.Created, got.Updated)
	}

	up, err := update(s)(ctx, &UpdateIn{Collection: "notes", ID: made.ID, Doc: json.RawMessage(`{"t":"two"}`)})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if string(up.Doc) != `{"t":"two"}` {
		t.Fatalf("update doc = %s, want the replacement", up.Doc)
	}
	if !up.Created.Equal(made.Created) {
		t.Fatalf("update moved Created from %v to %v; a replacement is not a new record", made.Created, up.Created)
	}

	del, err := remove(s)(ctx, &DeleteIn{Collection: "notes", ID: made.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if del.ID != made.ID || del.Collection != "notes" {
		t.Fatalf("delete echoed %+v, want the record it removed", del)
	}

	// Everything that addresses a record that is not there says so, and says the
	// same thing: a caller retrying a delete is not a caller with a broken store.
	if _, err := get(s)(ctx, &GetIn{Collection: "notes", ID: made.ID}); status(err) != 404 {
		t.Fatalf("get after delete = %v, want 404", err)
	}
	if _, err := update(s)(ctx, &UpdateIn{Collection: "notes", ID: made.ID, Doc: json.RawMessage(`{}`)}); status(err) != 404 {
		t.Fatalf("update after delete = %v, want 404", err)
	}
	if _, err := remove(s)(ctx, &DeleteIn{Collection: "notes", ID: made.ID}); status(err) != 404 {
		t.Fatalf("delete after delete = %v, want 404", err)
	}
}

// Paging: the page bounds are the store's, not the caller's, and consecutive
// pages tile the collection with no gaps and no repeats. That tiling is the
// whole contract — the order is total and stable, but records written in the
// same millisecond are not promised to come back in the order they went in, so
// the expectations here are read off the full listing rather than off the
// sequence of writes.
func TestListPaging(t *testing.T) {
	s, ctx := store(t), as("acme")
	const total = 7
	for i := range total {
		if _, err := create(s)(ctx, &CreateIn{Collection: "notes", Doc: json.RawMessage(`{"i":1}`)}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	all, err := list(s)(ctx, &ListIn{Collection: "notes"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all.Records) != total {
		t.Fatalf("the full listing has %d records, want %d", len(all.Records), total)
	}
	ids := make([]string, total)
	for i, r := range all.Records {
		ids[i] = r.ID
	}

	for _, tc := range []struct {
		name          string
		limit, offset int
		want          []string
	}{
		{"no limit takes the default", 0, 0, ids},
		{"a page", 3, 0, ids[:3]},
		{"the next page", 3, 3, ids[3:6]},
		{"the short last page", 3, 6, ids[6:]},
		{"past the end", 3, 99, nil},
		{"over the ceiling clamps, it does not fail", maxLimit + 1, 0, ids},
		{"a negative offset is the beginning", 2, -5, ids[:2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := list(s)(ctx, &ListIn{Collection: "notes", Limit: tc.limit, Offset: tc.offset})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(out.Records) != len(tc.want) {
				t.Fatalf("list returned %d records, want %d", len(out.Records), len(tc.want))
			}
			for i, r := range out.Records {
				if r.ID != tc.want[i] {
					t.Fatalf("record %d = %s, want %s; pages must tile the full listing", i, r.ID, tc.want[i])
				}
			}
		})
	}

	// A collection nobody has written to is empty, not missing: a collection IS
	// its records, so there is nothing for a 404 to be about.
	empty, err := list(s)(ctx, &ListIn{Collection: "nothing_here"})
	if err != nil || len(empty.Records) != 0 {
		t.Fatalf("list of an unwritten collection = %v, %v; want an empty list", empty, err)
	}
	if _, err := list(s)(ctx, &ListIn{Collection: "Not A Name"}); status(err) != 400 {
		t.Fatalf("list of a malformed name = %v, want 400", err)
	}
}

// Collections are derived from the records, so they appear and disappear with
// them and never need creating.
func TestCollections(t *testing.T) {
	s, ctx := store(t), as("acme")

	out, err := collections(s)(ctx, &CollectionsIn{})
	if err != nil || len(out.Collections) != 0 {
		t.Fatalf("collections on a fresh store = %v, %v; want none", out, err)
	}

	var last string
	for _, c := range []string{"notes", "notes", "notes", "users"} {
		r, err := create(s)(ctx, &CreateIn{Collection: c, Doc: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("create in %q: %v", c, err)
		}
		if c == "users" {
			last = r.ID
		}
	}

	out, err = collections(s)(ctx, &CollectionsIn{})
	if err != nil {
		t.Fatalf("collections: %v", err)
	}
	want := []Collection{{Name: "notes", Records: 3}, {Name: "users", Records: 1}}
	if len(out.Collections) != len(want) {
		t.Fatalf("collections = %+v, want %+v", out.Collections, want)
	}
	for i, c := range out.Collections {
		if c != want[i] {
			t.Fatalf("collection %d = %+v, want %+v", i, c, want[i])
		}
	}

	if _, err := remove(s)(ctx, &DeleteIn{Collection: "users", ID: last}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	out, _ = collections(s)(ctx, &CollectionsIn{})
	if len(out.Collections) != 1 || out.Collections[0].Name != "notes" {
		t.Fatalf("collections after emptying users = %+v, want only notes", out.Collections)
	}
}

// The tenant boundary. Two orgs share one file, so every statement filters on
// the org — this is the test that says so out loud. It walks every op, because
// a boundary that holds on five of six reads is not a boundary.
func TestTenantIsolation(t *testing.T) {
	s := store(t)
	acme, other, local := as("acme"), as("other"), context.Background()

	mine, err := create(s)(acme, &CreateIn{Collection: "notes", Doc: json.RawMessage(`{"secret":true}`)})
	if err != nil {
		t.Fatalf("create as acme: %v", err)
	}
	// Same collection name, different org: the names collide and the records
	// must not.
	theirs, err := create(s)(other, &CreateIn{Collection: "notes", Doc: json.RawMessage(`{"secret":false}`)})
	if err != nil {
		t.Fatalf("create as other: %v", err)
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		id   string
	}{
		{"another org", other, mine.ID},
		{"the local namespace", local, mine.ID},
		{"acme reaching for other", acme, theirs.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := get(s)(tc.ctx, &GetIn{Collection: "notes", ID: tc.id}); status(err) != 404 {
				t.Fatalf("get = %v, want 404; a record outside the caller's org does not exist to it", err)
			}
			if _, err := update(s)(tc.ctx, &UpdateIn{Collection: "notes", ID: tc.id, Doc: json.RawMessage(`{"owned":true}`)}); status(err) != 404 {
				t.Fatalf("update = %v, want 404", err)
			}
			if _, err := remove(s)(tc.ctx, &DeleteIn{Collection: "notes", ID: tc.id}); status(err) != 404 {
				t.Fatalf("delete = %v, want 404", err)
			}
		})
	}

	// A list shows the caller its own records and only its own.
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"acme", acme, mine.ID},
		{"other", other, theirs.ID},
	} {
		t.Run("list as "+tc.name, func(t *testing.T) {
			out, err := list(s)(tc.ctx, &ListIn{Collection: "notes"})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(out.Records) != 1 || out.Records[0].ID != tc.want {
				t.Fatalf("list = %+v, want exactly %s", out.Records, tc.want)
			}
		})
	}

	// The local namespace is a namespace like any other: empty here, and its own
	// records are invisible to the orgs.
	if out, _ := list(s)(local, &ListIn{Collection: "notes"}); len(out.Records) != 0 {
		t.Fatalf("the local namespace sees %+v; it must see nothing an org wrote", out.Records)
	}
	if _, err := create(s)(local, &CreateIn{Collection: "notes", Doc: json.RawMessage(`{"local":true}`)}); err != nil {
		t.Fatalf("create locally: %v", err)
	}
	if out, _ := list(s)(acme, &ListIn{Collection: "notes"}); len(out.Records) != 1 {
		t.Fatalf("acme sees %d records after a local write; it must still see only its own", len(out.Records))
	}

	// A caller with an org but NO validated user is the forge the boundary is
	// there for: it lands in the local namespace, never in the org it named.
	forged := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	if _, err := get(s)(forged, &GetIn{Collection: "notes", ID: mine.ID}); status(err) != 404 {
		t.Fatalf("get with an unvalidated X-Org-Id = %v, want 404", err)
	}
}

// A subsystem that cannot reach its data must not come up pretending it can.
func TestBuildFailsClosed(t *testing.T) {
	if _, err := build(cloud.Base{}); err == nil {
		t.Fatal("build with no DataDir succeeded; it must fail the mount")
	}
}

// Shutdown runs on a path where it may be called twice, or never have opened
// anything. Neither is an error.
func TestShutdownIsIdempotent(t *testing.T) {
	store(t)
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

// The routes are the contract the lead wires and every projection reads. Pinning
// them here means a typo in a path or an operation id fails in this package
// rather than in the composed document.
func TestRoutes(t *testing.T) {
	app := zip.New(zip.Config{AppName: "base-test"})
	routes(app, store(t))
	d := app.Declaration()

	want := map[string]string{
		"GET /v1/base/collections":                    "base_collections",
		"GET /v1/base/collections/:collection":        "base_list",
		"POST /v1/base/collections/:collection":       "base_create",
		"GET /v1/base/collections/:collection/:id":    "base_get",
		"PUT /v1/base/collections/:collection/:id":    "base_update",
		"DELETE /v1/base/collections/:collection/:id": "base_delete",
	}
	got := map[string]bool{}
	for _, r := range d.Routes {
		got[r.Method+" "+r.Pattern] = true
	}
	for route := range want {
		if !got[route] {
			t.Errorf("route %q is not registered; declared routes are %+v", route, d.Routes)
		}
	}
	ops := map[string]bool{}
	for _, op := range d.Ops {
		ops[op] = true
	}
	for _, id := range want {
		if !ops[id] {
			t.Errorf("operation %q is not registered; declared ops are %v", id, d.Ops)
		}
	}
	if len(d.Ops) != len(want) {
		t.Errorf("declared %d ops, want %d: every route here must be a TYPED op", len(d.Ops), len(want))
	}
}

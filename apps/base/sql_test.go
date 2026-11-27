// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/goja"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/filesystem"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func newApp() *zip.App { return zip.New(zip.Config{Logger: luxlog.New("test")}) }

// mountIn boots the subsystem over an existing data directory, so a test can
// stop a deployment and start the next one on the same volume.
func mountIn(t *testing.T, dataDir string) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_BASE_EMBED", "1")
	t.Setenv("BASE_API_PREFIX", apiPrefix)
	t.Setenv("CLOUD_DATA_DIR", dataDir)
	app := newApp()
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// seedWorld fills an org's Base with every kind of field a record can hold, an
// auth collection with a member who can sign in, and a few records, through the
// engine's own Go API, as a customer's Base is filled.
func seedWorld(t *testing.T, org, tag string) {
	t.Helper()
	bapp, err := mounted.pool.appFor(org)
	if err != nil {
		t.Fatalf("appFor(%s): %v", org, err)
	}
	public := ""
	things := core.NewBaseCollection("things")
	things.Fields.Add(
		&core.TextField{Name: "title", Required: true},
		&core.NumberField{Name: "score"},
		&core.BoolField{Name: "done"},
		&core.EmailField{Name: "mail"},
		&core.URLField{Name: "link"},
		&core.DateField{Name: "due"},
		&core.JSONField{Name: "doc"},
		&core.SelectField{Name: "kind", Values: []string{"a", "b"}, MaxSelect: 1},
		&core.AutodateField{Name: "touched", OnCreate: true, OnUpdate: true},
	)
	things.ListRule, things.ViewRule, things.CreateRule = &public, &public, &public
	things.AddIndex("idx_things_title_"+tag, true, "title", "")
	if err := bapp.Save(things); err != nil {
		t.Fatalf("save things(%s): %v", org, err)
	}
	members := core.NewAuthCollection("members")
	members.ListRule, members.ViewRule = &public, &public
	if err := bapp.Save(members); err != nil {
		t.Fatalf("save members(%s): %v", org, err)
	}

	for i, row := range []map[string]any{
		{"title": tag + " one", "score": 1.5, "done": true, "mail": "a@example.com", "link": "https://example.com/a", "due": "2026-01-02 03:04:05.000Z", "doc": map[string]any{"n": 1, "xs": []int{1, 2}}, "kind": "a"},
		{"title": tag + " two", "score": 0, "done": false, "kind": "b"},
		{"title": tag + " three ünï — ☃", "score": 1e21, "doc": []any{"x", nil, 2.5}},
	} {
		rec := core.NewRecord(things)
		for k, v := range row {
			rec.Set(k, v)
		}
		if err := bapp.Save(rec); err != nil {
			t.Fatalf("save record %d(%s): %v", i, org, err)
		}
	}
	m := core.NewRecord(members)
	m.SetEmail("who@" + tag + ".example.com")
	m.SetVerified(true)
	if err := bapp.Save(m); err != nil {
		t.Fatalf("save member(%s): %v", org, err)
	}
}

// snapshot reads everything an org's Base holds that a customer would see: every
// collection as Base exports it and every record of every collection, in a stable
// order, with the fields that are the engine's own time stamps left in.
func snapshot(t *testing.T, org string) map[string]any {
	t.Helper()
	bapp, err := mounted.pool.appFor(org)
	if err != nil {
		t.Fatalf("appFor(%s): %v", org, err)
	}
	cols, err := bapp.FindAllCollections()
	if err != nil {
		t.Fatalf("collections(%s): %v", org, err)
	}
	out := map[string]any{}
	for _, c := range cols {
		raw, _ := json.Marshal(c)
		var def map[string]any
		_ = json.Unmarshal(raw, &def)
		entry := map[string]any{"def": def}
		if c.IsView() {
			out[c.Name] = entry
			continue
		}
		recs, err := bapp.FindAllRecords(c.Id)
		if err != nil {
			t.Fatalf("records of %s(%s): %v", c.Name, org, err)
		}
		rows := make([]any, 0, len(recs))
		for _, r := range recs {
			raw, _ := json.Marshal(r)
			var row map[string]any
			_ = json.Unmarshal(raw, &row)
			// The hash of a password is the record's own and must arrive whole:
			// it is what signing in checks.
			row["_password_hash"] = r.GetString("password")
			row["_token_key"] = r.GetString("tokenKey")
			rows = append(rows, row)
		}
		slices.SortFunc(rows, func(a, b any) int {
			ai, bi := a.(map[string]any)["id"].(string), b.(map[string]any)["id"].(string)
			switch {
			case ai < bi:
				return -1
			case ai > bi:
				return 1
			}
			return 0
		})
		entry["rows"] = rows
		out[c.Name] = entry
	}
	return out
}

// signIn is what a member's session is on a Base: Hanzo IAM does the password, and
// the Base finds the member by address and signs it a token, which only the Base's
// own token secret (a row of its settings) can verify. The password argument names
// the case a caller that is not the member is refused.
func signIn(t *testing.T, org, tag, password string) (string, bool) {
	t.Helper()
	bapp, err := mounted.pool.appFor(org)
	if err != nil {
		t.Fatalf("appFor(%s): %v", org, err)
	}
	rec, err := bapp.FindAuthRecordByEmail("members", "who@"+tag+".example.com")
	if err != nil || password != "correct horse battery" {
		return "", false
	}
	tok, err := rec.NewAuthToken()
	if err != nil {
		t.Fatalf("sign a token(%s): %v", org, err)
	}
	return tok, true
}

// holds reports whether the Base of org accepts a token it signed earlier.
func holds(t *testing.T, org, token string) bool {
	t.Helper()
	bapp, err := mounted.pool.appFor(org)
	if err != nil {
		t.Fatalf("appFor(%s): %v", org, err)
	}
	_, err = bapp.FindAuthRecordByToken(token, core.TokenTypeAuth)
	return err == nil
}

// TestBaseMovesToHanzoSQL: Bases made as files, a deployment started over them with
// the policy set, and every org's collections, records and auth read back
// identically from the server; two orgs stay apart; signing in still works; and the
// next write lands on the server and not in the file.
func TestBaseMovesToHanzoSQL(t *testing.T) {
	dataDir := t.TempDir()

	// The deployment before: files.
	t.Setenv(locus.Env, "")
	mountIn(t, dataDir)
	orgs := map[string]string{"acme": "acme", "globex": "globex"}
	before := map[string]map[string]any{}
	tokens := map[string]string{}
	for org, tag := range orgs {
		seedWorld(t, org, tag)
		tok, ok := signIn(t, org, tag, "correct horse battery")
		if !ok {
			t.Fatalf("sign in on the file(%s)", org)
		}
		tokens[org] = tok
		before[org] = snapshot(t, org)
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	for org := range orgs {
		if _, err := os.Stat(filepath.Join(dataDir, "base", goja.TenantSegment(org), dataFile)); err != nil {
			t.Fatalf("the Base of %s has no file to move: %v", org, err)
		}
	}

	// The deployment after: hanzo-sql, moving lazily.
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	app := mountIn(t, dataDir)
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	for org, tag := range orgs {
		after := snapshot(t, org)
		if !reflect.DeepEqual(before[org], after) {
			b, _ := json.MarshalIndent(before[org], "", " ")
			a, _ := json.MarshalIndent(after, "", " ")
			t.Fatalf("%s did not arrive as it left\nbefore: %s\nafter: %s", org, b, a)
		}
		if _, ok := signIn(t, org, tag, "correct horse battery"); !ok {
			t.Fatalf("sign in on the server(%s)", org)
		}
		if _, ok := signIn(t, org, tag, "wrong"); ok {
			t.Fatalf("a wrong password signed in on the server(%s)", org)
		}
		if !holds(t, org, tokens[org]) {
			t.Fatalf("the token %s signed before the move is refused after it", org)
		}
		other := "globex"
		if org == other {
			other = "acme"
		}
		if holds(t, other, tokens[org]) {
			t.Fatalf("%s's token is accepted by %s", org, other)
		}
	}

	// Moved means moved: both databases of both orgs are in the registry.
	for _, sub := range []string{dataSub, auxSub} {
		stored, err := locus.Stores(sub)
		if err != nil {
			t.Fatalf("stores(%s): %v", sub, err)
		}
		if len(stored) != 3 {
			t.Fatalf("%s: %d stores on the server, want the two orgs and the platform's own: %v", sub, len(stored), stored)
		}
	}

	// A write now is the server's: it is not in the file.
	ns, _ := namespaceOf("acme")
	if place, _ := locus.WhereAt(ns, dataSub, filepath.Join(dataDir, "base", goja.TenantSegment("acme"), dataFile)); place != locus.OnServer {
		t.Fatalf("acme's Base is not on the server after its move")
	}
	if code, body := req(t, app, http.MethodPost, "/v1/base/collections/things/records", "acme",
		map[string]any{"title": "written after the move", "score": 7}); code != http.StatusOK {
		t.Fatalf("write after the move: %d %s", code, body)
	}
	code, body := req(t, app, http.MethodGet, "/v1/base/collections/things/records?filter="+url.QueryEscape("title~'after the move'"), "acme", nil)
	if code != http.StatusOK || !json.Valid(body) {
		t.Fatalf("read after the move: %d %s", code, body)
	}
	var page struct {
		Items []struct{ Title string } `json:"items"`
	}
	_ = json.Unmarshal(body, &page)
	if len(page.Items) != 1 {
		t.Fatalf("the write after the move reads back as %d records: %s", len(page.Items), body)
	}
	// And the other org does not see it.
	code, body = req(t, app, http.MethodGet, "/v1/base/collections/things/records?filter="+url.QueryEscape("title~'after the move'"), "globex", nil)
	_ = json.Unmarshal(body, &page)
	if code != http.StatusOK || len(page.Items) != 0 {
		t.Fatalf("globex reads acme's write: %d %s", code, body)
	}
}

// TestABaseOnEitherEngine: the same Base, made and read, on its files and on
// hanzo-sql; where it is born decides whether the volume holds a file; two orgs are
// two Bases whose records and tokens never meet.
func TestABaseOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dataDir := t.TempDir()
		mountIn(t, dataDir)
		t.Cleanup(func() { _ = Shutdown(context.Background()) })
		seedWorld(t, "initech", "initech")
		seedWorld(t, "hooli", "hooli")

		for _, org := range []string{"initech", "hooli"} {
			rows := snapshot(t, org)["things"].(map[string]any)["rows"].([]any)
			if len(rows) != 3 {
				t.Fatalf("%s holds %d things, want 3", org, len(rows))
			}
			for _, r := range rows {
				if title := r.(map[string]any)["title"].(string); !strings.HasPrefix(title, org) {
					t.Fatalf("%s holds %q, a record of the other org", org, title)
				}
			}
		}
		tok, ok := signIn(t, "initech", "initech", "correct horse battery")
		if !ok || holds(t, "hooli", tok) {
			t.Fatalf("a token of initech is held by hooli, or initech cannot sign in (%v)", ok)
		}

		_, err := os.Stat(filepath.Join(dataDir, "base", goja.TenantSegment("initech"), dataFile))
		onServer := locus.DSN() != ""
		if onServer == (err == nil) {
			t.Fatalf("on the server=%v and the file's stat says %v", onServer, err)
		}
	})
}

// TestSweepMovesTheBasesNobodyOpened: the sweep policy moves a file nobody has
// asked for, and leaves it served from the server.
func TestSweepMovesTheBasesNobodyOpened(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(locus.Env, "")
	mountIn(t, dataDir)
	seedWorld(t, "umbrella", "umbrella")
	want := snapshot(t, "umbrella")
	if err := Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "sweep")
	mountIn(t, dataDir)
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	mounted.pool.sweep(context.Background())

	ns, _ := namespaceOf("umbrella")
	place, err := locus.WhereAt(ns, dataSub, filepath.Join(dataDir, "base", goja.TenantSegment("umbrella"), dataFile))
	if err != nil || place != locus.OnServer {
		t.Fatalf("the sweep left umbrella on its file: %v %v", place, err)
	}
	if got := snapshot(t, "umbrella"); !reflect.DeepEqual(want, got) {
		t.Fatalf("umbrella did not survive the sweep")
	}
}

// TestAMovingBaseHoldsOnlyItsOwnOrg: while one org's Base is being opened (moved),
// its requests wait and are then told to try again, and another org's are served.
func TestAMovingBaseHoldsOnlyItsOwnOrg(t *testing.T) {
	t.Setenv(locus.Env, "")
	mountIn(t, t.TempDir())
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	p := mounted.pool

	o, mine := p.begin(goja.TenantSegment("slow"))
	if !mine {
		t.Fatal("could not claim the open of slow")
	}
	old := openWait
	openWait = 200 * time.Millisecond
	t.Cleanup(func() { openWait = old })

	if _, err := p.appFor("quick"); err != nil {
		t.Fatalf("another org waited behind a move: %v", err)
	}
	if _, err := p.appFor("slow"); !errors.Is(err, errOpening) {
		t.Fatalf("the moving org's request got %v, want to be told to try again", err)
	}
	p.end(goja.TenantSegment("slow"), o, nil, errOpening)
	if _, err := p.appFor("slow"); err != nil {
		t.Fatalf("the org is not served once its open ended: %v", err)
	}
}

// TestFilesGoToTheBucketAndStayThere: what is on the volume is copied into the
// filesystem the host hands Base, with its name and size, once; and the directory
// is left as it was.
func TestFilesGoToTheBucketAndStayThere(t *testing.T) {
	dir := t.TempDir()
	local, err := filesystem.NewLocal(filepath.Join(dir, storageDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Upload([]byte("hello, base"), "col/rec/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := local.Upload([]byte("%PDF-1.4 not really"), "col/rec2/b.pdf"); err != nil {
		t.Fatal(err)
	}
	_ = local.Close()

	bucket := t.TempDir()
	open := func() (*filesystem.System, error) { return filesystem.NewLocal(bucket) }
	if err := carry(context.Background(), dir, open); err != nil {
		t.Fatalf("carry: %v", err)
	}
	dst, _ := open()
	defer dst.Close()
	for key, size := range map[string]int64{"col/rec/a.txt": 11, "col/rec2/b.pdf": 19} {
		attrs, err := dst.Attributes(key)
		if err != nil || attrs.Size != size {
			t.Fatalf("%s in the bucket: %v %v", key, attrs, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, storageDir, "col/rec/a.txt")); err != nil {
		t.Fatalf("the volume's copy was removed: %v", err)
	}
	// Once: a file the bucket holds that the volume does not is not undone.
	if err := dst.Delete("col/rec/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := carry(context.Background(), dir, open); err != nil {
		t.Fatal(err)
	}
	if ok, _ := dst.Exists("col/rec/a.txt"); ok {
		t.Fatalf("a second carry copied again")
	}
}

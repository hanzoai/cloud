package library

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/object"

	"github.com/hanzoai/orm/pgtest"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// fake is the bucket, in memory.
type fake struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fake) put(_ context.Context, key, _ string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = append([]byte(nil), data...)
	return nil
}

func (f *fake) sign(_ context.Context, key, kind, name string, attach bool) (string, error) {
	if attach {
		return "https://s3.test/" + key + "?attach=" + name, nil
	}
	return "https://s3.test/" + key + "?type=" + inline(kind), nil
}

func (f *fake) upload(_ context.Context, key, kind string) (string, []client.Field, error) {
	return "https://s3.test/" + key + "?put", []client.Field{{Name: "Content-Type", Value: kind}}, nil
}

func (f *fake) size(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objs[key]
	if !ok {
		return 0, errors.New("NoSuchKey")
	}
	return int64(len(b)), nil
}

func (f *fake) remove(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.objs, k)
	}
	return nil
}

func (f *fake) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objs[key]
	return ok
}

// mount serves the Library on a bare app over a fresh store and a fake bucket.
func mount(t *testing.T) (*zip.App, *service, *fake) {
	t.Helper()
	return mountOn(t, "")
}

// mountOn is mount on the store an admin DSN names: "" is SQLite.
func mountOn(t *testing.T, admin string) (*zip.App, *service, *fake) {
	t.Helper()
	t.Setenv(locus.AdminEnv, admin)
	f := &fake{objs: map[string][]byte{}}
	was := bucket
	bucket = f
	t.Cleanup(func() { bucket = was })
	projectOwned = func(context.Context, string, string) (bool, error) { return false, plane.ErrNoPeer }
	db, closeDB, err := locus.Tenants(context.Background(), locus.Tenancy{Name: "library", DataDir: t.TempDir(), Tables: tables, Tenant: tenantOf})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = closeDB() })
	s := &service{store: store{db: db}, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test"), BodyLimit: 16 << 20})
	app.Use(cloud.Bridge())
	routes(app, s)
	return app, s, f
}

// call drives one request as user in org; an empty user is no validated principal.
func call(t *testing.T, app *zip.App, method, path, user, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// png is the smallest PNG header http.DetectContentType names image/png.
var png = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)

func keep(name, source, ref string) map[string]any {
	return map[string]any{
		"name": name, "mime": "image/png", "data": base64.StdEncoding.EncodeToString(png),
		"source": source, "ref": ref, "origin": "/chat?thread=" + ref,
	}
}

func item(t *testing.T, b []byte) client.LibraryItem {
	t.Helper()
	var it client.LibraryItem
	if err := json.Unmarshal(b, &it); err != nil {
		t.Fatalf("item: %v (%s)", err, b)
	}
	return it
}

func page(t *testing.T, b []byte) itemPage {
	t.Helper()
	var p itemPage
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("page: %v (%s)", err, b)
	}
	return p
}

// An item is its owner's: a member of the same org sees none of it until it is
// shared, another org never sees it, and only its owner changes or deletes it.
func TestAnItemIsItsOwnersUntilShared(t *testing.T) {
	app, _, f := mount(t)
	owned(t, app, f)
}

// The same scoping on PostgreSQL, where row-level security is the second guard,
// and the guard alone: a raw statement as the store's own login, with no
// condition of the builder's, reads only the org its transaction names.
func TestPostgresHoldsTheSameScoping(t *testing.T) {
	if pg == nil {
		t.Skip("no PostgreSQL on this machine: " + why)
	}
	// The server listens on a socket in its own directory; a URL names it as host.
	dir := strings.TrimPrefix(strings.Fields(pg.DSN("", ""))[0], "host=")
	dsn := "postgres://" + pgtest.Super + "@localhost:5432/postgres?sslmode=disable&host=" + url.QueryEscape(dir)
	app, s, f := mountOn(t, dsn)
	owned(t, app, f)
	as := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
	if _, err := s.put(as, &client.LibraryPutIn{Source: "chat", Name: "a.png", Mime: "image/png", Data: png}); err != nil {
		t.Fatalf("put: %v", err)
	}
	login, err := pg.Open("library", "library")
	if err != nil {
		t.Fatalf("open as the store's login: %v", err)
	}
	defer func() { _ = login.Close() }()
	for org, want := range map[string]int{"acme": 1, "globex": 0} {
		tx, err := login.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`SELECT set_config('role', 'library_tenant', true), set_config('app.org', $1, true)`, org); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM library.items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()
		if n != want {
			t.Errorf("as %s, a raw count of library.items is %d, want %d", org, n, want)
		}
	}
}

func owned(t *testing.T, app *zip.App, f *fake) {
	t.Helper()
	code, b := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", keep("poster.png", "studio", "run_1"))
	if code != http.StatusCreated {
		t.Fatalf("keep: %d %s", code, b)
	}
	it := item(t, b)
	if !strings.HasPrefix(it.ID, "lib_") || !it.Mine || it.Shared || it.Kind != "image" || it.Owner != "alice" {
		t.Fatalf("kept: %+v", it)
	}
	if it.Preview == "" || it.Preview != it.URL || it.Download == "" || it.Origin != "/chat?thread=run_1" {
		t.Fatalf("an image is its own preview, and keeps its origin: %+v", it)
	}

	// Bob, same org: nothing listed, and the id is not found by any verb.
	if _, b := call(t, app, http.MethodGet, "/v1/library", "bob", "acme", nil); len(page(t, b).Data) != 0 {
		t.Fatalf("bob listed alice's item: %s", b)
	}
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		var body any
		if m == http.MethodPatch {
			body = map[string]any{"shared": true}
		}
		if code, b := call(t, app, m, "/v1/library/"+it.ID, "bob", "acme", body); code != http.StatusNotFound {
			t.Fatalf("bob %s alice's item: %d %s", m, code, b)
		}
	}

	// Shared: bob reads it, as not his, and still changes nothing.
	if code, b := call(t, app, http.MethodPatch, "/v1/library/"+it.ID, "alice", "acme", map[string]any{"shared": true}); code != http.StatusOK || !item(t, b).Shared {
		t.Fatalf("share: %d %s", code, b)
	}
	_, b = call(t, app, http.MethodGet, "/v1/library", "bob", "acme", nil)
	if p := page(t, b); len(p.Data) != 1 || p.Data[0].Mine || p.Data[0].ID != it.ID {
		t.Fatalf("bob after the share: %s", b)
	}
	if _, b := call(t, app, http.MethodGet, "/v1/library?scope=mine", "bob", "acme", nil); len(page(t, b).Data) != 0 {
		t.Fatalf("scope=mine lists another's item: %s", b)
	}
	if code, _ := call(t, app, http.MethodDelete, "/v1/library/"+it.ID, "bob", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("bob deleted a shared item: %d", code)
	}

	// Another org — even a person with the same subject — sees nothing, and 404 by id.
	for _, user := range []string{"carol", "alice"} {
		if _, b := call(t, app, http.MethodGet, "/v1/library", user, "globex", nil); len(page(t, b).Data) != 0 {
			t.Fatalf("%s@globex listed acme's item: %s", user, b)
		}
		if code, b := call(t, app, http.MethodGet, "/v1/library/"+it.ID, user, "globex", nil); code != http.StatusNotFound {
			t.Fatalf("%s@globex read acme's item: %d %s", user, code, b)
		}
	}

	// The owner deletes it, and its bytes go with it.
	key := objectKey("acme", "alice", it.ID, it.Name)
	if !f.has(key) {
		t.Fatalf("no object at %s", key)
	}
	if code, b := call(t, app, http.MethodDelete, "/v1/library/"+it.ID, "alice", "acme", nil); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, b)
	}
	if f.has(key) {
		t.Fatal("the bytes outlived the item")
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/library/"+it.ID, "alice", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("deleted item still read: %d", code)
	}
}

// Nobody keeps nothing: a call with no validated principal is refused, and an
// application acting as itself keeps no Library.
func TestOnlyAPersonKeepsALibrary(t *testing.T) {
	app, _, _ := mount(t)
	if code, b := call(t, app, http.MethodGet, "/v1/library", "", "acme", nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("anonymous list: %d %s", code, b)
	}
	if code, b := call(t, app, http.MethodPost, "/v1/library", "", "acme", keep("a.png", "chat", "c")); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("anonymous keep: %d %s", code, b)
	}
	if code, b := call(t, app, http.MethodPost, "/v1/library", "app:acme/bot", "acme", keep("a.png", "chat", "c")); code != http.StatusForbidden {
		t.Fatalf("an application kept an item: %d %s", code, b)
	}
}

// What a writer says is held at the boundary: an origin is a path on this site
// and nothing else, and the words are the Library's.
func TestAWriteIsHeldAtTheBoundary(t *testing.T) {
	app, _, _ := mount(t)
	bad := map[string]func(map[string]any){
		"origin with a scheme":    func(m map[string]any) { m["origin"] = "https://evil.example/x" },
		"protocol-relative":       func(m map[string]any) { m["origin"] = "//evil.example/x" },
		"javascript":              func(m map[string]any) { m["origin"] = "javascript:alert(1)" },
		"unknown source":          func(m map[string]any) { m["source"] = "elsewhere" },
		"unknown kind":            func(m map[string]any) { m["kind"] = "virus" },
		"empty data":              func(m map[string]any) { m["data"] = "" },
		"no media type":           func(m map[string]any) { m["mime"] = "png" },
		"name across two lines":   func(m map[string]any) { m["name"] = "a\nb" },
		"preview is not an image": func(m map[string]any) { m["preview"] = base64.StdEncoding.EncodeToString([]byte("<html>")) },
	}
	for what, edit := range bad {
		body := keep("a.png", "chat", "c")
		edit(body)
		if code, b := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", body); code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", what, code, b)
		}
	}
}

// A save with a key already used answers the item already kept, so a product
// that retries — or a page that re-renders — files one item, not two.
func TestAKeyMakesASaveIdempotent(t *testing.T) {
	app, _, _ := mount(t)
	body := keep("a.png", "chat", "conv_1")
	body["key"] = "conv_1/msg_1/0"
	_, b1 := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", body)
	_, b2 := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", body)
	if a, b := item(t, b1), item(t, b2); a.ID == "" || a.ID != b.ID {
		t.Fatalf("one key, two items: %s / %s", b1, b2)
	}
	// The same key from another person is their own item.
	_, b3 := call(t, app, http.MethodPost, "/v1/library", "bob", "acme", body)
	if item(t, b3).ID == item(t, b1).ID {
		t.Fatal("bob's save answered alice's item")
	}
	if _, b := call(t, app, http.MethodGet, "/v1/library?ref=conv_1&scope=mine", "alice", "acme", nil); len(page(t, b).Data) != 1 {
		t.Fatalf("alice has %s", b)
	}
}

// A list narrows by kind and source and pages on its cursor, newest first,
// without repeating or dropping a row.
func TestAListPagesWithoutLoss(t *testing.T) {
	app, _, _ := mount(t)
	for i := range 5 {
		body := keep("n.png", "studio", "r")
		if i%2 == 1 {
			body["source"], body["mime"], body["name"] = "dev", "text/x-diff", "changes.patch"
		}
		if code, b := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", body); code != http.StatusCreated {
			t.Fatalf("keep %d: %d %s", i, code, b)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for range 5 {
		_, b := call(t, app, http.MethodGet, "/v1/library?limit=2&cursor="+cursor, "alice", "acme", nil)
		p := page(t, b)
		for _, it := range p.Data {
			if seen[it.ID] {
				t.Fatalf("%s listed twice", it.ID)
			}
			seen[it.ID] = true
		}
		if cursor = p.Next; cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("paged %d of 5", len(seen))
	}
	_, b := call(t, app, http.MethodGet, "/v1/library?source=dev", "alice", "acme", nil)
	if p := page(t, b); len(p.Data) != 2 || p.Data[0].Kind != "code" || p.Data[0].Preview != "" {
		t.Fatalf("source=dev: %s", b)
	}
	_, b = call(t, app, http.MethodGet, "/v1/library?kind=image", "alice", "acme", nil)
	if len(page(t, b).Data) != 3 {
		t.Fatalf("kind=image: %s", b)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/library?cursor=nonsense!", "alice", "acme", nil); code != http.StatusBadRequest {
		t.Fatalf("a forged cursor: %d", code)
	}
}

// A sibling writes through the plane as the person it acts for, and a large item
// is reserved, uploaded and committed — held to the size it reserved.
func TestTheWritePathOverThePlane(t *testing.T) {
	app, s, f := mount(t)
	as := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
	it, err := s.put(as, &client.LibraryPutIn{Source: "studio", Name: "still.png", Mime: "image/png", Data: png, Ref: "run_9", Origin: "/studio?run=run_9", Key: "run_9/a/0"})
	if err != nil || !it.Mine {
		t.Fatalf("put: %v %+v", err, it)
	}
	if _, b := call(t, app, http.MethodGet, "/v1/library?source=studio", "alice", "acme", nil); len(page(t, b).Data) != 1 {
		t.Fatalf("a plane write is not listed: %s", b)
	}
	if _, err := s.put(context.Background(), &client.LibraryPutIn{Source: "studio", Name: "x.png", Mime: "image/png", Data: png}); err == nil {
		t.Fatal("a call stating no caller kept an item")
	}

	res, err := s.reserve(as, &client.LibraryPutIn{Source: "studio", Name: "film.mp4", Mime: "video/mp4", Bytes: 64, Key: "run_9/film"})
	if err != nil || res.Upload.URL == "" || res.Item.URL != "" {
		t.Fatalf("reserve: %v %+v", err, res)
	}
	if _, b := call(t, app, http.MethodGet, "/v1/library?kind=video", "alice", "acme", nil); len(page(t, b).Data) != 0 {
		t.Fatalf("a reservation is listed before its bytes: %s", b)
	}
	if _, err := s.commit(as, &client.LibraryRef{ID: res.Item.ID}); err == nil {
		t.Fatal("committed with no bytes")
	}
	key := objectKey("acme", "alice", res.Item.ID, "film.mp4")
	_ = f.put(context.Background(), key, "video/mp4", bytes.Repeat([]byte{1}, 65))
	if _, err := s.commit(as, &client.LibraryRef{ID: res.Item.ID}); err == nil || f.has(key) {
		t.Fatal("an upload larger than reserved was kept")
	}
	_ = f.put(context.Background(), key, "video/mp4", bytes.Repeat([]byte{1}, 40))
	done, err := s.commit(as, &client.LibraryRef{ID: res.Item.ID})
	if err != nil || done.Bytes != 40 || done.URL == "" {
		t.Fatalf("commit: %v %+v", err, done)
	}
	bob := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob"})
	if _, err := s.get(bob, &client.LibraryRef{ID: done.ID}); err == nil {
		t.Fatal("bob read alice's item over the plane")
	}

	// A person reserves and commits a large upload over HTTP (/v1/library) the same way.
	code, b := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", map[string]any{
		"name": "take2.mp4", "mime": "video/mp4", "bytes": 128, "source": "chat",
	})
	if code != http.StatusCreated {
		t.Fatalf("http reserve: %d %s", code, b)
	}
	httpRes := item(t, b)
	if httpRes.Upload == nil || httpRes.Upload.URL == "" || httpRes.URL != "" {
		t.Fatalf("http reserve upload: %+v", httpRes)
	}
	httpKey := objectKey("acme", "alice", httpRes.ID, "take2.mp4")
	_ = f.put(context.Background(), httpKey, "video/mp4", bytes.Repeat([]byte{2}, 96))
	code, b = call(t, app, http.MethodPatch, "/v1/library/"+httpRes.ID, "alice", "acme", map[string]any{"commit": true})
	if code != http.StatusOK {
		t.Fatalf("http commit: %d %s", code, b)
	}
	if committed := item(t, b); committed.Bytes != 96 || committed.URL == "" {
		t.Fatalf("http committed: %+v", committed)
	}
}

// Opened in place, an item is served as a type that cannot run.
func TestInlineNeverRunsMarkup(t *testing.T) {
	for in, want := range map[string]string{
		"image/png":             "image/png",
		"video/mp4":             "video/mp4",
		"application/pdf":       "application/pdf",
		"text/html":             "text/plain; charset=utf-8",
		"image/svg+xml":         "text/plain; charset=utf-8",
		"application/xhtml+xml": "text/plain; charset=utf-8",
		"text/javascript":       "text/plain; charset=utf-8",
	} {
		if got := inline(in); got != want {
			t.Errorf("inline(%s) = %s, want %s", in, got, want)
		}
	}
	k := objectKey("acme", "alice", "lib_1", "../../etc/passwd")
	segs := strings.Split(k, "/")
	if len(segs) != 4 || segs[2] != "lib_1" {
		t.Errorf("a name escaped its prefix: %s", k)
	}
	for _, s := range segs {
		if s == ".." || s == "." || s == "" {
			t.Errorf("a name escaped its prefix: %s", k)
		}
	}
}

// The SuperAdmin's view of rows no one owns is the SuperAdmin's alone.
func TestTheUnownedViewIsTheSuperAdminsAlone(t *testing.T) {
	app, _, _ := mount(t)
	if code, b := call(t, app, http.MethodGet, "/v1/library/unowned", "alice", "acme", nil); code != http.StatusForbidden {
		t.Fatalf("a member read the unowned view: %d %s", code, b)
	}
}

// Every Library upload routes to the organization's own S3 bucket
// (object.BucketName(org, "library")), and the quota is enforced across the
// organization rather than per individual member.
func TestUploadsRouteToOrgBucketAndShareOrgQuota(t *testing.T) {
	st := &store3{}
	for _, org := range []string{"acme", "globex", "hanzo"} {
		k := objectKey(org, "alice", "lib_1", "model.bin")
		if got, want := st.bucketFor(k), object.BucketName(org, "library"); got != want {
			t.Fatalf("bucketFor(%s) = %q, want %q", org, got, want)
		}
		sk := stillKey(org, "alice", "lib_1")
		if got, want := st.bucketFor(sk), object.BucketName(org, "library"); got != want {
			t.Fatalf("bucketFor still(%s) = %q, want %q", org, got, want)
		}
	}

	app, _, _ := mount(t)
	was := quota
	quota = int64(len(png)) * 2
	t.Cleanup(func() { quota = was })

	// Alice and Bob in acme share acme's org quota: two items fit, the third is 507.
	if code, b := call(t, app, http.MethodPost, "/v1/library", "alice", "acme", keep("1.png", "chat", "r1")); code != http.StatusCreated {
		t.Fatalf("alice 1: %d %s", code, b)
	}
	if code, b := call(t, app, http.MethodPost, "/v1/library", "bob", "acme", keep("2.png", "chat", "r2")); code != http.StatusCreated {
		t.Fatalf("bob 2: %d %s", code, b)
	}
	if code, b := call(t, app, http.MethodPost, "/v1/library", "bob", "acme", keep("3.png", "chat", "r3")); code != http.StatusInsufficientStorage {
		t.Fatalf("bob 3 (past org quota): want 507, got %d %s", code, b)
	}
	// Another org has its own 100 GB quota and is unaffected by acme's usage.
	if code, b := call(t, app, http.MethodPost, "/v1/library", "carol", "globex", keep("1.png", "chat", "r1")); code != http.StatusCreated {
		t.Fatalf("carol@globex: %d %s", code, b)
	}
}

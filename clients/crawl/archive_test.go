package crawl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeVFS is an in-memory stand-in for the object seam. The archive's contract is
// "keys in, bytes out", so a map proves everything that matters here without an S3.
type fakeVFS struct {
	mu   sync.Mutex
	objs map[string][]byte
	puts int
	fail bool
}

func newFakeVFS() *fakeVFS { return &fakeVFS{objs: map[string][]byte{}} }

func (f *fakeVFS) Put(_ context.Context, key string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("object store down")
	}
	f.puts++
	f.objs[key] = append([]byte(nil), payload...)
	return nil
}

func (f *fakeVFS) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("object store down")
	}
	b, ok := f.objs[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func (f *fakeVFS) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, key)
	return nil
}

func (f *fakeVFS) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objs))
	for k := range f.objs {
		out = append(out, k)
	}
	return out
}

// bind installs an archive for the test and restores the previous one, so tests
// that touch process-wide state do not leak into each other.
func bind(t *testing.T, vfs *fakeVFS) {
	t.Helper()
	prev := store.Load()
	Bind(vfs)
	t.Cleanup(func() { store.Store(prev) })
}

func TestArchiveRoundTrip(t *testing.T) {
	f := newFakeVFS()
	a := &Archive{vfs: f}
	s := Scope{Org: "acme", Project: "proj"}
	p := &Page{URL: "https://example.com/final", Title: "T", Markdown: "# hi", Metadata: map[string]any{"status": 200}}

	if err := a.Put(context.Background(), s, "https://example.com/asked", p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := a.Get(context.Background(), s, "https://example.com/asked")
	if !ok {
		t.Fatal("Get missed a page that was just Put")
	}
	if got.Markdown != "# hi" || got.Title != "T" {
		t.Fatalf("round trip lost content: %+v", got)
	}
	// The FINAL url is what a citation must point at.
	if got.URL != "https://example.com/final" {
		t.Fatalf("URL = %q, want the final (post-redirect) url", got.URL)
	}
}

// The redirect case the key scheme exists for: a page filed under where it LANDED
// could never be found again, because callers only ever ask by the url they know.
func TestArchiveKeysByRequestedURLNotFinal(t *testing.T) {
	f := newFakeVFS()
	a := &Archive{vfs: f}
	s := Scope{Org: "acme"}
	asked := "https://example.com/short"
	p := &Page{URL: "https://example.com/very/long/final", Markdown: "body"}

	if err := a.Put(context.Background(), s, asked, p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := a.Get(context.Background(), s, asked); !ok {
		t.Fatal("a redirected page is unreachable by the url that was requested")
	}
	if _, ok := a.Get(context.Background(), s, p.URL); ok {
		t.Fatal("filed under the final url as well — the key must be the requested one only")
	}
}

// The isolation boundary. Two orgs reading the same URL must not see each other's
// copies, because the prefix is the only thing separating them.
func TestArchiveIsolatesOrgs(t *testing.T) {
	f := newFakeVFS()
	a := &Archive{vfs: f}
	url := "https://example.com/x"

	if err := a.Put(context.Background(), Scope{Org: "acme"}, url, &Page{URL: url, Markdown: "acme secret"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := a.Get(context.Background(), Scope{Org: "other"}, url); ok {
		t.Fatal("another org read acme's archived page")
	}
	if _, ok := a.Get(context.Background(), Scope{Org: "acme"}, url); !ok {
		t.Fatal("the owning org lost its own page")
	}
	// Projects separate within an org for the same reason.
	if _, ok := a.Get(context.Background(), Scope{Org: "acme", Project: "p2"}, url); ok {
		t.Fatal("a different project read the org-default page")
	}
}

// A key must never be steerable by the caller. The url is hashed and the scope is
// sanitised, so neither a crafted url nor a crafted org can walk out of its prefix.
func TestKeyCannotEscapeItsPrefix(t *testing.T) {
	nasty := []struct{ org, project, url string }{
		{"../../etc", "p", "https://example.com/a"},
		{"acme", "../../..", "https://example.com/a"},
		{"acme", "p", "https://example.com/../../../etc/passwd"},
		{"acme/../other", "p", "https://example.com/a"},
		{"", "", "https://example.com/a"},
	}
	for _, n := range nasty {
		k := key(Scope{Org: n.org, Project: n.project}, n.url)
		if !strings.HasPrefix(k, "crawl/") {
			t.Errorf("key(%q,%q) = %q — left the crawl/ prefix", n.org, n.project, k)
		}
		if strings.Contains(k, "..") {
			t.Errorf("key(%q,%q) = %q — contains a traversal segment", n.org, n.project, k)
		}
		if strings.Contains(k, "//") {
			t.Errorf("key(%q,%q) = %q — empty segment collapses two scopes into one", n.org, n.project, k)
		}
		// crawl/<org>/<project>/<64 hex>.json
		if parts := strings.Split(k, "/"); len(parts) != 4 {
			t.Errorf("key(%q,%q) = %q — %d segments, want 4", n.org, n.project, k, len(parts))
		}
	}
}

// Two different urls must never collide, and the same url must always land in the
// same place or Get could not find what Put wrote.
func TestKeyIsDeterministicAndDistinct(t *testing.T) {
	s := Scope{Org: "acme", Project: "p"}
	a1 := key(s, "https://example.com/a")
	a2 := key(s, "https://example.com/a")
	b := key(s, "https://example.com/b")
	if a1 != a2 {
		t.Fatal("key is not deterministic — Get can never find what Put wrote")
	}
	if a1 == b {
		t.Fatal("distinct urls collided on one key")
	}
}

// Read is the door: a hit must not touch the network. Proven by archiving a page
// under a url that could never be fetched — if Read reached the network it would
// error rather than return the stored body.
func TestReadServesFromArchiveWithoutFetching(t *testing.T) {
	f := newFakeVFS()
	bind(t, f)
	s := Scope{Org: "acme"}
	unfetchable := "http://127.0.0.1:1/blocked-by-the-address-guard"

	a := store.Load()
	if err := a.Put(context.Background(), s, unfetchable, &Page{URL: unfetchable, Markdown: "from the corpus"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := Read(context.Background(), s, unfetchable)
	if err != nil {
		t.Fatalf("Read: %v — a hit must not reach the network", err)
	}
	if got.Markdown != "from the corpus" {
		t.Fatalf("Read returned %q, want the archived body", got.Markdown)
	}
}

// A miss on an unreachable url still fails, and stores nothing: a failed fetch is
// not a page, and caching it would serve the failure back forever.
func TestReadMissDoesNotArchiveFailures(t *testing.T) {
	f := newFakeVFS()
	bind(t, f)
	if _, err := Read(context.Background(), Scope{Org: "acme"}, "http://169.254.169.254/latest/"); err == nil {
		t.Fatal("Read succeeded on a blocked address")
	}
	if len(f.keys()) != 0 {
		t.Fatalf("a failed fetch was archived: %v", f.keys())
	}
}

// No object store configured is a supported deployment: crawling keeps working and
// keeps nothing. It must not become a crawl outage.
func TestReadWorksWithNoArchiveBound(t *testing.T) {
	prev := store.Load()
	Bind(nil)
	t.Cleanup(func() { store.Store(prev) })

	// Still reaches the guard rather than failing on a nil archive.
	_, err := Read(context.Background(), Scope{Org: "acme"}, "http://127.0.0.1:1/")
	if err == nil || !strings.Contains(err.Error(), "refused to dial") {
		t.Fatalf("err = %v, want the fetch path's own error with no archive bound", err)
	}
}

// A store that is down costs a cache hit, never the page.
func TestArchiveFailureIsNotAReadFailure(t *testing.T) {
	f := newFakeVFS()
	f.fail = true
	bind(t, f)
	// The fetch itself fails here (blocked address), but the point is the error is
	// the FETCH's, not the store's — the archive never gets to fail the call.
	_, err := Read(context.Background(), Scope{Org: "acme"}, "http://10.0.0.1/")
	if err == nil {
		t.Fatal("expected the fetch error")
	}
	if strings.Contains(err.Error(), "object store down") {
		t.Fatalf("a storage failure surfaced as a crawl failure: %v", err)
	}
}

// seg must be INJECTIVE. Sanitising alone is not: these pairs all collapse to the
// same readable form, and a collision here means two orgs share one corpus prefix.
func TestSegIsInjective(t *testing.T) {
	collide := [][2]string{
		{"a/b", "a-b"},
		{"a b", "a-b"},
		{"a--b", "a-b"},
		{"ACME", "acme"},  // case folds
		{"acme ", "acme"}, // trims
		{"../../etc", "etc"},
		{"", "   "},
	}
	for _, p := range collide {
		if seg(p[0]) == seg(p[1]) {
			t.Errorf("seg(%q) == seg(%q) == %q — distinct scopes share a prefix", p[0], p[1], seg(p[0]))
		}
	}
	// Same input, same segment — or Get could never find what Put wrote.
	if seg("acme") != seg("acme") {
		t.Fatal("seg is not deterministic")
	}
}

// The readable half survives, because a corpus nobody can navigate is much less
// useful to index and build against.
func TestSegStaysReadable(t *testing.T) {
	if got := seg("acme"); !strings.HasPrefix(got, "acme-") {
		t.Errorf("seg(\"acme\") = %q, want a readable acme- prefix", got)
	}
	if got := seg("Big Corp"); !strings.HasPrefix(got, "big-corp-") {
		t.Errorf("seg(\"Big Corp\") = %q, want a readable big-corp- prefix", got)
	}
	// Nothing printable: the digest alone, never an empty segment.
	if got := seg("🙂"); got == "" || strings.Contains(got, "/") {
		t.Errorf("seg(emoji) = %q, want a non-empty digest segment", got)
	}
	// Bounded, whatever the input length.
	if got := seg(strings.Repeat("x", 500)); len(got) > 60 {
		t.Errorf("seg(long) length %d — keys must stay bounded", len(got))
	}
}

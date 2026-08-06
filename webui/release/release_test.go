package release

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/sites"
)

// ---- the in-memory bundle ----

func bundleOf(files map[string]string) *bundle {
	m := memFS{}
	var total int64
	for name, body := range files {
		m[name] = &memFile{name: name, data: []byte(body), mod: time.Unix(1700000000, 0).UTC()}
		total += int64(len(body))
	}
	return &bundle{prefix: "hanzo/.releases/hanzo-console/rel_" + "0123456789abcdef0123456789abcdef",
		files: len(m), bytes: total, fsys: m}
}

// TestMemFSServesWhatWasLoaded: the properties the console handler relies on —
// exact bytes, a real FileInfo, a miss that is fs.ErrNotExist (so the handler
// falls back to the SPA shell rather than erroring), and no synthesized
// directories (a directory request is not a file and must fall through).
func TestMemFSServesWhatWasLoaded(t *testing.T) {
	m := bundleOf(map[string]string{
		"index.html":                            "<html>shell</html>",
		"_next/static/css/bdec3a94ead6ad5f.css": "body{margin:0}",
	}).fsys

	f, err := m.Open("_next/static/css/bdec3a94ead6ad5f.css")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "body{margin:0}" {
		t.Fatalf("read = %q, %v; want the stored bytes", b, err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.IsDir() || info.Size() != 14 || info.Name() != "bdec3a94ead6ad5f.css" {
		t.Errorf("Stat = %q %d dir=%v, want the file's own identity", info.Name(), info.Size(), info.IsDir())
	}
	if info.ModTime().IsZero() {
		t.Error("ModTime is zero — a conditional GET would have nothing to answer against")
	}

	if _, err := m.Open("nope.js"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(missing) = %v, want fs.ErrNotExist so the handler falls back to the shell", err)
	}
	if _, err := m.Open("_next/static"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(directory) = %v, want fs.ErrNotExist — a directory is not an asset", err)
	}
}

// TestMemFileSeeks: http.ServeContent needs a ReadSeeker to answer a range
// request without the handler buffering the body. Prove a range actually works
// through the stdlib, not just that the method exists.
func TestMemFileSeeks(t *testing.T) {
	m := bundleOf(map[string]string{"index.html": "0123456789"}).fsys
	f, err := m.Open("index.html")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		t.Fatal("an open bundle file does not seek — ServeContent would buffer every asset")
	}
	info, _ := f.Stat()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	req.Header.Set("Range", "bytes=2-5")
	http.ServeContent(rec, req, "index.html", info.ModTime(), rs)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Errorf("range GET = %d %q, want 206 \"2345\"", rec.Code, rec.Body.String())
	}
}

// ---- the live view ----

// TestSourceOpenReadsTheMountedRelease, and TestSourceSwapIsAtomic below it: the
// handler reads through Source per request, so a swap has to be visible with no
// lock held by the reader and with no window in which half of each release is
// mounted.
func TestSourceOpenReadsTheMountedRelease(t *testing.T) {
	s := &Source{}
	s.cur.Store(bundleOf(map[string]string{"index.html": "A"}))

	f, err := s.Open("index.html")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "A" {
		t.Errorf("Open(index.html) = %q, want the mounted release's bytes", b)
	}
	if s.Release() == "" {
		t.Error("Release() is empty — which console is live must be answerable")
	}
}

func TestSourceSwapIsAtomic(t *testing.T) {
	s := &Source{}
	s.cur.Store(bundleOf(map[string]string{"index.html": "A", "a.js": "//a"}))

	read := func(name string) string {
		f, err := s.Open(name)
		if err != nil {
			return ""
		}
		b, _ := io.ReadAll(f)
		_ = f.Close()
		return string(b)
	}
	if read("a.js") != "//a" {
		t.Fatal("the first release did not mount")
	}

	s.cur.Store(bundleOf(map[string]string{"index.html": "B", "b.js": "//b"}))

	if got := read("index.html"); got != "B" {
		t.Errorf("index.html after the swap = %q, want the new release", got)
	}
	if read("a.js") != "" {
		t.Error("the old release's asset still resolves — the swap replaced part of a bundle, not the bundle")
	}
	if read("b.js") != "//b" {
		t.Error("the new release's asset does not resolve")
	}
}

// TestFSMakesANilSourceANilFS pins Go's oldest trap out of the composition roots:
// a nil *Source assigned to an fs.FS is a NON-nil interface, and webui would then
// dereference it on the first request instead of answering the 503 it is written
// to answer.
func TestFSMakesANilSourceANilFS(t *testing.T) {
	if got := FS(nil); got != nil {
		t.Errorf("FS(nil) = %#v, want a nil fs.FS", got)
	}
	s := &Source{}
	if FS(s) == nil {
		t.Error("FS(non-nil) = nil")
	}
}

// ---- resolution ----

type stubResolver struct {
	site  sites.Site
	found bool
	err   error
	org   string // what it was asked for
	slug  string
}

func (r *stubResolver) Resolve(context.Context, string) (sites.Site, bool, error) {
	return sites.Site{}, false, errors.New("the console must never resolve unpinned")
}

func (r *stubResolver) ResolveOrg(_ context.Context, org, slug string) (sites.Site, bool, error) {
	r.org, r.slug = org, slug
	return r.site, r.found, r.err
}

// TestResolveIsOrgPinnedAndNamesEveryFailure: the console resolves PINNED to its
// owning org — never the unique-across-orgs path — so a customer project named
// hanzo-console can never become the console. And every refusal names the site,
// because these strings are the only diagnosis a boot failure will have.
func TestResolveIsOrgPinnedAndNamesEveryFailure(t *testing.T) {
	defer sites.SetFallbackResolver(nil)
	cfg := Config{Org: "hanzo", Slug: "hanzo-console", Poll: time.Minute}

	live := sites.Site{Org: "hanzo", Slug: "hanzo-console", Bucket: "b", Prefix: "hanzo/.releases/hanzo-console/rel_x", Status: "live"}
	r := &stubResolver{site: live, found: true}
	sites.SetFallbackResolver(r)

	s := &Source{cfg: cfg}
	got, err := s.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.org != "hanzo" || r.slug != "hanzo-console" {
		t.Errorf("resolved %q/%q, want the configured org-pinned site", r.org, r.slug)
	}
	if got.Prefix != live.Prefix {
		t.Errorf("prefix = %q, want %q", got.Prefix, live.Prefix)
	}

	for name, tc := range map[string]struct {
		r    *stubResolver
		want string
	}{
		"missing":    {&stubResolver{found: false}, "no such site"},
		"not live":   {&stubResolver{site: sites.Site{Status: "draft", Prefix: "p"}, found: true}, "not live"},
		"errored":    {&stubResolver{err: errors.New("plane down")}, "plane down"},
		"prefixless": {&stubResolver{site: sites.Site{Status: "live"}, found: true}, "no prefix"},
	} {
		t.Run(name, func(t *testing.T) {
			sites.SetFallbackResolver(tc.r)
			_, err := (&Source{cfg: cfg}).resolve(context.Background())
			if err == nil {
				t.Fatalf("resolve succeeded on a %s site", name)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "hanzo/hanzo-console") {
				t.Errorf("error = %q, want it to name the site and %q", err, tc.want)
			}
		})
	}

	// No resolver at all is a WIRING fault (the site edge must be mounted first),
	// and it has to read as one rather than as an empty console.
	sites.SetFallbackResolver(nil)
	if _, err := (&Source{cfg: cfg}).resolve(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "no site resolver installed") {
		t.Errorf("error = %v, want it to name the missing resolver", err)
	}
}

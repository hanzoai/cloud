package sites

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
	"github.com/zap-proto/zip"
)

// store is an object store double with the behaviours the edge has to be right about:
//
//   - existence is the stat's answer. A store may answer a read of a DIRECTORY key
//     with an empty object while a stat says it is not there; only the stat can tell
//     `docs` the directory from `docs` the page.
//   - a read that goes out after the stat can find the object replaced, or fail;
//     the edge retries once and then answers 503.
//
// failGets makes that many reads fail, standing in for a publish replacing the
// object between the stat and the read.
type store struct {
	object.Store
	mu       sync.Mutex
	objects  map[string][]byte // "<bucket>/<key>"
	dirs     map[string]bool   // "<bucket>/<key>" a directory entry the store would read as empty
	failGets int
	gets     int
}

// current is the store serveSite points the edge at.
var current *store

// withStore points s at the double startStore made.
func withStore(s *Server) *Server {
	s.store = current
	return s
}

func startStore(t *testing.T) *store {
	t.Helper()
	st := &store{objects: map[string][]byte{}, dirs: map[string]bool{}}
	current = st
	t.Cleanup(func() { current = nil })
	return st
}

func (st *store) put(key string, body []byte) {
	st.mu.Lock()
	st.objects[key] = body
	st.mu.Unlock()
}

func etag(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func (st *store) Stat(_ context.Context, bucket, key string) (object.Info, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	body, ok := st.objects[bucket+"/"+key]
	if !ok || st.dirs[bucket+"/"+key] {
		return object.Info{}, object.ErrNoKey
	}
	return object.Info{Key: key, Size: int64(len(body)), ETag: etag(body)}, nil
}

func (st *store) Open(_ context.Context, bucket, key string, _ object.Get) (*object.Body, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.gets++
	if st.failGets > 0 {
		st.failGets--
		return nil, object.ErrConflict
	}
	body, ok := st.objects[bucket+"/"+key]
	if !ok {
		return nil, object.ErrNoKey
	}
	return &object.Body{ReadCloser: io.NopCloser(bytes.NewReader(body)), Info: object.Info{Key: key, Size: int64(len(body)), ETag: etag(body)}}, nil
}

// serveSite runs one request for mysite.hanzo.app through the edge, backed by st.
func serveSite(t *testing.T, path string) (int, []byte) {
	t.Helper()
	SetResolver(&fakeResolver{found: true, site: Site{Org: "o", Slug: "mysite", Bucket: "sites", Prefix: "o/mysite", Status: "live"}})
	t.Cleanup(func() { SetResolver(nil) })
	app := newTestApp(withStore(testServer()))
	resp, err := app.Test(httptest.NewRequest("GET", "http://mysite.hanzo.app"+path, nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s: the edge dropped the response: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: truncated body: %v", path, err)
	}
	return resp.StatusCode, b
}

// An object replaced between the HEAD and the GET is served whole, by a retry.
// Left to the response writer, that GET's 412 could only drop the connection.
func TestObjectReplacedMidRequestIsServedWhole(t *testing.T) {
	st := startStore(t)
	page := []byte("<!doctype html><title>mysite</title><p>hello</p>")
	st.put("sites/o/mysite/index.html", page)
	st.failGets = 1

	code, body := serveSite(t, "/")
	if code != http.StatusOK || !bytes.Equal(body, page) {
		t.Fatalf("GET / = %d %q, want 200 with the page", code, body)
	}
	if st.gets != 2 {
		t.Fatalf("store saw %d GETs, want 2 (the failed one and its retry)", st.gets)
	}
}

// A store that keeps failing gets an honest 503 — not a 200 that never finishes,
// and not a 404 for a page that exists.
func TestStoreThatCannotReadIs503(t *testing.T) {
	st := startStore(t)
	st.put("sites/o/mysite/index.html", []byte("<!doctype html><p>hello</p>"))
	st.failGets = 1 << 20

	code, _ := serveSite(t, "/")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET / with the store failing = %d, want 503", code)
	}
}

// An object larger than what the edge reads up front streams complete, with the
// Content-Length it declared.
func TestLargeObjectStreamsWhole(t *testing.T) {
	st := startStore(t)
	big := bytes.Repeat([]byte("0123456789abcdef"), 3*headBytes/16+7) // not a multiple of headBytes
	st.put("sites/o/mysite/assets/app.js", big)

	code, body := serveSite(t, "/assets/app.js")
	if code != http.StatusOK || !bytes.Equal(body, big) {
		t.Fatalf("GET /assets/app.js = %d, %d bytes; want 200, %d bytes intact", code, len(body), len(big))
	}
}

// Existence stays the HEAD's answer: a directory key the store would GET as an
// empty 200 still falls through to the page that shares its name.
func TestDirectoryKeyFallsThroughToItsPage(t *testing.T) {
	st := startStore(t)
	st.dirs["sites/o/mysite/docs"] = true
	page := []byte("<!doctype html><title>docs</title>")
	st.put("sites/o/mysite/docs.html", page)

	code, body := serveSite(t, "/docs")
	if code != http.StatusOK || !bytes.Equal(body, page) {
		t.Fatalf("GET /docs = %d %q, want 200 with docs.html", code, body)
	}
}

// A page that is not there is still a 404, and an empty object is still an
// empty 200.
func TestMissingAndEmptyObjects(t *testing.T) {
	st := startStore(t)
	st.put("sites/o/mysite/empty.txt", []byte{})

	if code, _ := serveSite(t, "/nope.html"); code != http.StatusNotFound {
		t.Fatalf("GET /nope.html = %d, want 404", code)
	}
	if code, body := serveSite(t, "/empty.txt"); code != http.StatusOK || len(body) != 0 {
		t.Fatalf("GET /empty.txt = %d %q, want an empty 200", code, body)
	}
}

// A published site is shown in Dev's project panel, a frame on another origin,
// and the edge answers every host under the sites apex with X-Frame-Options:
// SAMEORIGIN, which refused it ("refused to connect"). A frame-ancestors policy
// is what a browser obeys in its place, so every answer names who may frame it —
// the surfaces the brand registry names, and no wildcard — its pages and its
// 404s, on its slug host and on a custom domain alike.
func TestAPublishedSiteMayBeFramedByOurSurfacesAlone(t *testing.T) {
	st := startStore(t)
	st.put("sites/o/mysite/index.html", []byte("<!doctype html><title>mysite</title>"))
	SetResolver(&fakeResolver{found: true, site: Site{Org: "o", Slug: "mysite", Bucket: "sites", Prefix: "o/mysite", Status: "live"}})
	t.Cleanup(func() { SetResolver(nil) })

	policy := framers()
	if !strings.HasPrefix(policy, "frame-ancestors 'self' ") || !strings.Contains(policy+" ", " https://hanzo.app ") {
		t.Fatalf("policy = %q, want frame-ancestors naming hanzo.app", policy)
	}
	if strings.Contains(policy, "*") || strings.Contains(policy, ".hanzo.app") {
		t.Fatalf("policy %q admits a wildcard or a customer's site", policy)
	}
	for _, tc := range []struct {
		name, url string
		app       *zip.App
		code      int
	}{
		{"a page on its slug host", "http://mysite.hanzo.app/", newTestApp(withStore(testServer())), http.StatusOK},
		{"a missing page", "http://mysite.hanzo.app/nope.html", newTestApp(withStore(testServer())), http.StatusNotFound},
		{"a page on a custom domain", "http://mysite.tech/", newTestApp(withStore(selfServer())), 0},
	} {
		resp, err := tc.app.Test(httptest.NewRequest("GET", tc.url, nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		_ = resp.Body.Close()
		if tc.code != 0 && resp.StatusCode != tc.code {
			t.Fatalf("%s = %d, want %d", tc.name, resp.StatusCode, tc.code)
		}
		if resp.Header.Get("X-Hanzo-Site") != "mysite" {
			t.Fatalf("%s was not served as the site", tc.name)
		}
		if got := resp.Header.Get("Content-Security-Policy"); got != policy {
			t.Fatalf("%s answered policy %q, want %q", tc.name, got, policy)
		}
	}
}

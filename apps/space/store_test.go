package space

// store_test.go is the object-store double every test here drives the REAL mount
// against. It is a store and not a stub: it lists honestly, respecting prefix and
// delimiter, because the two derivations under
// test — (org, space) → bucket and drive → first key segment — are only visible
// in WHAT THE STORE WAS ASKED. A double that answered a canned body would prove
// the handler returns something and nothing about where it looked.

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/object"
	"github.com/zap-proto/zip"
)

// call is one question the store was asked. It keeps the SHAPE of the request the
// object store's wire would have carried, because that is how these tests read where
// a handler looked: Path is "/<bucket>/<key>" for an object and "/<bucket>/" for the
// bucket, and Query carries a listing's prefix and delimiter.
type call struct {
	Method string
	Path   string
	Raw    string
	Query  string
}

// entry is one key the store holds.
type entry struct {
	Key  string
	Size int64
}

// store is an object.Store that answers from local disk and writes down every
// question it is asked. It is a store and not a stub: it lists and deletes honestly,
// respecting prefix and delimiter, because the two derivations under test — (org,
// space) -> bucket and drive -> first key segment — are only visible in WHAT THE
// STORE WAS ASKED. A double that answered a canned body would prove the handler
// returns something and nothing about where it looked.
//
// Signing a link is arithmetic and asks the store nothing, so Share is not a call.
type store struct {
	object.Store
	mu      sync.Mutex
	calls   []call
	buckets []string           // buckets that exist before the test begins, physical names
	keys    map[string][]entry // physical bucket -> the keys it holds before the test begins
}

func newStore() *store { return &store{keys: map[string][]entry{}} }

// seed makes the buckets and keys the test declared, on a fresh local disk.
func (s *store) seed(t *testing.T) {
	t.Helper()
	l := &object.Local{Dir: t.TempDir(), Base: func() string { return "https://store.test" }}
	ctx := context.Background()
	for _, b := range s.buckets {
		if err := l.Ensure(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	for b, held := range s.keys {
		if err := l.Ensure(ctx, b); err != nil {
			t.Fatal(err)
		}
		for _, e := range held {
			if _, err := l.Put(ctx, b, e.Key, strings.NewReader(strings.Repeat("x", int(e.Size))), e.Size, object.Put{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.Store = l
}

func (s *store) note(method, path, query string) {
	s.mu.Lock()
	s.calls = append(s.calls, call{Method: method, Path: path, Raw: path, Query: query})
	s.mu.Unlock()
}

func (s *store) seen() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.calls...)
}

// asked returns the calls whose method matches, which is how a test names the one
// question it is about without depending on how many the store was asked around it.
func (s *store) asked(method string) []call {
	var out []call
	for _, c := range s.seen() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (s *store) Ensure(ctx context.Context, b string) error {
	s.note(http.MethodPut, "/"+b+"/", "")
	return s.Store.Ensure(ctx, b)
}

func (s *store) Has(ctx context.Context, b string) (bool, error) {
	s.note(http.MethodHead, "/"+b+"/", "")
	return s.Store.Has(ctx, b)
}

func (s *store) Buckets(ctx context.Context) ([]object.Bucket, error) {
	s.note(http.MethodGet, "/", "")
	return s.Store.Buckets(ctx)
}

func (s *store) Put(ctx context.Context, b, k string, r io.Reader, n int64, o object.Put) (object.Info, error) {
	s.note(http.MethodPut, "/"+b+"/"+k, "")
	return s.Store.Put(ctx, b, k, r, n, o)
}

func (s *store) Remove(ctx context.Context, b, k string) error {
	s.note(http.MethodDelete, "/"+b+"/"+k, "")
	return s.Store.Remove(ctx, b, k)
}

func (s *store) List(ctx context.Context, b string, o object.List) iter.Seq2[object.Info, error] {
	q := url.Values{"prefix": {o.Prefix}}
	if o.Shallow {
		q.Set("delimiter", "/")
	}
	s.note(http.MethodGet, "/"+b+"/", q.Encode())
	return s.Store.List(ctx, b, o)
}

// mount stands up the REAL /v1/space surface — the same Use the binary runs —
// over the store double, with the fee at zero so these tests measure the
// handlers and not the money. The money is measured where it lives:
// apps/s3/billing_test.go drives the SAME fare.Paid preamble end to end.
func mount(t *testing.T, st *store) *zip.App {
	t.Helper()
	st.seed(t)
	was := objects
	objects = func() object.Store { return st }
	t.Cleanup(func() { objects = was })
	t.Setenv(feeEnv, "0")
	t.Setenv("CLOUD_ENV", "mainnet")
	return mountWith(t, cloud.Deps{})
}

// mountWith is mount without the store, for the unconfigured posture.
func mountWith(t *testing.T, deps cloud.Deps) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

// httptestRequest builds one request without the identity headers, for the tests
// that are about a caller who presents none.
func httptestRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// send drives one request as the gateway would present it: a validated principal
// (X-User-Id) acting for an org (X-Org-Id), which is the only shape this surface
// admits.
func send(t *testing.T, app *zip.App, method, path, org string, body string) (int, string) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		out.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, out.String()
}

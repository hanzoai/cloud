package space

// store_test.go is the object-store double every test here drives the REAL mount
// against. It is a store and not a stub: it answers ListBuckets and ListObjects
// honestly, respecting prefix and delimiter, because the two derivations under
// test — (org, space) → bucket and drive → first key segment — are only visible
// in WHAT THE STORE WAS ASKED. A double that answered a canned body would prove
// the handler returns something and nothing about where it looked.

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// call is one request the store received. Path is DECODED and Raw is not, which
// is what lets a test tell "a key containing a separator" from "a key containing
// a percent-encoded separator" — the whole point of decoding at one client.
type call struct {
	Method string
	Path   string
	Raw    string
	Query  string
}

// object is one key the store holds.
type object struct {
	Key  string
	Size int64
}

// store answers the S3 wire for the handful of operations this surface makes.
type store struct {
	mu      sync.Mutex
	calls   []call
	buckets []string            // what ListBuckets answers, physical names
	keys    map[string][]object // physical bucket -> the keys it holds
	fail    map[string]string   // "METHOD /path" -> an S3 error code to answer with
}

func newStore() *store {
	return &store{keys: map[string][]object{}, fail: map[string]string{}}
}

func (s *store) seen() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.calls...)
}

// asked returns the calls whose method matches, which is how a test names the one
// question it is about without depending on how many the client asked around it.
func (s *store) asked(method string) []call {
	var out []call
	for _, c := range s.seen() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (s *store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls = append(s.calls, call{Method: r.Method, Path: r.URL.Path, Raw: r.URL.EscapedPath(), Query: r.URL.RawQuery})
	code := s.fail[r.Method+" "+r.URL.Path]
	s.mu.Unlock()

	if code != "" {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
		return
	}

	seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := seg[0]
	key := ""
	if len(seg) == 2 {
		key = seg[1]
	}

	switch {
	case r.Method == http.MethodGet && bucket == "":
		s.listBuckets(w)
	case r.Method == http.MethodGet && key == "":
		s.listObjects(w, bucket, r.URL.Query().Get("prefix"), r.URL.Query().Get("delimiter"))
	case r.Method == http.MethodHead:
		// BucketExists. A bucket the store does not hold is 404, which is what lets
		// a create succeed and a re-create answer 409.
		if !s.holds(bucket) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		// The store answers a delete with 204 and no body, and the client checks
		// for exactly that — a 200 here is an error, not a lenient success.
		w.WriteHeader(http.StatusNoContent)
	default:
		// MakeBucket, BucketExists, PutObject. The client reads a status and, for
		// a write, an ETag; nothing here needs a body.
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)
	}
}

// holds reports whether the store has this bucket at all, by either of the two
// ways a test can put one there.
func (s *store) holds(bucket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[bucket]; ok {
		return true
	}
	for _, b := range s.buckets {
		if b == bucket {
			return true
		}
	}
	return false
}

func (s *store) listBuckets(w http.ResponseWriter) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	b.WriteString(`<Owner><ID>t</ID><DisplayName>t</DisplayName></Owner><Buckets>`)
	s.mu.Lock()
	names := append([]string(nil), s.buckets...)
	s.mu.Unlock()
	for _, n := range names {
		fmt.Fprintf(&b, `<Bucket><Name>%s</Name><CreationDate>2026-01-02T15:04:05.000Z</CreationDate></Bucket>`, xmlText(n))
	}
	b.WriteString(`</Buckets></ListAllMyBucketsResult>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(b.String()))
}

// listObjects answers a v2 listing the way the store does: keys under prefix,
// and — when a delimiter is asked for — everything sharing a segment folded into
// one common prefix. That folding IS what makes listing the drives the same act
// as listing the root folder, so the double has to do it rather than assert it.
func (s *store) listObjects(w http.ResponseWriter, bucket, prefix, delim string) {
	s.mu.Lock()
	held := append([]object(nil), s.keys[bucket]...)
	s.mu.Unlock()

	var contents []object
	folded := map[string]bool{}
	for _, o := range held {
		if !strings.HasPrefix(o.Key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(o.Key, prefix)
		if delim != "" {
			if i := strings.Index(rest, delim); i >= 0 {
				folded[prefix+rest[:i+len(delim)]] = true
				continue
			}
		}
		contents = append(contents, o)
	}
	var prefixes []string
	for p := range folded {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	sort.Slice(contents, func(i, j int) bool { return contents[i].Key < contents[j].Key })

	var b strings.Builder
	// The client asks for encoding-type=url, so the store answers in it and says
	// so — which is what makes a key carrying a space or a separator survive the
	// round trip, and is the half of the decoding story that happens BELOW this
	// subsystem.
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&b, `<Name>%s</Name><EncodingType>url</EncodingType><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><Delimiter>%s</Delimiter><IsTruncated>false</IsTruncated>`,
		xmlText(bucket), url.QueryEscape(prefix), len(contents)+len(prefixes), url.QueryEscape(delim))
	for _, o := range contents {
		fmt.Fprintf(&b, `<Contents><Key>%s</Key><LastModified>2026-01-02T15:04:05.000Z</LastModified><ETag>&quot;abc&quot;</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`,
			url.QueryEscape(o.Key), o.Size)
	}
	for _, p := range prefixes {
		fmt.Fprintf(&b, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, url.QueryEscape(p))
	}
	b.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(b.String()))
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// mount stands up the REAL /v1/space surface — the same Use the binary runs —
// over the store double, with the fee at zero so these tests measure the
// handlers and not the money. The money is measured where it lives:
// apps/s3/billing_test.go drives the SAME fare.Paid preamble end to end.
func mount(t *testing.T, st *store) *zip.App {
	t.Helper()
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_ADMIN_ENDPOINT", srv.Listener.Addr().String())
	// A host:port public endpoint keeps a presigned URL path-style, so its path is
	// exactly "/<bucket>/<key>" and a test can read the derivation straight off it.
	t.Setenv("S3_PUBLIC_ENDPOINT", srv.Listener.Addr().String())
	t.Setenv("S3_PUBLIC_SECURE", "false")
	t.Setenv(feeEnv, "0")
	return mountWith(t, cloud.Deps{Env: "mainnet"})
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
	resp, err := app.Test(req)
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

package projects

// fakeS3 is a minimal, in-memory S3 backend faithful enough for hanzoai/s3-go to drive
// the projects deploy write-path (ensureBucket → purgePrefix → PutObject) end to
// end WITHOUT a live SeaweedFS/SeaweedFS. It implements exactly the operations
// blob.uploadSite performs — HEAD bucket, PUT ?policy, GET ?list-type=2,
// POST ?delete, and PUT object — and stores objects in a map so a REDEPLOY really
// purges the old prefix and writes the new one. It ignores request signatures
// (auth is out of scope for the write-path test); the security gates that DO
// matter (org 403, hosting 402/503) run before S3 is ever dialed and are tested
// separately. This mirrors the repo convention that live S3 round-trips are proven
// with a controlled backend, not the production store.

import (
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // key: "<bucket>/<key>"
	sizes   map[string]int64  // declared size override for sparse objects
	puts    int
	copies  int
	// failPut makes every object PUT return a fast, non-retryable 403 so the
	// deploy upload path fails deterministically (no network hang) for the
	// "failed deploy is not billed" test.
	failPut atomic.Bool
	// failCopyAfter, when > 0, lets that many server-side COPYs succeed and then
	// fails every later one with a fast 403 — the PARTIAL-COPY simulation the
	// release tests need to prove a half-copied release can never be activated.
	failCopyAfter atomic.Int32
	// mutateOnCopy, when set, rewrites this source key's bytes just before the
	// first COPY reads it, simulating a concurrent writer racing a publish so the
	// conditional (if-match) copy guard can be exercised.
	mutateOnCopy atomic.Value // string
}

// startFakeS3 boots the double and returns its bare host:port (for S3_ADMIN_ENDPOINT).
func startFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: make(map[string][]byte)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	t.Setenv("S3_ADMIN_ENDPOINT", host)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_SECURE", "false")
	t.Setenv("S3_REGION", "us-east-1")
	return f
}

// seed writes an object directly into the double, standing in for the build
// output an agent's code execution already produced inside our object store.
func (f *fakeS3) seed(bucket, key, body string) {
	f.mu.Lock()
	f.objects[bucket+"/"+key] = []byte(body)
	f.mu.Unlock()
}

// seedSparse declares an object of a given SIZE without allocating its bytes.
// The release plane never reads object bodies — it caps on the size the listing
// reports and then hands the bytes to the store to copy — so a sparse object is
// a faithful (and honest) model of a large build output, and lets the byte-cap
// test assert on half a gigabyte without allocating it.
func (f *fakeS3) seedSparse(bucket, key string, size int64) {
	f.mu.Lock()
	f.objects[bucket+"/"+key] = nil
	if f.sizes == nil {
		f.sizes = make(map[string]int64)
	}
	f.sizes[bucket+"/"+key] = size
	f.mu.Unlock()
}

// remove drops one object, standing in for bytes reclaimed out of band — a
// bucket lifecycle rule, an operator purge, or a retention pass.
func (f *fakeS3) remove(bucket, key string) {
	f.mu.Lock()
	delete(f.objects, bucket+"/"+key)
	f.mu.Unlock()
}

// body returns a stored object's bytes (ok=false when absent).
func (f *fakeS3) body(bucket, key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[bucket+"/"+key]
	return string(b), ok
}

// copyCount reports how many server-side COPYs the double has served.
func (f *fakeS3) copyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.copies
}

func (f *fakeS3) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

type lbContent struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type listBucketResult struct {
	XMLName     xml.Name    `xml:"ListBucketResult"`
	Name        string      `xml:"Name"`
	Prefix      string      `xml:"Prefix"`
	KeyCount    int         `xml:"KeyCount"`
	MaxKeys     int         `xml:"MaxKeys"`
	IsTruncated bool        `xml:"IsTruncated"`
	Contents    []lbContent `xml:"Contents"`
}

type deleteReq struct {
	XMLName xml.Name `xml:"Delete"`
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

type deletedResult struct {
	XMLName xml.Name `xml:"DeleteResult"`
	Deleted []struct {
		Key string `xml:"Key"`
	} `xml:"Deleted"`
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, hasKey := strings.Cut(p, "/")
	q := r.URL.Query()

	switch {
	case r.Method == http.MethodHead && !hasKey:
		w.WriteHeader(http.StatusOK) // bucket exists → ensureBucket skips MakeBucket
	// key != "", not hasKey: a bucket URL carries a trailing slash, so hasKey is
	// true with an EMPTY key and a bucket HEAD would otherwise be answered as a
	// missing object — ensureBucket would then re-create the bucket on every call.
	case r.Method == http.MethodHead && key != "":
		f.stat(w, bucket, key)
	case r.Method == http.MethodGet && q.Has("location"):
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
	case r.Method == http.MethodPut && q.Has("policy"):
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent) // SetBucketPolicy
	case r.Method == http.MethodPut && !hasKey:
		w.WriteHeader(http.StatusOK) // MakeBucket (not reached: HEAD reports exists)
	case r.Method == http.MethodGet && q.Get("list-type") == "2":
		f.list(w, bucket, q.Get("prefix"))
	case r.Method == http.MethodPost && q.Has("delete"):
		f.delete(w, bucket, r)
	case r.Method == http.MethodPut && hasKey && r.Header.Get("x-amz-copy-source") != "":
		f.copy(w, bucket, key, r)
	case r.Method == http.MethodPut && hasKey:
		f.put(w, bucket, key, r)
	case r.Method == http.MethodGet && hasKey:
		f.get(w, bucket, key)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// etagOf is the store's content identity for a body — real MD5, exactly as S3
// reports for a single-part PUT. The release plane digests these, so the double
// MUST report honest per-object etags or content-addressing would be untested.
func etagOf(body []byte) string { return fmt.Sprintf("%x", md5.Sum(body)) }

func (f *fakeS3) list(w http.ResponseWriter, bucket, prefix string) {
	f.mu.Lock()
	var keys []string
	want := bucket + "/" + prefix
	for k := range f.objects {
		if strings.HasPrefix(k, want) {
			keys = append(keys, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	bodies := make(map[string][]byte, len(keys))
	sizes := make(map[string]int64, len(keys))
	for _, k := range keys {
		bodies[k] = f.objects[bucket+"/"+k]
		if n, ok := f.sizes[bucket+"/"+k]; ok {
			sizes[k] = n
		} else {
			sizes[k] = int64(len(bodies[k]))
		}
	}
	f.mu.Unlock()
	sort.Strings(keys)
	res := listBucketResult{Name: bucket, Prefix: prefix, KeyCount: len(keys), MaxKeys: 1000, IsTruncated: false}
	for _, k := range keys {
		res.Contents = append(res.Contents, lbContent{
			Key: k, LastModified: time.Now().UTC().Format(time.RFC3339),
			ETag: fmt.Sprintf("%q", etagOf(bodies[k])), Size: sizes[k],
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
}

type copyResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

// copy implements S3 server-side COPY (PUT with x-amz-copy-source), including
// the conditional x-amz-copy-source-if-match precondition the release plane
// relies on to pin each object to the etag its manifest was digested from.
func (f *fakeS3) copy(w http.ResponseWriter, bucket, key string, r *http.Request) {
	src := strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/")
	if n := f.failCopyAfter.Load(); n > 0 {
		f.mu.Lock()
		done := f.copies
		f.mu.Unlock()
		if int32(done) >= n {
			s3Error(w, http.StatusForbidden, "AccessDenied", "denied")
			return
		}
	}
	f.mu.Lock()
	// Simulate a concurrent writer mutating the build output mid-publish.
	if victim, _ := f.mutateOnCopy.Load().(string); victim != "" {
		if _, ok := f.objects[bucket+"/"+victim]; ok {
			f.objects[bucket+"/"+victim] = []byte("MUTATED BY A CONCURRENT WRITER")
			f.mutateOnCopy.Store("")
		}
	}
	body, ok := f.objects[src]
	f.mu.Unlock()
	if !ok {
		s3Error(w, http.StatusNotFound, "NoSuchKey", "no such key")
		return
	}
	if want := strings.Trim(r.Header.Get("x-amz-copy-source-if-match"), `"`); want != "" && want != etagOf(body) {
		s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed", "etag mismatch")
		return
	}
	f.mu.Lock()
	f.objects[bucket+"/"+key] = body
	f.copies++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(copyResult{
		ETag:         fmt.Sprintf("%q", etagOf(body)),
		LastModified: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

func s3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status) // 4xx → hanzoai/s3-go does not retry
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>`+msg+`</Message></Error>`)
}

func (f *fakeS3) delete(w http.ResponseWriter, bucket string, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var dr deleteReq
	_ = xml.Unmarshal(body, &dr)
	var out deletedResult
	f.mu.Lock()
	for _, o := range dr.Objects {
		delete(f.objects, bucket+"/"+o.Key)
		out.Deleted = append(out.Deleted, struct {
			Key string `xml:"Key"`
		}{Key: o.Key})
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(out)
}

func (f *fakeS3) put(w http.ResponseWriter, bucket, key string, r *http.Request) {
	if f.failPut.Load() {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden) // 4xx → hanzoai/s3-go does not retry
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.objects[bucket+"/"+key] = body
	f.puts++
	f.mu.Unlock()
	w.Header().Set("ETag", fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum(body))))
	w.WriteHeader(http.StatusOK)
}

// stat implements S3 HEAD-object, the existence probe activation runs before it
// flips a site's pointer. It must be HONEST about absence — a double that
// answered 200 for every key would make the bytes-gone path untestable, which is
// exactly the path retention creates.
func (f *fakeS3) stat(w http.ResponseWriter, bucket, key string) {
	f.mu.Lock()
	body, ok := f.objects[bucket+"/"+key]
	size, sized := f.sizes[bucket+"/"+key]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound) // a HEAD carries no body; the status IS the answer
		return
	}
	if !sized {
		size = int64(len(body))
	}
	w.Header().Set("ETag", fmt.Sprintf("%q", etagOf(body)))
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3) get(w http.ResponseWriter, bucket, key string) {
	f.mu.Lock()
	body, ok := f.objects[bucket+"/"+key]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(body)
}

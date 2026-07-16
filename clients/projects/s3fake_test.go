package projects

// fakeS3 is a minimal, in-memory S3 backend faithful enough for minio-go to drive
// the projects deploy write-path (ensureBucket → purgePrefix → PutObject) end to
// end WITHOUT a live SeaweedFS/MinIO. It implements exactly the operations
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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // key: "<bucket>/<key>"
	puts    int
	// failPut makes every object PUT return a fast, non-retryable 403 so the
	// deploy upload path fails deterministically (no network hang) for the
	// "failed deploy is not billed" test.
	failPut atomic.Bool
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
	case r.Method == http.MethodPut && hasKey:
		f.put(w, bucket, key, r)
	case r.Method == http.MethodGet && hasKey:
		f.get(w, bucket, key)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (f *fakeS3) list(w http.ResponseWriter, bucket, prefix string) {
	f.mu.Lock()
	var keys []string
	want := bucket + "/" + prefix
	for k := range f.objects {
		if strings.HasPrefix(k, want) {
			keys = append(keys, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	f.mu.Unlock()
	sort.Strings(keys)
	res := listBucketResult{Name: bucket, Prefix: prefix, KeyCount: len(keys), MaxKeys: 1000, IsTruncated: false}
	for _, k := range keys {
		res.Contents = append(res.Contents, lbContent{
			Key: k, LastModified: time.Now().UTC().Format(time.RFC3339),
			ETag: `"x"`, Size: 1,
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
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
		w.WriteHeader(http.StatusForbidden) // 4xx → minio-go does not retry
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

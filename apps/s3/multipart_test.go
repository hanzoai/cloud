package s3_test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// multipart_test.go drives a large-file upload end to end against a store that
// speaks the multipart subset of S3: start, presigned parts PUT straight to the
// store, a resume that reads which parts landed, a completion that refuses a
// gap, and an abandon — all inside the caller's org.

// partStore is an object store holding multipart uploads in memory, keyed by
// physical bucket, so two orgs' uploads never meet.
type partStore struct {
	mu      sync.Mutex
	next    int
	uploads map[string]*upload // id -> upload
	objects map[string][]byte  // bucket/key -> bytes
}

type upload struct {
	bucket, key string
	parts       map[int][]byte
}

func (ps *partStore) serve(w http.ResponseWriter, r *http.Request) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	q := r.URL.Query()
	seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(seg) != 2 {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	bucket, key := seg[0], seg[1]
	id := q.Get("uploadId")
	up := ps.uploads[id]
	if id != "" && (up == nil || up.bucket != bucket || up.key != key) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<Error><Code>NoSuchUpload</Code><Message>no such upload</Message></Error>`)
		return
	}
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		ps.next++
		id := "up-" + strconv.Itoa(ps.next)
		ps.uploads[id] = &upload{bucket: bucket, key: key, parts: map[int][]byte{}}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, bucket, key, id)
	case r.Method == http.MethodPut && id != "":
		n, _ := strconv.Atoi(q.Get("partNumber"))
		body, _ := io.ReadAll(r.Body)
		up.parts[n] = body
		w.Header().Set("ETag", `"`+etag(body)+`"`)
	case r.Method == http.MethodGet && id != "":
		var b strings.Builder
		fmt.Fprintf(&b, `<ListPartsResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId><IsTruncated>false</IsTruncated>`, bucket, key, id)
		var ns []int
		for n := range up.parts {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		for _, n := range ns {
			fmt.Fprintf(&b, `<Part><PartNumber>%d</PartNumber><ETag>"%s"</ETag><Size>%d</Size></Part>`, n, etag(up.parts[n]), len(up.parts[n]))
		}
		b.WriteString(`</ListPartsResult>`)
		fmt.Fprint(w, b.String())
	case r.Method == http.MethodPost && id != "":
		var done struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		_ = xml.NewDecoder(r.Body).Decode(&done)
		var whole []byte
		for _, p := range done.Parts {
			body, ok := up.parts[p.PartNumber]
			if !ok || `"`+etag(body)+`"` != p.ETag && etag(body) != p.ETag {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `<Error><Code>InvalidPart</Code><Message>bad part</Message></Error>`)
				return
			}
			whole = append(whole, body...)
		}
		ps.objects[bucket+"/"+key] = whole
		delete(ps.uploads, id)
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"%s-%d"</ETag></CompleteMultipartUploadResult>`, bucket, key, etag(whole), len(done.Parts))
	case r.Method == http.MethodDelete && id != "":
		delete(ps.uploads, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func etag(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func jsonDo(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
}

func TestALargeFileUploadsInPartsAndResumes(t *testing.T) {
	ps := &partStore{uploads: map[string]*upload{}, objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(ps.serve))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	app := newAppAt(t, addr, addr)

	var started struct {
		Upload   string `json:"upload"`
		Key      string `json:"key"`
		PartSize int64  `json:"partSize"`
	}
	jsonDo(t, do(t, app, http.MethodPost, "/v1/s3/buckets/photos/uploads", "acme", `{"key":"videos/big.bin","type":"video/mp4"}`, false), &started)
	if started.Upload == "" || started.Key != "videos/big.bin" || started.PartSize != 16<<20 {
		t.Fatalf("started = %+v", started)
	}
	base := "/v1/s3/buckets/photos/uploads/" + started.Upload
	var minted struct {
		URLs []struct {
			Part int    `json:"part"`
			URL  string `json:"url"`
		} `json:"urls"`
	}
	jsonDo(t, do(t, app, http.MethodPost, base+"/parts", "acme", `{"key":"videos/big.bin","parts":[1,2,3]}`, false), &minted)
	if len(minted.URLs) != 3 {
		t.Fatalf("minted = %+v", minted)
	}
	parts := map[int][]byte{1: bytes.Repeat([]byte("a"), 100), 2: bytes.Repeat([]byte("b"), 100), 3: []byte("tail")}
	put := func(n int) {
		t.Helper()
		for _, u := range minted.URLs {
			if u.Part != n {
				continue
			}
			if !strings.Contains(u.URL, "partNumber="+strconv.Itoa(n)) || !strings.Contains(u.URL, "uploadId="+started.Upload) || !strings.Contains(u.URL, "X-Amz-Signature=") {
				t.Fatalf("part %d URL is not a presigned part address: %s", n, u.URL)
			}
			req, _ := http.NewRequest(http.MethodPut, u.URL, bytes.NewReader(parts[n]))
			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT part %d: %v %v", n, err, resp)
			}
			resp.Body.Close()
		}
	}
	// The connection drops after parts 1 and 3.
	put(1)
	put(3)
	var stored struct {
		Parts []struct {
			Part int   `json:"part"`
			Size int64 `json:"size"`
		} `json:"parts"`
	}
	jsonDo(t, do(t, app, http.MethodGet, base+"?key=videos/big.bin", "acme", "", false), &stored)
	if len(stored.Parts) != 2 || stored.Parts[0].Part != 1 || stored.Parts[1].Part != 3 {
		t.Fatalf("the resume read = %+v, want parts 1 and 3", stored)
	}
	resp := do(t, app, http.MethodPost, base+"/complete", "acme", `{"key":"videos/big.bin"}`, false)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "part 2 is missing") {
		t.Fatalf("completing with a gap = %d %s, want 409 naming part 2", resp.StatusCode, body)
	}
	put(2)
	var done struct {
		Key   string `json:"key"`
		Size  int64  `json:"size"`
		Parts int    `json:"parts"`
	}
	jsonDo(t, do(t, app, http.MethodPost, base+"/complete", "acme", `{"key":"videos/big.bin"}`, false), &done)
	if done.Size != 204 || done.Parts != 3 {
		t.Fatalf("done = %+v", done)
	}
	var whole []byte
	for b, v := range ps.objects {
		if strings.HasSuffix(b, "/videos/big.bin") {
			whole = v
		}
	}
	if string(whole) != string(parts[1])+string(parts[2])+string(parts[3]) {
		t.Fatalf("the assembled object is %d bytes, not the parts in order", len(whole))
	}
}

func TestAnUploadIsTheOrgsAlone(t *testing.T) {
	ps := &partStore{uploads: map[string]*upload{}, objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(ps.serve))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	app := newAppAt(t, addr, addr)

	var started struct {
		Upload string `json:"upload"`
	}
	jsonDo(t, do(t, app, http.MethodPost, "/v1/s3/buckets/photos/uploads", "acme", `{"key":"a.bin"}`, false), &started)
	base := "/v1/s3/buckets/photos/uploads/" + started.Upload
	// Another org naming the same bucket name and upload id reaches its own bucket,
	// where no such upload exists.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, base + "?key=a.bin", ""},
		{http.MethodPost, base + "/complete", `{"key":"a.bin"}`},
		{http.MethodDelete, base + "?key=a.bin", ""},
	} {
		resp := do(t, app, c.method, c.path, "rival", c.body, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s as another org = %d, want 404", c.method, c.path, resp.StatusCode)
		}
	}
	var gone struct {
		Upload string `json:"upload"`
	}
	jsonDo(t, do(t, app, http.MethodDelete, base+"?key=a.bin", "acme", "", false), &gone)
	resp := do(t, app, http.MethodGet, base+"?key=a.bin", "acme", "", false)
	resp.Body.Close()
	if gone.Upload != started.Upload || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an abandoned upload still answers: %d", resp.StatusCode)
	}
	// And nobody without a principal starts one.
	resp = do(t, app, http.MethodPost, "/v1/s3/buckets/photos/uploads", "", `{"key":"a.bin"}`, false)
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatal("an anonymous caller started an upload")
	}
	for _, body := range []string{`{"key":"../x"}`, `{"key":""}`} {
		resp := do(t, app, http.MethodPost, "/v1/s3/buckets/photos/uploads", "acme", body, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("start %s = %d, want 400", body, resp.StatusCode)
		}
	}
}

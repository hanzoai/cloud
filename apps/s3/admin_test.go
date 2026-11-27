package s3_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// admin_test.go holds who administers an org's buckets, asked through the real
// identity boundary: tokens IAM would sign, verified by cloud.Identify, so the
// admin bit the gate reads is the one production mints.
//
// The tests run in a customer's own org, where members share the org's buckets
// and its admins administer them; TestAHeldOrgsBucketsAreNotItsMembers runs in
// hanzo, a held org, where it was measured: every self-serve signup is a member
// there, so a brand-new customer listed the org's buckets, created and deleted
// them, and read and wrote the objects in them.

// bucketStore is an object store that keeps buckets and objects in memory: just
// the subset of S3 the bucket and object operations here ask for. A request it
// does not model — a conditional write, starting a multipart upload — is 501,
// which the client asks exactly once; an upload id it is asked about is one it
// never issued, so NoSuchUpload.
//
// It also keeps every request it was asked, METHOD and path, so a test can say
// whose namespace an operation reached rather than only what status it answered.
// The host's durability probe is the one writer it answers and does not keep
// (hostProbe): it is not an operation's request.
type bucketStore struct {
	mu      sync.Mutex
	buckets map[string]time.Time
	objects map[string][]byte // bucket/key
	asked   []string
}

// hostProbe is where the host's durability probe writes: internal/host/build.go
// durableBucket ("org-db") and durableProbePrefix (".probe/cas-"). cloud.BuildDeps
// points the host's object store at this fake too, through the same S3_ADMIN_*, and
// the probe runs on the host's clock rather than inside any request: whatever first
// needs the durable store asks it, and while this fake answers its conditional PUT
// 501 it is asked again 1s, 2s, 4s… later, four racing writes each time
// (tenure.ProbeCAS). So those writes land in whichever call is in flight, and a table
// asking whose namespace a CALLER reached read them as the caller's
// (TestNoCallerReachesAnotherOrgsBuckets, forge run 18549, on a commit that touched
// nothing here). They are still answered; they are not recorded. Every other address,
// the durable bucket's own included, still is.
const hostProbe = "/org-db/.probe/"

func newBucketStore(t *testing.T) (*bucketStore, string) {
	t.Helper()
	bs := &bucketStore{buckets: map[string]time.Time{}, objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(bs.serve))
	t.Cleanup(srv.Close)
	return bs, srv.Listener.Addr().String()
}

func (bs *bucketStore) has(bucket string) bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	_, ok := bs.buckets[bucket]
	return ok
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

// contents is a copy of every bucket and object the store holds.
type contents struct {
	buckets map[string]time.Time
	objects map[string][]byte
}

// contents copies what the store holds.
func (bs *bucketStore) contents() contents {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	s := contents{buckets: make(map[string]time.Time, len(bs.buckets)), objects: make(map[string][]byte, len(bs.objects))}
	for k, v := range bs.buckets {
		s.buckets[k] = v
	}
	for k, v := range bs.objects {
		s.objects[k] = bytes.Clone(v)
	}
	return s
}

// hold makes the store hold exactly s and forgets what it was asked.
func (bs *bucketStore) hold(s contents) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.buckets = make(map[string]time.Time, len(s.buckets))
	bs.objects = make(map[string][]byte, len(s.objects))
	for k, v := range s.buckets {
		bs.buckets[k] = v
	}
	for k, v := range s.objects {
		bs.objects[k] = bytes.Clone(v)
	}
	bs.asked = nil
}

// requests answers what the store was asked since it was last asked, or last
// told what to hold, and forgets it.
func (bs *bucketStore) requests() []string {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	out := bs.asked
	bs.asked = nil
	return out
}

func (bs *bucketStore) serve(w http.ResponseWriter, r *http.Request) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, hostProbe) {
		bs.asked = append(bs.asked, r.Method+" "+r.URL.Path)
	}
	if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Match") != "" || r.URL.Query().Has("uploads") {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	if r.URL.Query().Has("uploadId") {
		s3Error(w, http.StatusNotFound, "NoSuchUpload")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	_, exists := bs.buckets[bucket]
	switch {
	case bucket == "" && r.Method == http.MethodGet:
		names := make([]string, 0, len(bs.buckets))
		for n := range bs.buckets {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString(`<ListAllMyBucketsResult><Owner><ID>t</ID></Owner><Buckets>`)
		for _, n := range names {
			fmt.Fprintf(&b, `<Bucket><Name>%s</Name><CreationDate>%s</CreationDate></Bucket>`, n, bs.buckets[n].UTC().Format(time.RFC3339))
		}
		b.WriteString(`</Buckets></ListAllMyBucketsResult>`)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, b.String())
	case key == "" && r.Method == http.MethodHead:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
		}
	case key == "" && r.Method == http.MethodPut:
		bs.buckets[bucket] = time.Now()
	case key == "" && r.Method == http.MethodDelete:
		if !exists {
			s3Error(w, http.StatusNotFound, "NoSuchBucket")
			return
		}
		for k := range bs.objects {
			if strings.HasPrefix(k, bucket+"/") {
				s3Error(w, http.StatusConflict, "BucketNotEmpty")
				return
			}
		}
		delete(bs.buckets, bucket)
		w.WriteHeader(http.StatusNoContent)
	case !exists:
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
	case key == "" && r.Method == http.MethodGet:
		var b strings.Builder
		fmt.Fprintf(&b, `<ListBucketResult><Name>%s</Name><IsTruncated>false</IsTruncated>`, bucket)
		for k, v := range bs.objects {
			if rest, ok := strings.CutPrefix(k, bucket+"/"); ok {
				fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size><ETag>"e"</ETag><LastModified>%s</LastModified></Contents>`, rest, len(v), time.Now().UTC().Format(time.RFC3339))
			}
		}
		b.WriteString(`</ListBucketResult>`)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, b.String())
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		bs.objects[bucket+"/"+key] = body
		w.Header().Set("ETag", `"e"`)
	case r.Method == http.MethodGet:
		body, ok := bs.objects[bucket+"/"+key]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", `"e"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	case r.Method == http.MethodDelete:
		delete(bs.objects, bucket+"/"+key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// heldOrg is the org the harness acts in: a customer's own org by default, and
// hanzo for the held-org test (inHeldOrg).
var heldOrg = "acme"

// inHeldOrg points the harness at hanzo for the rest of t.
func inHeldOrg(t *testing.T) {
	t.Helper()
	was := heldOrg
	heldOrg = "hanzo"
	t.Cleanup(func() { heldOrg = was })
}

type bucketPeople struct {
	app   *zip.App
	store *bucketStore
	// member and other are plain members of hanzo; boss administers it; carol
	// owns carolco and is a plain member of hanzo; root is a SuperAdmin acting
	// in hanzo.
	member, other, boss, carol, root string
}

func bucketsAsPeople(t *testing.T) bucketPeople {
	t.Helper()
	bs, addr := newBucketStore(t)
	iss := iamtest.New(t)
	app := newAppBehind(t, addr, addr, func(app *zip.App) {
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	})
	in := func(org, role string) map[string]any { return map[string]any{"org": org, "role": role} }
	person := func(sub, name, home string, orgs ...map[string]any) string {
		return iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: home, Orgs: orgs})
	}
	return bucketPeople{
		app:    app,
		store:  bs,
		member: person("7a1e0000-0000-4000-8000-0000000000b1", "alice", heldOrg, in(heldOrg, "member")),
		other:  person("7a1e0000-0000-4000-8000-0000000000b2", "bob", heldOrg, in(heldOrg, "member")),
		boss:   person("7a1e0000-0000-4000-8000-0000000000b3", "boss", heldOrg, in(heldOrg, "admin")),
		carol:  person("7a1e0000-0000-4000-8000-0000000000b4", "carol", "carolco", in("carolco", "owner"), in(heldOrg, "member")),
		root:   person("7a1e0000-0000-4000-8000-0000000000b5", "z", "admin", in("admin", "owner")),
	}
}

// as sends one request in hanzo as the bearer and answers its status and body.
func (p bucketPeople) as(t *testing.T, bearer, method, path, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Org-Id", heldOrg)
	resp, err := p.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// byName calls op as MCP's tools/call does, with no route, and reports whether it
// answered rather than refused.
func (p bucketPeople) byName(t *testing.T, bearer, op string, args map[string]any) bool {
	t.Helper()
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": op, "arguments": args}})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Org-Id", heldOrg)
	resp, err := p.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return answered(string(b))
}

func physical(name string) string { return provisioning.BucketName(heldOrg, name) }

// TestAPlainMemberUsesBucketsAndCannotAdministerThem is the capability table for a
// plain member of an org: every object operation, and listing the buckets, as
// before; creating and deleting a bucket refused 403 with nothing done. Each
// refusal is paired with the admission it must not also block.
func TestAPlainMemberUsesBucketsAndCannotAdministerThem(t *testing.T) {
	p := bucketsAsPeople(t)
	if code, b := p.as(t, p.boss, http.MethodPost, "/v1/s3/buckets", `{"name":"shared"}`); code != http.StatusCreated {
		t.Fatalf("an admin could not create a bucket: %d %s", code, b)
	}

	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/v1/s3/buckets", "", http.StatusOK},
		{http.MethodPost, "/v1/s3/buckets/shared/objects", `{"key":"notes/a.txt"}`, http.StatusOK},
		{http.MethodGet, "/v1/s3/buckets/shared/objects/notes/a.txt", "", http.StatusOK},
		{http.MethodDelete, "/v1/s3/buckets/shared/objects/notes/a.txt", "", http.StatusNoContent},
	} {
		if code, b := p.as(t, p.member, c.method, c.path, c.body); code != c.want {
			t.Fatalf("a plain member %s %s = %d %s, want %d", c.method, c.path, code, b, c.want)
		}
	}
	if code, b := p.as(t, p.member, http.MethodGet, "/v1/s3/buckets", ""); !strings.Contains(b, `"shared"`) {
		t.Fatalf("a plain member's listing = %d %s, want the org's bucket in it", code, b)
	}

	for name, bearer := range map[string]string{"a plain member": p.member, "an admin of another org": p.carol} {
		if code, b := p.as(t, bearer, http.MethodPost, "/v1/s3/buckets", `{"name":"mine"}`); code != http.StatusForbidden {
			t.Fatalf("%s created a bucket: %d %s, want 403", name, code, b)
		}
		if p.store.has(physical("mine")) {
			t.Fatalf("a refused create by %s made the bucket", name)
		}
		if code, b := p.as(t, bearer, http.MethodDelete, "/v1/s3/buckets/shared", ""); code != http.StatusForbidden {
			t.Fatalf("%s deleted the org's bucket: %d %s, want 403", name, code, b)
		}
		if !p.store.has(physical("shared")) {
			t.Fatalf("a refused delete by %s removed the bucket", name)
		}
	}
}

// TestAnAdminAdministersTheOrgsBuckets: an admin of the org and a SuperAdmin
// acting in it create and delete buckets.
func TestAnAdminAdministersTheOrgsBuckets(t *testing.T) {
	p := bucketsAsPeople(t)
	for _, who := range []struct{ name, bearer, bucket string }{
		{"an admin of the org", p.boss, "by-boss"},
		{"a SuperAdmin", p.root, "by-root"},
	} {
		if code, b := p.as(t, who.bearer, http.MethodPost, "/v1/s3/buckets", `{"name":"`+who.bucket+`"}`); code != http.StatusCreated {
			t.Fatalf("%s could not create a bucket: %d %s", who.name, code, b)
		}
		if !p.store.has(physical(who.bucket)) {
			t.Fatalf("%s created %s and it is not in hanzo's namespace", who.name, who.bucket)
		}
		if code, b := p.as(t, who.bearer, http.MethodDelete, "/v1/s3/buckets/"+who.bucket, ""); code != http.StatusNoContent {
			t.Fatalf("%s could not delete a bucket: %d %s", who.name, code, b)
		}
		if p.store.has(physical(who.bucket)) {
			t.Fatalf("%s deleted %s and it is still there", who.name, who.bucket)
		}
	}
}

// TestBucketAdministrationIsRefusedByName: MCP's tools/call reaches the op with
// no route, and is refused for a plain member and admitted for an admin exactly
// as REST is.
func TestBucketAdministrationIsRefusedByName(t *testing.T) {
	p := bucketsAsPeople(t)
	if p.byName(t, p.member, "post_s3_buckets", map[string]any{"name": "by-name"}) {
		t.Fatal("a plain member created a bucket by name over MCP")
	}
	if p.store.has(physical("by-name")) {
		t.Fatal("a refused create by name made the bucket")
	}
	if !p.byName(t, p.boss, "post_s3_buckets", map[string]any{"name": "by-name"}) {
		t.Fatal("an admin could not create a bucket by name over MCP")
	}
	if p.byName(t, p.member, "delete_s3_buckets_by_bucket", map[string]any{"bucket": "by-name"}) {
		t.Fatal("a plain member deleted a bucket by name over MCP")
	}
	if !p.store.has(physical("by-name")) {
		t.Fatal("a refused delete by name removed the bucket")
	}
	if !p.byName(t, p.boss, "delete_s3_buckets_by_bucket", map[string]any{"bucket": "by-name"}) {
		t.Fatal("an admin could not delete a bucket by name over MCP")
	}
}

// In a held org (hanzo) the buckets are the platform's own — the CI cache, the
// model weights — and a plain member of it is a customer: it lists no bucket and
// reaches no object, while an admin of the org reaches both.
//
// An admin of another org who also holds a plain membership of hanzo is not
// refused here, because her request is not in hanzo at all. A plain membership of
// an org the platform runs opens none of it, so the boundary discards her
// selection and she acts in her own org (cloud.Acts, which is authz
// Claims.EffectiveOrg). What has to hold is that nothing of hanzo's reaches her:
// the store is never asked about hanzo's namespace on her behalf, and what she is
// answered is her own org's. This test once asked her for a 403, the answer from
// before the boundary stopped admitting that selection (f721eb3fc); the 200 that
// replaced it was her own org's bucket list, with none of hanzo's in it.
// tenancy_test.go asks the same of every address and every kind of caller.
func TestAHeldOrgsBucketsAreNotItsMembers(t *testing.T) {
	inHeldOrg(t)
	p := bucketsAsPeople(t)
	if code, b := p.as(t, p.boss, http.MethodPost, "/v1/s3/buckets", `{"name":"ci-cache"}`); code != http.StatusCreated {
		t.Fatalf("an admin of hanzo could not create a bucket: %d %s", code, b)
	}
	surfaces := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/s3/buckets", ""},
		{http.MethodGet, "/v1/s3/buckets/ci-cache/objects", ""},
		{http.MethodPost, "/v1/s3/buckets/ci-cache/objects", `{"key":"poison.tar"}`},
		{http.MethodGet, "/v1/s3/buckets/ci-cache/objects/poison.tar", ""},
		{http.MethodDelete, "/v1/s3/buckets/ci-cache/objects/poison.tar", ""},
	}
	for _, c := range surfaces {
		if code, b := p.as(t, p.member, c.method, c.path, c.body); code != http.StatusForbidden {
			t.Fatalf("a member of hanzo %s %s = %d %s, want 403", c.method, c.path, code, b)
		}
	}

	hanzo := provisioning.BucketPrefix("hanzo")
	p.store.requests()
	for _, c := range surfaces {
		code, b := p.as(t, p.carol, c.method, c.path, c.body)
		if strings.Contains(b, hanzo) {
			t.Fatalf("an admin of another org %s %s = %d, answered in hanzo's namespace: %s", c.method, c.path, code, b)
		}
		for _, r := range p.store.requests() {
			if strings.Contains(r, hanzo) {
				t.Fatalf("an admin of another org %s %s asked the store %q, in hanzo's namespace", c.method, c.path, r)
			}
		}
	}
	if code, b := p.as(t, p.carol, http.MethodGet, "/v1/s3/buckets", ""); code != http.StatusOK || strings.Contains(b, `"ci-cache"`) {
		t.Fatalf("an admin of another org listed %d %s, want her own org's buckets and not hanzo's", code, b)
	}
	if code, b := p.as(t, p.carol, http.MethodGet, "/v1/s3/buckets/ci-cache/objects", ""); code != http.StatusNotFound {
		t.Fatalf("an admin of another org listed ci-cache: %d %s, want 404 (her own org has no such bucket)", code, b)
	}
	if code, b := p.as(t, p.boss, http.MethodGet, "/v1/s3/buckets/ci-cache/objects", ""); code != http.StatusOK {
		t.Fatalf("an admin of hanzo could not list its objects: %d %s", code, b)
	}
}

// The org's own application makes a bucket — CI makes its cache bucket on first
// use with the client_credentials token it holds — and removes none; a plain
// member makes none. The headers are the ones the identity boundary mints.
func TestTheOrgsApplicationMakesABucketAndRemovesNone(t *testing.T) {
	bs, addr := newBucketStore(t)
	app := newAppBehind(t, addr, addr, func(app *zip.App) { app.Use(cloud.Bridge()) })
	send := func(method, path, body string, hdr map[string]string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	ci := map[string]string{"X-User-Id": "app:acme/acme-ci", "X-User-IsApp": "true"}
	member := map[string]string{"X-User-Id": "u-member"}
	if code := send(http.MethodPost, "/v1/s3/buckets", `{"name":"by-member"}`, member); code != http.StatusForbidden {
		t.Fatalf("a member made a bucket: %d", code)
	}
	if code := send(http.MethodPost, "/v1/s3/buckets", `{"name":"ci-cache"}`, ci); code != http.StatusCreated {
		t.Fatalf("the org's application could not make its bucket: %d", code)
	}
	if code := send(http.MethodDelete, "/v1/s3/buckets/ci-cache", "", ci); code != http.StatusForbidden {
		t.Fatalf("the org's application removed a bucket: %d", code)
	}
	if !bs.has(provisioning.BucketName("acme", "ci-cache")) {
		t.Fatal("the bucket the application made is gone")
	}
}

// The fake keeps what an operation asked, so the host's own durability probe, which
// runs on the host's clock against the same store, is answered as before (a
// conditional PUT, 501) and not kept; the durable bucket's other addresses and every
// tenant address still are.
func TestTheStoreKeepsOperationsNotTheHostsProbe(t *testing.T) {
	bs, _ := newBucketStore(t)
	probe := httptest.NewRequest(http.MethodPut, hostProbe+"cas-324742e3b2c2912fdc23a180b1074b57", strings.NewReader("p"))
	probe.Header.Set("If-None-Match", "*")
	w := httptest.NewRecorder()
	bs.serve(w, probe)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("the probe's conditional write = %d, want 501 as before", w.Code)
	}
	bs.serve(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/org-db/orgs/acme/store.db", strings.NewReader("x")))
	bs.serve(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	got, want := bs.requests(), []string{"PUT /org-db/orgs/acme/store.db", "GET /"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the store kept %q, want %q", got, want)
	}
}

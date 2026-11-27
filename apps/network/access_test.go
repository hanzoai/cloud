package network

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/internal/basetest"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/plane"
	// devmaster keys this test binary: cek opens nothing without a master and a test
	// process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// lockedBuffer is a log sink a test reads while the server writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// intake is the anonymous intake and the invite admin mounted on the hanzo brand
// over a real Base (internal/basetest), an in-memory KMS and a real audit trail, with every count and
// the invite key forgotten before and after.
type intake struct {
	app   *zip.App
	svc   *cloud.Service[state]
	kms   *kmstest.Mem
	trail *audit.Recorder
	logs  *lockedBuffer
}

// mountIntake mounts routes() as production does, the admin group behind its gate, on a
// deployment that has issued before: the invite key is sealed in KMS.
func mountIntake(t *testing.T) *intake {
	t.Helper()
	in := mountUnsealed(t)
	in.key(t)
	return in
}

// mountUnsealed mounts it on a deployment whose KMS holds no invite key yet: nothing has
// been issued.
func mountUnsealed(t *testing.T) *intake {
	t.Helper()
	return mountIntakeWith(t, func(app *zip.App, s *cloud.Service[state]) { routes(app, s) })
}

// mountIntakeWith mounts it with the given registration, for a test of the ops
// with no route gate in front of them (the call plane and MCP invoke an op that way).
func mountIntakeWith(t *testing.T, register func(*zip.App, *cloud.Service[state])) *intake {
	t.Helper()
	return mountOn(t, register, basetest.Serve)
}

// mountOn mounts it over the base peer that serve stands up: the real one, or a test's
// own where a Base that fails is the point.
func mountOn(t *testing.T, register func(*zip.App, *cloud.Service[state]), serve func(*testing.T)) *intake {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	serve(t)
	t.Setenv("CLOUD_BRAND", "hanzo")

	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })

	logs := &lockedBuffer{}
	log := luxlog.NewWriter(logs)
	k := kmstest.New()
	c := coord.Alone() // every test counts in a coordinator of its own
	st := newState(newClient(log), c)
	st.trail = trail
	s := &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "network"), State: st}
	s.Log, s.KMS, s.Coord = log, k, c

	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	compose(app)
	register(app, s)
	return &intake{app: app, svc: s, kms: k, trail: trail, logs: logs}
}

// fill puts n charges on the window name under key, the way the ops charge it, so a
// test reaches a limit without sending every request that would. It answers what the
// window has left.
func (in *intake) fill(t *testing.T, name, key string, n, limit int, every time.Duration) int {
	t.Helper()
	left := limit
	for range n {
		left, _ = in.svc.Coord.Spend(context.Background(), name, []string{key}, []int{1}, limit, every)
	}
	return left
}

// answer is one response: its status, its content type, its body and the headers
// that say anything (all but the ones that differ for every request).
type answer struct {
	code int
	kind string
	body string
	head string
}

// caller is one principal as the identity boundary mints it. The zero value is a
// stranger with no credential.
type caller struct {
	org, user string
	orgAdmin  bool // IAM's admin bit of its own org
	super     bool // platform sudo
}

var (
	visitor  = caller{}
	ordinary = caller{org: "hanzo", user: "hanzo/dave"}
	chief    = caller{org: "hanzo", user: "hanzo/erin", orgAdmin: true}
	operator = caller{org: "admin", user: "admin/z", super: true}
)

// send issues one request as who, from the address ip as the edge would hand it
// on (none: an in-cluster caller). A stranger carries a forged org header the ops
// must not read.
func (in *intake) send(t *testing.T, method, path, body string, who caller, ip string) answer {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if who.user == "" {
		req.Header.Set("X-Org-Id", "mallory")
	} else {
		req.Header.Set("X-User-Id", who.user)
		req.Header.Set("X-Org-Id", who.org)
		req.Header.Set("X-User-Owner", who.org)
		if who.orgAdmin {
			req.Header.Set("X-User-IsOrgAdmin", "true")
		}
		if who.super {
			req.Header.Set("X-User-IsAdmin", "true")
		}
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := in.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header.Get("Content-Type"), string(out), headers(resp.Header)}
}

// headers is a response's headers as one string, sorted, without the ones that are
// new with every request.
func headers(h http.Header) string {
	var lines []string
	for k, v := range h {
		switch strings.ToLower(k) {
		case "date", "x-request-id", "x-trace-id", "traceparent", "content-length":
			continue
		}
		lines = append(lines, k+": "+strings.Join(v, ","))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// verify asks whether code is a live invite, from ip.
func (in *intake) verify(t *testing.T, code, ip string) answer {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"code": code})
	return in.send(t, http.MethodPost, "/v1/network/invite/verify", string(b), visitor, ip)
}

// problem reports whether a is an RFC 9457 problem document for code.
func problem(a answer, code int) bool {
	var p struct {
		Status int    `json:"status"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	return a.code == code && strings.HasPrefix(a.kind, "application/problem+json") &&
		json.Unmarshal([]byte(a.body), &p) == nil && p.Status == code && p.Detail != ""
}

// key is the invite key the deployment holds, sealed on first use if need be.
func (in *intake) key(t *testing.T) []byte {
	t.Helper()
	k, err := in.svc.State.seal.Get(context.Background(), in.kms, true)
	if err != nil {
		t.Fatalf("the invite key: %v", err)
	}
	return k
}

// seed files an invite for code directly in the brand's Base, in the stored shape
// the design fixes: the id is the first 15 hex characters of the code's
// HMAC-SHA-256 under the deployment key, and the record holds that HMAC and never
// the code. edit changes the row before it is written.
func (in *intake) seed(t *testing.T, code string, edit func(*map[string]any)) string {
	t.Helper()
	if !codeShape.MatchString(code) || strings.ContainsAny(code[4:], "ILOU") {
		t.Fatalf("seed %q: a seed is a canonical code, the one spelling the HMAC is taken over", code)
	}
	m := hmac.New(sha256.New, in.key(t))
	m.Write([]byte(code))
	sum := hex.EncodeToString(m.Sum(nil))
	now := time.Now().UTC()
	row := map[string]any{
		"id": sum[:15], "code_hmac": sum, "status": "active", "note": "seeded", "created_by": "admin/z",
		"issued_at": now.Format(baseDate), "expires_at": now.Add(24 * time.Hour).Format(baseDate),
	}
	if edit != nil {
		edit(&row)
	}
	in.put(t, invitesName, row)
	return sum[:15]
}

// put writes one record straight into the brand's Base, growing it first.
func (in *intake) put(t *testing.T, collection string, rec map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(rec)
	ctx := cloud.For(context.Background(), "hanzo")
	if err := (ops{s: in.svc}).grow(ctx, "hanzo"); err != nil {
		t.Fatalf("grow the Base: %v", err)
	}
	if _, err := basepeer.BaseUpsert(plane.For(ctx, "hanzo"), &api.BaseUpsertIn{Collection: collection, Source: "application", Records: []json.RawMessage{raw}}); err != nil {
		t.Fatalf("put into %s: %v", collection, err)
	}
}

// records reads every record of one collection of org's Base, as the plane
// answers them, or nil when the org's Base holds none of that name.
func records(t *testing.T, org, collection string) []map[string]any {
	t.Helper()
	page, err := basepeer.BaseList(plane.For(cloud.For(context.Background(), org), org), &api.BaseListIn{Collection: collection, PerPage: 1000})
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(page.Items))
	for _, raw := range page.Items {
		var r map[string]any
		_ = json.Unmarshal(raw, &r)
		out = append(out, r)
	}
	return out
}

// trailOf is every record of action with this result, in chain order.
func (in *intake) trailOf(t *testing.T, action, result string) []audit.Record {
	t.Helper()
	rows, _, err := in.trail.Query(context.Background(), audit.Filter{Action: action})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	var out []audit.Record
	for _, r := range rows {
		if r.Outcome.Result == result {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// remove deletes one record straight out of the brand's Base.
func (in *intake) remove(t *testing.T, collection, id string) {
	t.Helper()
	ctx := cloud.For(context.Background(), "hanzo")
	if _, err := basepeer.BaseDelete(plane.For(ctx, "hanzo"), &api.BaseDeleteIn{Collection: collection, IDs: []string{id}, Source: "application"}); err != nil {
		t.Fatalf("delete %s from %s: %v", id, collection, err)
	}
}

// pod is a second pod of the same deployment: its own process state (its own key holder),
// the one KMS, the one coordinator, the one audit trail and the one Base.
func (in *intake) pod(t *testing.T) *intake {
	t.Helper()
	st := newState(newClient(in.svc.Log), in.svc.Coord)
	st.trail = in.trail
	s := &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "network"), State: st}
	s.Log, s.KMS, s.Coord = in.svc.Log, in.kms, in.svc.Coord
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	compose(app)
	routes(app, s)
	return &intake{app: app, svc: s, kms: in.kms, trail: in.trail, logs: in.logs}
}

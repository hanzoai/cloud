package principals

// harness_test.go stands up what production runs, in one process: company's
// formations and founder checks, tax's W-9s and W-8s with legal's e-signature,
// wallet's addresses, agents, KMS, and /v1/principal — every owner answering on the real
// plane, which it reaches by name exactly as it does across sockets. The only
// thing not real is where the sanctions lists come from: a test cannot fetch the
// Treasury's file, so it hands luxfi/aml's store entries in the published shape.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/principal"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/company"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/legal"
	"github.com/hanzoai/cloud/apps/tax"
	"github.com/hanzoai/cloud/apps/wallet"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// designated is what the fixture lists designate, beyond one placeholder per
// publisher: a test adds an address to it and reloads.
type designated struct {
	mu        sync.Mutex
	addresses []string
}

var fixture designated

// designations answers the four publishers' entries: a designated entity and a
// designated person on OFAC's list, the addresses a test designated, and one
// placeholder on each of the others so every list is loaded and fresh.
func designations(context.Context) []ScreenResult {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	name := func(list, ref, kind, full string) ScreenEntry {
		return ScreenEntry{List: list, RefID: ref, Group: ref, Kind: kind,
			Names: []ScreenName{{Full: full, Type: NamePrimary, Strong: true}}}
	}
	ofac := []ScreenEntry{
		name(ListOFAC, "9001", KindEntity, "Sinister Shipping Company"),
		name(ListOFAC, "9002", KindIndividual, "Viktor Blackwood"),
	}
	for i, a := range fixture.addresses {
		e := name(ListOFAC, "95"+string(rune('0'+i)), KindEntity, "Dark Wallet Group")
		e.Documents = []ScreenDocument{{Kind: DocAddress, Number: a, Chain: "ETH"}}
		ofac = append(ofac, e)
	}
	return []ScreenResult{
		{Source: ListOFAC, Entries: ofac, Digest: "ofac"},
		{Source: ListUN, Entries: []ScreenEntry{name(ListUN, "QDi.1", KindIndividual, "Placeholder Nations Subject")}, Digest: "un"},
		{Source: ListEU, Entries: []ScreenEntry{name(ListEU, "EU.1", KindEntity, "Placeholder Union Holdings")}, Digest: "eu"},
		{Source: ListOFSI, Entries: []ScreenEntry{name(ListOFSI, "RUS0001", KindIndividual, "Placeholder Kingdom Subject")}, Digest: "ofsi"},
	}
}

type env struct {
	t   *testing.T
	app *zip.App
	rec *audit.Recorder
	dir string
}

// who is the caller of one request.
type who struct {
	org   string
	admin bool // an admin of its own org
	super bool // SuperAdmin: owner == "admin"
}

func admin(org string) who    { return who{org: org, admin: true} }
func member(org string) who   { return who{org: org} }
func reviewer(org string) who { return who{org: org, super: true} }

func newEnv(t *testing.T) *env {
	t.Helper()
	sock, err := os.MkdirTemp("", "principal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })
	t.Setenv("ZIP_RUNTIME_DIR", sock)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	// The event plane tax reads payments from, held in memory as tax's own tests
	// hold it: its store is a warehouse and a bus this process does not have.
	planetest.Events(t)

	fixture = designated{}
	was := published
	published = designations
	t.Cleanup(func() { published = was })

	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	kmsc, err := kms.New(kms.Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(master)}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	deps := cloud.Deps{KMS: kmsc, Audit: rec, MasterKey: master}
	// The principals' store is the one this suite puts on the leg's engine, so it alone is
	// mounted with the DSN the leg set; the neighbours it composes (the wallet's KMS, for
	// one) are not ported yet and open as they do where no DSN is set.
	dsn := os.Getenv(locus.Env)
	t.Setenv(locus.Env, "")
	for _, m := range []struct {
		name string
		use  func(cloud.Router, cloud.Deps) error
	}{
		{"wallet", wallet.Use}, {"legal", legal.Use}, {"company", company.Use}, {"tax", tax.Use},
		{"agent", agents.Use}, {"principal", Use},
	} {
		if m.name == "principal" {
			t.Setenv(locus.Env, dsn)
		}
		err := m.use(app, deps)
		if m.name == "principal" {
			t.Setenv(locus.Env, "")
		}
		if err != nil {
			t.Fatalf("%s.Use: %v", m.name, err)
		}
	}
	for _, name := range []string{"wallet", "legal", "company", "tax", "agent", "principal"} {
		stop, err := cloud.ServePlane(name, luxlog.New("test"))
		if err != nil {
			t.Fatalf("plane %s: %v", name, err)
		}
		t.Cleanup(func() { _ = stop() })
	}
	t.Cleanup(func() {
		_ = Shutdown()
		_ = tax.Shutdown()
		_ = agents.Shutdown(context.Background())
		_ = company.Shutdown(context.Background())
		_ = legal.Shutdown()
		_ = wallet.Shutdown()
		_ = kmsc.Close()
		_ = rec.Close()
	})
	e := &env{t: t, app: app, rec: rec, dir: dir}
	e.fresh()
	return e
}

// fresh waits for the lists to load: the refresh runs on its own goroutine, as it
// does in production.
func (e *env) fresh() {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for mounted.State.lists.fit() != nil {
		if time.Now().After(deadline) {
			e.t.Fatalf("the lists never loaded: %v", mounted.State.lists.fit())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// designate adds an address to OFAC's list and reloads the lists.
func (e *env) designate(address string) {
	fixture.mu.Lock()
	fixture.addresses = append(fixture.addresses, address)
	fixture.mu.Unlock()
	mounted.State.lists.load()
}

// raw drives one request as w and answers the status and body.
func (e *env) raw(method, path string, w who, body any) (int, []byte) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if w.org != "" {
		rq.Header.Set("X-Org-Id", w.org)
		rq.Header.Set("X-User-Id", "u_"+w.org)
		rq.Header.Set("X-User-Name", "admin-"+w.org)
		rq.Header.Set("X-User-Email", "admin@"+w.org+".test")
		rq.Header.Set("X-User-Owner", w.org)
	}
	if w.admin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if w.super {
		rq.Header.Set("X-User-IsAdmin", "true")
		rq.Header.Set("X-User-Owner", "admin")
		rq.Header.Set("X-User-Id", "reviewer")
	}
	resp, err := e.app.Test(rq, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// must drives a request, failing the test on any status but want.
func (e *env) must(want int, method, path string, w who, body, out any) {
	e.t.Helper()
	code, b := e.raw(method, path, w, body)
	if code != want {
		e.t.Fatalf("%s %s as %+v = %d, want %d: %s", method, path, w, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// form forms org as a Delaware company under name, as the org's own admin (the
// identity apps/company admits), with founders verified by a platform reviewer.
func (e *env) form(org, name string, founders ...string) {
	e.t.Helper()
	e.must(201, http.MethodPost, "/v1/company", admin(org), map[string]any{"structure": "c-corp", "jurisdiction": "DE", "name": name}, nil)
	e.must(200, http.MethodPost, "/v1/company/advance", admin(org), map[string]any{"to": "founders"}, nil)
	var fs []map[string]any
	share := 10000 / len(founders)
	for i, f := range founders {
		bps := share
		if i == 0 {
			bps = 10000 - share*(len(founders)-1)
		}
		fs = append(fs, map[string]any{"name": f, "email": strings.ToLower(strings.Fields(f)[0]) + "@" + org + ".test", "equityBps": bps})
	}
	e.must(200, http.MethodPost, "/v1/company/founders", admin(org), map[string]any{"founders": fs}, nil)
	e.must(200, http.MethodPost, "/v1/company/kyc", admin(org), nil, nil)
	for _, f := range fs {
		e.must(200, http.MethodPost, "/v1/company/kyc/decision", reviewer(org), map[string]any{"email": f["email"], "status": "reviewer_confirmed"}, nil)
	}
}

// w9 writes org's W-9.
func (e *env) w9(org string, cls tax.Classification, typ tax.TINType, tin string) {
	e.t.Helper()
	e.must(200, http.MethodPut, "/v1/tax/profile", admin(org), map[string]any{
		"name": org + " Inc", "classification": cls, "tin": tin, "tinType": typ,
		"address": map[string]any{"line1": "1 Market St", "city": "San Francisco", "state": "CA", "zip": "94105"},
	}, nil)
}

// certify signs org's form end to end through legal.
func (e *env) certify(org string) {
	e.t.Helper()
	var p tax.Profile
	e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin(org), nil, &p)
	e.must(200, http.MethodPost, "/v1/legal/documents/"+p.Certification.Document+"/sign/complete", admin(org), map[string]any{"signed": true}, nil)
	e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin(org), nil, &p)
}

// grant has payer ask payee for its form and payee grant it.
func (e *env) grant(payer, payee string) {
	e.t.Helper()
	var w tax.W9
	e.must(201, http.MethodPost, "/v1/tax/w9", admin(payer), map[string]any{"payee": payee}, &w)
	e.must(200, http.MethodPost, "/v1/tax/w9/"+w.ID+"/grant", admin(payee), nil, nil)
}

// clear asks for a clearance as a member of payer.
func (e *env) clear(payer string, body map[string]any) principal.Clearance {
	e.t.Helper()
	var c principal.Clearance
	e.must(201, http.MethodPost, "/v1/principal/clearance", member(payer), body, &c)
	return c
}

// actions are the principal audit actions recorded for org, oldest first.
func (e *env) actions(org string) []string {
	e.t.Helper()
	rows, _, err := e.rec.Query(context.Background(), audit.Filter{Org: org, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.HasPrefix(rows[i].Action, "principal.") {
			out = append(out, rows[i].Action)
		}
	}
	return out
}

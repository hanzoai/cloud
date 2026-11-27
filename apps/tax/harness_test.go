package tax

// harness_test.go stands up what production runs, in one process: the real
// x402 rail with its real settlement store and a real co-resident ledger, real
// wallets for the payees, legal's real e-signature, and tax — the rail, legal and
// the event plane answering on the real plane, which tax reaches by name exactly
// as it does across sockets. A payment in a test here is an EIP-3009
// authorization signed, verified, and settled between two orgs' balances, and
// stated by the rail as an economic event.
//
// The event plane is the one thing held in memory (planetest.Events): its store
// is a warehouse and a bus this process does not have.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/legal"
	"github.com/hanzoai/cloud/apps/wallet"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/plane"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ok answers every commerce call 200, so the metering client reports itself
// enabled; with the ledger co-resident it posts natively and never calls this.
type ok struct{}

func (ok) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
}

// prices is the marketplace's price table, mutable per test.
type prices struct {
	mu sync.Mutex
	m  map[string]x402.Terms
}

func (p *prices) Price(_ context.Context, resource string) (x402.Terms, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.m[resource]
	return t, ok, nil
}

type env struct {
	t      *testing.T
	app    *zip.App
	events *planetest.Economic
	rec    *audit.Recorder
	logs   *syncBuf
	dir    string
	ledger money.FinanceClient
	prices *prices
	keys   map[string]*ecdsa.PrivateKey
	wallet map[string]string
	n      int
}

// who is the caller of one request.
type who struct {
	org   string
	admin bool // an admin of its own org
	super bool // SuperAdmin: owner == "admin"
}

func admin(org string) who  { return who{org: org, admin: true} }
func member(org string) who { return who{org: org} }

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("CLOUD_X402_ASSET", testAsset)
	logs := &syncBuf{}
	prev := luxlog.Default()
	luxlog.SetDefault(luxlog.NewWriter(logs))
	t.Cleanup(func() { luxlog.SetDefault(prev) })

	sock, err := os.MkdirTemp("", "tax")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })
	t.Setenv("ZIP_RUNTIME_DIR", sock)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	ledger := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(ledger)
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	kmsc, err := kms.New(kms.Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(master)}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms: %v", err)
	}
	gauge, err := meter.New(meter.Config{BaseURL: "http://commerce.test", HTTPClient: ok{}})
	if err != nil {
		t.Fatalf("metering: %v", err)
	}

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge()) // the composer's, installed once at the root
	ev := planetest.Events(t)
	deps := cloud.Deps{KMS: kmsc, Audit: rec, Metering: gauge, MasterKey: master}
	for name, use := range map[string]func(cloud.Router, cloud.Deps) error{
		"wallet": wallet.Use, "x402": x402.Use, "legal": legal.Use, "tax": Use,
	} {
		if err := use(app, deps); err != nil {
			t.Fatalf("%s.Use: %v", name, err)
		}
	}
	p := &prices{m: map[string]x402.Terms{}}
	x402.Publish(p)
	app.Group("/paid", x402.Enforce()).Raw(http.MethodGet, "/*", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]any{"served": true})
	})
	for _, name := range []string{"x402", "legal", "tax"} {
		stop, err := cloud.ServePlane(name, luxlog.New("test"))
		if err != nil {
			t.Fatalf("plane %s: %v", name, err)
		}
		t.Cleanup(func() { _ = stop() })
	}
	t.Cleanup(func() {
		_ = Shutdown()
		_ = legal.Shutdown()
		_ = x402.Shutdown()
		_ = wallet.Shutdown()
		_ = kmsc.Close()
		_ = rec.Close()
		x402.Publish(nil)
		finance.Publish(nil)
	})
	return &env{t: t, app: app, events: ev, rec: rec, logs: logs, dir: dir, ledger: ledger, prices: p,
		keys: map[string]*ecdsa.PrivateKey{}, wallet: map[string]string{}}
}

// raw drives one request as w and answers the status, body and headers.
func (e *env) raw(method, path string, w who, body any, hdr map[string]string) (int, []byte, http.Header) {
	e.t.Helper()
	if strings.HasPrefix(path, "/v1/wallet") {
		// A wallet's key is sealed in the org's KMS store, which is ported to hanzo-sql by
		// its own commit and opens as a file until then. The tax store, which these tests
		// are for, is not opened by a wallet.
		dsn := os.Getenv(locus.Env)
		e.t.Setenv(locus.Env, "")
		defer e.t.Setenv(locus.Env, dsn)
	}
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
	}
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := e.app.Test(rq, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// do is raw, decoding a JSON answer into out when there is one.
func (e *env) do(method, path string, w who, body, out any) int {
	e.t.Helper()
	code, b, _ := e.raw(method, path, w, body, nil)
	if out != nil && code < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
	return code
}

// must is do, failing the test on any status but want.
func (e *env) must(want int, method, path string, w who, body, out any) {
	e.t.Helper()
	code, b, _ := e.raw(method, path, w, body, nil)
	if code != want {
		e.t.Fatalf("%s %s as %+v = %d, want %d: %s", method, path, w, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// payTo settles one real x402 payment of cents from payer's balance to payee's:
// a priced resource, a 402 challenge, an EIP-3009 authorization signed by the
// payer's key, and the settlement that debits one org's ledger and credits the
// other's.
func (e *env) payTo(payer, payee string, cents int64) {
	e.t.Helper()
	e.fund(payer)
	wid := e.recipient(payee)
	e.n++
	path := fmt.Sprintf("/paid/%s/%s/%d", payer, payee, e.n)
	e.prices.mu.Lock()
	e.prices.m[path] = x402.Terms{Amount: money.FromCents(cents), RecipientOrg: payee, RecipientWalletID: wid}
	e.prices.mu.Unlock()

	code, b, hdr := e.raw(http.MethodGet, path, who{org: payer}, nil, nil)
	if code != http.StatusPaymentRequired {
		e.t.Fatalf("challenge %s = %d: %s", path, code, b)
	}
	var challenge protocol.PaymentRequired
	if err := protocol.DecodeHeader(hdr.Get(protocol.HeaderPaymentRequired), &challenge); err != nil || len(challenge.Accepts) != 1 {
		e.t.Fatalf("challenge: %v %+v", err, challenge)
	}
	var nonce [32]byte
	_, _ = rand.Read(nonce[:])
	now := time.Now().Unix()
	payment, err := protocol.Sign(challenge.Accepts[0], e.keys[payer], nonce, uint64(now-60), uint64(now+300))
	if err != nil {
		e.t.Fatalf("sign: %v", err)
	}
	signed, err := protocol.EncodeHeader(payment)
	if err != nil {
		e.t.Fatalf("encode: %v", err)
	}
	code, b, _ = e.raw(http.MethodGet, path, who{org: payer}, nil,
		map[string]string{protocol.HeaderPaymentSignature: signed})
	if code != http.StatusOK {
		e.t.Fatalf("paid %s = %d: %s", path, code, b)
	}
	// The settlement owes its statement; the rail's drainer states it off the
	// request's path, and a pass run here is that drainer, run now.
	if _, err := x402.Announce(context.Background()); err != nil {
		e.t.Fatalf("state the settlement: %v", err)
	}
}

// fund gives payer a key and a balance, once.
func (e *env) fund(org string) {
	e.t.Helper()
	if e.keys[org] != nil {
		return
	}
	k, err := crypto.GenerateKey()
	if err != nil {
		e.t.Fatal(err)
	}
	e.keys[org] = k
	if _, err := e.ledger.Deposit(context.Background(), money.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(10_000_000), Currency: "usd", Ref: "fund-" + org,
	}); err != nil {
		e.t.Fatalf("fund %s: %v", org, err)
	}
}

// recipient provisions payee's payout wallet, once.
func (e *env) recipient(org string) string {
	e.t.Helper()
	if id := e.wallet[org]; id != "" {
		return id
	}
	var acct, w struct {
		ID string `json:"id"`
	}
	e.must(200, http.MethodPost, "/v1/wallet/accounts", who{org: org}, map[string]any{"name": "payee"}, &acct)
	e.must(200, http.MethodPost, "/v1/wallet", who{org: org}, map[string]any{
		"accountId": acct.ID, "name": "earnings", "custody": "kms", "tier": "hot", "chain": "eip155:36963"}, &w)
	e.wallet[org] = w.ID
	return w.ID
}

// profile writes org's W-9.
func (e *env) profile(org string, cls Classification, typ TINType, tin string, electronic bool) {
	e.t.Helper()
	e.must(200, http.MethodPut, "/v1/tax/profile", admin(org), map[string]any{
		"name": strings.ToUpper(org[:1]) + org[1:] + " Holdings", "classification": cls,
		"address": map[string]any{"line1": "1 Market St", "city": "San Francisco", "state": "CA", "zip": "94105"},
		"phone":   "415-555-0100", "tin": tin, "tinType": typ, "electronicConsent": electronic,
	}, nil)
}

// certify signs org's W-9 end to end: open the signature, complete it in legal,
// and record it.
func (e *env) certify(org string) {
	e.t.Helper()
	var p Profile
	e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin(org), nil, &p)
	if p.Certification.Status != certPending || p.Certification.Document == "" {
		e.t.Fatalf("certify opened %+v", p.Certification)
	}
	e.must(200, http.MethodPost, "/v1/legal/documents/"+p.Certification.Document+"/sign/complete", admin(org),
		map[string]any{"signed": true}, nil)
	e.must(200, http.MethodPost, "/v1/tax/profile/certify", admin(org), nil, &p)
	if p.Certification.Status != certCertified {
		e.t.Fatalf("certify recorded %+v", p.Certification)
	}
}

// share has payer ask payee for its W-9 and payee grant it.
func (e *env) share(payer, payee string, cat Category) string {
	e.t.Helper()
	var w W9
	e.must(201, http.MethodPost, "/v1/tax/w9", admin(payer), map[string]any{"payee": payee, "category": cat}, &w)
	e.must(200, http.MethodPost, "/v1/tax/w9/"+w.ID+"/grant", admin(payee), nil, nil)
	return w.ID
}

// actions are the audit actions recorded in org, oldest first.
func (e *env) actions(org string) []string {
	e.t.Helper()
	rows, _, err := e.rec.Query(context.Background(), audit.Filter{Org: org, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.HasPrefix(rows[i].Action, "tax.") {
			out = append(out, rows[i].Action)
		}
	}
	return out
}

// state records one payment on the event plane as the rail that moved it would
// have stated it — for the rails no app in this fleet runs.
func (e *env) state(ev client.EconomicEvent) string {
	e.t.Helper()
	return e.events.Put(ev)
}

// testAsset is the EIP-3009 token the challenge names in these tests: a payer signs over
// its address, so a rail with none set is one no payer can pay. The tests pin no real
// token; this is a made-up stand-in for the operator's CLOUD_X402_ASSET.
const testAsset = "0x1111111111111111111111111111111111111111"

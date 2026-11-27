package marketplace

// harness_test.go stands up the market as the fleet runs it, in one process: the
// wallets, x402 on a co-resident ledger, agents, the tool plane and the
// marketplace, each answering on the real plane, which the marketplace reaches by
// name exactly as it does across sockets. Principal, tax and projects are
// stand-ins that answer what a test tells them to — their own decisions are
// proved in their own packages; here what matters is that the market asks, as
// whom, and obeys the answer.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/tools"
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
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/cloud/sdk/secret"
)

// refusingDoer fails every outbound commerce call. Nothing here may leave the
// process: with a co-resident ledger the metering spine posts the payer debit
// natively, so any HTTP attempt means the money took a path this test is not
// asserting on, and it should fail loudly rather than pass quietly.
type refusingDoer struct{}

func (refusingDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("no commerce call may leave this process")
}

// fakeProvider offers a fixed set of connector actions — platform tools every org
// reaches — so the platform can list and price them.
type fakeProvider struct {
	tools []tools.Tool
}

func (f *fakeProvider) Source() tools.Source { return tools.SourceConnector }
func (f *fakeProvider) List(context.Context, tools.Scope) ([]tools.Tool, error) {
	return f.tools, nil
}
func (f *fakeProvider) Dispatch(context.Context, tools.Principal, string, map[string]any) (any, error) {
	return map[string]any{"ran": true}, nil
}

// ownRows offers each org its OWN functions, as functions, agents and an org's
// servers do: one name in two orgs is two tools, and a call runs the caller's.
type ownRows struct{ byOrg map[string][]string }

func (ownRows) Source() tools.Source { return tools.SourceFunction }
func (p ownRows) List(_ context.Context, s tools.Scope) ([]tools.Tool, error) {
	var out []tools.Tool
	for _, n := range p.byOrg[s.Org] {
		out = append(out, tools.Tool{Name: n, Source: tools.SourceFunction, Dispatchable: true})
	}
	return out, nil
}
func (ownRows) Dispatch(_ context.Context, p tools.Principal, _ string, _ map[string]any) (any, error) {
	return map[string]any{"ranFor": p.Org}, nil
}

// compose stands in for the composer. Production programs are built by
// cloud.App, which installs the principal enrichment once at the root before
// any route; a test that mounts this subsystem on a bare app owns that duty
// itself, exactly once, here.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// who is the caller of one request.
type who struct {
	org   string
	user  string // the person, when it is not the org's one default
	admin bool   // an admin of its own org
	super bool   // SuperAdmin: owner == "admin"
}

func member(org string) who { return who{org: org} }
func admin(org string) who  { return who{org: org, admin: true} }

// platform is the reserved admin org: the platform's own books, and the only
// seller of a tool every org reaches.
const platform = "admin"

var (
	anyone  = who{}
	arbiter = who{org: platform, admin: true, super: true}
)

// env is the market in one process.
type env struct {
	t      *testing.T
	app    *zip.App
	fin    money.FinanceClient
	events *planetest.Economic
	rec    *audit.Recorder
	key    *ecdsa.PrivateKey // the buyers' payment key
	peers  *stand
	// dsn is the hanzo-sql the marketplace's store is on, empty on SQLite.
	dsn string
	// master and kms are the deployment's data-plane master and KMS: a second replica of the
	// store opens with them.
	master []byte
	kms    secret.Store
	// resource is what the last challenge said it was FOR (resource.url on the
	// PaymentRequired envelope).
	resource string
}

func newEnv(t *testing.T, offered ...string) *env {
	t.Helper()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(raw))
	// The tool plane bills its own orchestration unit on every successful call;
	// priced at zero, the only thing that can move money here is x402.
	t.Setenv("CLOUD_TOOLS_FEE_CENTS", "0")
	t.Setenv("CLOUD_X402_ASSET", testAsset)
	log := luxlog.New("test")
	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)

	kraw := make([]byte, 32)
	_, _ = rand.Read(kraw)
	kmsc, err := kms.New(kms.Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(kraw)}, log)
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	// A settlement is two ledger entries that agree, so the ledger is co-resident
	// and the metering spine (which is how the PAYER is debited) posts into it.
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	gauge, err := meter.New(meter.Config{BaseURL: "http://commerce.test", HTTPClient: refusingDoer{}})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	// Every test mounts the apps again, and the plane's registry appends: start each
	// from an empty one. The event plane binds the socket directory every peer here
	// shares.
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	events := planetest.Events(t)
	peers := serveStand(t)

	app := zip.New(zip.Config{Logger: log})
	compose(app)
	deps := cloud.Deps{KMS: kmsc, Metering: gauge, Audit: rec, MasterKey: raw}
	// The market mounts neighbours (the wallet's KMS, for one) whose stores are not ported
	// to hanzo-sql yet and open as files until they are. The marketplace's
	// store is the one this suite puts on the leg's engine, so it alone is mounted with the
	// DSN the leg set; the rest of the market opens as it does where no DSN is set.
	dsn := os.Getenv(locus.Env)
	t.Setenv(locus.Env, "")
	for _, m := range []struct {
		name string
		use  func(cloud.Router, cloud.Deps) error
	}{
		{"wallet", wallet.Use}, {"x402", x402.Use}, {"agent", agents.Use}, {"marketplace", Use}, {"tool", tools.Use},
	} {
		if m.name == "marketplace" {
			t.Setenv(locus.Env, dsn)
		}
		err := m.use(app, deps)
		if m.name == "marketplace" {
			t.Setenv(locus.Env, "")
		}
		if err != nil {
			t.Fatalf("%s.Use: %v", m.name, err)
		}
	}
	var offer []tools.Tool
	for _, name := range offered {
		offer = append(offer, tools.Tool{Name: name, Source: tools.SourceConnector, Dispatchable: true})
	}
	tools.Default().Register(&fakeProvider{tools: offer})
	for _, name := range []string{"wallet", "x402", "agent", "tool", "marketplace"} {
		stop, err := cloud.ServePlane(name, log)
		if err != nil {
			t.Fatalf("plane %s: %v", name, err)
		}
		t.Cleanup(func() { _ = stop() })
	}
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
		_ = tools.Shutdown(context.Background())
		_ = agents.Shutdown(context.Background())
		_ = x402.Shutdown()
		_ = wallet.Shutdown()
		finance.Publish(nil)
		tools.Default().SetActivation(nil)
		_ = kmsc.Close()
		_ = rec.Close()
	})
	e := &env{t: t, app: app, fin: fin, events: events, rec: rec, peers: peers, dsn: dsn, master: raw, kms: kmsc}
	e.key, _ = crypto.GenerateKey()
	return e
}

// req drives one request as w, optionally carrying an x402 proof.
func (e *env) req(method, path string, w who, proof string, body any) (int, []byte, http.Header) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if w.org != "" {
		hr.Header.Set("X-Org-Id", w.org)
		hr.Header.Set("X-User-Id", cmp.Or(w.user, "u_"+w.org))
		hr.Header.Set("X-User-Email", "admin@"+w.org+".test")
		hr.Header.Set("X-User-Owner", w.org)
	}
	if w.admin {
		hr.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if w.super {
		hr.Header.Set("X-User-IsAdmin", "true")
		hr.Header.Set("X-User-Owner", "admin")
	}
	if proof != "" {
		hr.Header.Set(protocol.HeaderPaymentSignature, proof)
	}
	resp, err := e.app.Test(hr, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// must drives a request, failing the test on any status but want.
func (e *env) must(want int, method, path string, w who, body, out any) {
	e.t.Helper()
	code, b, _ := e.req(method, path, w, "", body)
	if code != want {
		e.t.Fatalf("%s %s as %+v = %d, want %d: %s", method, path, w, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// createWallet provisions a real KMS wallet in org and returns its id.
func (e *env) createWallet(org string) string {
	e.t.Helper()
	var acct struct {
		ID string `json:"id"`
	}
	e.must(200, http.MethodPost, "/v1/wallet/accounts", admin(org), map[string]any{"name": "payee"}, &acct)
	var w struct {
		ID string `json:"id"`
	}
	e.must(200, http.MethodPost, "/v1/wallet", admin(org), map[string]any{"accountId": acct.ID, "name": "earnings",
		"custody": "kms", "tier": "hot", "chain": "eip155:36963"}, &w)
	return w.ID
}

// list publishes a listing as org's admin and returns the created row.
func (e *env) list(org string, in publishReq) Listing {
	e.t.Helper()
	var l Listing
	e.must(http.StatusCreated, http.MethodPost, "/v1/marketplace/listings", admin(org), in, &l)
	return l
}

// shelve publishes a platform tool listing, as a SuperAdmin at home in the
// platform's org.
func (e *env) shelve(in publishReq) Listing {
	e.t.Helper()
	var l Listing
	e.must(http.StatusCreated, http.MethodPost, "/v1/marketplace/listings", arbiter, in, &l)
	return l
}

// install activates a tool for org, the way a buyer does.
func (e *env) install(org, tool string) {
	e.t.Helper()
	e.must(200, http.MethodPost, "/v1/marketplace/install", member(org), map[string]any{"tool": tool}, nil)
}

// call dispatches a tool as org, optionally paying.
func (e *env) call(org, tool, proof string) (int, []byte, http.Header) {
	e.t.Helper()
	return e.req(http.MethodPost, "/v1/tool/call", member(org), proof, map[string]any{"name": tool})
}

// challengeOf reads the PaymentRequirements off a 402 refusal's header, and parks
// what it was for.
func (e *env) challengeOf(h http.Header) protocol.PaymentRequirements {
	e.t.Helper()
	raw := h.Get(protocol.HeaderPaymentRequired)
	if raw == "" {
		e.t.Fatalf("402 carried no %s header — a challenge a client cannot read is not a challenge", protocol.HeaderPaymentRequired)
	}
	var required protocol.PaymentRequired
	if err := protocol.DecodeHeader(raw, &required); err != nil {
		e.t.Fatalf("decode challenge %q: %v", raw, err)
	}
	if len(required.Accepts) != 1 {
		e.t.Fatalf("challenge offered %d ways to pay, want 1", len(required.Accepts))
	}
	e.resource = required.Resource.URL
	return required.Accepts[0]
}

// pay signs an authorization over exactly the challenge's terms, valid for
// window from now, echoing the resource it was challenged for — what a compliant
// x402 client does — and returns the PAYMENT-SIGNATURE value.
func (e *env) pay(req protocol.PaymentRequirements, window time.Duration) string {
	e.t.Helper()
	now := time.Now().Unix()
	var nonce [32]byte
	_, _ = rand.Read(nonce[:])
	p, err := protocol.Sign(req, e.key, nonce, uint64(now-60), uint64(now+int64(window.Seconds())))
	if err != nil {
		e.t.Fatalf("sign: %v", err)
	}
	p.Resource = &protocol.ResourceInfo{URL: e.resource}
	signed, err := protocol.EncodeHeader(p)
	if err != nil {
		e.t.Fatalf("encode: %v", err)
	}
	return signed
}

// balance is the exact ledger balance of (org, subject).
func (e *env) balance(org, subject string) money.Amount {
	e.t.Helper()
	amt, err := e.fin.Balance(context.Background(), org, subject, "usd", false)
	if err != nil {
		e.t.Fatalf("balance %s/%s: %v", org, subject, err)
	}
	return amt
}

// fund deposits amount into org's own ledger.
func (e *env) fund(org string, amount money.Amount) {
	e.t.Helper()
	if _, err := e.fin.Deposit(context.Background(), money.DepositInput{
		Org: org, Subject: org, Amount: amount, Currency: "usd", Ref: "fund_" + org + "_" + amount.String(),
	}); err != nil {
		e.t.Fatalf("fund %s: %v", org, err)
	}
}

// actions are the audit actions recorded for org with prefix, oldest first.
func (e *env) actions(org, prefix string) []string {
	e.t.Helper()
	rows, _, err := e.rec.Query(context.Background(), audit.Filter{Org: org, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		if len(rows[i].Action) >= len(prefix) && rows[i].Action[:len(prefix)] == prefix {
			out = append(out, rows[i].Action)
		}
	}
	return out
}

// ── the stand-ins ─────────────────────────────────────────────────────────────

// clearCall is one clearance the market asked for.
type clearCall struct {
	payer string
	in    client.ClearIn
	id    string // the clearance it was given
}

// down is a verdict a test sets to have principal fail to answer at all.
const down = "down"

// stand is principal, tax, projects and train, answering as a test sets them.
type stand struct {
	mu            sync.Mutex
	verdict       map[string]string // payee → verdict; cleared when unset
	withheld      map[string]string // payee → what a cleared payment to it withholds
	clears        []clearCall
	standing      map[string]client.Standing
	forms         map[string]client.TaxForm
	inbox         map[string][]client.Received
	documented    map[string]bool
	projects      map[string]map[string]bool                   // org → project slug → exists
	capabilities  map[string]map[string]client.CapabilityOwned // org → name → owned
	installedCaps map[string]map[string]string                 // org → name → ref
	// meet runs before a clearance is answered, outside mu, and a test sets it to hold
	// a funding at the moment two replicas could be funding one payment together.
	meet func()
}

func (s *stand) set(fn func(*stand)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *stand) asked() []clearCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]clearCall(nil), s.clears...)
}

func serveStand(t *testing.T) *stand {
	t.Helper()
	s := &stand{verdict: map[string]string{}, withheld: map[string]string{}, standing: map[string]client.Standing{}, forms: map[string]client.TaxForm{},
		inbox: map[string][]client.Received{}, documented: map[string]bool{}, projects: map[string]map[string]bool{},
		capabilities: map[string]map[string]client.CapabilityOwned{}, installedCaps: map[string]map[string]string{}}
	org := func(ctx context.Context) (string, error) {
		o := zip.CallerOf(ctx).Org
		if o == "" {
			return "", zip.ErrForbidden("no org on the call")
		}
		return o, nil
	}
	p := zip.New(zip.Config{AppName: "principal"})
	p.Post("/principal/clear", func(ctx context.Context, in *client.ClearIn) (*client.Cleared, error) {
		payer, err := org(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		meet := s.meet
		s.mu.Unlock()
		if meet != nil {
			meet()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		v := s.verdict[in.Payee]
		out := &client.Cleared{ID: "clr_" + randHex(6), Verdict: client.ClearCleared, Required: []string{}, Net: in.Amount}
		s.clears = append(s.clears, clearCall{payer: payer, in: *in, id: out.ID})
		if w := s.withheld[in.Payee]; w != "" {
			gross, _ := money.ParseUSD(in.Amount)
			kept, _ := money.ParseUSD(w)
			out.Net, out.Withheld = dollars(gross.Sub(kept)), w
		}
		switch v {
		case down:
			return nil, zip.Errorf(http.StatusServiceUnavailable, "principal is down")
		case client.ClearNeedsForm:
			out.Verdict, out.Required, out.Net = v, []string{"tax_form"}, ""
		case client.ClearBlocked:
			out.Verdict, out.Required, out.Net = v, []string{"sanctions_review"}, ""
		}
		return out, nil
	}, zip.WithOperationID(client.PrincipalClear))
	p.Post("/principal/standing", func(ctx context.Context, _ *client.PrincipalIn) (*client.Standing, error) {
		o, err := org(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		st, ok := s.standing[o]
		if !ok {
			st = client.Standing{Identity: "none", Sanctions: "unscreened", Reason: "no name on file"}
		}
		if st.Statements == nil {
			st.Statements = []client.Statement{}
		}
		return &st, nil
	}, zip.WithOperationID(client.PrincipalStanding))
	p.Post("/principal/public", func(_ context.Context, in *client.PublicIn) (*client.Publics, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := &client.Publics{}
		for _, o := range in.Orgs {
			out.Principals = append(out.Principals, client.Public{Org: o, Documented: s.documented[o]})
		}
		return out, nil
	}, zip.WithOperationID(client.PrincipalPublic))
	listen(t, p, "principal")

	x := zip.New(zip.Config{AppName: "tax"})
	x.Post("/tax/standing", func(ctx context.Context, _ *client.StandingIn) (*client.TaxForm, error) {
		o, err := org(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		f := s.forms[o]
		return &f, nil
	}, zip.WithOperationID(client.TaxStanding))
	x.Post("/tax/inbox", func(ctx context.Context, in *client.InboxIn) (*client.Inbox, error) {
		o, err := org(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		out := &client.Inbox{Received: []client.Received{}}
		for _, r := range s.inbox[o] {
			if in.Year == 0 || r.Year == in.Year {
				out.Received = append(out.Received, r)
			}
		}
		return out, nil
	}, zip.WithOperationID(client.TaxInbox))
	listen(t, x, "tax")

	pr := zip.New(zip.Config{AppName: "project"})
	pr.Post("/projects/ownership", func(ctx context.Context, in *client.OwnerIn) (*client.Ownership, error) {
		o, err := org(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		out := &client.Ownership{Mine: s.projects[o][in.IDOrSlug]}
		for other, slugs := range s.projects {
			if other != o && slugs[in.IDOrSlug] {
				out.Other = true
			}
		}
		return out, nil
	}, zip.WithOperationID(client.ProjectsOwnership))
	listen(t, pr, "project")

	tr := zip.New(zip.Config{AppName: "train"})
	tr.Post("/train/capabilities/owned", func(ctx context.Context, in *client.CapabilityOwnedIn) (*client.CapabilityOwned, error) {
		o, err := org(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if caps, ok := s.capabilities[o]; ok {
			if c, ok := caps[in.Name]; ok {
				return &c, nil
			}
		}
		return &client.CapabilityOwned{Mine: false, Name: in.Name}, nil
	}, zip.WithOperationID(client.TrainCapabilityOwned))
	tr.Post("/train/capabilities/install", func(ctx context.Context, in *client.CapabilityInstallIn) (*client.CapabilityInstalled, error) {
		o, err := org(ctx)
		if err != nil {
			return nil, err
		}
		source, sha, ok := client.CapabilityOf(in.Ref)
		if !ok {
			return nil, zip.ErrBadRequest("invalid ref")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		c, ok := s.capabilities[source][in.Name]
		if !ok || c.SHA256 != sha {
			return nil, zip.ErrNotFound("capability not found")
		}
		if s.installedCaps[o] == nil {
			s.installedCaps[o] = map[string]string{}
		}
		if in.On {
			s.installedCaps[o][in.Name] = in.Ref
		} else {
			delete(s.installedCaps[o], in.Name)
		}
		return &client.CapabilityInstalled{Name: in.Name, Installed: in.On, SHA256: sha}, nil
	}, zip.WithOperationID(client.TrainCapabilityInstall))
	tr.Post("/train/capabilities/weights", func(_ context.Context, in *client.CapabilityWeightsIn) (*client.CapabilityWeights, error) {
		source, sha, ok := client.CapabilityOf(in.Ref)
		if !ok {
			return nil, zip.ErrBadRequest("invalid ref")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		c, ok := s.capabilities[source][in.Name]
		if !ok || c.SHA256 != sha {
			return nil, zip.ErrNotFound("capability not found")
		}
		return &client.CapabilityWeights{
			Name:           c.Name,
			Version:        1,
			SHA256:         c.SHA256,
			BaseSHA256:     c.BaseSHA256,
			Representation: c.Representation,
			Form:           c.Form,
			Bytes:          c.Bytes,
			Tar:            base64.StdEncoding.EncodeToString([]byte("tar-" + c.Name)),
		}, nil
	}, zip.WithOperationID(client.TrainCapabilityWeights))
	listen(t, tr, "train")
	return s
}

// listen binds one stand-in's socket and waits for it to accept.
func listen(t *testing.T, app *zip.App, name string) {
	t.Helper()
	path := zip.SocketPath(name)
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 400 {
		if conn, err := net.Dial("unix", path); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the %s stand-in never began listening on %s", name, path)
}

// testAsset is the EIP-3009 token the challenge names in these tests: a payer signs over
// its address, so a rail with none set is one no payer can pay. The tests pin no real
// token; this is a made-up stand-in for the operator's CLOUD_X402_ASSET.
const testAsset = "0x1111111111111111111111111111111111111111"

package marketplace

// split_test.go proves the pay-per-use rail across the PROCESS BOUNDARY — the half
// payments_test.go cannot reach, because it composes the four subsystems in one
// process and the fleet has never shipped that way.
//
// The shipped topology is one binary per app: manifest/apps.go declares tools,
// marketplace, x402, wallets and commerce as five ordinary prefix-routed rows, the
// Dockerfile builds a plugin binary per row, and cmd/cloud loads each as a CHILD
// PROCESS. Every seam that made a price payable — x402.reg, tools.std's charger,
// wallets' mounted singleton, finance.Current — is a process-global, so in that
// topology all four bind nothing. A priced tool was refused rather than sold.
//
// So this test IS that topology, with real operating-system processes. The test
// binary re-execs itself once per app (TestMain → runChild); each child mounts ONE
// subsystem and serves its plane socket, and NOTHING is shared but the socket
// directory. The parent is the TOOLS process: it mounts the tool plane alone, with
// no charger, no price table, no wallet store and no ledger — exactly what
// plugin/tools/main.go links.
//
// What it therefore proves cannot be proved any other way: a settlement that
// crosses four sockets and lands in a ledger three processes away, exactly once,
// for a quarter of a cent.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/wallets"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── the child half ────────────────────────────────────────────────────────────

const (
	roleEnv    = "CLOUD_SPLIT_ROLE"
	sellerOrg  = "sellerorg"
	buyerOrg   = "buyerorg"
	pricedTool = "conn_premium"
	freeTool   = "conn_free"
	price      = "0.0025"
)

// TestMain turns this binary into any one of the fleet's app processes.
//
// A child never runs the test framework: it mounts its subsystem, serves its plane
// socket and blocks. That is what makes the isolation real — a child shares no
// package global with the parent, because it is a different process image.
func TestMain(m *testing.M) {
	if role := strings.TrimSpace(os.Getenv(roleEnv)); role != "" {
		os.Exit(runChild(role))
	}
	os.Exit(m.Run())
}

// runChild is one app's whole composition root, the same shape plugin/<app>/main.go
// has: mount this subsystem and nothing else, then serve the plane.
//
// It prints READY (with whatever the parent must learn from it) once its socket
// accepts, so the parent never races a cold child.
func runChild(role string) int {
	log := luxlog.New(role)
	dir := os.Getenv("CLOUD_SPLIT_DIR")
	app := zip.New(zip.Config{Logger: log})
	app.Use(cloud.Bridge())
	deps := cloud.Deps{Logger: log, DataDir: dir}
	extra := ""

	switch role {
	case "wallets":
		k, err := kms.New(kms.Config{DataDir: dir, MasterKeyB64: os.Getenv("CLOUD_SPLIT_KMS")}, log)
		if err != nil {
			return childFail("kms.New: %v", err)
		}
		deps.KMS = k
		if err := wallets.Mount(app, deps); err != nil {
			return childFail("wallets.Mount: %v", err)
		}
		id, err := seedWallet(app, sellerOrg)
		if err != nil {
			return childFail("seed wallet: %v", err)
		}
		extra = id

	case "commerce":
		// The ledger, and the ONE process that opens it. metering resolves the
		// co-resident ledger natively (finance.Current), so the payer debit this
		// process serves on the plane is a real entry in a real store.
		finance.Publish(finance.New(filepath.Join(dir, "ledger")))
		meter, err := metering.New(metering.Config{BaseURL: "http://commerce.split", HTTPClient: refusingDoer{}})
		if err != nil {
			return childFail("metering.New: %v", err)
		}
		// commerce.Mount publishes the ledger ops before it validates its HTTP
		// dependencies, which is stated at its definition — so this process serves
		// the real finance_record / finance_credit handlers against the real ledger
		// without also standing up a payments provider it will never be asked for.
		_ = commerce.Mount(app, cloud.Deps{Logger: log, DataDir: dir, Metering: meter})

	case "marketplace":
		if err := Mount(app, deps); err != nil {
			return childFail("marketplace.Mount: %v", err)
		}
		if err := seedListing(mounted.State.store, os.Getenv("CLOUD_SPLIT_WALLET")); err != nil {
			return childFail("seed listing: %v", err)
		}

	case "x402":
		// No metering client, no ledger, no wallet store, no price table — the
		// shipped x402 binary exactly. Everything it needs, it asks for.
		if err := x402.Mount(app, deps); err != nil {
			return childFail("x402.Mount: %v", err)
		}

	default:
		return childFail("unknown role %q", role)
	}

	stop, err := cloud.ServePlane(role, log)
	if err != nil {
		return childFail("ServePlane: %v", err)
	}
	defer func() { _ = stop() }()

	fmt.Printf("READY %s\n", extra)
	// Held open by the parent's stdin pipe: when the parent exits or kills us, the
	// read returns and the process ends. No signal handling, no orphan.
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

func childFail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "child: "+format+"\n", args...)
	return 1
}

// seedWallet provisions the seller's payout wallet through the wallets subsystem's
// OWN routes, in the process that owns the store.
func seedWallet(app *zip.App, org string) (string, error) {
	var acct struct {
		ID string `json:"id"`
	}
	if _, b, err := childReq(app, http.MethodPost, "/v1/wallets/accounts", org, `{"name":"payee"}`); err != nil {
		return "", err
	} else if err := json.Unmarshal(b, &acct); err != nil || acct.ID == "" {
		return "", fmt.Errorf("create account: %v (%s)", err, b)
	}
	code, b, err := childReq(app, http.MethodPost, "/v1/wallets", org,
		`{"accountId":"`+acct.ID+`","name":"earnings","custody":"kms","tier":"hot","chain":"eip155:36963"}`)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("create wallet = %d (%s)", code, b)
	}
	var w struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &w); err != nil || w.ID == "" {
		return "", fmt.Errorf("decode wallet: %v (%s)", err, b)
	}
	return w.ID, nil
}

// seedListing writes the two listings into the store this process owns.
//
// It writes the ROWS and not through POST /v1/marketplace/listings, and the reason
// is a gap this change does NOT close: publish first asks tools.Default().Exists,
// and in the marketplace binary that registry has no providers — no connector, no
// function, no MCP source mounts there — so every publish answers "unknown tool".
// The same is true of install, which calls tools.Default().Activate against a
// registry with no activation store. Both are the SAME bug class as the one under
// test, on a different seam (the tool REGISTRY, not the payment rail), and they need
// their own ops. Faking a provider here would hide that; stating it, and seeding the
// datum the payment path actually reads, does not. See apps/tools/LLM.md.
func seedListing(store *Store, wallet string) error {
	amount, err := money.ParseUSD(price)
	if err != nil {
		return err
	}
	for _, l := range []Listing{
		{PublisherOrg: sellerOrg, Tool: pricedTool, Title: "Premium", Price: amount,
			Currency: "USD", Recipient: wallet, Public: true},
		{PublisherOrg: sellerOrg, Tool: freeTool, Title: "Free", Currency: "USD", Public: true},
	} {
		if _, err := store.Create(context.Background(), l); err != nil {
			return fmt.Errorf("create listing %s: %w", l.Tool, err)
		}
	}
	return nil
}

func childReq(app *zip.App, method, path, org, body string) (int, []byte, error) {
	hr := httptest.NewRequest(method, path, strings.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org)
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 0})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// ── the parent half: the tools process ────────────────────────────────────────

// fleet is the four peer processes plus this one.
type fleet struct {
	t      *testing.T
	app    *zip.App // the TOOLS process's router — this test binary
	key    *ecdsa.PrivateKey
	wallet string
	kids   map[string]*exec.Cmd
}

// splitFleet starts the peers as real processes and mounts the tool plane HERE,
// alone, with every seam a co-resident composition would have bound left unbound.
func splitFleet(t *testing.T) *fleet {
	t.Helper()
	if testing.Short() {
		t.Skip("spawns real child processes")
	}
	run := t.TempDir()
	t.Setenv("ZIP_RUNTIME_DIR", run)

	master := make([]byte, 32)
	_, _ = rand.Read(master)
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(master))
	// The tool plane bills its own orchestration unit on every successful call,
	// fire-and-forget on a background context. Priced at zero, the only thing that
	// can move money here is x402.
	t.Setenv("CLOUD_TOOLS_FEE_CENTS", "0")

	f := &fleet{t: t, kids: map[string]*exec.Cmd{}}
	f.key, _ = crypto.GenerateKey()

	// THIS process is the tools binary. Assert it, rather than assume it: another
	// test in this package mounts marketplace, which installs the charger and the
	// price table on the very globals whose absence is the thing under test.
	tools.SetCharger(nil)
	x402.Publish(nil)
	finance.Publish(nil)
	t.Cleanup(func() { tools.SetCharger(nil); x402.Publish(nil); finance.Publish(nil) })

	kmsKey := make([]byte, 32)
	_, _ = rand.Read(kmsKey)
	f.wallet = f.start("wallets", "CLOUD_SPLIT_KMS="+base64.StdEncoding.EncodeToString(kmsKey))
	f.start("commerce")
	f.start("marketplace", "CLOUD_SPLIT_WALLET="+f.wallet)
	f.start("x402")

	log := luxlog.New("tools")
	f.app = zip.New(zip.Config{Logger: log})
	f.app.Use(cloud.Bridge())
	if err := tools.Mount(f.app, cloud.Deps{Logger: log, DataDir: t.TempDir()}); err != nil {
		t.Fatalf("tools.Mount: %v", err)
	}
	tools.Default().Register(&fakeProvider{tools: []tools.Tool{
		{Name: pricedTool, Source: tools.SourceConnector, Dispatchable: true},
		{Name: freeTool, Source: tools.SourceConnector, Dispatchable: true},
	}})
	t.Cleanup(func() { _ = tools.Shutdown(context.Background()); tools.Default().SetActivation(nil) })

	// Activation is the TOOL plane's own store, so it is written here — the one
	// place in this test that is not an app's public route, because marketplace's
	// install writes tools.Default() in ITS process and that registry has no
	// activation store at all. That is a separate seam from the payment rail and it
	// is named in apps/tools/LLM.md rather than papered over here.
	for _, name := range []string{pricedTool, freeTool} {
		if err := tools.Default().Activate(context.Background(), buyerOrg, "default", name, "u_"+buyerOrg); err != nil {
			t.Fatalf("activate %s: %v", name, err)
		}
	}
	return f
}

// start launches one peer and waits for its READY line, returning whatever it
// reported. A child that dies before READY fails the test with its own stderr.
func (f *fleet) start(role string, env ...string) string {
	f.t.Helper()
	dir := f.t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(), append([]string{roleEnv + "=" + role, "CLOUD_SPLIT_DIR=" + dir}, env...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		f.t.Fatalf("%s stdout: %v", role, err)
	}
	hold, err := cmd.StdinPipe()
	if err != nil {
		f.t.Fatalf("%s stdin: %v", role, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		f.t.Fatalf("start %s: %v", role, err)
	}
	f.kids[role] = cmd
	f.t.Cleanup(func() {
		_ = hold.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// SCAN for READY rather than reading one line: a real subsystem prints its own
	// startup chatter on stdout, and a handshake that assumed line one would be
	// asserting on whichever library logged first.
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "READY"); ok {
				got <- strings.TrimSpace(rest)
				break
			}
		}
		close(got)
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case v, ok := <-got:
		if !ok {
			f.t.Fatalf("%s exited before READY\nstderr:\n%s", role, stderr.String())
		}
		return v
	case <-time.After(90 * time.Second):
		f.t.Fatalf("%s did not report READY\nstderr:\n%s", role, stderr.String())
		return ""
	}
}

// kill stops one peer and waits until its socket stops ACCEPTING, so a test that
// asserts "this app is gone" is not racing its exit.
//
// The socket FILE outlives the process — a killed listener unlinks nothing — which
// is exactly the state the rail must read as an OUTAGE rather than as an absence.
// So the outage tests below run against a peer whose socket is still on disk and
// still refuses every connection, which is the harder and more realistic half.
func (f *fleet) kill(role string) {
	f.t.Helper()
	cmd := f.kids[role]
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	for i := 0; i < 400; i++ {
		c, err := net.DialTimeout("unix", zip.SocketPath(role), time.Second)
		if err != nil {
			return
		}
		_ = c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("%s socket still accepting after kill", role)
}

// call dispatches a tool as org through the tool plane's OWN route, optionally
// paying. This is the door the product serves; nothing here is a stand-in for it.
func (f *fleet) call(org, tool, proof string) (int, []byte, http.Header) {
	f.t.Helper()
	hr := httptest.NewRequest(http.MethodPost, "/v1/tools/call", strings.NewReader(`{"name":"`+tool+`"}`))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org)
	if proof != "" {
		hr.Header.Set(x402.HeaderProof, proof)
	}
	resp, err := f.app.Test(hr, zip.TestConfig{Timeout: 0})
	if err != nil {
		f.t.Fatalf("tools/call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// balance reads a ledger THREE processes away, over the plane, as that org.
func (f *fleet) balance(org, subject string) money.Amount {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), 10*time.Second)
	defer cancel()
	out, err := cloud.Ask[plane.BalanceIn, plane.Balance](ctx, "commerce", plane.FinanceBalance,
		&plane.BalanceIn{Subject: subject, Currency: "usd"})
	if err != nil {
		f.t.Fatalf("balance %s/%s: %v", org, subject, err)
	}
	amt, err := out.Amount.Parse()
	if err != nil {
		f.t.Fatalf("parse balance: %v", err)
	}
	return money.FromDecimal(amt.Decimal())
}

// fund credits the buyer through the very op the settlement's payee side uses, so
// the test's own setup exercises the fourth hop before anything depends on it.
func (f *fleet) fund(org string, amount money.Amount) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), 10*time.Second)
	defer cancel()
	if _, err := cloud.Ask[plane.CreditIn, plane.Credited](ctx, "commerce", plane.FinanceCredit,
		&plane.CreditIn{Subject: org, Amount: plane.Amount(amount.Unwrap()), Ref: "fund_" + org}); err != nil {
		f.t.Fatalf("fund %s: %v", org, err)
	}
}

// pay signs an authorization over exactly the challenge's terms.
func (f *fleet) pay(req x402.PaymentRequirements, nonce string) string {
	f.t.Helper()
	now := time.Now().Unix()
	p, err := x402.Sign(req, f.key, nonce, now-60, now+300, "", "")
	if err != nil {
		f.t.Fatalf("sign: %v", err)
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func (f *fleet) challengeOf(h http.Header) x402.PaymentRequirements {
	f.t.Helper()
	raw := h.Get(x402.HeaderRequirements)
	if raw == "" {
		f.t.Fatalf("402 carried no %s header — a challenge a client cannot read is not a challenge",
			x402.HeaderRequirements)
	}
	var req x402.PaymentRequirements
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		f.t.Fatalf("decode challenge %q: %v", raw, err)
	}
	return req
}

func newNonce(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "0x" + hex.EncodeToString(b)
}

// ── the proofs ────────────────────────────────────────────────────────────────

// TestSplitFleetSettlesAPricedTool is the defect closed. Five processes, four
// sockets, one quarter of a cent:
//
//	tools ──settle──▶ x402 ──price──▶ marketplace
//	                   │  ──payee──▶ wallets
//	                   └─ ──debit/credit──▶ commerce
//
// Every hop is a real unix socket between real processes. The assertions are the
// ones a shop has to satisfy: the buyer is charged EXACTLY the price, the seller's
// wallet is credited EXACTLY the price, and a sub-cent price stays a real price
// rather than rounding to zero somewhere on the wire.
func TestSplitFleetSettlesAPricedTool(t *testing.T) {
	f := splitFleet(t)

	want, err := money.ParseUSD(price)
	if err != nil {
		t.Fatalf("parse price: %v", err)
	}
	f.fund(buyerOrg, money.FromCents(100))
	before := f.balance(buyerOrg, buyerOrg)

	// (a) UNPAID → 402 carrying terms the client can act on. Not ErrChargerUnset,
	// which is what this call answered for as long as the fleet has been split.
	code, body, hdr := f.call(buyerOrg, pricedTool, "")
	if code != http.StatusPaymentRequired {
		t.Fatalf("unpaid priced call = %d (%s), want 402", code, body)
	}
	if bytes.Contains(body, []byte("payment seam not configured")) {
		t.Fatalf("the tools process still has no rail across the boundary: %s", body)
	}
	req := f.challengeOf(hdr)
	if req.Resource != plane.ToolResource(pricedTool) {
		t.Fatalf("challenge names %q, want %q", req.Resource, plane.ToolResource(pricedTool))
	}
	if req.Payee == "" {
		t.Fatal("challenge names no payee address — wallets was never reached")
	}
	// $0.0025 at USDC's 6 decimals is 2500 smallest units. A cents-typed hop
	// anywhere on this path would have offered 0.
	if req.Amount != "2500" {
		t.Fatalf("challenge amount = %q, want 2500 (0.0025 USD at 6dp)", req.Amount)
	}
	if got := f.balance(buyerOrg, buyerOrg); got.Cmp(before) != 0 {
		t.Fatalf("a refused call moved money: %s → %s", before, got)
	}

	// (b) PAID → served, and both ledger sides tie exactly.
	nonce := newNonce(t)
	code, body, hdr = f.call(buyerOrg, pricedTool, f.pay(req, nonce))
	if code != http.StatusOK {
		t.Fatalf("paid call = %d (%s), want 200", code, body)
	}
	if !bytes.Contains(body, []byte(`"ran":true`)) {
		t.Fatalf("tool did not run after payment: %s", body)
	}
	if hdr.Get(x402.HeaderReceipt) == "" {
		t.Fatalf("paid call carried no %s receipt", x402.HeaderReceipt)
	}

	debited := before.Sub(f.balance(buyerOrg, buyerOrg))
	if debited.Cmp(want) != 0 {
		t.Fatalf("payer debited %s, want exactly %s", debited, want)
	}
	credited := f.balance(sellerOrg, f.wallet)
	if credited.Cmp(want) != 0 {
		t.Fatalf("seller credited %s, want exactly %s", credited, want)
	}

	// (c) REPLAY. The settlement id is keccak(from|nonce), so the same
	// authorization maps to the same row in x402's store and to the same
	// idempotency key on both ledger writes. Re-submitting it moves NOTHING.
	buyerAfter, sellerAfter := f.balance(buyerOrg, buyerOrg), f.balance(sellerOrg, f.wallet)
	code, body, _ = f.call(buyerOrg, pricedTool, f.pay(req, nonce))
	if code != http.StatusOK {
		t.Fatalf("replayed authorization = %d (%s); an idempotent retry is served, not refused", code, body)
	}
	if got := f.balance(buyerOrg, buyerOrg); got.Cmp(buyerAfter) != 0 {
		t.Fatalf("a replay debited the buyer again: %s → %s", buyerAfter, got)
	}
	if got := f.balance(sellerOrg, f.wallet); got.Cmp(sellerAfter) != 0 {
		t.Fatalf("a replay credited the seller again: %s → %s", sellerAfter, got)
	}
}

// TestSplitFleetFreeToolNeedsNoPayment: closing the seam must not put a toll on
// what was free. A listed-but-free tool dispatches with no challenge and no ledger
// movement, even though every dispatch is offered to the rail — over four sockets.
func TestSplitFleetFreeToolNeedsNoPayment(t *testing.T) {
	f := splitFleet(t)
	f.fund(buyerOrg, money.FromCents(100))
	before := f.balance(buyerOrg, buyerOrg)

	code, body, hdr := f.call(buyerOrg, freeTool, "")
	if code != http.StatusOK {
		t.Fatalf("free tool = %d (%s), want 200 — an unpriced tool is not for sale", code, body)
	}
	if hdr.Get(x402.HeaderRequirements) != "" {
		t.Fatalf("a free tool was challenged for payment it does not cost")
	}
	if got := f.balance(buyerOrg, buyerOrg); got.Cmp(before) != 0 {
		t.Fatalf("a free call moved money: %s → %s", before, got)
	}
}

// TestSplitFleetFailsClosedWithoutTheRail is the property that must hold when the
// plane cannot deliver: with the x402 process GONE, a priced tool is refused and
// nothing moves. Never served free, never half-settled.
//
// It is the same shape as the bug this whole change exists to fix, pointed the
// other way: the old failure was silent and permanent, this one is loud and lasts
// exactly as long as the outage.
func TestSplitFleetFailsClosedWithoutTheRail(t *testing.T) {
	f := splitFleet(t)
	f.fund(buyerOrg, money.FromCents(100))
	before := f.balance(buyerOrg, buyerOrg)

	f.kill("x402")

	code, body, hdr := f.call(buyerOrg, pricedTool, "")
	if code == http.StatusOK {
		t.Fatalf("a priced tool was SERVED with no payment rail: %s", body)
	}
	if hdr.Get(x402.HeaderRequirements) != "" {
		t.Fatalf("an unenforceable price issued a challenge a client could satisfy: %s",
			hdr.Get(x402.HeaderRequirements))
	}
	if got := f.balance(buyerOrg, buyerOrg); got.Cmp(before) != 0 {
		t.Fatalf("a refused call moved money: %s → %s", before, got)
	}
	if got := f.balance(sellerOrg, f.wallet); !got.IsZero() {
		t.Fatalf("the seller was credited for a call that never ran: %s", got)
	}
}

// TestSplitFleetPriceOutageIsNotFree pins the fail-OPEN direction, which is the one
// that costs money. With the price table's process gone, the rail cannot learn what
// a tool costs — and an unknown price is never zero. The call is refused.
func TestSplitFleetPriceOutageIsNotFree(t *testing.T) {
	f := splitFleet(t)
	f.fund(buyerOrg, money.FromCents(100))
	before := f.balance(buyerOrg, buyerOrg)

	f.kill("marketplace")

	code, body, _ := f.call(buyerOrg, pricedTool, "")
	if code == http.StatusOK {
		t.Fatalf("a priced tool was SERVED while its price was unknowable: %s", body)
	}
	if got := f.balance(buyerOrg, buyerOrg); got.Cmp(before) != 0 {
		t.Fatalf("a refused call moved money: %s → %s", before, got)
	}
}

// TestSplitFleetProofIsNotABearerToken is the attack a settlement id invites. The
// id is keccak(payer address | nonce) and the proof rides a request HEADER, so a
// second tenant that captures one — a log, a proxy, a shared client — can replay it
// verbatim. Everything about it matches the recorded settlement except WHO it was
// for, and matching is what returns the original receipt and serves the tool.
//
// So the payer is part of the settlement's identity, and a different one is a replay
// rather than a retry: org B is refused, and A's ledger is untouched by B's attempt.
func TestSplitFleetProofIsNotABearerToken(t *testing.T) {
	const thief = "thieforg"
	f := splitFleet(t)
	f.fund(buyerOrg, money.FromCents(100))
	f.fund(thief, money.FromCents(100))
	if err := tools.Default().Activate(context.Background(), thief, "default", pricedTool, "u_"+thief); err != nil {
		t.Fatalf("activate for %s: %v", thief, err)
	}

	// The buyer pays for its own call, honestly.
	_, _, hdr := f.call(buyerOrg, pricedTool, "")
	proof := f.pay(f.challengeOf(hdr), newNonce(t))
	if code, body, _ := f.call(buyerOrg, pricedTool, proof); code != http.StatusOK {
		t.Fatalf("the honest paid call = %d (%s), want 200", code, body)
	}
	buyerAfter, sellerAfter := f.balance(buyerOrg, buyerOrg), f.balance(sellerOrg, f.wallet)

	// THE ATTACK: another org submits the SAME authorization, byte for byte.
	code, body, _ := f.call(thief, pricedTool, proof)
	if code == http.StatusOK {
		t.Fatalf("a captured X-Payment served another tenant's call: %s", body)
	}
	if got := f.balance(thief, thief); got.Cmp(money.FromCents(100)) != 0 {
		t.Fatalf("the replaying org was charged %s for a refused call", money.FromCents(100).Sub(got))
	}
	if got := f.balance(buyerOrg, buyerOrg); got.Cmp(buyerAfter) != 0 {
		t.Fatalf("the replay moved the REAL payer's money: %s → %s", buyerAfter, got)
	}
	if got := f.balance(sellerOrg, f.wallet); got.Cmp(sellerAfter) != 0 {
		t.Fatalf("the replay credited the seller a second time: %s → %s", sellerAfter, got)
	}
}

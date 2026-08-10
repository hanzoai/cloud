package cloud

// ONE OPERATION, FOUR DOORS, ONE CHARGE.
//
// prepaid_e2e_test.go proves the loop closes from a SIGNATURE over TCP: a token
// becomes a wallet and four writes spend it down. It proves it on one door, and
// that is the gap this file exists for — the operation it exercises is reachable
// three other ways, and until now every one of them was free. Not by decision:
// the gate read the path the TRANSPORT carried, and over MCP that path is /mcp
// while over the plane it is /.well-known/zip/op/<name>. Neither names a declared
// surface, so both priced at zero however the operation inside was declared.
//
// So: one typed op, registered once at a surface declared at 25c, reached over
// all four doors the fleet has, against a real double-entry ledger. Each door
// debits exactly 25c, and at zero each refuses without running the handler.
//
// WHAT IS REAL HERE. A real unix socket carrying real ZAP frames (no TCP, no
// loopback — the file is in the temp dir and the test reads it back). A real TCP
// listener for REST and for MCP tools/call. The real in-process CLI, dispatching
// through LocalInvoke. The real double-entry ledger, read back entry by entry.
// The metering client points at a commerce that FAILS THE TEST if it is ever
// asked for a balance over HTTP, so a debit that did not go through the
// co-resident ledger cannot pass as one that did.
//
// WHAT IS HANDED IN. The identity, as the gateway-minted headers — the same split
// prepaid_test.go and prepaid_e2e_test.go keep between them. Turning a signature
// into those headers is one question and it is answered next door; this file asks
// the other one, which is what happens to an operation AFTER the identity exists.
// The ZAP door carries the identity zip forwards for a caller with no inbound
// request (zip.WithCaller), which is exactly how an in-cluster hop states who it
// acts for.
//
// THE CONTROLS ARE THE POINT. An unpriced op and a READ of the priced surface run
// through all four doors and must move nothing — asserted after a re-fund, so
// neither can pass by being broke. A gate that fires where it should not is the
// same defect as one that never fires, and only a control catches it.

import (
	"github.com/hanzoai/cloud/internal/planetest"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// The three operations. They differ in exactly one respect each, so a difference
// in outcome has exactly one cause:
//
//	tollRun  — the priced surface, a WRITE.       money must move.
//	tollRead — the priced surface, a READ.        a read spends nothing (Consumes).
//	tollFree — an unpriced surface, a WRITE.      nothing was declared, nothing is owed.
const (
	tollRun   = "probe_run"
	tollRead  = "probe_list"
	tollFree  = "audit_write"
	tollPrice = 25
	tollFund  = 100

	tollOrg     = "acme" // a pooled tenant org: ledger and wallet are both "acme".
	tollUser    = "bob"
	tollService = "toll"
)

type tollIn struct {
	N int `json:"n"`
}

type tollOut struct {
	OK bool `json:"ok"`
}

// tollDoor is one way to reach an operation. Each returns whether the call was
// REFUSED and the sentence it was refused with — the two facts every door can
// answer, however differently it spells them on the wire.
type tollDoor struct {
	name string
	call func(t *testing.T, op string) (refused bool, detail string)
}

// tollRig is the binary's money edge as serve.go composes it, plus the ledger the
// debits land in and a count of how many times a handler actually ran.
type tollRig struct {
	led  *ledgerReader
	ran  *atomic.Int64
	base string
	sock string
	app  *zip.App
}

func tollApp(t *testing.T) *tollRig { return tollRigWith(t, true) }

// tollRigWith composes the rig with or without the HTTP edge gate. edge=false is
// not a variant anybody deploys — it is the question "what happens when the edge
// gate is not there", and the answer has to be "the op seam charges", never
// "nobody does".
func tollRigWith(t *testing.T, edge bool) *tollRig {
	t.Helper()
	t.Setenv(zip.RuntimeDirEnv, planetest.Dir(t))

	led := e2eLedger(t)
	index(t, &Config{},
		Plugin{Name: "probe", Price: tollPrice},
		Plugin{Name: "audit", Price: Free},
	)
	// Pin the declarations before spending a cent against them. Every number below
	// is arithmetic on these two.
	if got := PriceOf("/v1/probe/run").Cents(); got != tollPrice {
		t.Fatalf("the priced surface resolves to %dc, want %dc", got, tollPrice)
	}
	if got := PriceOf("/v1/audit/write").Cents(); got != 0 {
		t.Fatalf("the control surface resolves to %dc, want 0c — it is not a control if it costs money", got)
	}

	m := mustClient(t, forbiddenCommerce(t), false)
	var ran atomic.Int64
	app := zip.New(zip.Config{AppName: tollService})

	// The edge, in serve.go's order. SanitizeIdentity is deliberately absent: this
	// test hands the identity in as the headers a gateway mints, and what turns a
	// signature into those headers is prepaid_e2e_test.go's question.
	app.Use(Bridge())
	if edge {
		app.Use(BillingGate(m, DefaultPrice))
	}
	app.Use(SpendGate(nil))
	app.Use(DenyEnvelope())
	app.Authorize(Toll(m, nil))

	run := func(ctx context.Context, in *tollIn) (*tollOut, error) {
		ran.Add(1)
		return &tollOut{OK: true}, nil
	}
	zip.Post(app, "/v1/probe/run", run, zip.WithOperationID(tollRun))
	zip.Get(app, "/v1/probe/list", run, zip.WithOperationID(tollRead))
	zip.Post(app, "/v1/audit/write", run, zip.WithOperationID(tollFree))

	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })

	// A REAL unix socket, speaking ZAP. Not loopback with a different name: the
	// path below is a file, this test stats it, and nothing about the hop touches
	// a port.
	sock := zip.SocketPath(tollService)
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		fi, err := os.Stat(sock)
		if err == nil {
			if fi.Mode()&os.ModeSocket == 0 {
				t.Fatalf("%s is not a socket (mode %s)", sock, fi.Mode())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ZAP socket never appeared at %s", sock)
		}
		time.Sleep(2 * time.Millisecond)
	}

	return &tollRig{led: led, ran: &ran, base: "http://" + ln.Addr().String(), sock: sock, app: app}
}

// pathOf is the REST address of an operation — the only door that needs one,
// which is the whole reason the other three could go unbilled.
func pathOf(op string) (string, string) {
	switch op {
	case tollRun:
		return http.MethodPost, "/v1/probe/run"
	case tollRead:
		return http.MethodGet, "/v1/probe/list"
	default:
		return http.MethodPost, "/v1/audit/write"
	}
}

// identify stamps the gateway's assertion onto an outbound request.
func identify(h http.Header) {
	h.Set("X-Org-Id", tollOrg)
	h.Set("X-User-Id", tollUser)
	h.Set("X-User-Name", tollUser)
}

func (r *tollRig) doors() []tollDoor {
	return []tollDoor{
		{"REST", func(t *testing.T, op string) (bool, string) {
			t.Helper()
			method, path := pathOf(op)
			req, _ := http.NewRequest(method, r.base+path, strings.NewReader(`{"n":1}`))
			req.Header.Set("Content-Type", "application/json")
			identify(req.Header)
			res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				t.Fatalf("REST %s: %v", path, err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			return res.StatusCode != http.StatusOK, string(body)
		}},

		{"MCP", func(t *testing.T, op string) (bool, string) {
			t.Helper()
			frame, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": op, "arguments": map[string]any{"n": 1}},
			})
			req, _ := http.NewRequest(http.MethodPost, r.base+"/mcp", strings.NewReader(string(frame)))
			req.Header.Set("Content-Type", "application/json")
			identify(req.Header)
			res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				t.Fatalf("MCP %s: %v", op, err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			// A tools/call reports a handler failure as isError content, per the MCP
			// spec — the status is 200 either way, so the status is not the answer.
			var ans struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &ans); err != nil {
				t.Fatalf("MCP %s: reply is not a frame: %v (%s)", op, err, body)
			}
			text := ""
			if len(ans.Result.Content) > 0 {
				text = ans.Result.Content[0].Text
			}
			return ans.Result.IsError, text
		}},

		{"ZAP", func(t *testing.T, op string) (bool, string) {
			t.Helper()
			conn, err := zip.Dial(r.sock)
			if err != nil {
				t.Fatalf("dial %s: %v", r.sock, err)
			}
			ctx := zip.WithCaller(context.Background(), zip.Caller{
				Org: tollOrg, User: tollUser, Name: tollUser,
			})
			if _, err := zip.Call[tollIn, tollOut](ctx, conn, op, &tollIn{N: 1}); err != nil {
				return true, err.Error()
			}
			return false, ""
		}},

		{"CLI", func(t *testing.T, op string) (bool, string) {
			t.Helper()
			service, name, _ := strings.Cut(op, "_")
			cli := r.app.CLI()
			cli.Out = io.Discard
			ctx := zip.WithCaller(context.Background(), zip.Caller{
				Org: tollOrg, User: tollUser, Name: tollUser,
			})
			if err := cli.Run(ctx, []string{service, name, "--n", "1"}); err != nil {
				return true, err.Error()
			}
			return false, ""
		}},
	}
}

func (r *tollRig) fund(t *testing.T, cents int64, ref string) {
	t.Helper()
	if _, err := r.led.fin.Deposit(context.Background(), types.DepositInput{
		Org: tollOrg, Subject: tollOrg, Amount: money.FromCents(cents), Ref: ref,
	}); err != nil {
		t.Fatalf("fund: %v", err)
	}
}

func (r *tollRig) balance(t *testing.T) int64 {
	t.Helper()
	bal, err := r.led.fin.Balance(context.Background(), tollOrg, tollOrg, "usd", false)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return bal.Cents()
}

// settled waits for a debit to land. The edge gate records on a detached
// goroutine on purpose — a client disconnect must not cancel a debit for work
// already delivered — so waiting is the only honest way to observe it.
func (r *tollRig) settled(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := r.balance(t)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("balance settled at %dc, want %dc", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ── the charge: one operation, four doors, one debit each ────────────────────────

// TestTollChargesEveryDoorOnce is the whole claim. Each door is funded for exactly
// one call, spends it, and is then refused at zero without the handler running.
//
// The CLI is the door that refuses for a different reason, and that reason is a
// property rather than a shortfall: an in-process invoke carries no request, so
// there is no attested principal, no signed billing_account claim and therefore no
// wallet. There is nobody to charge. Serving it anyway is the exact leak this gate
// closes, so it is refused whether or not anyone is funded — which the funded leg
// below asserts explicitly.
func TestTollChargesEveryDoorOnce(t *testing.T) {
	for _, d := range tollApp(t).doors() {
		t.Run(d.name, func(t *testing.T) {
			r := tollApp(t)
			door := r.doors()[doorIndex(d.name)]

			r.fund(t, tollPrice, "fund-"+d.name)
			before := r.ran.Load()

			refused, detail := door.call(t, tollRun)
			if d.name == "CLI" {
				if !refused {
					t.Fatalf("a priced op ran from the command line with no principal behind it — " +
						"the binary charged nobody and served the work anyway")
				}
				if r.ran.Load() != before {
					t.Fatal("the handler ran for a caller with no wallet")
				}
				time.Sleep(150 * time.Millisecond)
				if got := r.balance(t); got != tollPrice {
					t.Fatalf("balance is %dc, want the %dc paid in — a refused call moved money", got, tollPrice)
				}
				return
			}

			if refused {
				t.Fatalf("%s: a funded caller was refused: %s", d.name, detail)
			}
			if r.ran.Load() != before+1 {
				t.Fatalf("%s: handler ran %d times, want 1", d.name, r.ran.Load()-before)
			}
			r.settled(t, 0)

			// ── at zero, REFUSE, and do not run ─────────────────────────────────
			ran := r.ran.Load()
			refused, detail = door.call(t, tollRun)
			if !refused {
				t.Fatalf("%s: the call on an empty wallet was served — a prepaid system that "+
					"serves on empty is a free tier by accident (%s)", d.name, detail)
			}
			if r.ran.Load() != ran {
				t.Fatalf("%s: the handler ran on an empty wallet", d.name)
			}
			if !strings.Contains(detail, "credits") && !strings.Contains(detail, "insufficient_balance") {
				t.Fatalf("%s: the refusal names no way to cure it: %q — a 402 a caller cannot "+
					"act on is a 500 with better manners", d.name, detail)
			}

			// ── the books ───────────────────────────────────────────────────────
			// One movement, one balanced entry: one deposit, one debit, at the
			// DECLARED price. A second debit here is the double-charge this seam's
			// stand-down rule exists to prevent.
			entries, err := r.led.fin.ListEntries(context.Background(), tollOrg, false, 0)
			if err != nil {
				t.Fatalf("read entries: %v", err)
			}
			deposits, debits := 0, 0
			for _, e := range entries {
				switch e.Kind {
				case finance.KindDeposit:
					deposits++
				case finance.KindUsage:
					debits++
					if e.Amount.Cents() != tollPrice {
						t.Errorf("usage entry %s = %dc, want the DECLARED %dc", e.ID, e.Amount.Cents(), tollPrice)
					}
				default:
					t.Errorf("entry %s has kind %q — an entry nobody can classify must not be in the books", e.ID, e.Kind)
				}
			}
			if deposits != 1 || debits != 1 {
				t.Fatalf("%s: books hold %d deposits and %d debits, want 1 and 1 — one call, one charge",
					d.name, deposits, debits)
			}
		})
	}
}

func doorIndex(name string) int {
	switch name {
	case "REST":
		return 0
	case "MCP":
		return 1
	case "ZAP":
		return 2
	default:
		return 3
	}
}

// ── the controls ────────────────────────────────────────────────────────────────

// TestTollMovesNothingForUnpricedWork is the control that makes the test above mean
// something. The same four doors, the same funded wallet, an UNPRICED operation and
// a READ of the PRICED one: both must run and neither must move a cent.
//
// The wallet is funded FIRST and checked after, so neither control can pass by being
// broke — the failure mode where "nothing was charged" and "nothing could have been
// charged" look identical.
func TestTollMovesNothingForUnpricedWork(t *testing.T) {
	for _, op := range []struct{ name, id string }{
		{"unpriced surface", tollFree},
		{"read of a priced surface", tollRead},
	} {
		t.Run(op.name, func(t *testing.T) {
			for _, d := range tollApp(t).doors() {
				t.Run(d.name, func(t *testing.T) {
					r := tollApp(t)
					door := r.doors()[doorIndex(d.name)]
					r.fund(t, tollFund, "fund-control")
					before := r.ran.Load()

					refused, detail := door.call(t, op.id)
					if refused {
						t.Fatalf("%s over %s was refused: %s — work that costs nothing was gated",
							op.name, d.name, detail)
					}
					if r.ran.Load() != before+1 {
						t.Fatalf("%s over %s: handler ran %d times, want 1", op.name, d.name, r.ran.Load()-before)
					}
					// Give an erroneous debit the same window a real one gets, then
					// assert nothing arrived.
					time.Sleep(150 * time.Millisecond)
					if got := r.balance(t); got != tollFund {
						t.Fatalf("%s over %s left the balance at %dc, want the %dc paid in — "+
							"something charged for work that costs nothing", op.name, d.name, got, tollFund)
					}
					entries, err := r.led.fin.ListEntries(context.Background(), tollOrg, false, 0)
					if err != nil {
						t.Fatalf("read entries: %v", err)
					}
					for _, e := range entries {
						if e.Kind == finance.KindUsage {
							t.Fatalf("%s over %s wrote a usage entry of %dc", op.name, d.name, e.Amount.Cents())
						}
					}
				})
			}
		})
	}
}

// TestTollTakesOverWhenTheEdgeIsGone pins the one property that makes two money
// seams safe to have at all.
//
// The op seam stands down for a request the edge gate has CLAIMED, and the first
// version of that check inferred the claim from the request's path: a path that
// names a declared surface must be one the edge answered for. True — and the truth
// of it depends on a line in a composition root two files away. Unmount BillingGate
// and the inference still says yes, the op seam still stands down, and a priced
// operation over REST becomes free with both gates present in the source. So the
// claim is now MADE, and this is the test that would have caught the difference:
// with no edge gate, REST must still be charged exactly once — by the op seam.
func TestTollTakesOverWhenTheEdgeIsGone(t *testing.T) {
	r := tollRigWith(t, false)
	r.fund(t, tollPrice, "fund-noedge")
	before := r.ran.Load()

	if refused, detail := r.doors()[doorIndex("REST")].call(t, tollRun); refused {
		t.Fatalf("a funded caller was refused with no edge gate mounted: %s", detail)
	}
	if r.ran.Load() != before+1 {
		t.Fatalf("handler ran %d times, want 1", r.ran.Load()-before)
	}
	if got := r.balance(t); got != 0 {
		t.Fatalf("balance is %dc, want 0c — with the edge gate unmounted NOTHING charged "+
			"the operation, which is the gap a stand-down that guesses would leave", got)
	}

	ran := r.ran.Load()
	refused, detail := r.doors()[doorIndex("REST")].call(t, tollRun)
	if !refused {
		t.Fatalf("the call on an empty wallet was served: %s", detail)
	}
	if r.ran.Load() != ran {
		t.Fatal("the handler ran on an empty wallet")
	}
}

// ── the seam itself ─────────────────────────────────────────────────────────────

// TestOperationIsTheSameValueAtEveryDoor is the measurement the whole design rests
// on, pinned so it cannot quietly stop being true: the operation zip hands the
// authorizer is the SAME value however the call arrived, while the request path —
// which is what the HTTP edge reads — is the operation on exactly one of the four.
//
// If this ever fails, the edge gate and the op gate are pricing two different
// things again, which is the bug.
func TestOperationIsTheSameValueAtEveryDoor(t *testing.T) {
	r := tollApp(t)
	r.fund(t, tollFund, "fund-measure")
	var ops, reqs []string
	// The observer REPLACES Toll for this test on purpose: what is being measured
	// is the VALUE zip hands the seam, not what cloud does with it.
	r.app.Authorize(func(ctx context.Context, op zip.Op, _ any) error {
		ops = append(ops, op.Method+" "+op.Path)
		if c, live := Request(ctx); live {
			reqs = append(reqs, c.Method()+" "+c.Path())
		} else {
			reqs = append(reqs, "(no request)")
		}
		return nil
	})
	for _, d := range r.doors() {
		if refused, detail := d.call(t, tollRun); refused {
			t.Fatalf("%s: %s", d.name, detail)
		}
	}
	for i, got := range ops {
		if got != "POST /v1/probe/run" {
			t.Errorf("door %d saw operation %q, want %q — the seam is not transport-agnostic",
				i, got, "POST /v1/probe/run")
		}
	}
	// And the counter-fact, stated out loud: three of the four requests are
	// envelopes that name no surface, which is exactly why the edge could not
	// price them.
	envelopes := 0
	for _, req := range reqs {
		path := strings.TrimPrefix(req, "POST ")
		if req == "(no request)" || !PriceOf(path).Declared() {
			envelopes++
		}
	}
	if envelopes != 3 {
		t.Fatalf("requests seen = %v; %d of them name no declared surface, want 3 — if the "+
			"edge can see every operation there is nothing for this seam to do", reqs, envelopes)
	}
}

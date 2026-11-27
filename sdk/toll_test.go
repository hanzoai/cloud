package cloud

// ONE OPERATION, FOUR TRANSPORTS, ONE CHARGE.
//
// prepaid_e2e_test.go proves the loop closes from a SIGNATURE over TCP: a token
// becomes a wallet and four writes spend it down. It proves it on one
// transport, and that is the gap this file exists for — the operation it
// exercises is reachable three other ways, and until now every one of them was
// free. Not by decision: the gate read the path the TRANSPORT carried, and over
// MCP that path is /mcp while over the plane it is /.well-known/zip/op/<name>.
// Neither names a declared surface, so both priced at zero however the operation
// inside was declared.
//
// So: one typed op, registered once at a surface declared at 25c, reached over
// all four transports the fleet has, against a real double-entry ledger. Each
// transport debits exactly 25c, and at zero each refuses without running the
// handler.
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
// into those headers is one question and it is answered there; this file asks
// the other one, which is what happens to an operation AFTER the identity exists.
// The ZAP transport carries the identity zip forwards for a caller with no
// inbound request (zip.WithCaller), which is exactly how an in-cluster hop
// states who it acts for.
//
// THE CONTROLS ARE THE POINT. An unpriced op and a READ of the priced surface run
// through all four transports and must move nothing — asserted after a re-fund,
// so neither can pass by being broke. A gate that fires where it should not is
// the same defect as one that never fires, and only a control catches it.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/money"
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

// tollEndpoint is one way to reach an operation. Each returns whether the call was
// REFUSED and the sentence it was refused with — the two facts every transport
// can answer, however differently it spells them on the wire.
type tollEndpoint struct {
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

// pathOf is the REST address of an operation — the only transport that needs
// one, which is the whole reason the other three could go unbilled.
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

func (r *tollRig) endpoints() []tollEndpoint {
	return []tollEndpoint{
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
			//
			// The `error` member is decoded too, and it is not decoration. A frame
			// carrying a JSON-RPC error carries NO `result`, so `Result.IsError`
			// decodes to FALSE — byte-identical to a success. A caller asking this
			// helper "was the op refused?" would read -32602 "no such tool" as an
			// ADMITTED CALL, which is the one answer a toll test must never get wrong:
			// the gate would look green because the name it guards does not exist.
			var ans struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &ans); err != nil {
				t.Fatalf("MCP %s: reply is not a frame: %v (%s)", op, err, body)
			}
			if ans.Error != nil {
				t.Fatalf("MCP %s: the server answered a protocol error, so this op was never invoked "+
					"and the toll was never asked: %d %s", op, ans.Error.Code, ans.Error.Message)
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
	if _, err := r.led.fin.Deposit(context.Background(), money.DepositInput{
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

func endpointIndex(name string) int {
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

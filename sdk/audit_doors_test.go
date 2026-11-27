package cloud

// ONE OPERATION, FOUR DOORS, ONE RECORD.
//
// toll_test.go asks what an operation COSTS however it was reached, and found
// three doors charging nothing because the gate read the path the transport
// carried. This asks the other question about the same value: what the
// tamper-evident trail SAYS happened. Measured before the fix, against a real
// recorder read back row by row, one op reached four ways wrote:
//
//	REST   "POST /v1/probe/run"                   resource {run}
//	MCP    "POST /mcp"                            resource {}
//	ZAP    "POST /.well-known/zip/op/probe_run"   resource {op}
//	CLI    nothing
//
// Two rows name the door. The MCP row is the one that matters most, because it
// is the door agents use and because every tools/call in the fleet writes those
// same four words — the record cannot say which operation an agent ran.
//
// WHAT IS REAL HERE. A real recorder on disk, queried back. A real TCP listener
// for REST and for MCP tools/call. A real unix socket carrying ZAP frames. The
// real in-process CLI. And the real rule the composition root installs — Rule,
// not a stand-in — so what is proven is that the program's own rule notes the
// operation, not that a helper could.
//
// THE CONTROLS ARE THE POINT. Naming the operation also means JUDGING the
// operation, so a safe read reached over MCP must now leave NO record where
// before the envelope's POST made one, and an admin read reached over MCP must
// leave one where before the admin clause never fired. A rule that records more
// is not the same as a rule that records right; only a control that must stay
// silent tells them apart.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type doorIn struct {
	N int `json:"n"`
}

type doorOut struct {
	OK bool `json:"ok"`
}

// doorRig is the trail's edge as serve.go composes it — AuditTrail behind
// Bridge, under the composition root's own Rule — plus the recorder the records
// land in.
type doorRig struct {
	rec  *audit.Recorder
	base string
	sock string
	app  *zip.App

	// What the handler saw of its own request: the route the transport matched,
	// and the operation Rule noted. Substitution turns on whether these differ,
	// so a test that only reads the record would be trusting the predicate it is
	// there to check.
	saw view
}

type view struct {
	route string
	op    zip.Op
	noted bool
}

func doorApp(t *testing.T) *doorRig {
	t.Helper()
	t.Setenv(zip.RuntimeDirEnv, kit.Sockets(t))

	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	const service = "probe"
	app := zip.New(zip.Config{AppName: service})
	app.Use(Bridge())
	app.Use(AuditTrail(rec))
	app.Authorize(Rule(nil, nil))
	// The witness for the door no middleware wraps, wired as serve.go wires it.
	app.OnResult(Trail(rec))

	rig := &doorRig{rec: rec}
	run := func(ctx context.Context, in *doorIn) (*doorOut, error) {
		if c, live := Request(ctx); live {
			op, ok := Noted(c)
			rig.saw = view{route: Matched(c), op: op, noted: ok}
		} else {
			rig.saw = view{}
		}
		return &doorOut{OK: true}, nil
	}
	app.Post("/v1/probe/run", run, zip.WithOperationID("probe_run"))
	app.Get("/v1/probe/list", run, zip.WithOperationID("probe_list"))
	app.Get("/v1/admin/orgs", run, zip.WithOperationID("admin_orgs"))
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })

	sp := zip.SocketPath(service)
	go func() { _ = app.Listen(sp) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if fi, err := os.Stat(sp); err == nil {
			if fi.Mode()&os.ModeSocket == 0 {
				t.Fatalf("%s is not a socket (mode %s)", sp, fi.Mode())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ZAP socket never appeared at %s", sp)
		}
		time.Sleep(2 * time.Millisecond)
	}
	rig.base = "http://" + ln.Addr().String()
	rig.sock = sp
	rig.app = app
	return rig
}

// restOf is the REST address of an operation — the only door that needs one,
// which is the whole reason the other three could go unnamed.
func restOf(op string) (string, string) {
	switch op {
	case "probe_run":
		return http.MethodPost, "/v1/probe/run"
	case "probe_list":
		return http.MethodGet, "/v1/probe/list"
	default:
		return http.MethodGet, "/v1/admin/orgs"
	}
}

func stampIdentity(h http.Header) {
	h.Set("X-Org-Id", "acme")
	h.Set("X-User-Id", "bob")
	h.Set("X-User-Name", "bob")
}

func doorCaller() context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob", Name: "bob"})
}

type door struct {
	name string
	hit  func(t *testing.T, r *doorRig, op string)
}

func doors() []door {
	return []door{
		{"REST", func(t *testing.T, r *doorRig, op string) {
			t.Helper()
			method, path := restOf(op)
			req, _ := http.NewRequest(method, r.base+path, strings.NewReader(`{"n":1}`))
			req.Header.Set("Content-Type", "application/json")
			stampIdentity(req.Header)
			res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				t.Fatalf("REST %s: %v", path, err)
			}
			defer res.Body.Close()
			_, _ = io.Copy(io.Discard, res.Body)
		}},

		{"MCP", func(t *testing.T, r *doorRig, op string) {
			t.Helper()
			frame, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": op, "arguments": map[string]any{"n": 1}},
			})
			req, _ := http.NewRequest(http.MethodPost, r.base+"/mcp", strings.NewReader(string(frame)))
			req.Header.Set("Content-Type", "application/json")
			stampIdentity(req.Header)
			res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				t.Fatalf("MCP %s: %v", op, err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			// A JSON-RPC error frame carries no result, so the op was never
			// invoked and nothing here was exercised. Reading that as a pass is
			// the one mistake this test must not make.
			var ans struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &ans); err != nil {
				t.Fatalf("MCP %s: reply is not a frame: %v (%s)", op, err, body)
			}
			if ans.Error != nil {
				t.Fatalf("MCP %s: protocol error, so the op never ran: %d %s",
					op, ans.Error.Code, ans.Error.Message)
			}
		}},

		{"ZAP", func(t *testing.T, r *doorRig, op string) {
			t.Helper()
			conn, err := zip.Dial(r.sock)
			if err != nil {
				t.Fatalf("dial %s: %v", r.sock, err)
			}
			if _, err := zip.Call[doorIn, doorOut](doorCaller(), conn, op, &doorIn{N: 1}); err != nil {
				t.Fatalf("ZAP %s: %v", op, err)
			}
		}},

		{"CLI", func(t *testing.T, r *doorRig, op string) {
			t.Helper()
			service, name, _ := strings.Cut(op, "_")
			cli := r.app.CLI()
			cli.Out = io.Discard
			if err := cli.Run(doorCaller(), []string{service, name, "--n", "1"}); err != nil {
				t.Fatalf("CLI %s: %v", op, err)
			}
		}},
	}
}

// written returns the records this call added, newest first.
func (r *doorRig) written(t *testing.T, before int) []audit.Record {
	t.Helper()
	// The append is on the request goroutine and completes before the response,
	// but the ZAP and CLI doors return through their own paths; give the row a
	// moment rather than racing the query.
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, _, err := r.rec.Query(context.Background(), audit.Filter{})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(rows) > before || time.Now().After(deadline) {
			return rows[:len(rows)-before]
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *doorRig) count(t *testing.T) int {
	t.Helper()
	_, total, err := r.rec.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return total
}

// A CHANGE IS NAMED BY THE OPERATION IT MADE, on every door that carries a
// request. The CLI is the stated bound and has its own test below.
func TestTheTrailNamesTheOperationNotTheDoor(t *testing.T) {
	for _, d := range doors() {
		if d.name == "CLI" {
			continue
		}
		t.Run(d.name, func(t *testing.T) {
			r := doorApp(t)
			before := r.count(t)
			d.hit(t, r, "probe_run")
			all := r.written(t, before)
			rows := outcomes(all)
			if len(all) != 2 || len(rows) != 1 {
				t.Fatalf("%s wrote %d records, want the change's begun record and its one outcome", d.name, len(all))
			}
			for _, b := range all {
				if b.Outcome.Result == begun && b.Action != "POST /v1/probe/run" {
					t.Errorf("%s recorded the change as begun under %q — the door, not the operation", d.name, b.Action)
				}
			}
			got := rows[0]
			if want := "POST /v1/probe/run"; got.Action != want {
				t.Errorf("%s recorded action %q, want %q — the record names the door, not the operation",
					d.name, got.Action, want)
			}
			if got.Resource.Type != "run" {
				t.Errorf("%s recorded resource type %q, want %q", d.name, got.Resource.Type, "run")
			}
			if got.Actor.Sub != "bob" {
				t.Errorf("%s recorded actor %q, want bob", d.name, got.Actor.Sub)
			}
		})
	}
}

// A SAFE READ REACHED OVER MCP LEAVES NO RECORD. The envelope is a POST, so a
// predicate reading the transport records it; the operation is a GET by a member
// of its own org, which is request-log noise and not a security event. This is
// the control that separates "records more" from "records right".
func TestASafeReadOverAnEnvelopeIsNotAnEvent(t *testing.T) {
	for _, d := range doors() {
		if d.name == "CLI" {
			continue
		}
		t.Run(d.name, func(t *testing.T) {
			r := doorApp(t)
			before := r.count(t)
			d.hit(t, r, "probe_list")
			time.Sleep(100 * time.Millisecond)
			if after := r.count(t); after != before {
				rows, _, _ := r.rec.Query(context.Background(), audit.Filter{})
				t.Fatalf("%s recorded a safe same-org read as %q — the envelope was judged, not the operation",
					d.name, rows[0].Action)
			}
		})
	}
}

// AN ADMIN READ REACHED OVER MCP IS AN EVENT. The admin clause reads the path,
// and over an envelope that path is /mcp — so an agent reading an admin surface
// through the door agents use passed the clause entirely. Judging the operation
// is what puts it back in scope, under its own name.
func TestAnAdminReadOverAnEnvelopeIsAnEvent(t *testing.T) {
	for _, d := range doors() {
		if d.name == "CLI" {
			continue
		}
		t.Run(d.name, func(t *testing.T) {
			r := doorApp(t)
			before := r.count(t)
			d.hit(t, r, "admin_orgs")
			rows := r.written(t, before)
			if len(rows) != 1 {
				t.Fatalf("%s wrote %d records for an admin read, want exactly 1", d.name, len(rows))
			}
			if want := "GET /v1/admin/orgs"; rows[0].Action != want {
				t.Errorf("%s recorded %q, want %q", d.name, rows[0].Action, want)
			}
		})
	}
}

// AND THE FOURTH DOOR. An in-process invoke carries no request, so no transport
// middleware wraps it and for a long time it left no trace of having run — an
// operator on a pod invoking a privileged operation, unrecorded. zip's OnResult
// is told the outcome of every invoke including that one, and Trail writes the
// record from it.
//
// It is told the OPERATION, which is the whole reason this is possible: there is
// no path to read, and the op is the same value here as at every other door.
func TestTheInProcessCLIIsRecordedToo(t *testing.T) {
	r := doorApp(t)
	before := r.count(t)
	for _, d := range doors() {
		if d.name == "CLI" {
			d.hit(t, r, "probe_run")
		}
	}
	rows := r.written(t, before)
	if len(rows) != 1 {
		t.Fatalf("an in-process invoke wrote %d records, want exactly 1", len(rows))
	}
	if want := "POST /v1/probe/run"; rows[0].Action != want {
		t.Errorf("recorded action %q, want %q", rows[0].Action, want)
	}
	if rows[0].Actor.Sub != "bob" {
		t.Errorf("recorded actor %q, want bob — the caller is the only identity an "+
			"in-process invoke has", rows[0].Actor.Sub)
	}
	// The witness that DID run is the only one: a request-carried call must not
	// also be recorded here, or one event gets two records that can disagree.
	before = r.count(t)
	for _, d := range doors() {
		if d.name == "REST" {
			d.hit(t, r, "probe_run")
		}
	}
	if rows := outcomes(r.written(t, before)); len(rows) != 1 {
		t.Fatalf("a REST call wrote %d outcome records, want exactly 1 — both witnesses ran", len(rows))
	}
}

package cloud

// ONE OPERATION, EVERY DOOR, ONE RECORD.
//
// A typed operation is reachable more than one way and only one of them puts the
// operation in the request path. The trail is written by transport middleware,
// which reads that path — so before this, one op registered once at
// /v1/probe/run wrote a different record per door:
//
//	REST   "POST /v1/probe/run"                   resource {run}
//	MCP    "POST /mcp"                            resource {}
//	ZAP    "POST /.well-known/zip/op/probe_run"   resource {op}
//	CLI    nothing
//
// Two rows name the door. The MCP row is the one that matters most, because it
// is the door agents use and because every tools/call writes those same four
// words — the record cannot say which operation an agent ran.
//
// WHAT IS REAL HERE. A recorder on disk, queried back. A TCP listener for REST
// and for MCP tools/call. A unix socket carrying ZAP frames. The in-process CLI.
// And the rule the composition root installs, not a stand-in.
//
// THE CONTROLS ARE THE POINT. Naming the operation also means JUDGING it, so a
// safe read reached over MCP must now leave NO record where the envelope's POST
// made one. A rule that records more is not the same as a rule that records
// right, and only a control that must stay silent tells them apart.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/audit"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type probeIn struct {
	N int `json:"n"`
}

type probeOut struct {
	OK bool `json:"ok"`
}

type doorRig struct {
	rec  *audit.Recorder
	base string
	sock string
	app  *zip.App
}

func doorApp(t *testing.T) *doorRig {
	t.Helper()
	// A SHORT socket directory. A unix socket path has a hard length limit that
	// the default temp dir on macOS exceeds, and the failure is a bind error at
	// the end of a passing-looking setup.
	run, err := os.MkdirTemp("/tmp", "z")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(run) })
	t.Setenv(zip.RuntimeDirEnv, run)

	// The trail is an ENCRYPTED store and refuses to open without a key, which is
	// the right default and is why every store test states one. A zero key is the
	// package idiom: what is under test here is what gets recorded, not the codec.
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))

	rec, err := audit.Open(t.TempDir()+"/audit.db", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	const service = "probe"
	app := zip.New(zip.Config{AppName: service})
	app.Use(Carry())
	app.Use(AuditTrail(rec))
	app.Authorize(Rule())

	run2 := func(ctx context.Context, in *probeIn) (*probeOut, error) { return &probeOut{OK: true}, nil }
	zip.Post[probeIn, probeOut](app, "/v1/probe/run", run2, zip.WithOperationID("probe_run"))
	zip.Get[probeIn, probeOut](app, "/v1/probe/list", run2, zip.WithOperationID("probe_list"))
	// ZAP FIRST, and the order is load-bearing. The MCP door and the op-call
	// plane are installed when the app is first served, so a handler taken before
	// that answers 404 on both — which reads exactly like the gap this file is
	// about. Serving the socket installs them; the TCP listener below then shares
	// the same prepared handler.
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })

	return &doorRig{rec: rec, base: "http://" + ln.Addr().String(), sock: sp, app: app}
}

func restOf(op string) (string, string) {
	if op == "probe_run" {
		return http.MethodPost, "/v1/probe/run"
	}
	return http.MethodGet, "/v1/probe/list"
}

func identify(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set("X-Org-Id", "acme")
	h.Set("X-User-Id", "bob")
	h.Set("X-User-Name", "bob")
}

func caller() context.Context {
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
			identify(req.Header)
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
			identify(req.Header)
			res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				t.Fatalf("MCP %s: %v", op, err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			// A JSON-RPC error frame carries no result, so the op never ran and
			// nothing here was exercised. Reading that as a pass is the one
			// mistake this test must not make.
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
			if _, err := zip.Call[probeIn, probeOut](caller(), conn, op, &probeIn{N: 1}); err != nil {
				t.Fatalf("ZAP %s: %v", op, err)
			}
		}},
		{"CLI", func(t *testing.T, r *doorRig, op string) {
			t.Helper()
			service, name, _ := strings.Cut(op, "_")
			cli := r.app.CLI()
			cli.Out = io.Discard
			if err := cli.Run(caller(), []string{service, name, "--n", "1"}); err != nil {
				t.Fatalf("CLI %s: %v", op, err)
			}
		}},
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

func (r *doorRig) written(t *testing.T, before int) []audit.Record {
	t.Helper()
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
			rows := r.written(t, before)
			if len(rows) != 1 {
				t.Fatalf("%s wrote %d records, want exactly 1", d.name, len(rows))
			}
			if want := "POST /v1/probe/run"; rows[0].Action != want {
				t.Errorf("%s recorded action %q, want %q — the record names the door, "+
					"not the operation", d.name, rows[0].Action, want)
			}
			if rows[0].Resource.Type != "run" {
				t.Errorf("%s recorded resource type %q, want %q",
					d.name, rows[0].Resource.Type, "run")
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
				t.Fatalf("%s recorded a safe same-org read as %q — the envelope was "+
					"judged, not the operation", d.name, rows[0].Action)
			}
		})
	}
}

// THE BOUND, MEASURED RATHER THAN ASSUMED. An in-process invoke has no request,
// so no transport middleware wraps it and nothing writes a record. Closing it
// needs a post-invoke hook in zip, because a record carries an outcome and
// Authorize runs before there is one. This test exists so the day that hook
// lands, it fails and says so.
func TestTheInProcessCLILeavesNoRecordYet(t *testing.T) {
	r := doorApp(t)
	before := r.count(t)
	for _, d := range doors() {
		if d.name == "CLI" {
			d.hit(t, r, "probe_run")
		}
	}
	time.Sleep(100 * time.Millisecond)
	if after := r.count(t); after != before {
		t.Fatalf("the in-process CLI now writes a record (%d -> %d). That is the fix, "+
			"not a regression: delete this test and assert the operation on the CLI "+
			"door in TestTheTrailNamesTheOperationNotTheDoor.", before, after)
	}
}

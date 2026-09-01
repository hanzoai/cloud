package fleet_test

// A subsystem this process already serves is reached IN MEMORY, and that is what
// this file proves — not by reading the code, but by making the dial impossible
// and requiring the answer anyway.
//
// The property is easy to assert falsely: point At at the app's real socket and
// the test passes whether or not the short-circuit exists, because the socket
// path reaches the same door. So At here names an address nothing is listening
// on. If ask() dials, it fails; the answer can only have come from memory.
//
// It is the same shape apps/commerce proves the plane's zip.Here with, for the
// same reason.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/fleet"
)

// servingApp brings an app up on the canonical socket for name, which is what
// makes zip.Serving(name) answer with it.
func servingApp(t *testing.T, name string) *zip.App {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())

	app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	zip.Get(app, "/v1/"+name+"/ping", func(context.Context, *struct{}) (*struct {
		OK bool `json:"ok"`
	}, error) {
		return &struct {
			OK bool `json:"ok"`
		}{OK: true}, nil
	})

	sock := zip.SocketPath(name)
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	deadline := time.Now().Add(5 * time.Second)
	for zip.Serving(name) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("%s never bound %s", name, sock)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return app
}

// deadEnd is an At naming an address nothing serves. A hop that dials it fails;
// that is the whole point.
func deadEnd(t *testing.T) fleet.At {
	t.Helper()
	dead := filepath.Join(t.TempDir(), "nothing-listens-here.sock")
	return func(string) (string, string, error) { return dead, "/mcp", nil }
}

func toolsList() *fasthttp.Request {
	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetBody([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	return req
}

func TestACoResidentSubsystemIsReachedWithoutDialing(t *testing.T) {
	servingApp(t, "probe")

	req := toolsList()
	defer fasthttp.ReleaseRequest(req)

	ans := fleet.Ask(context.Background(), deadEnd(t), []string{"probe"}, req)[0]
	if ans.Err != nil {
		t.Fatalf("a co-resident subsystem was dialed instead of called: %v", ans.Err)
	}
	if len(ans.Body) == 0 {
		t.Fatal("no answer from the in-memory door")
	}

	var frame struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(ans.Body, &frame); err != nil {
		t.Fatalf("the in-memory answer is not a frame: %v", err)
	}
	if len(frame.Result) == 0 {
		t.Fatalf("the door answered no result: %s", ans.Body)
	}
}

func TestAPeerThisProcessDoesNotServeIsStillDialed(t *testing.T) {
	// The control. Without it the test above passes for an implementation that
	// never dials at all, which would be a different defect wearing the same green.
	servingApp(t, "probe")

	req := toolsList()
	defer fasthttp.ReleaseRequest(req)

	ans := fleet.Ask(context.Background(), deadEnd(t), []string{"absent"}, req)[0]
	if ans.Err == nil {
		t.Fatal("a peer this process does not serve must be dialed, and that dial must fail here")
	}
}

func TestTheStatedCallerReachesTheInMemoryDoor(t *testing.T) {
	// Over the socket a child reads identity off the request's headers. In memory
	// there is no request, so the caller is STATED — and a stated caller is what
	// CallerOf answers when nothing else is there. If this regressed, a
	// co-resident subsystem would see an anonymous caller and its per-caller half
	// would answer nothing, which is the failure that looks most like success.
	servingApp(t, "probe")

	req := toolsList()
	defer fasthttp.ReleaseRequest(req)

	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1"})
	ans := fleet.Ask(ctx, deadEnd(t), []string{"probe"}, req)[0]
	if ans.Err != nil {
		t.Fatalf("stated caller did not reach the door: %v", ans.Err)
	}
	if got := zip.CallerOf(ctx).Org; got != "acme" {
		t.Fatalf("the caller this hop states is %q, not acme", got)
	}
}

func TestGraphReachesACoResidentSubsystemWithoutDialing(t *testing.T) {
	// The GraphQL hop carries an arbitrary REST address rather than a typed op, so
	// it cannot use zip.Here — it replays the request against the serving app's own
	// router. Same proof: the dial is impossible, so an answer can only be local.
	app := servingApp(t, "probe")

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.Header.SetMethod("GET")
	req.SetRequestURI("/v1/probe/ping")

	resp, err := fleet.ServeHere(app, req)
	if err != nil {
		t.Fatalf("serving in memory: %v", err)
	}
	defer fasthttp.ReleaseResponse(resp)

	if code := resp.StatusCode(); code != 200 {
		t.Fatalf("the live router answered %d, not 200: %s", code, resp.Body())
	}
	if body := string(resp.Body()); body != `{"ok":true}` {
		t.Fatalf("the in-memory answer is %q", body)
	}
}

func TestTheInMemoryRouterIsTheSERVEDOne(t *testing.T) {
	// The failure this forbids: reaching for App.Test, which prepares an app of its
	// own and can answer for routes the served one does not have. A route the app
	// never registered must 404 here exactly as it would over the socket.
	app := servingApp(t, "probe")

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.Header.SetMethod("GET")
	req.SetRequestURI("/v1/probe/route-nobody-registered")

	resp, err := fleet.ServeHere(app, req)
	if err != nil {
		t.Fatalf("serving in memory: %v", err)
	}
	defer fasthttp.ReleaseResponse(resp)

	if code := resp.StatusCode(); code != 404 {
		t.Fatalf("an unregistered route answered %d, not 404", code)
	}
}

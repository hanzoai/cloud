package client

// The graph's hop takes the same short-circuit the MCP hop does, and this proves
// the WIRING rather than the mechanism.
//
// An earlier pass tested serveHere directly and was green with the short-circuit
// in send() deleted — the helper worked and nothing called it. So this drives
// send() itself, with an At that names an address nothing serves: if the hop
// dials, it fails, and an answer can only have come from memory.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/kit"
)

func servingProbe(t *testing.T, name string) *zip.App {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))

	app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	app.Get("/v1/"+name+"/ping", func(context.Context, *struct{}) (*struct {
		OK bool `json:"ok"`
	}, error) {
		return &struct {
			OK bool `json:"ok"`
		}{OK: true}, nil
	})

	go func() { _ = app.Listen(zip.SocketPath(name)) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	deadline := time.Now().Add(5 * time.Second)
	for zip.Serving(name) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("%s never bound its socket", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return app
}

func TestSendAnswersFromMemoryWhenTheAppIsHere(t *testing.T) {
	servingProbe(t, "probe")

	dead := filepath.Join(kit.Sockets(t), "nothing-listens.sock")
	at := func(string) (string, string, error) { return dead, "/mcp", nil }

	ans := send(at, openapi.Field{App: "probe", Method: "GET", Path: "/v1/probe/ping"},
		"/v1/probe/ping", nil, nil)
	if ans.Err != nil {
		t.Fatalf("a co-resident subsystem was dialed instead of served: %v", ans.Err)
	}
	if got := string(ans.Body); got != `{"ok":true}` {
		t.Fatalf("the in-memory answer is %q", got)
	}
}

func TestSendStillDialsAPeerThisProcessDoesNotServe(t *testing.T) {
	// The control: without it the test above passes for a hop that never dials,
	// which is a different defect wearing the same green.
	servingProbe(t, "probe")

	dead := filepath.Join(kit.Sockets(t), "nothing-listens.sock")
	at := func(string) (string, string, error) { return dead, "/mcp", nil }

	ans := send(at, openapi.Field{App: "absent", Method: "GET", Path: "/v1/absent/ping"},
		"/v1/absent/ping", nil, nil)
	if ans.Err == nil {
		t.Fatal("a peer this process does not serve must be dialed, and that dial must fail here")
	}
	if got := fmt.Sprint(ans.Err); got == "" {
		t.Fatal("the dial failure must say why")
	}
}

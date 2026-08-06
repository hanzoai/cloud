package cloud

// SCRATCH probe — measures what the authorizer seam actually sees. Deleted after.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type probeIn struct {
	N int `json:"n"`
}
type probeOut struct {
	OK bool `json:"ok"`
}

func TestZZProbeSeam(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(zip.RuntimeDirEnv, dir)

	var seen atomic.Value // []string
	var ran atomic.Int64
	app := zip.New(zip.Config{AppName: "probeapp"})
	app.Use(Bridge())
	app.Authorize(func(ctx context.Context, op zip.Op, in any) error {
		line := fmt.Sprintf("op=%s method=%s path=%s", op.OperationID, op.Method, op.Path)
		if c, ok := Request(ctx); ok {
			line += " req=" + c.Method() + " " + c.Path()
		} else {
			line += " req=NONE"
		}
		cl := zip.CallerOf(ctx)
		line += fmt.Sprintf(" caller{org=%q user=%q name=%q}", cl.Org, cl.User, cl.Name)
		prev, _ := seen.Load().([]string)
		seen.Store(append(append([]string{}, prev...), line))
		return nil
	})
	zip.Post(app, "/v1/probe/run", func(ctx context.Context, in *probeIn) (*probeOut, error) {
		ran.Add(1)
		return &probeOut{OK: true}, nil
	}, zip.WithOperationID("probe_run"))

	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	base := "http://" + ln.Addr().String()

	post := func(path, body string) (int, string) {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "bob")
		req.Header.Set("X-User-Name", "bob")
		res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer res.Body.Close()
		b := make([]byte, 4096)
		n, _ := res.Body.Read(b)
		return res.StatusCode, string(b[:n])
	}

	// 1. REST
	code, body := post("/v1/probe/run", `{"n":1}`)
	t.Logf("REST      -> %d %s", code, body)

	// 2. MCP over HTTP
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "probe_run", "arguments": map[string]any{"n": 2}},
	})
	code, body = post("/mcp", string(frame))
	t.Logf("MCP-HTTP  -> %d %s", code, body)

	// 3. ZAP over a REAL unix socket.
	sock := zip.SocketPath("probeapp")
	go func() { _ = app.Listen(sock) }()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("socket at %s", sock)
	conn, err := zip.DialApp("probeapp")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob", Name: "bob"})
	out, err := zip.Call[probeIn, probeOut](ctx, conn, "probe_run", &probeIn{N: 3})
	t.Logf("ZAP-UDS   -> out=%+v err=%v", out, err)

	// 4. CLI LocalInvoke
	cli := app.CLI()
	cli.Out = os.Stderr
	err = cli.Run(ctx, []string{"probe", "run", "--n", "4"})
	t.Logf("CLI       -> err=%v", err)

	for _, l := range seen.Load().([]string) {
		t.Logf("SEEN: %s", l)
	}
	t.Logf("handler ran %d times", ran.Load())
	t.Logf("runtime dir %s contents:", dir)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		t.Logf("  %s", filepath.Join(dir, e.Name()))
	}
}

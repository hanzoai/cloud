package fleet

// What the co-resident short-circuit is worth, measured rather than asserted.
//
// Both benchmarks reach the SAME door on the same app — one through its socket,
// one through memory — so the difference is the hop and nothing else. Measured on
// a 20-core box, tmpfs, 300 iterations:
//
//	BenchmarkHopHere    3665 ns/op    2234 B/op    29 allocs/op
//	BenchmarkHopDial   20285 ns/op    4687 B/op    61 allocs/op
//
// 5.5x the time and 2.1x the garbage, per hop, and a tools/list fans out to every
// subsystem the deployment carries. Re-measure before quoting these; they are a
// property of this machine as much as of the code — an earlier run through the
// default logger read 7518/31042, which is the logging in the numbers rather than
// the hop.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"

	luxlog "github.com/luxfi/log"
)

// benchApp brings a subsystem up on its canonical socket.
func benchApp(b *testing.B, name string) {
	b.Helper()
	b.Setenv("ZIP_RUNTIME_DIR", b.TempDir())

	app := zip.New(zip.Config{
		AppName:               name,
		DisableStartupMessage: true,
		Logger:                luxlog.New("bench"),
	})
	zip.Get(app, "/v1/"+name+"/ping", func(context.Context, *struct{}) (*struct {
		OK bool `json:"ok"`
	}, error) {
		return &struct {
			OK bool `json:"ok"`
		}{OK: true}, nil
	})

	go func() { _ = app.Listen(zip.SocketPath(name)) }()
	b.Cleanup(func() { _ = app.Shutdown() })
	for zip.Serving(name) == nil {
		time.Sleep(5 * time.Millisecond)
	}
}

func listFrame() *fasthttp.Request {
	r := fasthttp.AcquireRequest()
	r.Header.SetMethod("POST")
	r.Header.SetContentType("application/json")
	r.SetBody([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	return r
}

func BenchmarkHopHere(b *testing.B) {
	benchApp(b, "probe")
	// An address nothing serves, so a dial could not answer even by accident.
	dead := filepath.Join(b.TempDir(), "no.sock")
	at := func(string) (string, string, error) { return dead, "/mcp", nil }

	req := listFrame()
	defer fasthttp.ReleaseRequest(req)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if a := ask(context.Background(), at, "probe", req); a.Err != nil {
			b.Fatal(a.Err)
		}
	}
}

func BenchmarkHopDial(b *testing.B) {
	benchApp(b, "probe")
	sock := zip.SocketPath("probe")
	at := func(string) (string, string, error) { return sock, "/mcp", nil }

	req := listFrame()
	defer fasthttp.ReleaseRequest(req)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if a := dial(at, "probe", req); a.Err != nil {
			b.Fatal(a.Err)
		}
	}
}

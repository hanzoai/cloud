package plane

// The defect these pin: a plane call was cut off at the TRANSPORT's 30s
// response-read default while the caller had given itself 110s. The work
// succeeded — production measured a turn at 41,153ms answered 200 by both `ai`
// and `agents` — and the caller had already hung up. Every log on the callee
// side said healthy; only the caller reported failure.
//
// A unit test cannot hold a socket open for 40 seconds to prove this, so these
// assert the two properties that make the failure impossible instead: the
// ceiling exceeds the longest real caller budget, and the registration actually
// takes effect on a dialled transport.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// The number that mattered. zap-proto/http's default is 30s; the chat bridge
// budgets 110s for one turn. A ceiling below the caller's own budget means the
// caller can never spend it — which is exactly what shipped.
func TestTheCeilingExceedsTheLongestCallerBudget(t *testing.T) {
	const transportDefault = 30 * time.Second
	const bridgeAgentTimeout = 110 * time.Second // apps/integrations/channel.go

	if ReadCeiling <= transportDefault {
		t.Fatalf("ReadCeiling %v does not widen the %v default", ReadCeiling, transportDefault)
	}
	if ReadCeiling <= bridgeAgentTimeout {
		t.Errorf("ReadCeiling %v is at or below the chat bridge's own budget %v — "+
			"a caller must expire on ITS deadline, never on the transport's",
			ReadCeiling, bridgeAgentTimeout)
	}
}

// Network is copied from zip because zip keeps it unexported. A copy that
// drifts would send a unix path to a tcp dialler, which fails at the connection
// rather than anywhere legible — so pin the rule.
func TestNetworkOfMatchesZipsRule(t *testing.T) {
	for addr, want := range map[string]string{
		"/var/lib/cloud/run/agents.sock": "unix",
		"./relative.sock":                "unix",
		"@abstract":                      "unix",
		"127.0.0.1:8000":                 "tcp",
		"example.com:443":                "tcp",
	} {
		if got := Network(addr); got != want {
			t.Errorf("Network(%q) = %q, want %q", addr, got, want)
		}
	}
}

// A CALL WITH A DEADLINE WAITS FOR IT, past the ceiling; a call without one
// stops at the ceiling. The ceiling here is 100ms and the peer answers at 300ms.
func TestADeadlineSizesTheWait(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "p.sock")
	srv := Transport().Serve(sock, func(c *fasthttp.RequestCtx) {
		time.Sleep(300 * time.Millisecond)
		c.SetBodyString("done")
	})
	go func() { _ = srv.ListenAndServe() }()
	defer func() { _ = srv.Close() }()
	for i := 0; i < 100; i++ {
		if up, _ := Listening(sock); up {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c := dial(sock, 100*time.Millisecond)
	defer c.CloseIdleConnections()
	call := func(ctx context.Context) (string, error) {
		req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI("http://peer/")
		err := c.DoContext(ctx, req, resp)
		return string(resp.Body()), err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if body, err := call(ctx); err != nil || body != "done" {
		t.Fatalf("a call with a 5s deadline got %q, %v — the 100ms ceiling cut it", body, err)
	}
	if _, err := call(context.Background()); err == nil {
		t.Fatal("a call with no deadline outlived the ceiling")
	}
}

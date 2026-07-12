package git

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	zap "github.com/zap-proto/go"
	zaprpc "github.com/zap-proto/go/rpc"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/zapface"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// zapface reply field offsets — the wire contract zapface/wire.go encodes. Kept
// here (not imported — they are internal to zapface) so the test asserts the
// exact bytes a ZAP client reads.
const (
	zapReplyOkOff        = 0
	zapReplyStatusOff    = 4
	zapReplyResultOff    = 8
	zapReplyErrorJSONOff = 16
	zapReqMethodOff      = 0
	zapReqPayloadOff     = 8
	zapReqFixedSize      = 16
)

// mountZapApp mounts git AND the shared zapface /zap plane on one app, with a
// tiny identity shim that maps `Authorization: Bearer test-<org>` to the
// X-Org-Id / X-User-Id headers the gateway/identity middleware would mint in
// production. This is the honest stand-in for the gateway: the ZAP frame really
// traverses the /zap plane into /v1/git/zap/*, and the org is resolved by the
// SAME principal.Org the REST path uses.
func mountZapApp(t *testing.T) (base string, stop func()) {
	t.Helper()
	t.Setenv("CLOUD_GIT_SSH_ADDR", "127.0.0.1:0")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})

	// Identity shim (stands in for the gateway + SanitizeIdentity middleware):
	// derive X-Org-Id/X-User-Id from a "Bearer test-<org>" credential, written
	// onto the REQUEST headers so the downstream handler's principal.Org (which
	// reads X-Org-Id/X-User-Id off the request) sees a validated principal. The
	// zapface bridge replays the WS-upgrade Authorization header on every /v1
	// dispatch, so this runs for ZAP calls exactly as for REST. Registered on the
	// raw fiber app to reach the raw request headers (zip.Ctx only exposes
	// response-header writes), before Mount so it precedes the git routes.
	app.Fiber().Use(func(c fiber.Ctx) error {
		if h := c.Get("Authorization"); strings.HasPrefix(h, "Bearer test-") {
			org := strings.TrimPrefix(h, "Bearer test-")
			c.Request().Header.Set("X-Org-Id", org)
			c.Request().Header.Set("X-User-Id", "u_"+org)
		}
		return c.Next()
	})

	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: "api.hanzo.test"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// The shared ZAP-over-WebSocket plane — the SAME one serve.go mounts. It
	// bridges ZAP frames onto this app's /v1 routes, so git/zap/* are procedures.
	app.Get("/zap", zapface.Handler(app.Fiber(), zapface.Options{Logger: luxlog.New("test")}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Fiber().Listener(ln) }()
	addr := ln.Addr().String()
	waitTCP(t, addr)
	t.Cleanup(func() { _ = Shutdown() })
	return addr, func() { _ = app.Fiber().Shutdown() }
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener %s never came up", addr)
}

// zapReply is the decoded reply the client reads off the wire.
type zapReply struct {
	ok        bool
	status    uint32
	result    string
	errorJSON string
}

// zapCall drives one ZAP procedure over the WebSocket and returns the decoded
// reply — the browser/service ZAP client path, byte-for-byte.
func zapCall(t *testing.T, c *websocket.Conn, ctx context.Context, method string, input any, promiseID uint32) zapReply {
	t.Helper()
	var payload string
	if input != nil {
		j, _ := json.Marshal(input)
		payload = `{"json":` + string(j) + `}` // SuperJSON wrap
	}
	inner := buildZapInner(method, payload)
	frame := zaprpc.BuildRequest(zaprpc.Call{Method: 1, PromiseID: promiseID, Payload: inner})
	if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	resp, err := zaprpc.ParseResponse(data)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.PromiseID != promiseID {
		t.Fatalf("promiseID echo = %d, want %d", resp.PromiseID, promiseID)
	}
	m, err := zap.Parse(resp.Body)
	if err != nil {
		t.Fatalf("parse reply body: %v", err)
	}
	r := m.Root()
	return zapReply{
		ok:        r.Bool(zapReplyOkOff),
		status:    r.Uint32(zapReplyStatusOff),
		result:    r.Text(zapReplyResultOff),
		errorJSON: r.Text(zapReplyErrorJSONOff),
	}
}

// buildZapInner builds the inner {method, payload} request object zapface
// decodes (wire.go reqMethodOff=0 / reqPayloadOff=8 / fixed 16).
func buildZapInner(method, payload string) []byte {
	b := zap.NewBuilder(len(method) + len(payload) + zapReqFixedSize + 64)
	ob := b.StartObject(zapReqFixedSize)
	ob.SetText(zapReqMethodOff, method)
	ob.SetText(zapReqPayloadOff, payload)
	ob.FinishAsRoot()
	return b.Finish()
}

// unwrapResult unwraps the SuperJSON {"json":V} the reply result carries.
func unwrapResult(s string) []byte {
	var env struct {
		JSON json.RawMessage `json:"json"`
	}
	if err := json.Unmarshal([]byte(s), &env); err == nil && len(env.JSON) > 0 {
		return env.JSON
	}
	return []byte(s)
}

// TestZAPControlPlaneRoundTrip is the ZAP proof: createRepo + listRepos over the
// shared /zap WebSocket plane hit the SAME core funcs the REST handlers call, and
// a ZAP-created repo is then visible over the REST list — one implementation,
// two transports.
func TestZAPControlPlaneRoundTrip(t *testing.T) {
	base, stop := mountZapApp(t)
	defer stop()
	wsURL := fmt.Sprintf("ws://%s/zap", base)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer test-acme"}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.CloseNow()

	// 1) createRepo over ZAP.
	rep := zapCall(t, c, ctx, "git/zap/createRepo", map[string]any{"name": "zapsvc"}, 1)
	if !rep.ok || rep.status != http.StatusOK {
		t.Fatalf("createRepo: ok=%v status=%d err=%s", rep.ok, rep.status, rep.errorJSON)
	}
	var created repoView
	if err := json.Unmarshal(unwrapResult(rep.result), &created); err != nil {
		t.Fatalf("decode createRepo result: %v (%s)", err, rep.result)
	}
	if created.Org != "acme" || created.Name != "zapsvc" {
		t.Fatalf("unexpected repo: %+v", created)
	}
	if created.CloneURL != "https://api.hanzo.test/v1/git/acme/zapsvc.git" {
		t.Fatalf("unexpected cloneUrl: %q", created.CloneURL)
	}
	if !strings.HasPrefix(created.SSHURL, "git@git.hanzo.test:acme/zapsvc.git") {
		t.Fatalf("unexpected sshUrl: %q", created.SSHURL)
	}

	// 2) listRepos over ZAP — sees the ZAP-created repo.
	rep = zapCall(t, c, ctx, "git/zap/listRepos", map[string]any{}, 2)
	if !rep.ok {
		t.Fatalf("listRepos !ok: %s", rep.errorJSON)
	}
	var listed []repoView
	if err := json.Unmarshal(unwrapResult(rep.result), &listed); err != nil {
		t.Fatalf("decode listRepos: %v (%s)", err, rep.result)
	}
	if len(listed) != 1 || listed[0].Name != "zapsvc" {
		t.Fatalf("listRepos over ZAP = %+v", listed)
	}

	// 3) Cross-transport proof: the ZAP-created repo is present in the SAME store
	// the REST handler reads, via the SAME core func (coreList) the REST list
	// handler calls. One implementation, two transports.
	out, err := coreList(mounted, context.Background(), "acme", "")
	if err != nil {
		t.Fatalf("core list: %v", err)
	}
	if len(out) != 1 || out[0].Name != "zapsvc" {
		t.Fatalf("REST/core view of ZAP-created repo = %+v", out)
	}

	// 4) A bad createRepo over ZAP maps the core error to a non-ok reply (the ZAP
	// twin of the REST 400) — same core validation, different wire shape.
	rep = zapCall(t, c, ctx, "git/zap/createRepo", map[string]any{"name": "bad name!"}, 3)
	if rep.ok {
		t.Fatalf("createRepo with invalid name should not be ok: %+v", rep)
	}
}

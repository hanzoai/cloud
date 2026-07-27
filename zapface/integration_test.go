package zapface

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

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// startZapApp boots a zip app that mounts a /v1 surface (exactly
// how cloud's `ai` subsystem mounts the beego handler at bare /v1/*) and the
// /zap ZAP-over-WebSocket face, listening on an ephemeral port. Returns the base
// host:port and a stop func.
func startZapApp(t *testing.T) (string, func()) {
	t.Helper()
	app := zip.New(zip.Config{})

	// /v1 handlers (mirror ai/mount.go: app.All("/v1/*", ...)). The surface is
	// RESTful, so these switch on METHOD AND PATH — the same pair the dispatcher
	// now has to carry, and the reason it can no longer infer a verb from a name.
	app.All("/v1/*", func(c *zip.Ctx) error {
		path, method := c.Path(), c.Method()
		switch {
		case method == "GET" && strings.HasSuffix(path, "/ai/providers/global"):
			return c.JSON(200, fiber.Map{
				"status": "ok", "msg": "",
				"data": []fiber.Map{
					{"owner": "admin", "name": "openai", "category": "Model", "_cookie": c.Header("Cookie")},
				},
			})
		case method == "GET" && strings.HasSuffix(path, "/ai/providers"):
			return c.JSON(200, fiber.Map{
				"status": "ok", "msg": "",
				"data":  []fiber.Map{{"owner": c.Query("owner"), "name": "p1"}},
				"data2": 1,
			})
		case method == "POST" && strings.HasSuffix(path, "/ai/providers"):
			body := map[string]any{}
			_ = json.Unmarshal(c.Body(), &body)
			return c.JSON(200, fiber.Map{"status": "ok", "msg": "added " + asString(body["name"])})
		case method == "DELETE" && strings.Contains(path, "/ai/providers/"):
			return c.JSON(200, fiber.Map{"status": "ok", "msg": "deleted " + path})
		default:
			return c.JSON(404, fiber.Map{"status": "error", "msg": "not found"})
		}
	})

	// The /zap face under test, dispatching into THIS app's /v1 surface.
	app.Get("/zap", Handler(app.Fiber(), Options{}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Fiber().Listener(ln) }()

	// Wait until the listener answers.
	addr := ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return addr, func() { _ = app.Fiber().Shutdown() }
}

// TestEndToEndOverWebSocket drives a real ZAP frame through the full server
// path: native Fiber WS upgrade (zip/wsx) -> mintCap -> rpc.ParseRequest ->
// dispatch -> the app's /v1/* mount -> /v1 envelope ->
// rpc.BuildResponse -> WS reply.
func TestEndToEndOverWebSocket(t *testing.T) {
	addr, stop := startZapApp(t)
	defer stop()
	wsURL := fmt.Sprintf("ws://%s/zap", addr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{"iam_access_token=abc123"}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.CloseNow()

	call := func(method string, input any, promiseID uint32) zapReply {
		var payload string
		if input != nil {
			j, _ := json.Marshal(input)
			payload = superJSONWrap(j)
		}
		inner := buildInnerZapRequest(method, payload)
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
			ok:        r.Bool(replyOkOff),
			status:    r.Uint32(replyStatusOff),
			result:    r.Text(replyResultOff),
			errorJSON: r.Text(replyErrorJSONOff),
		}
	}

	// 1) A global listing -> ok, real data, cookie replayed to the /v1 handler.
	rep := call("GET ai/providers/global", nil, 1)
	if !rep.ok || rep.status != 200 {
		t.Fatalf("GET ai/providers/global: ok=%v status=%d err=%s", rep.ok, rep.status, rep.errorJSON)
	}
	var providers []map[string]any
	if err := json.Unmarshal(superJSONUnwrap(rep.result), &providers); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(providers) != 1 || providers[0]["name"] != "openai" {
		t.Fatalf("providers = %v", providers)
	}
	if providers[0]["_cookie"] != "iam_access_token=abc123" {
		t.Fatalf("cookie not replayed to /v1 handler: %v", providers[0]["_cookie"])
	}

	// 2) A collection list with a query param.
	rep = call("GET ai/providers", map[string]any{"owner": "admin"}, 2)
	if !rep.ok {
		t.Fatalf("GET ai/providers !ok: %s", rep.errorJSON)
	}
	_ = json.Unmarshal(superJSONUnwrap(rep.result), &providers)
	if providers[0]["owner"] != "admin" {
		t.Fatalf("owner query not forwarded: %v", providers)
	}

	// 3) Create (POST body).
	rep = call("POST ai/providers", map[string]any{"owner": "admin", "name": "claude"}, 3)
	if !rep.ok {
		t.Fatalf("POST ai/providers !ok: %s", rep.errorJSON)
	}

	// 4) A DELETE reaches the DELETE route — the case the old prefix heuristic
	// got wrong: it had no "get-" prefix, so it would have been sent as a POST
	// and silently landed on the create route instead of destroying anything.
	rep = call("DELETE ai/providers/admin/openai", nil, 4)
	if !rep.ok {
		t.Fatalf("DELETE ai/providers/admin/openai !ok: %s", rep.errorJSON)
	}

	// 5) unknown path -> /v1 404 envelope -> !ok.
	rep = call("GET ai/nonexistent", nil, 5)
	if rep.ok {
		t.Fatalf("unknown path should not be ok")
	}

	// 6) A method with NO verb is refused outright, not guessed at.
	rep = call("get-providers", nil, 6)
	if rep.ok {
		t.Fatalf("a verbless method must be refused, not inferred")
	}
	if !strings.Contains(rep.errorJSON, "verb is required") {
		t.Fatalf("refusal should name the missing verb, got %s", rep.errorJSON)
	}
}

// TestUpgradeRejectedWithoutAuth proves mintCap fails closed: no cookie and no
// bearer on the upgrade -> HTTP 401, no WebSocket.
func TestUpgradeRejectedWithoutAuth(t *testing.T) {
	addr, stop := startZapApp(t)
	defer stop()
	wsURL := fmt.Sprintf("ws://%s/zap", addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL, nil) // no cookie, no bearer
	if err == nil {
		t.Fatalf("expected upgrade rejection, got a connection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("want HTTP 401 on unauth upgrade, got %d (err=%v)", got, err)
	}
}

func buildInnerZapRequest(method, payload string) []byte {
	b := zap.NewBuilder(len(method) + len(payload) + reqFixedSize + 64)
	ob := b.StartObject(reqFixedSize)
	ob.SetText(reqMethodOff, method)
	ob.SetText(reqPayloadOff, payload)
	ob.FinishAsRoot()
	return b.Finish()
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

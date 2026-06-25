package zapface

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	zap "github.com/zap-proto/go"
	zaprpc "github.com/zap-proto/go/rpc"

	fiber "github.com/gofiber/fiber/v3"
)

// TestEndToEndOverWebSocket drives a real ZAP frame through the full server
// path: WS upgrade -> mintCap -> rpc.ParseRequest -> dispatch -> a Fiber app
// that mounts a casibase-shaped /v1 handler (exactly how cloud's `ai` subsystem
// mounts the beego handler at bare /v1/*) -> rpc.BuildResponse -> WS reply.
//
// This proves the Go side end-to-end in-process: the same wire console2 emits,
// the same dispatch-into-/v1 reuse, the casibase {status,msg,data} envelope
// translated to a ZapReply the browser decodes.
func TestEndToEndOverWebSocket(t *testing.T) {
	// A Fiber app standing in for cloud's app, with the casibase /v1 surface
	// mounted at bare /v1/* (mirrors ai/mount.go: app.All("/v1/*", ...)).
	app := fiber.New()
	app.All("/v1/*", func(c fiber.Ctx) error {
		switch {
		case strings.HasSuffix(c.Path(), "/get-global-providers"):
			// Echo the cookie back so the test can assert auth replay works.
			return c.Status(200).JSON(fiber.Map{
				"status": "ok",
				"msg":    "",
				"data": []fiber.Map{
					{"owner": "admin", "name": "openai", "category": "Model", "_cookie": c.Get("Cookie")},
				},
			})
		case strings.HasSuffix(c.Path(), "/get-providers"):
			return c.Status(200).JSON(fiber.Map{
				"status": "ok", "msg": "",
				"data":  []fiber.Map{{"owner": c.Query("owner"), "name": "p1"}},
				"data2": 1,
			})
		case strings.HasSuffix(c.Path(), "/add-provider"):
			body := map[string]any{}
			_ = json.Unmarshal(c.Body(), &body)
			return c.Status(200).JSON(fiber.Map{"status": "ok", "msg": "added " + asString(body["name"])})
		default:
			return c.Status(404).JSON(fiber.Map{"status": "error", "msg": "not found"})
		}
	})

	// Stand up the zapface WS handler against this Fiber app.
	srv := httptest.NewServer(Handler(app, Options{OriginPatterns: []string{"*"}}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/zap"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect WITH a cookie header (the auth slot mintCap requires).
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{"casdoor_session_id=abc123"}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.CloseNow()

	// Helper: send one ZAP call (method+input), get the decoded ZapReply.
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

	// 1) get-global-providers -> ok, real data, and cookie was replayed.
	rep := call("get-global-providers", nil, 1)
	if !rep.ok || rep.status != 200 {
		t.Fatalf("get-global-providers: ok=%v status=%d err=%s", rep.ok, rep.status, rep.errorJSON)
	}
	var providers []map[string]any
	if err := json.Unmarshal(superJSONUnwrap(rep.result), &providers); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(providers) != 1 || providers[0]["name"] != "openai" {
		t.Fatalf("providers = %v", providers)
	}
	if providers[0]["_cookie"] != "casdoor_session_id=abc123" {
		t.Fatalf("cookie not replayed to /v1 handler: %v", providers[0]["_cookie"])
	}

	// 2) get-providers with a query param -> ok.
	rep = call("get-providers", map[string]any{"owner": "admin"}, 2)
	if !rep.ok {
		t.Fatalf("get-providers !ok: %s", rep.errorJSON)
	}
	_ = json.Unmarshal(superJSONUnwrap(rep.result), &providers)
	if providers[0]["owner"] != "admin" {
		t.Fatalf("get-providers owner query not forwarded: %v", providers)
	}

	// 3) add-provider (POST body) -> ok, msg echoes the resource name.
	rep = call("add-provider", map[string]any{"owner": "admin", "name": "claude"}, 3)
	if !rep.ok {
		t.Fatalf("add-provider !ok: %s", rep.errorJSON)
	}

	// 4) unknown method -> casibase 404 envelope -> !ok.
	rep = call("get-nonexistent", nil, 4)
	if rep.ok {
		t.Fatalf("unknown method should not be ok")
	}
}

// TestUpgradeRejectedWithoutAuth proves mintCap fails closed: no cookie and no
// bearer on the upgrade -> HTTP 401, no WebSocket.
func TestUpgradeRejectedWithoutAuth(t *testing.T) {
	app := fiber.New()
	srv := httptest.NewServer(Handler(app, Options{OriginPatterns: []string{"*"}}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/zap"

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

// buildInnerZapRequest builds console2's inner {method@0,payload@8} struct.
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

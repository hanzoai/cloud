// clawdev runs the claw surface behind a real listener for a live drive of the
// protocol. It stands in for exactly two things a production deployment has and
// a laptop does not: hanzoai/gateway, which stamps the validated identity
// headers claw reads, and a static origin for OpenClaw's built control UI.
// Everything else is the real mount.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/bot"
	"github.com/hanzoai/cloud/clients/claw"
	"github.com/hanzoai/cloud/types"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// echoAI answers a prompt without reaching a model, so a live drive exercises
// the turn rather than the network. It is the AIClient seam and nothing else.
type echoAI struct{ delay time.Duration }

func (e echoAI) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(e.delay):
	}
	last := req.Prompt
	if i := strings.LastIndex(last, "\n"); i >= 0 {
		last = last[i+1:]
	}
	return &types.ChatResponse{
		Content:          "You said: " + strings.TrimSpace(last) + ". This reply came from clawdev, not a model.",
		PromptTokens:     len(req.Prompt) / 4,
		CompletionTokens: 16,
	}, nil
}

func (e echoAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }

func main() {
	addr := flag.String("listen", "127.0.0.1:8899", "listen address")
	dir := flag.String("data", "", "data dir (default: a temp dir)")
	ui := flag.String("ui", "/Users/z/work/openclaw/openclaw/dist/control-ui", "control UI build")
	org := flag.String("org", "acme", "the org the stand-in gateway stamps")
	flag.Parse()

	data := *dir
	if data == "" {
		var err error
		if data, err = os.MkdirTemp("", "clawdev"); err != nil {
			die(err)
		}
	}
	fmt.Println("data dir:", data)

	app := zip.New(zip.Config{AppName: "clawdev", DisableStartupMessage: true})

	// The stand-in for hanzoai/gateway: strip anything a client sent, stamp the
	// validated identity. In production this comes from an IAM JWT.
	app.Use(func(c *zip.Ctx) error {
		h := &c.Fiber().Request().Header
		for _, k := range []string{"X-Org-Id", "X-User-Id", "X-User-IsAdmin", "X-User-IsOrgAdmin", "X-User-Owner"} {
			h.Del(k)
		}
		h.Set("X-Org-Id", *org)
		h.Set("X-User-Id", "z@"+*org)
		h.Set("X-User-Owner", *org)
		h.Set("X-User-IsOrgAdmin", "true")
		return c.Next()
	})

	deps := cloud.Deps{
		Logger:         luxlog.NewWriter(os.Stderr),
		DataDir:        data,
		Version:        "clawdev",
		AI:             echoAI{delay: 150 * time.Millisecond},
		AIDefaultModel: "clawdev-echo",
	}
	if err := bot.Mount(app, deps); err != nil {
		die(err)
	}
	if err := claw.Mount(app, deps); err != nil {
		die(err)
	}

	// The control UI's own origin. The bundle asks for ws://{host}{basePath},
	// so it is served at the claw path: the socket is then the same address.
	serveUI(app, *ui)

	fmt.Println("claw ws:   ws://" + *addr + "/v1/claw")
	fmt.Println("control ui: http://" + *addr + "/v1/claw/")
	if err := app.Fiber().Listen(*addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		die(err)
	}
}

func serveUI(app *zip.App, root string) {
	index, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "no control UI at", root, "-", err)
		return
	}
	// The bundle infers its base path from the document's own, and asks for the
	// boot config beside it.
	shell := strings.Replace(string(index), "<html ", `<html data-openclaw-control-ui-base-path="/v1/claw" `, 1)

	// A frame recorder, injected ahead of the bundle, so a live drive can read
	// what the UI actually asked for and what came back.
	const probe = `<script>(function(){var W=window.WebSocket;window.__frames=[];function P(u,p){var s=(p===undefined)?new W(u):new W(u,p);var send=s.send.bind(s);s.send=function(d){try{window.__frames.push(["out",String(d).slice(0,600)])}catch(e){}return send(d)};s.addEventListener("message",function(e){try{window.__frames.push(["in",String(e.data).slice(0,600)])}catch(x){}});return s}P.prototype=W.prototype;P.CONNECTING=W.CONNECTING;P.OPEN=W.OPEN;P.CLOSING=W.CLOSING;P.CLOSED=W.CLOSED;window.WebSocket=P;})()</script>`
	shell = strings.Replace(shell, "<head>", "<head>"+probe, 1)

	app.Get("/v1/claw/control-ui-config.json", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{
			"basePath":        "/v1/claw",
			"assistantName":   "Claw on Hanzo",
			"assistantAvatar": "",
			"terminalEnabled": false,
		})
	})
	// Registered as subtree prefixes, never a bare /v1/claw/* — that wildcard
	// also matches /v1/claw itself and would shadow the protocol socket.
	shellAt := func(c *zip.Ctx) error { return send(c, "text/html; charset=utf-8", []byte(shell)) }
	app.Get("/v1/claw/", shellAt)
	for _, dir := range []string{"assets", "fonts", "themes", "app-art", "plugin-art", "provider-icons", "file-icons", "community-art"} {
		app.Get("/v1/claw/"+dir+"/*", fileAt(root, shell))
	}
	for _, f := range []string{"favicon.svg", "favicon.ico", "favicon-32.png", "apple-touch-icon.png", "manifest.webmanifest", "sw.js", "social-card.png", "asset-manifest.json"} {
		app.Get("/v1/claw/"+f, fileAt(root, shell))
	}
	// Deep links: every client-side route is one more segment under the mount.
	app.Get("/v1/claw/:a", shellAt)
	app.Get("/v1/claw/:a/:b", shellAt)
	app.Get("/v1/claw/:a/:b/:c", shellAt)
	_ = func(c *zip.Ctx) error {
		return nil
	}
}

func fileAt(root, shell string) zip.Handler {
	return func(c *zip.Ctx) error {
		rel := strings.TrimPrefix(c.Path(), "/v1/claw/")
		b, err := os.ReadFile(filepath.Join(root, filepath.Clean("/"+rel)))
		if err != nil {
			return send(c, "text/html; charset=utf-8", []byte(shell))
		}
		return send(c, ctype(rel), b)
	}
}

func send(c *zip.Ctx, mime string, body []byte) error {
	c.Fiber().Set("Content-Type", mime)
	c.Fiber().Status(http.StatusOK)
	_, err := io.Copy(c.Fiber().Response().BodyWriter(), strings.NewReader(string(body)))
	return err
}

func ctype(p string) string {
	switch {
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".json"), strings.HasSuffix(p, ".webmanifest"):
		return "application/json"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".png"):
		return "image/png"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(p, ".ico"):
		return "image/x-icon"
	}
	return "application/octet-stream"
}

func die(err error) { fmt.Fprintln(os.Stderr, "clawdev:", err); os.Exit(1) }

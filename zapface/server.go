package zapface

import (
	"net/http"

	"github.com/coder/websocket"
	luxlog "github.com/luxfi/log"
	zaprpc "github.com/zap-proto/go/rpc"

	fiber "github.com/gofiber/fiber/v3"
)

// Options configure the ZAP WebSocket face.
type Options struct {
	// OriginPatterns is the WebSocket Origin allowlist (coder/websocket
	// AcceptOptions.OriginPatterns). Empty == same-origin only; set the SPA
	// hosts (e.g. "console2.hanzo.ai") for cross-origin browser access.
	OriginPatterns []string
	// MaxMessage caps one inbound WS message in bytes. Default 4 MiB.
	MaxMessage int64
	// Logger for connection/dispatch diagnostics.
	Logger luxlog.Logger
}

// authContext is the per-connection auth slot minted on the WS upgrade. It
// carries the browser's credential material (replayed on each /v1 call) — the
// authoritative check stays in the existing /v1 session/JWT filter, so auth is
// defined in exactly ONE place.
type authContext struct {
	cookie     string
	authHeader string
	acceptLang string
}

// mintCap reads the credential material off the WS upgrade request. Returns
// (ctx, true) when there is auth material to replay, or (_, false) to reject
// the upgrade with HTTP 401 (fail-closed). Cookie OR Authorization satisfies
// the gate; the per-call /v1 filter then validates it for real.
func mintCap(r *http.Request) (authContext, bool) {
	ctx := authContext{
		cookie:     r.Header.Get("Cookie"),
		authHeader: bearerFromRequest(r),
		acceptLang: r.Header.Get("Accept-Language"),
	}
	if ctx.cookie == "" && ctx.authHeader == "" {
		return authContext{}, false
	}
	return ctx, true
}

// bearerFromRequest extracts a bearer token. Browsers cannot set the
// Authorization header on a native WebSocket, so @zap-proto/web falls back to
// an ?authorization=Bearer%20<tok> query param — accept both.
func bearerFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		return h
	}
	if q := r.URL.Query().Get("authorization"); q != "" {
		return q
	}
	return ""
}

// Handler returns the http.HandlerFunc mounted at /zap. It upgrades to
// WebSocket, mints the per-connection auth slot, then bridges each binary ZAP
// frame to the /v1 dispatch via rpc.ParseRequest -> dispatch -> rpc.BuildResponse.
func Handler(rawFiber *fiber.App, opts Options) http.HandlerFunc {
	if opts.MaxMessage == 0 {
		opts.MaxMessage = 4 << 20
	}
	disp := newDispatcher(rawFiber)
	log := opts.Logger

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := mintCap(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: opts.OriginPatterns,
		})
		if err != nil {
			return // Accept already wrote the failure response
		}
		defer c.CloseNow()
		c.SetReadLimit(opts.MaxMessage)

		ctx := r.Context()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return // client closed or read error — end the session
			}
			if typ != websocket.MessageBinary {
				// ZAP is binary-only. A text frame is a protocol violation.
				c.Close(websocket.StatusUnsupportedData, "zap: binary frames only")
				return
			}

			call, perr := zaprpc.ParseRequest(data)
			if perr != nil {
				if log != nil {
					log.Warn("zapface: bad request frame", "err", perr)
				}
				// Cannot recover a promiseID from an unparseable frame; drop it.
				continue
			}

			rep := disp.dispatch(call, auth.cookie, auth.authHeader, auth.acceptLang)
			body := encodeZapReply(rep)
			out := zaprpc.BuildResponse(rep.status, call.PromiseID, body)
			if werr := c.Write(ctx, websocket.MessageBinary, out); werr != nil {
				return
			}
		}
	})
}

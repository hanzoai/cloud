package zapface

import (
	luxlog "github.com/luxfi/log"
	zaprpc "github.com/zap-proto/go/rpc"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

// Options configure the ZAP WebSocket face.
type Options struct {
	// OriginPatterns is informational here; the WS upgrade allows all origins
	// (zip is multi-tenant — auth, not Origin, is the gate) and the per-call
	// /v1 filter does the authoritative check. Kept for symmetry/logging.
	OriginPatterns []string
	// MaxMessage caps one inbound WS message in bytes. Default 4 MiB.
	MaxMessage int
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

// Handler returns the zip.Handler mounted at /zap. It mints the per-connection
// auth slot from the upgrade request (fail-closed: 401 with no credential),
// upgrades to a native Fiber WebSocket (wsx), then bridges each binary ZAP
// frame to the /v1 dispatch via rpc.ParseRequest -> dispatch -> rpc.BuildResponse.
//
// WebSocket is served Fiber-natively (fasthttp/websocket via zip/wsx), NOT
// through a net/http adaptor — fasthttp's synthetic ResponseWriter cannot be
// hijacked, so an adaptor-based upgrade 404s.
func Handler(rawFiber *fiber.App, opts Options) zip.Handler {
	if opts.MaxMessage == 0 {
		opts.MaxMessage = 4 << 20
	}
	disp := newDispatcher(rawFiber)
	log := opts.Logger

	return func(c *zip.Ctx) error {
		// mintCap BEFORE the upgrade so an unauthenticated client gets a clean
		// HTTP 401 and no socket is opened. The credential is captured here and
		// closed over into the per-connection read loop below, so the exact
		// cookie/bearer the browser sent on the upgrade is replayed on every
		// /v1 dispatch (parity with the REST client's credentials: 'include').
		auth, ok := mintCap(c)
		if !ok {
			return c.JSON(401, map[string]string{"status": "error", "msg": "unauthorized"})
		}

		return wsx.Upgrade(func(ws *wsx.Conn) error {
			ws.SetReadLimit(int64(opts.MaxMessage))
			for {
				typ, data, err := ws.ReadMessage()
				if err != nil {
					return nil // client closed or read error — end the session
				}
				if typ != wsx.BinaryMessage {
					_ = ws.WriteMessage(wsx.CloseMessage, nil)
					return nil
				}
				call, perr := zaprpc.ParseRequest(data)
				if perr != nil {
					if log != nil {
						log.Warn("zapface: bad request frame", "err", perr)
					}
					continue // cannot recover a promiseID from an unparseable frame
				}
				rep := disp.dispatch(call, auth.cookie, auth.authHeader, auth.acceptLang)
				body := encodeZapReply(rep)
				out := zaprpc.BuildResponse(rep.status, call.PromiseID, body)
				if werr := ws.WriteMessage(wsx.BinaryMessage, out); werr != nil {
					return nil
				}
			}
		})(c)
	}
}

// mintCap reads the credential material off the upgrade request. Returns
// (ctx, true) when there is auth material to replay, or (_, false) to reject
// the upgrade with HTTP 401 (fail-closed). Cookie OR Authorization (header or
// the ?authorization= query browsers fall back to) satisfies the gate; the
// per-call /v1 filter then validates it for real.
func mintCap(c *zip.Ctx) (authContext, bool) {
	ac := authContext{
		cookie:     c.Header("Cookie"),
		authHeader: bearerFrom(c),
		acceptLang: c.Header("Accept-Language"),
	}
	if ac.cookie == "" && ac.authHeader == "" {
		return authContext{}, false
	}
	return ac, true
}

func bearerFrom(c *zip.Ctx) string {
	if h := c.Header("Authorization"); h != "" {
		return h
	}
	if q := c.Query("authorization"); q != "" {
		return q
	}
	return ""
}

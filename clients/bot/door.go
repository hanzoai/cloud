package bot

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

// The two transports. Both turn a request frame into a *Call and run the same
// dispatch, so a method is reached one way and behaves the same either way.
//
// The socket is served Fiber-natively through zip/wsx — the same primitive the
// ZAP face uses (zapface/server.go). A net/http adaptor cannot serve it:
// fasthttp's synthetic ResponseWriter is not hijackable, so an adaptor-based
// upgrade answers 404.

// door answers GET /v1/bot. Asking to upgrade is a fact about the request
// rather than a flag beside it, so a client that asked for the protocol gets
// the socket. Every other GET is passed along: a browser asking for a page here
// is asking whatever serves pages, and the roster it might have meant has its
// own address at /v1/bot/runs. Refusing it instead would take an address this
// surface has no answer for.
func door(s *cloud.Service[state], c *zip.Ctx) error {
	if websocket.FastHTTPIsWebSocketUpgrade(c.Fiber().RequestCtx()) {
		return stream(s, c)
	}
	return c.Next()
}

// stream upgrades to the protocol socket. The caller is resolved BEFORE the
// upgrade, for two reasons: an unauthenticated client then gets a clean HTTP
// refusal and no socket is opened, and the request context is recycled by
// fasthttp the moment the handler returns, so nothing inside the read loop may
// touch it.
func stream(s *cloud.Service[state], c *zip.Ctx) error {
	me, err := resolve(s, c)
	if err != nil {
		return err
	}
	return wsx.Upgrade(func(ws *wsx.Conn) error {
		k := newConn(me)
		s.State.hub.add(k)
		defer func() {
			s.State.hub.drop(k) // ends the connection
			<-k.gone            // and lets its writer finish while the socket is still ours
		}()

		go k.pump(ws)
		go k.beat(tickEvery)

		// The first frame is a challenge, sent before anyone asks. A browser
		// does not need it — it waits 750ms and connects anyway — but the iOS
		// and Android clients block on one unconditionally and close the socket
		// when none arrives, Android after two seconds. The nonce is not
		// checked when it comes back: identity here is IAM's answer, and these
		// clients do not require the server to have verified what they signed.
		// It costs one frame and it is the whole of what those two need.
		k.send(Event{Type: "event", Event: "connect.challenge", Payload: map[string]any{
			"nonce": mint("nonce"),
			"ts":    time.Now().UnixMilli(),
		}})

		ws.SetReadLimit(maxFrame)
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				return nil // the client closed, or the socket failed: either ends the session
			}
			if kind != wsx.TextMessage {
				// The envelope is JSON text. A binary frame is a different
				// protocol on the same port, and there is nothing to answer.
				k.stop()
				return nil
			}
			var req Request
			if err := json.Unmarshal(data, &req); err != nil || req.Type != kindRequest || req.ID == "" {
				continue // no id to correlate an answer to; the client will time the request out
			}
			// One goroutine per request: a method that waits on a model or a
			// sandbox must not stop the next frame from being read. The
			// context is the connection's, not the upgrade request's — that
			// one is recycled the moment this handler returns.
			one := &Call{
				Method: req.Method,
				svc:    s,
				conn:   k,
				ctx:    k.ctx,
				params: req.Params,
				me:     k.me,
			}
			one.me.grant = k.held()
			id := req.ID
			go func() { k.send(answer(one, id)) }()
		}
	})(c)
}

// call answers POST /v1/bot: one request frame over plain HTTP, for a caller
// with one thing to ask and no reason to hold a connection open. The envelope
// carries the outcome of the method, so a method that refuses still answers 200 with
// ok:false — the status says only whether the method was reached. A caller
// with no validated identity is refused before any frame is read, which is the
// one thing HTTP answers for.
func call(s *cloud.Service[state], c *zip.Ctx) error {
	me, err := resolve(s, c)
	if err != nil {
		return err
	}
	body := c.Body()
	if len(body) > maxFrame {
		return zip.ErrBadRequest("frame exceeds the maximum payload")
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return zip.ErrBadRequest("body is not a request frame")
	}
	if req.Type != kindRequest || req.ID == "" || req.Method == "" {
		return zip.ErrBadRequest(`a request frame is {"type":"req","id":…,"method":…}`)
	}
	return c.JSON(http.StatusOK, answer(&Call{
		Method: req.Method,
		svc:    s,
		ctx:    c.Context(),
		params: req.Params,
		me:     me,
	}, req.ID))
}

package bot

import (
	"encoding/json"
	"net/http"

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

// door answers GET /v1/bot, which is one path with two answers. Asking to
// upgrade is a fact about the request rather than a flag beside it, so a client
// that asked for the protocol gets the socket and every other GET gets the
// roster. A browser asking for a page at this path is neither, and gets the
// roster's answer rather than a refusal it cannot read: pages are served by
// whatever serves pages, not from here.
func door(s *cloud.Service[state], c *zip.Ctx) error {
	if websocket.FastHTTPIsWebSocketUpgrade(c.Fiber().RequestCtx()) {
		return stream(s, c)
	}
	return list(s, c)
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

// call runs one request frame over plain HTTP. The envelope carries the
// outcome of the method, so a method that refuses still answers 200 with
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

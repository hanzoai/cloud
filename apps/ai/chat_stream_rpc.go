// Copyright © 2026 Hanzo AI. MIT License.

package ai

// chat_stream_rpc.go — a completion a sibling buys streamed: ai_chat_stream.
//
// ai_chat answers once the model has finished, so a caller that shows a reply as it
// is written (Hanzo Dev's chat) saw it all at the end. This op is ai_chat with the
// answer delivered as the model produces it: the op authorises, binds a unix socket
// beside the plane's own (as sandbox_stream does) and answers its path, and the
// completion is bought on the first connection, framed onto the socket by
// infer.StreamWriter with the price last.
//
// THE CUSTOMER IS STILL CHARGED ONCE, BY THE CALLER, AT THE MODEL API'S PRICE.
// Everything buy does is done here in the same order (pace, entitlement, screen,
// zen's claim, the model API on one Bill), and the trailer carries that Bill's
// price where Chat.Billed would. The caller's meter tells it to its own Bill and
// settles once (sdk/metered_ai.go ChatStream). A refusal crosses as the status and
// body it would have had, with no price.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/infer"
	aiobject "github.com/hanzoai/ai/object"
)

// streamWait is how long a stream's socket waits for its one connection.
const streamWait = 30 * time.Second

// planeChatStream binds the socket a streamed completion is answered on and buys
// the completion when the caller connects.
func planeChatStream(ctx context.Context, in *client.ChatIn) (*client.Streamed, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, zip.ErrInternal("ai: stream: " + err.Error())
	}
	path := filepath.Join(filepath.Dir(zip.SocketPath("ai")), "c-"+hex.EncodeToString(id[:])+".sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, zip.ErrInternal("ai: stream: " + err.Error())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, zip.ErrInternal("ai: stream: " + err.Error())
	}
	// The completion outlives the op that asked for it, as the caller who asked.
	go streamOn(context.WithoutCancel(ctx), ln, in)
	return &client.Streamed{Socket: path, ExpiresIn: int(streamWait / time.Second)}, nil
}

// streamOn accepts the one connection and answers the completion onto it.
func streamOn(ctx context.Context, ln net.Listener, in *client.ChatIn) {
	if ul, ok := ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(streamWait))
	}
	conn, err := ln.Accept()
	_ = ln.Close() // one connection; closing unlinks the socket
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = buyStreamed(ctx, conn, in)
}

// buyStreamed is buy, with the answer written to sw as it is produced and the
// price (or plan-covered marker) written after it.
func buyStreamed(ctx context.Context, conn net.Conn, in *client.ChatIn) error {
	const path = "/v1/chat/completions"
	sw := infer.NewStreamWriter(conn)
	whole := func(out *client.Chat, nanos int64, told, covered, cash bool) error {
		sw.Header().Set("Content-Type", "application/json")
		sw.WriteHeader(out.Status)
		_, _ = sw.Write(out.Body)
		if covered {
			return sw.EndCovered()
		}
		return sw.EndBilled(nanos, told, cash)
	}
	if out := pace(ctx, limiter, path); out != nil {
		return whole(out, 0, false, false, false)
	}
	g, refused := admitPlane(ctx, limiter, path, in)
	if refused != nil {
		return whole(refused, 0, false, false, false)
	}
	in, refused = screen(ctx, path, in)
	if refused != nil {
		releasePlane(limiter, g)
		return whole(refused, 0, false, false, false)
	}
	bill := &aiobject.Bill{}
	ctx = context.WithValue(ctx, billKey{}, bill)
	if fn := zen.Load(); fn != nil && in.ContentType == "" {
		if out, ok := (*fn)(ctx, path, in.Request); ok {
			if out.Status >= http.StatusBadRequest {
				releasePlane(limiter, g)
				return whole(out, 0, false, false, false)
			}
			n, told := bill.Nanos()
			if g.Covered() {
				if told && n > 0 {
					settlePlane(limiter, g, n)
				}
				return whole(out, 0, false, true, false)
			}
			return whole(out, n, told, false, g != nil && g.Cash)
		}
	}
	// A caller reads to the trailer even after its own client hangs up, so a write
	// fails only once the caller is gone; the request the model API is answering is
	// cancelled with it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	rw := &cancelOnFail{StreamWriter: sw, cancel: cancel}
	if err := enter(ctx, rw, http.MethodPost, path, in.Request, in.ContentType, bill); err != nil {
		releasePlane(limiter, g)
		body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": err.Error()}})
		return whole(&client.Chat{Status: http.StatusInternalServerError, Body: body}, 0, false, false, false)
	}
	if rw.statusCode() >= http.StatusBadRequest {
		releasePlane(limiter, g)
		return sw.End(0, false)
	}
	n, told := bill.Nanos()
	if g.Covered() {
		if told && n > 0 {
			settlePlane(limiter, g, n)
		}
		return sw.EndCovered()
	}
	return sw.EndBilled(n, told, g != nil && g.Cash)
}

// cancelOnFail is a StreamWriter that cancels the request it answers once a write
// fails: nobody is reading. It also records the first HTTP status code written so
// unserved responses (>= 400) release their counted plan slots.
type cancelOnFail struct {
	*infer.StreamWriter
	cancel context.CancelFunc
	status int
}

func (c *cancelOnFail) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.StreamWriter.WriteHeader(code)
}

func (c *cancelOnFail) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	n, err := c.StreamWriter.Write(p)
	if err != nil {
		c.cancel()
	}
	return n, err
}

func (c *cancelOnFail) statusCode() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

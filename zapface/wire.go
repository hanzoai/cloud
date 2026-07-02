// Package zapface serves the browser-facing ZAP RPC plane over WebSocket.
//
// Two transports, ONE dispatch path. Native in-cluster peers reach cloud's
// procedures over the ZAP transport (TCP/QUIC). Browsers — which cannot open a
// raw socket — reach the SAME /v1 handlers over a WebSocket carrying binary ZAP
// frames. There is no second copy of any business logic here: every call is
// replayed as an in-process *http.Request through the existing Fiber app
// (adaptor.FiberApp), so the same controllers, middleware, auth filters, and
// /v1 {status,msg,data} envelope back both transports.
//
// Wire contract (matches @zap-proto/web + github.com/zap-proto/go/rpc exactly):
//
//   - One WebSocket BINARY message == one ZAP router envelope. No extra length
//     prefix (WS frames already carry message boundaries). Text frames are a
//     protocol violation and close the socket (1003).
//   - The outer envelope is github.com/zap-proto/go/rpc: a request decodes via
//     rpc.ParseRequest into rpc.Call{Method,PromiseID,Target,Cap,Payload}; the
//     reply is rpc.BuildResponse(status, promiseID, body). These are
//     byte-for-byte identical to the TS runtime's buildRequest/parseResponse,
//     so a frame built by console2's @zap-proto/web is decoded here and our
//     reply is decoded there.
//   - The INNER request payload (rpc.Call.Payload) is console2's ZapRequest
//     struct: { method @0 :Text, payload @8 :Text } (fixed size 16). `method`
//     is the /v1 endpoint name (e.g. "get-providers"); `payload` is a
//     SuperJSON string of the call arguments (” for none).
//   - The INNER reply body is console2's ZapReply struct:
//     { ok @0 :Bool, status @4 :UInt32, result @8 :Text, errorJson @16 :Text }
//     (fixed size 24). `result` is a SuperJSON string of the /v1 `data`;
//     `errorJson` is a JSON error envelope when ok == false.
//
// The inner struct offsets/sizes live ONLY here — the single source of truth
// mirroring console2/src/lib/zap/transport.ts.
package zapface

import (
	"encoding/json"

	zap "github.com/zap-proto/go"
)

// ZapRequest inner-struct layout — mirrors console2 transport.ts (zapRequestOff
// / zapRequestSize). Method and Payload are the only fields the browser sends.
const (
	reqMethodOff  = 0
	reqPayloadOff = 8
	reqFixedSize  = 16
)

// ZapReply inner-struct layout — mirrors console2 transport.ts (zapReplyOff).
const (
	replyOkOff        = 0
	replyStatusOff    = 4
	replyResultOff    = 8
	replyErrorJSONOff = 16
	replyFixedSize    = 24
)

// zapRequest is the decoded inner request: which /v1 method, and its SuperJSON
// argument string.
type zapRequest struct {
	method  string
	payload string
}

// parseZapRequest decodes the inner ZapRequest struct from rpc.Call.Payload.
// The bytes are a v1 ZAP message (console2's Builder emits v1).
func parseZapRequest(b []byte) (zapRequest, error) {
	m, err := zap.Parse(b)
	if err != nil {
		return zapRequest{}, err
	}
	r := m.Root()
	return zapRequest{
		method:  r.Text(reqMethodOff),
		payload: r.Text(reqPayloadOff),
	}, nil
}

// zapReply is the inner reply the browser decodes (ZapReply.wrap).
type zapReply struct {
	ok        bool
	status    uint32
	result    string // SuperJSON string of /v1 `data`
	errorJSON string // JSON error envelope ('' when ok)
}

// encodeZapReply builds the inner ZapReply struct as a v1 ZAP message (the
// rpc envelope then wraps it as the response Body via rpc.BuildResponse).
func encodeZapReply(rep zapReply) []byte {
	b := zap.NewBuilder(len(rep.result) + len(rep.errorJSON) + replyFixedSize + 64)
	ob := b.StartObject(replyFixedSize)
	ob.SetBool(replyOkOff, rep.ok)
	ob.SetUint32(replyStatusOff, rep.status)
	ob.SetText(replyResultOff, rep.result)
	ob.SetText(replyErrorJSONOff, rep.errorJSON)
	ob.FinishAsRoot()
	return b.Finish()
}

// superJSONWrap wraps a raw JSON value as the SuperJSON wire shape that
// console2's `SuperJSON.parse(reply.result)` expects: a plain value V is
// transported as {"json": V}. (SuperJSON adds a "meta" key only for
// non-JSON-native types — Dates/Maps/BigInt — which /v1 responses do not
// emit, so the bare {"json":...} envelope round-trips losslessly.)
func superJSONWrap(rawJSON []byte) string {
	if len(rawJSON) == 0 {
		rawJSON = []byte("null")
	}
	out, _ := json.Marshal(superJSONEnvelope{JSON: json.RawMessage(rawJSON)})
	return string(out)
}

// superJSONUnwrap reverses superJSONWrap for inbound payloads: given a
// SuperJSON string, return the inner JSON value bytes. An empty string means
// "no arguments". A string that is not a SuperJSON envelope is returned as-is
// (defensive — callers may send a bare JSON object).
func superJSONUnwrap(s string) []byte {
	if s == "" {
		return nil
	}
	var env superJSONEnvelope
	if err := json.Unmarshal([]byte(s), &env); err == nil && len(env.JSON) > 0 {
		return env.JSON
	}
	return []byte(s)
}

type superJSONEnvelope struct {
	JSON json.RawMessage `json:"json"`
}

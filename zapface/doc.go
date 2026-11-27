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
//     so a frame built by console's @zap-proto/web is decoded here and our
//     reply is decoded there.
//   - The INNER request payload (rpc.Call.Payload) is console's ZapRequest
//     struct: { method @0 :Text, payload @8 :Text } (fixed size 16). `method`
//     is a /v1 request line, "<VERB> <path>" (e.g. "GET ai/providers") — see
//     splitMethod, which REFUSES a bare name rather than guessing its verb;
//     `payload` is a SuperJSON string of the call arguments (” for none).
//   - The INNER reply body is console's ZapReply struct:
//     { ok @0 :Bool, status @4 :UInt32, result @8 :Text, errorJson @16 :Text }
//     (fixed size 24). `result` is a SuperJSON string of the /v1 `data`;
//     `errorJson` is a JSON error envelope when ok == false.
//
// The inner struct offsets/sizes live ONLY here — the single source of truth
// mirroring console/src/lib/zap/transport.ts.
package zapface

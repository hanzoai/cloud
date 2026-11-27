package zapface

import (
	"encoding/json"

	zap "github.com/zap-proto/go"
)

// ZapRequest inner-struct layout — mirrors console transport.ts (zapRequestOff
// / zapRequestSize). Method and Payload are the only fields the browser sends.
const (
	reqMethodOff  = 0
	reqPayloadOff = 8
	reqFixedSize  = 16
)

// ZapReply inner-struct layout — mirrors console transport.ts (zapReplyOff).
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
// The bytes are a v1 ZAP message (console's Builder emits v1).
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
// console's `SuperJSON.parse(reply.result)` expects: a plain value V is
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

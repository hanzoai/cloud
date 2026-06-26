package zapface

import (
	"encoding/json"
	"testing"

	zap "github.com/zap-proto/go"
	zaprpc "github.com/zap-proto/go/rpc"
)

// buildClientRequestBytes reproduces, byte-for-byte, what console2's
// @zap-proto/web client puts on the WebSocket for one call:
//
//	conn.bootstrap.call(METHOD_RPC, { payload: newZapRequest({method, payload}) })
//	  -> buildRequest(Call{ method: METHOD_RPC, promiseID, payload })
//
// where newZapRequest builds the inner {method@0, payload@8} (size 16) struct.
// We build the inner struct with the same offsets/sizes the TS runtime uses
// (transport.ts), then wrap it in the rpc request envelope.
func buildClientRequestBytes(method, payload string, promiseID uint32) []byte {
	inner := zap.NewBuilder(len(method) + len(payload) + reqFixedSize + 64)
	ob := inner.StartObject(reqFixedSize)
	ob.SetText(reqMethodOff, method)
	ob.SetText(reqPayloadOff, payload)
	ob.FinishAsRoot()
	innerBytes := inner.Finish()

	return zaprpc.BuildRequest(zaprpc.Call{
		Method:    1, // METHOD_RPC
		PromiseID: promiseID,
		Target:    zaprpc.NoTarget,
		Payload:   innerBytes,
	})
}

// TestParseClientRequest proves the server decodes a real client frame: outer
// rpc envelope -> inner ZapRequest -> (method, SuperJSON payload).
func TestParseClientRequest(t *testing.T) {
	const method = "get-providers"
	// console2 sends SuperJSON.stringify(input); a plain object X -> {"json":X}.
	input := map[string]any{"owner": "admin", "store": "default", "limit": "20"}
	innerJSON, _ := json.Marshal(input)
	payload := superJSONWrap(innerJSON) // {"json":{...}}

	frame := buildClientRequestBytes(method, payload, 7)

	// Server side: parse the rpc envelope.
	call, err := zaprpc.ParseRequest(frame)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if call.Method != 1 {
		t.Fatalf("Method = %d, want 1 (METHOD_RPC)", call.Method)
	}
	if call.PromiseID != 7 {
		t.Fatalf("PromiseID = %d, want 7", call.PromiseID)
	}

	// Inner decode.
	zr, err := parseZapRequest(call.Payload)
	if err != nil {
		t.Fatalf("parseZapRequest: %v", err)
	}
	if zr.method != method {
		t.Fatalf("method = %q, want %q", zr.method, method)
	}
	if zr.payload != payload {
		t.Fatalf("payload = %q, want %q", zr.payload, payload)
	}

	// And the SuperJSON unwrap recovers the original input object.
	got := superJSONUnwrap(zr.payload)
	var roundtrip map[string]any
	if err := json.Unmarshal(got, &roundtrip); err != nil {
		t.Fatalf("unwrap: %v (%s)", err, got)
	}
	for k, v := range input {
		if roundtrip[k] != v {
			t.Fatalf("input[%q] = %v, want %v", k, roundtrip[k], v)
		}
	}
}

// TestEncodeReplyRoundTrip proves a reply the server builds is decoded by the
// SAME envelope+struct decode the client uses (rpc.ParseResponse +
// ZapReply.wrap), with the promiseID echoed and SuperJSON result recoverable.
func TestEncodeReplyRoundTrip(t *testing.T) {
	// Simulate a successful casibase data payload (an array of providers).
	data := json.RawMessage(`[{"owner":"admin","name":"openai","category":"Model"}]`)
	rep := zapReply{ok: true, status: 200, result: superJSONWrap(data)}

	body := encodeZapReply(rep)
	frame := zaprpc.BuildResponse(rep.status, 7, body)

	// Client side: parse the response envelope.
	resp, err := zaprpc.ParseResponse(frame)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.PromiseID != 7 {
		t.Fatalf("PromiseID = %d, want 7", resp.PromiseID)
	}
	if resp.Status != 200 {
		t.Fatalf("Status = %d, want 200", resp.Status)
	}

	// Inner decode of ZapReply (mirror transport.ts ZapReply.wrap offsets).
	m, err := zap.Parse(resp.Body)
	if err != nil {
		t.Fatalf("parse reply body: %v", err)
	}
	r := m.Root()
	if !r.Bool(replyOkOff) {
		t.Fatalf("ok = false, want true")
	}
	if r.Uint32(replyStatusOff) != 200 {
		t.Fatalf("inner status = %d, want 200", r.Uint32(replyStatusOff))
	}
	gotResult := r.Text(replyResultOff)
	if gotResult != rep.result {
		t.Fatalf("result = %q, want %q", gotResult, rep.result)
	}
	if r.Text(replyErrorJSONOff) != "" {
		t.Fatalf("errorJson = %q, want empty", r.Text(replyErrorJSONOff))
	}

	// SuperJSON unwrap recovers the providers array.
	inner := superJSONUnwrap(gotResult)
	var providers []map[string]any
	if err := json.Unmarshal(inner, &providers); err != nil {
		t.Fatalf("unwrap result: %v (%s)", err, inner)
	}
	if len(providers) != 1 || providers[0]["name"] != "openai" {
		t.Fatalf("providers = %v, want one openai provider", providers)
	}
}

// TestErrorReply proves the error envelope shape the client reads on !ok.
func TestErrorReply(t *testing.T) {
	rep := errReply(401, "UNAUTHORIZED", "Not authorized")
	body := encodeZapReply(rep)
	frame := zaprpc.BuildResponse(rep.status, 3, body)

	resp, err := zaprpc.ParseResponse(frame)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Status != 401 {
		t.Fatalf("status = %d, want 401", resp.Status)
	}
	m, _ := zap.Parse(resp.Body)
	r := m.Root()
	if r.Bool(replyOkOff) {
		t.Fatalf("ok = true, want false")
	}
	ej := r.Text(replyErrorJSONOff)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(ej), &parsed); err != nil {
		t.Fatalf("errorJson not JSON: %v (%s)", err, ej)
	}
	if parsed["msg"] != "Not authorized" {
		t.Fatalf("errorJson.msg = %q, want %q", parsed["msg"], "Not authorized")
	}
}

// TestBuildHTTPRequest proves the (method,input) -> /v1 HTTP mapping matches the
// casibase REST convention the console2 ProviderApi uses.
func TestBuildHTTPRequest(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		input      any
		wantMethod string
		wantPath   string
		wantQuery  map[string]string
		wantBodyHa string // a substring expected in the body ('' = no body)
	}{
		{
			name:       "get-global-providers no args",
			method:     "get-global-providers",
			input:      nil,
			wantMethod: "GET",
			wantPath:   "/v1/get-global-providers",
		},
		{
			name:       "get-provider with id",
			method:     "get-provider",
			input:      map[string]any{"id": "admin/openai"},
			wantMethod: "GET",
			wantPath:   "/v1/get-provider",
			wantQuery:  map[string]string{"id": "admin/openai"},
		},
		{
			name:       "get-providers list query",
			method:     "get-providers",
			input:      map[string]any{"owner": "admin", "store": "default"},
			wantMethod: "GET",
			wantPath:   "/v1/get-providers",
			wantQuery:  map[string]string{"owner": "admin", "store": "default"},
		},
		{
			name:       "update-provider id+resource",
			method:     "update-provider",
			input:      map[string]any{"id": "admin/openai", "provider": map[string]any{"owner": "admin", "name": "openai", "type": "OpenAI"}},
			wantMethod: "POST",
			wantPath:   "/v1/update-provider",
			wantQuery:  map[string]string{"id": "admin/openai"},
			wantBodyHa: `"type":"OpenAI"`,
		},
		{
			name:       "add-provider bare resource",
			method:     "add-provider",
			input:      map[string]any{"owner": "admin", "name": "openai", "type": "OpenAI"},
			wantMethod: "POST",
			wantPath:   "/v1/add-provider",
			wantBodyHa: `"name":"openai"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload string
			if tc.input != nil {
				j, _ := json.Marshal(tc.input)
				payload = superJSONWrap(j)
			}
			r, err := buildHTTPRequest(zapRequest{method: tc.method, payload: payload})
			if err != nil {
				t.Fatalf("buildHTTPRequest: %v", err)
			}
			if r.Method != tc.wantMethod {
				t.Fatalf("HTTP method = %s, want %s", r.Method, tc.wantMethod)
			}
			if r.URL.Path != tc.wantPath {
				t.Fatalf("path = %s, want %s", r.URL.Path, tc.wantPath)
			}
			for k, want := range tc.wantQuery {
				if got := r.URL.Query().Get(k); got != want {
					t.Fatalf("query[%q] = %q, want %q", k, got, want)
				}
			}
			if tc.wantBodyHa != "" {
				buf := make([]byte, r.ContentLength)
				_, _ = r.Body.Read(buf)
				if !contains(string(buf), tc.wantBodyHa) {
					t.Fatalf("body = %s, want to contain %s", buf, tc.wantBodyHa)
				}
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- Cross-language harness helpers (driven by testdata/xcheck.mjs) ---
// Gated by ZAPFACE_XCHECK so they only run from the cross-check bash harness.

// TestXCheckParseReq reads ZAPFACE_REQ_HEX (a frame built by the REAL TS
// @zap-proto runtime) and prints the decoded method+unwrapped-input as JSON to
// stdout, proving Go parses real client bytes.
func TestXCheckParseReq(t *testing.T) {
	if testingEnv("ZAPFACE_XCHECK") == "" {
		t.Skip("set ZAPFACE_XCHECK to run the cross-language harness")
	}
	frame := mustHex(t, testingEnv("ZAPFACE_REQ_HEX"))
	call, err := zaprpc.ParseRequest(frame)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	zr, err := parseZapRequest(call.Payload)
	if err != nil {
		t.Fatalf("parseZapRequest: %v", err)
	}
	out := map[string]any{
		"promiseID": call.PromiseID,
		"method":    zr.method,
		"input":     json.RawMessage(orNull(superJSONUnwrap(zr.payload))),
	}
	b, _ := json.Marshal(out)
	writeStdout(string(b))
}

// TestXCheckBuildReply builds a server reply frame (rpc.BuildResponse wrapping a
// ZapReply with a providers array) and prints its hex, so the TS runtime can
// decode it.
func TestXCheckBuildReply(t *testing.T) {
	if testingEnv("ZAPFACE_XCHECK") == "" {
		t.Skip("set ZAPFACE_XCHECK to run the cross-language harness")
	}
	data := json.RawMessage(`[{"owner":"admin","name":"openai","category":"Model"}]`)
	rep := zapReply{ok: true, status: 200, result: superJSONWrap(data)}
	body := encodeZapReply(rep)
	frame := zaprpc.BuildResponse(rep.status, 42, body)
	if out := testingEnv("ZAPFACE_REPLY_OUT"); out != "" {
		mustWriteFile(t, out, hexEncode(frame))
		return
	}
	writeStdout(hexEncode(frame))
}

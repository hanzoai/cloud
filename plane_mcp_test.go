package cloud

// The plane MCP endpoint's WIRE, on its own socket with no fleet in front of it.
//
// It is a separate question from which endpoint a caller reaches, which the
// subsystem harness beside it already asks: this one asks what crosses.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	zapmcp "github.com/zap-proto/mcp"
	"github.com/zap-proto/zip"
)

// TestThePlaneEndpointSpeaksFrames sends ZAP bytes and reads ZAP bytes back,
// with the op's own answer inside them.
//
// The control is the JSON-RPC an MCP client sends. It is not a frame on this
// socket, and the endpoint refuses it as one rather than reading it under a
// codec nobody wrote it in — which is what makes the first half worth anything,
// since a body both codecs accepted would say nothing about which one ran.
func TestThePlaneEndpointSpeaksFrames(t *testing.T) {
	_, sock := subsystem(t)

	ask, err := zapmcp.Marshal(&zapmcp.Frame{Kind: zapmcp.Request, ID: "1", Method: "tools/call",
		Params: []byte(`{"name":"` + tenantOp + `","arguments":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(ask, []byte("ZAP\x00")) {
		t.Fatalf("the request carries no ZAP magic, so this test is not measuring the wire: %q", ask)
	}

	code, ctype, body := frames(t, sock, zip.CallContentType, ask)
	if code != http.StatusOK {
		t.Fatalf("a frame got %d: %q", code, body)
	}
	if ctype != zip.CallContentType {
		t.Errorf("Content-Type = %q, want %q", ctype, zip.CallContentType)
	}
	var ans zapmcp.Frame
	if err := zapmcp.Unmarshal(body, &ans); err != nil {
		t.Fatalf("the answer is not a frame: %v (%q)", err, body)
	}
	if ans.Kind != zapmcp.Response || ans.Err != nil {
		t.Fatalf("answer kind %s, err %v", ans.Kind, ans.Err)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(ans.Result, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("the frame carries no tools/call result: %v (%q)", err, ans.Result)
	}
	var got seen
	if err := json.Unmarshal([]byte(result.Content[0].Text), &got); err != nil {
		t.Fatalf("the content is not the op's output: %v (%s)", err, result.Content[0].Text)
	}
	if !got.Validated || got.Org != "hanzo" {
		t.Errorf("the op resolved (%q, validated=%v); the frame did not carry the caller", got.Org, got.Validated)
	}
	t.Logf("frame in %d bytes, frame out %d bytes, op answered org=%q validated=%v",
		len(ask), len(body), got.Org, got.Validated)

	code, _, body = frames(t, sock, "application/json",
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tenantOp+`","arguments":{}}}`))
	if code != http.StatusOK {
		t.Fatalf("a body that is not a frame got %d, want a refusal it can read: %q", code, body)
	}
	var refused zapmcp.Frame
	if err := zapmcp.Unmarshal(body, &refused); err != nil {
		t.Fatalf("the refusal is not a frame either: %v (%q)", err, body)
	}
	if refused.Err == nil || refused.Err.Code != zapmcp.CodeParse {
		t.Fatalf("a JSON-RPC body was answered %+v, want a parse error — this socket reads frames", refused.Err)
	}
	t.Logf("control: JSON-RPC on the plane socket -> %d %s", refused.Err.Code, refused.Err.Message)
}

// frames posts one body to a plane socket's MCP endpoint over the transport the
// fleet's own hop uses, and returns the status, the content type and the bytes.
func frames(t *testing.T, sock, ctype string, body []byte) (int, string, []byte) {
	t.Helper()
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.Header.SetMethod(http.MethodPost)
	req.SetHost("websearch")
	req.URI().SetPath(manifest.MCPPath)
	req.Header.SetContentType(ctype)
	req.Header.Set(zip.HeaderOrg, "hanzo")
	req.Header.Set(zip.HeaderUser, "hanzo/z@hanzo.ai")
	req.SetBody(body)
	if err := zaphttp.Dial("unix", sock).Do(req, resp); err != nil {
		t.Fatalf("POST %s at %s: %v", manifest.MCPPath, sock, err)
	}
	return resp.StatusCode(), string(resp.Header.ContentType()), append([]byte(nil), resp.Body()...)
}

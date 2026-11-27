package cloud

// The plane MCP endpoint's WIRE, on its own socket with no fleet in front of it.
//
// It is a separate question from which endpoint a caller reaches, which the
// subsystem harness beside it already asks: this one asks what crosses.

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"github.com/zap-proto/zip"
)

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

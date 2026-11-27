package script

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/piece/catalog"
)

// A generated piece runs as its authors wrote it: bitly's own validate() asks
// Bitly who the token belongs to, and its create_bitlink action posts with the
// token the connection sealed — in the shapes each reads. The doer stands in for
// Bitly, so the test sees exactly what would have gone out.
func TestGeneratedPieceValidatesAndRuns(t *testing.T) {
	var seen []HTTPRequest
	status := 200
	rt, err := NewRuntime(func(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
		seen = append(seen, req)
		if status != 200 {
			return HTTPResponse{Status: status, Body: map[string]any{"message": "FORBIDDEN"}}, nil
		}
		return HTTPResponse{Status: 200, Body: map[string]any{"id": "bit.ly/abc", "link": "https://bit.ly/abc"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	src, ok := catalog.Program("bitly")
	if !ok {
		t.Fatal("bitly has no program")
	}
	c, err := rt.Compile("bitly", src)
	if err != nil {
		t.Fatal(err)
	}

	if err := rt.Validate(context.Background(), c, "CUSTOM_AUTH", map[string]any{"accessToken": "tok"}); err != nil {
		t.Fatalf("a token Bitly accepts was refused: %v", err)
	}
	if got := seen[len(seen)-1]; got.Headers["Authorization"] != "Bearer tok" || !strings.HasPrefix(got.URL, "https://api-ssl.bitly.com/v4/user") {
		t.Fatalf("validate sent %+v", got)
	}

	status = 403
	if err := rt.Validate(context.Background(), c, "CUSTOM_AUTH", map[string]any{"accessToken": "bad"}); err == nil {
		t.Fatal("a token Bitly refuses passed the piece's own check")
	}

	status = 200
	out, err := rt.Run(context.Background(), c, RunInput{
		Action: "create_bitlink",
		Auth:   map[string]any{"type": "CUSTOM_AUTH", "props": map[string]any{"accessToken": "tok"}},
		Props:  map[string]any{"long_url": "https://hanzo.ai"},
	})
	if err != nil {
		t.Fatalf("create_bitlink: %v", err)
	}
	got := seen[len(seen)-1]
	if got.Method != "POST" || got.Headers["Authorization"] != "Bearer tok" || !strings.Contains(got.URL, "/v4/") {
		t.Fatalf("create_bitlink sent %+v", got)
	}
	if m, _ := out.(map[string]any); m == nil {
		t.Fatalf("create_bitlink answered %v", out)
	}
}

// pieces-common's authentication field becomes the Authorization header, and
// an answer outside 2xx is an HttpError the piece can catch.
func TestHTTPClientMatchesPiecesCommon(t *testing.T) {
	var got HTTPRequest
	rt, _ := NewRuntime(func(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
		got = req
		return HTTPResponse{Status: 401, Body: map[string]any{"error": "no"}}, nil
	})
	prog := []byte(`
const { createPiece, createAction } = require('@activepieces/pieces-framework');
const { httpClient, HttpError } = require('@activepieces/pieces-common');
module.exports.p = createPiece({ displayName: 'T', actions: [createAction({
  name: 'go', displayName: 'Go',
  async run(ctx) {
    try {
      await httpClient.sendRequest({ method: 'POST', url: 'https://example.com/x', body: { a: 1 },
        authentication: { type: 'BASIC', username: 'u', password: 'p' } });
      return 'no error';
    } catch (e) {
      return { isHttpError: e instanceof HttpError, status: e.response.status };
    }
  },
})], triggers: [] });`)
	c, err := rt.Compile("t", prog)
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.Run(context.Background(), c, RunInput{Action: "go"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.(map[string]any)
	if m["isHttpError"] != true || m["status"] != int64(401) {
		t.Fatalf("want a caught HttpError 401, got %v", out)
	}
	if got.Headers["Authorization"] != "Basic dTpw" || got.Headers["Content-Type"] != "application/json" || got.Headers["Accept"] != "application/json" {
		t.Fatalf("headers sent: %v", got.Headers)
	}
}

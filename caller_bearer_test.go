package cloud

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// CallerBearer relays the caller's OWN validated JWT bearer and nothing else: a JWT
// passes through unchanged, an opaque API key is not relayable, and no credential
// yields "". This is the token a downstream org-scoped service (the DNS forward
// head) re-validates to enforce tenant isolation across the hop.
func TestCallerBearer(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/probe", func(c *zip.Ctx) error { return c.Bytes(200, []byte(CallerBearer(c))) })

	probe := func(setup func(*http.Request)) string {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		if setup != nil {
			setup(req)
		}
		res, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return string(b)
	}

	cases := []struct {
		name  string
		setup func(*http.Request)
		want  string
	}{
		{"jwt bearer relayed unchanged", func(r *http.Request) { r.Header.Set("Authorization", "Bearer jwt.header.sig") }, "jwt.header.sig"},
		{"X-Authorization fallback", func(r *http.Request) { r.Header.Set("X-Authorization", "Bearer x.y.z") }, "x.y.z"},
		{"opaque hk- api key is NOT relayable", func(r *http.Request) { r.Header.Set("Authorization", "Bearer hk-secret") }, ""},
		{"opaque sk- api key is NOT relayable", func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-secret") }, ""},
		{"no credential yields empty", nil, ""},
	}
	for _, c := range cases {
		if got := probe(c.setup); got != c.want {
			t.Errorf("%s: CallerBearer = %q, want %q", c.name, got, c.want)
		}
	}
}

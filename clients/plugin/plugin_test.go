package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/goa"
)

// TestBuildWasm mounts the goa Rust echo guest (testdata/echo.wasm) as a wasm
// plugin and exercises it end to end through the produced http.Handler.
func TestBuildWasm(t *testing.T) {
	p := Plugin{
		Name: "echo", Kind: "wasm", Lang: "rust", Source: "echo.wasm",
		Prefix: "/v1/echo", Pool: 2,
		Routes: []goa.Route{{Method: "POST", Path: "/echo", Func: "echo"}},
	}
	h, err := build(context.Background(), p, "testdata")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/echo/echo", "application/json", strings.NewReader(`{"name":"ada"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got map[string]map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response not JSON: %q", body)
	}
	if got["echo"]["name"] != "ada" {
		t.Fatalf("got %s", body)
	}
}

// TestBuildProxy forwards to a standalone backend over the default HTTP
// transport, with and without prefix stripping.
func TestBuildProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hit:"+r.URL.Path)
	}))
	defer backend.Close()

	for _, tc := range []struct {
		name  string
		strip bool
		want  string
	}{
		{"keep-prefix", false, "hit:/v1/svc/foo"},
		{"strip-prefix", true, "hit:/foo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := build(context.Background(),
				Plugin{Name: "svc", Kind: "proxy", Prefix: "/v1/svc", Target: backend.URL, StripPrefix: tc.strip}, "")
			if err != nil {
				t.Fatal(err)
			}
			front := httptest.NewServer(h)
			defer front.Close()
			resp, err := http.Get(front.URL + "/v1/svc/foo")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if string(b) != tc.want {
				t.Fatalf("got %q want %q", b, tc.want)
			}
		})
	}
}

// TestTransportSeam: "http" is built in; a custom transport is selectable once
// registered; an unknown transport errors.
func TestTransportSeam(t *testing.T) {
	if _, err := transportFor("http"); err != nil {
		t.Fatalf("http transport: %v", err)
	}
	if _, err := transportFor(""); err != nil {
		t.Fatalf("default transport: %v", err)
	}
	if _, err := transportFor("zap"); err == nil {
		t.Fatal("unregistered transport should error")
	}
	RegisterTransport("zap", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	}))
	if _, err := transportFor("zap"); err != nil {
		t.Fatalf("zap transport after register: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUnknownKind(t *testing.T) {
	if _, err := build(context.Background(), Plugin{Name: "x", Kind: "bogus"}, ""); err == nil {
		t.Fatal("unknown kind should error")
	}
}

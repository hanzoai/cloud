package bot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// reasonProxy is the one reason the seven relay operations share. Three wire facts
// each independently forbid a typed op:
//
//   - ONE registration, EVERY method. zip's typed registrars are per-method and
//     there is no All[In, Out].
//   - a GREEDY wildcard whose value the proxy RE-MOUNTS on the executor
//     (Params("*") → target). fiber names it `*1` and the document `{wildcard1}`,
//     and a whole sub-path is not a scalar zip's bindURL can set on an In field.
//   - a VERBATIM response. proxy answers c.Bytes(resp.StatusCode, rb) under the
//     executor's own Content-Type, which is frequently not JSON at all. A typed op
//     can only answer c.JSON(out) under the status it DECLARED, so both move.
//
// The five publish no MCP tool and no CLI command. They DO carry prose:
// openapi.Describe declares it beside the wire fact in relay.go, which is the seam
// for exactly an operation the wire refuses to type.
const reasonProxy = "proxy. One All() registration for every method, over a greedy wildcard the proxy " +
	"re-mounts on the runtime, relaying the runtime's own status code and Content-Type verbatim. zip has " +
	"no All[In, Out], no In field can bind a whole sub-path, and a typed op can only answer c.JSON(out) " +
	"under its declared status (zip v1.18.12 typed.go:302-311) — method, path and response all move."

// botOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. EVERY served operation counts, so a route mounted at an address nobody

// TestRunToleratesAMalformedBody is the MEASUREMENT behind the one refusal above,
// so the day zip can declare a body-tolerant op the conversion is a test away rather
// than a re-derivation. POST /v1/bot/runs never reads its body, so bytes that are
// not JSON at all still answer 501 — which op.invoke's unconditional 400 cannot.
func TestRunToleratesAMalformedBody(t *testing.T) {
	app := mountWith(t, newFake())
	for _, body := range []string{"", "{", "not json at all", `{"task":"x"}`} {
		code, _ := post(t, app, "/v1/bot/runs", body, "acme")
		if code != 501 {
			t.Fatalf("POST /v1/bot/runs with body %q: got %d, want 501 — the route is body-tolerant, "+
				"which is half of why it is not a typed op", body, code)
		}
	}
}

// post drives a body-carrying request with the gateway-minted identity headers.
// The package's `call` helper sends no body, and the body is exactly what the two
// tests here are about.
func post(t *testing.T, app *zip.App, path, body, org string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// TestStopIsAddressedByTheURLAlone pins the fact that makes stopBotIn safe: the run
// id is bound from the PATH, and a body naming a different run cannot redirect the
// stop. zip's op.invoke binds body, then query, then path — the URL is the
// addressing authority — and `json:"-"` means the decoder never even sees a body
// runId. Without this, typing the route would have handed callers a way to stop a
// run other than the one the URL names.
func TestStopIsAddressedByTheURLAlone(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "wanted"})
	rt.seed("acme", Run{ID: "other"})
	app := mountWith(t, rt)
	code, body := post(t, app, "/v1/bot/runs/wanted/stop", `{"runId":"other"}`, "acme")
	if code != 200 {
		t.Fatalf("stop: got %d (%s), want 200", code, body)
	}
	stops := rt.stopCalls()
	if len(stops) != 1 || stops[0].id != "wanted" {
		t.Fatalf("the runtime was asked for %+v; the URL named \"wanted\" — a body must never redirect a stop", stops)
	}
	if !rt.has("acme", "other") {
		t.Fatal("the body's run was stopped — the URL is the addressing authority, not the body")
	}
	if !strings.Contains(body, `"runId":"wanted"`) {
		t.Fatalf("receipt names the wrong run: %s", body)
	}
}

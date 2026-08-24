package bot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// reasonProxy is the one reason the five relay operations share. Two wire facts
// each independently forbid a typed op, both re-read against the PINNED zip
// (v1.36.3) rather than inherited as prose:
//
//   - a GREEDY wildcard whose value the proxy RE-MOUNTS on the executor
//     (Params("*") → target). A typed op publishes op.Path verbatim, so the
//     registry would key it at `/v1/bot/runtime/*` while cloud's router reading
//     names the segment `{wildcard1}` — Fold then finds no live route at the
//     registry's key and refuses the WHOLE document. Typing does not remove a
//     wildcard; only real addresses do.
//   - a VERBATIM response. proxy answers c.Bytes(resp.StatusCode, rb) under the
//     executor's own Content-Type, which is frequently not JSON at all. A typed
//     op ends at c.JSON(out) under a status it DECLARED (typed.go:563-567), and
//     statusOf (typed.go:192-212) refuses any code the op did not declare, so
//     status, body and content type all move.
//
// "One All() registration for every method" is NOT a third fact and used to be
// cited as one. zip's per-method typed registrars are five lines
// (typed.go:85-108); All() is a convenience, not a blocker. The two facts above
// carry the refusal alone.
//
// The five publish no MCP tool and no CLI command. They DO carry prose:
// openapi.Describe declares it beside the wire fact in relay.go, which is the
// client for exactly an operation the wire refuses to type.
const reasonProxy = "proxy. A greedy wildcard the proxy re-mounts on the runtime, relaying the runtime's " +
	"own status code and Content-Type verbatim. The registry publishes op.Path verbatim while the router " +
	"names the segment {wildcard1}, so Fold refuses the whole document; and a typed op can only answer " +
	"c.JSON(out) under a status it declared (zip v1.36.3 typed.go:563-567, statusOf typed.go:192-212)."

// TestLaunchRefusesEveryCallAndSaysWhichWay is the wire of the launch stub, and
// the reason it is written out body by body is that TYPING IT MOVED ONE OF THEM.
//
// The route answered 501 to any bytes at all while it was a raw handler, because
// the handler never read the body. A typed op decodes first: op.invoke returns
// ErrBadRequest on a body it cannot parse, BEFORE the handler is entered
// (zip v1.36.3 typed.go:487-490). So malformed bytes are 400 now and everything
// else is unchanged — an absent body, an empty one and any well-formed JSON all
// still answer 501, and no run is minted on any path.
//
// That delta was taken deliberately. It is a REFUSAL turning into a different
// refusal on a route with no success at all, not a success turning into a
// failure, and the tolerance was self-imposed rather than a contract any caller
// was written against. Everything an SDK, a CLI or an agent can now do with this
// address — read its schema, see its declared 501, call it and be told why — is
// what the raw route published nothing about.
func TestLaunchRefusesEveryCallAndSaysWhichWay(t *testing.T) {
	app := mountWith(t, newFake())
	for _, tc := range []struct {
		body string
		want int
	}{
		{"", http.StatusNotImplemented},             // no body at all
		{"{}", http.StatusNotImplemented},           // an empty object
		{`{"task":"x"}`, http.StatusNotImplemented}, // a launch a caller meant
		{"{", http.StatusBadRequest},                // THE DELTA: was 501
		{"not json at all", http.StatusBadRequest},  // THE DELTA: was 501
	} {
		code, body := post(t, app, "/v1/bot/runs", tc.body, "acme")
		if code != tc.want {
			t.Errorf("POST /v1/bot/runs with body %q: got %d (%s), want %d", tc.body, code, body, tc.want)
		}
	}
}

// TestLaunchRefusalKeepsItsSentence pins the BODY of the 501, which the
// conversion had to carry unchanged: the handler returns the same zip error it
// always did, so cloud's problem-details envelope renders the same detail. A
// declared status is a fact about the DOCUMENT; the wire here is still an error
// return, and this is what says so.
func TestLaunchRefusalKeepsItsSentence(t *testing.T) {
	app := mountWith(t, newFake())
	code, body := post(t, app, "/v1/bot/runs", "", "acme")
	if code != http.StatusNotImplemented {
		t.Fatalf("launch: got %d (%s), want 501", code, body)
	}
	for _, want := range []string{
		`"status":501`,
		"launching a bot is not implemented",
		"the bot runtime exposes no launch operation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the 501 body no longer carries %q: %s", want, body)
		}
	}
}

// post drives a body-carrying request with the gateway-minted identity headers.
// The package's `call` helper sends no body, and the body is exactly what the
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

// TestStopIsAddressedByTheURLAlone pins the fact that makes stopBotIn safe: the
// run id is bound from the PATH, and a body naming a different run cannot
// redirect the stop. zip's op.invoke binds body, then query, then path — the URL
// is the addressing authority and binds LAST, so it wins whatever the body says.
//
// The field carries BOTH tags (`json:"runId" url:"runId"`), which is what lets
// the MCP tool name its target at all — an MCP tools/call reaches op.invoke with
// a nil path map, so a `json:"-"` field would be unaddressable there. That makes
// this test the one that has to hold: with the body now visible to the decoder,
// nothing but bind ORDER keeps a body from redirecting a stop.
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

// TestStopQueryCannotRedirectAWrite is the other half of the same authority
// rule, and it is not covered by the body case: zip binds query BETWEEN body and
// path, so a `?runId=` is strictly more authoritative than the body and still
// strictly less than the segment the router matched on.
func TestStopQueryCannotRedirectAWrite(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "wanted"})
	rt.seed("acme", Run{ID: "other"})
	app := mountWith(t, rt)
	code, body := post(t, app, "/v1/bot/runs/wanted/stop?runId=other", `{"runId":"other"}`, "acme")
	if code != 200 {
		t.Fatalf("stop: got %d (%s), want 200", code, body)
	}
	if stops := rt.stopCalls(); len(stops) != 1 || stops[0].id != "wanted" {
		t.Fatalf("the runtime was asked for %+v; the URL named \"wanted\"", stops)
	}
	if !rt.has("acme", "other") {
		t.Fatal("the query's run was stopped — the path segment is the addressing authority")
	}
}

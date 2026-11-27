package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The run plane over the REAL transport, against a double written from the
// runtime's side of the wire: hanzoai/bot's gateway (src/gateway/bots-http.ts).
// It answers only what that server answers, and the way it answers:
//
//   - GET /v1/bots and POST /v1/bots/{runId}/stop, nothing else; any other path is
//     a plain-text 404, which the transport reads as "not served".
//   - The org is read off the caller's own token (callerOrg): a person's token acts
//     in its org, and X-Org-Id may only name an org of that token. The shared
//     service token is admitted and acts in no org, so it lists nothing and stops
//     nothing.
//
// Nothing here is written from cloud's constants. A test that asks cloud's own
// listOp of a stub built from listOp agrees with itself whatever the runtime
// serves, and that is how /v1/bot shipped against a runtime serving /v1/bots.

const serviceToken = "svc-gateway-token"

var stopPath = regexp.MustCompile(`^/v1/bots/([^/]+)/stop$`)

type gatewayDouble struct {
	mu     sync.Mutex
	runs   map[string][]map[string]any // org -> rows
	auth   []string                    // the Authorization of every call it served
	missed []string                    // paths it does not serve
}

func (g *gatewayDouble) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	t.Setenv("BOT_GATEWAY_TOKEN", serviceToken)
}

// orgOf is the runtime's callerOrg: a person's token (tokenOf) acts in its own org,
// and in the org X-Org-Id names only when that is the same one; anything else,
// the service token included, acts in no org.
func orgOf(r *http.Request) string {
	person, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "+tokenOf(""))
	if !ok || person == "" {
		return ""
	}
	if named := r.Header.Get("X-Org-Id"); named != "" && named != person {
		return ""
	}
	return person
}

func (g *gatewayDouble) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := stopPath.FindStringSubmatch(r.URL.Path)
	switch {
	case r.URL.Path == "/v1/bots" && r.Method == http.MethodGet:
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		rows := g.runs[orgOf(r)]
		if rows == nil {
			rows = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bots": rows})
	case m != nil && r.Method == http.MethodPost:
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		org, runID := orgOf(r), m[1]
		w.Header().Set("Content-Type", "application/json")
		for i, row := range g.runs[org] {
			if org != "" && row["runId"] == runID {
				g.runs[org] = append(g.runs[org][:i], g.runs[org][i+1:]...)
				_ = json.NewEncoder(w).Encode(map[string]any{"runId": runID, "status": "stopped"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no such bot for this org"}`))
	default:
		g.missed = append(g.missed, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}
}

func (g *gatewayDouble) seen() (auth, missed []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.auth...), append([]string(nil), g.missed...)
}

// mountOver mounts the whole capability exactly as the plugin does (Use), over the
// double, so the transport and wire.go are the shipping ones.
func mountOver(t *testing.T, g *gatewayDouble) *zip.App {
	t.Helper()
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	g.start(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	compose(app)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("bot.Use: %v", err)
	}
	return app
}

func newDouble() *gatewayDouble {
	return &gatewayDouble{runs: map[string][]map[string]any{
		"acme":   {{"runId": "run_acme", "task": "acme work", "surface": "desktop", "status": "running", "startedAt": "2026-11-15T09:00:00Z"}},
		"globex": {{"runId": "run_globex", "task": "globex work", "surface": "terminal", "status": "running", "startedAt": "2026-11-15T09:00:00Z"}},
	}}
}

// A list reaches the runtime at the address it serves and brings back the org's
// runs. Before this, cloud asked for /v1/bot, the runtime answered a plain 404, and
// every list was a 502.
func TestListReachesTheRuntimesOwnAddress(t *testing.T) {
	g := newDouble()
	app := mountOver(t, g)

	code, body := call(t, app, http.MethodGet, "/v1/bot/runs", "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if ids := listRunIDs(t, body); len(ids) != 1 || ids[0] != "run_acme" {
		t.Fatalf("acme must see exactly its own run, got %v (%s)", ids, body)
	}
	if _, missed := g.seen(); len(missed) != 0 {
		t.Fatalf("cloud asked the runtime for addresses it does not serve: %v", missed)
	}
}

// A stop reaches the runtime's stop and takes the run off the org's list; a run of
// another org is the runtime's structured 404, which cloud reports as absent.
func TestStopReachesTheRuntimesOwnAddress(t *testing.T) {
	g := newDouble()
	app := mountOver(t, g)

	if code, body := call(t, app, http.MethodPost, "/v1/bot/runs/run_acme/stop", "acme"); code != http.StatusOK {
		t.Fatalf("stop want 200, got %d (%s)", code, body)
	}
	if _, body := call(t, app, http.MethodGet, "/v1/bot/runs", "acme"); len(listRunIDs(t, body)) != 0 {
		t.Fatalf("a stopped run must leave the list, got %s", body)
	}
	if code, body := call(t, app, http.MethodPost, "/v1/bot/runs/run_globex/stop", "acme"); code != http.StatusNotFound {
		t.Fatalf("another org's run want 404, got %d (%s)", code, body)
	}
	if _, missed := g.seen(); len(missed) != 0 {
		t.Fatalf("cloud asked the runtime for addresses it does not serve: %v", missed)
	}
}

// The runtime is asked as the person who asked cloud: their own token on every
// call, never the service token, which the runtime gives no org.
func TestTheRuntimeIsAskedAsThePerson(t *testing.T) {
	g := newDouble()
	app := mountOver(t, g)

	call(t, app, http.MethodGet, "/v1/bot/runs", "acme")
	call(t, app, http.MethodPost, "/v1/bot/runs/run_acme/stop", "acme")

	auth, _ := g.seen()
	if len(auth) != 2 {
		t.Fatalf("want a list and a stop at the runtime, saw %d calls", len(auth))
	}
	for _, a := range auth {
		if a != "Bearer "+tokenOf("acme") {
			t.Fatalf("the runtime was asked with %q, want the person's own token", a)
		}
	}
}

// An API key is refused before the runtime is asked, so nothing the runtime would
// answer for no org (an empty list, a 404) can be read as this org's answer.
func TestAnAPIKeyNeverReachesTheRuntime(t *testing.T) {
	g := newDouble()
	app := mountOver(t, g)

	if code, body := ask(t, app, http.MethodGet, "/v1/bot/runs", "acme", "sk-live-acme-key"); code != http.StatusForbidden {
		t.Fatalf("list with an API key want 403, got %d (%s)", code, body)
	}
	if code, body := ask(t, app, http.MethodPost, "/v1/bot/runs/run_acme/stop", "acme", "sk-live-acme-key"); code != http.StatusForbidden {
		t.Fatalf("stop with an API key want 403, got %d (%s)", code, body)
	}
	if auth, missed := g.seen(); len(auth)+len(missed) != 0 {
		t.Fatalf("the runtime was asked for an API-key caller: %v %v", auth, missed)
	}
}

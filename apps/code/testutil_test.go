package code

import (
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeEmbedder is a deterministic, offline embedder: it hashes each code token
// into a fixed-dim bag-of-tokens vector (the hashing trick). Text sharing tokens
// with the query gets a higher cosine, so the semantic tier + fusion are provable
// without a live model. Same model for index + query ⇒ dimensions always match.
type fakeEmbedder struct {
	dims    int
	enabled bool
}

func (f fakeEmbedder) Enabled() bool { return f.enabled }

func (f fakeEmbedder) Embed(_ context.Context, _, _, _ string, texts []string) ([][]float32, error) {
	if !f.enabled {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dims)
		for _, tok := range codeTokens(t) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(tok))
			v[h.Sum32()%uint32(f.dims)]++
		}
		out[i] = v
	}
	return out, nil
}

// fakeSynth returns a fixed answer, proving /ask synthesizes over grounding
// without a live model.
type fakeSynth struct{ enabled bool }

func (f fakeSynth) Enabled() bool { return f.enabled }
func (f fakeSynth) Synthesize(_ context.Context, _, _, _ string, prompt string) (string, error) {
	if !f.enabled {
		return "", context.Canceled
	}
	return "GROUNDED_ANSWER", nil
}

func newTestService(t *testing.T) *service {
	t.Helper()
	dataDir := t.TempDir()
	s := &service{
		dataDir: dataDir,
		embed:   fakeEmbedder{dims: 64, enabled: true},
		synth:   fakeSynth{enabled: true},
		log:     luxlog.New("test"),
		stores:  cloud.NewOrgStore(cloud.Base{DataDir: dataDir}, "code", openStore),
	}
	t.Cleanup(func() { _ = s.stores.CloseAll() })
	return s
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func newTestApp(t *testing.T) (*zip.App, *service) {
	t.Helper()
	s := newTestService(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	// The REAL registration, not a reconstruction of it: routes() is what Mount
	// calls, so every typed op is exercised here exactly as the binary serves it.
	// A hand-listed copy drifts silently the first time a route moves.
	if err := routes(app, s); err != nil {
		t.Fatalf("routes: %v", err)
	}
	return app, s
}

// doAuth runs a request carrying a VALIDATED principal (X-User-Id set, as
// SanitizeIdentity would from a verified token) so the org gate is satisfied.
func doAuth(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	return runReq(t, app, req)
}

// requestBudget is what these tests allow ONE in-process request, and it is large
// on purpose. zip's Test defaults to one second, and POST /v1/code/index does real
// work inside it — parse every file, extract symbols, chunk them and embed the
// chunks — so on a loaded box the deadline expired mid-index and the suite failed
// with "i/o timeout" at a route that was working. Nothing here crosses a socket, so
// this bound is not protecting against a slow peer; the only thing it can catch is a
// HANG, and `go test -timeout` already catches that with a bound that fits the whole
// package. Sized so that only a hang trips it.
const requestBudget = 30 * time.Second

func runReq(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Test(req, zip.TestConfig{Timeout: requestBudget})
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", string(b), err)
	}
}

// ── shared fixtures ──────────────────────────────────────────────────────────

const goFixture = `package p

// Greeter builds greetings.
type Greeter struct {
	name string
}

// Hello returns a greeting for the configured name.
func (g *Greeter) Hello() string {
	return greet(g.name)
}

// greet formats a greeting line.
func greet(name string) string {
	return "hi " + name
}

const MaxNameLen = 32
`

const tsFixture = `export function getUser(id: string): User {
  return fetchUser(id);
}

export class UserService {
  find(id: string) {
    return getUser(id);
  }
}
`

const pyFixture = `class Animal:
    def speak(self):
        return make_sound()


def make_sound():
    return "roar"
`

func indexFixtures(t *testing.T, app *zip.App, org, repo string) indexResult {
	t.Helper()
	body := indexIn{Repo: repo, Files: []fileInput{
		{Path: "greeter.go", Content: goFixture},
		{Path: "user.ts", Content: tsFixture},
		{Path: "animal.py", Content: pyFixture},
	}}
	status, b := doAuth(t, app, http.MethodPost, "/v1/code/index", org, body)
	if status != http.StatusOK {
		t.Fatalf("index status=%d body=%s", status, b)
	}
	var res indexResult
	mustJSON(t, b, &res)
	return res
}

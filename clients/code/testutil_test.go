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

func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
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
func (f fakeSynth) Synthesize(_ context.Context, prompt string) (string, error) {
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
		stores:  cloud.NewOrgStore(dataDir, "code", openStore),
	}
	t.Cleanup(func() { _ = s.stores.CloseAll() })
	return s
}

func newTestApp(t *testing.T) (*zip.App, *service) {
	t.Helper()
	s := newTestService(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/code/search", s.handleSearch)
	app.Post("/v1/code/context", s.handleContext)
	app.Get("/v1/code/ask", s.handleAsk)
	app.Post("/v1/code/ask", s.handleAsk)
	app.Post("/v1/code/index", s.handleIndex)
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

func runReq(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Fiber().Test(req)
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
	body := indexReq{Repo: repo, Files: []fileInput{
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

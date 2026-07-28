package leaderboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// dsCall records one datastore query the handler issued — the (sql, args) pair the
// isolation tests inspect to prove the tenant key is bound, never interpolated.
type dsCall struct {
	sql  string
	args []any
}

type fakeDS struct {
	mu     sync.Mutex
	calls  []dsCall
	answer func(sql string, args []any) []map[string]any
}

func (f *fakeDS) query(_ context.Context, sql string, args ...any) ([]map[string]any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dsCall{sql, args})
	f.mu.Unlock()
	if f.answer != nil {
		return f.answer(sql, args), nil
	}
	return nil, nil
}

func (f *fakeDS) allCalls() []dsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dsCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// installFakeDS swaps the datastore seams for a fake (enabled, DDL no-op) and
// restores them + resets the rollup latch on cleanup, so each test exercises the
// full read+assemble path against canned rows without a live warehouse.
func installFakeDS(t *testing.T, answer func(sql string, args []any) []map[string]any) *fakeDS {
	t.Helper()
	f := &fakeDS{answer: answer}
	oq, oe, oen, oet := queryDatastore, execDatastore, datastoreEnabled, ensureUsageTable
	queryDatastore = f.query
	execDatastore = func(context.Context, string, ...any) error { return nil }
	datastoreEnabled = func() bool { return true }
	ensureUsageTable = func(context.Context) error { return nil }
	rollupReady.Store(false)
	t.Cleanup(func() {
		queryDatastore, execDatastore, datastoreEnabled, ensureUsageTable = oq, oe, oen, oet
		rollupReady.Store(false)
	})
	return f
}

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// principalHeaders builds the server-minted identity headers for a validated caller
// (X-User-Id non-empty ⇒ principal.Org accepts it). X-User-Name feeds the ledger id.
func principalHeaders(org, name string) map[string]string {
	return map[string]string{
		"X-Org-Id":    org,
		"X-User-Id":   "uid-" + org + "-" + name,
		"X-User-Name": name,
	}
}

func withHeader(h map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for kk, vv := range h {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func doGet(t *testing.T, app *zip.App, path string, headers map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func doJSON(t *testing.T, app *zip.App, method, path string, headers map[string]string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, rb
}

// seedUserOptin / seedOrgOptin write directly to the mounted store (set by Mount)
// so a test can arrange opt-in state without driving the PUT endpoints.
func seedUserOptin(t *testing.T, id, org, handle string, listed bool) {
	t.Helper()
	if mountedStore == nil {
		t.Fatal("mountedStore nil; call mountApp first")
	}
	if err := mountedStore.PutUser(context.Background(), userOptin{UserID: id, Org: org, Handle: handle, Listed: listed}, 1); err != nil {
		t.Fatalf("seed user optin: %v", err)
	}
}

func seedOrgOptin(t *testing.T, org, display string, listed bool) {
	t.Helper()
	if mountedStore == nil {
		t.Fatal("mountedStore nil; call mountApp first")
	}
	if err := mountedStore.PutOrg(context.Background(), orgOptin{Org: org, Display: display, Listed: listed}, 1); err != nil {
		t.Fatalf("seed org optin: %v", err)
	}
}

func userRow(id string, req, tok, cost uint64) map[string]any {
	return map[string]any{
		"user_id": id, "requests": req, "total_tokens": tok,
		"prompt_tokens": tok, "completion_tokens": uint64(0), "cost_cents": cost,
	}
}

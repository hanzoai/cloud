package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// newTestGH spins up an httptest server that counts /orgs/{org}/repos
// calls and returns a deterministic single-page repo list. The returned
// closer must be called by the test.
func newTestGH(t *testing.T) (gh *ghClient, calls *atomic.Int64, close func()) {
	t.Helper()

	calls = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Repo{
			{Name: "alpha", FullName: "acme/alpha"},
			{Name: "beta", FullName: "acme/beta"},
		})
	}))

	// Minimum PAT-mode TokenProvider — write a temp token file and
	// build the Config around it so NewTokenProvider succeeds.
	dir := t.TempDir()
	patFile := filepath.Join(dir, "token")
	if err := os.WriteFile(patFile, []byte("ghp_test\n"), 0o600); err != nil {
		t.Fatalf("write pat: %v", err)
	}
	cfg := &Config{PATFile: patFile}
	tp, err := NewTokenProvider(cfg)
	if err != nil {
		t.Fatalf("NewTokenProvider: %v", err)
	}

	gh = newGHClient(tp, srv.URL, time.Hour)
	return gh, calls, srv.Close
}

func TestListRepos_CachesWithinTTL(t *testing.T) {
	gh, calls, close := newTestGH(t)
	defer close()

	ctx := context.Background()
	first, err := gh.ListRepos(ctx, "acme")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first len=%d want 2", len(first))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("after first call: calls=%d want 1", got)
	}

	// Within TTL — second call must NOT hit the network.
	second, err := gh.ListRepos(ctx, "acme")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second len=%d want 2", len(second))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("after second call: calls=%d want 1 (cache miss)", got)
	}
}

func TestListRepos_RefetchesAfterTTL(t *testing.T) {
	gh, calls, close := newTestGH(t)
	defer close()
	gh.repoTTL = 50 * time.Millisecond

	ctx := context.Background()
	if _, err := gh.ListRepos(ctx, "acme"); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := gh.ListRepos(ctx, "acme"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls=%d want 2 (TTL expired between calls)", got)
	}
}

func TestListRepos_DisabledCacheAlwaysFetches(t *testing.T) {
	gh, calls, close := newTestGH(t)
	defer close()
	gh.repoTTL = 0

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := gh.ListRepos(ctx, "acme"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls=%d want 3 (cache disabled)", got)
	}
}

func TestListRepos_FallsBackToStaleOnError(t *testing.T) {
	// First serve OK, then 500. The cache should hold the OK result and
	// return it on the second call instead of bubbling the 500.
	calls := &atomic.Int64{}
	mode := atomic.Int32{} // 0 = OK, 1 = 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if mode.Load() == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Repo{{Name: "alpha", FullName: "acme/alpha"}})
	}))
	defer srv.Close()

	dir := t.TempDir()
	patFile := filepath.Join(dir, "token")
	_ = os.WriteFile(patFile, []byte("ghp_test\n"), 0o600)
	tp, err := NewTokenProvider(&Config{PATFile: patFile})
	if err != nil {
		t.Fatalf("NewTokenProvider: %v", err)
	}
	gh := newGHClient(tp, srv.URL, 10*time.Millisecond)

	ctx := context.Background()
	if _, err := gh.ListRepos(ctx, "acme"); err != nil {
		t.Fatalf("first ok: %v", err)
	}
	time.Sleep(30 * time.Millisecond) // expire TTL
	mode.Store(1)
	got, err := gh.ListRepos(ctx, "acme")
	if err != nil {
		t.Fatalf("after stale: unexpected err=%v (should have fallen back to cached snapshot)", err)
	}
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("after stale: got=%v want cached [alpha]", got)
	}
}

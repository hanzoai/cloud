package risk

// store_test.go — the recording warehouse every test in this package drives the
// plane against, plus the package's own fixtures.
//
// It is installed ONCE, in TestMain, and never swapped afterwards. The plane runs
// background folds, so a test that reassigned the seam mid-run would race a
// goroutine reading it; the fake is therefore one value with its own lock, and a
// test RESETS it rather than replacing it.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// stmt is one statement the plane sent to the warehouse, with the values it
// bound. The tests assert on these, which is the only way to prove a predicate is
// BOUND rather than interpolated: an interpolated value would be in the text and
// absent from the args.
type stmt struct {
	SQL  string
	Args []any
}

// warehouse is the recording store.
type warehouse struct {
	mu    sync.Mutex
	up    bool
	sent  []stmt
	table map[string][]map[string]any // rows keyed by the bound org
}

var probe = &warehouse{up: true, table: map[string][]map[string]any{}}

func (w *warehouse) reset(up bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.up, w.sent, w.table = up, nil, map[string][]map[string]any{}
}

// hold files rows under an org key. A read binds an org; only rows filed under
// exactly that key come back, which is what makes "a foreign subject returns zero
// rows" a measurement and not a stub.
func (w *warehouse) hold(org string, rows ...map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.table[org] = append(w.table[org], rows...)
}

func (w *warehouse) ready() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.up
}

func (w *warehouse) query(sql string, args []any) ([]map[string]any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, stmt{SQL: sql, Args: args})
	if !w.up {
		return nil, errStore
	}
	if len(args) == 0 {
		return nil, nil
	}
	org, _ := args[0].(string)
	return w.table[org], nil
}

func (w *warehouse) exec(sql string, args []any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, stmt{SQL: sql, Args: args})
	if !w.up {
		return errStore
	}
	return nil
}

// reads returns every recorded statement that touched the feature table.
func (w *warehouse) reads() []stmt {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]stmt, 0, len(w.sent))
	for _, s := range w.sent {
		if strings.Contains(s.SQL, featureTable) && strings.HasPrefix(strings.TrimSpace(s.SQL), "SELECT") {
			out = append(out, s)
		}
	}
	return out
}

// all returns every recorded statement.
func (w *warehouse) all() []stmt {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]stmt(nil), w.sent...)
}

func TestMain(m *testing.M) {
	// The data plane has no plaintext-at-rest mode, and a test run has no boot to
	// decide a posture, so the suite states a dev key when the environment has not
	// already supplied a real one.
	if os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	}
	storeReady = probe.ready
	storeQuery = func(_ context.Context, sql string, args ...any) ([]map[string]any, error) {
		return probe.query(sql, args)
	}
	storeExec = func(_ context.Context, sql string, args ...any) error {
		return probe.exec(sql, args)
	}
	os.Exit(m.Run())
}

// ── fixtures ─────────────────────────────────────────────────────────────────

// two tenants that differ ONLY in the organisation half, and two that differ only
// in the BRAND half. Both pairs matter: the first is the ordinary cross-tenant
// case, the second is the one a bare org key would collapse.
const (
	orgA   = "acme"
	orgB   = "globex"
	brandA = "hanzo"
	brandB = "zoo"
)

func key(t *testing.T, brandID, org string) tenant {
	t.Helper()
	k, err := qualify(brandID, org)
	if err != nil {
		t.Fatalf("qualify(%q, %q): %v", brandID, org, err)
	}
	return k
}

// baseAt is the shared dependency set over one data directory, so a test can
// build two planes over the SAME directory and prove a restart carries state.
func baseAt(t *testing.T, dir string) cloud.Base {
	t.Helper()
	return cloud.NewBase(cloud.Deps{Logger: luxlog.New("risktest"), Brand: brandA, DataDir: dir}, "risk")
}

// newTestPlane builds a plane over a temporary data directory. The caller closes
// it, which also waits for every background fold.
func newTestPlane(t *testing.T) *plane {
	t.Helper()
	p, err := newPlane(baseAt(t, t.TempDir()))
	if err != nil {
		t.Fatalf("newPlane: %v", err)
	}
	t.Cleanup(func() { _ = p.close() })
	return p
}

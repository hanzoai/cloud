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
	"time"

	"github.com/hanzoai/cloud"
	// The data plane has no plaintext-at-rest mode and a test binary has no boot to
	// resolve a key through, so it keys itself. Stated once for the fleet rather
	// than as a posture each package decides for itself.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// stmt is one statement the plane sent to the warehouse, with the values it
// bound. The tests assert on these, which is the only way to prove a predicate is
// BOUND rather than interpolated: an interpolated value would be in the text and
// absent from the args.
type stmt struct {
	SQL  string
	Args []any
}

// emitted is one row of a SOURCE plane, as the ingest door would have written
// it: product events in event.event, captured failures in event.error, metered
// inference in hanzo.cloud_usage. It is filed under the BARE org, because that is
// the tenant column those planes actually carry.
//
// It exists so the moat can be tested END TO END rather than in halves: an
// organisation emits, the rollup folds, the surface is read back. A fake that
// only records statements can prove a predicate is bound and cannot prove the
// loop closes.
type emitted struct {
	// Plane is which rollup reads it, by [rollupStmt.Name].
	Plane   string
	Subject string
	At      time.Time
	Spend   int64
	// Product is the emitting SURFACE on the row — event.fact's own `product` column.
	// Empty is one of the organisation's own product events; [surface] is one of THIS
	// APP's decisions, which the person rollup must not fold back into the model it
	// decided with.
	Product string
}

// warehouse is the recording store.
type warehouse struct {
	mu    sync.Mutex
	up    bool
	sent  []stmt
	table map[string][]map[string]any // risk_feature rows keyed by the bound QUALIFIED key
	src   map[string][]emitted        // source-plane rows keyed by the BARE org
}

var probe = &warehouse{up: true, table: map[string][]map[string]any{}, src: map[string][]emitted{}}

func (w *warehouse) reset(up bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.up, w.sent = up, nil
	w.table, w.src = map[string][]map[string]any{}, map[string][]emitted{}
}

// emit files source-plane rows under the BARE org, the way the ingest door does.
func (w *warehouse) emit(org string, evs ...emitted) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.src[org] = append(w.src[org], evs...)
}

// fold executes a rollup INSERT the way the warehouse would: it reads the source
// plane under the BOUND bare org, within the BOUND window, and writes feature
// rows under the BOUND qualified key.
//
// It is deliberately faithful about which value goes where. If the statement ever
// bound the qualified key to the read or the bare org to the write, this fake
// would file the rows under a key no read uses and every loop test would fail —
// which is the point of modelling it rather than stubbing it.
func (w *warehouse) fold(sql string, args []any) {
	var name string
	for _, r := range rollups {
		if r.SQL == sql {
			name = r.Name
			break
		}
	}
	if name == "" || len(args) < 4 {
		return
	}
	qualified, _ := args[0].(string)
	bare, _ := args[1].(string)
	from, ferr := time.Parse("2006-01-02 15:04:05", args[2].(string))
	to, terr := time.Parse("2006-01-02 15:04:05", args[3].(string))
	if ferr != nil || terr != nil {
		return
	}
	for _, e := range w.src[bare] {
		if e.Plane != name || e.At.Before(from) || !e.At.Before(to) {
			continue
		}
		// THE PRODUCT PREDICATE, honoured by READING IT OUT OF THE STATEMENT rather
		// than by assuming which rollups carry it. A rollup that stopped excluding this
		// app's own facts would stop being filtered here too — which is what makes the
		// exclusion a measurement rather than a restatement of the same intent in a
		// second place.
		if e.Product != "" && strings.Contains(sql, notOurOwn) && e.Product == surface {
			continue
		}
		bucket := e.At.UTC().Truncate(5 * time.Minute)
		row := map[string]any{"subject": e.Subject, "bucket": bucket}
		switch name {
		case "person":
			row["subject_kind"] = kindPerson
			row["events"], row["sessions"], row["distincts"], row["paths"] = uint32(1), uint32(1), uint32(1), uint32(1)
		case "session":
			row["subject_kind"] = kindSession
			row["events"], row["sessions"] = uint32(1), uint32(1)
		case "fault":
			row["subject_kind"] = kindPerson
			row["errors"] = uint32(1)
		case "account":
			row["subject_kind"] = kindAccount
			row["calls"], row["tokens"], row["spend_nano"], row["ips"] = uint32(1), uint64(100), e.Spend, uint32(1)
		}
		w.table[qualified] = append(w.table[qualified], row)
	}
}

// surfaceAt is a bucket stamp a real surface could hold: aligned to the surface's
// own grain and `back` buckets before [horizon].
//
// Fixtures derive their stamps from the production constants instead of picking
// `now`, because a rollup never writes the newest [rollLag] minutes and no reader
// ever asks for them. A fixture row stamped `now` is a row that could not exist,
// and it silently stops exercising anything the moment the window is honoured.
func surfaceAt(back int) time.Time {
	return horizon(time.Now().UTC()).Add(-time.Duration(back) * featureBucket)
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

// query answers a read the way the warehouse would: under the BOUND org and
// within the BOUND window, half-open.
//
// It honours the window rather than returning the org's whole table, because a
// fake that ignores it makes every window assertion in this package a stub — a
// watermark that marked past what the surface holds, or a read that asked for
// the wrong span, would come back with the right rows anyway and no test could
// tell. The window is read out of the BOUND args for the same reason the fold
// reads the org out of them: a value that had been interpolated into the text
// would not be there to find.
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
	// The BASELINE table has no org column — that is the whole property it exists
	// to have — so its rows are filed under the table and not under a tenant. A
	// fake that keyed them by args[0] would be keying them by the window's start,
	// which is a value the caller has to guess to a second in order to see its own
	// fixture.
	key, _ := args[0].(string)
	if strings.Contains(sql, baselineTable) {
		key = baselineTable
	}
	rows := w.table[key]
	from, to, bounded := boundWindow(args)
	if !bounded {
		return rows, nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		b, ok := r["bucket"].(time.Time)
		if !ok {
			out = append(out, r)
			continue
		}
		if b := b.UTC(); b.Before(from) || !b.Before(to) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// holdBands files rows of the NETWORK BASELINE. They are filed under the table
// because that table has no tenant to file them under.
func (w *warehouse) holdBands(rows ...map[string]any) {
	w.hold(baselineTable, rows...)
}

// boundWindow recovers the half-open window a statement bound. [featureWhere] binds it
// last and as [tsLiteral] text, so the last two args that parse under that layout
// are it.
func boundWindow(args []any) (from, to time.Time, ok bool) {
	if len(args) < 3 {
		return
	}
	a, aok := args[len(args)-2].(string)
	b, bok := args[len(args)-1].(string)
	if !aok || !bok {
		return
	}
	const layout = "2006-01-02 15:04:05"
	from, aerr := time.Parse(layout, a)
	to, berr := time.Parse(layout, b)
	return from.UTC(), to.UTC(), aerr == nil && berr == nil
}

func (w *warehouse) exec(sql string, args []any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, stmt{SQL: sql, Args: args})
	if !w.up {
		return errStore
	}
	w.fold(sql, args)
	return nil
}

// rowsFor is how many feature rows are filed under a qualified key. Re-running a
// rollup over one window would double them, which is what the watermark exists to
// prevent and what [TestRollup_RollsEachWindowOnce] measures.
func (w *warehouse) rowsFor(key string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.table[key])
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
	return cloud.NewBase(cloud.Deps{Brand: brandA, DataDir: dir}, "risk")
}

// free is the money seam as a PLANE test sees it: every bound is granted and
// every meter is a no-op, so these tests measure the plane and never the ledger.
// The priced path has its own fixture ([mountBilled]) and its own tests, which is
// where a gate that stopped gating would be caught — a plane test that also
// carried a ledger would be two subjects in one assertion.
func free(string, int) (func(int), error) { return func(int) {}, nil }

// newTestPlane builds a plane over a temporary data directory. The caller closes
// it, which also waits for every background fold.
func newTestPlane(t *testing.T) *plane {
	t.Helper()
	p, err := newPlane(baseAt(t, t.TempDir()))
	if err != nil {
		t.Fatalf("newPlane: %v", err)
	}
	t.Cleanup(func() { _ = p.close(context.Background()) })
	return p
}

// holdFolds takes every fold ticket, so the plane starts no background fold for
// the duration of the test.
//
// A test that DRIVES a fold and then measures what it read is otherwise racing
// the fold the plane arms the moment a tenant becomes resident: whichever runs
// first advances the watermarks, and the other correctly reads nothing — which
// looks exactly like the defect. The tickets are the plane's own admission
// mechanism, so holding them is the real thing being quiet rather than a hook cut
// into production code for the tests.
func holdFolds(t *testing.T, p *plane) {
	t.Helper()
	for i := range maxFolds {
		select {
		case p.folds <- struct{}{}:
		default:
			t.Fatalf("only %d of %d fold tickets were free", i, maxFolds)
		}
	}
	t.Cleanup(func() {
		for range maxFolds {
			select {
			case <-p.folds:
			default:
			}
		}
	})
}

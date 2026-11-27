package benchmark

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// pods serves the arena from n replicas: n services, each with the store opened for
// itself (its own pool, as a pod has) over the one database. A SQLite file belongs to one
// process, so there the replicas share the file's one store.
func pods(t *testing.T, n int) ([]*zip.App, []*cloud.Service[state]) {
	t.Helper()
	app, s := mount(t)
	apps, svcs := []*zip.App{app}, []*cloud.Service[state]{s}
	for len(apps) < n {
		st := s.State.store
		if locus.DSN() != "" {
			dir, _ := dirs.Load(s.State.store)
			var err error
			if st, err = openStore(dir.(string)); err != nil {
				t.Fatalf("openStore: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
		}
		svc := &cloud.Service[state]{Base: s.Base, State: state{store: st, audit: s.State.audit}}
		a := zip.New(zip.Config{Logger: luxlog.New("test")})
		a.Use(cloud.Bridge())
		routes(a, svc)
		apps, svcs = append(apps, a), append(svcs, svc)
	}
	return apps, svcs
}

// together sends one request to each replica at the same moment and answers their codes.
func together(t *testing.T, apps []*zip.App, send func(app *zip.App) int) []int {
	t.Helper()
	var ready, done sync.WaitGroup
	ready.Add(len(apps))
	codes := make([]int, len(apps))
	for i, app := range apps {
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			ready.Wait()
			codes[i] = send(app)
		}()
	}
	done.Wait()
	return codes
}

// An org's daily allowance is the DEPLOYMENT'S. With room for one more request of 300
// rows, two replicas handed one each at the same moment admit exactly one: the count and
// the rows it admits are one transaction under the store's write lock, where a mutex in
// each pod let both read the same remaining room and both write.
//
// MUTATION: write without the store's lock (BeginTx in place of BeginWrite) and both
// replicas fit under the allowance.
func TestTwoReplicasShareOneDailyAllowance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		apps, svcs := pods(t, 2)
		now := time.Now().UTC()
		for i := 0; i < maxClaimsPerDay-300; i++ {
			c := claim
			c.Source = fmt.Sprintf("pre %d", i)
			if err := svcs[0].State.store.PutClaim(bg, storedClaim{publishedClaim: c, At: now, By: "acme/x", Org: "acme", Visibility: Public}); err != nil {
				t.Fatal(err)
			}
		}
		codes := together(t, apps, func(app *zip.App) int {
			rows := make([]publishedClaim, 300)
			g := fmt.Sprintf("%p", app)
			for i := range rows {
				rows[i] = claim
				rows[i].Source = fmt.Sprintf("%s %d", g, i)
			}
			code, _ := do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": rows})
			return code
		})
		ok, limited := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				limited++
			}
		}
		if ok != 1 || limited != 1 {
			t.Fatalf("replicas answered %v; want one admitted and one at the limit", codes)
		}
		if n := writtenTodayT(t, svcs[1].State.store, "acme", now); n != maxClaimsPerDay {
			t.Fatalf("%d rows today, want exactly the allowance of %d", n, maxClaimsPerDay)
		}
	})
}

// A run is imported once for the deployment: handed the same run at the same moment,
// two replicas import it once and tell the other it is there already, and its items are
// in the store once.
//
// MUTATION: check for the run and append without the store's lock and both import it.
func TestTwoReplicasImportARunOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		apps, svcs := pods(t, 2)
		publish(t, apps[0], "m1", "run-m1", 66.7)
		codes := together(t, apps, func(app *zip.App) int {
			code, _, _ := importAs(t, app, super, map[string]any{"benchmark": "gpqa_diamond", "model": "m1", "run": "run-m1", "data": items(3, 2)})
			return code
		})
		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("replicas answered %v; want one import and one 409", codes)
		}
		if n := len(attemptsIn(t, svcs[1].State.store, "gpqa_diamond")); n != 3 {
			t.Fatalf("the store holds %d attempts, want the run's 3 once", n)
		}
		// What one replica published and imported, the other serves.
		if row := board(t, apps[1], nobody)["m1"]; row.Measured == nil || row.N != 3 {
			t.Errorf("the other replica's board has %+v for m1, want its measured 3 items", row)
		}
	})
}

// What the file stores recorded is carried into the store once, whole, and left where
// it is. A replica that starts after finds the marker and carries nothing, two that start
// together carry it once between them, and a replica with no volume finds nothing.
//
// MUTATION: carry without the marker and a second open duplicates every row.
func TestWhatTheFileStoresRecordedIsCarriedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		write := func(path string, rows ...any) {
			t.Helper()
			full := filepath.Join(dir, "benchmark", path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			var body []byte
			for _, r := range rows {
				line, _ := json.Marshal(r)
				body = append(append(body, line...), '\n')
			}
			body = append(body, "not a row\n"...)
			if err := os.WriteFile(full, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		c := claim
		c.Source = "legacy card"
		write("claims/gpqa_diamond.jsonl", storedClaim{publishedClaim: c, At: at, By: "acme/x", Org: "acme", Visibility: Public})
		write("attempts/m1.jsonl",
			attempt{Benchmark: "gpqa_diamond", ID: "q1", Model: "m1", Correct: true, Answer: "A", Run: "r1", At: at},
			attempt{Benchmark: "gpqa_diamond", ID: "q2", Model: "m1", Answer: "B", Run: "r1", At: at})
		write("presets.jsonl", Preset{Name: "research", Owner: "acme", Arms: []string{"zen5-pro"}, Rank: []string{"zen5-pro"}, Panel: 1, At: at})

		first, err := openStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Close() })
		second := first
		if locus.DSN() != "" {
			if second, err = openStore(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = second.Close() })
		}

		var wg sync.WaitGroup
		carried := make([][]Legacy, 2)
		for i, st := range []*Store{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				if carried[i], err = st.importLegacy(bg, dir); err != nil {
					t.Errorf("replica %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		total := map[string]int{}
		for _, ls := range carried {
			for _, l := range ls {
				total[l.Plane] += l.Carried
			}
		}
		if total["claims"] != 1 || total["attempts"] != 2 || total["presets"] != 1 {
			t.Fatalf("the replicas carried %v between them, want 1 claim, 2 attempts and 1 preset once", total)
		}
		if n := len(storedClaimsOf(t, second, "gpqa_diamond")); n != 1 {
			t.Errorf("%d claims after carrying, want 1", n)
		}
		if n := len(attemptsIn(t, second, "gpqa_diamond")); n != 2 {
			t.Errorf("%d attempts after carrying, want 2", n)
		}
		if v := versionsIn(t, second, "acme"); len(v) != 1 || v[0].Name != "research" {
			t.Errorf("presets after carrying: %+v", v)
		}

		// A replica that starts later, or the same one again, carries nothing.
		again, err := second.importLegacy(bg, dir)
		if err != nil || len(again) != 0 {
			t.Fatalf("a later open carried %+v (%v), want nothing", again, err)
		}
		// And the files are as they were.
		if _, err := os.Stat(filepath.Join(dir, "benchmark", "presets.jsonl")); err != nil {
			t.Errorf("the legacy file is gone: %v", err)
		}
		// A replica with no volume finds no files and writes nothing.
		if none, err := second.importLegacy(bg, t.TempDir()); err != nil || len(none) != 0 {
			t.Errorf("a directory with no files carried %+v (%v)", none, err)
		}
	})
}

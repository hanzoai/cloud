package reference

// warehouse_test.go is a warehouse small enough to reason about and faithful
// enough to hold the durable half to account.
//
// It exists because three of this plane's properties are properties of the
// SEQUENCE of statements a refresh issues, not of any function: which version
// prune spares, whether a take that shrank is allowed to land, and whether a
// receipt with nothing in it becomes a current version. Every one of those was
// asserted before by reading the statement CONSTANT, which is the shape of a test
// that cannot fail — the constant was right and the call site was wrong, and a
// test that reads only the constant passes either way.
//
// So it answers the six statements this package issues, and nothing else. An
// unrecognised statement is a test failure rather than an empty result set: a
// fake that silently answers nothing to a statement it does not know is the same
// toothless test in another costume.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// warehouse is the in-memory stand-in: the two tables, and a log of every
// statement issued so a test can assert on the sequence as well as the outcome.
type warehouse struct {
	mu sync.Mutex
	// manifest is hanzo.reference_source keyed the way its ORDER BY keys it.
	manifest map[[3]string]version
	// members is hanzo.reference_entry, keyed by (set, source, version).
	members map[[3]string][]Entry
	// pruned records every prune, in order, as (set, source, keep, alsoKeep).
	pruned [][4]string
	// wrote counts entry-row writes, so "unchanged writes no rows" stays provable.
	wrote int
	// down makes every statement fail, which is a warehouse that is not up.
	down bool
}

func newWarehouse() *warehouse {
	return &warehouse{manifest: map[[3]string]version{}, members: map[[3]string][]Entry{}}
}

// use substitutes this warehouse for the real one for the life of the test, and
// resets the package's DDL latch so each test bootstraps against its own store.
//
// It must be called BEFORE mount: the restore is registered first and cleanup is
// last-in-first-out, so the mounted service's Shutdown runs — and its background
// loop stops touching these values — before they are put back.
func (w *warehouse) use(t *testing.T) {
	t.Helper()
	ready, query, exec := storeReady, storeQuery, storeExec
	tableMu.Lock()
	tableReady = false
	tableMu.Unlock()
	t.Cleanup(func() {
		storeReady, storeQuery, storeExec = ready, query, exec
		tableMu.Lock()
		tableReady = false
		tableMu.Unlock()
	})
	storeReady = func() bool { return !w.isDown() }
	storeQuery = w.query
	storeExec = w.exec
}

func (w *warehouse) isDown() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.down
}

func (w *warehouse) exec(_ context.Context, stmt string, args ...any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.down {
		return fmt.Errorf("warehouse down")
	}
	switch {
	case strings.HasPrefix(stmt, "CREATE"):
		return nil
	case strings.HasPrefix(stmt, markStatement[:40]):
		v := version{
			Set: args[0].(string), Source: args[1].(string), Version: args[2].(string),
			Origin: args[3].(string), Terms: args[4].(string),
			AsOf: args[5].(time.Time), Fetched: args[6].(time.Time),
			Keys: args[7].(uint64), Landed: args[8].(uint64),
			Status: args[9].(string), Refusal: args[10].(string),
		}
		w.manifest[[3]string{v.Set, v.Source, v.Version}] = v
		return nil
	case strings.HasPrefix(stmt, "INSERT INTO "+entryTable):
		// Nine bound values per row, in the order insert() renders them.
		for i := 0; i+9 <= len(args); i += 9 {
			k := [3]string{args[i].(string), args[i+1].(string), args[i+2].(string)}
			w.members[k] = append(w.members[k], Entry{
				Key: args[i+3].(string), Value: args[i+4].(map[string]string),
				Score: args[i+5].(float64), Orgs: args[i+6].(uint32), N: args[i+7].(uint64),
			})
			w.wrote++
		}
		return nil
	case strings.HasPrefix(stmt, "ALTER TABLE "+entryTable):
		set, source, keep, alsoKeep := args[0].(string), args[1].(string), args[2].(string), args[3].(string)
		w.pruned = append(w.pruned, [4]string{set, source, keep, alsoKeep})
		for k := range w.members {
			if k[0] == set && k[1] == source && k[2] != keep && k[2] != alsoKeep {
				delete(w.members, k)
			}
		}
		return nil
	}
	return fmt.Errorf("warehouse: no statement like %.60q", stmt)
}

func (w *warehouse) query(_ context.Context, q string, args ...any) ([]map[string]any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.down {
		return nil, fmt.Errorf("warehouse down")
	}
	switch {
	case strings.HasPrefix(q, currentStatement[:40]):
		want := args[0].(string)
		newest := map[[2]string]version{}
		for _, v := range w.manifest {
			if v.Status != want {
				continue
			}
			k := [2]string{v.Set, v.Source}
			if held, ok := newest[k]; !ok || v.Fetched.After(held.Fetched) {
				newest[k] = v
			}
		}
		out := make([]map[string]any, 0, len(newest))
		for _, v := range newest {
			out = append(out, rowOf(v))
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i]["set"].(string)+out[i]["source"].(string) < out[j]["set"].(string)+out[j]["source"].(string)
		})
		return out, nil
	case strings.HasPrefix(q, takenStatement[:40]):
		v, ok := w.manifest[[3]string{args[0].(string), args[1].(string), args[2].(string)}]
		if !ok {
			return nil, nil
		}
		return []map[string]any{rowOf(v)}, nil
	case strings.HasPrefix(q, readStatement[:30]):
		got := w.members[[3]string{args[0].(string), args[1].(string), args[2].(string)}]
		out := make([]map[string]any, 0, len(got))
		for _, e := range got {
			out = append(out, map[string]any{"key": e.Key, "value": e.Value, "score": e.Score, "orgs": e.Orgs, "n": e.N})
		}
		return out, nil
	}
	return nil, fmt.Errorf("warehouse: no query like %.60q", q)
}

func rowOf(v version) map[string]any {
	return map[string]any{
		"set": v.Set, "source": v.Source, "version": v.Version,
		"origin": v.Origin, "terms": v.Terms,
		"as_of": v.AsOf, "fetched": v.Fetched,
		"keys": v.Keys, "landed": v.Landed,
		"status": v.Status, "refusal": v.Refusal,
	}
}

// held reads one manifest row back.
func (w *warehouse) held(set, source, ver string) (version, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.manifest[[3]string{set, source, ver}]
	return v, ok
}

// rows reads one version's membership back — the thing prune deletes and an
// auditor asks for.
func (w *warehouse) rows(set, source, ver string) []Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.members[[3]string{set, source, ver}]
}

// prunes copies the prune log.
func (w *warehouse) prunes() [][4]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][4]string{}, w.pruned...)
}

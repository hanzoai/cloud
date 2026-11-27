package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// estateDoc is a document as the index writes it: two orgs in two groups,
// one private repository counted in the figures and absent from every list.
func estateDoc(read string) []byte {
	repos := []map[string]any{}
	for i := 0; i < 7; i++ {
		repos = append(repos, map[string]any{"repo": fmt.Sprintf("hanzoai/r%d", i), "org": "hanzoai", "group": "hanzo",
			"commits": 70 - i, "forge": "https://git.hanzo.ai/hanzoai/r", "mirror": true})
	}
	repos = append(repos, map[string]any{"repo": "luxfi/node", "org": "luxfi", "group": "lux", "commits": 9})
	doc := map[string]any{
		"read": read, "source": "forge", "complete": true, "months": []string{"2026-09", "2026-10"},
		"window": map[string]any{"from": "2025-10-31", "to": "2026-10-30", "by": "committer"},
		"totals": map[string]any{"orgs": 2, "repos": 9, "public": 8, "private": 1, "commits": 300, "ours": 210, "merges": 12,
			"contributors": 5, "own": 280, "forks": 20, "active": 8},
		"groups": []map[string]any{{"name": "hanzo", "orgs": 1, "commits": 291, "months": []int{200, 91}, "daily": map[string]int{"2026-10-30": 3}},
			{"name": "lux", "orgs": 1, "commits": 9}},
		"orgs": []map[string]any{{"name": "hanzoai", "group": "hanzo", "commits": 291, "private": 1,
			"forge": "https://git.hanzo.ai/hanzoai", "github": "https://github.com/hanzoai"}},
		"daily":  map[string]int{"2026-10-30": 3},
		"latest": []map[string]any{{"repo": "hanzoai/r0", "sha": "abc", "at": "2026-10-30T12:00:00Z", "message": "m"}},
		"repos":  repos,
	}
	b, _ := json.Marshal(doc)
	return b
}

func TestActivityPagesThePublicRepositories(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		mounted.Load().State.activity = newActivityCache(func(context.Context) ([]byte, error) {
			return estateDoc("2026-10-30T12:00:00Z"), nil
		})

		// Anonymous: the op is public, and nothing about it names a caller.
		code, body := do(t, app, http.MethodGet, "/v1/git/activity?limit=3&page=2", "", nil)
		if code != http.StatusOK {
			t.Fatalf("anonymous GET: want 200, got %d %s", code, body)
		}
		var out activityOut
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out.Totals.Commits != 300 || out.Totals.Ours != 210 || out.Totals.Private != 1 || out.Totals.Own != 280 || out.Totals.Orgs != 2 {
			t.Fatalf("totals = %+v", out.Totals)
		}
		if out.Repos.Total != 8 || out.Repos.Page != 2 || out.Repos.Limit != 3 || len(out.Repos.Data) != 3 || out.Repos.Data[0].Repo != "hanzoai/r3" {
			t.Fatalf("page 2 of 3 = %+v", out.Repos)
		}
		if len(out.Months) != 2 || len(out.Groups[0].Months) != 2 || out.Groups[0].Months[1] != 91 {
			t.Fatalf("months = %v, hanzo %v", out.Months, out.Groups[0].Months)
		}
		if len(out.Groups) != 2 || out.Groups[0].Daily["2026-10-30"] != 3 || out.Window.By != "committer" {
			t.Fatalf("groups/window = %+v %+v", out.Groups, out.Window)
		}

		// Narrowed to a group, the total is that group's.
		code, body = do(t, app, http.MethodGet, "/v1/git/activity?group=lux", "", nil)
		if code != http.StatusOK {
			t.Fatalf("group GET: %d %s", code, body)
		}
		_ = json.Unmarshal(body, &out)
		if out.Repos.Total != 1 || out.Repos.Data[0].Repo != "luxfi/node" {
			t.Fatalf("group=lux = %+v", out.Repos)
		}

		// Past the end is an empty page, not an error, and the limit is capped.
		_, body = do(t, app, http.MethodGet, "/v1/git/activity?org=HanzoAI&page=9&limit=999", "", nil)
		_ = json.Unmarshal(body, &out)
		if out.Repos.Total != 7 || len(out.Repos.Data) != 0 || out.Repos.Limit != 200 {
			t.Fatalf("past the end = %+v", out.Repos)
		}
	})
}

func TestActivityRefusesUntilTheEstateIsIndexed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		mounted.Load().State.activity = newActivityCache(func(context.Context) ([]byte, error) {
			return nil, errNotIndexed
		})
		code, body := do(t, app, http.MethodGet, "/v1/git/activity", "", nil)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("never indexed: want 503, got %d %s", code, body)
		}
	})
}

// A failed fetch keeps the document the cache had, and only the first read of
// a process ever waits on the store.
func TestActivityKeepsItsLastDocumentWhenTheStoreFails(t *testing.T) {
	var calls atomic.Int32
	a := newActivityCache(func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			return estateDoc("first"), nil
		}
		return nil, errors.New("the store is down")
	})
	doc, err := a.get(context.Background())
	if err != nil || doc.Read != "first" {
		t.Fatalf("first read = %v, %v", doc, err)
	}
	a.mu.Lock()
	a.at = time.Now().Add(-2 * activityFresh) // stale: the next read starts a fetch
	a.mu.Unlock()
	start := time.Now()
	doc, err = a.get(context.Background())
	if err != nil || doc.Read != "first" {
		t.Fatalf("stale read = %v, %v", doc, err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("a stale read waited %s on the store", time.Since(start))
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for {
		a.mu.Lock()
		running := a.running
		a.mu.Unlock()
		if !running || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if doc, _ = a.get(context.Background()); doc == nil || doc.Read != "first" || calls.Load() != 2 {
		t.Fatalf("after a failed fetch: doc=%v calls=%d", doc, calls.Load())
	}
}

// The answer comes from memory: a warm read is far inside the 100ms a page can
// afford, however large the estate.
func TestActivityAnswersFromMemory(t *testing.T) {
	a := newActivityCache(func(context.Context) ([]byte, error) { return estateDoc("warm"), nil })
	if _, err := a.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 1000; i++ {
		doc, err := a.get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = doc.page(&activityIn{Limit: 50})
	}
	if per := time.Since(start) / 1000; per > time.Millisecond {
		t.Fatalf("a warm read took %s", per)
	}
}

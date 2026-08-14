package lsp

// coldslots_test.go pins the one process-level bound the cold prepare path owes.
// unpack bounds a SINGLE tree (maxTree); this proves the PROCESS is bounded too —
// many cold hovers at distinct shas, each a fresh root, must not pull an unbounded
// number of whole trees into memory at once, which is the OOM a funded caller
// could otherwise buy a few cents at a time.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	luxlog "github.com/luxfi/log"
)

func TestColdPreparesAreBounded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /root", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ready{Ready: true, Cold: true, Langs: []string{"go"}})
	})
	up := httptest.NewServer(mux)
	defer up.Close()

	// The stub stands in for the tree read and records how many are resident at the
	// peak: it counts up on entry, holds briefly (a real read keeps the tree live
	// for the whole handoff to the daemon), then counts down.
	var live, peak int32
	prev := readTree
	readTree = func(ctx context.Context, org, repo, sha string) (forge.Tree, error) {
		n := atomic.AddInt32(&live, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt32(&live, -1)
		return forge.Tree{Rev: "sha", Files: nil}, nil
	}
	defer func() { readTree = prev }()

	s := &state{
		Base:   cloud.Base{Log: luxlog.New("test")},
		daemon: &daemon{url: up.URL, key: "k", http: &http.Client{Timeout: 5 * time.Second}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out Answer
			_ = s.prepare(context.Background(), nil, "acme", "repo", "sha", &out)
		}()
	}
	wg.Wait()

	switch p := int(atomic.LoadInt32(&peak)); {
	case p == 0:
		t.Fatal("no cold prepares ran — the test proved nothing")
	case p > cap(coldSlots):
		t.Fatalf("peak concurrent cold prepares = %d, want <= %d (the process is unbounded)", p, cap(coldSlots))
	}
}

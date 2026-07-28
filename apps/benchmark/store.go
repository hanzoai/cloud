package benchmark

// AttemptStore is the durability seam. The read/leaderboard/compare paths and the run
// worker go through it — NEVER pod-local files in production. fileStore is the local-dev
// backend (DataDir JSONL); the cloud backend (relational metadata + object-store raw
// responses + materialized leaderboard views) implements the same interface, so a large
// evaluation persists durably and the API pod stays stateless.
//
// Append-only + idempotent by (benchmark, item, model): re-running is free
// (cache-before-spend), a re-scored label is a new score_event, raw responses are never
// overwritten. Existing attempts import idempotently by stable id — no rsync-into-
// whichever-pod-is-live design.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type AttemptStore interface {
	// Attempts returns every stored attempt for a benchmark (the measured plane).
	Attempts(benchmark string) []attempt
	// Has reports whether (benchmark, item, model) is already attempted — the
	// cross-run cache the worker checks before spending.
	Has(benchmark, item, model string) bool
	// Append records one attempt idempotently (same key ⇒ no duplicate spend).
	Append(a attempt) error
}

// fileStore is the LOCAL-DEV backend: append-only JSONL under {DataDir}/benchmark/attempts,
// one file per model. Production swaps in the cloud backend behind this same interface.
type fileStore struct {
	dir string
	mu  sync.Mutex
}

func newFileStore(dataDir string) *fileStore {
	return &fileStore{dir: filepath.Join(dataDir, "benchmark", "attempts")}
}

func (f *fileStore) Attempts(benchmark string) []attempt {
	all := loadAttempts(f.dir)
	if benchmark == "" {
		return all
	}
	out := all[:0:0]
	for _, a := range all {
		if a.Benchmark == benchmark {
			out = append(out, a)
		}
	}
	return out
}

func (f *fileStore) Has(benchmark, item, model string) bool {
	for _, a := range f.Attempts(benchmark) {
		if a.ID == item && a.Model == model && a.Answer != "" {
			return true
		}
	}
	return false
}

func (f *fileStore) Append(a attempt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(filepath.Join(f.dir, safeName(a.Model)+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	line, _ := json.Marshal(a)
	_, err = fh.Write(append(line, '\n'))
	return err
}

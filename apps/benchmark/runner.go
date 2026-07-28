package benchmark

// The run-execution worker: turn POST /v1/benchmark/runs into real attempts. Given a
// (benchmark, target) it loads the benchmark's items, calls the target endpoint
// (OpenAI-compatible; a catalog model via the internal gateway, or a BYO endpoint+key),
// extracts + scores each answer, and appends an attempt to the append-only store —
// SKIPPING any (item, model) already attempted (cache-before-spend: never pay twice).
//
// Execution is bounded + resumable: the store is the checkpoint, so a killed run
// resumes for free. This is the async worker behind the 202 that postRun returns.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// item is one benchmark question with its gold answer. Loaded from the benchmark's
// committed item file ({DataDir}/benchmark/items/{benchmark}.jsonl) — the evaluator
// plane; kept out of any training export (answer-key isolation).
type item struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Gold     string `json:"gold"`
}

// target is what a run measures: a model id served through the internal gateway, or a
// BYO OpenAI-compatible endpoint + key (benchmark YOUR model — the cloud offering).
type target struct {
	Model    string
	Endpoint string // base URL, e.g. https://openrouter.ai/api/v1 ; "" => internal gateway
	APIKey   string
}

var boxed = regexp.MustCompile(`\\boxed\{\s*([A-Da-d])\s*\}`)
var answerIs = regexp.MustCompile(`(?i)(?:final answer|answer)\s*(?:is|:)?\s*\(?\s*([A-Da-d])\b`)

func extractLetter(s string) string {
	if m := boxed.FindAllStringSubmatch(s, -1); len(m) > 0 {
		return strings.ToUpper(m[len(m)-1][1])
	}
	if m := answerIs.FindAllStringSubmatch(s, -1); len(m) > 0 {
		return strings.ToUpper(m[len(m)-1][1])
	}
	return ""
}

// loadItems reads the benchmark's evaluator items (question + gold).
func loadItems(dataDir, benchmark string) ([]item, error) {
	p := filepath.Join(dataDir, "benchmark", "items", benchmark+".jsonl")
	fh, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("no item set for %q (%s): %w", benchmark, p, err)
	}
	defer fh.Close()
	var out []item
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var it item
		if json.Unmarshal(sc.Bytes(), &it) == nil && it.ID != "" {
			out = append(out, it)
		}
	}
	return out, nil
}

// cachedPairs returns the (item ids) already attempted for (benchmark, model) — the
// cross-run cache the worker skips. Reads the same append-only store the leaderboard does.
func cachedPairs(dataDir, benchmark, model string) map[string]bool {
	seen := map[string]bool{}
	for _, a := range loadAttempts(filepath.Join(dataDir, "benchmark", "attempts")) {
		if a.Benchmark == benchmark && a.Model == model && a.Answer != "" {
			seen[a.ID] = true
		}
	}
	return seen
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// callModel runs one OpenAI-compatible chat completion and returns the raw text.
func callModel(ctx context.Context, t target, question string) (string, error) {
	base := t.Endpoint
	if base == "" {
		base = "http://ai.hanzo.svc/v1" // internal gateway (co-resident); BYO overrides
	}
	body, _ := json.Marshal(map[string]any{
		"model":      t.Model,
		"messages":   []map[string]string{{"role": "user", "content": "Solve this graduate-level multiple-choice question. Reason step by step, then end with your final answer as \\boxed{X}.\n\n" + question}},
		"max_tokens": 16000,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	res, err := (&http.Client{Timeout: 180 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var cr chatResp
	if err := json.NewDecoder(res.Body).Decode(&cr); err != nil {
		return "", err
	}
	if cr.Error != nil {
		return "", fmt.Errorf("upstream: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("no choices")
	}
	return cr.Choices[0].Message.Content + cr.Choices[0].Message.Reasoning, nil
}

// runBenchmark executes (benchmark, target) over its item set, cache-before-spend, and
// appends attempts to the store. Returns (attempted, cached, faults). Bounded concurrency.
func runBenchmark(ctx context.Context, dataDir, benchmark string, t target, concurrency int) (int, int, int, error) {
	items, err := loadItems(dataDir, benchmark)
	if err != nil {
		return 0, 0, 0, err
	}
	cached := cachedPairs(dataDir, benchmark, t.Model)
	dir := filepath.Join(dataDir, "benchmark", "attempts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, 0, 0, err
	}
	fh, err := os.OpenFile(filepath.Join(dir, safeName(t.Model)+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, 0, 0, err
	}
	defer fh.Close()

	if concurrency < 1 {
		concurrency = 4
	}
	var mu sync.Mutex
	var attempted, faults int
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, it := range items {
		if cached[it.ID] {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it item) {
			defer wg.Done()
			defer func() { <-sem }()
			txt, cerr := callModel(ctx, t, it.Question)
			ans := extractLetter(txt)
			rec := attempt{Benchmark: benchmark, ID: it.ID, Model: t.Model, Answer: ans, Correct: ans != "" && ans == strings.ToUpper(it.Gold)}
			line, _ := json.Marshal(rec)
			mu.Lock()
			fh.Write(append(line, '\n'))
			attempted++
			if ans == "" || cerr != nil {
				faults++
			}
			mu.Unlock()
		}(it)
	}
	wg.Wait()
	return attempted, len(cached), faults, nil
}

func safeName(s string) string {
	return strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(s)
}

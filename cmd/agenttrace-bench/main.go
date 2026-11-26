// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

// Command agenttrace-bench executes the HIP-1348 matched-baseline benchmark
// comparing untraced execution, a naive synchronous JSON tracer, and Hanzo's
// batched OpenTelemetry GenAI unified agent tracer (apps/agenttrace) across all
// six canonical subsystems, plus three explicit negative regimes.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/agenttrace"
)

// BenchmarkReport is the structured JSON artifact written to results.json.
type BenchmarkReport struct {
	SchemaVersion   string                 `json:"schemaVersion"`
	HIP             string                 `json:"hip"`
	Issue           string                 `json:"issue"`
	GeneratedAtUTC  string                 `json:"generatedAtUtc"`
	Seed            uint64                 `json:"seed"`
	TasksPerRegime  int                    `json:"tasksPerRegime"`
	SpansPerTask    int                    `json:"spansPerTask"`
	Host            HostInfo               `json:"host"`
	Corpus          CorpusInfo             `json:"corpus"`
	MatchedRegimes  []RegimeResult         `json:"matchedRegimes"`
	NegativeRegimes NegativeRegimeResults  `json:"negativeRegimes"`
	HypothesisCheck HypothesisVerification `json:"hypothesisCheck"`
}

// HostInfo captures exact machine/runtime metadata for reproducibility.
type HostInfo struct {
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	GoVersion string `json:"goVersion"`
	NumCPU    int    `json:"numCpu"`
}

// CorpusInfo summarizes the real versioned hanzoai/cloud repository artifacts
// used to drive deterministic multi-hop workloads.
type CorpusInfo struct {
	RepoRoot     string   `json:"repoRoot"`
	FileCount    int      `json:"fileCount"`
	TotalBytes   int64    `json:"totalBytes"`
	CorpusSHA256 string   `json:"corpusSha256"`
	SampleFiles  []string `json:"sampleFiles"`
}

// RegimeResult records overhead and completeness metrics for one matched regime.
type RegimeResult struct {
	Name                   string  `json:"name"`
	Description            string  `json:"description"`
	Tasks                  int     `json:"tasks"`
	SpansEmitted           int64   `json:"spansEmitted"`
	SpansExported          int64   `json:"spansExported"`
	SpansDropped           int64   `json:"spansDropped"`
	TotalWallMs            float64 `json:"totalWallMs"`
	MeanTaskUs             float64 `json:"meanTaskUs"`
	P50TaskUs              float64 `json:"p50TaskUs"`
	P95TaskUs              float64 `json:"p95TaskUs"`
	P99TaskUs              float64 `json:"p99TaskUs"`
	OverheadNsPerSpan      float64 `json:"overheadNsPerSpan"`
	PureTracerNsPerSpan    float64 `json:"pureTracerNsPerSpan,omitempty"`
	OverheadUsPerTask      float64 `json:"overheadUsPerTask"`
	OverheadP50Pct         float64 `json:"overheadP50Pct"`
	OverheadP95Pct         float64 `json:"overheadP95Pct"`
	OverheadP99Pct         float64 `json:"overheadP99Pct"`
	AllocsPerTask          float64 `json:"allocsPerTask"`
	AllocsPerSpan          float64 `json:"allocsPerSpan"`
	HeapBytesPerSpan       float64 `json:"heapBytesPerSpan"`
	ExportedBytesPerTask   float64 `json:"exportedBytesPerTask"`
	Completeness6Of6Pct    float64 `json:"completeness6Of6Pct"`
	OrphanSpans            int     `json:"orphanSpans"`
	BillingReconciledPct   float64 `json:"billingReconciledPct"`
	CrossTenantLeaks       int     `json:"crossTenantLeaks"`
	SecretLeaks            int     `json:"secretLeaks"`
	OTLPValidBatchesPct    float64 `json:"otlpValidBatchesPct"`
	ExportBatchesValidated int     `json:"exportBatchesValidated"`
}

// NegativeRegimeResults aggregates all three explicit negative-regime experiments.
type NegativeRegimeResults struct {
	RawPayloadCapture   RawPayloadCaptureResult   `json:"rawPayloadCapture"`
	BurstOverload       BurstOverloadResult       `json:"burstOverload"`
	VFSMicroSpanExplode VFSMicroSpanExplodeResult `json:"vfsMicroSpanExplosion"`
}

// RawPayloadCaptureResult contrasts redact vs hash vs 64 KiB raw prompt/completion capture.
type RawPayloadCaptureResult struct {
	Tasks                    int     `json:"tasks"`
	PayloadBytesPerTask      int     `json:"payloadBytesPerTask"`
	RedactMeanTaskUs         float64 `json:"redactMeanTaskUs"`
	HashMeanTaskUs           float64 `json:"hashMeanTaskUs"`
	RawMeanTaskUs            float64 `json:"rawMeanTaskUs"`
	RedactHeapBytesPerSpan   float64 `json:"redactHeapBytesPerSpan"`
	HashHeapBytesPerSpan     float64 `json:"hashHeapBytesPerSpan"`
	RawHeapBytesPerSpan      float64 `json:"rawHeapBytesPerSpan"`
	RedactOTLPBytesPerTask   float64 `json:"redactOtlpBytesPerTask"`
	HashOTLPBytesPerTask     float64 `json:"hashOtlpBytesPerTask"`
	RawOTLPBytesPerTask      float64 `json:"rawOtlpBytesPerTask"`
	RawVsRedactByteBloatX    float64 `json:"rawVsRedactByteBloatX"`
	RawVsRedactLatencyRatioX float64 `json:"rawVsRedactLatencyRatioX"`
	RedactSecretLeaks        int     `json:"redactSecretLeaks"`
	HashSecretLeaks          int     `json:"hashSecretLeaks"`
	RawSecretLeaks           int     `json:"rawSecretLeaks"`
}

// BurstOverloadResult contrasts non-blocking ring drops vs blocking backpressure under collector lag.
type BurstOverloadResult struct {
	BurstSpans              int     `json:"burstSpans"`
	RingCapacity            int     `json:"ringCapacity"`
	BatchSize               int     `json:"batchSize"`
	SinkBatchDelayMs        float64 `json:"sinkBatchDelayMs"`
	DropModeP50EmitUs       float64 `json:"dropModeP50EmitUs"`
	DropModeP99EmitUs       float64 `json:"dropModeP99EmitUs"`
	DropModeTotalEmitMs     float64 `json:"dropModeTotalEmitMs"`
	DropModeDroppedSpans    uint64  `json:"dropModeDroppedSpans"`
	DropModeDropRatePct     float64 `json:"dropModeDropRatePct"`
	BlockModeP50EmitUs      float64 `json:"blockModeP50EmitUs"`
	BlockModeP99EmitUs      float64 `json:"blockModeP99EmitUs"`
	BlockModeTotalEmitMs    float64 `json:"blockModeTotalEmitMs"`
	BlockModeDroppedSpans   uint64  `json:"blockModeDroppedSpans"`
	BlockVsDropP99SlowdownX float64 `json:"blockVsDropP99SlowdownX"`
}

// VFSMicroSpanExplodeResult contrasts coalesced VFS ops vs tracing every 4 KiB read individually.
type VFSMicroSpanExplodeResult struct {
	Tasks                   int     `json:"tasks"`
	TransferBytesPerTask    int64   `json:"transferBytesPerTask"`
	ChunkSizeBytes          int     `json:"chunkSizeBytes"`
	CoalescedSpansPerTask   int     `json:"coalescedSpansPerTask"`
	MicroSpansPerTask       int     `json:"microSpansPerTask"`
	CoalescedMeanTaskUs     float64 `json:"coalescedMeanTaskUs"`
	MicroSpanMeanTaskUs     float64 `json:"microSpanMeanTaskUs"`
	CoalescedOTLPBytesTask  float64 `json:"coalescedOtlpBytesPerTask"`
	MicroSpanOTLPBytesTask  float64 `json:"microSpanOtlpBytesPerTask"`
	CoalescedHeapBytesTask  float64 `json:"coalescedHeapBytesPerTask"`
	MicroSpanHeapBytesTask  float64 `json:"microSpanHeapBytesPerTask"`
	SpanCardinalityRatioX   float64 `json:"spanCardinalityRatioX"`
	OTLPByteExplosionRatioX float64 `json:"otlpByteExplosionRatioX"`
	LatencyExplosionRatioX  float64 `json:"latencyExplosionRatioX"`
}

// HypothesisVerification records pass/fail against the HIP-1348 falsifiable criteria.
type HypothesisVerification struct {
	Passed                   bool    `json:"passed"`
	SpeedupVsNaiveP50X       float64 `json:"speedupVsNaiveP50X"`
	SpeedupVsNaiveP99X       float64 `json:"speedupVsNaiveP99X"`
	AllocsReductionVsNaiveX  float64 `json:"allocsReductionVsNaiveX"`
	BatchedCompletenessPct   float64 `json:"batchedCompletenessPct"`
	BatchedBillingReconcPct  float64 `json:"batchedBillingReconciledPct"`
	BatchedZeroLeaks         bool    `json:"batchedZeroLeaks"`
	NegativeRegimesConfirmed bool    `json:"negativeRegimesConfirmed"`
}

type repoArtifact struct {
	RelPath string
	Content []byte
	Digest  string
}

type workloadTask struct {
	TaskID         string
	OrgID          string
	TenantID       string
	Artifact       repoArtifact
	QueryText      string
	PromptText     string
	CompletionText string
	InputTokens    int64
	OutputTokens   int64
	CachedTokens   int64
	TTFTMs         float64
	ToolName       string
	ToolLatencyMs  float64
	VFSBytes       int64
	S3Puts         int64
	S3Gets         int64
	SandboxBootMs  float64
}

func main() {
	var (
		seed     = flag.Uint64("seed", 1348, "deterministic PRNG seed")
		tasks    = flag.Int("tasks", 120, "number of 6-subsystem agent tasks per matched regime")
		repoRoot = flag.String("repo-root", ".", "path to hanzoai/cloud repository root")
		outJSON  = flag.String("out-json", "", "optional path to write results.json")
	)
	flag.Parse()

	rep, err := runBenchmark(*seed, *tasks, *repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agenttrace-bench error: %v\n", err)
		os.Exit(1)
	}

	if *outJSON != "" {
		if err := os.MkdirAll(filepath.Dir(*outJSON), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", *outJSON, err)
			os.Exit(1)
		}
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal results: %v\n", err)
			os.Exit(1)
		}
		raw = append(raw, '\n')
		if err := os.WriteFile(*outJSON, raw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *outJSON, err)
			os.Exit(1)
		}
	}

	printSummary(rep)
}

func runBenchmark(seed uint64, numTasks int, repoRoot string) (BenchmarkReport, error) {
	if numTasks < 10 {
		numTasks = 10
	}
	artifacts, corpusInfo, err := loadRepoCorpus(repoRoot)
	if err != nil {
		return BenchmarkReport{}, err
	}
	workload := buildWorkload(seed, numTasks, artifacts)

	// Run matched regimes on a locked OS thread for stable wall-clock percentiles.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	baseRes := runUntracedBaseline(workload)
	naiveRes, err := runNaiveSyncJSONTracer(workload, baseRes)
	if err != nil {
		return BenchmarkReport{}, err
	}
	batchedRes, err := runHanzoAgentTraceBatched(workload, seed, baseRes)
	if err != nil {
		return BenchmarkReport{}, err
	}

	negRaw, err := runNegativeRawPayload(seed, artifacts)
	if err != nil {
		return BenchmarkReport{}, err
	}
	negBurst := runNegativeBurstOverload(seed)
	negVFS, err := runNegativeVFSMicroSpans(seed, artifacts)
	if err != nil {
		return BenchmarkReport{}, err
	}

	speedupP50 := safeRatio(naiveRes.OverheadUsPerTask, math.Max(0.1, batchedRes.OverheadUsPerTask))
	speedupP99 := safeRatio(naiveRes.P99TaskUs-baseRes.P99TaskUs, math.Max(0.1, batchedRes.P99TaskUs-baseRes.P99TaskUs))
	allocReduction := safeRatio(naiveRes.AllocsPerSpan, batchedRes.AllocsPerSpan)

	negConfirmed := negRaw.RawVsRedactByteBloatX >= 10.0 &&
		negRaw.RawSecretLeaks > 0 &&
		negRaw.RedactSecretLeaks == 0 &&
		negBurst.DropModeDroppedSpans > 0 &&
		negBurst.BlockModeDroppedSpans == 0 &&
		negBurst.BlockVsDropP99SlowdownX >= 2.0 &&
		negVFS.SpanCardinalityRatioX >= 100.0 &&
		negVFS.OTLPByteExplosionRatioX >= 50.0

	zeroLeaks := batchedRes.CrossTenantLeaks == 0 && batchedRes.SecretLeaks == 0 && batchedRes.OrphanSpans == 0
	passed := batchedRes.Completeness6Of6Pct == 100.0 &&
		batchedRes.BillingReconciledPct == 100.0 &&
		batchedRes.OTLPValidBatchesPct == 100.0 &&
		zeroLeaks &&
		speedupP50 >= 2.0 &&
		negConfirmed

	return BenchmarkReport{
		SchemaVersion:  "hip-1348/v1",
		HIP:            "HIP-1348",
		Issue:          "hanzoai/cloud#29",
		GeneratedAtUTC: time.Now().UTC().Format(time.RFC3339),
		Seed:           seed,
		TasksPerRegime: numTasks,
		SpansPerTask:   7,
		Host: HostInfo{
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
			GoVersion: runtime.Version(),
			NumCPU:    runtime.NumCPU(),
		},
		Corpus: corpusInfo,
		MatchedRegimes: []RegimeResult{
			baseRes,
			naiveRes,
			batchedRes,
		},
		NegativeRegimes: NegativeRegimeResults{
			RawPayloadCapture:   negRaw,
			BurstOverload:       negBurst,
			VFSMicroSpanExplode: negVFS,
		},
		HypothesisCheck: HypothesisVerification{
			Passed:                   passed,
			SpeedupVsNaiveP50X:       round2(speedupP50),
			SpeedupVsNaiveP99X:       round2(speedupP99),
			AllocsReductionVsNaiveX:  round2(allocReduction),
			BatchedCompletenessPct:   batchedRes.Completeness6Of6Pct,
			BatchedBillingReconcPct:  batchedRes.BillingReconciledPct,
			BatchedZeroLeaks:         zeroLeaks,
			NegativeRegimesConfirmed: negConfirmed,
		},
	}, nil
}

// loadRepoCorpus reads real versioned files from the hanzoai/cloud repository in
// deterministic sorted order so the benchmark drives realistic file digests and sizes.
func loadRepoCorpus(repoRoot string) ([]repoArtifact, CorpusInfo, error) {
	candidates := []string{
		"go.mod",
		"LLM.md",
		"Makefile",
		"config.go",
		"serve.go",
		"middleware_tracing.go",
		"telemetry.go",
		"apps/apps.go",
		"apps/imports_test.go",
		"apps/agenttrace/context.go",
		"apps/agenttrace/dlp.go",
		"apps/agenttrace/otlp.go",
		"apps/agenttrace/verify.go",
		"apps/agenttrace/tracer.go",
		"apps/agenttrace/http.go",
	}

	var artifacts []repoArtifact
	var totalBytes int64
	corpusHasher := sha256.New()
	var sampleNames []string

	for _, rel := range candidates {
		full := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		artifacts = append(artifacts, repoArtifact{
			RelPath: rel,
			Content: data,
			Digest:  digest,
		})
		totalBytes += int64(len(data))
		corpusHasher.Write([]byte(rel))
		corpusHasher.Write(sum[:])
		if len(sampleNames) < 6 {
			sampleNames = append(sampleNames, rel)
		}
	}

	if len(artifacts) == 0 {
		return nil, CorpusInfo{}, fmt.Errorf("no hanzoai/cloud corpus files found under %q", repoRoot)
	}

	return artifacts, CorpusInfo{
		RepoRoot:     repoRoot,
		FileCount:    len(artifacts),
		TotalBytes:   totalBytes,
		CorpusSHA256: "sha256:" + hex.EncodeToString(corpusHasher.Sum(nil)),
		SampleFiles:  sampleNames,
	}, nil
}

func buildWorkload(seed uint64, numTasks int, artifacts []repoArtifact) []workloadTask {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	orgs := []string{"org-alpha", "org-beta", "org-gamma", "org-delta"}
	tools := []string{"code_search", "read_file", "apply_patch", "run_tests", "git_diff"}

	tasks := make([]workloadTask, numTasks)
	for i := 0; i < numTasks; i++ {
		art := artifacts[i%len(artifacts)]
		org := orgs[i%len(orgs)]
		inTok := int64(512 + rng.IntN(3072))
		outTok := int64(128 + rng.IntN(768))
		cachedTok := inTok / 2
		// Embed deterministic canary secret + PII in prompt to prove DLP enforcement.
		prompt := fmt.Sprintf("Analyze %s (size=%d) for engineer-%d@hanzo.test using key sk-canary-%016x",
			art.RelPath, len(art.Content), i, rng.Uint64())
		completion := fmt.Sprintf("Verified %s with %d symbols and clean boundaries.", art.RelPath, 10+rng.IntN(90))
		tasks[i] = workloadTask{
			TaskID:         fmt.Sprintf("task-hip1348-%04d", i),
			OrgID:          org,
			TenantID:       org,
			Artifact:       art,
			QueryText:      fmt.Sprintf("lookup symbols in %s for user-%d@hanzo.test", art.RelPath, i),
			PromptText:     prompt,
			CompletionText: completion,
			InputTokens:    inTok,
			OutputTokens:   outTok,
			CachedTokens:   cachedTok,
			TTFTMs:         float64(45+rng.IntN(120)) + 0.5,
			ToolName:       tools[i%len(tools)],
			ToolLatencyMs:  float64(4+rng.IntN(25)) + 0.25,
			VFSBytes:       int64(len(art.Content)),
			S3Puts:         int64(1 + rng.IntN(3)),
			S3Gets:         int64(2 + rng.IntN(6)),
			SandboxBootMs:  float64(28+rng.IntN(40)) + 0.5,
		}
	}
	return tasks
}

// executeSubsystemWork performs a deterministic, repeatable ~200us CPU work unit
// across all 6 subsystems over the real versioned repo artifact bytes, kept
// identical across all 3 matched regimes.
var workSink uint64

func executeSubsystemWork(t *workloadTask) uint64 {
	var acc uint64
	// 1. Sandbox policy digest verification
	pSum := sha256.Sum256([]byte(t.Artifact.Digest))
	acc ^= uint64(pSum[0])<<8 | uint64(pSum[1])

	// 2. Retrieval hash scan over deterministic 32 KiB window of real repo bytes
	chunk := t.Artifact.Content
	if len(chunk) > 8192 {
		chunk = chunk[:8192]
	}
	for round := 0; round < 6; round++ {
		rSum := sha256.Sum256(chunk)
		acc ^= uint64(rSum[round%8]) << 8
	}

	// 3. Model token/line count pass
	lines := uint64(bytes.Count(chunk, []byte{'\n'}))
	acc += lines + uint64(t.InputTokens+t.OutputTokens)

	// 4. MCP tool digest
	mSum := sha256.Sum256([]byte(t.ToolName + ":" + t.Artifact.RelPath))
	acc ^= uint64(mSum[3])

	// 5. VFS checksum passes
	for round := 0; round < 2; round++ {
		vSum := sha256.Sum256(chunk[:min(len(chunk), 4096)])
		acc ^= uint64(vSum[5])
	}

	// 6. Billing integer calculation
	rc := agenttrace.DefaultRateCard()
	acc += uint64(rc.ModelCostMicroUSD(t.InputTokens, t.OutputTokens, t.CachedTokens))
	workSink ^= acc
	return acc
}

func runUntracedBaseline(workload []workloadTask) RegimeResult {
	// Warmup all artifacts into CPU cache before timing.
	for i := range workload {
		executeSubsystemWork(&workload[i])
	}
	runtime.GC()
	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	durs := make([]float64, len(workload))
	t0 := time.Now()
	for i := range workload {
		s := time.Now()
		executeSubsystemWork(&workload[i])
		durs[i] = float64(time.Since(s).Nanoseconds()) / 1000.0
	}
	totalWall := time.Since(t0)
	runtime.ReadMemStats(&mAfter)

	meanUs, p50, p95, p99 := summarizeMicros(durs)
	allocsPerTask := float64(mAfter.Mallocs-mBefore.Mallocs) / float64(len(workload))

	return RegimeResult{
		Name:                 "untraced_baseline",
		Description:          "Zero tracing instrumentation; identical 6-subsystem repo-artifact work unit (0% completeness).",
		Tasks:                len(workload),
		SpansEmitted:         0,
		SpansExported:        0,
		SpansDropped:         0,
		TotalWallMs:          round3(float64(totalWall.Nanoseconds()) / 1e6),
		MeanTaskUs:           round2(meanUs),
		P50TaskUs:            round2(p50),
		P95TaskUs:            round2(p95),
		P99TaskUs:            round2(p99),
		OverheadNsPerSpan:    0,
		OverheadUsPerTask:    0,
		OverheadP50Pct:       0,
		OverheadP95Pct:       0,
		OverheadP99Pct:       0,
		AllocsPerTask:        round2(allocsPerTask),
		AllocsPerSpan:        0,
		HeapBytesPerSpan:     0,
		ExportedBytesPerTask: 0,
		Completeness6Of6Pct:  0,
		OrphanSpans:          0,
		BillingReconciledPct: 0,
		CrossTenantLeaks:     0,
		SecretLeaks:          0,
		OTLPValidBatchesPct:  0,
	}
}

// naiveSyncTracer models a naive per-span synchronous tracer: global sync.Mutex,
// map[string]any attribute construction, encoding/json.Marshal, and synchronous
// unbuffered file write + fsync per span.
type naiveSyncTracer struct {
	mu           sync.Mutex
	f            *os.File
	bytesWritten int64
	spansWritten int64
	secretLeaks  int
}

func (n *naiveSyncTracer) emit(rec map[string]any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if agenttrace.ContainsSecret(string(raw)) {
		n.secretLeaks++
	}
	wb, err := n.f.Write(raw)
	if err != nil {
		return err
	}
	// Synchronous disk flush on every span (naive per-span fsync anti-pattern).
	if err := n.f.Sync(); err != nil {
		return err
	}
	n.bytesWritten += int64(wb)
	n.spansWritten++
	return nil
}

func runNaiveSyncJSONTracer(workload []workloadTask, base RegimeResult) (RegimeResult, error) {
	tmpFile, err := os.CreateTemp("", "agenttrace-naive-*.jsonl")
	if err != nil {
		return RegimeResult{}, err
	}
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	nt := &naiveSyncTracer{f: tmpFile}
	rc := agenttrace.DefaultRateCard()

	runtime.GC()
	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	durs := make([]float64, len(workload))
	t0 := time.Now()
	for i := range workload {
		wt := &workload[i]
		s := time.Now()
		executeSubsystemWork(wt)

		now := time.Now().UnixNano()
		cost := rc.ModelCostMicroUSD(wt.InputTokens, wt.OutputTokens, wt.CachedTokens) +
			rc.RetrievalCallMicroUSD + rc.MCPCallMicroUSD +
			wt.S3Puts*rc.S3PutMicroUSD + wt.S3Gets*rc.S3GetMicroUSD +
			int64(math.Ceil(wt.SandboxBootMs))*rc.SandboxMicroUSDPerMs

		records := []map[string]any{
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "root", "op": "invoke_agent", "ts": now},
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "sandbox", "boot_ms": wt.SandboxBootMs, "policy": wt.Artifact.Digest, "ts": now},
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "retrieval", "query": wt.QueryText, "top_k": 8, "hits": 5, "ts": now},
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "model", "model": "zen-72b", "in_tok": wt.InputTokens, "out_tok": wt.OutputTokens, "prompt": wt.PromptText, "completion": wt.CompletionText, "ts": now},
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "mcp", "tool": wt.ToolName, "latency_ms": wt.ToolLatencyMs, "status": "ok", "ts": now},
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "vfs", "op": "snapshot", "bytes": wt.VFSBytes, "s3_puts": wt.S3Puts, "s3_gets": wt.S3Gets, "ts": now},
			{"task_id": wt.TaskID, "org_id": wt.OrgID, "subsystem": "billing", "cost_micro_usd": cost, "ts": now},
		}
		for _, rec := range records {
			if err := nt.emit(rec); err != nil {
				return RegimeResult{}, err
			}
		}
		durs[i] = float64(time.Since(s).Nanoseconds()) / 1000.0
	}
	totalWall := time.Since(t0)
	runtime.ReadMemStats(&mAfter)

	meanUs, p50, p95, p99 := summarizeMicros(durs)
	spansPerTask := 7.0
	overheadTaskUs := p50 - base.P50TaskUs
	overheadNsSpan := (overheadTaskUs * 1000.0) / spansPerTask
	allocsTask := float64(mAfter.Mallocs-mBefore.Mallocs) / float64(len(workload))
	heapBytesTask := float64(mAfter.TotalAlloc-mBefore.TotalAlloc) / float64(len(workload))

	return RegimeResult{
		Name:                 "naive_sync_json_tracer",
		Description:          "Per-span global mutex + map[string]any + encoding/json.Marshal + synchronous file write & fsync; no W3C DAG, no OTLP/JSON, no DLP redaction.",
		Tasks:                len(workload),
		SpansEmitted:         nt.spansWritten,
		SpansExported:        nt.spansWritten,
		SpansDropped:         0,
		TotalWallMs:          round3(float64(totalWall.Nanoseconds()) / 1e6),
		MeanTaskUs:           round2(meanUs),
		P50TaskUs:            round2(p50),
		P95TaskUs:            round2(p95),
		P99TaskUs:            round2(p99),
		OverheadNsPerSpan:    round2(overheadNsSpan),
		OverheadUsPerTask:    round2(overheadTaskUs),
		OverheadP50Pct:       round2(pctDelta(p50, base.P50TaskUs)),
		OverheadP95Pct:       round2(pctDelta(p95, base.P95TaskUs)),
		OverheadP99Pct:       round2(pctDelta(p99, base.P99TaskUs)),
		AllocsPerTask:        round2(allocsTask),
		AllocsPerSpan:        round2(allocsTask / spansPerTask),
		HeapBytesPerSpan:     round2(heapBytesTask / spansPerTask),
		ExportedBytesPerTask: round2(float64(nt.bytesWritten) / float64(len(workload))),
		Completeness6Of6Pct:  0,
		OrphanSpans:          int(nt.spansWritten),
		BillingReconciledPct: 0,
		CrossTenantLeaks:     0,
		SecretLeaks:          nt.secretLeaks,
		OTLPValidBatchesPct:  0,
	}, nil
}

func runHanzoAgentTraceBatched(workload []workloadTask, seed uint64, base RegimeResult) (RegimeResult, error) {
	tmpFile, err := os.CreateTemp("", "agenttrace-batched-*.otlp.jsonl")
	if err != nil {
		return RegimeResult{}, err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	writerSink := agenttrace.NewWriterSink(tmpFile, true)
	tr := agenttrace.NewTracer(agenttrace.TracerConfig{
		CaptureMode:       agenttrace.CaptureRedact,
		RingCapacity:      8192,
		BatchSize:         256,
		FlushInterval:     25 * time.Millisecond,
		BackpressureMode:  agenttrace.BackpressureDropNewest,
		DeterministicSeed: seed,
		EnableStore:       true,
		StoreConfig:       agenttrace.TraceStoreConfig{MaxTasksPerOrg: len(workload) + 64},
		Sink:              writerSink,
	})

	// Warmup + isolated pure-tracer ns/span measurement over 500 tasks (3,500 spans).
	pureTr := agenttrace.NewTracer(agenttrace.TracerConfig{
		CaptureMode:       agenttrace.CaptureRedact,
		RingCapacity:      16384,
		BatchSize:         256,
		FlushInterval:     50 * time.Millisecond,
		DeterministicSeed: seed + 1,
		Sink:              agenttrace.NewWriterSink(io.Discard, false),
	})
	for i := 0; i < min(40, len(workload)); i++ {
		emitWorkloadTask(pureTr, &workload[i])
	}
	const pureIters = 500
	pureStart := time.Now()
	for i := 0; i < pureIters; i++ {
		emitWorkloadTask(pureTr, &workload[i%len(workload)])
	}
	pureElapsed := time.Since(pureStart)
	_ = pureTr.Shutdown(context.Background())
	pureTracerNsPerSpan := float64(pureElapsed.Nanoseconds()) / float64(pureIters*7)

	runtime.GC()
	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	durs := make([]float64, len(workload))
	t0 := time.Now()
	for i := range workload {
		wt := &workload[i]
		s := time.Now()
		executeSubsystemWork(wt)
		emitWorkloadTask(tr, wt)
		durs[i] = float64(time.Since(s).Nanoseconds()) / 1000.0
	}
	if err := tr.Flush(context.Background()); err != nil {
		return RegimeResult{}, err
	}
	totalWall := time.Since(t0)
	runtime.ReadMemStats(&mAfter)

	st := tr.Stats()
	if err := tr.Shutdown(context.Background()); err != nil {
		return RegimeResult{}, err
	}
	if err := tmpFile.Close(); err != nil {
		return RegimeResult{}, err
	}

	// Verify completeness, billing reconciliation, DLP secret hygiene, and cross-tenant isolation across ALL tasks.
	completeTasks := 0
	billingOKTasks := 0
	orphanSpans := 0
	secretLeaks := 0
	crossTenantLeaks := 0

	for i := range workload {
		wt := &workload[i]
		spans, err := tr.Store().GetTask(wt.OrgID, wt.TenantID, wt.TaskID)
		if err != nil {
			continue
		}
		rep := agenttrace.VerifyTaskCompleteness(spans, agenttrace.VerifyOptions{
			RateCard:         tr.RateCard(),
			ExpectedOrgID:    wt.OrgID,
			ExpectedTenantID: wt.TenantID,
		})
		if rep.Complete {
			completeTasks++
		}
		if rep.BillingReconciled {
			billingOKTasks++
		}
		orphanSpans += rep.OrphanSpanCount
		secretLeaks += rep.SecretLeakCount

		// Probe from an unauthorized org to confirm 0 cross-tenant leaks.
		otherOrg := "org-unauthorized"
		if otherOrg == wt.OrgID {
			otherOrg = "org-unauthorized-2"
		}
		if leakedSpans, err := tr.Store().GetTask(otherOrg, otherOrg, wt.TaskID); err == nil || len(leakedSpans) > 0 {
			crossTenantLeaks++
		}
	}

	// Validate every emitted OTLP/JSON batch line on disk against strict OTLP schema.
	validBatches, totalBatches, err := validateOTLPFile(tmpPath)
	if err != nil {
		return RegimeResult{}, err
	}

	meanUs, p50, p95, p99 := summarizeMicros(durs)
	spansPerTask := 7.0
	overheadTaskUs := p50 - base.P50TaskUs
	overheadNsSpan := (overheadTaskUs * 1000.0) / spansPerTask
	allocsTask := float64(mAfter.Mallocs-mBefore.Mallocs) / float64(len(workload))
	heapBytesTask := float64(mAfter.TotalAlloc-mBefore.TotalAlloc) / float64(len(workload))

	otlpValidPct := 0.0
	if totalBatches > 0 {
		otlpValidPct = 100.0 * float64(validBatches) / float64(totalBatches)
	}

	return RegimeResult{
		Name:                   "hanzo_agenttrace_batched",
		Description:            "Unified HIP-1348 W3C + OTel GenAI tracing across all 6 subsystems with zero-interface Attrs, default DLP redaction, bounded ring buffer, and batched OTLP/JSON export.",
		Tasks:                  len(workload),
		SpansEmitted:           int64(st.Ring.EnqueuedSpans + st.Ring.DroppedSpans),
		SpansExported:          int64(st.ExportedSpans),
		SpansDropped:           int64(st.Ring.DroppedSpans),
		TotalWallMs:            round3(float64(totalWall.Nanoseconds()) / 1e6),
		MeanTaskUs:             round2(meanUs),
		P50TaskUs:              round2(p50),
		P95TaskUs:              round2(p95),
		P99TaskUs:              round2(p99),
		OverheadNsPerSpan:      round2(overheadNsSpan),
		PureTracerNsPerSpan:    round2(pureTracerNsPerSpan),
		OverheadUsPerTask:      round2(overheadTaskUs),
		OverheadP50Pct:         round2(pctDelta(p50, base.P50TaskUs)),
		OverheadP95Pct:         round2(pctDelta(p95, base.P95TaskUs)),
		OverheadP99Pct:         round2(pctDelta(p99, base.P99TaskUs)),
		AllocsPerTask:          round2(allocsTask),
		AllocsPerSpan:          round2(allocsTask / spansPerTask),
		HeapBytesPerSpan:       round2(heapBytesTask / spansPerTask),
		ExportedBytesPerTask:   round2(float64(writerSink.BytesWritten()) / float64(len(workload))),
		Completeness6Of6Pct:    round2(100.0 * float64(completeTasks) / float64(len(workload))),
		OrphanSpans:            orphanSpans,
		BillingReconciledPct:   round2(100.0 * float64(billingOKTasks) / float64(len(workload))),
		CrossTenantLeaks:       crossTenantLeaks,
		SecretLeaks:            secretLeaks,
		OTLPValidBatchesPct:    round2(otlpValidPct),
		ExportBatchesValidated: validBatches,
	}, nil
}

func emitWorkloadTask(tr *agenttrace.Tracer, wt *workloadTask) {
	ctx, root, _ := tr.StartTask(context.Background(), wt.TaskID, wt.OrgID, wt.TenantID, "kai-coder")
	meter := tr.NewTaskMeter()

	// 1. Sandbox startup (hanzoai/visor)
	sbIn := agenttrace.SandboxSpanInput{
		SandboxID:    "sbx-" + wt.TaskID,
		BootMs:       wt.SandboxBootMs,
		ImageRef:     "ghcr.io/hanzoai/visor-dev:v1.4",
		PolicyDigest: wt.Artifact.Digest,
	}
	_, sbSp, _ := tr.StartSandboxSpan(ctx, sbIn)
	meter.RecordSandbox(sbIn)
	sbSp.End()

	// 2. Retrieval across W3C traceparent/tracestate HTTP header hop (hanzoai/index)
	wireHdr := make(http.Header, 3)
	agenttrace.InjectHeaders(wireHdr, root.Context())
	remoteTC, _ := agenttrace.ExtractHeaders(wireHdr)
	remoteCtx := agenttrace.WithTaskContext(context.Background(), remoteTC)

	retIn := agenttrace.RetrievalSpanInput{
		Backend:  "index",
		Query:    wt.QueryText,
		TopK:     8,
		HitCount: 5,
		ACLScope: "org:" + wt.OrgID + "/repo:cloud",
	}
	_, retSp, _ := tr.StartRetrievalSpan(remoteCtx, retIn)
	meter.RecordRetrieval(retIn)
	retSp.End()

	// 3. Model call (Enso routing + Zen generation)
	modIn := agenttrace.ModelSpanInput{
		System:        agenttrace.SystemHanzoZen,
		EnsoRoute:     "enso://us-west/zen-72b-instruct",
		RequestModel:  "zen-72b-instruct",
		ResponseModel: "zen-72b-instruct-2026",
		FinishReason:  "tool_calls",
		InputTokens:   wt.InputTokens,
		OutputTokens:  wt.OutputTokens,
		CachedTokens:  wt.CachedTokens,
		TTFTMs:        wt.TTFTMs,
		Prompt:        wt.PromptText,
		Completion:    wt.CompletionText,
	}
	modCtx, modSp, _ := tr.StartModelSpan(ctx, modIn)
	meter.RecordModel(modIn)
	modSp.End()

	// 4. MCP tool call nested under model span (hanzoai/mcp)
	mcpIn := agenttrace.MCPSpanInput{
		Server:     "hanzo-code-mcp",
		ToolName:   wt.ToolName,
		ToolCallID: "call-" + wt.TaskID,
		Status:     "ok",
		LatencyMs:  wt.ToolLatencyMs,
	}
	mcpCtx, mcpSp, _ := tr.StartMCPSpan(modCtx, mcpIn)
	meter.RecordMCP(mcpIn)
	mcpSp.End()

	// 5. VFS coalesced snapshot nested under MCP span (hanzoai/vfs -> hanzoai/s3)
	vfsIn := agenttrace.VFSSpanInput{
		Op:           "snapshot",
		Path:         wt.Artifact.RelPath,
		Bytes:        wt.VFSBytes,
		S3PutCount:   wt.S3Puts,
		S3GetCount:   wt.S3Gets,
		CoalescedOps: 8,
	}
	_, vfsSp, _ := tr.StartVFSSpan(mcpCtx, vfsIn)
	meter.RecordVFS(vfsIn)
	vfsSp.End()

	// 6. Billing & Cloud metering reconciled 1:1 under the same task_id
	_, billSp, _ := tr.StartBillingSpan(ctx, meter.BillingInput("meter-"+wt.TaskID))
	billSp.End()

	root.End()
}

// Negative Regime A: Full raw 64 KiB prompt + 64 KiB completion payload capture
// vs default CaptureRedact and CaptureHash.
func runNegativeRawPayload(seed uint64, artifacts []repoArtifact) (RawPayloadCaptureResult, error) {
	const tasks = 60
	const partBytes = 64 * 1024 // 64 KiB prompt + 64 KiB completion = 128 KiB raw payload/task
	wl := buildWorkload(seed+101, tasks, artifacts)

	// Pad each task's prompt and completion deterministically to 64 KiB using repo content + canary secret.
	var padBuf bytes.Buffer
	for padBuf.Len() < partBytes {
		for _, a := range artifacts {
			padBuf.Write(a.Content)
			if padBuf.Len() >= partBytes {
				break
			}
		}
	}
	basePad := padBuf.String()[:partBytes-128]
	for i := range wl {
		wl[i].PromptText = fmt.Sprintf("PROMPT-%04d sk-rawleak-%016x user%d@hanzo.test ", i, uint64(i+1348), i) + basePad
		wl[i].CompletionText = fmt.Sprintf("COMPLETION-%04d ", i) + basePad
	}

	measureMode := func(mode agenttrace.ContentCaptureMode) (meanUs, heapBytesSpan, otlpBytesTask float64, secretLeaks int) {
		var outBuf bytes.Buffer
		sink := agenttrace.NewWriterSink(&outBuf, false)
		tr := agenttrace.NewTracer(agenttrace.TracerConfig{
			CaptureMode:       mode,
			RingCapacity:      4096,
			BatchSize:         64,
			FlushInterval:     10 * time.Millisecond,
			DeterministicSeed: seed + 101,
			EnableStore:       true,
			Sink:              sink,
		})
		runtime.GC()
		var mBefore, mAfter runtime.MemStats
		runtime.ReadMemStats(&mBefore)

		t0 := time.Now()
		for i := range wl {
			emitWorkloadTask(tr, &wl[i])
		}
		_ = tr.Flush(context.Background())
		elapsed := time.Since(t0)
		runtime.ReadMemStats(&mAfter)
		_ = tr.Shutdown(context.Background())

		for i := range wl {
			spans, err := tr.Store().GetTask(wl[i].OrgID, wl[i].TenantID, wl[i].TaskID)
			if err != nil {
				continue
			}
			rep := agenttrace.VerifyTaskCompleteness(spans, agenttrace.VerifyOptions{RateCard: tr.RateCard()})
			secretLeaks += rep.SecretLeakCount
		}

		meanUs = float64(elapsed.Nanoseconds()) / 1000.0 / float64(tasks)
		heapBytesSpan = float64(mAfter.TotalAlloc-mBefore.TotalAlloc) / float64(tasks*7)
		otlpBytesTask = float64(sink.BytesWritten()) / float64(tasks)
		return
	}

	redUs, redHeap, redBytes, redLeaks := measureMode(agenttrace.CaptureRedact)
	hashUs, hashHeap, hashBytes, hashLeaks := measureMode(agenttrace.CaptureHash)
	rawUs, rawHeap, rawBytes, rawLeaks := measureMode(agenttrace.CaptureRaw)

	return RawPayloadCaptureResult{
		Tasks:                    tasks,
		PayloadBytesPerTask:      len(wl[0].PromptText) + len(wl[0].CompletionText),
		RedactMeanTaskUs:         round2(redUs),
		HashMeanTaskUs:           round2(hashUs),
		RawMeanTaskUs:            round2(rawUs),
		RedactHeapBytesPerSpan:   round2(redHeap),
		HashHeapBytesPerSpan:     round2(hashHeap),
		RawHeapBytesPerSpan:      round2(rawHeap),
		RedactOTLPBytesPerTask:   round2(redBytes),
		HashOTLPBytesPerTask:     round2(hashBytes),
		RawOTLPBytesPerTask:      round2(rawBytes),
		RawVsRedactByteBloatX:    round2(safeRatio(rawBytes, redBytes)),
		RawVsRedactLatencyRatioX: round2(safeRatio(rawUs, redUs)),
		RedactSecretLeaks:        redLeaks,
		HashSecretLeaks:          hashLeaks,
		RawSecretLeaks:           rawLeaks,
	}, nil
}

// Negative Regime B: Extreme burst overload beyond ring buffer capacity with a
// lagging exporter (2ms/batch), contrasting DropNewest vs Block backpressure.
func runNegativeBurstOverload(seed uint64) BurstOverloadResult {
	const (
		burstSpans = 4096
		ringCap    = 256
		batchSize  = 64
		sinkDelay  = 2 * time.Millisecond
	)

	runMode := func(mode agenttrace.BackpressureMode) (p50Us, p99Us, totalMs float64, dropped uint64) {
		tr := agenttrace.NewTracer(agenttrace.TracerConfig{
			CaptureMode:       agenttrace.CaptureRedact,
			RingCapacity:      ringCap,
			BatchSize:         batchSize,
			FlushInterval:     10 * time.Millisecond,
			BackpressureMode:  mode,
			DeterministicSeed: seed + 202,
			Sink:              &agenttrace.SlowSink{Delay: sinkDelay, Inner: agenttrace.NewWriterSink(io.Discard, false)},
		})
		ctx, root, _ := tr.StartTask(context.Background(), "burst-root", "org-burst", "org-burst", "kai")
		in := agenttrace.MCPSpanInput{Server: "mcp", ToolName: "ping", ToolCallID: "c1", Status: "ok", LatencyMs: 1}

		durs := make([]float64, burstSpans)
		t0 := time.Now()
		for i := 0; i < burstSpans; i++ {
			s := time.Now()
			_, sp, _ := tr.StartMCPSpan(ctx, in)
			sp.End()
			durs[i] = float64(time.Since(s).Nanoseconds()) / 1000.0
		}
		totalMs = float64(time.Since(t0).Nanoseconds()) / 1e6
		root.End()
		_ = tr.Shutdown(context.Background())

		st := tr.Stats()
		_, p50Us, _, p99Us = summarizeMicros(durs)
		return p50Us, p99Us, totalMs, st.Ring.DroppedSpans
	}

	dropP50, dropP99, dropTotalMs, dropCount := runMode(agenttrace.BackpressureDropNewest)
	blkP50, blkP99, blkTotalMs, blkCount := runMode(agenttrace.BackpressureBlock)

	return BurstOverloadResult{
		BurstSpans:              burstSpans,
		RingCapacity:            ringCap,
		BatchSize:               batchSize,
		SinkBatchDelayMs:        float64(sinkDelay.Milliseconds()),
		DropModeP50EmitUs:       round2(dropP50),
		DropModeP99EmitUs:       round2(dropP99),
		DropModeTotalEmitMs:     round3(dropTotalMs),
		DropModeDroppedSpans:    dropCount,
		DropModeDropRatePct:     round2(100.0 * float64(dropCount) / float64(burstSpans+1)),
		BlockModeP50EmitUs:      round2(blkP50),
		BlockModeP99EmitUs:      round2(blkP99),
		BlockModeTotalEmitMs:    round3(blkTotalMs),
		BlockModeDroppedSpans:   blkCount,
		BlockVsDropP99SlowdownX: round2(safeRatio(blkP99, math.Max(0.01, dropP99))),
	}
}

// Negative Regime C: Micro-span over-instrumentation tracing every 4 KiB VFS
// read individually (1,024 spans for a 4 MiB transfer) vs 1 coalesced VFS span.
func runNegativeVFSMicroSpans(seed uint64, artifacts []repoArtifact) (VFSMicroSpanExplodeResult, error) {
	const (
		tasks         = 30
		transferBytes = 4 * 1024 * 1024 // 4 MiB
		chunkBytes    = 4 * 1024        // 4 KiB
		numChunks     = transferBytes / chunkBytes
	)
	wl := buildWorkload(seed+303, tasks, artifacts)

	measure := func(coalesced bool) (meanUs, otlpBytesTask, heapBytesTask float64, spansPerTask int) {
		sink := agenttrace.NewWriterSink(io.Discard, false)
		tr := agenttrace.NewTracer(agenttrace.TracerConfig{
			CaptureMode:       agenttrace.CaptureRedact,
			RingCapacity:      65536,
			BatchSize:         512,
			FlushInterval:     10 * time.Millisecond,
			DeterministicSeed: seed + 303,
			Sink:              sink,
		})

		runtime.GC()
		var mBefore, mAfter runtime.MemStats
		runtime.ReadMemStats(&mBefore)

		t0 := time.Now()
		for i := range wl {
			wt := &wl[i]
			ctx, root, _ := tr.StartTask(context.Background(), wt.TaskID, wt.OrgID, wt.TenantID, "kai")
			if coalesced {
				_, vsp, _ := tr.StartVFSSpan(ctx, agenttrace.VFSSpanInput{
					Op: "read", Path: wt.Artifact.RelPath, Bytes: transferBytes,
					S3GetCount: 4, CoalescedOps: numChunks,
				})
				vsp.End()
			} else {
				for c := 0; c < numChunks; c++ {
					_, vsp, _ := tr.StartVFSSpan(ctx, agenttrace.VFSSpanInput{
						Op: "read", Path: wt.Artifact.RelPath, Bytes: chunkBytes,
						S3GetCount: 1, CoalescedOps: 1,
					})
					vsp.End()
				}
			}
			root.End()
		}
		_ = tr.Flush(context.Background())
		elapsed := time.Since(t0)
		runtime.ReadMemStats(&mAfter)
		_ = tr.Shutdown(context.Background())

		if coalesced {
			spansPerTask = 2 // root + 1 coalesced VFS span
		} else {
			spansPerTask = 1 + numChunks // root + 1,024 micro VFS spans
		}
		meanUs = float64(elapsed.Nanoseconds()) / 1000.0 / float64(tasks)
		otlpBytesTask = float64(sink.BytesWritten()) / float64(tasks)
		heapBytesTask = float64(mAfter.TotalAlloc-mBefore.TotalAlloc) / float64(tasks)
		return
	}

	cUs, cBytes, cHeap, cSpans := measure(true)
	mUs, mBytes, mHeap, mSpans := measure(false)

	return VFSMicroSpanExplodeResult{
		Tasks:                   tasks,
		TransferBytesPerTask:    transferBytes,
		ChunkSizeBytes:          chunkBytes,
		CoalescedSpansPerTask:   cSpans,
		MicroSpansPerTask:       mSpans,
		CoalescedMeanTaskUs:     round2(cUs),
		MicroSpanMeanTaskUs:     round2(mUs),
		CoalescedOTLPBytesTask:  round2(cBytes),
		MicroSpanOTLPBytesTask:  round2(mBytes),
		CoalescedHeapBytesTask:  round2(cHeap),
		MicroSpanHeapBytesTask:  round2(mHeap),
		SpanCardinalityRatioX:   round2(safeRatio(float64(mSpans), float64(cSpans))),
		OTLPByteExplosionRatioX: round2(safeRatio(mBytes, cBytes)),
		LatencyExplosionRatioX:  round2(safeRatio(mUs, cUs)),
	}, nil
}

func validateOTLPFile(path string) (valid, total int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		total++
		req, decErr := agenttrace.DecodeOTLPJSON([]byte(line))
		if decErr == nil && len(req.ResourceSpans) == 1 && len(req.ResourceSpans[0].ScopeSpans) == 1 && len(req.ResourceSpans[0].ScopeSpans[0].Spans) > 0 {
			valid++
		}
	}
	return valid, total, sc.Err()
}

func summarizeMicros(vals []float64) (mean, p50, p95, p99 float64) {
	if len(vals) == 0 {
		return 0, 0, 0, 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	var sum float64
	for _, v := range cp {
		sum += v
	}
	mean = sum / float64(len(cp))
	p50 = percentile(cp, 0.50)
	p95 = percentile(cp, 0.95)
	p99 = percentile(cp, 0.99)
	return
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func pctDelta(val, base float64) float64 {
	if base <= 0 {
		return 0
	}
	return 100.0 * (val - base) / base
}

func safeRatio(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return num / den
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func printSummary(rep BenchmarkReport) {
	fmt.Printf("=== HIP-1348 Unified Agent Execution Tracing Benchmark (%s) ===\n", rep.Issue)
	fmt.Printf("Host: %s/%s (%d CPUs, %s) | Seed: %d | Tasks/regime: %d | Corpus: %d files (%d bytes)\n\n",
		rep.Host.GOOS, rep.Host.GOARCH, rep.Host.NumCPU, rep.Host.GoVersion,
		rep.Seed, rep.TasksPerRegime, rep.Corpus.FileCount, rep.Corpus.TotalBytes)

	fmt.Printf("%-26s %9s %9s %9s %10s %10s %9s %8s %7s %7s\n",
		"Regime", "p50(us)", "p95(us)", "p99(us)", "ns/span", "allocs/sp", "6/6(%)", "bill(%)", "secLk", "xTenLk")
	for _, r := range rep.MatchedRegimes {
		fmt.Printf("%-26s %9.2f %9.2f %9.2f %10.1f %10.2f %9.2f %8.2f %7d %7d\n",
			r.Name, r.P50TaskUs, r.P95TaskUs, r.P99TaskUs, r.OverheadNsPerSpan,
			r.AllocsPerSpan, r.Completeness6Of6Pct, r.BillingReconciledPct, r.SecretLeaks, r.CrossTenantLeaks)
	}

	fmt.Printf("\nNegative Regime A (64 KiB Raw Prompt/Completion Capture):\n")
	nr := rep.NegativeRegimes.RawPayloadCapture
	fmt.Printf("  Redact: %.1f us/task, %.0f B/task OTLP, %d leaks | Hash: %.1f us/task, %.0f B/task, %d leaks | Raw: %.1f us/task, %.0f B/task (%.1fx bloat), %d leaks\n",
		nr.RedactMeanTaskUs, nr.RedactOTLPBytesPerTask, nr.RedactSecretLeaks,
		nr.HashMeanTaskUs, nr.HashOTLPBytesPerTask, nr.HashSecretLeaks,
		nr.RawMeanTaskUs, nr.RawOTLPBytesPerTask, nr.RawVsRedactByteBloatX, nr.RawSecretLeaks)

	fmt.Printf("Negative Regime B (Burst Overload: %d spans into %d-slot ring, %.0fms sink delay):\n",
		rep.NegativeRegimes.BurstOverload.BurstSpans, rep.NegativeRegimes.BurstOverload.RingCapacity, rep.NegativeRegimes.BurstOverload.SinkBatchDelayMs)
	nb := rep.NegativeRegimes.BurstOverload
	fmt.Printf("  DropNewest: p99=%.2f us, total=%.2f ms, dropped=%d (%.2f%%) | Block: p99=%.2f us (%.1fx slower), total=%.2f ms, dropped=%d\n",
		nb.DropModeP99EmitUs, nb.DropModeTotalEmitMs, nb.DropModeDroppedSpans, nb.DropModeDropRatePct,
		nb.BlockModeP99EmitUs, nb.BlockVsDropP99SlowdownX, nb.BlockModeTotalEmitMs, nb.BlockModeDroppedSpans)

	fmt.Printf("Negative Regime C (VFS 4 MiB Micro-Span Explosion):\n")
	nv := rep.NegativeRegimes.VFSMicroSpanExplode
	fmt.Printf("  Coalesced (%d spans/task): %.1f us/task, %.0f B/task | Micro 4KiB (%d spans/task): %.1f us/task (%.1fx), %.0f B/task (%.1fx OTLP bloat)\n",
		nv.CoalescedSpansPerTask, nv.CoalescedMeanTaskUs, nv.CoalescedOTLPBytesTask,
		nv.MicroSpansPerTask, nv.MicroSpanMeanTaskUs, nv.LatencyExplosionRatioX,
		nv.MicroSpanOTLPBytesTask, nv.OTLPByteExplosionRatioX)

	fmt.Printf("\nHypothesis Check: passed=%v (speedup vs naive p50=%.2fx, allocs reduction=%.2fx)\n",
		rep.HypothesisCheck.Passed, rep.HypothesisCheck.SpeedupVsNaiveP50X, rep.HypothesisCheck.AllocsReductionVsNaiveX)
}

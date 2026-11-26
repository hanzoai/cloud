// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestW3CTraceparentAndTracestateRoundTrip(t *testing.T) {
	gen := newIDGenerator(1348)
	tc := TaskContext{
		TaskID:    "task-hip1348-001",
		OrgID:     "org-alpha",
		TenantID:  "ws-prod",
		TraceID:   gen.nextTraceID(),
		SpanID:    gen.nextSpanID(),
		Flags:     TraceFlagSampled,
		Subsystem: SubsystemModel,
	}

	hdr := make(http.Header)
	InjectHeaders(hdr, tc)

	got, err := ExtractHeaders(hdr)
	if err != nil {
		t.Fatalf("ExtractHeaders failed: %v", err)
	}
	if got.TraceID != tc.TraceID || got.SpanID != tc.SpanID || got.Flags != tc.Flags {
		t.Fatalf("trace context mismatch: got %+v want %+v", got, tc)
	}
	if got.TaskID != tc.TaskID || got.OrgID != tc.OrgID || got.TenantID != tc.TenantID || got.Subsystem != tc.Subsystem {
		t.Fatalf("tracestate metadata mismatch: got %+v want %+v", got, tc)
	}

	// Reject malformed / invalid W3C headers fail-closed.
	badTraceparents := []string{
		"",
		"00-00000000000000000000000000000000-1111111111111111-01", // all-zero trace_id
		"00-11111111111111111111111111111111-0000000000000000-01", // all-zero span_id
		"ff-11111111111111111111111111111111-1111111111111111-01", // invalid version ff
		"01-11111111111111111111111111111111-1111111111111111-01", // unsupported version
		"00-1111111111111111111111111111111G-1111111111111111-01", // non-hex
		"00-1111111111111111111111111111111A-1111111111111111-01", // uppercase hex
		"00-1111-2222-01", // wrong length
	}
	for _, raw := range badTraceparents {
		if _, _, _, err := ParseTraceparent(raw); err == nil {
			t.Errorf("expected ParseTraceparent(%q) to fail", raw)
		}
	}

	// Reject control characters in tracestate.
	if _, _, _, _, err := ParseTracestate("hanzo=tid:abc\r\nX-Evil:1"); err == nil {
		t.Errorf("expected ParseTracestate with CRLF to fail")
	}

	// Reject conflicting X-Hanzo-Task-Id vs tracestate.
	hdr.Set(HeaderTaskID, "task-spoofed")
	if _, err := ExtractHeaders(hdr); err == nil {
		t.Errorf("expected ExtractHeaders to reject conflicting X-Hanzo-Task-Id")
	}
}

func emitCompleteSixSubsystemTask(t *testing.T, tr *Tracer, taskID, orgID, tenantID string) {
	t.Helper()
	ctx, rootSpan, err := tr.StartTask(context.Background(), taskID, orgID, tenantID, "kai-coder")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	meter := tr.NewTaskMeter()

	// 1. Sandbox startup (hanzoai/visor)
	sbIn := SandboxSpanInput{
		SandboxID:    "sbx-" + taskID,
		BootMs:       42.5,
		ImageRef:     "ghcr.io/hanzoai/visor-dev:v1.4@sha256:abcd1234",
		PolicyDigest: DigestString("seccomp-strict-v1"),
	}
	_, sbSpan, err := tr.StartSandboxSpan(ctx, sbIn)
	if err != nil {
		t.Fatalf("StartSandboxSpan: %v", err)
	}
	meter.RecordSandbox(sbIn)
	sbSpan.End()

	// Propagate over HTTP headers to simulate remote subsystem boundary (hanzoai/index).
	wireHdr := make(http.Header)
	InjectHeaders(wireHdr, rootSpan.Context())
	remoteTC, err := ExtractHeaders(wireHdr)
	if err != nil {
		t.Fatalf("ExtractHeaders across hop: %v", err)
	}
	remoteCtx := WithTaskContext(context.Background(), remoteTC)

	// 2. Retrieval (hanzoai/index)
	retIn := RetrievalSpanInput{
		Backend:  "index",
		Query:    "find OTel tracing middleware for user alice@example.com",
		TopK:     8,
		HitCount: 5,
		ACLScope: "org:" + orgID + "/repo:cloud",
	}
	_, retSpan, err := tr.StartRetrievalSpan(remoteCtx, retIn)
	if err != nil {
		t.Fatalf("StartRetrievalSpan: %v", err)
	}
	meter.RecordRetrieval(retIn)
	retSpan.End()

	// 3. Model call (Enso routing + Zen generation)
	modIn := ModelSpanInput{
		System:        SystemHanzoZen,
		EnsoRoute:     "enso://us-west/zen-72b-instruct",
		RequestModel:  "zen-72b-instruct",
		ResponseModel: "zen-72b-instruct-2026",
		FinishReason:  "tool_calls",
		InputTokens:   2400,
		OutputTokens:  380,
		CachedTokens:  1200,
		TTFTMs:        118.4,
		Prompt:        "System prompt with key sk-proj-secret1234567890abcdef and bob@corp.io",
		Completion:    "Call MCP search_code tool",
	}
	modCtx, modSpan, err := tr.StartModelSpan(ctx, modIn)
	if err != nil {
		t.Fatalf("StartModelSpan: %v", err)
	}
	meter.RecordModel(modIn)
	modSpan.End()

	// 4. MCP tool call nested under model decision (hanzoai/mcp)
	mcpIn := MCPSpanInput{
		Server:     "hanzo-code-mcp",
		ToolName:   "code_search",
		ToolCallID: "call_" + taskID + "_1",
		Status:     "ok",
		LatencyMs:  14.2,
	}
	mcpCtx, mcpSpan, err := tr.StartMCPSpan(modCtx, mcpIn)
	if err != nil {
		t.Fatalf("StartMCPSpan: %v", err)
	}
	meter.RecordMCP(mcpIn)
	mcpSpan.End()

	// 5. VFS operation nested under MCP tool (hanzoai/vfs -> hanzoai/s3)
	vfsIn := VFSSpanInput{
		Op:           "snapshot",
		Path:         "/workspace/cloud/apps/agenttrace",
		Bytes:        262144,
		S3PutCount:   2,
		S3GetCount:   4,
		CoalescedOps: 16,
	}
	_, vfsSpan, err := tr.StartVFSSpan(mcpCtx, vfsIn)
	if err != nil {
		t.Fatalf("StartVFSSpan: %v", err)
	}
	meter.RecordVFS(vfsIn)
	vfsSpan.End()

	// 6. Billing & Cloud metering reconciled 1:1 under the same task_id
	_, billSpan, err := tr.StartBillingSpan(ctx, meter.BillingInput("meter-"+taskID))
	if err != nil {
		t.Fatalf("StartBillingSpan: %v", err)
	}
	billSpan.End()

	rootSpan.End()
}

func TestUnifiedSixSubsystemTaskCompleteness(t *testing.T) {
	var otlpOut bytes.Buffer
	tr := NewTracer(TracerConfig{
		CaptureMode:       CaptureRedact,
		RingCapacity:      512,
		BatchSize:         32,
		FlushInterval:     10 * time.Millisecond,
		DeterministicSeed: 1348,
		EnableStore:       true,
		Sink:              NewWriterSink(&otlpOut, false),
	})
	defer func() { _ = tr.Shutdown(context.Background()) }()

	emitCompleteSixSubsystemTask(t, tr, "task-full-001", "org-hanzo", "org-hanzo")
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	spans, err := tr.Store().GetTask("org-hanzo", "org-hanzo", "task-full-001")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(spans) != 7 {
		t.Fatalf("expected 7 spans (1 root + 6 subsystems), got %d", len(spans))
	}

	rep := VerifyTaskCompleteness(spans, VerifyOptions{
		RateCard:         tr.RateCard(),
		ExpectedOrgID:    "org-hanzo",
		ExpectedTenantID: "org-hanzo",
	})
	if !rep.Complete {
		t.Fatalf("expected Complete=true, got violations=%v report=%+v", rep.Violations, rep)
	}
	if rep.SubsystemCoverage != 1.0 || len(rep.SubsystemsPresent) != 6 {
		t.Fatalf("expected 6/6 subsystem coverage, got %.2f (%v)", rep.SubsystemCoverage, rep.SubsystemsPresent)
	}
	if rep.OrphanSpanCount != 0 || rep.CycleDetected || rep.RootSpanCount != 1 {
		t.Fatalf("DAG invariant broken: roots=%d orphans=%d cycle=%v", rep.RootSpanCount, rep.OrphanSpanCount, rep.CycleDetected)
	}
	if !rep.BillingReconciled || rep.BillingDeltaMicroUSD != 0 || rep.BilledCostMicroUSD <= 0 {
		t.Fatalf("billing reconciliation failed: reconciled=%v delta=%d billed=%d expected=%d",
			rep.BillingReconciled, rep.BillingDeltaMicroUSD, rep.BilledCostMicroUSD, rep.ExpectedCostMicroUSD)
	}
	if rep.SecretLeakCount != 0 {
		t.Fatalf("expected 0 secret leaks, got %d", rep.SecretLeakCount)
	}
	if otlpOut.Len() == 0 {
		t.Fatalf("expected OTLP/JSON output written to WriterSink")
	}
}

func TestCompletenessDetectsOrphansCyclesMissingAndBillingMismatch(t *testing.T) {
	tr := NewTracer(TracerConfig{
		DeterministicSeed: 42,
		EnableStore:       true,
	})
	defer func() { _ = tr.Shutdown(context.Background()) }()

	emitCompleteSixSubsystemTask(t, tr, "task-base", "org-a", "org-a")
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	base, err := tr.Store().GetTask("org-a", "org-a", "task-base")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	t.Run("orphan_span", func(t *testing.T) {
		mut := cloneSpans(base)
		mut[2].ParentSpanID = SpanID{9, 9, 9, 9, 9, 9, 9, 9}
		rep := VerifyTaskCompleteness(mut, VerifyOptions{RateCard: tr.RateCard()})
		if rep.Complete || rep.OrphanSpanCount != 1 {
			t.Fatalf("expected 1 orphan span and Complete=false, got complete=%v orphans=%d", rep.Complete, rep.OrphanSpanCount)
		}
	})

	t.Run("cycle_in_dag", func(t *testing.T) {
		mut := cloneSpans(base)
		// Point two non-root spans at each other.
		mut[1].ParentSpanID = mut[2].SpanID
		mut[2].ParentSpanID = mut[1].SpanID
		rep := VerifyTaskCompleteness(mut, VerifyOptions{RateCard: tr.RateCard()})
		if rep.Complete || !rep.CycleDetected {
			t.Fatalf("expected CycleDetected=true and Complete=false, got %+v", rep)
		}
	})

	t.Run("missing_subsystem", func(t *testing.T) {
		mut := make([]Span, 0, len(base)-1)
		for _, sp := range base {
			if sp.Subsystem == SubsystemVFS {
				continue
			}
			mut = append(mut, sp.Clone())
		}
		rep := VerifyTaskCompleteness(mut, VerifyOptions{RateCard: tr.RateCard()})
		if rep.Complete || len(rep.MissingSubsystems) != 1 || rep.MissingSubsystems[0] != SubsystemVFS {
			t.Fatalf("expected missing VFS subsystem, got %+v", rep.MissingSubsystems)
		}
	})

	t.Run("billing_mismatch", func(t *testing.T) {
		mut := cloneSpans(base)
		for i := range mut {
			if mut[i].Subsystem == SubsystemBilling {
				for j := range mut[i].Attributes {
					if mut[i].Attributes[j].Key == AttrBillingCostMicroUSD {
						mut[i].Attributes[j].Int += 1
					}
				}
			}
		}
		rep := VerifyTaskCompleteness(mut, VerifyOptions{RateCard: tr.RateCard()})
		if rep.Complete || rep.BillingReconciled || rep.BillingDeltaMicroUSD != 1 {
			t.Fatalf("expected billing delta=1 mismatch, got reconciled=%v delta=%d", rep.BillingReconciled, rep.BillingDeltaMicroUSD)
		}
	})

	t.Run("cross_tenant_contamination", func(t *testing.T) {
		mut := cloneSpans(base)
		mut[1].OrgID = "org-evil"
		rep := VerifyTaskCompleteness(mut, VerifyOptions{RateCard: tr.RateCard()})
		if rep.Complete || rep.TenantConsistent {
			t.Fatalf("expected TenantConsistent=false on cross-tenant span")
		}
	})
}

func TestDLPRedactionAndContentCaptureModes(t *testing.T) {
	secrets := []string{
		"api key " + strings.Join([]string{"sk", "live", "abcdef1234567890xyz999"}, "-"),
		"hanzo key " + strings.Join([]string{"hz", "live", "99887766554433221100"}, "_"),
		"github " + strings.Join([]string{"ghp", "1234567890abcdefghij1234567890"}, "_"),
		"aws " + strings.Join([]string{"AKIA", "IOSFODNN7", "EXAMPLE"}, ""),
		"header Bearer " + strings.Join([]string{"eyJhbGci", "OiJIUzI1NiJ9", ".", "eyJzdWIi", "OiJ1c2VyMSJ9", ".", "signaturepart12345"}, ""),
		"contact security@hanzo.ai or 123-45-6789",
		strings.Join([]string{"-----BEGIN ", "RSA ", "PRIVATE KEY-----\n", "MIIEpAIB", "AAKCAQEA\n", "-----END ", "RSA ", "PRIVATE KEY-----"}, ""),
	}
	for _, s := range secrets {
		if !ContainsSecret(s) {
			t.Errorf("ContainsSecret failed to detect: %q", s)
		}
		scrubbed, red := ScrubSecrets(s)
		if !red || ContainsSecret(scrubbed) {
			t.Errorf("ScrubSecrets failed on %q -> %q (red=%v)", s, scrubbed, red)
		}
	}

	// Compare CaptureRedact vs CaptureHash vs CaptureRaw.
	prompt := "Summarize invoice for alice@example.com using " + strings.Join([]string{"sk", "test", "1234567890abcdefgh"}, "-")
	for _, mode := range []ContentCaptureMode{CaptureRedact, CaptureHash, CaptureRaw} {
		tr := NewTracer(TracerConfig{
			CaptureMode:       mode,
			DeterministicSeed: 7,
			EnableStore:       true,
		})
		ctx, root, err := tr.StartTask(context.Background(), "task-dlp-"+string(mode), "org-sec", "org-sec", "kai")
		if err != nil {
			t.Fatal(err)
		}
		_, msp, err := tr.StartModelSpan(ctx, ModelSpanInput{
			RequestModel: "zen-72b",
			InputTokens:  100,
			OutputTokens: 20,
			Prompt:       prompt,
			Completion:   "OK",
			ExtraAttributes: []Attr{
				StringAttr("http.header.authorization", "Bearer topsecretvalue1234567890"),
				StringAttr("custom.note", "user bob@example.com"),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msp.EndWithError(errors.New("upstream error with sk-proj-99999999999999999999"))
		root.End()
		_ = tr.Flush(context.Background())

		spans, err := tr.Store().GetTask("org-sec", "org-sec", "task-dlp-"+string(mode))
		if err != nil {
			t.Fatal(err)
		}
		var modelSp *Span
		for i := range spans {
			if spans[i].Subsystem == SubsystemModel {
				modelSp = &spans[i]
				break
			}
		}
		if modelSp == nil {
			t.Fatalf("missing model span for mode %s", mode)
		}
		gotPrompt, _ := modelSp.StrAttr(AttrGenAIPrompt)
		gotAuth, _ := modelSp.StrAttr("http.header.authorization")
		if gotAuth != redactedPlaceholder {
			t.Errorf("mode=%s: sensitive key authorization MUST always be redacted, got %q", mode, gotAuth)
		}
		switch mode {
		case CaptureRedact:
			if gotPrompt != redactedPlaceholder {
				t.Errorf("CaptureRedact prompt=%q want %q", gotPrompt, redactedPlaceholder)
			}
			if ContainsSecret(modelSp.StatusMessage) {
				t.Errorf("CaptureRedact leaked secret in StatusMessage: %q", modelSp.StatusMessage)
			}
		case CaptureHash:
			if !strings.HasPrefix(gotPrompt, "sha256:") || !strings.Contains(gotPrompt, ";bytes=") {
				t.Errorf("CaptureHash prompt=%q want sha256:<hex>;bytes=<n>", gotPrompt)
			}
			if ContainsSecret(gotPrompt) {
				t.Errorf("CaptureHash leaked secret in prompt")
			}
		case CaptureRaw:
			if gotPrompt != prompt {
				t.Errorf("CaptureRaw should retain prompt verbatim for opt-in benchmark/debug")
			}
		}
		_ = tr.Shutdown(context.Background())
	}
}

func TestMultiTenantIsolationFailClosed(t *testing.T) {
	store := NewTraceStore(TraceStoreConfig{MaxTasksPerOrg: 16})
	gen := newIDGenerator(99)
	tid := gen.nextTraceID()
	sid := gen.nextSpanID()

	spA := Span{
		TaskID: "task-shared-id", OrgID: "org-alpha", TenantID: "ws-1",
		Subsystem: SubsystemRoot, TraceID: tid, SpanID: sid,
		Name: "agent.task", StartTimeNano: 100, EndTimeNano: 200,
	}
	if err := store.Ingest(spA); err != nil {
		t.Fatalf("Ingest org-alpha: %v", err)
	}

	// Cross-org read denied fail-closed.
	if _, err := store.GetTask("org-beta", "org-beta", "task-shared-id"); !errors.Is(err, ErrCrossTenantDenied) {
		t.Fatalf("expected ErrCrossTenantDenied for cross-org read, got %v", err)
	}
	// Cross-sub-tenant read inside same org denied fail-closed.
	if _, err := store.GetTask("org-alpha", "ws-2", "task-shared-id"); !errors.Is(err, ErrCrossTenantDenied) {
		t.Fatalf("expected ErrCrossTenantDenied for cross-tenant read, got %v", err)
	}
	// Empty org denied fail-closed.
	if _, err := store.GetTask("", "", "task-shared-id"); !errors.Is(err, ErrEmptyTenantScope) {
		t.Fatalf("expected ErrEmptyTenantScope for empty org, got %v", err)
	}
	// Cross-org span injection into org-alpha's task rejected fail-closed.
	spEvil := spA.Clone()
	spEvil.OrgID = "org-beta"
	spEvil.TenantID = "org-beta"
	spEvil.SpanID = gen.nextSpanID()
	if err := store.Ingest(spEvil); !errors.Is(err, ErrCrossTenantContam) {
		t.Fatalf("expected ErrCrossTenantContam on cross-org injection, got %v", err)
	}
	// Listing tasks for org-beta reveals 0 tasks.
	listB, err := store.ListTaskIDs("org-beta", "org-beta")
	if err != nil || len(listB) != 0 {
		t.Fatalf("expected empty list for org-beta, got %v (err=%v)", listB, err)
	}
	// Verify counters recorded the blocked attempts.
	st := store.Stats()
	if st.CrossTenantDenials < 2 || st.CrossTenantRejections < 1 {
		t.Fatalf("expected cross-tenant telemetry counters > 0, got %+v", st)
	}
}

func TestBoundedRingBufferNonBlockingDropAndBlockModes(t *testing.T) {
	// 1. Non-blocking DropNewest under lagging exporter: never stalls, records explicit drops.
	slowDrop := NewTracer(TracerConfig{
		RingCapacity:     32,
		BatchSize:        16,
		FlushInterval:    50 * time.Millisecond,
		BackpressureMode: BackpressureDropNewest,
		Sink:             &SlowSink{Delay: 20 * time.Millisecond},
	})
	start := time.Now()
	const burst = 500
	for i := 0; i < burst; i++ {
		_, sp, err := slowDrop.StartTask(context.Background(), fmt.Sprintf("drop-%d", i), "org-load", "org-load", "kai")
		if err != nil {
			t.Fatal(err)
		}
		sp.End()
	}
	enqueueElapsed := time.Since(start)
	_ = slowDrop.Shutdown(context.Background())

	st := slowDrop.Stats()
	if st.Ring.DroppedSpans == 0 {
		t.Fatalf("expected non-zero DroppedSpans under burst > capacity, got %+v", st)
	}
	if st.Ring.EnqueuedSpans+st.Ring.DroppedSpans != burst {
		t.Fatalf("accounting mismatch: enqueued(%d) + dropped(%d) != %d", st.Ring.EnqueuedSpans, st.Ring.DroppedSpans, burst)
	}
	if enqueueElapsed > 100*time.Millisecond {
		t.Fatalf("non-blocking DropNewest took too long (%v), appears to have blocked", enqueueElapsed)
	}

	// 2. Blocking mode: 0 drops, all spans exported once drained.
	slowBlock := NewTracer(TracerConfig{
		RingCapacity:     16,
		BatchSize:        8,
		FlushInterval:    5 * time.Millisecond,
		BackpressureMode: BackpressureBlock,
		Sink:             &SlowSink{Delay: 1 * time.Millisecond},
	})
	for i := 0; i < 64; i++ {
		_, sp, err := slowBlock.StartTask(context.Background(), fmt.Sprintf("blk-%d", i), "org-load", "org-load", "kai")
		if err != nil {
			t.Fatal(err)
		}
		sp.End()
	}
	_ = slowBlock.Shutdown(context.Background())
	bst := slowBlock.Stats()
	if bst.Ring.DroppedSpans != 0 || bst.ExportedSpans != 64 {
		t.Fatalf("expected 0 drops and 64 exported in Block mode, got %+v", bst)
	}
}

func TestConcurrentWritersRaceClean(t *testing.T) {
	tr := NewTracer(TracerConfig{
		RingCapacity:  8192,
		BatchSize:     128,
		FlushInterval: 5 * time.Millisecond,
		EnableStore:   true,
	})
	defer func() { _ = tr.Shutdown(context.Background()) }()

	const workers = 12
	const tasksPerWorker = 15
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			org := fmt.Sprintf("org-%d", wid%4)
			for i := 0; i < tasksPerWorker; i++ {
				taskID := fmt.Sprintf("conc-w%d-t%d", wid, i)
				emitCompleteSixSubsystemTask(t, tr, taskID, org, org)
			}
		}(w)
	}
	wg.Wait()

	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	st := tr.Stats()
	wantSpans := uint64(workers * tasksPerWorker * 7)
	if st.Ring.DroppedSpans != 0 || st.ExportedSpans != wantSpans {
		t.Fatalf("expected %d exported spans and 0 drops, got %+v", wantSpans, st)
	}

	for w := 0; w < workers; w++ {
		org := fmt.Sprintf("org-%d", w%4)
		otherOrg := fmt.Sprintf("org-%d", (w+1)%4)
		for i := 0; i < tasksPerWorker; i++ {
			taskID := fmt.Sprintf("conc-w%d-t%d", w, i)
			spans, err := tr.Store().GetTask(org, org, taskID)
			if err != nil {
				t.Fatalf("GetTask(%s, %s): %v", org, taskID, err)
			}
			rep := VerifyTaskCompleteness(spans, VerifyOptions{RateCard: tr.RateCard(), ExpectedOrgID: org})
			if !rep.Complete {
				t.Fatalf("task %s incomplete: %v", taskID, rep.Violations)
			}
			if _, err := tr.Store().GetTask(otherOrg, otherOrg, taskID); !errors.Is(err, ErrCrossTenantDenied) {
				t.Fatalf("cross-tenant leak for %s accessed by %s: err=%v", taskID, otherOrg, err)
			}
		}
	}
}

func TestOTLPJSONSchemaValidation(t *testing.T) {
	tr := NewTracer(TracerConfig{
		DeterministicSeed: 1348,
		EnableStore:       true,
	})
	defer func() { _ = tr.Shutdown(context.Background()) }()

	emitCompleteSixSubsystemTask(t, tr, "task-otlp-01", "org-otlp", "org-otlp")
	// Add a span with special JSON characters to verify RFC 8259 escaping.
	ctx, root, _ := tr.StartTask(context.Background(), "task-escape", "org-otlp", "org-otlp", "kai \"quoted\"\n<tag>&\\")
	_, msp, _ := tr.StartMCPSpan(ctx, MCPSpanInput{
		Server: "srv", ToolName: "tool\"A\"", ToolCallID: "c1", Status: "ok", LatencyMs: 3.5,
		ExtraAttributes: []Attr{BoolAttr("hanzo.test.flag", true)},
	})
	msp.End()
	root.End()

	if err := tr.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans, err := tr.Store().GetTask("org-otlp", "org-otlp", "task-otlp-01")
	if err != nil {
		t.Fatal(err)
	}
	escSpans, err := tr.Store().GetTask("org-otlp", "org-otlp", "task-escape")
	if err != nil {
		t.Fatal(err)
	}
	all := append(spans, escSpans...)

	raw := EncodeOTLPJSON(all, "hanzo-cloud-test")
	if !json.Valid(raw) {
		t.Fatalf("EncodeOTLPJSON produced invalid JSON: %s", string(raw))
	}
	decoded, err := DecodeOTLPJSON(raw)
	if err != nil {
		t.Fatalf("DecodeOTLPJSON failed: %v", err)
	}
	if len(decoded.ResourceSpans) != 1 || len(decoded.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatalf("unexpected OTLP envelope structure: %+v", decoded)
	}
	scope := decoded.ResourceSpans[0].ScopeSpans[0].Scope
	if scope.Name != ScopeName || scope.Version != ScopeVersion {
		t.Fatalf("unexpected scope: %+v", scope)
	}
	otlpSpans := decoded.ResourceSpans[0].ScopeSpans[0].Spans
	if len(otlpSpans) != len(all) {
		t.Fatalf("span count mismatch: decoded %d want %d", len(otlpSpans), len(all))
	}
	for _, s := range otlpSpans {
		if len(s.TraceID) != 32 || len(s.SpanID) != 16 {
			t.Errorf("invalid OTLP hex ids: trace=%q span=%q", s.TraceID, s.SpanID)
		}
		if s.StartTimeUnixNano == "" || s.EndTimeUnixNano == "" {
			t.Errorf("missing OTLP unix nano timestamps on span %q", s.Name)
		}
	}
}

func TestHTTPMountIsolationAndOperatorGuard(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})

	svc, err := NewService(Deps{
		Log: luxlog.New("test"),
		Org: func(ctx context.Context) (string, bool) {
			c := zip.CallerOf(ctx)
			if c.User == "" || c.Org == "" {
				return "", false
			}
			return c.Org, true
		},
		Acting: func(ctx context.Context) (string, error) {
			c := zip.CallerOf(ctx)
			if c.User == "" {
				return "", zip.ErrUnauthorized("authentication required")
			}
			if c.Org == "" {
				return "", zip.ErrForbidden("X-Org-Id required")
			}
			return c.Org, nil
		},
		// Leave Operator nil to exercise NewService's built-in fail-closed SuperAdmin guard.
		Config: TracerConfig{DeterministicSeed: 1348},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer func() { _ = svc.Close(context.Background()) }()
	MountService(app, svc)

	doReq := func(method, path, org string, admin bool, body any) *http.Response {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		if org != "" {
			req.Header.Set("X-Org-Id", org)
			req.Header.Set("X-User-Id", "u_"+org)
		}
		if admin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s failed: %v", method, path, err)
		}
		return resp
	}

	// 1. Health is reachable unauthenticated.
	resp := doReq(http.MethodGet, "/v1/agenttrace/health", "", false, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. Seed a complete 6-subsystem trace directly into org-alpha via Tracer.
	emitCompleteSixSubsystemTask(t, svc.Tracer(), "task-http-01", "org-alpha", "org-alpha")
	_ = svc.Tracer().Flush(context.Background())

	// 3. org-alpha can read, verify, and OTLP-export its task.
	for _, ep := range []string{
		"/v1/agenttrace/tasks",
		"/v1/agenttrace/tasks/task-http-01",
		"/v1/agenttrace/tasks/task-http-01/verify",
		"/v1/agenttrace/tasks/task-http-01/otlp",
	} {
		r := doReq(http.MethodGet, ep, "org-alpha", false, nil)
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("GET %s as org-alpha: status=%d body=%s", ep, r.StatusCode, string(b))
		}
		if strings.HasSuffix(ep, "/verify") {
			var rep CompletenessReport
			if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
				t.Fatalf("decode verify report: %v", err)
			}
			if !rep.Complete {
				t.Fatalf("expected Complete=true from HTTP /verify, got %+v", rep)
			}
		}
		_ = r.Body.Close()
	}

	// 4. org-beta is denied fail-closed (403) on org-alpha's task.
	for _, ep := range []string{
		"/v1/agenttrace/tasks/task-http-01",
		"/v1/agenttrace/tasks/task-http-01/verify",
		"/v1/agenttrace/tasks/task-http-01/otlp",
	} {
		r := doReq(http.MethodGet, ep, "org-beta", false, nil)
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("GET %s as org-beta: status=%d want 403", ep, r.StatusCode)
		}
		_ = r.Body.Close()
	}

	// 5. POST /v1/agenttrace/spans enforces DLP redaction and rejects cross-tenant injection.
	gen := newIDGenerator(77)
	ingResp := doReq(http.MethodPost, "/v1/agenttrace/spans", "org-beta", false, ingestSpansIn{
		Flush: true,
		Spans: []spanIngestItem{{
			TaskID:        "task-http-01", // owned by org-alpha!
			Subsystem:     SubsystemMCP,
			TraceID:       gen.nextTraceID().String(),
			SpanID:        gen.nextSpanID().String(),
			Name:          "mcp.inject",
			Kind:          SpanKindClient,
			StartTimeNano: 1000,
			EndTimeNano:   2000,
			Status:        StatusOK,
		}},
	})
	if ingResp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-tenant POST /spans status=%d want 403", ingResp.StatusCode)
	}
	_ = ingResp.Body.Close()

	// Valid ingest for org-beta with secret in attribute gets redacted.
	okIng := doReq(http.MethodPost, "/v1/agenttrace/spans?flush=false", "org-beta", false, ingestSpansIn{
		Flush: true,
		Spans: []spanIngestItem{{
			TaskID:        "task-beta-01",
			Subsystem:     SubsystemRoot,
			TraceID:       gen.nextTraceID().String(),
			SpanID:        gen.nextSpanID().String(),
			Name:          "agent.task",
			Kind:          SpanKindServer,
			StartTimeNano: 1000,
			EndTimeNano:   2000,
			Status:        StatusOK,
			Attributes: []Attr{
				StringAttr(AttrGenAIPrompt, "secret prompt "+strings.Join([]string{"sk", "live", "1234567890abcdefgh"}, "-")),
				StringAttr(AttrHanzoOrgID, "org-alpha"), // spoof attempt must be overwritten with org-beta!
			},
		}},
	})
	if okIng.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(okIng.Body)
		t.Fatalf("POST /spans status=%d want 202 body=%s", okIng.StatusCode, string(b))
	}
	var out ingestSpansOut
	_ = json.NewDecoder(okIng.Body).Decode(&out)
	_ = okIng.Body.Close()
	if out.Accepted != 1 || out.RedactedAttrs == 0 {
		t.Fatalf("expected accepted=1 redacted>0, got %+v", out)
	}
	// Verify stored span belongs to org-beta (not spoofed org-alpha) and prompt is redacted.
	betaSpans, err := svc.Store().GetTask("org-beta", "org-beta", "task-beta-01")
	if err != nil || len(betaSpans) != 1 {
		t.Fatalf("expected stored span under org-beta, got len=%d err=%v", len(betaSpans), err)
	}
	if p, _ := betaSpans[0].StrAttr(AttrGenAIPrompt); p != redactedPlaceholder {
		t.Fatalf("expected redacted prompt over HTTP ingest, got %q", p)
	}

	// 6. Diagnostics endpoint: non-admin 403 fail-closed, operator admin 200.
	diagNonAdmin := doReq(http.MethodGet, "/v1/agenttrace/diagnostics", "org-alpha", false, nil)
	if diagNonAdmin.StatusCode != http.StatusForbidden {
		t.Fatalf("diagnostics as non-admin status=%d want 403", diagNonAdmin.StatusCode)
	}
	_ = diagNonAdmin.Body.Close()

	diagAdmin := doReq(http.MethodGet, "/v1/agenttrace/diagnostics", "admin", true, nil)
	if diagAdmin.StatusCode != http.StatusOK {
		t.Fatalf("diagnostics as operator admin status=%d want 200", diagAdmin.StatusCode)
	}
	_ = diagAdmin.Body.Close()
}

func cloneSpans(in []Span) []Span {
	out := make([]Span, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func BenchmarkEmitSpanBatched(b *testing.B) {
	tr := NewTracer(TracerConfig{
		CaptureMode:       CaptureRedact,
		RingCapacity:      16384,
		BatchSize:         512,
		FlushInterval:     100 * time.Millisecond,
		DeterministicSeed: 1348,
		Sink:              NewWriterSink(io.Discard, false),
	})
	defer func() { _ = tr.Shutdown(context.Background()) }()

	ctx, root, _ := tr.StartTask(context.Background(), "bench-task", "org-bench", "org-bench", "kai")
	defer root.End()

	in := ModelSpanInput{
		RequestModel: "zen-72b",
		InputTokens:  1024,
		OutputTokens: 256,
		CachedTokens: 512,
		TTFTMs:       95.0,
		Prompt:       "Inspect architecture invariants",
		Completion:   "Verified",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, sp, _ := tr.StartModelSpan(ctx, in)
		sp.End()
	}
	b.StopTimer()
	_ = tr.Flush(context.Background())
}

func BenchmarkEncodeOTLPJSON(b *testing.B) {
	tr := NewTracer(TracerConfig{
		DeterministicSeed: 1348,
		EnableStore:       true,
	})
	defer func() { _ = tr.Shutdown(context.Background()) }()

	ctx, root, _ := tr.StartTask(context.Background(), "bench-otlp", "org-bench", "org-bench", "kai")
	_, sp, _ := tr.StartModelSpan(ctx, ModelSpanInput{
		RequestModel: "zen-72b", InputTokens: 500, OutputTokens: 120, CachedTokens: 200, TTFTMs: 80,
	})
	sp.End()
	root.End()
	_ = tr.Flush(context.Background())
	spans, _ := tr.Store().GetTask("org-bench", "org-bench", "bench-otlp")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeOTLPJSON(spans, ServiceName)
	}
}

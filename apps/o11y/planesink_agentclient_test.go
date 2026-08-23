package o11y

// The seam between what an agent RUN emits and what this package files it under.
//
// planeOrg reads one key — hanzo.org — per span, and falls back to the platform's
// own org when it is absent. The agent spans used to name their tenant
// hanzo.agent.org, a spelling nothing here reads, so every run/step/tool span was
// stored as PLATFORM telemetry: absent from the org-scoped read the console
// issues, and sitting in the platform's bucket with that tenant's tool names and
// user subjects in it. Both halves looked correct in isolation, which is exactly
// why the seam needs a test of its own.
//
// The other half lives in apps/agents (TestEverySpanOfARunIsFiledUnderItsTenant),
// which asserts every span a run produces carries this key. Together they are the
// contract; either alone is the bug that shipped.

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestAgentRunSpansAreFiledUnderTheirTenant renders the exact attribute set an
// agent run emits and asserts each span lands on the tenant's rows — never the
// platform's.
func TestAgentRunSpansAreFiledUnderTheirTenant(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	tr := tp.Tracer("hanzo.ai/cloud/agents")

	// The three spans a run is, each with the attributes apps/agents sets on it.
	ctx, run := tr.Start(context.Background(), "agent.run helper", trace.WithSpanKind(trace.SpanKindInternal))
	run.SetAttributes(
		attribute.String("hanzo.agent.name", "helper"),
		attribute.String("hanzo.org", "acme"),
		attribute.String("hanzo.agent.run_id", "run_123"),
		attribute.String("hanzo.user", "u-acme"),
	)
	ctx, step := tr.Start(ctx, "agent.step", trace.WithSpanKind(trace.SpanKindInternal))
	step.SetAttributes(
		attribute.String("hanzo.agent.run_id", "run_123"),
		attribute.String("hanzo.org", "acme"),
	)
	_, tool := tr.Start(ctx, "agent.tool post_search_query", trace.WithSpanKind(trace.SpanKindInternal))
	tool.SetAttributes(
		attribute.String("gen_ai.tool.name", "post_search_query"),
		attribute.String("hanzo.org", "acme"),
		attribute.String("hanzo.agent.run_id", "run_123"),
	)
	tool.End()
	step.End()
	run.End()

	rows := sdkSpanRowsOf(sr.Ended())
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (run, step, tool)", len(rows))
	}
	for _, row := range rows {
		name := spanCol(t, row, "name")
		if got := spanCol(t, row, "org"); got != "acme" {
			t.Fatalf("%v is filed under %v, want acme — a run's span stored as %q is "+
				"invisible to the tenant that ran it", name, got, platformOrg)
		}
	}

	// The trace SUMMARY is keyed (org, trace_id), so a run whose spans disagreed
	// about their tenant would be split into two partials — the trace list would
	// then report a fraction of the spans and the wrong duration for each half.
	// One tenant, one summary row.
	partials := traceRowsOf(rows)
	if len(partials) != 1 {
		t.Fatalf("got %d trace summary rows, want 1 — the run's spans disagree about their tenant", len(partials))
	}
	if got := partials[0][0]; got != "acme" {
		t.Fatalf("the trace summary is filed under %v, want acme", got)
	}
	if got := partials[0][4]; got != uint64(3) {
		t.Fatalf("the summary counts %v spans, want 3", got)
	}
}

// TestAnAgentSpanMissingItsTenantIsThePlatforms is the negative that names the
// failure: this is precisely what every run looked like before the attribute was
// corrected, and it is what a future rename would silently restore.
func TestAnAgentSpanMissingItsTenantIsThePlatforms(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	_, span := tp.Tracer("hanzo.ai/cloud/agents").Start(context.Background(), "agent.run helper")
	// The old spelling: a tenant named in a key nothing reads.
	span.SetAttributes(attribute.String("hanzo.agent.org", "acme"))
	span.End()

	rows := sdkSpanRowsOf(sr.Ended())
	if got := spanCol(t, rows[0], "org"); got != platformOrg {
		t.Fatalf("org = %v; a span naming its tenant under an unread key must fall "+
			"back to %q — if this now resolves the tenant, planeOrg reads more than one key",
			got, platformOrg)
	}
}

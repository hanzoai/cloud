// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// planesink_test.go — the pure row builders planesink.go promises are "unit-tested
// without a store or a socket". Every function here is total on its input: a wire
// batch (span or log) or an SDK span in, an event.span / event.log row out, with no
// datastore and no listener. The column-count guard pins the row shape to the column
// list so a builder and its schema can never drift silently — the exact failure the
// admin board's plane migration was built to end (a query that names a column the
// table lacks returns honest-looking nothing forever).

package o11y

import (
	"context"
	"strings"
	"testing"
	"time"

	zaplogreceiver "github.com/hanzoai/o11y/pkg/zaplogreceiver"
	zapreceiver "github.com/hanzoai/o11y/pkg/zapreceiver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// colIndex resolves a column name to its position in the fixed column list, so the
// tests read a row by NAME (row[col(planeSpanColumns,"status")]) and stay legible if
// the order ever changes — the guard test below is what forbids that change.
func colIndex(t *testing.T, cols []string, name string) int {
	t.Helper()
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	t.Fatalf("column %q not in %v", name, cols)
	return -1
}

func spanCol(t *testing.T, row []any, name string) any {
	return row[colIndex(t, planeSpanColumns, name)]
}
func logCol(t *testing.T, row []any, name string) any { return row[colIndex(t, planeLogColumns, name)] }

// The row a builder appends MUST be exactly as wide as the column list it is inserted
// with — a positional Insert silently misaligns otherwise.
func TestPlaneColumns_CountsAreFixed(t *testing.T) {
	if len(planeSpanColumns) != 12 {
		t.Errorf("planeSpanColumns = %d cols, want 12: %v", len(planeSpanColumns), planeSpanColumns)
	}
	if len(planeLogColumns) != 12 {
		t.Errorf("planeLogColumns = %d cols, want 12: %v", len(planeLogColumns), planeLogColumns)
	}
}

func TestSpanRowsOf_FullSpan(t *testing.T) {
	const startNs, endNs = int64(1_700_000_000_000_000_000), int64(1_700_000_000_012_000_000)
	b := &zapreceiver.SpanBatch{
		AppName:  "eval",
		Version:  "v1.2.3",
		Resource: map[string]string{"service.name": "evalsvc", "deployment.environment": "prod"},
		Spans: []zapreceiver.Span{{
			TraceID: "trace-1", SpanID: "span-1", ParentSpanID: "parent-1",
			Name: "GET /v1/eval", Kind: "server",
			StartUnixNs: startNs, EndUnixNs: endNs,
			Attributes: map[string]any{
				"hanzo.org":                 "acme",
				"http.route":                "/v1/eval",
				"http.response.status_code": float64(502), // JSON numbers decode to float64
			},
			StatusCode: "error", StatusMsg: "upstream 502",
		}},
	}
	rows := spanRowsOf(b)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if len(row) != len(planeSpanColumns) {
		t.Fatalf("row width %d != column count %d", len(row), len(planeSpanColumns))
	}
	if got := spanCol(t, row, "org"); got != "acme" {
		t.Errorf("org = %v, want acme (from hanzo.org attribute)", got)
	}
	if got := spanCol(t, row, "id"); got != "span-1" {
		t.Errorf("id = %v, want span-1 (the span id IS the row id)", got)
	}
	if got := spanCol(t, row, "span_id"); got != "span-1" {
		t.Errorf("span_id = %v, want span-1", got)
	}
	if got := spanCol(t, row, "trace_id"); got != "trace-1" {
		t.Errorf("trace_id = %v, want trace-1", got)
	}
	if got := spanCol(t, row, "parent"); got != "parent-1" {
		t.Errorf("parent = %v, want parent-1", got)
	}
	if got := spanCol(t, row, "name"); got != "GET /v1/eval" {
		t.Errorf("name = %v", got)
	}
	if got := spanCol(t, row, "kind"); got != "server" {
		t.Errorf("kind = %v, want server", got)
	}
	if got := spanCol(t, row, "service"); got != "evalsvc" {
		t.Errorf("service = %v, want evalsvc", got)
	}
	if got := spanCol(t, row, "status"); got != "error" {
		t.Errorf("status = %v, want error", got)
	}
	if got, ok := spanCol(t, row, "duration").(uint64); !ok || got != uint64(endNs-startNs) {
		t.Errorf("duration = %v (%T), want %d ns (uint64)", got, spanCol(t, row, "duration"), endNs-startNs)
	}
	if got, ok := spanCol(t, row, "time").(time.Time); !ok || !got.Equal(time.Unix(0, startNs).UTC()) {
		t.Errorf("time = %v, want %v", got, time.Unix(0, startNs).UTC())
	}
	attrs, ok := spanCol(t, row, "attributes").(map[string]string)
	if !ok {
		t.Fatalf("attributes col is %T, want map[string]string", spanCol(t, row, "attributes"))
	}
	// Resource, service.version (from batch Version), span attrs and the status
	// message all fold into the one attribute map.
	for k, want := range map[string]string{
		"service.name":              "evalsvc",
		"deployment.environment":    "prod",
		"service.version":           "v1.2.3",
		"http.route":                "/v1/eval",
		"http.response.status_code": "502", // float64(502) renders as the integer, never "502.0"
		"status.message":            "upstream 502",
		"hanzo.org":                 "acme",
	} {
		if attrs[k] != want {
			t.Errorf("attrs[%q] = %q, want %q", k, attrs[k], want)
		}
	}
}

// A span that names no tenant belongs to the platform, its kind defaults to
// internal, and a completed span with no failure declared is ok.
func TestSpanRowsOf_PlatformDefaults(t *testing.T) {
	rows := spanRowsOf(&zapreceiver.SpanBatch{
		AppName: "cloud",
		Spans:   []zapreceiver.Span{{SpanID: "s", Name: "job", StartUnixNs: 5, EndUnixNs: 4 /* end<=start */}},
	})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if got := spanCol(t, row, "org"); got != platformOrg {
		t.Errorf("org = %v, want %q (platform default)", got, platformOrg)
	}
	if got := spanCol(t, row, "kind"); got != "internal" {
		t.Errorf("kind = %v, want internal", got)
	}
	if got := spanCol(t, row, "status"); got != "ok" {
		t.Errorf("status = %v, want ok", got)
	}
	if got := spanCol(t, row, "service"); got != "cloud" {
		t.Errorf("service = %v, want cloud (AppName fallback)", got)
	}
	if got, ok := spanCol(t, row, "duration").(uint64); !ok || got != 0 {
		t.Errorf("duration = %v, want 0 when end<=start", got)
	}
}

func TestSpanRowsOf_EmptyAndNil(t *testing.T) {
	if rows := spanRowsOf(nil); rows != nil {
		t.Errorf("nil batch -> %v, want nil", rows)
	}
	if rows := spanRowsOf(&zapreceiver.SpanBatch{}); rows != nil {
		t.Errorf("no-span batch -> %v, want nil", rows)
	}
}

func TestLogRowsOf_FullRecord(t *testing.T) {
	const ns = int64(1_700_000_000_500_000_000)
	b := &zaplogreceiver.LogBatch{
		AppName:  "gateway",
		Resource: map[string]string{"service.name": "gatewaysvc", "app": "gateway"},
		Records: []zaplogreceiver.LogRecord{{
			TimeUnixNs: ns, Severity: 9, SeverityText: "INFO",
			Body: "served", TraceID: "t1", SpanID: "s1",
			Attributes: map[string]any{"code": float64(200)},
		}},
	}
	rows := logRowsOf(b)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if len(row) != len(planeLogColumns) {
		t.Fatalf("row width %d != column count %d", len(row), len(planeLogColumns))
	}
	if got := logCol(t, row, "kind"); got != "log" {
		t.Errorf("kind = %v, want log", got)
	}
	if got := logCol(t, row, "name"); got != "log" {
		t.Errorf("name = %v, want log (no EventName)", got)
	}
	if got := logCol(t, row, "service"); got != "gatewaysvc" {
		t.Errorf("service = %v, want gatewaysvc", got)
	}
	if got := logCol(t, row, "severity_text"); got != "INFO" {
		t.Errorf("severity_text = %v, want INFO", got)
	}
	if got, ok := logCol(t, row, "severity_number").(uint8); !ok || got != 9 {
		t.Errorf("severity_number = %v (%T), want uint8(9)", got, logCol(t, row, "severity_number"))
	}
	if got := logCol(t, row, "body"); got != "served" {
		t.Errorf("body = %v, want served", got)
	}
	if got := logCol(t, row, "trace_id"); got != "t1" {
		t.Errorf("trace_id = %v, want t1", got)
	}
	if got, ok := logCol(t, row, "time").(time.Time); !ok || !got.Equal(time.Unix(0, ns).UTC()) {
		t.Errorf("time = %v, want %v", got, time.Unix(0, ns).UTC())
	}
	if got := logCol(t, row, "org"); got != platformOrg {
		t.Errorf("org = %v, want %q (a fleet log line is the platform's)", got, platformOrg)
	}
	if attrs, ok := logCol(t, row, "attributes").(map[string]string); !ok || attrs["code"] != "200" {
		t.Errorf("attributes = %v, want code=200", logCol(t, row, "attributes"))
	}
}

// TimeUnixNs==0 falls back to the observed time; EventName names the row; severity
// above the byte range clamps rather than wraps.
func TestLogRowsOf_FallbacksAndClamp(t *testing.T) {
	const obs = int64(1_700_000_001_000_000_000)
	rows := logRowsOf(&zaplogreceiver.LogBatch{
		AppName: "svc",
		Records: []zaplogreceiver.LogRecord{{
			ObservedTimeUnixNs: obs, Severity: 999, SeverityText: "FATAL",
			Body: "boom", EventName: "crash",
		}},
	})
	row := rows[0]
	if got := logCol(t, row, "name"); got != "crash" {
		t.Errorf("name = %v, want crash (EventName)", got)
	}
	if got, ok := logCol(t, row, "severity_number").(uint8); !ok || got != 255 {
		t.Errorf("severity_number = %v, want 255 (clamped)", got)
	}
	if got, ok := logCol(t, row, "time").(time.Time); !ok || !got.Equal(time.Unix(0, obs).UTC()) {
		t.Errorf("time = %v, want observed %v", got, time.Unix(0, obs).UTC())
	}
}

func TestLogRowsOf_EmptyAndNil(t *testing.T) {
	if rows := logRowsOf(nil); rows != nil {
		t.Errorf("nil batch -> %v, want nil", rows)
	}
	if rows := logRowsOf(&zaplogreceiver.LogBatch{}); rows != nil {
		t.Errorf("no-record batch -> %v, want nil", rows)
	}
}

// Two byte-identical log lines in one batch must stay two rows — the id is salted by
// position so a ReplacingMergeTree does not collapse them.
func TestLogRowID_DeterministicAndPositionSalted(t *testing.T) {
	r := zaplogreceiver.LogRecord{Body: "same", TraceID: "t", SpanID: "s"}
	a := logRowID("svc", 100, 0, r)
	b := logRowID("svc", 100, 0, r)
	c := logRowID("svc", 100, 1, r)
	if a != b {
		t.Errorf("same inputs -> %q vs %q, want stable", a, b)
	}
	if a == c {
		t.Errorf("position 0 and 1 -> same id %q, want distinct", a)
	}
	if len(a) != 32 {
		t.Errorf("id length = %d, want 32 hex chars", len(a))
	}
}

func TestAttrString(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hi", "hi"},
		{"bool", true, "true"},
		{"whole-float", float64(502), "502"}, // status codes read "502", never "502.0"
		{"frac-float", float64(1.5), "1.5"},
		{"slice", []any{"a", "b"}, `["a","b"]`}, // non-scalar keeps its JSON form
		{"map", map[string]any{"k": "v"}, `{"k":"v"}`},
	} {
		if got := attrString(tc.in); got != tc.want {
			t.Errorf("%s: attrString(%v) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestPlaneSpanKind(t *testing.T) {
	for in, want := range map[string]string{
		"server": "server", "SPAN_KIND_SERVER": "server",
		"client": "client", "SPAN_KIND_CLIENT": "client",
		"producer": "producer", "SPAN_KIND_PRODUCER": "producer",
		"consumer": "consumer", "SPAN_KIND_CONSUMER": "consumer",
		"internal": "internal", "": "internal", "whatever": "internal",
	} {
		if got := planeSpanKind(in); got != want {
			t.Errorf("planeSpanKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlaneStatus(t *testing.T) {
	for in, want := range map[string]string{
		"error": "error", "Error": "error", "ERROR": "error",
		"ok": "ok", "OK": "ok", "unset": "ok", "": "ok",
	} {
		if got := planeStatus(in); got != want {
			t.Errorf("planeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlaneService(t *testing.T) {
	if got := planeService(map[string]string{"service.name": "a", "app": "b"}, "c"); got != "a" {
		t.Errorf("service.name must win: got %q", got)
	}
	if got := planeService(map[string]string{"app": "b"}, "c"); got != "c" {
		t.Errorf("AppName is second: got %q", got)
	}
	if got := planeService(map[string]string{"app": "b"}, ""); got != "b" {
		t.Errorf("app label is third: got %q", got)
	}
	if got := planeService(nil, ""); got != "" {
		t.Errorf("nothing -> empty: got %q", got)
	}
}

// The k8s derivation, in OTel's precedence order. A more-specific workload
// alongside a less-specific one resolves to the owner the spec names first.
func TestPlaneService_K8sWorkloadPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource map[string]string
		want     string
	}{
		{"deployment beats container", map[string]string{
			"k8s.deployment.name": "ingress", "k8s.container.name": "ingress-sidecar"}, "ingress"},
		{"deployment beats replicaset", map[string]string{
			"k8s.deployment.name": "cloud", "k8s.replicaset.name": "cloud-5685c4d7b5"}, "cloud"},
		{"statefulset", map[string]string{
			"k8s.statefulset.name": "hanzod-mv", "k8s.container.name": "hanzod"}, "hanzod-mv"},
		{"daemonset", map[string]string{
			"k8s.daemonset.name": "csi-do-node", "k8s.container.name": "csi-do-plugin"}, "csi-do-node"},
		{"cronjob beats job", map[string]string{
			"k8s.cronjob.name": "reindex", "k8s.job.name": "reindex-29..."}, "reindex"},
		{"container is the floor", map[string]string{"k8s.container.name": "mpc-node"}, "mpc-node"},
		{"pod name is NOT a service", map[string]string{
			"k8s.pod.name": "ingress-b7854888d-gb8xw"}, ""},
		{"app label beats the k8s derivation", map[string]string{
			"app": "gateway", "k8s.deployment.name": "gateway-canary"}, "gateway"},
	} {
		if got := planeService(tc.resource, ""); got != tc.want {
			t.Errorf("%s: planeService(%v) = %q, want %q", tc.name, tc.resource, got, tc.want)
		}
	}
}

// The production regression this derivation closes, end to end on the builder: the
// otel-agent's filelog receiver states k8s.* and NOTHING else, and the infra log
// lens filters `service = <workload>`. An empty service column is that lens dark.
func TestLogRowsOf_FilelogResourceResolvesWorkload(t *testing.T) {
	rows := logRowsOf(&zaplogreceiver.LogBatch{
		Resource: map[string]string{
			"k8s.namespace.name":  "hanzo",
			"k8s.pod.name":        "ingress-b7854888d-gb8xw",
			"k8s.container.name":  "ingress",
			"k8s.deployment.name": "ingress",
			"host.name":           "otel-agent-4gppg",
			"log.iostream":        "stdout",
		},
		Records: []zaplogreceiver.LogRecord{{TimeUnixNs: 1, Body: "GET /v2/sessions"}},
	})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got := logCol(t, rows[0], "service"); got != "ingress" {
		t.Errorf("service = %q, want ingress — an empty service is the infra log lens dark", got)
	}
}

// The in-process span path shares the resolver, so a resource with no service.name
// resolves its workload identically instead of writing an empty service.
func TestSdkSpanRowsOf_DerivesWorkloadService(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("k8s.deployment.name", "cloud"))),
	)
	_, span := tp.Tracer("planesink-test").Start(context.Background(), "job")
	span.End()

	rows := sdkSpanRowsOf(sr.Ended())
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got := spanCol(t, rows[0], "service"); got != "cloud" {
		t.Errorf("service = %q, want cloud (derived from k8s.deployment.name)", got)
	}
}

func TestPlaneOrg(t *testing.T) {
	if got := planeOrg(map[string]string{"hanzo.org": "acme"}); got != "acme" {
		t.Errorf("hanzo.org must win: got %q", got)
	}
	if got := planeOrg(map[string]string{}); got != platformOrg {
		t.Errorf("no tenant -> platform: got %q, want %q", got, platformOrg)
	}
}

// The in-process twin: a real recorded SDK span renders the same event.span row shape
// as the wire path, sharing every helper (org, kind, status, service).
func TestSdkSpanRowsOf_RecordedSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "evalsvc"))),
	)
	_, span := tp.Tracer("planesink-test").Start(context.Background(), "GET /v1/x",
		trace.WithSpanKind(trace.SpanKindServer))
	span.SetAttributes(attribute.String("hanzo.org", "acme"), attribute.Int("http.response.status_code", 500))
	span.SetStatus(codes.Error, "boom")
	span.End()

	rows := sdkSpanRowsOf(sr.Ended())
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if len(row) != len(planeSpanColumns) {
		t.Fatalf("row width %d != column count %d", len(row), len(planeSpanColumns))
	}
	if got := spanCol(t, row, "org"); got != "acme" {
		t.Errorf("org = %v, want acme", got)
	}
	if got := spanCol(t, row, "name"); got != "GET /v1/x" {
		t.Errorf("name = %v", got)
	}
	if got := spanCol(t, row, "kind"); got != "server" {
		t.Errorf("kind = %v, want server", got)
	}
	if got := spanCol(t, row, "service"); got != "evalsvc" {
		t.Errorf("service = %v, want evalsvc", got)
	}
	if got := spanCol(t, row, "status"); got != "error" {
		t.Errorf("status = %v, want error", got)
	}
	if _, ok := spanCol(t, row, "duration").(uint64); !ok {
		t.Errorf("duration is %T, want uint64", spanCol(t, row, "duration"))
	}
	if got := spanCol(t, row, "parent"); got != "" {
		t.Errorf("root span parent = %v, want empty", got)
	}
	attrs, ok := spanCol(t, row, "attributes").(map[string]string)
	if !ok || attrs["status.message"] != "boom" || attrs["service.name"] != "evalsvc" || !strings.Contains(attrs["hanzo.org"], "acme") {
		t.Errorf("attributes = %v", spanCol(t, row, "attributes"))
	}
}

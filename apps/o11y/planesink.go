// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// planesink.go — the WRITE half of cloud's own telemetry, ON THE EVENT PLANE.
//
// Spans and logs that reach this binary land in event.span / event.log — the
// same 15-column envelope every other signal shares (event.event, event.error,
// event.metric) — never in the retired o11y_* databases. ONE shape for every
// signal's write path, and it is the shape metrics.go already proved: a ZAP
// receiver decodes the wire, a native writer appends a prepared batch over the
// branded datastore client. No embedded otelcol pipeline, no pdata bridge, no
// exporter fork — the previous path (ingest.go + zapingest.go + spanconv.go +
// tracesink.go) existed to feed the SigNoz-schema exporters, and with the plane
// as the store the entire translation layer is deleted rather than ported.
//
// Wire compatibility is exact: the same ZAP span wire on :4317 and log wire on
// :4318 the embedded collector bound (zapreceiver / zaplogreceiver — the same
// decoders it used), so no sender changes. The in-process trace sink keeps its
// contract too: cloud.RegisterTraceSink receives the live SDK batch and this
// file writes it as rows — one converter fewer than the pdata detour.
//
// Safety posture (this feeds a LIVE, SHARED telemetry store):
//   - Bound by CAPABILITY, not by a flag: ingest runs exactly when a datastore
//     DSN is configured, because the DSN is the thing it writes to (the same
//     posture ingest.go held since 2026-07-25's connection-refused lesson).
//   - Fail-soft: any construction error logs and returns nil — a bad telemetry
//     config can never take cloud down.
//   - The in-process trace sink stays OPT-IN (O11Y_TRACES_ZAP_INPROCESS), and
//     shutdown deregisters it before the sink closes.
//
// Row identity: every table is a ReplacingMergeTree keyed on (…, id), so ids
// are DERIVED — a span's id is its span_id; a log line's id is a hash of what
// it says and when. Idempotency is structural, exactly as in the analytics
// warehouse next door.

package o11y

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	ds "github.com/hanzo-ds/go"
	zaplogreceiver "github.com/hanzoai/o11y/pkg/zaplogreceiver"
	zapreceiver "github.com/hanzoai/o11y/pkg/zapreceiver"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/hanzoai/cloud"
)

// The plane tables this sink appends to, and the exact column lists it states.
// Columns the wire has no value for (session_id, distinct_id, url, el, …) are
// omitted so the table's own defaults apply — the implsentry discipline.
const (
	planeSpanTable = "event.span"
	planeLogTable  = "event.log"

	// The ZAP wire addresses, unchanged from the embedded collector: 4317 is
	// the canonical span wire every Hanzo service sends to, 4318 the log wire.
	// This sink binds ONLY these two sockets — cloud owns its own HTTP and
	// health listeners, so the :9090-class collision the old collector had to
	// be configured around cannot exist here.
	planeSpanListen = "0.0.0.0:4317"
	planeLogListen  = "0.0.0.0:4318"

	// platformOrg attributes a row that carries no tenant of its own: fleet
	// infra telemetry belongs to the platform. A span stamped hanzo.org (the
	// TracingMiddleware tenant) keeps its own org.
	platformOrg = "hanzo"
)

// ingested_at IS DELIBERATELY ABSENT FROM BOTH LISTS, and it is the one omission
// that is a rule rather than a default. The plane's DDL (hanzoai/o11y owns it)
// declares it `DateTime64(3) DEFAULT now64(3)` and then measures BOTH retention
// and layout from it — `TTL toDateTime(ingested_at) + toIntervalDay(30)` and
// `PARTITION BY toDate(ingested_at)` on event.span. A writer that binds it hands
// the wire control of when its own row expires: a batch stamped 30 days back is
// accepted, answers 200, and is TTL-eligible before the reply lands. So the
// server stamps it, always. apps/analytics states the same rule for the four
// warehouse writers and pins it with a test (TestRetentionIsNotARequestParameter);
// TestPlaneWritersNeverBindIngestedAt below is that pin for these two.
//
// It is ALSO event.span's ReplacingMergeTree version column, which is why leaving
// it to the DEFAULT is correct and not merely convenient: identity is
// (org, trace_id, time, id) and every one of those is a pure function of the
// span, so a re-sent span produces a NEW version of the SAME identity and the
// merge collapses it. The version has to be a clock, and the server's clock is
// the only one every writer shares.
var (
	planeSpanColumns = []string{"org", "time", "id", "name", "kind", "service",
		"trace_id", "span_id", "parent", "duration", "status", "attributes"}
	planeLogColumns = []string{"org", "time", "id", "name", "kind", "service",
		"severity_text", "severity_number", "body", "trace_id", "span_id", "attributes"}
)

// planeSink pins the ingest resources for the process life so shutdown can
// stop the listeners and flush the connection, mirroring metricsIngest.
type planeSink struct {
	sink    *datastoreSink
	spanRcv *zapreceiver.Receiver
	logRcv  *zaplogreceiver.Receiver
}

var embeddedPlaneSink *planeSink

// mountPlaneIngest starts the ZAP span+log receivers writing to the event
// plane, and registers the in-process trace sink when its flag is on. Called
// by mountO11y; order-independent (no Fiber route). Fail-soft at every branch.
func mountPlaneIngest(deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-plane-ingest")

	dsn := embeddedDSN()
	if dsn == "" {
		log.Warn("plane ingest not started: no datastore DSN (needs O11Y_DATASTORE_DSN)")
		return nil
	}
	sink, err := newDatastoreSink(context.Background(), dsn)
	if err != nil {
		log.Warn("plane ingest init failed; spans/logs stay unwritten", "err", err)
		return nil // fail-soft
	}
	ps := &planeSink{sink: sink}

	spanRcv, err := zapreceiver.New(zapreceiver.Config{
		Listen: planeSpanListen,
		NodeID: "cloud-o11y-plane",
		OnBatch: func(ctx context.Context, b *zapreceiver.SpanBatch) error {
			rows := spanRowsOf(b)
			if len(rows) == 0 {
				return nil
			}
			return ps.sink.Insert(ctx, planeSpanTable, planeSpanColumns, rows)
		},
	})
	if err != nil {
		log.Warn("plane span ingest failed to start", "listen", planeSpanListen, "err", err)
	} else {
		ps.spanRcv = spanRcv
	}

	logRcv, err := zaplogreceiver.New(zaplogreceiver.Config{
		Listen: planeLogListen,
		NodeID: "cloud-o11y-plane",
		OnBatch: func(ctx context.Context, b *zaplogreceiver.LogBatch) error {
			rows := logRowsOf(b)
			if len(rows) == 0 {
				return nil
			}
			return ps.sink.Insert(ctx, planeLogTable, planeLogColumns, rows)
		},
	})
	if err != nil {
		log.Warn("plane log ingest failed to start", "listen", planeLogListen, "err", err)
	} else {
		ps.logRcv = logRcv
	}

	// The in-process sink for cloud's OWN spans: the host hands the live SDK
	// batch over (cloud/telemetry.go), this writes it as rows. Same opt-in
	// flag, same fall-through-to-the-wire contract tracesink.go carried.
	if cloud.TraceInprocEnabled() {
		cloud.RegisterTraceSink(func(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
			rows := sdkSpanRowsOf(spans)
			if len(rows) == 0 {
				return nil
			}
			return ps.sink.Insert(ctx, planeSpanTable, planeSpanColumns, rows)
		})
		log.Info("in-process trace sink live: cloud's own spans -> event.span (Cost-0, no socket)")
	}

	embeddedPlaneSink = ps
	log.Info("plane ingest running", "spans", planeSpanListen, "logs", planeLogListen,
		"sink", planeSpanTable+" + "+planeLogTable)
	return nil
}

// shutdownPlaneIngest deregisters the trace sink (a late export falls back to
// the wire), stops the listeners, and closes the connection. Nil-safe.
func shutdownPlaneIngest(context.Context) error {
	cloud.RegisterTraceSink(nil)
	ps := embeddedPlaneSink
	if ps == nil {
		return nil
	}
	if ps.spanRcv != nil {
		ps.spanRcv.Stop()
	}
	if ps.logRcv != nil {
		ps.logRcv.Stop()
	}
	return ps.sink.Close()
}

// ── pure row builders (unit-tested without a store or a socket) ─────────────

// spanRowsOf renders one wire SpanBatch as event.span rows. One batch is one
// service; resource attributes fold into each row's attribute map (the plane
// carries no separate resource object — deployment.environment et al. ride
// beside the span's own attributes, as the existing rows already do).
func spanRowsOf(b *zapreceiver.SpanBatch) [][]any {
	if b == nil || len(b.Spans) == 0 {
		return nil
	}
	service := planeService(b.Resource, b.AppName)
	rows := make([][]any, 0, len(b.Spans))
	for _, s := range b.Spans {
		attrs := make(map[string]string, len(b.Resource)+len(s.Attributes)+2)
		for k, v := range b.Resource {
			attrs[k] = v
		}
		if b.Version != "" {
			attrs["service.version"] = b.Version
		}
		for k, v := range s.Attributes {
			attrs[k] = attrString(v)
		}
		if s.StatusMsg != "" {
			attrs["status.message"] = s.StatusMsg
		}
		var dur uint64
		if s.EndUnixNs > s.StartUnixNs {
			dur = uint64(s.EndUnixNs - s.StartUnixNs)
		}
		rows = append(rows, []any{
			planeOrg(attrs),
			time.Unix(0, s.StartUnixNs).UTC(),
			s.SpanID,
			s.Name,
			planeSpanKind(s.Kind),
			service,
			s.TraceID,
			s.SpanID,
			s.ParentSpanID,
			dur,
			planeStatus(s.StatusCode),
			attrs,
		})
	}
	return rows
}

// logRowsOf renders one wire LogBatch as event.log rows.
func logRowsOf(b *zaplogreceiver.LogBatch) [][]any {
	if b == nil || len(b.Records) == 0 {
		return nil
	}
	service := planeService(b.Resource, b.AppName)
	rows := make([][]any, 0, len(b.Records))
	for i, r := range b.Records {
		attrs := make(map[string]string, len(b.Resource)+len(r.Attributes))
		for k, v := range b.Resource {
			attrs[k] = v
		}
		for k, v := range r.Attributes {
			attrs[k] = attrString(v)
		}
		ns := r.TimeUnixNs
		if ns == 0 {
			ns = r.ObservedTimeUnixNs
		}
		ts := time.Unix(0, ns).UTC()
		if ns == 0 {
			ts = time.Now().UTC()
		}
		name := r.EventName
		if name == "" {
			name = "log"
		}
		rows = append(rows, []any{
			planeOrg(attrs),
			ts,
			logRowID(service, ns, i, r),
			name,
			"log",
			service,
			r.SeverityText,
			uint8(min(max(r.Severity, 0), 255)),
			r.Body,
			r.TraceID,
			r.SpanID,
			attrs,
		})
	}
	return rows
}

// sdkSpanRowsOf renders the host's live SDK batch as event.span rows — the
// in-process twin of spanRowsOf, one converter per input shape, one row shape.
func sdkSpanRowsOf(spans []sdktrace.ReadOnlySpan) [][]any {
	rows := make([][]any, 0, len(spans))
	for _, s := range spans {
		if s == nil {
			continue
		}
		attrs := map[string]string{}
		if res := s.Resource(); res != nil {
			for _, kv := range res.Attributes() {
				attrs[string(kv.Key)] = kv.Value.Emit()
			}
		}
		// The SAME resolver the wire path uses — an SDK resource that names no
		// service still carries its workload, and one function decides what a
		// service IS for every row on the plane.
		service := planeService(attrs, "")
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		if msg := s.Status().Description; msg != "" {
			attrs["status.message"] = msg
		}
		sc := s.SpanContext()
		parent := ""
		if s.Parent().HasSpanID() {
			parent = s.Parent().SpanID().String()
		}
		var dur uint64
		if d := s.EndTime().Sub(s.StartTime()); d > 0 {
			dur = uint64(d.Nanoseconds())
		}
		status := "ok"
		if s.Status().Code.String() == "Error" {
			status = "error"
		}
		rows = append(rows, []any{
			planeOrg(attrs),
			s.StartTime().UTC(),
			sc.SpanID().String(),
			s.Name(),
			strings.ToLower(s.SpanKind().String()),
			service,
			sc.TraceID().String(),
			sc.SpanID().String(),
			parent,
			dur,
			status,
			attrs,
		})
	}
	return rows
}

// planeOrg is the row's tenant: the hanzo.org the TracingMiddleware stamped
// when the telemetry belongs to a tenant, else the platform's own.
func planeOrg(attrs map[string]string) string {
	if org := attrs["hanzo.org"]; org != "" {
		return org
	}
	return platformOrg
}

// k8sWorkloadKeys is OTel's own service.name recommendation for a resource that
// declares none, in the spec's precedence order: the workload that OWNS the pod,
// most-stable name first. `k8s.pod.name` sits between job and container in the
// spec and is DELIBERATELY absent — it is per-replica (ingress-b7854888d-gb8xw),
// so it would make `service` unbounded on a LowCardinality column and would never
// match a workload-keyed read. Everything left here is stable across a rollout
// except k8s.replicaset.name, which only ever fires for a bare ReplicaSet because
// the Deployment above it wins whenever one exists.
var k8sWorkloadKeys = []string{
	"k8s.deployment.name",
	"k8s.replicaset.name",
	"k8s.statefulset.name",
	"k8s.daemonset.name",
	"k8s.cronjob.name",
	"k8s.job.name",
	"k8s.container.name",
}

// planeService resolves the service column — the WORKLOAD a row came from, which
// is what every reader keys on (apps/o11y/logs.go binds `service = ?` to the
// product's workload name, and the fleet board groups by it).
//
// The resource's own service.name wins, then the wire batch's app name, then the
// legacy `app` label the retired SigNoz exporter path resolved. Past that we
// DERIVE it from the Kubernetes resource, because the fleet's largest log
// producer states no service.name at all: the otel-agent's filelog receiver
// stamps k8s.* on every tailed container line and nothing else, so 91% of
// event.log arrived with an empty service — and the infra log lens, which filters
// on exactly that column, went dark for every product in the catalog. Deriving
// the workload is the one place that can fix it for spans and logs at once, and
// it is OTel's documented inference rather than a rule invented here.
func planeService(resource map[string]string, appName string) string {
	if s := resource["service.name"]; s != "" {
		return s
	}
	if appName != "" {
		return appName
	}
	if s := resource["app"]; s != "" {
		return s
	}
	for _, k := range k8sWorkloadKeys {
		if s := resource[k]; s != "" {
			return s
		}
	}
	return ""
}

// planeSpanKind normalizes the wire kind onto the plane's lowercase vocabulary
// (server/client/producer/consumer/internal), defaulting internal — OTel's own
// default for a span that states none.
func planeSpanKind(k string) string {
	switch k {
	case "server", "SPAN_KIND_SERVER":
		return "server"
	case "client", "SPAN_KIND_CLIENT":
		return "client"
	case "producer", "SPAN_KIND_PRODUCER":
		return "producer"
	case "consumer", "SPAN_KIND_CONSUMER":
		return "consumer"
	default:
		return "internal"
	}
}

// planeStatus maps a wire status code onto event.span's status column: error
// stays error, everything else (ok, unset) is ok — a span that completed
// without declaring failure succeeded.
func planeStatus(code string) string {
	switch code {
	case "error", "Error", "ERROR":
		return "error"
	default:
		return "ok"
	}
}

// logRowID derives a log row's ReplacingMergeTree identity from what the line
// says and when it says it, salted by its position in the batch so two equal
// lines in one batch stay two rows.
func logRowID(service string, ns int64, i int, r zaplogreceiver.LogRecord) string {
	h := sha256.New()
	h.Write([]byte(service))
	h.Write([]byte(strconv.FormatInt(ns, 10)))
	h.Write([]byte(strconv.Itoa(i)))
	h.Write([]byte(r.Body))
	h.Write([]byte(r.TraceID))
	h.Write([]byte(r.SpanID))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// attrString renders a JSON-decoded attribute value for the Map(String,String)
// column. JSON has one number type, so a whole float renders as its integer
// form — http.response.status_code reads "502", never "502.0". Non-scalars
// (arrays, objects) keep their JSON form.
func attrString(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return n
	case bool:
		return strconv.FormatBool(n)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// datastoreSink is this package's connection to the Hanzo Datastore, over the
// branded github.com/hanzo-ds/go client — a thin wrapper that is the whole of
// the client's use here. datastore-go brings the ONE ch-go transport line the
// o11y runtime already uses (MVS-unified), so it coexists in the single binary.
//
// It lives beside its ONE caller. The type was written for a second writer (an
// LLM-observability ingest path that inserted UNQUALIFIED `traces` /
// `observations` / `scores`); that writer is deleted, because the DSN it shared
// with this file carries no database, so those names resolved to `default` —
// tables no migration in this platform creates. Its concept is served twice
// over already: LLM observability is READ off gen_ai spans in event.span by the
// o11y runtime, and the eval product owns the grounded projections
// (hanzo.eval_traces / hanzo.eval_scores, apps/eval/telemetry.go) with DDL it
// creates itself.
type datastoreSink struct {
	conn ds.Conn
}

// newDatastoreSink opens (and pings) the native Datastore connection from the DSN.
func newDatastoreSink(ctx context.Context, dsn string) (*datastoreSink, error) {
	opt, err := ds.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse datastore dsn: %w", err)
	}
	conn, err := ds.Open(opt)
	if err != nil {
		return nil, fmt.Errorf("open datastore: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping datastore: %w", err)
	}
	return &datastoreSink{conn: conn}, nil
}

// Insert writes rows to a table as ONE prepared native batch. table is stated
// FULLY QUALIFIED by every caller (event.span, event.log): the DSN names no
// database, so an unqualified name would silently address `default`.
func (s *datastoreSink) Insert(ctx context.Context, table string, columns []string, rows [][]any) error {
	query := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ")"
	batch, err := s.conn.PrepareBatch(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, row := range rows {
		if err := batch.Append(row...); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return err
	}
	// Rows are counted HERE — the one choke point every plane row this process
	// writes passes through (event.span from the ZAP span receiver and from the
	// in-process trace sink, event.log from the log receiver) — and only AFTER
	// Send returns, so the count is rows that LANDED, not rows that were
	// offered. event.span going to zero here is the signal that was missing for
	// four and a half months.
	//
	// The table name IS the stream name for these two, which is what lets the
	// analytics bus drain (apps/analytics/warehouse.go) count onto the same
	// series: a row of a given signal counts once, whichever writer carried it.
	cloud.ObserveRows(table, len(rows))
	return nil
}

// Close releases the native connection.
func (s *datastoreSink) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

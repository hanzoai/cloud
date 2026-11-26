// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	maxIngestBatchSpans = 512
	maxAttrPerSpan      = 128
	maxAttrValueBytes   = 64 << 10 // 64 KiB cap per HTTP-ingested attribute
)

// Deps supplies the host dependencies required by the agenttrace app.
// It imports nothing of its host so the package mounts unchanged in any zip host.
type Deps struct {
	// Log is the host logger scoped to the agenttrace subsystem.
	Log luxlog.Logger

	// Org returns the validated org of the caller on ctx.
	Org func(ctx context.Context) (org string, ok bool)

	// Acting returns the validated org a call acts for, or a zip.ErrUnauthorized / zip.ErrForbidden.
	Acting func(ctx context.Context) (org string, err error)

	// Operator enforces platform operator authority for global exporter diagnostics
	// (GET /v1/agenttrace/diagnostics). When nil, defaults to fail-closed SuperAdmin.
	Operator func(ctx context.Context) error

	// Config optionally overrides default Tracer parameters.
	Config TracerConfig
}

// Service owns the unified HIP-1348 Tracer, multi-tenant TraceStore, and host identity bindings.
type Service struct {
	log      luxlog.Logger
	org      func(context.Context) (string, bool)
	acting   func(context.Context) (string, error)
	operator func(context.Context) error
	tracer   *Tracer
}

var (
	mountedMu sync.Mutex
	mounted   *Service
)

// NewService constructs a standalone agenttrace Service from deps.
func NewService(deps Deps) (*Service, error) {
	if deps.Log == nil || deps.Org == nil || deps.Acting == nil {
		return nil, errors.New("agenttrace: Deps requires Log, Org, and Acting")
	}
	operator := deps.Operator
	if operator == nil {
		operator = func(ctx context.Context) error {
			if _, err := deps.Acting(ctx); err != nil {
				return err
			}
			if !zip.CallerOf(ctx).Admin {
				return zip.ErrForbidden("platform operator authority required for global trace diagnostics")
			}
			return nil
		}
	}
	cfg := deps.Config
	cfg.EnableStore = true
	tracer := NewTracer(cfg)

	return &Service{
		log:      deps.Log,
		org:      deps.Org,
		acting:   deps.Acting,
		operator: operator,
		tracer:   tracer,
	}, nil
}

// Tracer returns the underlying unified HIP-1348 Tracer.
func (s *Service) Tracer() *Tracer { return s.tracer }

// Store returns the tenant-isolated TraceStore.
func (s *Service) Store() *TraceStore { return s.tracer.Store() }

// Close gracefully flushes and shuts down the background BatchExporter.
func (s *Service) Close(ctx context.Context) error {
	if s == nil || s.tracer == nil {
		return nil
	}
	return s.tracer.Shutdown(ctx)
}

// Mount registers the typed /v1/agenttrace HTTP surface on app.
func Mount(app *zip.App, deps Deps) error {
	if app == nil {
		return errors.New("agenttrace: nil app")
	}
	svc, err := NewService(deps)
	if err != nil {
		return fmt.Errorf("agenttrace.Mount: %w", err)
	}
	MountService(app, svc)
	mountedMu.Lock()
	mounted = svc
	mountedMu.Unlock()
	svc.log.Info("agenttrace mounted", "prefix", "/v1/agenttrace", "capture_mode", string(svc.tracer.CaptureMode()))
	return nil
}

// Shutdown flushes and shuts down the mounted agenttrace Service.
func Shutdown(ctx context.Context) error {
	mountedMu.Lock()
	svc := mounted
	mounted = nil
	mountedMu.Unlock()
	if svc == nil {
		return nil
	}
	return svc.Close(ctx)
}

// MountService mounts an existing Service on app at /v1/agenttrace.
func MountService(app *zip.App, svc *Service) {
	o := ops{s: svc}
	g := app.Group("/v1/agenttrace")
	g.Get("/health", o.health)
	g.Post("/spans", o.ingestSpans, zip.WithStatus(http.StatusAccepted))
	g.Get("/tasks", o.listTasks)
	g.Get("/tasks/:taskId", o.getTask)
	g.Get("/tasks/:taskId/verify", o.verifyTask)
	g.Get("/tasks/:taskId/otlp", o.exportTaskOTLP)
	g.Get("/diagnostics", o.diagnostics)
}

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type ops struct{ s *Service }

// healthOut reports non-tenant-specific liveness and aggregate exporter readiness.
type healthOut struct {
	Status        string             `json:"status"`
	Service       string             `json:"service"`
	CaptureMode   ContentCaptureMode `json:"captureMode"`
	RingCapacity  int                `json:"ringCapacity"`
	BufferedSpans int                `json:"bufferedSpans"`
	ExportedSpans uint64             `json:"exportedSpans"`
	DroppedSpans  uint64             `json:"droppedSpans"`
}

// spanIngestItem describes a single span submitted over HTTP.
// Org is NEVER accepted from the request payload; it is stamped strictly from
// the server-validated IAM identity boundary (Acting).
type spanIngestItem struct {
	TaskID        string     `json:"taskId" url:"-"`
	TenantID      string     `json:"tenantId" url:"-"`
	Subsystem     Subsystem  `json:"subsystem" url:"-"`
	TraceID       string     `json:"traceId" url:"-"`
	SpanID        string     `json:"spanId" url:"-"`
	ParentSpanID  string     `json:"parentSpanId" url:"-"`
	Name          string     `json:"name" url:"-"`
	Kind          SpanKind   `json:"kind" url:"-"`
	StartTimeNano int64      `json:"startTimeUnixNano" url:"-"`
	EndTimeNano   int64      `json:"endTimeUnixNano" url:"-"`
	Status        StatusCode `json:"status" url:"-"`
	StatusMessage string     `json:"statusMessage" url:"-"`
	Attributes    []Attr     `json:"attributes" url:"-"`
}

// ingestSpansIn submits a batch of spans under the caller's validated org.
// Every field carries `url:"-"` to prevent query-string parameter hijacking.
type ingestSpansIn struct {
	Spans []spanIngestItem `json:"spans" url:"-"`
	Flush bool             `json:"flush" url:"-"`
}

// ingestSpansOut confirms accepted/dropped counts after DLP sanitization.
type ingestSpansOut struct {
	Accepted      int `json:"accepted"`
	Dropped       int `json:"dropped"`
	RedactedAttrs int `json:"redactedAttrs"`
}

// taskListIn optionally filters tasks by a sub-org tenantId within the caller's org.
type taskListIn struct {
	TenantID string `json:"tenantId" query:"tenantId"`
}

// taskListOut lists task IDs owned strictly by the caller's validated org/tenant.
type taskListOut struct {
	OrgID    string   `json:"orgId"`
	TenantID string   `json:"tenantId"`
	Tasks    []string `json:"tasks"`
}

// taskRef addresses a single task by path parameter and optional sub-org tenantId.
type taskRef struct {
	TaskID   string `json:"taskId"`
	TenantID string `json:"tenantId" query:"tenantId"`
}

// taskDetailOut wraps the unified trace spans for a single task owned by the caller.
type taskDetailOut struct {
	TaskID   string `json:"taskId"`
	OrgID    string `json:"orgId"`
	TenantID string `json:"tenantId"`
	TraceID  string `json:"traceId"`
	Spans    []Span `json:"spans"`
}

// diagnosticsOut reports global exporter, ring buffer, and store telemetry for operators.
type diagnosticsOut struct {
	CaptureMode ContentCaptureMode `json:"captureMode"`
	Exporter    ExporterStats      `json:"exporter"`
	Store       TraceStoreStats    `json:"store"`
}

// health returns liveness and aggregate ring/exporter counters with zero tenant PII.
func (o ops) health(_ context.Context, _ *struct{}) (*healthOut, error) {
	st := o.s.tracer.Stats()
	return &healthOut{
		Status:        "ok",
		Service:       "agenttrace",
		CaptureMode:   o.s.tracer.CaptureMode(),
		RingCapacity:  st.Ring.Capacity,
		BufferedSpans: st.Ring.Buffered,
		ExportedSpans: st.ExportedSpans,
		DroppedSpans:  st.Ring.DroppedSpans,
	}, nil
}

// ingestSpans accepts spans strictly under the caller's server-validated IAM org,
// sanitizes all attributes through the DLP gate, and enqueues them into the
// non-blocking bounded ring buffer.
func (o ops) ingestSpans(ctx context.Context, in *ingestSpansIn) (*ingestSpansOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	if !ValidateIdentifier(org) {
		return nil, zip.ErrUnauthorized("authenticated org required")
	}
	if in == nil || len(in.Spans) == 0 {
		return nil, zip.ErrBadRequest("spans list must be non-empty")
	}
	if len(in.Spans) > maxIngestBatchSpans {
		return nil, zip.ErrBadRequest(fmt.Sprintf("batch exceeds maximum %d spans", maxIngestBatchSpans))
	}

	mode := o.s.tracer.CaptureMode()
	var accepted, dropped, redactedTotal int

	for i, item := range in.Spans {
		if !ValidateIdentifier(item.TaskID) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: invalid taskId", i))
		}
		if !item.Subsystem.Valid() {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: invalid subsystem %q", i, item.Subsystem))
		}
		tenant := strings.TrimSpace(item.TenantID)
		if tenant == "" {
			tenant = org
		} else if !ValidateIdentifier(tenant) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: invalid tenantId", i))
		}
		tid, err := ParseTraceID(item.TraceID)
		if err != nil {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: %v", i, err))
		}
		sid, err := ParseSpanID(item.SpanID)
		if err != nil {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: %v", i, err))
		}
		var psid SpanID
		if item.ParentSpanID != "" {
			psid, err = ParseSpanID(item.ParentSpanID)
			if err != nil {
				return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: invalid parentSpanId", i))
			}
		}
		if item.StartTimeNano <= 0 || item.EndTimeNano < item.StartTimeNano {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: invalid start/end timestamps", i))
		}
		if len(item.Attributes) > maxAttrPerSpan {
			return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: exceeds maximum %d attributes", i, maxAttrPerSpan))
		}

		// Fail-closed cross-tenant guard before enqueueing if task already belongs to another org/tenant.
		if _, checkErr := o.s.Store().GetTask(org, tenant, item.TaskID); errors.Is(checkErr, ErrCrossTenantDenied) {
			return nil, zip.ErrForbidden("cross-tenant span injection denied")
		}

		attrs := make([]Attr, 0, len(item.Attributes)+4)
		redacted := 0
		for _, a := range item.Attributes {
			// Prevent spoofing server-stamped correlation keys via attributes.
			switch a.Key {
			case AttrHanzoOrgID, AttrHanzoTenantID, AttrHanzoTaskID, AttrHanzoSubsystem:
				continue
			}
			if a.Kind == AttrString && len(a.Str) > maxAttrValueBytes {
				return nil, zip.ErrBadRequest(fmt.Sprintf("spans[%d]: attribute %q exceeds size limit", i, a.Key))
			}
			clean, wasRed := SanitizeAttr(a, mode)
			if wasRed {
				redacted++
			}
			attrs = append(attrs, clean)
		}
		// Authoritatively stamp correlation attributes from validated identity.
		attrs = append(attrs,
			StringAttr(AttrHanzoTaskID, item.TaskID),
			StringAttr(AttrHanzoOrgID, org),
			StringAttr(AttrHanzoTenantID, tenant),
			StringAttr(AttrHanzoSubsystem, string(item.Subsystem)),
		)

		statusMsg := item.StatusMessage
		if mode != CaptureRaw && statusMsg != "" {
			if cleaned, wasRed := ScrubSecrets(statusMsg); wasRed {
				statusMsg = cleaned
				redacted++
			}
		}

		sp := Span{
			TaskID:        item.TaskID,
			OrgID:         org,
			TenantID:      tenant,
			Subsystem:     item.Subsystem,
			TraceID:       tid,
			SpanID:        sid,
			ParentSpanID:  psid,
			Name:          item.Name,
			Kind:          item.Kind,
			StartTimeNano: item.StartTimeNano,
			EndTimeNano:   item.EndTimeNano,
			Status:        item.Status,
			StatusMessage: statusMsg,
			Attributes:    attrs,
			RedactedCount: redacted,
		}
		if o.s.tracer.exporter.Enqueue(sp) {
			accepted++
		} else {
			dropped++
		}
		redactedTotal += redacted
	}

	if in.Flush {
		_ = o.s.tracer.Flush(ctx)
	}
	return &ingestSpansOut{
		Accepted:      accepted,
		Dropped:       dropped,
		RedactedAttrs: redactedTotal,
	}, nil
}

// listTasks returns task IDs strictly within the caller's validated org/tenant.
func (o ops) listTasks(ctx context.Context, in *taskListIn) (*taskListOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	tenant := org
	if in != nil && strings.TrimSpace(in.TenantID) != "" {
		tenant = strings.TrimSpace(in.TenantID)
		if !ValidateIdentifier(tenant) {
			return nil, zip.ErrBadRequest("invalid tenantId")
		}
	}
	tasks, err := o.s.Store().ListTaskIDs(org, tenant)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if tasks == nil {
		tasks = []string{}
	}
	return &taskListOut{
		OrgID:    org,
		TenantID: tenant,
		Tasks:    tasks,
	}, nil
}

// getTask returns all spans correlated under taskId for the caller's validated org.
func (o ops) getTask(ctx context.Context, in *taskRef) (*taskDetailOut, error) {
	org, tenant, spans, err := o.loadAuthorizedTask(ctx, in)
	if err != nil {
		return nil, err
	}
	traceID := ""
	if len(spans) > 0 {
		traceID = spans[0].TraceID.String()
	}
	return &taskDetailOut{
		TaskID:   in.TaskID,
		OrgID:    org,
		TenantID: tenant,
		TraceID:  traceID,
		Spans:    spans,
	}, nil
}

// verifyTask executes VerifyTaskCompleteness for a task strictly owned by the caller.
func (o ops) verifyTask(ctx context.Context, in *taskRef) (*CompletenessReport, error) {
	org, tenant, spans, err := o.loadAuthorizedTask(ctx, in)
	if err != nil {
		return nil, err
	}
	rep := VerifyTaskCompleteness(spans, VerifyOptions{
		RateCard:         o.s.tracer.RateCard(),
		ExpectedOrgID:    org,
		ExpectedTenantID: tenant,
	})
	return &rep, nil
}

// exportTaskOTLP exports a caller-owned task in standard OTLP/JSON schema.
func (o ops) exportTaskOTLP(ctx context.Context, in *taskRef) (*OTLPTraceRequest, error) {
	_, _, spans, err := o.loadAuthorizedTask(ctx, in)
	if err != nil {
		return nil, err
	}
	raw := EncodeOTLPJSON(spans, ServiceName)
	req, err := DecodeOTLPJSON(raw)
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// diagnostics returns global ring buffer, drop-counter, and store metrics.
// Strictly guarded by platform operator authorization (fail-closed).
func (o ops) diagnostics(ctx context.Context, _ *struct{}) (*diagnosticsOut, error) {
	if err := o.s.operator(ctx); err != nil {
		return nil, err
	}
	return &diagnosticsOut{
		CaptureMode: o.s.tracer.CaptureMode(),
		Exporter:    o.s.tracer.Stats(),
		Store:       o.s.Store().Stats(),
	}, nil
}

func (o ops) loadAuthorizedTask(ctx context.Context, in *taskRef) (org, tenant string, spans []Span, err error) {
	org, err = o.s.acting(ctx)
	if err != nil {
		return "", "", nil, err
	}
	if in == nil || !ValidateIdentifier(in.TaskID) {
		return "", "", nil, zip.ErrBadRequest("invalid taskId")
	}
	tenant = org
	if strings.TrimSpace(in.TenantID) != "" {
		tenant = strings.TrimSpace(in.TenantID)
		if !ValidateIdentifier(tenant) {
			return "", "", nil, zip.ErrBadRequest("invalid tenantId")
		}
	}
	spans, err = o.s.Store().GetTask(org, tenant, in.TaskID)
	if err != nil {
		return "", "", nil, mapStoreError(err)
	}
	return org, tenant, spans, nil
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, ErrEmptyTenantScope):
		return zip.ErrUnauthorized(err.Error())
	case errors.Is(err, ErrCrossTenantDenied), errors.Is(err, ErrCrossTenantContam):
		return zip.ErrForbidden(err.Error())
	case errors.Is(err, ErrTaskNotFound):
		return zip.ErrNotFound(err.Error())
	default:
		return err
	}
}

package studio

// lane.go — the two lanes a step can run on besides the gateway, and the door the
// render workers claim through.
//
// THE RENDER LANE IS THIS PROCESS'S, AND NO CUSTOMER SHARD HOLDS IT. Render jobs —
// HTML to pixels, documents, slides, compositions, video edits — run on the
// platform's own CPU workers for every org. Putting them in an org's shard of the
// workflow app's engine would put them where that org's members can list and claim
// them through /v1/workflow, and org hanzo is every self-serve customer. So they are
// standalone activities in THIS engine, in a shard (laneOrg) no request maps to,
// and the workers reach them only through /v1/studio/render, which admits the
// render workload and nobody else (door, below). A job names no credential and no
// origin: its inputs are signed GET URLs and its one output a signed PUT URL, each
// good for that job's objects alone.
//
// THE GPU LANE IS THE ORG'S. A studio.render step goes to the gpu-jobs lane of the
// workflow app's engine, which the org's own linked GPUs (hanzo link) claim; this
// process asks for it over the plane, in the run's org.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/workflow"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/orm/tenant"
	tasksauth "github.com/hanzoai/tasks/pkg/auth"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"
)

// The render lane.
const (
	laneOrg   = "studio-render" // the shard; no IAM org maps to it
	laneNS    = "render-jobs"
	laneQueue = "render-jobs"
	laneType  = "render"
	// A render job waits at most cpuStart for a worker, runs at most cpuClose a
	// claim, keeps its claim by heartbeating within cpuLease, and is handed out
	// cpuAttempts times — a worker that dies loses its lease and the next takes it.
	cpuStart    = 2 * time.Hour
	cpuClose    = 30 * time.Minute
	cpuLease    = 90 * time.Second
	cpuAttempts = 3
)

// The GPU lane (the workflow app's, cli/gpu.go's convention).
const (
	gpuNS    = "gpu-jobs"
	gpuType  = "studio.render"
	gpuStart = 7 * 24 * time.Hour
	gpuClose = 4 * time.Hour
	gpuLease = time.Hour
)

// The engine's states for a standalone activity.
const (
	jobStarted   = "ACTIVITY_TASK_STATE_STARTED"
	jobCompleted = "ACTIVITY_TASK_STATE_COMPLETED"
	jobFailed    = "ACTIVITY_TASK_STATE_FAILED"
	jobCanceled  = "ACTIVITY_TASK_STATE_CANCELED"
)

var errNoJob = errors.New("the lane holds no such job")

// job is one job on a lane.
type job struct {
	ID    string
	Queue string
	Input any
}

// task is a job as the lane reports it.
type task struct {
	Status string
	Cause  string
	Result json.RawMessage
}

// lane is a queue of jobs a worker claims.
type lane interface {
	enqueue(ctx context.Context, org string, j job) error
	describe(ctx context.Context, org, id string) (task, error)
	cancel(ctx context.Context, org, id, reason string) error
}

// ── the render lane ──────────────────────────────────────────────────────

// cpuLane is the render lane in this process's engine.
type cpuLane struct{ engine func() *tasks.Embedded }

func (l cpuLane) view() (tasks.View, error) {
	eng := l.engine()
	if eng == nil {
		return tasks.View{}, errors.New("the engine is starting")
	}
	return eng.View(tasks.Org(laneOrg)), nil
}

// laneNamespace registers the render lane's namespace, once per engine.
func laneNamespace(eng *tasks.Embedded) error {
	err := eng.View(tasks.Org(laneOrg)).RegisterNamespace(tasks.Namespace{NamespaceInfo: tasks.NamespaceInfo{Name: laneNS}})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "exist") {
		return err
	}
	return nil
}

// enqueue puts j on the lane once: the job id is the request id, so a repeat finds
// the job the first made and never resets a claim.
func (l cpuLane) enqueue(_ context.Context, _ string, j job) error {
	v, err := l.view()
	if err != nil {
		return err
	}
	if _, ok, err := v.DescribeActivity(laneNS, j.ID, j.ID); err != nil || ok {
		return err
	}
	_, err = v.StartActivity(laneNS, j.ID, j.ID, tasks.TypeRef{Name: laneType}, laneQueue, j.Input,
		&tasks.RetryPolicy{MaximumAttempts: cpuAttempts}, "", cpuStart.String(), cpuClose.String(), cpuLease.String(), "studio", j.ID)
	return err
}

func (l cpuLane) describe(_ context.Context, _ string, id string) (task, error) {
	v, err := l.view()
	if err != nil {
		return task{}, err
	}
	a, ok, err := v.DescribeActivity(laneNS, id, id)
	if err != nil {
		return task{}, err
	}
	if !ok {
		return task{}, errNoJob
	}
	res, _ := json.Marshal(a.Result)
	return task{Status: a.Status, Cause: a.FailureCause, Result: res}, nil
}

func (l cpuLane) cancel(_ context.Context, _ string, id, reason string) error {
	eng := l.engine()
	if eng == nil {
		return errors.New("the engine is starting")
	}
	err := eng.CancelActivityForOrg(laneOrg, laneNS, id, id, reason, "studio")
	if err != nil && (strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "terminal")) {
		return nil
	}
	return err
}

// ── the GPU lane ─────────────────────────────────────────────────────────

// gpuLane is the org's gpu-jobs lane in the workflow app's engine, over the plane.
type gpuLane struct{}

func (gpuLane) enqueue(ctx context.Context, org string, j job) error {
	in, err := json.Marshal(j.Input)
	if err != nil {
		return err
	}
	_, err = workflow.WorkflowEnqueue(cloud.For(ctx, org), &client.EnqueueIn{
		Namespace: gpuNS, ID: j.ID, Type: gpuType, Queue: j.Queue, Input: in,
		Attempts: 1, Heartbeat: gpuLease.String(), Start: gpuStart.String(), Close: gpuClose.String(),
	})
	if status(err) == http.StatusConflict {
		return fatal("no GPU has linked in this org: link one with `hanzo link`")
	}
	return err
}

func (gpuLane) describe(ctx context.Context, org, id string) (task, error) {
	a, err := workflow.WorkflowActivity(cloud.For(ctx, org), &client.ActivityIn{Namespace: gpuNS, ID: id})
	if status(err) == http.StatusNotFound {
		return task{}, errNoJob
	}
	if err != nil {
		return task{}, err
	}
	var row tasks.StandaloneActivity
	if err := json.Unmarshal(a.Row, &row); err != nil {
		return task{}, err
	}
	res, _ := json.Marshal(row.Result)
	return task{Status: row.Status, Cause: row.FailureCause, Result: res}, nil
}

func (gpuLane) cancel(ctx context.Context, org, id, reason string) error {
	_, err := workflow.WorkflowCancel(cloud.For(ctx, org), &client.CancelIn{Namespace: gpuNS, ID: id, Reason: reason})
	switch status(err) {
	case http.StatusNotFound, http.StatusConflict:
		return nil
	}
	return err
}

// status is the HTTP status a plane refusal carried, 0 for anything else.
func status(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// ── a part on a lane ─────────────────────────────────────────────────────

// jobID is one attempt of one part: derived, so a replayed or recovered part
// names the job it already made.
func jobID(p Part) string { return fmt.Sprintf("%s_%s_%d_%d", p.Run, p.Step, p.Item, p.Attempt) }

// lanePart runs a part on its lane: build the job (signing its inputs), put it on
// the lane, follow it to its end, and commit the files it uploaded.
func (s *state) lanePart(ctx context.Context, r runRow, pr partRow, p Part) (result, int64, error) {
	l, queue := lane(s.cpu), laneQueue
	if known[p.Op].executor == gpu {
		l, queue = s.gpu, gpuNS
	}
	id := jobID(p)
	if pr.Job != id {
		input, err := s.jobInput(ctx, r, p)
		if err != nil {
			return result{}, 0, err
		}
		if _, err := s.db.Update(ctx, "parts", tenant.Row{"job": id, "outputs": "[]"}, partKey(p)); err != nil {
			return result{}, 0, err
		}
		if err := l.enqueue(ctx, p.Org, job{ID: id, Queue: queue, Input: input}); err != nil {
			return result{}, 0, err
		}
	}
	started := false
	for {
		cur, ok, err := s.runRow(ctx, p.Run)
		if err == nil && (!ok || cur.Status == canceled) {
			if err := l.cancel(context.WithoutCancel(ctx), p.Org, id, "run cancelled"); err != nil {
				s.log.Debug("studio: cancel a job", "job", id, "err", err)
			}
			return result{}, 0, stop()
		}
		t, err := l.describe(ctx, p.Org, id)
		switch {
		case errors.Is(err, errNoJob):
			return result{}, 0, fatal("the job left the lane before it finished")
		case err != nil:
			s.log.Debug("studio: the lane did not answer; reading again", "job", id, "err", err)
		case t.Status == jobStarted && !started:
			started = true
			s.progress(ctx, p, 0.05, "rendering")
		case t.Status == jobCompleted:
			out, err := s.land(ctx, r, p)
			if err != nil {
				return result{}, 0, err
			}
			cost, err := s.cost(ctx, p, out)
			return out, cost, err
		case t.Status == jobFailed:
			return result{}, 0, fatal("%s", firstLine(or(t.Cause, "the render failed")))
		case t.Status == jobCanceled:
			return result{}, 0, stop()
		}
		select {
		case <-ctx.Done():
			return result{}, 0, ctx.Err()
		case <-time.After(s.poll):
		}
	}
}

// land commits the files a finished job uploaded — the items the door reserved
// for it — as the run's person. The Library stats each object as it commits it,
// so a file the worker says it made and did not is refused there.
func (s *state) land(ctx context.Context, r runRow, p Part) (result, error) {
	pr, ok, err := s.partRow(ctx, p.Run, p.Step, p.Item)
	if err != nil {
		return result{}, err
	}
	var planned []string
	if ok {
		_ = json.Unmarshal([]byte(pr.Outputs), &planned)
	}
	if len(planned) == 0 {
		return result{}, fatal("the job finished and uploaded nothing")
	}
	as := s.as(ctx, r)
	var out result
	for _, id := range planned {
		it, err := s.lib.commit(as, id)
		if err != nil {
			return result{}, fatal("the job's file %s did not commit: %v", id, err)
		}
		out.Assets = append(out.Assets, it.ID)
	}
	return out, nil
}

// progress records a part's progress on its step.
func (s *state) progress(ctx context.Context, p Part, f float64, msg string) {
	if _, err := s.db.Update(ctx, "steps", tenant.Row{"progress": f, "message": msg},
		tenant.And(tenant.Eq("run", p.Run), tenant.Eq("id", p.Step))); err == nil {
		s.emit(ctx, p.Run, "step", map[string]any{"step": p.Step})
	}
}

// ── the door ─────────────────────────────────────────────────────────────

// workers are the subjects that may claim render jobs: the render workload's own
// application, hanzo-render, as the identity boundary spells an application acting
// as itself (app:<owner>/<name>, whichever owner IAM files it under), or what
// STUDIO_RENDER_WORKERS names instead (comma separated). Nobody else reaches the
// lane.
func workers() []string {
	var out []string
	for _, w := range strings.Split(env.Or("STUDIO_RENDER_WORKERS", "app:hanzo/hanzo-render,app:admin/hanzo-render"), ",") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// door serves the render lane to its workers: register the namespace, claim, and
// the three verbs on a claimed job — heartbeat, complete, fail. It is the engine's
// own activity surface, entered in process for the lane's shard; every other path
// is 404 and any other caller 403.
func (s *state) door(c *zip.Ctx) error {
	sub := strings.TrimSpace(c.User())
	if sub == "" {
		return zip.ErrUnauthorized("a render worker signs in with its workload identity")
	}
	if !slices.Contains(workers(), sub) {
		s.log.Info("studio: refused a render claim", "subject", sub)
		return zip.ErrForbidden("this identity is not a render worker")
	}
	rest := strings.Trim(strings.TrimPrefix(c.Path(), prefix+"/render"), "/")
	parts := strings.Split(rest, "/")
	switch {
	case rest == "namespaces" && c.Method() == http.MethodPost:
		return c.JSON(http.StatusOK, map[string]any{"namespaceInfo": map[string]any{"name": laneNS}})
	case len(parts) == 4 && parts[0] == "namespaces" && parts[1] == laneNS && parts[2] == "activities" && parts[3] == "claim":
	case len(parts) == 6 && parts[0] == "namespaces" && parts[1] == laneNS && parts[2] == "activities" &&
		slices.Contains([]string{"heartbeat", "complete", "fail", "output"}, parts[5]):
	default:
		return zip.ErrNotFound("no such render lane operation")
	}
	if c.Method() != http.MethodPost {
		return zip.Errorf(http.StatusMethodNotAllowed, "POST")
	}
	eng := s.work.engine()
	if eng == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "studio is starting")
	}
	body := c.Body()
	if parts[3] == "claim" {
		// A worker claims from the lane's one queue, whatever it asked for.
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if in == nil {
			in = map[string]any{}
		}
		in["taskQueue"] = laneQueue
		body, _ = json.Marshal(in)
	} else if parts[5] == "heartbeat" {
		s.beat(c.Context(), parts[3], body)
	} else if parts[5] == "output" {
		return s.output(c, parts[3], body)
	}
	req := httptest.NewRequestWithContext(
		tasksauth.WithIdentity(context.WithoutCancel(c.Context()), laneOrg, "", sub, ""),
		http.MethodPost, "/v1/tasks/"+rest, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	eng.HTTPHandler().ServeHTTP(rec, req)
	ct := rec.Header().Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.SetHeader("Content-Type", ct)
	c.Status(rec.Code)
	if rec.Code == http.StatusNoContent {
		return nil
	}
	_, err := io.Copy(c.Fiber().Response().BodyWriter(), rec.Body)
	return err
}

// claimed is the part a render job belongs to, read from the job itself.
type claimed struct {
	Org  string `json:"org"`
	Run  string `json:"run"`
	Step string `json:"step"`
	Item int    `json:"item"`
	Op   string `json:"op"`
}

func (s *state) claimed(id string) (claimed, tasks.StandaloneActivity, bool) {
	v, err := cpuLane{engine: s.work.engine}.view()
	if err != nil {
		return claimed{}, tasks.StandaloneActivity{}, false
	}
	a, ok, err := v.DescribeActivity(laneNS, id, id)
	if err != nil || !ok {
		return claimed{}, tasks.StandaloneActivity{}, false
	}
	b, _ := json.Marshal(a.Input)
	var in claimed
	if json.Unmarshal(b, &in) != nil || in.Org == "" || in.Run == "" {
		return claimed{}, *a, false
	}
	return in, *a, true
}

// beat carries a worker's progress onto its job's step.
func (s *state) beat(ctx context.Context, id string, body []byte) {
	var hb struct {
		Details struct {
			Progress float64 `json:"progress"`
			Message  string  `json:"message"`
		} `json:"details"`
	}
	if json.Unmarshal(body, &hb) != nil {
		return
	}
	in, _, ok := s.claimed(id)
	if !ok {
		return
	}
	f := min(max(hb.Details.Progress, 0), 1)
	s.progress(scoped(ctx, in.Org), Part{Run: in.Run, Step: in.Step}, f, firstLine(hb.Details.Message))
}

// output reserves the Library item a claimed job will upload its file to, as the
// run's person, and answers where to PUT it. Only a job that is claimed and still
// running gets one, of the media type its op makes, and at most one.
func (s *state) output(c *zip.Ctx, id string, body []byte) error {
	var want struct {
		Mime   string `json:"mime"`
		Bytes  int64  `json:"bytes"`
		Sha256 string `json:"sha256"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(body, &want); err != nil || want.Bytes <= 0 || want.Mime == "" {
		return zip.ErrBadRequest("output names the file's mime and bytes")
	}
	in, a, ok := s.claimed(id)
	if !ok {
		return zip.ErrNotFound("no such job")
	}
	if a.Status != jobStarted {
		return zip.ErrConflict("the job is not running")
	}
	if want.Bytes > maxRendered {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "a rendered file is at most %d bytes", maxRendered)
	}
	if expect, err := output(Part{Op: in.Op, With: mustWith(a.Input)}); err != nil || want.Mime != expect {
		return zip.Errorf(http.StatusUnprocessableEntity, "this job makes %s", expect)
	}
	ctx := scoped(context.WithoutCancel(c.Context()), in.Org)
	r, ok, err := s.runRow(ctx, in.Run)
	if err != nil {
		return err
	}
	if !ok || r.Status == canceled {
		return zip.ErrConflict("the run was cancelled")
	}
	p := Part{Org: in.Org, Run: in.Run, Step: in.Step, Item: in.Item, Op: in.Op}
	pr, _, err := s.partRow(ctx, p.Run, p.Step, p.Item)
	if err != nil {
		return err
	}
	if pr.Job != id {
		return zip.ErrConflict("the job is not this part's current one")
	}
	var planned []string
	_ = json.Unmarshal([]byte(pr.Outputs), &planned)
	if len(planned) > 0 {
		return zip.ErrConflict("this job already has its upload")
	}
	item, up, err := s.lib.reserve(s.as(ctx, r), libraryIn{
		Kind: libraryKind(want.Mime, p.Op), Name: nameFor(p, 0, want.Mime), Mime: want.Mime,
		Ref: p.Run, Origin: origin(p.Run), Key: itemKey(p, 0),
	}, want.Bytes)
	if err != nil {
		return err
	}
	b, _ := json.Marshal([]string{item.ID})
	if _, err := s.db.Update(ctx, "parts", tenant.Row{"outputs": string(b)}, partKey(p)); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, up)
}

// maxRendered is the largest file a render job may upload.
const maxRendered = 2 << 30

// mustWith reads a job's with.
func mustWith(input any) map[string]any {
	b, _ := json.Marshal(input)
	var in struct {
		With map[string]any `json:"with"`
	}
	_ = json.Unmarshal(b, &in)
	return in.With
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func or(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

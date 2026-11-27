package auto

// job.go — Scheduled: everything that runs without its person watching, in one
// list.
//
// THERE IS ONE SCHEDULER AND THIS IS IT. A schedule is an automation, armed on the
// one durable engine (engine.go); a run of one is a durable workflow there. A
// product whose background work runs on its own durable path — a Studio render,
// a long video — states each run here as it goes (auto_job_track, over the plane),
// so Scheduled lists it beside the automations' runs with the same states and the
// same owner rule. Nothing here starts a second clock.
//
// A JOB IS ITS PERSON'S. An automation's run is the person it was made as
// (SetRunPerson), or, for a run recorded before that, its flow's person; a tracked
// job is the person whose call tracked it, and a later call from anyone else is
// refused rather than allowed to move it. Nobody lists another member's jobs — not
// an org admin — and a job recorded for nobody is listed to nobody.
//
//	✓ GET /v1/auto/jobs        your jobs, newest first, across every product
//	✓ GET /v1/auto/jobs/:id    one
//	  GET /v1/auto/events      the same rows, live, as Server-Sent Events

import (
	"bufio"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// The five states a job is in, whatever its product calls them.
const (
	StateScheduled = "scheduled"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

var (
	jobStates   = []string{StateScheduled, StateRunning, StateSucceeded, StateFailed, StateCancelled}
	jobProducts = []string{"chat", "dev", "studio", "workflow"}
)

const (
	maxJobOutputs = 64
	maxJobText    = 2048
	maxJobRef     = 200
	maxJobOrigin  = 512
)

func oneOf(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// done reports whether a state is an end.
func done(state string) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCancelled
}

// stateOf is a run status in the five words.
func stateOf(s FlowRunStatus) string {
	switch s {
	case RunQueued:
		return StateScheduled
	case RunRunning, RunPaused:
		return StateRunning
	case RunSucceeded:
		return StateSucceeded
	case RunSkipped:
		return StateCancelled
	}
	return StateFailed
}

// jobRow is a tracked job as the table holds it.
type jobRow struct {
	ID, Org, Owner, Product, Kind, Title, Ref, Origin, State string
	Progress                                                 sql.NullFloat64
	Summary, Error, Outputs                                  string
	Cost                                                     sql.NullInt64
	Created, Started, Finished, Updated                      int64
}

const jobCols = `id,org,owner,product,kind,title,ref,origin,state,progress,summary,error,outputs,cost,created,started,finished,updated`

func scanJob(sc interface{ Scan(...any) error }) (jobRow, error) {
	var j jobRow
	err := sc.Scan(&j.ID, &j.Org, &j.Owner, &j.Product, &j.Kind, &j.Title, &j.Ref, &j.Origin, &j.State,
		&j.Progress, &j.Summary, &j.Error, &j.Outputs, &j.Cost, &j.Created, &j.Started, &j.Finished, &j.Updated)
	return j, err
}

// migrateJobs creates the jobs table, inside the store's migration. Every index
// leads with org.
func migrateJobs(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS automations_jobs (
  id        TEXT PRIMARY KEY,
  org       TEXT NOT NULL,
  owner     TEXT NOT NULL,
  product   TEXT NOT NULL,
  kind      TEXT NOT NULL DEFAULT '',
  title     TEXT NOT NULL DEFAULT '',
  ref       TEXT NOT NULL,
  origin    TEXT NOT NULL DEFAULT '',
  state     TEXT NOT NULL,
  progress  DOUBLE PRECISION,
  summary   TEXT NOT NULL DEFAULT '',
  error     TEXT NOT NULL DEFAULT '',
  outputs   TEXT NOT NULL DEFAULT '[]',
  cost      BIGINT,
  created   BIGINT NOT NULL,
  started   BIGINT NOT NULL DEFAULT 0,
  finished  BIGINT NOT NULL DEFAULT 0,
  updated   BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_auto_jobs_ref ON automations_jobs(org, product, ref);
CREATE INDEX IF NOT EXISTS ix_auto_jobs_owner ON automations_jobs(org, owner, updated);
CREATE INDEX IF NOT EXISTS ix_auto_runs_org_updated ON automations_runs(org, updated);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("jobs: %w", err)
	}
	return nil
}

// errNotYours is a track of a job another person holds.
var errNotYours = errors.New("auto: this job is another person's")

// TrackJob writes one job as it stands, keyed by (org, product, ref), and
// answers it. A job is created for the caller and only the caller moves it.
func (s *Store) TrackJob(ctx context.Context, org, owner string, in jobSpec) (jobRow, error) {
	if err := dialect.Text(owner, in.product, in.kind, in.title, in.ref, in.origin, in.state, in.summary, in.err, in.outputs); err != nil {
		return jobRow{}, fmt.Errorf("track job: %w", err)
	}
	now := time.Now().UnixMilli()
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return jobRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM automations_jobs WHERE org=$1 AND product=$2 AND ref=$3`, org, in.product, in.ref))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		j = jobRow{ID: mint.ID("job"), Org: org, Owner: owner, Product: in.product, Ref: in.ref, Created: now}
	case err != nil:
		return jobRow{}, err
	case j.Owner != owner:
		return jobRow{}, errNotYours
	}
	j.Kind, j.Title, j.Origin, j.State = in.kind, in.title, in.origin, in.state
	j.Progress, j.Summary, j.Error, j.Outputs, j.Cost = in.progress, in.summary, in.err, in.outputs, in.cost
	if j.Started == 0 && in.state != StateScheduled {
		j.Started = now
	}
	j.Finished = 0
	if done(in.state) {
		j.Finished = now
	}
	j.Updated = now
	_, err = tx.ExecContext(ctx, `INSERT INTO automations_jobs (`+jobCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,title=excluded.title,origin=excluded.origin,state=excluded.state,
progress=excluded.progress,summary=excluded.summary,error=excluded.error,outputs=excluded.outputs,cost=excluded.cost,
started=excluded.started,finished=excluded.finished,updated=excluded.updated`,
		j.ID, j.Org, j.Owner, j.Product, j.Kind, j.Title, j.Ref, j.Origin, j.State, j.Progress, j.Summary, j.Error,
		j.Outputs, j.Cost, j.Created, j.Started, j.Finished, j.Updated)
	if err != nil {
		return jobRow{}, fmt.Errorf("track job: %w", err)
	}
	return j, tx.Commit()
}

// jobsOf reads one person's tracked jobs, newest first, changed after since.
func (s *Store) jobsOf(ctx context.Context, org, owner string, since int64, limit int) ([]jobRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM automations_jobs
WHERE org=$1 AND owner=$2 AND owner<>'' AND updated>$3 ORDER BY updated DESC LIMIT $4`, org, owner, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []jobRow
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// job reads one of a person's tracked jobs.
func (s *Store) job(ctx context.Context, org, owner, id string) (jobRow, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM automations_jobs
WHERE org=$1 AND id=$2 AND owner=$3 AND owner<>''`, org, id, owner))
	if errors.Is(err, sql.ErrNoRows) {
		return jobRow{}, errNotFound
	}
	return j, err
}

// personRun is a flow's run with what Scheduled shows of its flow.
type personRun struct {
	FlowRun
	automation bool
}

// RunsOf reads the runs one person holds — made as them, or, for a run recorded
// before runs named their person, of a flow they hold — newest first, changed
// after since.
func (s *Store) RunsOf(ctx context.Context, org, sub string, since int64, limit int) ([]personRun, error) {
	if sub == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("r.", runCols)+`, f.automation FROM automations_runs r
JOIN automations_flows f ON f.org=r.org AND f.id=r.flow_id
WHERE r.org=$1 AND r.updated>$2 AND (r.person=$3 OR (r.person='' AND f.owner_sub=$4))
ORDER BY r.updated DESC LIMIT $5`, org, since, sub, sub, limit)
	if err != nil {
		return nil, fmt.Errorf("runs of: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []personRun
	for rows.Next() {
		var p personRun
		var auto int
		r := &p.FlowRun
		if err := rows.Scan(&r.ID, &r.Org, &r.FlowID, &r.FlowVersionID, &r.WorkflowID, &r.Status, &r.StartTime,
			&r.FinishTime, &r.Created, &r.Updated, &r.Session, &r.Summary, &r.Person, &r.Email, &r.Actor, &r.Draft,
			&r.Posts, &r.Outputs, &auto); err != nil {
			return nil, err
		}
		p.automation = auto == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// prefixed qualifies each of a column list's names.
func prefixed(p, cols string) string {
	return p + strings.ReplaceAll(cols, ",", ","+p)
}

// holds reports whether sub holds run r: made as them, or of a flow they hold.
func holds(s *cloud.Service[state], ctx context.Context, org, sub string, r FlowRun) bool {
	if sub == "" {
		return false
	}
	if r.Person != "" {
		return r.Person == sub
	}
	who, err := s.State.store.FlowOwner(ctx, org, r.FlowID)
	return err == nil && who.Sub == sub
}

// held loads flow id of org for the person who holds it, and answers anyone
// else — another member, an org admin — as though it did not exist.
func held(s *cloud.Service[state], ctx context.Context, org, id string) (Flow, error) {
	f, err := s.State.store.GetFlow(ctx, org, id)
	if err != nil {
		return Flow{}, mapStoreErr(err, "flow not found")
	}
	who, err := s.State.store.FlowOwner(ctx, org, f.ID)
	if err != nil || manages(ctx, who) != nil {
		return Flow{}, zip.ErrNotFound("flow not found")
	}
	return f, nil
}

// heldRun loads run id of org for the person who holds it, and answers anyone
// else as though it did not exist.
func heldRun(s *cloud.Service[state], ctx context.Context, org, id string) (FlowRun, error) {
	r, err := s.State.store.GetRun(ctx, org, id)
	if err != nil {
		return FlowRun{}, mapStoreErr(err, "run not found")
	}
	sub, _ := principal.SubjectFrom(ctx)
	if !holds(s, ctx, org, strings.TrimSpace(sub), r) {
		return FlowRun{}, zip.ErrNotFound("run not found")
	}
	return r, nil
}

// ── the wire ──────────────────────────────────────────────────────────────────

// jobSpec is a validated JobTrackIn.
type jobSpec struct {
	product, kind, title, ref, origin, state, summary, err, outputs string
	progress                                                        sql.NullFloat64
	cost                                                            sql.NullInt64
}

// printable holds s to printable text of at most n bytes.
func printable(s string, n int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, len(s) <= n && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}

func checkJob(in *client.JobTrackIn) (jobSpec, error) {
	var sp jobSpec
	var ok bool
	if sp.product = strings.ToLower(strings.TrimSpace(in.Product)); !oneOf(jobProducts, sp.product) {
		return sp, invalid("product must be chat, dev, studio or workflow")
	}
	if sp.state = strings.ToLower(strings.TrimSpace(in.State)); !oneOf(jobStates, sp.state) {
		return sp, invalid("state must be scheduled, running, succeeded, failed or cancelled")
	}
	if sp.ref, ok = printable(in.Ref, maxJobRef); !ok || sp.ref == "" || strings.ContainsAny(sp.ref, "\n\t") {
		return sp, invalid("ref is one line of at most 200 bytes")
	}
	if sp.kind, ok = printable(in.Kind, 64); !ok || strings.ContainsAny(sp.kind, "\n\t") {
		return sp, invalid("kind is one word")
	}
	if sp.title, ok = printable(in.Title, maxName*4); !ok || strings.ContainsAny(sp.title, "\n\t") {
		return sp, invalid("title is one line")
	}
	sp.origin = strings.TrimSpace(in.Origin)
	if len(sp.origin) > maxJobOrigin || (sp.origin != "" && (!strings.HasPrefix(sp.origin, "/") || strings.HasPrefix(sp.origin, "//") || strings.ContainsAny(sp.origin, "\\\n\t "))) {
		return sp, invalid("origin is a path on this site: it starts with one /")
	}
	if sp.summary, ok = printable(in.Summary, maxJobText); !ok {
		return sp, invalid("summary is at most 2 KiB of text")
	}
	if sp.err, ok = printable(in.Error, maxJobText); !ok {
		return sp, invalid("error is at most 2 KiB of text")
	}
	if p := in.Progress; p != nil {
		if math.IsNaN(*p) || *p < 0 || *p > 1 {
			return sp, invalid("progress is between 0 and 1")
		}
		sp.progress = sql.NullFloat64{Float64: *p, Valid: true}
	}
	if c := in.Cost; c != nil {
		if *c < 0 {
			return sp, invalid("cost is not negative")
		}
		sp.cost = sql.NullInt64{Int64: *c, Valid: true}
	}
	if len(in.Outputs) > maxJobOutputs {
		return sp, invalid("at most 64 outputs")
	}
	outs := make([]string, 0, len(in.Outputs))
	for _, id := range in.Outputs {
		if !strings.HasPrefix(id, "lib_") || len(id) > 64 || strings.ContainsFunc(id, unicode.IsSpace) {
			return sp, invalid("an output is a Library item id")
		}
		outs = append(outs, id)
	}
	b, _ := json.Marshal(outs)
	sp.outputs = string(b)
	return sp, nil
}

// presentJob is a tracked job on the wire.
func presentJob(j jobRow) client.Job {
	out := client.Job{
		ID: j.ID, Product: j.Product, Kind: j.Kind, Title: j.Title, State: j.State, Status: j.State,
		Ref: j.Ref, Origin: j.Origin, Summary: j.Summary, Error: j.Error, Outputs: []string{},
		Started: stamp(cmpOr(j.Started, j.Created)), Updated: stamp(j.Updated),
	}
	if j.Progress.Valid {
		p := j.Progress.Float64
		out.Progress = &p
	}
	if j.Cost.Valid {
		c := j.Cost.Int64
		out.Cost = &c
	}
	if j.Finished > 0 {
		f := stamp(j.Finished)
		out.Finished = &f
	}
	_ = json.Unmarshal([]byte(j.Outputs), &out.Outputs)
	return out
}

// presentFlowRun is an automation's or a workflow's run as a job.
func (o ops) presentFlowRun(ctx context.Context, org string, p personRun) client.Job {
	r := p.FlowRun
	out := client.Job{
		ID: r.ID, Product: "workflow", Kind: "flow", State: stateOf(r.Status), Status: wireStatus(r.Status),
		Ref: r.FlowID, Origin: "/dev/-/automations/" + r.FlowID, Summary: r.Summary, Outputs: []string{},
		Started: stamp(cmpOr(r.StartTime, r.Created)), Updated: stamp(r.Updated),
	}
	if p.automation {
		id := r.FlowID
		out.Kind, out.Automation = "automation", &id
		if v, err := o.s.State.store.LatestVersion(ctx, org, r.FlowID); err == nil {
			if d, ok := definitionOf(&v); ok {
				out.Title, out.Product = d.Name, d.Source
			}
		}
	}
	if out.Title == "" {
		out.Title = "Workflow run"
	}
	if r.Session != "" {
		s := r.Session
		out.Session = &s
	}
	if out.State == StateFailed {
		out.Error = r.Summary
	}
	if terminal(r.Status) && r.FinishTime > 0 {
		f := stamp(r.FinishTime)
		out.Finished = &f
	}
	_ = json.Unmarshal([]byte(cmpOrStr(r.Outputs, "[]")), &out.Outputs)
	return out
}

func cmpOr(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// trackJob is the plane op: a product states one of its background runs.
func (o ops) trackJob(ctx context.Context, in *client.JobTrackIn) (*client.Job, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("auto: no org on the call")
	}
	sub, keyed := principal.SubjectFrom(ctx)
	if sub = strings.TrimSpace(sub); !keyed || sub == "" || strings.HasPrefix(sub, "app:") {
		return nil, zip.ErrForbidden("auto: a job is a person's, and this call names no person")
	}
	sp, err := checkJob(in)
	if err != nil {
		return nil, err
	}
	j, err := o.s.State.store.TrackJob(ctx, org, sub, sp)
	if errors.Is(err, errNotYours) {
		return nil, zip.ErrForbidden("auto: this job is another person's")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "auto: the job was not recorded: %v", err)
	}
	out := presentJob(j)
	return &out, nil
}

// exposeJobs publishes auto_job_track.
func exposeJobs(o ops) {
	cloud.Plane().Post("/auto/job/track", o.trackJob,
		zip.WithOperationID(client.AutoJobTrack),
		zip.WithSummary("Record one run of a product's background work so Scheduled lists it"))
}

// jobQuery narrows a job list.
type jobQuery struct {
	// Product keeps one product's jobs: chat, dev, studio or workflow.
	Product string `json:"product"`
	// State keeps one state: scheduled, running, succeeded, failed or cancelled.
	State string `json:"state"`
	// Limit bounds the page (default 100, at most 500).
	Limit int `json:"limit"`
}

type jobPage struct {
	// Data are your jobs, the most recently changed first.
	Data []client.Job `json:"data"`
}

// me is the caller as the person a job is held by.
func me(ctx context.Context) (org, sub string, err error) {
	org, err = principal.Acting(ctx)
	if err != nil {
		return "", "", err
	}
	sub, keyed := principal.SubjectFrom(ctx)
	if sub = strings.TrimSpace(sub); !keyed || sub == "" {
		return "", "", zip.ErrForbidden("auto: this credential names no person")
	}
	return org, sub, nil
}

// timed is a job and the stored moment it last changed, in unix milliseconds:
// the wire's RFC 3339 is whole seconds, too coarse to resume a stream from.
type timed struct {
	job client.Job
	at  int64
}

// jobs reads a person's jobs of both kinds changed after since, merged with the
// most recently changed first.
func (o ops) jobs(ctx context.Context, org, sub string, since int64, limit int) ([]timed, error) {
	runs, err := o.s.State.store.RunsOf(ctx, org, sub, since, limit)
	if err != nil {
		return nil, err
	}
	tracked, err := o.s.State.store.jobsOf(ctx, org, sub, since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]timed, 0, len(runs)+len(tracked))
	for _, r := range runs {
		out = append(out, timed{o.presentFlowRun(ctx, org, r), r.Updated})
	}
	for _, j := range tracked {
		out = append(out, timed{presentJob(j), j.Updated})
	}
	slices.SortStableFunc(out, func(a, b timed) int { return cmp.Compare(b.at, a.at) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListJobs lists everything of yours that runs without you watching, across
// every product: each run of your automations and workflows, and every Studio
// render, long video or other background run a product recorded for you. Each
// is in one of five states — scheduled, running, succeeded, failed, cancelled —
// with what it made (Library item ids) and what it cost when that is known.
// Another member's jobs are never listed.
//
// Example: {"product": "studio", "state": "running"}
func (o ops) listJobs(ctx context.Context, in *jobQuery) (*jobPage, error) {
	org, sub, err := me(ctx)
	if err != nil {
		return nil, err
	}
	product := strings.ToLower(strings.TrimSpace(in.Product))
	if product != "" && !oneOf(jobProducts, product) {
		return nil, invalid("product must be chat, dev, studio or workflow")
	}
	st := strings.ToLower(strings.TrimSpace(in.State))
	if st != "" && !oneOf(jobStates, st) {
		return nil, invalid("state must be scheduled, running, succeeded, failed or cancelled")
	}
	limit := 100
	if in.Limit > 0 {
		limit = min(in.Limit, 500)
	}
	all, err := o.jobs(ctx, org, sub, 0, 1000)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "auto: jobs could not be read: %v", err)
	}
	out := &jobPage{Data: []client.Job{}}
	for _, t := range all {
		j := t.job
		if (product == "" || j.Product == product) && (st == "" || j.State == st) {
			out.Data = append(out.Data, j)
			if len(out.Data) == limit {
				break
			}
		}
	}
	return out, nil
}

// jobRef names one job.
type jobRef struct {
	// ID is the job, from the path: a run_ or a job_ id.
	ID string `json:"id"`
}

// GetJob reads one of your jobs. A job that is not yours answers 404, as one that
// does not exist.
//
// Example: {"id": "run_3f7a…"}
func (o ops) getJob(ctx context.Context, in *jobRef) (*client.Job, error) {
	org, sub, err := me(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if strings.HasPrefix(id, "job_") {
		j, err := o.s.State.store.job(ctx, org, sub, id)
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("job not found")
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "auto: the job could not be read: %v", err)
		}
		out := presentJob(j)
		return &out, nil
	}
	r, err := o.s.State.store.GetRun(ctx, org, id)
	if err != nil || !holds(o.s, ctx, org, sub, r) {
		return nil, zip.ErrNotFound("job not found")
	}
	f, _ := o.s.State.store.GetFlow(ctx, org, r.FlowID)
	v, verr := o.s.State.store.LatestVersion(ctx, org, f.ID)
	out := o.presentFlowRun(ctx, org, personRun{FlowRun: r, automation: verr == nil && isAutomation(&v)})
	return &out, nil
}

// The live stream's pace: how often it looks for changes, how often it says it
// is still there, and how long one stream is held before its client reconnects —
// which is when the credential it opened with is asked about again.
var (
	eventsPoll  = 2 * time.Second
	eventsBeat  = 15 * time.Second
	eventsSpan  = 15 * time.Minute
	eventsBatch = 200
)

func init() {
	spec.Register("/v1/auto/events", http.MethodGet, nil, spec.Bytes{Type: "text/event-stream"})
	spec.Describe("/v1/auto/events", http.MethodGet,
		"Stream your jobs and automations as they change, as Server-Sent Events",
		"Holds one text/event-stream open and writes a frame each time one of your jobs or automations "+
			"changes: `event: job` with the job as GET /v1/auto/jobs/{id} answers it, and `event: automation` "+
			"with the automation as GET /v1/auto/automations/{id} answers it. Only your own rows are sent; "+
			"another member's never are. Authenticate with `Authorization: Bearer` — a fetch-based reader, "+
			"since a browser EventSource cannot set it. A `: ping` comment is written every 15 seconds, and "+
			"the server closes the stream after 15 minutes; reconnect and re-read the lists. Frames carry no "+
			"`id`: the lists are the truth, and the stream is a live hint over them.")
}

// events streams the caller's jobs and automations as they change. UNTYPED: the
// reply is a stream that outlives the handler (SendStreamWriter), and a typed op
// answers one value.
func (o ops) events(c *zip.Ctx) error {
	ctx := principal.WithSubject(principal.WithValidated(principal.WithOrg(c.Forward(), c), c), c)
	org, sub, err := me(ctx)
	if err != nil {
		return err
	}
	// The stream runs after this handler returns, so it holds what it needs and
	// never touches c. Its rows are the store's, read as this person.
	ctx = context.WithoutCancel(ctx)
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	return c.SendStreamWriter(func(w *bufio.Writer) {
		o.pump(ctx, w, org, sub, time.Now().Add(eventsSpan))
	})
}

// pump writes frames until the span ends or a write fails. It starts from now:
// what changed before the stream opened is what the lists already answered.
func (o ops) pump(ctx context.Context, w *bufio.Writer, org, sub string, until time.Time) {
	since := time.Now().UnixMilli()
	beat := time.Now()
	autos := map[string]string{}
	if _, err := w.WriteString(": open\n\n"); err != nil || w.Flush() != nil {
		return
	}
	for time.Now().Before(until) {
		time.Sleep(eventsPoll)
		js, err := o.jobs(ctx, org, sub, since, eventsBatch)
		if err != nil {
			continue
		}
		for i := len(js) - 1; i >= 0; i-- {
			j := js[i].job
			if !frame(w, "job", j) {
				return
			}
			since = max(since, js[i].at)
			if j.Automation != nil {
				autos[*j.Automation] = ""
			}
		}
		for id := range autos {
			if a, err := o.getAutomation(ctx, &automationRef{ID: id}); err == nil {
				if !frame(w, "automation", a) {
					return
				}
			}
			delete(autos, id)
		}
		if time.Since(beat) >= eventsBeat {
			if _, err := w.WriteString(": ping\n\n"); err != nil || w.Flush() != nil {
				return
			}
			beat = time.Now()
		}
	}
}

// frame writes one event; false when the client is gone.
func frame(w *bufio.Writer, event string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return false
	}
	return w.Flush() == nil
}

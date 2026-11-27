package train

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hanzoai/cloud/sdk"
)

// Job is a job as read: what was asked, where it is, and what it produced.
type Job struct {
	Spec
	// ID names the job.
	ID string `json:"id"`
	// Project is the creator's project scope.
	Project string `json:"project"`
	// Status is queued, running, evaluating, succeeded, rejected, failed,
	// cancelled or stopped.
	Status string `json:"status"`
	// Error says why a job failed or stopped. A job stopped at what its payer or
	// its own bounds could hold says budget, max_seconds, insufficient_balance,
	// spend_cap, balance_unavailable or no_payer.
	Error string `json:"error,omitempty"`
	// Usage is what its tasks used and what that cost.
	Usage Usage `json:"usage"`
	// CreatedBy is the principal that created it.
	CreatedBy string `json:"created_by"`
	// Created, Started and Ended are unix seconds; 0 is not yet.
	Created int64 `json:"created"`
	Started int64 `json:"started"`
	Ended   int64 `json:"ended"`
	// Tasks are its units of execution.
	Tasks []TaskView `json:"tasks"`
	// Artifacts are its outputs.
	Artifacts []artifact `json:"artifacts"`
	// Result is the lead's evaluation and verdict, once reported.
	Result json.RawMessage `json:"result,omitempty"`
	// Published is the publish record, once published.
	Published json.RawMessage `json:"published,omitempty"`
}

// Usage is a job's compute: the device-seconds its reports metered, what is
// held for it beyond them while it runs, and what they cost.
type Usage struct {
	// Seconds are the device-seconds used.
	Seconds int64 `json:"seconds"`
	// Held are the device-seconds held beyond Seconds; 0 once it ended.
	Held int64 `json:"held"`
	// CostMicroUSD is what Seconds were charged, in micro-USD: 0 on the org's own machines.
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// TaskView is a task as a job read shows it; its lease never leaves the store.
type TaskView struct {
	// ID names the task.
	ID string `json:"id"`
	// Role is lead or join.
	Role string `json:"role"`
	// Machine is the machine it was placed on, or empty for any.
	Machine string `json:"machine,omitempty"`
	// Status is open, claimed, done or lost.
	Status string `json:"status"`
	// Host is the machine that claimed it.
	Host string `json:"host,omitempty"`
	// Seen is its last report, unix seconds.
	Seen int64 `json:"seen,omitempty"`
	// Device is the accelerator class it runs on — gpu or cpu — and Devices how many.
	Device  string `json:"device,omitempty"`
	Devices int64  `json:"devices,omitempty"`
	// Seconds are the device-seconds its reports metered, and Held those held for it.
	Seconds int64 `json:"seconds"`
	Held    int64 `json:"held"`
}

func (o ops) view(ctx context.Context, st *store, r row) (*Job, error) {
	j := &Job{ID: r.ID, Project: r.Project, Status: r.Status, Error: r.Error, CreatedBy: r.By,
		Created: r.Created, Started: r.Started, Ended: r.Ended, Tasks: []TaskView{}, Artifacts: []artifact{}}
	if err := json.Unmarshal([]byte(r.Spec), &j.Spec); err != nil {
		return nil, err
	}
	if r.Result != "" {
		j.Result = json.RawMessage(r.Result)
	}
	if r.Published != "" {
		j.Published = json.RawMessage(r.Published)
	}
	ts, err := st.tasks(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		j.Tasks = append(j.Tasks, TaskView{ID: t.ID, Role: t.Role, Machine: t.Machine, Status: t.Status, Host: t.Host, Seen: t.Seen,
			Device: t.Class, Devices: t.Devices, Seconds: t.Used, Held: t.Held})
		j.Usage.Seconds += t.Used
		j.Usage.CostMicroUSD += cost(t.Rate, t.Used)
		if t.Status == claimed {
			j.Usage.Held += t.Held - t.Used
		}
	}
	as, err := st.artifacts(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if as != nil {
		j.Artifacts = as
	}
	return j, nil
}

// load resolves a job id in the caller's org, or the one not-found every other
// org's id also gets.
func (o ops) load(ctx context.Context, id string) (*store, string, row, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, "", row{}, err
	}
	r, err := st.job(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil, "", row{}, missing("job")
	}
	if err != nil {
		return nil, "", row{}, o.fault("job read", org, err)
	}
	return st, org, r, nil
}

// CreateTrainJob creates a job that teaches base_model one capability: the data,
// the objective, how the trained parameters are represented (adaptation), what they
// must not damage and by how much (protect), where it runs and how far
// (resources), the suites it is judged on (evaluation) and what it produces
// (output). It is validated in one order — shape, base, the agreement of the
// artifacts it names, then each choice against what that base runs today — and
// the first failure is the answer: a 400 problem carrying `code`, and `supported`
// when a choice is not run for that base. A dataset the org uploaded
// (`artifact:<sha256>`) is read back here if it has not been. A job that names
// the org's machines is queued for them; one that names none is queued for
// Hanzo's executor, charged to the caller's wallet by the device-second inside its
// budget, which it must name, and is refused 402 when that wallet cannot hold its
// first window on its own. An org has at most 64 jobs that have not ended (409
// job_limit). auto is resolved here and `adaptation.chose` says to what and why.
//
// Example: {"base_model": "kai", "revision": "a7", "dataset": {"uri": "stage:a11"}, "adaptation": {"mode": "full"}, "protect": {"suites": ["typed_decisions"], "projection": {"strength": 0.5}, "distillation": true, "budget": {"accuracy": 0.02, "ece": 0.03}}, "resources": {"machines": ["dgx"], "steps": 2000}, "evaluation": {"suites": ["labels"]}, "output": {"kind": "capability", "name": "open-labels"}}
func (o ops) createJob(ctx context.Context, in *Spec) (*Job, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if in.Dataset != nil {
		if m := uploaded.FindStringSubmatch(in.Dataset.URI); m != nil {
			if err := o.landed(ctx, st, org, m[1]); err != nil {
				return nil, err
			}
		}
	}
	if err := in.validate(ctx, "jobs", st); err != nil {
		return nil, err
	}
	live, err := st.live(ctx)
	if err != nil {
		return nil, o.fault("job read", org, err)
	}
	if live >= maxLive {
		return nil, conflict("job_limit", fmt.Sprintf("the org has %d jobs that have not ended, and at most %d may be; cancel one first", live, maxLive))
	}
	p := cloud.PayerOf(ctx)
	scope := project(ctx)
	// The first window alone is weighed: while the payer's other work holds its
	// money the job waits for a claim, and it is refused only when the wallet could
	// not run it at all.
	if hanzo(*in) {
		if !p.Billable() {
			return nil, cloud.Denied(cloud.ErrNoLedger)
		}
		if err := o.s.State.afford(ctx, p.Wallet, scope, p.Validated && p.Project == scope, first(ctx, *in)); err != nil {
			return nil, cloud.Denied(err)
		}
	}
	r, err := st.create(ctx, org, scope, caller(ctx), p.Wallet.String(), p.Validated && p.Project == scope, in, unix())
	if err != nil {
		return nil, o.fault("job create", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.s.State.bell.ring(org)
	if hanzo(*in) {
		o.s.State.bell.ring(platform)
	}
	o.s.Log.Info("train job queued", "org", org, "job", r.ID, "base", r.Base, "hanzo", hanzo(*in))
	return o.view(ctx, st, r)
}

// maxLive bounds the jobs an org has that have not ended.
const maxLive = 64

// landed reads back a dataset the org uploaded and has not yet been confirmed:
// those bytes, sealed under the org's key, or the upload's refusal. An upload
// already stored, and a sha the org never uploaded, pass; validation answers the
// second.
func (o ops) landed(ctx context.Context, st *store, org, sum string) error {
	a, err := st.artifact(ctx, "", sum)
	if errors.Is(err, errNotFound) || (err == nil && a.State != "pending") {
		return nil
	}
	if err != nil {
		return o.fault("artifact read", org, err)
	}
	if err := o.stored(ctx, st, org, "", sum); err != nil {
		return err
	}
	return o.ship(org)
}

type listIn struct {
	// Status narrows to one status.
	Status string `json:"status"`
	// Limit bounds the answer; default 50, at most 500.
	Limit int `json:"limit"`
}

type jobsOut struct {
	// Data is the org's jobs, newest first.
	Data []*Job `json:"data"`
}

// ListTrainJobs answers the org's jobs, newest first, narrowed by ?status=.
func (o ops) listJobs(ctx context.Context, in *listIn) (*jobsOut, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	rs, err := st.jobs(ctx, in.Status, min(limit, 500))
	if err != nil {
		return nil, o.fault("job list", org, err)
	}
	out := &jobsOut{Data: []*Job{}}
	for _, r := range rs {
		j, err := o.view(ctx, st, r)
		if err != nil {
			return nil, o.fault("job read", org, err)
		}
		out.Data = append(out.Data, j)
	}
	return out, nil
}

type idIn struct {
	// ID names the job.
	ID string `json:"id"`
}

// GetTrainJob answers one job: what it asked for, its status and tasks, its
// artifacts, its result and verdict, and whether it was published. A job of
// another org is not found, exactly as an unknown id is.
func (o ops) getJob(ctx context.Context, in *idIn) (*Job, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	j, err := o.view(ctx, st, r)
	if err != nil {
		return nil, o.fault("job read", org, err)
	}
	return j, nil
}

// CancelTrainJob stops a job that has not ended. Its executors learn it on their
// next report and stop their trainers; a job that already ended is a 409.
func (o ops) cancel(ctx context.Context, in *idIn) (*Job, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := st.end(ctx, r.ID, cancelled, "cancelled by "+caller(ctx), unix()); errors.Is(err, errTerminal) {
		return nil, conflict("terminal", "job already "+r.Status)
	} else if err != nil {
		return nil, o.fault("job cancel", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.s.State.bell.ring(org)
	if r, err = st.job(ctx, r.ID); err != nil {
		return nil, o.fault("job read", org, err)
	}
	return o.view(ctx, st, r)
}

type eventsIn struct {
	// ID names the job.
	ID string `json:"id"`
	// After is the last seq the caller holds; 0 reads from the start.
	After int64 `json:"after"`
	// Wait is how many seconds to hold the request for a first new event, at most 25.
	Wait int `json:"wait"`
}

type eventsOut struct {
	// Data is the events after the cursor, in order.
	Data []event `json:"data"`
	// Next is the cursor to pass as after.
	Next int64 `json:"next"`
	// Status is the job's status as of this answer.
	Status string `json:"status"`
}

// ListTrainJobEvents answers a job's events after a cursor — status changes, the
// coordinator's address, metrics, log lines, artifacts and the result — waiting up
// to `wait` seconds for the first new one, so a caller following a run holds one
// request at a time.
func (o ops) listEvents(ctx context.Context, in *eventsIn) (*eventsOut, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out := &eventsOut{Data: []event{}, Next: in.After, Status: r.Status}
	wait := time.Duration(min(max(in.Wait, 0), 25)) * time.Second
	err = o.poll(ctx, org, wait, func() (bool, error) {
		es, err := st.events(ctx, r.ID, in.After, 1000)
		if err != nil {
			return false, err
		}
		if len(es) > 0 {
			out.Data = es
			out.Next = es[len(es)-1].Seq
		}
		return len(es) > 0 || terminal(r.Status), nil
	})
	if err != nil {
		return nil, o.fault("event read", org, err)
	}
	if r, err = st.job(ctx, r.ID); err == nil {
		out.Status = r.Status
	}
	return out, nil
}

type series struct {
	// Name is the metric.
	Name string `json:"name"`
	// Points are [step, value, at] in step order.
	Points [][3]float64 `json:"points"`
}

type metricsOut struct {
	// Data is one series per metric name.
	Data []series `json:"data"`
}

// ListTrainJobMetrics answers a job's metric series — loss, throughput and each
// validation suite's numbers as the executor reported them — one per name, each
// point [step, value, unix seconds].
func (o ops) metrics(ctx context.Context, in *idIn) (*metricsOut, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	m, err := st.series(ctx, r.ID)
	if err != nil {
		return nil, o.fault("metric read", org, err)
	}
	out := &metricsOut{Data: []series{}}
	for n, ps := range m {
		out.Data = append(out.Data, series{Name: n, Points: ps})
	}
	slices.SortFunc(out.Data, func(a, b series) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out, nil
}

// Download is an artifact with a short-lived address to fetch it.
type Download struct {
	artifact
	// URL fetches the bytes until Expires; absent until the artifact is stored.
	URL string `json:"url,omitempty"`
	// Expires is when URL stops working, unix seconds.
	Expires int64 `json:"expires,omitempty"`
}

type artifactsOut struct {
	// Data is the job's artifacts.
	Data []Download `json:"data"`
}

// ListTrainJobArtifacts answers a job's artifacts — checkpoint, capability, lora,
// basis or merged, each named by the SHA-256 of its bytes — with a download
// address that lives ten minutes for each one stored. One deleted, by the org or
// by retention, reads deleted and has none.
func (o ops) listArtifacts(ctx context.Context, in *idIn) (*artifactsOut, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	as, err := st.artifacts(ctx, r.ID)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	out := &artifactsOut{Data: []Download{}}
	for _, a := range as {
		d := Download{artifact: a}
		if a.State == "stored" {
			if d.URL, d.Expires, err = o.download(ctx, st, org, a.SHA256); err != nil {
				return nil, o.fault("artifact link", org, err)
			}
		}
		out.Data = append(out.Data, d)
	}
	return out, nil
}

// key is an artifact's object: the org's prefix and the bytes' sha256.
func key(org, sum string) string { return fmt.Sprintf("%s/%s", org, sum) }

// keyOf is where an org's stored object of that sha256 was written.
func (o ops) keyOf(ctx context.Context, st *store, org, sum string) (string, error) {
	ob, err := st.object(ctx, sum)
	switch {
	case err == nil && ob.Key != "":
		return ob.Key, nil
	case err != nil && !errors.Is(err, errNotFound):
		return "", err
	}
	return key(org, sum), nil
}

// download presigns a stored object of the org's, at the key it was written to.
func (o ops) download(ctx context.Context, st *store, org, sum string) (string, int64, error) {
	k, err := o.keyOf(ctx, st, org, sum)
	if err != nil {
		return "", 0, err
	}
	u, err := o.s.State.objects.link(ctx, k)
	if err != nil {
		return "", 0, err
	}
	return u, time.Now().Add(downloadTTL).Unix(), nil
}

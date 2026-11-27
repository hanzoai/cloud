package train

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// Supports is what an executor's build runs, so a claim is answered only with
// a task it can run.
type Supports struct {
	// Adaptations are the modes it runs.
	Adaptations []string `json:"adaptations"`
	// Protect are the protections it runs: projection, distillation, capabilities.
	Protect []string `json:"protect"`
	// Terms are the objective terms it runs.
	Terms []string `json:"terms"`
	// Outputs are the output kinds it produces.
	Outputs []string `json:"outputs"`
}

type claimIn struct {
	// Machine is the machine claiming, by name: one of the org's linked machines,
	// or for a platform claim the Hanzo host.
	Machine string `json:"machine"`
	// Platform claims for Hanzo's executor: a task of any org's job that names no
	// machines. A SuperAdmin's claim alone, and on the platform's audit trail.
	Platform bool `json:"platform"`
	// Bases are the base models its trainer runs.
	Bases []string `json:"bases"`
	// Devices are its accelerators, one entry per device: cuda, rocm, metal,
	// vulkan, cpu. A task runs on those of one kind its job allows.
	Devices []string `json:"devices"`
	// Supports is what its build runs.
	Supports Supports `json:"supports"`
}

// Assignment is a claimed task: its lease, returned once, and its job.
type Assignment struct {
	// Org is the job's org. A platform executor reports for the task acting in it.
	Org string `json:"org"`
	// Task is the unit this executor now holds.
	Task Claimed `json:"task"`
	// Job is the job it belongs to.
	Job *Job `json:"job"`
}

// Claimed is a task as its executor sees it.
type Claimed struct {
	// ID names the task.
	ID string `json:"id"`
	// Job names its job.
	Job string `json:"job"`
	// Role is lead or join.
	Role string `json:"role"`
	// Lease authenticates every report for the task; it is answered once.
	Lease string `json:"lease"`
	// Coordinator is where a join task reaches its lead.
	Coordinator string `json:"coordinator,omitempty"`
	// Device is the accelerator kind it runs on, and Devices how many of them.
	Device  string `json:"device"`
	Devices int64  `json:"devices"`
	// Held is the device-seconds held for it. The cloud meters the task by the
	// clock and stops its job before it runs past them.
	Held int64 `json:"held"`
	// Report is the most seconds that may pass between its reports.
	Report int64 `json:"report"`
}

// runs reports whether an executor supporting sup can run spec.
func runs(sup Supports, bases []string, spec Spec) bool {
	if !slices.Contains(bases, spec.BaseModel) || !slices.Contains(sup.Adaptations, spec.Adaptation.Mode) || !slices.Contains(sup.Outputs, spec.Output.Kind) {
		return false
	}
	if p := spec.Protect; p != nil {
		for _, m := range p.parts() {
			if !slices.Contains(sup.Protect, m) {
				return false
			}
		}
	}
	for _, t := range spec.Objective.Terms {
		if !slices.Contains(sup.Terms, t.Kind) {
			return false
		}
	}
	return true
}

// ClaimTrainTask is an executor's long poll for work: it names the machine it
// runs on, its devices and what its build runs, and is answered, within 25
// seconds, one task it can run — a job's lead first; a join once its lead has
// said where it coordinates — or 204. The answer carries the task's lease, once,
// which every report for the task must carry; the device-seconds held for it;
// and how often it must report. An executor acting as a principal of an org
// claims that org's tasks placed on its machine and can claim no other org's. A
// platform claim (SuperAdmin alone) is Hanzo's executor: it is answered a task of
// any org's job that names no machines, its first window held against the job's
// payer first, and the claim is put on the platform's audit trail.
//
// Example: {"machine": "dgx", "bases": ["kai"], "devices": ["cuda"], "supports": {"adaptations": ["full", "readout"], "protect": ["projection", "distillation"], "terms": ["pairwise_margin", "hard_margin", "consistency"], "outputs": ["capability", "checkpoint"]}}
func (o ops) claim(ctx context.Context, in *claimIn) (*Assignment, error) {
	if !name.MatchString(in.Machine) {
		return nil, invalid("machine: %q is not a name", in.Machine)
	}
	if len(in.Devices) == 0 || len(in.Devices) > 64 {
		return nil, invalid("devices: one entry per device, 1 to 64 of %s", strings.Join(devices, ", "))
	}
	for _, d := range in.Devices {
		if !slices.Contains(devices, d) {
			return nil, invalid("devices: %q is none of %s", d, strings.Join(devices, ", "))
		}
	}
	if in.Platform {
		if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
			return nil, cloud.Super.Refusal()
		}
		return o.claimPlatform(ctx, in)
	}
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	var got *Assignment
	err = o.poll(ctx, org, claimWait, func() (bool, error) {
		ts, err := st.candidates(ctx, in.Machine)
		if err != nil {
			return false, err
		}
		for _, t := range ts {
			r, err := st.job(ctx, t.Job)
			if err != nil {
				return false, err
			}
			var spec Spec
			if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
				return false, err
			}
			device, n := fit(in.Devices, spec)
			if device == "" || !runs(in.Supports, in.Bases, spec) {
				continue
			}
			// The org's own hardware: held a window at a time for its caps, never priced.
			t.Class, t.Devices = class(device), n
			v, err := o.extend(ctx, st, org, r, spec, t)
			if err != nil {
				return false, err
			}
			if v.seconds == 0 {
				if err := o.halt(ctx, st, org, r, v.reason); err != nil {
					return false, err
				}
				continue
			}
			c := claim{Class: t.Class, Devices: n, Held: v.seconds}
			lease, ok, err := st.take(ctx, t, caller(ctx), in.Machine, c, unix())
			if err != nil {
				return false, err
			}
			if !ok {
				continue
			}
			if err := o.ship(org); err != nil {
				o.giveBack(ctx, st, org, t, lease)
				return false, err
			}
			if got, err = o.assigned(ctx, st, org, t, lease, device, c); err != nil {
				return false, err
			}
			o.s.Log.Info("train task claimed", "org", org, "job", t.Job, "task", t.ID, "role", t.Role, "machine", in.Machine)
			return true, nil
		}
		return false, nil
	})
	return o.claimed(org, got, err)
}

// claimed is a claim's answer: the assignment, 204 when there was none, and a
// failure as the caller's.
func (o ops) claimed(org string, got *Assignment, err error) (*Assignment, error) {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return nil, err
	}
	if err != nil {
		return nil, o.fault("claim", org, err)
	}
	if got == nil {
		return nil, nil
	}
	o.s.State.bell.ring(got.Org)
	return got, nil
}

// assigned reads a just-taken task's job back as its executor sees it.
func (o ops) assigned(ctx context.Context, st *store, org string, t task, lease, device string, c claim) (*Assignment, error) {
	r, err := st.job(ctx, t.Job)
	if err != nil {
		return nil, err
	}
	j, err := o.view(ctx, st, r)
	if err != nil {
		return nil, err
	}
	return &Assignment{Org: org, Job: j, Task: Claimed{ID: t.ID, Job: t.Job, Role: t.Role, Lease: lease, Coordinator: t.Coordinator,
		Device: device, Devices: c.Devices, Held: c.Held, Report: reportEvery}}, nil
}

// halt ends a job before it runs, its first window refused by its own caps or
// its payer's money: nothing ran, so nothing is charged.
func (o ops) halt(ctx context.Context, st *store, org string, r row, reason string) error {
	if err := st.end(ctx, r.ID, stopped, reason, unix()); err != nil && !errors.Is(err, errTerminal) {
		return err
	}
	if err := o.ship(org); err != nil {
		return err
	}
	o.s.State.bell.ring(org)
	o.s.Log.Info("train job stopped before it ran: its first window cannot be held", "org", org, "job", r.ID, "reason", reason)
	return nil
}

// platform is the bell a platform claim waits on: a job that names no machines
// rings it. No org is the empty name.
const platform = ""

// offer is one org's open task a platform claim may take.
type offer struct {
	ns     namespace.Namespace
	org    string
	t      task
	r      row
	spec   Spec
	device string
	n      int64
}

// claimPlatform is Hanzo's executor's claim: the oldest open lead task of any
// org's job that names no machines and that this executor runs. Its first window
// is held against the job's payer before it is taken; a job whose window its caps
// or its payer refuse is stopped before it runs, and one whose payer's balance
// cannot be read waits for the next claim.
func (o ops) claimPlatform(ctx context.Context, in *claimIn) (*Assignment, error) {
	var got *Assignment
	err := o.poll(ctx, platform, claimWait, func() (bool, error) {
		for _, c := range o.offers(ctx, in) {
			a, err := o.takePlatform(ctx, c, in)
			if err != nil {
				return false, err
			}
			if a != nil {
				got = a
				return true, nil
			}
		}
		return false, nil
	})
	return o.claimed(platform, got, err)
}

// offers are the unplaced tasks of every org this replica holds that the
// claimant runs, oldest job first. An org whose store cannot be read is skipped
// and logged; the next claim reads it again.
func (o ops) offers(ctx context.Context, in *claimIn) []offer {
	var out []offer
	now := unix()
	_ = o.s.State.stores.Each(func(ns namespace.Namespace, st *store, err error) {
		if err != nil || ctx.Err() != nil || !o.s.State.stores.Owned(ns) {
			return
		}
		if err := st.expire(ctx, now, o.s.State.ttl); err != nil {
			o.s.Log.Warn("train platform claim: expire", "namespace", ns, "err", err)
			return
		}
		ts, err := st.unplaced(ctx)
		if err != nil {
			o.s.Log.Warn("train platform claim: read", "namespace", ns, "err", err)
			return
		}
		for _, t := range ts {
			r, err := st.job(ctx, t.Job)
			if err != nil {
				continue
			}
			var spec Spec
			if json.Unmarshal([]byte(r.Spec), &spec) != nil {
				continue
			}
			device, n := fit(in.Devices, spec)
			if device == "" || !runs(in.Supports, in.Bases, spec) {
				continue
			}
			out = append(out, offer{ns: ns, org: orgOf(r, ns), t: t, r: r, spec: spec, device: device, n: n})
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].r.Created < out[j].r.Created })
	return out
}

// orgOf is the org a job was created in, as its creator's credential named it;
// the namespace's own name for a job written before jobs recorded it.
func orgOf(r row, ns namespace.Namespace) string {
	if r.Org != "" {
		return r.Org
	}
	return ns.ID()
}

// takePlatform holds an offer's first window and takes its task, or answers nil
// when it may not or another claim won. A payer whose money holds a window on
// fewer of the claimant's devices than it offers is given that many: the device
// count halves until a window is held, down to one, so a job its create admitted
// on one device's window runs rather than stops. A job whose payer's money is
// held by its other work, or whose balance cannot be read, waits for a later
// claim; one its bounds or its payer refuse outright is stopped before it runs.
// The payer's lock is held from the first weighing to the take.
func (o ops) takePlatform(ctx context.Context, c offer, in *claimIn) (*Assignment, error) {
	t := c.t
	t.Class = class(c.device)
	t.Rate = rate(ctx, c.spec.BaseModel, t.Class)
	// The store is asked for here and not kept from the sweep that found the offer: on
	// hanzo-sql a sweep closes a store it had to open, once its callback returns.
	st, err := o.s.State.stores.For(c.ns)
	if err != nil {
		return nil, err
	}
	unlock, err := o.gate(ctx, st, payerOf(c.org, c.r.Payer))
	if err != nil {
		return nil, err
	}
	defer unlock()
	var v verdict
	for n := c.n; ; n = max(n/2, 1) {
		t.Devices = n
		var err error
		if v, err = o.extend(ctx, st, c.org, c.r, c.spec, t); err != nil {
			return nil, err
		}
		if v.seconds > 0 || n == 1 || (v.reason != stopFunds && v.reason != stopCap) {
			break
		}
	}
	if v.seconds == 0 {
		if v.unknown || v.busy {
			return nil, nil
		}
		return nil, o.halt(ctx, st, c.org, c.r, v.reason)
	}
	cl := claim{Class: t.Class, Devices: t.Devices, Rate: t.Rate, Held: v.seconds}
	lease, ok, err := st.take(ctx, t, caller(ctx), in.Machine, cl, unix())
	if err != nil || !ok {
		return nil, err
	}
	if err := o.audited(ctx, c, in.Machine, cl); err != nil {
		o.giveBack(ctx, st, c.org, t, lease)
		_ = o.ship(c.org)
		return nil, err
	}
	if err := o.ship(c.org); err != nil {
		o.giveBack(ctx, st, c.org, t, lease)
		return nil, err
	}
	o.s.Log.Info("train task claimed by the platform", "org", c.org, "job", t.Job, "task", t.ID, "machine", in.Machine, "rate", t.Rate, "held", v.seconds)
	return o.assigned(ctx, st, c.org, t, lease, c.device, cl)
}

// giveBack returns a task taken under lease to open, for a claim that is not
// answered: its executor never learns the lease, so nothing runs under it.
func (o ops) giveBack(ctx context.Context, st *store, org string, t task, lease string) {
	if err := st.untake(ctx, t, lease); err != nil {
		o.s.Log.Error("train claim: a task taken and not answered could not be given back", "org", org, "job", t.Job, "task", t.ID, "err", err)
	}
}

// audited puts a platform claim on the platform's audit trail: Hanzo's executor
// taking an org's job and starting to charge its payer. A claim the trail cannot
// record is refused.
func (o ops) audited(ctx context.Context, c offer, machine string, cl claim) error {
	who := cloud.Who(ctx)
	o.s.Log.Info("train: the platform claims an org's job", "org", c.org, "job", c.r.ID, "machine", machine, "by", who.User)
	if o.s.State.audit == nil {
		o.s.Log.Error("train: a platform claim refused: this deployment has no audit trail to record it", "org", c.org, "job", c.r.ID)
		return zip.Errorf(http.StatusServiceUnavailable, "train: this claim could not be audited")
	}
	after, _ := json.Marshal(map[string]any{"org": c.org, "job": c.r.ID, "task": c.t.ID, "machine": machine,
		"device": cl.Class, "devices": cl.Devices, "rate_micro_usd_hour": cl.Rate, "held": cl.Held, "payer": c.r.Payer})
	_, err := o.s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: who.Org, Sub: who.User, Email: who.Email},
		Action:    "train.claim",
		Resource:  audit.Resource{Type: "train.job", ID: c.org + "/" + c.r.ID},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "Hanzo's executor runs an org's job, charged to its payer"},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(after),
	})
	if err != nil {
		o.s.Log.Error("train: a platform claim could not be audited", "org", c.org, "job", c.r.ID, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "train: this claim could not be audited")
	}
	return nil
}

type reportIn struct {
	// ID names the job, from the path.
	ID string `json:"id"`
	// Task is the reporting task.
	Task string `json:"task"`
	// Lease is the task's lease, as the claim answered it.
	Lease string `json:"lease"`
	// Events are the task's new events, in order.
	Events []json.RawMessage `json:"events"`
}

type reportOut struct {
	// Cancel says the job was cancelled, or stopped at what its payer or its own
	// bounds could hold: kill the trainer, report nothing more.
	Cancel bool `json:"cancel"`
	// Held is the device-seconds held for the task now.
	Held int64 `json:"held"`
	// Status is the job's status after this report.
	Status string `json:"status"`
	// Seq is the job's last event seq.
	Seq int64 `json:"seq"`
}

// report is the common head of an executor event.
type report struct {
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Error  string  `json:"error"`
	Addr   string  `json:"addr"`
	Name   string  `json:"name"`
	Step   int64   `json:"step"`
	Value  float64 `json:"value"`
	Line   string  `json:"line"`
	SHA256 string  `json:"sha256"`
}

// body is an event as it is kept: its type's own fields and nothing else, a log
// line and an error cut at maxLine. A result is kept as it came.
func (e report) body() json.RawMessage {
	m := map[string]any{"type": e.Type}
	switch e.Type {
	case "status":
		m["status"] = e.Status
		if e.Error != "" {
			m["error"] = e.Error[:min(len(e.Error), maxLine)]
		}
	case "coordinator":
		m["addr"] = e.Addr
	case "metric":
		m["name"], m["step"], m["value"] = e.Name, e.Step, e.Value
	case "log":
		m["line"] = e.Line[:min(len(e.Line), maxLine)]
	case "artifact":
		m["sha256"] = e.SHA256
	}
	raw, _ := json.Marshal(m)
	return raw
}

// result is a lead's last event: the verdict and what it rests on.
type result struct {
	Verdict string `json:"verdict"`
	Base    struct {
		Revision string `json:"revision"`
		SHA256   string `json:"sha256"`
	} `json:"base"`
	Trained json.RawMessage `json:"trained"`
	Suites  []struct {
		Suite  string `json:"suite"`
		Role   string `json:"role"`
		Within bool   `json:"within"`
	} `json:"suites"`
	Research json.RawMessage `json:"research"`
	Outputs  []struct {
		Kind   string `json:"kind"`
		SHA256 string `json:"sha256"`
	} `json:"outputs"`
}

// claimWait bounds a claim's long poll; a var so a test can shorten it.
var claimWait = 25 * time.Second

var addr = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,255}:[0-9]{1,5}$`)

const (
	maxEvents = 500
	maxLine   = 4096
	// maxResult bounds a result event's bytes: its evaluation, kept whole.
	maxResult = 1 << 20
	// maxNotes bounds the log and metric events a job keeps; past it they are
	// not kept, while every status, coordinator, artifact and result still is.
	maxNotes = 100_000
)

// leased resolves the task a report or a registration is made under: the job's,
// claimed, under this lease, and — for a task Hanzo runs — made under the
// platform credential.
func (o ops) leased(ctx context.Context, st *store, r row, id, lease string, now int64) (task, error) {
	t, err := st.held(ctx, r.ID, id, lease, now)
	if err != nil {
		return task{}, err
	}
	if t.Machine == "" && !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return task{}, zip.ErrForbidden("a task Hanzo runs reports under the platform credential")
	}
	return t, nil
}

// ReportTrainTask is an executor's report for the task it holds: status changes,
// the lead's coordinator address, metrics, log lines, stored artifacts and, last,
// the lead's result. It must carry the task's lease; a report under any other is
// 409 lease_lost and the executor stops. A report with no events is a heartbeat,
// due at least every `report` seconds the claim answered — a task silent for the
// lease's life is lost, and a lost lead fails its job. Each report meters the
// task by the clock: its devices times the seconds since it was claimed, no more
// than is held, charged to the job's payer for a job Hanzo runs. The answer says
// how many device-seconds are held now. When the next window cannot be held and
// what is held runs low, the job ends stopped and the answer says cancel: kill
// the trainer and report nothing more. Once the job has ended a report records
// nothing and answers the job's status; cancel says it was cancelled or stopped.
//
// Example: {"id": "job_0123456789abcdef", "task": "tsk_0123456789abcdef", "lease": "trl_…", "events": [{"type": "metric", "name": "nll", "step": 100, "value": 0.66}]}
func (o ops) report(ctx context.Context, in *reportIn) (*reportOut, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	now := unix()
	t, err := o.leased(ctx, st, r, in.Task, in.Lease, now)
	if errors.Is(err, errNotFound) {
		return nil, missing("task")
	}
	if errors.Is(err, errLease) {
		if terminal(r.Status) && r.Status != failed {
			// Charges a report counted and could not send go out with this answer.
			if err := o.settle(ctx, st, org); err != nil {
				return nil, err
			}
			return &reportOut{Cancel: halted(r.Status), Status: r.Status}, nil
		}
		return nil, conflict("lease_lost", "this task's lease is not held")
	}
	if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return nil, err
		}
		return nil, o.fault("report", org, err)
	}
	if len(in.Events) > maxEvents {
		return nil, invalid("events: at most %d a report", maxEvents)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
		return nil, o.fault("report", org, err)
	}
	t, err = o.use(ctx, st, org, r, t, now)
	if err != nil {
		return nil, o.fault("report", org, err)
	}
	out, err := o.apply(ctx, st, org, r, spec, t, in.Events, now)
	// The seconds are metered whether or not an event was refused, and their charge
	// is in the outbox with them: the file ships, then the outbox goes to the meter.
	// A file that does not ship keeps the charge for the next ship to send.
	if serr := o.settle(ctx, st, org); serr != nil {
		return nil, serr
	}
	return out, err
}

// apply records a report's events in order, then holds the task's next window or
// stops its job, and answers the report.
func (o ops) apply(ctx context.Context, st *store, org string, r row, spec Spec, t task, events []json.RawMessage, now int64) (*reportOut, error) {
	var bodies []json.RawMessage
	var points []point
	notes, err := st.notes(ctx, r.ID)
	if err != nil {
		return nil, o.fault("report", org, err)
	}
	for _, raw := range events {
		var e report
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, invalid("events: %v", err)
		}
		if e.Type == "result" && len(raw) > maxResult {
			return nil, invalid("events: a result is at most %d bytes", maxResult)
		}
		note := e.Type == "log" || e.Type == "metric"
		if note && notes >= maxNotes {
			continue
		}
		switch e.Type {
		case "status":
			if err := o.status(ctx, st, r, t, e, now); err != nil {
				return nil, err
			}
		case "coordinator":
			if t.Role != "lead" || !addr.MatchString(e.Addr) {
				return nil, invalid("coordinator: a lead's host:port")
			}
			if err := st.coordinate(ctx, t.ID, e.Addr); err != nil {
				return nil, o.fault("report", org, err)
			}
		case "metric":
			if !name.MatchString(e.Name) || !finite(e.Value) || e.Step < 0 || math.Abs(e.Value) > 1e300 {
				return nil, invalid("metric: a name, a step at or above 0 and a finite value")
			}
			points = append(points, point{Name: e.Name, Step: e.Step, Value: e.Value})
		case "log":
		case "artifact":
			if err := o.stored(ctx, st, org, r.ID, e.SHA256); err != nil {
				return nil, err
			}
		case "result":
			if t.Role != "lead" {
				return nil, invalid("result: the lead's alone")
			}
			if err := o.result(ctx, st, org, r, raw, now); err != nil {
				return nil, err
			}
		default:
			return nil, invalid("events: %q is no event type", e.Type)
		}
		if note {
			notes++
		}
		if e.Type != "result" {
			raw = e.body()
		}
		bodies = append(bodies, raw)
	}
	r, err = st.job(ctx, r.ID)
	if err != nil {
		return nil, o.fault("report", org, err)
	}
	if !terminal(r.Status) {
		if t, err = o.window(ctx, st, org, r, spec, t, now); err != nil {
			return nil, o.fault("report", org, err)
		}
	}
	seq, err := st.append(ctx, r.ID, t.ID, bodies, now)
	if err != nil {
		return nil, o.fault("report", org, err)
	}
	if err := st.measure(ctx, r.ID, points, now); err != nil {
		return nil, o.fault("report", org, err)
	}
	if len(bodies) > 0 {
		o.s.State.bell.ring(org)
	}
	if r, err = st.job(ctx, r.ID); err != nil {
		return nil, o.fault("report", org, err)
	}
	return &reportOut{Cancel: halted(r.Status), Status: r.Status, Seq: seq, Held: t.Held}, nil
}

// halted reports whether a job's executors must kill their trainers: it was
// cancelled, or stopped at what its payer or its own bounds could hold.
func halted(status string) bool { return status == cancelled || status == stopped }

// status applies a task's status event: a lead moves its job running and then
// evaluating, a failed lead fails the job, a lead that stopped on its own ends
// it stopped with what it stored kept, and a join's end closes the join.
func (o ops) status(ctx context.Context, st *store, r row, t task, e report, now int64) error {
	switch {
	case e.Status == "running" && t.Role == "lead":
		return nil
	case e.Status == "evaluating" && t.Role == "lead":
		return st.status(ctx, r.ID, running, evaluating)
	case e.Status == "failed" && t.Role == "lead":
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = "the executor reported failure"
		}
		if len(msg) > maxLine {
			msg = msg[:maxLine]
		}
		if err := st.end(ctx, r.ID, failed, msg, now); err != nil && !errors.Is(err, errTerminal) {
			return err
		}
		return nil
	case e.Status == "stopped" && t.Role == "lead":
		if err := st.end(ctx, r.ID, stopped, "the executor stopped", now); err != nil && !errors.Is(err, errTerminal) {
			return err
		}
		return nil
	case (e.Status == "done" || e.Status == "failed" || e.Status == "stopped") && t.Role == "join":
		return st.close(ctx, t.ID, done)
	}
	return invalid("status: %q is not a %s's status", e.Status, t.Role)
}

// result ends a job with its lead's verdict once every output it names is stored.
func (o ops) result(ctx context.Context, st *store, org string, r row, raw json.RawMessage, now int64) error {
	var res result
	if err := json.Unmarshal(raw, &res); err != nil {
		return invalid("result: %v", err)
	}
	status := succeeded
	switch res.Verdict {
	case "accepted", "none":
	case "rejected":
		status = rejected
	default:
		return invalid("result.verdict: accepted, rejected or none")
	}
	var spec Spec
	if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
		return err
	}
	if spec.Protect != nil && res.Verdict == "none" {
		return invalid("result.verdict: a job with protect is accepted or rejected")
	}
	have := false
	for _, out := range res.Outputs {
		a, err := st.artifact(ctx, r.ID, out.SHA256)
		if errors.Is(err, errNotFound) || (err == nil && a.State != "stored") {
			return conflict("artifact_missing", fmt.Sprintf("output %s is not stored", out.SHA256))
		}
		if err != nil {
			return err
		}
		have = have || a.Kind == spec.Output.Kind
	}
	if !have {
		return conflict("artifact_missing", "no stored "+spec.Output.Kind+" among the outputs")
	}
	if err := st.finish(ctx, r.ID, status, string(raw), now); errors.Is(err, errTerminal) {
		return conflict("terminal", "job already ended")
	} else if err != nil {
		return err
	}
	o.s.Log.Info("train job ended", "org", org, "job", r.ID, "status", status)
	return nil
}

type registerIn struct {
	// ID names the job, from the path.
	ID string `json:"id"`
	// Task is the reporting task.
	Task string `json:"task"`
	// Lease is the task's lease.
	Lease string `json:"lease"`
	// Kind is checkpoint, lora, capability, basis or merged.
	Kind string `json:"kind"`
	// Name labels it.
	Name string `json:"name"`
	// SHA256 is the hex digest of the bytes.
	SHA256 string `json:"sha256"`
	// Size is the byte count.
	Size int64 `json:"size"`
	// Meta describes it: its base and revision, its modules, a basis's address.
	Meta map[string]any `json:"meta"`
}

type registerOut struct {
	// SHA256 names the artifact.
	SHA256 string `json:"sha256"`
	// Stored says the org already holds these bytes: nothing to upload.
	Stored bool `json:"stored"`
	// Upload is the grant to PUT the bytes, when they are not stored.
	Upload *upload `json:"upload,omitempty"`
}

// RegisterTrainArtifact records an output its executor is about to store, named by
// the SHA-256 of its bytes, and answers a presigned PUT for exactly that object
// under the org's prefix — S3 accepts it only with the checksum and encrypts it
// under the org's key — or, over 64 MiB, a presigned PUT per 64 MiB part, or
// stored: true when the org already holds those bytes. The executor then reports
// an artifact event naming it, and the object is read back, those bytes and
// sealed under the org's key, before it counts as stored.
//
// Example: {"id": "job_0123456789abcdef", "task": "tsk_0123456789abcdef", "lease": "trl_…", "kind": "capability", "name": "open-labels", "sha256": "…", "size": 1048576}
func (o ops) register(ctx context.Context, in *registerIn) (*registerOut, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if _, err := o.leased(ctx, st, r, in.Task, in.Lease, unix()); errors.Is(err, errNotFound) {
		return nil, missing("task")
	} else if errors.Is(err, errLease) {
		return nil, conflict("lease_lost", "this task's lease is not held")
	} else if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return nil, err
		}
		return nil, o.fault("register", org, err)
	}
	if !slices.Contains(kinds, in.Kind) || !sha.MatchString(in.SHA256) || in.Size <= 0 || in.Size > maxArtifact || (in.Name != "" && !name.MatchString(in.Name)) {
		return nil, invalid("an artifact needs a kind (%s), a sha256, a size in (0, %d] and a name", strings.Join(kinds, ", "), int64(maxArtifact))
	}
	var spec Spec
	if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
		return nil, o.fault("register", org, err)
	}
	if in.Meta == nil {
		in.Meta = map[string]any{}
	}
	in.Meta["base_model"], in.Meta["revision"] = spec.BaseModel, spec.Revision
	meta, _ := json.Marshal(in.Meta)
	a := artifact{Job: r.ID, SHA256: in.SHA256, Kind: in.Kind, Name: in.Name, Size: in.Size, Meta: meta, Created: unix(), payer: r.Payer}
	return o.admit(ctx, st, org, a, tarType, r.Project, r.Validated)
}

// admit records an artifact about to be stored and answers its grant: stored
// when the org already holds the bytes sealed under its key, else a presigned
// upload sealed under the org's key — refused 409 pending_limit while the org
// holds more than pendingCeiling bytes granted and not confirmed, and 402/503
// when the artifact's payer cannot pay a month of keeping what it already keeps
// in the org, what the org has pending, and these bytes. project and validated
// are the payer's scope, as a create weighs it.
func (o ops) admit(ctx context.Context, st *store, org string, a artifact, mime, project string, validated bool) (*registerOut, error) {
	held, err := st.holds(ctx, a.SHA256)
	if err != nil {
		return nil, o.fault("register", org, err)
	}
	if !held {
		p, err := st.pending(ctx, a.SHA256)
		if err != nil {
			return nil, o.fault("register", org, err)
		}
		if p+a.Size > pendingCeiling {
			return nil, conflict("pending_limit", fmt.Sprintf("the org has %d bytes granted and not confirmed, and at most %d may be; confirm an upload with its artifact event, or wait for its grant to lapse", p, int64(pendingCeiling)))
		}
		payer := payerOf(org, a.payer)
		kept, err := st.kept(ctx, payer.String())
		if err != nil {
			return nil, o.fault("register", org, err)
		}
		if err := o.s.State.afford(ctx, payer, project, validated, monthMicros(kept+p+a.Size, storageRate(ctx))); err != nil {
			return nil, cloud.Denied(err)
		}
	}
	a.State = "pending"
	if held {
		a.State = "stored"
	}
	if err := st.record(ctx, a); err != nil {
		return nil, o.fault("register", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	if held {
		return &registerOut{SHA256: a.SHA256, Stored: true}, nil
	}
	seal, err := o.s.State.seal(ctx, st, org)
	if err != nil {
		o.s.Log.Error("train: the org's key is unavailable", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
	}
	up, err := o.s.State.objects.grant(ctx, key(org, a.SHA256), a.SHA256, a.Size, mime, seal)
	if err != nil {
		o.s.Log.Error("train upload grant failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
	}
	if err := st.open(ctx, a.Job, a.SHA256, up.id); err != nil {
		return nil, o.fault("register", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	return &registerOut{SHA256: a.SHA256, Upload: up}, nil
}

// stored reads an uploaded artifact back and, when it is those bytes sealed
// under the org's key, marks it stored and keeps its object. job is "" for an
// upload of the org's.
func (o ops) stored(ctx context.Context, st *store, org, job, sum string) error {
	a, err := st.artifact(ctx, job, sum)
	if errors.Is(err, errNotFound) {
		return invalid("artifact: %s was not registered for this job", sum)
	}
	if err != nil {
		return o.fault("artifact read", org, err)
	}
	if a.State == "stored" {
		return nil
	}
	id, err := st.upload(ctx, job, sum)
	if err != nil {
		return o.fault("artifact read", org, err)
	}
	seal, err := o.s.State.seal(ctx, st, org)
	if err != nil {
		o.s.Log.Error("train: the org's key is unavailable", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
	}
	k := key(org, sum)
	switch err := o.s.State.objects.verify(ctx, k, sum, a.Size, id, seal); {
	case errors.Is(err, errMissing):
		return conflict("artifact_missing", "no object "+sum+" was uploaded")
	case errors.Is(err, errMismatch):
		return conflict("artifact_mismatch", "the object stored as "+sum+" is not those bytes")
	case errors.Is(err, errPlain):
		return conflict("artifact_mismatch", "the object stored as "+sum+" is not encrypted under the org's key")
	case err != nil:
		o.s.Log.Error("train artifact verify failed", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
	}
	if err := st.stored(ctx, a, k, seal, unix()); err != nil {
		return o.fault("artifact store", org, err)
	}
	return nil
}

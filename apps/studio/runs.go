package studio

// runs.go — /v1/studio/runs: start a run, read it, list them, cancel, retry, and
// its versions.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/orm/tenant"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
)

// idemTTL is how long an Idempotency-Key names the run it started.
const idemTTL = 24 * time.Hour

// plan is a run request resolved to what it executes.
type plan struct {
	graph    Graph
	deps     map[string][]string
	input    map[string]any
	inputs   map[string]any
	template string
	flow     string
	brand    string
	tokens   map[string]any
	kind     string
	title    string
	version  int64
	parent   string
	root     string
}

// resolve turns a run request into its plan: exactly one source of the graph,
// the input checked against its inputs schema, every asset it names readable by
// the person, the brand's tokens read.
func (o ops) resolve(ctx context.Context, me string, in *RunCreate) (plan, error) {
	var p plan
	sources := 0
	for _, set := range []bool{in.Template != "", in.Flow != "", in.Graph != nil} {
		if set {
			sources++
		}
	}
	if sources > 1 || (sources == 0 && in.Parent == "") {
		return p, zip.ErrBadRequest("name exactly one of template, flow or graph (or parent alone)")
	}
	if in.Parent != "" {
		par, err := o.s.State.readableRun(ctx, me, in.Parent)
		if err != nil {
			return p, notFound(err)
		}
		if err := json.Unmarshal([]byte(par.Graph), &p.graph); err != nil {
			return p, err
		}
		_ = json.Unmarshal([]byte(par.Input), &p.input)
		p.template, p.flow, p.brand, p.kind, p.title = par.Template, par.Flow, par.Brand, par.Kind, par.Title
		p.root, p.parent = par.Root, par.ID
		last, ok, err := o.s.State.db.Select[runRow]("runs", "version").Where(tenant.Eq("root", par.Root)).OrderBy("version DESC").Limit(1).First(ctx)
		if err != nil {
			return p, err
		}
		p.version = 1
		if ok {
			p.version = last.Version + 1
		}
	}
	switch {
	case in.Template != "":
		t, err := o.s.State.template(ctx, me, in.Template)
		if err != nil {
			return p, notFound(err)
		}
		p.graph, p.inputs, p.template, p.kind, p.title = t.Graph, t.Inputs, t.ID, t.Kind, t.Title
	case in.Flow != "":
		f, err := o.s.State.flow(ctx, me, in.Flow)
		if err != nil {
			return p, notFound(err)
		}
		p.graph, p.inputs, p.flow, p.kind, p.title = f.Graph, f.Inputs, f.ID, or(f.Kind, kindOf(f.Graph)), f.Name
	case in.Graph != nil:
		p.graph, p.kind, p.title = *in.Graph, kindOf(*in.Graph), ""
	}
	if p.input == nil {
		p.input = map[string]any{}
	}
	for k, v := range in.Input {
		p.input[k] = v
	}
	if b, _ := json.Marshal(p.input); len(b) > maxInput {
		return p, unprocessable("input is at most %d bytes", maxInput)
	}
	deps, err := check(p.graph)
	if err != nil {
		return p, err
	}
	p.deps = deps
	if err := conform(p.inputs, p.input); err != nil {
		return p, err
	}
	// An input naming a document is one the person may read.
	props, _ := p.inputs["properties"].(map[string]any)
	for name, raw := range props {
		spec, _ := raw.(map[string]any)
		if spec["format"] != "document" {
			continue
		}
		if id, ok := p.input[name].(string); ok {
			if _, err := o.s.State.document(ctx, me, id); err != nil {
				return p, unprocessable("input.%s is not a document you may use", name)
			}
		}
	}
	if err := o.s.State.mayRead(ctx, me, p.input); err != nil {
		return p, err
	}
	if in.Brand != "" {
		p.brand = in.Brand
	}
	if p.brand != "" {
		b, err := o.s.State.brand(ctx, me, p.brand)
		if err != nil {
			return p, notFound(err)
		}
		p.tokens = b.Tokens
	}
	if t := strings.TrimSpace(in.Title); t != "" {
		p.title = t
	}
	if p.title == "" {
		p.title = titleOf(p.input, p.kind)
	}
	if len(p.title) > 120 {
		p.title = p.title[:120]
	}
	if p.version == 0 {
		p.version = 1
	}
	return p, nil
}

// conform checks an input against an inputs schema: every required property
// present, and each present property of the declared JSON type.
func conform(schema, input map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	props, _ := schema["properties"].(map[string]any)
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			name, _ := r.(string)
			if v, ok := input[name]; !ok || v == nil || v == "" {
				return unprocessable("input.%s is required", name)
			}
		}
	}
	for name, raw := range props {
		v, ok := input[name]
		if !ok {
			if def, ok := raw.(map[string]any)["default"]; ok {
				input[name] = def
			}
			continue
		}
		spec, _ := raw.(map[string]any)
		want, _ := spec["type"].(string)
		good := true
		switch want {
		case "string":
			_, good = v.(string)
		case "number", "integer":
			_, good = v.(float64)
		case "boolean":
			_, good = v.(bool)
		case "array":
			_, good = v.([]any)
		case "object":
			_, good = v.(map[string]any)
		}
		if !good {
			return unprocessable("input.%s is a %s", name, want)
		}
	}
	return nil
}

// mayRead refuses an input naming an asset the person may not read.
func (s *state) mayRead(ctx context.Context, me string, v any) error {
	var bad error
	var visit func(any)
	visit = func(v any) {
		if bad != nil {
			return
		}
		switch t := v.(type) {
		case string:
			ids := assetURL.FindAllStringSubmatch(t, -1)
			if assetID.MatchString(t) {
				ids = append(ids, []string{t, t})
			}
			for _, m := range ids {
				if _, err := s.readable(ctx, m[1]); err != nil {
					bad = unprocessable("item %s is not one you may use", m[1])
					return
				}
			}
		case map[string]any:
			for _, x := range t {
				visit(x)
			}
		case []any:
			for _, x := range t {
				visit(x)
			}
		}
	}
	visit(v)
	return bad
}

// titleOf names a run that was given no title.
func titleOf(input map[string]any, kind string) string {
	for _, k := range []string{"title", "brief", "prompt", "text", "caption"} {
		if s, ok := input[k].(string); ok && strings.TrimSpace(s) != "" {
			s = strings.TrimSpace(s)
			if len(s) > 80 {
				s = s[:80]
			}
			return s
		}
	}
	return "Untitled " + kind
}

// Starts a run: name exactly one of template, flow or graph, with the input its
// inputs schema asks for. The run is checked, priced and its estimate reserved
// against your org's wallet, then queued; the answer is the run at queued. Follow
// it on GET /v1/studio/runs/{id}/events.
//
// parent makes this run the next version of that run: it takes the parent's graph
// and input, overlaid by what this request states. Send Idempotency-Key to make a
// retried request start one run, not two. 402 when the wallet cannot cover the
// estimate; 422 when the graph or input does not validate.
//
// Example: {"template": "image", "input": {"prompt": "a lighthouse at dawn, risograph"}}
func (o ops) create(ctx context.Context, in *RunCreate) (*Run, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	idem := strings.TrimSpace(in.Key)
	if len(idem) > 128 {
		return nil, zip.ErrBadRequest("Idempotency-Key is at most 128 characters")
	}
	if idem != "" {
		if k, ok, err := st.db.Select[keyRow]("keys").Where(tenant.And(tenant.Eq("creator", me), tenant.Eq("key", idem))).First(ctx); err != nil {
			return nil, err
		} else if ok && now()-k.Created < int64(idemTTL) {
			return o.run(ctx, &ref{ID: k.Run})
		}
	}
	p, err := o.resolve(ctx, me, in)
	if err != nil {
		return nil, err
	}
	est := st.estimate(ctx, p.graph, p.input)
	payer := cloud.PayerOf(ctx)
	id := mint.ID("run")
	if p.root == "" {
		p.root = id
	}
	graph, _ := json.Marshal(p.graph)
	input, _ := json.Marshal(p.input)
	tokens, _ := json.Marshal(nonNilMap(p.tokens))
	pay, _ := json.Marshal(recordPayer(payer))
	row := runRow{
		ID: id, Creator: me, Shared: in.Shared, Kind: p.kind, Title: p.title, Status: queued,
		Template: p.template, Flow: p.flow, Brand: p.brand, Graph: string(graph), Input: string(input),
		Tokens: string(tokens), Version: p.version, Parent: p.parent, Root: p.root,
		Estimate: est.Cents, Payer: string(pay), Created: now(),
	}
	err = st.reserve(ctx, payer, est.Cents, func(tx *tenant.DB) error {
		if err := tx.Insert(ctx, "runs", row); err != nil {
			return err
		}
		for i, s := range p.graph.Steps {
			if err := tx.Insert(ctx, "steps", stepRow{Run: id, ID: s.ID, Seq: int64(i), Op: s.Op, Title: s.Title, Status: pending, Outputs: "[]"}); err != nil {
				return err
			}
		}
		if idem != "" {
			return tx.Upsert(ctx, "keys", keyRow{Creator: me, Key: idem, Run: id, Created: now()}, "creator", "key")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	st.emit(ctx, id, "run", nil)
	if err := st.work.start(context.WithoutCancel(ctx), Job{Org: orgOf(ctx), Run: id}); err != nil {
		st.fail(ctx, id, "the run could not start: "+err.Error())
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the run could not start; try again")
	}
	return o.run(ctx, &ref{ID: id})
}

// fail closes a run that never got to run.
func (s *state) fail(ctx context.Context, id, why string) {
	fault, _ := json.Marshal(problem(503, "not_started", why))
	if _, err := s.db.Update(ctx, "runs", tenant.Row{"status": failed, "fault": string(fault), "finished": now()}, tenant.Eq("id", id)); err == nil {
		s.emit(ctx, id, "done", nil)
	}
}

// Prices a run without starting it: the estimate POST /v1/studio/runs would
// reserve, step by step. Nothing is reserved and nothing runs.
func (o ops) price(ctx context.Context, in *RunCreate) (*Estimate, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	p, err := o.resolve(ctx, me, in)
	if err != nil {
		return nil, err
	}
	e := o.s.State.estimate(ctx, p.graph, p.input)
	return &e, nil
}

// Reads one run: its graph, its input, every step's state, the assets it made
// and what it cost. A run you may not read answers 404.
func (o ops) run(ctx context.Context, in *ref) (*Run, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	r, err := o.s.State.readableRun(ctx, me, in.ID)
	if err != nil {
		return nil, notFound(err)
	}
	return o.s.State.runView(ctx, r)
}

// Lists the runs you may read — your own and those your org shares — newest
// first. Filter by kind or status.
func (o ops) runs(ctx context.Context, in *runsIn) (*RunPage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	cond := visible(me)
	if in.Kind != "" {
		cond = tenant.And(cond, tenant.Eq("kind", in.Kind))
	}
	if in.Status != "" {
		cond = tenant.And(cond, tenant.Eq("status", in.Status))
	}
	limit, after, err := paging(in.Limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	if after != nil {
		cond = tenant.And(cond, after.before())
	}
	rows, err := o.s.State.db.Select[runRow]("runs").Where(cond).OrderBy("created DESC", "id DESC").Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &RunPage{Data: []Run{}}
	for i, r := range rows {
		if i == limit {
			out.Next = cursorOf(rows[i-1].Created, rows[i-1].ID)
			break
		}
		v, err := o.s.State.runView(ctx, r)
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *v)
	}
	return out, nil
}

// Renames or shares one of your runs. Sharing a run lets every member of your
// org read it; its files are Library items, shared from the Library.
func (o ops) runPatch(ctx context.Context, in *RunPatch) (*Run, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	set := tenant.Row{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || len(t) > 120 {
			return nil, zip.ErrBadRequest("title is 1 to 120 characters")
		}
		set["title"] = t
	}
	if in.Shared != nil {
		set["shared"] = *in.Shared
	}
	if len(set) > 0 {
		err := st.db.Tx(ctx, func(tx *tenant.DB) error {
			n, err := tx.Update(ctx, "runs", set, tenant.And(tenant.Eq("id", in.ID), mine(me)))
			if err != nil {
				return err
			}
			if n == 0 {
				return gone("run")
			}
			return nil
		})
		if err != nil {
			return nil, notFound(err)
		}
	}
	return o.run(ctx, &ref{ID: in.ID})
}

// Cancels one of your runs: a step in flight is told to stop, nothing new
// starts, and the reservation settles to what finished steps cost. Cancelling a
// run that already ended answers it unchanged.
func (o ops) cancel(ctx context.Context, in *ref) (*Run, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	r, ok, err := st.db.Select[runRow]("runs").Where(tenant.And(tenant.Eq("id", in.ID), mine(me))).First(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("run not found")
	}
	if r.Status != queued && r.Status != running {
		return o.run(ctx, in)
	}
	n, err := st.db.Update(ctx, "runs", tenant.Row{"status": canceled, "finished": now()},
		tenant.And(tenant.Eq("id", r.ID), tenant.In("status", queued, running)))
	if err != nil {
		return nil, err
	}
	if n > 0 {
		bg := context.WithoutCancel(ctx)
		parts, _ := st.db.Select[partRow]("parts").Where(tenant.And(tenant.Eq("run", r.ID), tenant.Eq("status", running))).All(ctx)
		for _, pr := range parts {
			if pr.Job == "" {
				continue
			}
			l := lane(st.cpu)
			for _, s := range mustGraph(r).Steps {
				if s.ID == pr.Step && known[s.Op].executor == gpu {
					l = st.gpu
				}
			}
			if err := l.cancel(bg, orgOf(ctx), pr.Job, "run cancelled"); err != nil {
				st.log.Debug("studio: cancel a job", "job", pr.Job, "err", err)
			}
		}
		_, _ = st.db.Update(ctx, "steps", tenant.Row{"status": canceled}, tenant.And(tenant.Eq("run", r.ID), tenant.In("status", pending, queued, running)))
		_, _ = st.db.Update(ctx, "parts", tenant.Row{"status": canceled}, tenant.And(tenant.Eq("run", r.ID), tenant.Eq("status", running)))
		st.work.end(bg, r.ID, int(r.Attempt), "cancelled by its creator")
		_ = st.recharge(ctx, r.ID)
		st.emit(ctx, r.ID, "done", nil)
	}
	return o.run(ctx, in)
}

// Resumes one of your runs that failed or was cancelled: the steps that did not
// succeed run again in the same run, and the steps that did keep their outputs
// and are neither run nor charged again. What is left to run is reserved first
// (402 when it cannot be); 409 while the run is still going or once it succeeded.
func (o ops) retry(ctx context.Context, in *ref) (*Run, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	r, ok, err := st.db.Select[runRow]("runs").Where(tenant.And(tenant.Eq("id", in.ID), mine(me))).First(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("run not found")
	}
	if r.Status != failed && r.Status != canceled {
		return nil, zip.ErrConflict("only a failed or cancelled run is retried; this one is " + r.Status)
	}
	g := mustGraph(r)
	var input map[string]any
	_ = json.Unmarshal([]byte(r.Input), &input)
	done := map[string]bool{}
	steps, err := st.db.Select[stepRow]("steps").Where(tenant.Eq("run", r.ID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		done[s.ID] = s.Status == succeeded
	}
	rest := Graph{}
	for _, s := range g.Steps {
		if !done[s.ID] {
			rest.Steps = append(rest.Steps, s)
		}
	}
	left := st.estimate(ctx, rest, input).Cents
	attempt := r.Attempt + 1
	payer := cloud.PayerOf(ctx)
	pay, _ := json.Marshal(recordPayer(payer))
	err = st.reserve(ctx, payer, left, func(tx *tenant.DB) error {
		n, err := tx.Update(ctx, "runs", tenant.Row{"status": queued, "attempt": attempt, "fault": "", "finished": int64(0),
			"estimate": r.Charged + left, "payer": string(pay)},
			tenant.And(tenant.Eq("id", r.ID), tenant.In("status", failed, canceled)))
		if err != nil {
			return err
		}
		if n == 0 {
			return zip.ErrConflict("the run changed; read it again")
		}
		if _, err := tx.Update(ctx, "steps", tenant.Row{"status": pending, "fault": "", "progress": 0.0, "message": "", "finished": int64(0)},
			tenant.And(tenant.Eq("run", r.ID), tenant.Ne("status", succeeded))); err != nil {
			return err
		}
		_, err = tx.Update(ctx, "parts", tenant.Row{"status": pending, "fault": ""},
			tenant.And(tenant.Eq("run", r.ID), tenant.Ne("status", succeeded)))
		return err
	})
	if err != nil {
		return nil, err
	}
	st.emit(ctx, r.ID, "run", nil)
	if err := st.work.start(context.WithoutCancel(ctx), Job{Org: orgOf(ctx), Run: r.ID, Attempt: int(attempt)}); err != nil {
		st.fail(ctx, r.ID, "the run could not start: "+err.Error())
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the run could not start; try again")
	}
	return o.run(ctx, in)
}

// Lists every version of a creation you may read — the runs in this run's
// lineage — oldest (version 1) first.
func (o ops) versions(ctx context.Context, in *ref) (*RunPage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	st := o.s.State
	r, err := st.readableRun(ctx, me, in.ID)
	if err != nil {
		return nil, notFound(err)
	}
	rows, err := st.db.Select[runRow]("runs").Where(tenant.And(tenant.Eq("root", r.Root), visible(me))).OrderBy("version ASC").Limit(200).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &RunPage{Data: []Run{}}
	for _, x := range rows {
		v, err := st.runView(ctx, x)
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *v)
	}
	return out, nil
}

// ── views ────────────────────────────────────────────────────────────────

func (s *state) readableRun(ctx context.Context, me, id string) (runRow, error) {
	r, ok, err := s.db.Select[runRow]("runs").Where(tenant.And(tenant.Eq("id", id), visible(me))).First(ctx)
	if err != nil {
		return runRow{}, err
	}
	if !ok {
		return runRow{}, gone("run")
	}
	return r, nil
}

func mustGraph(r runRow) Graph {
	var g Graph
	_ = json.Unmarshal([]byte(r.Graph), &g)
	return g
}

// runView is a run as the wire carries it.
func (s *state) runView(ctx context.Context, r runRow) (*Run, error) {
	v := &Run{
		ID: r.ID, Kind: r.Kind, Title: r.Title, Status: r.Status, Template: r.Template, Flow: r.Flow, Brand: r.Brand,
		Graph: mustGraph(r), Version: int(r.Version), Parent: r.Parent, Root: r.Root,
		EstimateCents: r.Estimate, ChargedCents: r.Charged, Text: r.Text,
		Org: orgOf(ctx), Creator: r.Creator, Shared: r.Shared,
		Created: stamp(r.Created), Started: stamp(r.Started), Finished: stamp(r.Finished),
		Steps: []Step{}, Outputs: []Item{},
	}
	_ = json.Unmarshal([]byte(r.Input), &v.Input)
	if v.Input == nil {
		v.Input = map[string]any{}
	}
	if r.Fault != "" {
		var p Problem
		if json.Unmarshal([]byte(r.Fault), &p) == nil {
			v.Error = &p
		}
	}
	steps, err := s.db.Select[stepRow]("steps").Where(tenant.Eq("run", r.ID)).OrderBy("seq ASC").All(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range steps {
		v.Steps = append(v.Steps, st.view())
	}
	// The final steps' outputs first: the steps no other step reads.
	read := map[string]bool{}
	for _, d := range depsOf(v.Graph) {
		for _, x := range d {
			read[x] = true
		}
	}
	var first, rest []Item
	for _, st := range v.Steps {
		for _, id := range st.Outputs {
			it, err := s.readable(ctx, id)
			if err != nil {
				continue
			}
			if !read[st.ID] {
				first = append(first, it)
			} else {
				rest = append(rest, it)
			}
		}
	}
	v.Outputs = append(append(v.Outputs, first...), rest...)
	if v.Outputs == nil {
		v.Outputs = []Item{}
	}
	return v, nil
}

func depsOf(g Graph) map[string][]string {
	d, err := check(g)
	if err != nil {
		return nil
	}
	return d
}

// view is a step as the wire carries it.
func (st stepRow) view() Step {
	v := Step{
		ID: st.ID, Op: st.Op, Title: st.Title, Executor: known[st.Op].executor, Status: st.Status,
		Attempt: int(st.Attempt), Progress: st.Progress, Message: st.Message, Items: int(st.Items),
		Text: st.Text, CostCents: st.Cost, Started: stamp(st.Started), Finished: stamp(st.Finished),
		Outputs: []string{},
	}
	_ = json.Unmarshal([]byte(st.Outputs), &v.Outputs)
	if v.Outputs == nil {
		v.Outputs = []string{}
	}
	if st.Status != succeeded {
		// A step's planned outputs are not outputs until it lands them.
		v.Outputs = []string{}
	}
	if st.Fault != "" {
		var p Problem
		if json.Unmarshal([]byte(st.Fault), &p) == nil {
			v.Error = &p
		}
	}
	return v
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

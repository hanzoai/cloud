package studio

// activities.go — the four activities a run's workflow runs: load its plan, run a
// part, settle a step, close the run. Each acts for the org its input states and
// for nobody else: every store statement it makes is scoped to that org.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/orm/tenant"
)

// load reads the plan a run executes.
func (s *state) load(ctx context.Context, j Job) (Plan, error) {
	ctx = scoped(ctx, j.Org)
	r, ok, err := s.runRow(ctx, j.Run)
	if err != nil {
		return Plan{}, err
	}
	if !ok {
		return Plan{}, fatal("run %s is gone", j.Run)
	}
	var p Plan
	if err := json.Unmarshal([]byte(r.Graph), &p.Graph); err != nil {
		return Plan{}, fatal("run %s: its graph does not read: %v", j.Run, err)
	}
	if p.Deps, err = check(p.Graph); err != nil {
		return Plan{}, fatal("%v", err)
	}
	_ = json.Unmarshal([]byte(r.Input), &p.Input)
	_ = json.Unmarshal([]byte(r.Tokens), &p.Brand)
	if p.Input == nil {
		p.Input = map[string]any{}
	}
	if p.Brand == nil {
		p.Brand = map[string]any{}
	}
	if r.Status == queued {
		if _, err := s.db.Update(ctx, "runs", tenant.Row{"status": running, "started": now()},
			tenant.And(tenant.Eq("id", j.Run), tenant.Eq("status", queued))); err != nil {
			return Plan{}, err
		}
		s.emit(ctx, j.Run, "run", nil)
	}
	return p, nil
}

// part runs one part of one step, once. A part that already succeeded — on an
// earlier attempt of this run, or before this process died — answers what it
// recorded and is not run again.
func (s *state) part(ctx context.Context, p Part) (result, error) {
	ctx = scoped(ctx, p.Org)
	r, ok, err := s.runRow(ctx, p.Run)
	if err != nil {
		return result{}, err
	}
	if !ok || r.Status == canceled {
		return result{}, stop()
	}
	pr, ok, err := s.partRow(ctx, p.Run, p.Step, p.Item)
	if err != nil {
		return result{}, err
	}
	if ok && pr.Status == succeeded {
		// Billing is exactly-once by its ref, so asking again is how a debit that
		// failed after the part was recorded is finished.
		if err := s.debit(ctx, r, p, pr.Cost); err != nil {
			return result{}, err
		}
		return pr.result(), nil
	}
	if !ok {
		pr = partRow{Run: p.Run, Step: p.Step, Item: int64(p.Item), Status: running, Outputs: "[]"}
		if _, err := s.db.CreateIfAbsent(ctx, "parts", pr, "run", "step", "item"); err != nil {
			return result{}, err
		}
	}
	if _, err := s.db.Update(ctx, "parts", tenant.Row{"status": running, "attempt": pr.Attempt + 1, "fault": ""}, partKey(p)); err != nil {
		return result{}, err
	}
	s.begin(ctx, p)

	var out result
	var cost int64
	switch known[p.Op].executor {
	case gateway:
		out, cost, err = s.gateway(ctx, r, p)
	case cpu, gpu:
		out, cost, err = s.lanePart(ctx, r, pr, p)
	default:
		err = fatal("op %s has no executor in this deployment", p.Op)
	}
	if err != nil {
		var msg = cause(err)
		if _, uerr := s.db.Update(ctx, "parts", tenant.Row{"fault": msg}, partKey(p)); uerr != nil {
			s.log.Debug("studio: record a part's fault", "err", uerr)
		}
		s.log.Info("studio: part failed", "run", p.Run, "step", p.Step, "item", p.Item, "op", p.Op, "err", msg)
		return result{}, err
	}
	outs, _ := json.Marshal(nonNil(out.Assets))
	js := ""
	if out.JSON != nil {
		b, _ := json.Marshal(out.JSON)
		js = string(b)
	}
	set := tenant.Row{"status": succeeded, "outputs": string(outs), "text": out.Text, "json": js, "cost": cost, "fault": ""}
	if _, err := s.db.Update(ctx, "parts", set, partKey(p)); err != nil {
		return result{}, err
	}
	if err := s.recharge(ctx, p.Run); err != nil {
		return result{}, err
	}
	for _, a := range out.Assets {
		s.emit(ctx, p.Run, "asset", map[string]any{"step": p.Step, "asset": a})
	}
	if err := s.debit(ctx, r, p, cost); err != nil {
		return result{}, err
	}
	return out, nil
}

// begin marks a part's step running, the first time one of its parts starts.
func (s *state) begin(ctx context.Context, p Part) {
	n, err := s.db.Update(ctx, "steps", tenant.Row{"status": running, "started": now(), "items": int64(p.Items)},
		tenant.And(tenant.Eq("run", p.Run), tenant.Eq("id", p.Step), tenant.In("status", pending, queued, failed, canceled)))
	if err != nil {
		s.log.Debug("studio: mark a step running", "err", err)
		return
	}
	if n > 0 {
		s.bumpAttempt(ctx, p.Run, p.Step)
		s.emit(ctx, p.Run, "step", map[string]any{"step": p.Step})
	}
}

// bumpAttempt counts one more start of a step.
func (s *state) bumpAttempt(ctx context.Context, run, step string) {
	st, ok, err := s.stepRow(ctx, run, step)
	if err != nil || !ok {
		return
	}
	_, _ = s.db.Update(ctx, "steps", tenant.Row{"attempt": st.Attempt + 1}, tenant.And(tenant.Eq("run", run), tenant.Eq("id", step)))
}

// step settles one step from its parts' results, or records why it failed.
func (s *state) step(ctx context.Context, st Settle) error {
	ctx = scoped(ctx, st.Org)
	key := tenant.And(tenant.Eq("run", st.Run), tenant.Eq("id", st.Step))
	if st.Fault != "" {
		fault, _ := json.Marshal(problem(422, "step_failed", st.Fault))
		if _, err := s.db.Update(ctx, "steps", tenant.Row{"status": failed, "fault": string(fault), "finished": now()}, key); err != nil {
			return err
		}
		s.emit(ctx, st.Run, "step", map[string]any{"step": st.Step})
		return nil
	}
	var assets []string
	texts := []string{}
	var js any
	for _, r := range st.Results {
		assets = append(assets, r.Assets...)
		if r.Text != "" {
			texts = append(texts, r.Text)
		}
	}
	if len(st.Results) == 1 {
		js = st.Results[0].JSON
	} else {
		all := make([]any, len(st.Results))
		for i, r := range st.Results {
			all[i] = r.JSON
		}
		js = all
	}
	outs, _ := json.Marshal(nonNil(assets))
	jb := ""
	if js != nil {
		b, _ := json.Marshal(js)
		jb = string(b)
	}
	cost, err := s.stepCost(ctx, st.Run, st.Step)
	if err != nil {
		return err
	}
	set := tenant.Row{"status": succeeded, "outputs": string(outs), "text": strings.Join(texts, "\n\n"), "json": jb,
		"cost": cost, "progress": 1.0, "message": "", "finished": now(), "items": int64(st.Items), "fault": ""}
	if _, err := s.db.Update(ctx, "steps", set, key); err != nil {
		return err
	}
	s.emit(ctx, st.Run, "step", map[string]any{"step": st.Step})
	s.track(ctx, st.Run)
	return nil
}

// close ends a run: its status, why it failed, and what it cost, summed from its
// parts. The reservation it held is released by the run no longer being open.
func (s *state) close(ctx context.Context, c Close) error {
	ctx = scoped(ctx, c.Org)
	r, ok, err := s.runRow(ctx, c.Run)
	if err != nil {
		return err
	}
	if !ok || r.Status == canceled {
		return nil
	}
	if err := s.recharge(ctx, c.Run); err != nil {
		return err
	}
	set := tenant.Row{"status": c.Status, "finished": now()}
	if c.Status == failed {
		fault, _ := json.Marshal(problem(422, "run_failed", c.Fault))
		set["fault"] = string(fault)
	} else {
		set["fault"] = ""
	}
	if text, err := s.lastText(ctx, r); err == nil && text != "" {
		set["text"] = text
	}
	if _, err := s.db.Update(ctx, "runs", set, tenant.Eq("id", c.Run)); err != nil {
		return err
	}
	if c.Status != succeeded {
		// Steps that never got to run say so.
		if _, err := s.db.Update(ctx, "steps", tenant.Row{"status": "skipped"},
			tenant.And(tenant.Eq("run", c.Run), tenant.In("status", pending, queued))); err != nil {
			return err
		}
	}
	s.emit(ctx, c.Run, "done", nil)
	return nil
}

// lastText is the text of the last chat step in a run's graph that made one.
func (s *state) lastText(ctx context.Context, r runRow) (string, error) {
	var g Graph
	if err := json.Unmarshal([]byte(r.Graph), &g); err != nil {
		return "", err
	}
	for i := len(g.Steps) - 1; i >= 0; i-- {
		if g.Steps[i].Op != "chat" {
			continue
		}
		st, ok, err := s.stepRow(ctx, r.ID, g.Steps[i].ID)
		if err != nil || !ok {
			return "", err
		}
		return st.Text, nil
	}
	return "", nil
}

// recharge sums a run's cost from its parts.
func (s *state) recharge(ctx context.Context, run string) error {
	parts, err := s.db.Select[partRow]("parts").Where(tenant.Eq("run", run)).All(ctx)
	if err != nil {
		return err
	}
	var total int64
	for _, p := range parts {
		if p.Status == succeeded {
			total += p.Cost
		}
	}
	_, err = s.db.Update(ctx, "runs", tenant.Row{"charged": total}, tenant.Eq("id", run))
	return err
}

// stepCost sums one step's cost from its parts.
func (s *state) stepCost(ctx context.Context, run, step string) (int64, error) {
	parts, err := s.db.Select[partRow]("parts").Where(tenant.And(tenant.Eq("run", run), tenant.Eq("step", step))).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, p := range parts {
		if p.Status == succeeded {
			total += p.Cost
		}
	}
	return total, nil
}

// ── rows ─────────────────────────────────────────────────────────────────

func (s *state) runRow(ctx context.Context, id string) (runRow, bool, error) {
	return s.db.Select[runRow]("runs").Where(tenant.Eq("id", id)).First(ctx)
}

func (s *state) stepRow(ctx context.Context, run, id string) (stepRow, bool, error) {
	return s.db.Select[stepRow]("steps").Where(tenant.And(tenant.Eq("run", run), tenant.Eq("id", id))).First(ctx)
}

func (s *state) partRow(ctx context.Context, run, step string, item int) (partRow, bool, error) {
	return s.db.Select[partRow]("parts").Where(tenant.And(tenant.Eq("run", run), tenant.Eq("step", step), tenant.Eq("item", int64(item)))).First(ctx)
}

func partKey(p Part) tenant.Cond {
	return tenant.And(tenant.Eq("run", p.Run), tenant.Eq("step", p.Step), tenant.Eq("item", int64(p.Item)))
}

// result is what a succeeded part recorded.
func (p partRow) result() result {
	r := result{Text: p.Text}
	_ = json.Unmarshal([]byte(p.Outputs), &r.Assets)
	if p.JSON != "" {
		_ = json.Unmarshal([]byte(p.JSON), &r.JSON)
	}
	return r
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// problem is an RFC 9457 document for a step or a run that failed.
func problem(status int, code, detail string) Problem {
	return Problem{Type: "about:blank", Status: status, Detail: detail, Code: code}
}

// errGone is a resource the caller cannot read: absent and foreign alike.
var errGone = errors.New("not found")

// gone is the 404 for a resource a caller cannot read, whatever the reason.
func gone(what string) error { return fmt.Errorf("%s %w", what, errGone) }

package bots

import (
	"context"
	"net/url"

	"github.com/hanzoai/cloud/clients/runtime"
)

// wire.go is bots' WIRE CONTRACT with the bot runtime — the stub behind the
// Runtime seam. It says WHAT bots asks the runtime (list this org's runs, halt one
// of them); how the bytes get there is runtime's problem.
//
// This file is the ONE place in clients/bots that knows the runtime exists. The
// handlers (bots.go) never see it: they hold the Runtime seam, which a test fills
// with a fake.
//
// The id space is the RUNTIME'S. A run id here is whatever the runtime named its
// session — cloud does not mint it, does not store it, and could not resolve it if
// it tried. That is exactly why list and stop agree: both speak the only id space
// that has ever held a real run.

const listOp = "/v1/bots"

func stopOp(runID string) string { return "/v1/bots/" + url.PathEscape(runID) + "/stop" }

// wire is the Runtime seam over the real runtime.
type wire struct{}

// runRow is the runtime's row shape (its GET /v1/bots emits exactly these). It
// carries no sessionUrl: the runtime does not know its own public origin, so the
// control plane derives that from runId.
type runRow struct {
	RunID     string `json:"runId"`
	Task      string `json:"task"`
	Surface   string `json:"surface"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
}

// List reads org's runs. The runtime scopes them to the tenant cloud names, so the
// answer is already this org's and only this org's.
func (wire) List(ctx context.Context, org string) ([]Run, error) {
	var answer struct {
		Bots []runRow `json:"bots"`
	}
	if err := runtime.Read(ctx, runtime.Call{Op: listOp, Org: org}, &answer); err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(answer.Bots))
	for _, b := range answer.Bots {
		if b.RunID == "" {
			continue // a row cloud cannot address is not a run it can offer
		}
		out = append(out, Run{
			ID: b.RunID, Task: b.Task, Surface: b.Surface,
			Status: b.Status, StartedAt: b.StartedAt,
		})
	}
	return out, nil
}

// Stop halts org's run. It reports the runtime's own answer verbatim — absent,
// unserved, or a failure — so the handler decides what each MEANS rather than this
// stub deciding for it.
func (wire) Stop(ctx context.Context, org, runID string) error {
	return runtime.Do(ctx, runtime.Call{Op: stopOp(runID), Org: org})
}

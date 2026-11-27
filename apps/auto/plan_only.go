package auto

import (
	"context"

	"github.com/hanzoai/cloud/sdk"
)

// planOnly begins the summary of a run whose Dev run was a plan, so a run that
// wrote its plan and changed nothing is not read as one that did the work.
const planOnly = "Plan only (nothing was changed): "

// summary is the one line the run is remembered by (summarize), led by planOnly
// when the Dev run was a plan.
func (in AgentFinish) summary() string {
	line := summarize(in.Outcome, in.Answer, in.Error)
	if in.Plan {
		return planOnly + line
	}
	return line
}

// storedPlan is whether the automation's stored definition starts its Dev run in
// plan mode, for a start that is answered from the session it already recorded
// and so never built the task. The mode is agentTask's, the one place it is set.
func storedPlan(ctx context.Context, s *cloud.Service[state], in AgentStart) bool {
	v, _, err := s.State.store.Definition(ctx, in.Owner, in.FlowID)
	if err != nil {
		return false
	}
	d, ok := definitionOf(&v)
	return ok && agentTask(d.trigger().NextAction.Settings.Input).Mode == modePlan
}

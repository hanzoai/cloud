package agents

import (
	"context"
	"errors"
)

// TaskController is the seam to the hanzoai/tasks durable-execution engine — the
// ONE canonical engine for durable/retriable/scheduled agent work. This sessions
// surface is the REGISTRY + control + ZAP-stream VIEW layer; it deliberately owns
// NO scheduler, ticker, or lease. When a session is backed by a tasks workflow
// (Session.TaskWorkflowID set), a control command forwards through this seam to
// the engine's signal/cancel API instead of only being recorded.
//
// The method set mirrors github.com/hanzoai/tasks/pkg/sdk/client.Client exactly,
// so the live adapter is a thin wrapper (Signal→Client.SignalWorkflow,
// Cancel→Client.CancelWorkflow) with no impedance mismatch:
//
//	SignalWorkflow(ctx, workflowID, runID, signalName string, arg any) error
//	CancelWorkflow(ctx, workflowID, runID string) error
//
// TASKS PLUG-IN POINT. The live controller is wired in Mount from a dialed tasks
// client (client.Dial(TASKS_URL)); until the hanzoai/tasks native engine lands
// (today its workflow opcodes return 501 by design — "the shape is in place so
// callers depend on the API while the engine lands behind it"), the default is
// the disabled controller: control is still durably RECORDED as a session event
// for stream-consuming surfaces, and the forward is a clean no-op.
type TaskController interface {
	// Signal forwards a cooperative control signal (pause/resume/message) to the
	// durable workflow backing a session. name is the signal name; payload is the
	// opaque signal argument (e.g. a steer message), nil when there is none.
	Signal(ctx context.Context, workflowID, runID, name string, payload []byte) error
	// Cancel gracefully cancels the durable workflow backing a session (a stop).
	Cancel(ctx context.Context, workflowID, runID, reason string) error
	// Enabled reports whether a real tasks backend is wired. When false the
	// control endpoints record the intent and skip the forward (honest degrade,
	// same pattern as deps.AI).
	Enabled() bool
}

// errTasksNotConfigured is returned by the disabled controller's Signal/Cancel.
// Control handlers never surface it as a failure — they check Enabled() first —
// but it exists so a mis-wired direct call fails closed with a clear message.
var errTasksNotConfigured = errors.New("agents: tasks durable-execution backend not configured")

// disabledTaskController is the fail-safe default: no engine wired. It records
// nothing and forwards nothing; the control endpoints persist the command as a
// session event regardless, which is what today's stream-consuming surfaces
// (the @hanzo/dev CLI outer agent) act on.
type disabledTaskController struct{}

func (disabledTaskController) Signal(context.Context, string, string, string, []byte) error {
	return errTasksNotConfigured
}
func (disabledTaskController) Cancel(context.Context, string, string, string) error {
	return errTasksNotConfigured
}
func (disabledTaskController) Enabled() bool { return false }

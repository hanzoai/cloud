package functions

// Fleet executor — target=fleet runs the function as an fn.run job on the org's
// OWN linked GPU fleet (hanzo link), through the ONE embedded tasks engine: the
// same org-scoped gpu-jobs queue, the same claim loop, the same result shape a
// direct tasks-API submit gets. No loopback HTTP, no second protocol.
//
// Synchronous by design: invoke blocks until the job completes, bounded by the
// function's TimeoutSec (900s ceiling, matching the sandbox). Longer training
// jobs belong on the tasks API directly, not behind a blocking invoke.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

const (
	fleetNamespace = "gpu-jobs"
	fleetJobType   = "fn.run"
	fleetPoll      = 500 * time.Millisecond
)

// Terminal ACTIVITY_TASK_STATE_* values (the engine's status family).
const (
	fleetStateCompleted = "ACTIVITY_TASK_STATE_COMPLETED"
	fleetStateFailed    = "ACTIVITY_TASK_STATE_FAILED"
	fleetStateCanceled  = "ACTIVITY_TASK_STATE_CANCELED"
)

var errFleetUnconfigured = errors.New("fleet executor: tasks engine not ready")

// engineView is the seam tests replace: the org-scoped embedded-engine view.
var engineView = func(org string) (fleetView, error) {
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil, errFleetUnconfigured
	}
	return eng.View(org), nil
}

// fleetView is the slice of tasksengine.View fleetRun needs.
type fleetView interface {
	RegisterNamespace(ns tasksengine.Namespace) error
	StartActivity(ns, activityID, runID string, typ tasksengine.TypeRef, taskQueue string, input any, retry *tasksengine.RetryPolicy, scheduleToClose, scheduleToStart, startToClose, heartbeat string, identity, requestID string) (*tasksengine.StandaloneActivity, error)
	DescribeActivity(ns, activityID, runID string) (*tasksengine.StandaloneActivity, bool, error)
}

// fleetRun executes one fleet-targeted function invocation and maps the job
// outcome onto the sandbox execResult shape, so metering/recording downstream
// cannot tell the executors apart.
func fleetRun(ctx context.Context, org string, f Function, input string) (execResult, error) {
	v, err := engineView(org)
	if err != nil {
		return execResult{}, err
	}
	// Idempotent: the worker's register() creates the same namespace for claims.
	_ = v.RegisterNamespace(tasksengine.Namespace{
		NamespaceInfo: tasksengine.NamespaceInfo{Name: fleetNamespace},
	})

	timeout := f.TimeoutSec
	if timeout <= 0 {
		timeout = 30
	} else if timeout > 900 {
		timeout = 900
	}
	id := fleetJobID(f.Name)
	payload := map[string]any{
		"name":           f.Name,
		"script":         f.Code,
		"input":          input,
		"timeoutSeconds": timeout,
	}
	if _, err := v.StartActivity(fleetNamespace, id, id,
		tasksengine.TypeRef{Name: fleetJobType}, fleetNamespace, payload,
		nil, "", fmt.Sprintf("%ds", timeout), "", "120s", "functions", id); err != nil {
		return execResult{}, fmt.Errorf("fleet enqueue: %w", err)
	}

	deadline := time.NewTimer(time.Duration(timeout) * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(fleetPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return execResult{}, ctx.Err()
		case <-deadline.C:
			return execResult{StatusCode: 504, Errout: "fleet job timed out (no worker or job overran)"}, nil
		case <-tick.C:
		}
		act, ok, err := v.DescribeActivity(fleetNamespace, id, id)
		if err != nil || !ok {
			continue // transient read miss — the deadline bounds us
		}
		switch act.Status {
		case fleetStateCompleted:
			return fleetResult(act.Result), nil
		case fleetStateFailed, fleetStateCanceled:
			return execResult{StatusCode: 500, Errout: strings.TrimSpace(act.FailureCause)}, nil
		}
	}
}

// fleetResult maps the fn.run result payload ({output, exitCode, ...}) onto the
// executor contract.
func fleetResult(result any) execResult {
	res := execResult{StatusCode: 200, Ok: true}
	b, err := json.Marshal(result)
	if err != nil {
		return res
	}
	var m struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exitCode"`
	}
	if json.Unmarshal(b, &m) == nil {
		res.Output = m.Output
		if m.ExitCode != 0 {
			res.Ok = false
			res.StatusCode = 500
		}
	}
	return res
}

// fleetJobID is unique per invocation ("fn-<name>-<nonce>") — activity ids are
// engine keys, and an invoke must never collide with a prior run.
func fleetJobID(name string) string {
	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	return "fn-" + strings.ToLower(name) + "-" + hex.EncodeToString(nonce[:])
}

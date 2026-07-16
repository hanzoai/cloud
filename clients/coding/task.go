package coding

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud/clients/runtime"
)

// task.go is coding's WIRE CONTRACT with the bot runtime — the stub behind the
// Runner seam. It says WHAT coding asks the runtime to do (run one coding job)
// and how a progress line is shaped; how the bytes get there is runtime's problem.
//
// This file is the ONE place in clients/coding that knows the runtime exists.
// coding.go (the orchestrator) is pure and never sees it.
//
// The runtime answers a stream: one message per step/log as the job progresses
// (clone → dev exec → commit → push), then a terminal result (or error). Coding
// mirrors every step into the agent session live, so the run is watchable at
// GET /v1/agents/sessions/:id/stream, and returns the terminal outcome.
//
// CREDENTIAL CUSTODY: the per-org agent git credential travels in the request
// BODY (never a URL, never argv, never a log). That is why the call declares
// Secret — the transport then refuses to carry it over a cleartext hop.

// taskOp addresses the runtime's coding-task operation.
const taskOp = "/v1/coding-tasks"

// credential is the per-org agent git credential the sandbox presents to native
// git. Token is the secret (an hk- key); Username is the basic-auth user label.
// Encoded into the request body only — never logged.
type credential struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// taskRequest is the cloud→runtime body for one coding run.
type taskRequest struct {
	CloneURL          string     `json:"cloneUrl"`          // https://<domain>/v1/git/<org>/<repo>.git
	BaseBranch        string     `json:"baseBranch"`        // branch to start from (default repo default)
	Branch            string     `json:"branch"`            // branch to create + push (e.g. agent/<sessionid>)
	Prompt            string     `json:"prompt"`            // the engineering task
	SessionID         string     `json:"sessionId"`         // cloud session id (correlation)
	RunTimeoutSeconds int        `json:"runTimeoutSeconds"` // sandbox run budget
	Credential        credential `json:"credential"`        // agent git credential (write-only)
}

// message is the discriminated shape of one streamed line: step/log while the job
// runs, result/error to end it.
type message struct {
	Type      string `json:"type"` // step | log | result | error
	Step      string `json:"step,omitempty"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSha string `json:"commitSha,omitempty"`
	Diffstat  string `json:"diffstat,omitempty"`
	Changed   bool   `json:"changed,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	LogTail   string `json:"logTail,omitempty"`
}

// runner is the Runner seam over the real runtime. Its fake twin in coding_test.go
// is what the orchestrator is tested against.
type runner struct{}

// Run hands one coding job to the runtime and streams its progress, invoking
// onStep for each line as it arrives, then returns the terminal result. A
// transport failure, or a stream that ends without a terminal message, is an
// error — no partial success is fabricated. org/userID are the tenant context the
// runtime trusts AFTER its own bearer gate.
func (runner) Run(ctx context.Context, org, userID string, req RunRequest, onStep func(Step)) (RunResult, error) {
	var out RunResult
	var terminal bool
	err := runtime.Stream(ctx, runtime.Call{
		Op:   taskOp,
		Org:  org,
		User: userID,
		Body: taskRequest{
			CloneURL: req.CloneURL, BaseBranch: req.BaseBranch, Branch: req.Branch,
			Prompt: req.Prompt, SessionID: req.SessionID, RunTimeoutSeconds: req.RunTimeoutSeconds,
			Credential: credential{Username: req.CredUser, Token: req.CredToken},
		},
		Secret: true, // the body carries the org's git credential
	}, func(msg []byte) {
		var m message
		if json.Unmarshal(msg, &m) != nil {
			return // skip a malformed message rather than abort the whole run
		}
		switch m.Type {
		case "result":
			out = RunResult{
				Branch: m.Branch, CommitSha: m.CommitSha, Diffstat: m.Diffstat,
				Changed: m.Changed, OK: m.OK, LogTail: m.LogTail,
			}
			terminal = true
		case "error":
			out = RunResult{OK: false, LogTail: m.LogTail, Error: nonEmpty(m.Message, "coding task failed")}
			terminal = true
		default: // step | log — mirror live
			if onStep != nil {
				onStep(Step{Type: m.Type, Step: m.Step, Message: m.Message, Status: m.Status})
			}
		}
	})
	if err != nil {
		return RunResult{}, fmt.Errorf("coding: run task: %w", err)
	}
	if !terminal {
		return RunResult{}, fmt.Errorf("coding: stream ended without a result")
	}
	return out, nil
}

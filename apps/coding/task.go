package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud/apps/bots"
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

// taskOp addresses the runtime's sandbox-run operation.
const taskOp = "/v1/coding-tasks"

// sandboxURL is WHERE A RUN GOES, and it is deliberately not the bot address.
//
// A sandbox is not the bot. Coding, deep research and bare exec all want the
// same thing — a computer to run something in — and none of them wants the
// service that runs Slack channels. BOT_GATEWAY_URL is the right name for bot
// traffic and stays that; this is the name for a sandbox.
//
// The old name is accepted for ONE release so a deploy cannot half-land, then it
// is deleted. Not a permanent alias: two live names for one address is how the
// two ends stop agreeing about where a run went, with nothing in a log to say so.
// Empty here means the transport's own default, which today is the same pod.
func sandboxURL() string {
	for _, k := range []string{"SANDBOX_URL", "BOT_GATEWAY_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// credential is the per-org agent git credential the sandbox presents to native
// git. Token is the secret (an sk- key); Username is the basic-auth user label.
// Encoded into the request body only — never logged.
type credential struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// taskRequest is the cloud→runtime body for one sandbox run.
//
// EVERY GIT FIELD IS omitempty, AND THAT IS THE CONTRACT, NOT A TIDINESS
// PREFERENCE. A run with no repo must put NO credential on the wire at all —
// not an empty one. `credential` is a pointer for the same reason: a value type
// would always marshal, so "no repo" would still ship a `credential` object and
// the runtime could not tell an absent grant from a blank one.
type taskRequest struct {
	Prompt            string      `json:"prompt"`               // the task
	Tool              string      `json:"tool,omitempty"`       // dev|claude|codex|python|node (default dev)
	Desktop           bool        `json:"desktop,omitempty"`    // select the xvfb image variant
	SessionID         string      `json:"sessionId"`            // cloud session id (correlation)
	RunTimeoutSeconds int         `json:"runTimeoutSeconds"`    // sandbox run budget
	CloneURL          string      `json:"cloneUrl,omitempty"`   // https://<domain>/v1/git/<org>/<repo>.git
	BaseBranch        string      `json:"baseBranch,omitempty"` // branch to start from (default repo default)
	Branch            string      `json:"branch,omitempty"`     // branch to create + push (e.g. agent/<sessionid>)
	Credential        *credential `json:"credential,omitempty"` // agent git credential (write-only); nil when there is no repo
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
	body := taskRequest{
		Prompt: req.Prompt, Tool: req.Tool, Desktop: req.Desktop,
		SessionID: req.SessionID, RunTimeoutSeconds: req.RunTimeoutSeconds,
	}
	// The git half travels together or not at all — there is no path here that
	// puts a credential on the wire without the repo it belongs to. A caller
	// that supplies one anyway is REFUSED rather than quietly trimmed: silently
	// dropping a secret hides the bug that minted it, and the runtime says the
	// same thing at its own boundary, so the two ends agree.
	if req.CloneURL == "" && (req.CredToken != "" || req.CredUser != "") {
		return out, errors.New("coding: a credential without a repo cannot be used, and must not be sent")
	}
	if req.CloneURL != "" {
		body.CloneURL, body.BaseBranch, body.Branch = req.CloneURL, req.BaseBranch, req.Branch
		body.Credential = &credential{Username: req.CredUser, Token: req.CredToken}
	}
	err := bots.Stream(ctx, bots.Call{
		Op:   taskOp,
		Org:  org,
		User: userID,
		Base: sandboxURL(),
		Body: body,
		// Secret ONLY when the body actually carries the org's git credential.
		// A run with no repo has no secret to protect, so it must not be refused
		// by the cleartext guard that exists to protect one.
		Secret: body.Credential != nil,
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

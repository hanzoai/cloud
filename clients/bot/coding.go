package bot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// coding.go is the IN-PROCESS client for bot-gateway's native coding-task runner
// (POST /v1/coding-tasks). It is the ONE wire seam cloud uses to hand a coding job
// to the sandbox runtime — distinct from the /v1/bot/* reverse proxy (bot.go),
// which relays an inbound browser/API request. Here cloud ORIGINATES the call
// server-side (from a Slack coding trigger), so it mints the identity headers
// itself (X-Org-Id / X-User-Id) and presents the shared gateway service token as
// the pod-boundary bearer.
//
// The transport is a line-delimited JSON (NDJSON) stream: bot-gateway emits one
// JSON object per line as the run progresses (clone → dev exec → commit → push),
// each a `step`/`log`, and a final `result` (or `error`) line. Cloud mirrors every
// step into the agent session as it arrives, so `GET /v1/agents/sessions/:id/stream`
// carries the run live, and returns the terminal result.
//
// CREDENTIAL CUSTODY: the per-org agent git credential travels in the request
// BODY (never on a URL, never on argv, never logged). bot-gateway injects it into
// git via an env-fed http.extraHeader inside the sandbox; cloud never logs the
// Credential field and this client never places it in an error string.

// Credential is the per-org agent git credential the sandbox presents to native
// git. Token is the secret (an hk- key); Username is the basic-auth user label.
// Marshaled into the request body only — never logged.
type Credential struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// CodingTaskRequest is the cloud→bot contract for one coding run.
type CodingTaskRequest struct {
	CloneURL          string     `json:"cloneUrl"`          // https://<domain>/v1/git/<org>/<repo>.git
	BaseBranch        string     `json:"baseBranch"`        // branch to start from (default repo default)
	Branch            string     `json:"branch"`            // branch to create + push (e.g. agent/<sessionid>)
	Prompt            string     `json:"prompt"`            // the engineering task
	SessionID         string     `json:"sessionId"`         // cloud session id (correlation)
	RunTimeoutSeconds int        `json:"runTimeoutSeconds"` // sandbox run budget
	Credential        Credential `json:"credential"`        // agent git credential (write-only)
}

// CodingStep is one progress event mirrored into the session. Type is "step" or
// "log"; Step names the phase (clone|plan|edit|commit|push); Status is ok|error
// for a completed phase. All fields are safe to surface (no credential).
type CodingStep struct {
	Type    string `json:"type"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
	Status  string `json:"status,omitempty"`
}

// CodingTaskResult is the terminal outcome bot-gateway reports.
type CodingTaskResult struct {
	Branch    string `json:"branch"`
	CommitSha string `json:"commitSha"`
	Diffstat  string `json:"diffstat"`
	Changed   bool   `json:"changed"`
	OK        bool   `json:"ok"`
	LogTail   string `json:"logTail"`
	Error     string `json:"error,omitempty"`
}

// codingEnvelope is the discriminated line shape: one of step/log/result/error.
type codingEnvelope struct {
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

const (
	// codingLineCap bounds one NDJSON line (a diffstat/log line can be large but
	// never unbounded) so a hostile/huge line can't exhaust cloud memory.
	codingLineCap = 1 << 20 // 1 MiB per line
	// codingErrBodyCap bounds the non-2xx error body we read for a message.
	codingErrBodyCap = 64 << 10
)

// codingHTTP has NO client-side timeout: a coding run legitimately streams for
// minutes. The deadline is the caller's ctx (bounded by the coding job), applied
// to the request; a stuck stream is cut when ctx expires.
var codingHTTP = &http.Client{}

// RunCodingTask POSTs a coding job to bot-gateway and streams the NDJSON result,
// invoking onStep for each progress line as it arrives. It returns the terminal
// result. A non-2xx status, a transport error, or a stream that ends without a
// terminal line is an error (no partial success is fabricated). org/userID are
// the gateway-minted tenant context bot-gateway trusts AFTER its own bearer gate.
func RunCodingTask(ctx context.Context, org, userID string, req CodingTaskRequest, onStep func(CodingStep)) (CodingTaskResult, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return CodingTaskResult{}, fmt.Errorf("bot: marshal coding request: %w", err)
	}
	target := botURL() + "/v1/coding-tasks"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return CodingTaskResult{}, fmt.Errorf("bot: build coding request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/x-ndjson")
	// Server-originated identity: cloud has already resolved the tenant, so it mints
	// the headers bot-gateway trusts (post pod-boundary auth). The service bearer is
	// the shared gateway token (KMS-injected env); absent => bot-gateway fails the
	// request closed at its auth gate.
	hreq.Header.Set("X-Org-Id", org)
	if userID != "" {
		hreq.Header.Set("X-User-Id", userID)
	}
	if tok := getenv("BOT_GATEWAY_TOKEN"); tok != "" {
		hreq.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := codingHTTP.Do(hreq)
	if err != nil {
		return CodingTaskResult{}, fmt.Errorf("bot: coding gateway unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, codingErrBodyCap))
		return CodingTaskResult{}, fmt.Errorf("bot: coding gateway status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), codingLineCap)
	var result CodingTaskResult
	var gotTerminal bool
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env codingEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue // skip a malformed line rather than abort the whole run
		}
		switch env.Type {
		case "result":
			result = CodingTaskResult{
				Branch: env.Branch, CommitSha: env.CommitSha, Diffstat: env.Diffstat,
				Changed: env.Changed, OK: env.OK, LogTail: env.LogTail,
			}
			gotTerminal = true
		case "error":
			result = CodingTaskResult{OK: false, LogTail: env.LogTail, Error: nonEmpty(env.Message, "coding task failed")}
			gotTerminal = true
		default: // step | log — mirror live
			if onStep != nil {
				onStep(CodingStep{Type: env.Type, Step: env.Step, Message: env.Message, Status: env.Status})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return CodingTaskResult{}, fmt.Errorf("bot: coding stream read: %w", err)
	}
	if !gotTerminal {
		return CodingTaskResult{}, fmt.Errorf("bot: coding stream ended without a result")
	}
	return result, nil
}

// nonEmpty returns a when non-empty, else b.
func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

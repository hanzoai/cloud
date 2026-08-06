// The coding agent, streamed.
//
// This is the endpoint apps/coding has been dispatching at since before there
// was anything to dispatch to. Its frames are `wire.Frame`, whose JSON tags are
// byte-identical to apps/coding/task.go's own `message` struct — which is what
// lets apps/coding rebind its Runner seam from the bot gateway to a box without
// its orchestrator or its tests moving.
//
// `@hanzo/dev` already emits a JSONL event stream under `dev exec --json`. We
// TRANSLATE that stream; we do not re-implement an agent loop. If a frame shape
// here starts growing agent semantics, the translation has become a second
// agent and should be deleted.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/sandbox/wire"

	"github.com/zap-proto/zip"
)

// agentRun runs one prompt and streams NDJSON. It always ends with exactly one
// terminal frame (result or error) — apps/coding treats a stream that ends
// without one as a failure, and it is right to.
func (b *box) agentRun(c *zip.Ctx) error {
	var req wire.AgentRunRequest
	if err := c.Bind(&req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "%s", "body: "+err.Error())
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return zip.Errorf(http.StatusBadRequest, "%s", "prompt required")
	}
	cwd := b.workdir
	if req.Cwd != "" {
		abs, ok := b.resolve(req.Cwd)
		if !ok {
			return zip.Errorf(http.StatusBadRequest, "%s", "cwd escapes the project")
		}
		cwd = abs
	}
	sec := req.TimeoutSec
	if sec <= 0 {
		sec = envInt("BOX_AGENT_TIMEOUT_SEC", 1800)
	}

	c.SetHeader("Content-Type", "application/x-ndjson")
	c.SetHeader("Cache-Control", "no-store")

	// The body is a STREAM, so it is written from inside zip's stream seam
	// rather than to a captured ResponseWriter. Every `return` below ends the
	// stream exactly where the old handler ended the response — the terminal
	// frame has already been sent by then, which is the contract apps/coding
	// relies on.
	return c.SendStreamWriter(func(w *bufio.Writer) {
		enc := json.NewEncoder(w)
		send := func(f wire.Frame) {
			_ = enc.Encode(f)
			_ = w.Flush()
		}

		ctx, cancel := context.WithTimeout(c.Context(), time.Duration(sec)*time.Second)
		defer cancel()

		send(wire.Frame{Type: "step", Step: "agent", Status: "running", Message: "starting @hanzo/dev"})

		// `dev exec --json` is the non-interactive mode; the prompt is an argument,
		// never a shell string, so nothing in it is interpreted by a shell.
		cmd := exec.CommandContext(ctx, agentBin(), "exec", "--json", "--skip-git-repo-check", req.Prompt)
		cmd.Dir = cwd
		cmd.Env = b.env(map[string]string{"HANZO_SESSION_ID": req.SessionID})

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			send(wire.Frame{Type: "error", Message: "agent stdout: " + err.Error()})
			return
		}
		var tail cappedBuf
		tail.max = 64 << 10
		cmd.Stderr = &tail

		if err := cmd.Start(); err != nil {
			// The commonest cause by far is an image built without @hanzo/dev, which
			// is exactly the gap this whole effort exists to close. Say so plainly
			// rather than emitting "exec format error" and letting someone guess.
			send(wire.Frame{Type: "error", Message: "agent not runnable in this box: " + err.Error(), LogTail: tail.String()})
			return
		}

		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
		for sc.Scan() {
			if f, ok := translate(sc.Bytes()); ok {
				send(f)
			}
		}
		runErr := cmd.Wait()

		if runErr != nil {
			msg := "agent run failed: " + runErr.Error()
			if ctx.Err() == context.DeadlineExceeded {
				msg = "agent run timed out"
			}
			send(wire.Frame{Type: "error", Message: msg, LogTail: tail.String()})
			return
		}
		send(wire.Frame{Type: "result", OK: true, LogTail: tail.String()})
	})
}

func agentBin() string { return envOr("BOX_AGENT_BIN", "dev") }

// translate maps one `dev exec --json` event line onto a wire.Frame.
//
// It is deliberately LOSSY and deliberately total: dev's event vocabulary is
// its own and will grow, and a box that drops a run because it met an event
// name it did not recognise would be worse than useless. Anything unrecognised
// becomes a `log` frame carrying the raw line, which is still watchable at
// GET /v1/agents/sessions/:id/stream.
func translate(line []byte) (wire.Frame, bool) {
	s := strings.TrimSpace(string(line))
	if s == "" {
		return wire.Frame{}, false
	}
	var ev struct {
		Type string `json:"type"`
		Msg  struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Text    string `json:"text"`
			Command any    `json:"command"`
		} `json:"msg"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return wire.Frame{Type: "log", Message: truncate(s, 4000)}, true
	}
	kind := firstNonEmpty(ev.Msg.Type, ev.Type)
	switch {
	case strings.Contains(kind, "exec_command"), strings.Contains(kind, "patch_apply"), strings.Contains(kind, "tool"):
		return wire.Frame{Type: "step", Step: kind, Status: "running",
			Message: truncate(firstNonEmpty(ev.Msg.Message, ev.Msg.Text, s), 2000)}, true
	case strings.Contains(kind, "error"):
		return wire.Frame{Type: "log", Step: kind,
			Message: truncate(firstNonEmpty(ev.Msg.Message, ev.Msg.Text, s), 4000)}, true
	default:
		return wire.Frame{Type: "log", Step: kind,
			Message: truncate(firstNonEmpty(ev.Msg.Message, ev.Msg.Text, s), 4000)}, true
	}
}

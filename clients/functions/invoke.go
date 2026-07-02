package functions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// execClient delegates function execution to the sandboxed code executor. This
// binary NEVER runs tenant code in-process (mirrors clients/exec): it POSTs the
// function's runtime + source + input to CODE_EXEC_UPSTREAM with the KMS-sourced
// service key on X-API-Key. When the upstream is unset, invoke fails closed —
// no execution, no fabricated output.
//
// The upstream + key are the SAME operator-set, KMS-synced env the exec
// subsystem uses (CODE_EXEC_UPSTREAM / CODE_EXEC_API_KEY), so there is one
// sandbox and one service credential across the binary. The target URL is
// operator-controlled (never derived from tenant input), so invoke cannot be
// steered at internal hosts (no SSRF from the function name or payload).
type execClient struct {
	upstream string
	apiKey   string
	http     *http.Client
}

func newExecClient() *execClient {
	return &execClient{
		upstream: strings.TrimRight(strings.TrimSpace(os.Getenv("CODE_EXEC_UPSTREAM")), "/"),
		apiKey:   strings.TrimSpace(os.Getenv("CODE_EXEC_API_KEY")),
		http:     &http.Client{},
	}
}

func (e *execClient) configured() bool { return e.upstream != "" }

// langFor maps a registry runtime to the executor's language id.
func langFor(runtime string) string {
	switch runtime {
	case "python":
		return "py"
	case "node":
		return "js"
	case "deno":
		return "ts"
	default:
		return runtime
	}
}

type execResult struct {
	StatusCode int
	Output     string
	Errout     string
	Ok         bool
}

// run executes one function on the sandbox and returns the outcome. Errors are
// returned as (result, err) with result carrying whatever the sandbox produced;
// the caller records the invocation regardless.
func (e *execClient) run(ctx context.Context, f Function, input string, timeoutSec int) (execResult, error) {
	if !e.configured() {
		return execResult{}, errExecUnconfigured
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	} else if timeoutSec > 900 {
		timeoutSec = 900
	}
	rctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// LibreChat code-interpreter shape (the contract clients/exec proxies). args
	// carries the caller input on stdin; env NAMES are declared but values are
	// resolved sandbox-side from the mounted secret refs — never sent from here.
	payload := map[string]any{
		"lang": langFor(f.Runtime),
		"code": f.Code,
		"args": []string{input},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, e.upstream+"/exec", bytes.NewReader(body))
	if err != nil {
		return execResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("X-API-Key", e.apiKey)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return execResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	res := execResult{StatusCode: resp.StatusCode, Ok: resp.StatusCode >= 200 && resp.StatusCode < 300}
	res.Output, res.Errout = parseExecBody(raw)
	if res.Errout != "" {
		res.Ok = false
	}
	return res, nil
}

// parseExecBody defensively pulls stdout/stderr from the executor response
// across the shape variants (stdout/output/result, stderr/error). A rename
// upstream degrades to the raw body, never a throw.
func parseExecBody(raw []byte) (out, errout string) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return strings.TrimSpace(string(raw)), ""
	}
	// Some executors nest under "run".
	if run, ok := m["run"].(map[string]any); ok {
		out = firstStr(run, "stdout", "output", "result")
		errout = firstStr(run, "stderr", "error")
		if out != "" || errout != "" {
			return out, errout
		}
	}
	out = firstStr(m, "stdout", "output", "result", "logs")
	errout = firstStr(m, "stderr", "error")
	return out, errout
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var errExecUnconfigured = errors.New("code execution runtime not configured")

// invoke runs a function and records a REAL invocation. Fail-closed when the
// sandbox is unconfigured (503, nothing recorded, nothing fabricated).
func (s *svc) invoke(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	f, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("function not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if !s.exec.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "code execution runtime not configured on this deployment")
	}
	var body struct {
		Input string `json:"input"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}

	start := time.Now()
	res, runErr := s.exec.run(c.Context(), f, body.Input, f.TimeoutSec)
	dur := time.Since(start).Milliseconds()

	id, _ := genID("inv")
	iv := Invocation{
		ID: id, Org: org, FunctionName: name, Method: "POST",
		DurationMs: dur, CreatedAt: time.Now().Unix(),
		StatusCode: res.StatusCode, Output: truncate(res.Output, 64*1024),
	}
	switch {
	case runErr != nil:
		iv.Status = "error"
		iv.Error = runErr.Error()
		if iv.StatusCode == 0 {
			iv.StatusCode = http.StatusBadGateway
		}
	case res.Ok:
		iv.Status = "ok"
	default:
		iv.Status = "error"
		iv.Error = truncate(res.Errout, 16*1024)
	}
	if err := s.store.InsertInvocation(c.Context(), iv); err != nil {
		s.log.Warn("record invocation failed", "org", org, "fn", name, "err", err)
	}
	code := http.StatusOK
	if iv.Status != "ok" {
		code = http.StatusBadGateway
	}
	return c.JSON(code, invocationView{
		ID: iv.ID, StatusCode: iv.StatusCode, Status: iv.Status, Method: iv.Method,
		Time: rfc3339(iv.CreatedAt), DurationMs: iv.DurationMs,
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

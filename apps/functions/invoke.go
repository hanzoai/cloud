package functions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/exec"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// execClient runs a function's code in a SANDBOX, through apps/exec.
//
// "A function invoke is a sandbox with a seconds-long lease" — the sandbox package
// doc says so, and this is that sentence in code. What it replaces is an HTTP client
// to CODE_EXEC_UPSTREAM, whose Service (code-exec.hanzo) had zero endpoints: every
// non-fleet invoke in production was a 502 against a workload nobody deployed.
//
// It composes over apps/exec rather than over apps/sandbox directly, and that is
// deliberate. "Run this snippet in this language and tell me what it printed" is one
// operation, and exec already owns it — the language table, the artifact sweep, the
// session rule. A second copy here would be a second table to keep in step with the
// image, which is precisely how the two consumers of CODE_EXEC_UPSTREAM ended up
// asking the same executor for two different paths.
//
// The lease ENDS with the call. A code-interpreter session outlives its run because
// the reply hands back an id the caller addresses next; a function invoke is over
// the moment it answers, so holding the pod until the reaper notices would be
// fifteen idle minutes of a node per invocation.
type execClient struct{}

func newExecClient() *execClient { return &execClient{} }

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

// run executes one function in a sandbox and returns the outcome. Errors are
// returned as (result, err) with result carrying whatever the sandbox produced; the
// caller records the invocation regardless.
//
// A deployment with no sandboxes app answers 503 rather than pretending: plane.Ask
// distinguishes "not deployed here" from "deployed and failing", and only the first
// is a configuration fact.
func (e *execClient) run(ctx context.Context, f Function, input string, timeoutSec int) (execResult, error) {
	if timeoutSec <= 0 {
		timeoutSec = 30
	} else if timeoutSec > 900 {
		timeoutSec = 900
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		org = f.Org
	}
	rctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// The caller's input rides as the program's first argument, which is the shape
	// the code-interpreter contract already carries it in (`args`). Nothing about the
	// function's environment is sent from here: env NAMES are declared on the
	// registry row and their VALUES are resolved sandbox-side from mounted refs.
	res, err := exec.Run(rctx, org, &exec.CodeRun{
		Lang: langFor(f.Runtime), Code: f.Code, Args: []string{input}})
	if err != nil {
		if errors.Is(err, plane.ErrNoPeer) {
			return execResult{}, errExecUnconfigured
		}
		return execResult{}, err
	}
	// End the lease on the context that OUTLIVES the run's timeout. Using rctx would
	// mean a function that ran to its own deadline leaves its pod behind, which is
	// the exact leak this call exists to prevent.
	if derr := exec.End(ctx, org, res.SessionID); derr != nil {
		mounted.Log.Warn("end function sandbox", "org", org, "fn", f.Name, "err", derr)
	}
	out := execResult{StatusCode: http.StatusOK, Output: res.Stdout, Errout: res.Stderr}
	out.Ok = strings.TrimSpace(res.Stderr) == ""
	return out, nil
}

var errExecUnconfigured = errors.New("code execution runtime not deployed here")

// invoke runs a function and records a REAL invocation. Fail-closed when the
// sandbox is unconfigured (503, nothing recorded, nothing fabricated).
func invoke(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := nameParam(c)
	f, err := store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("function not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body struct {
		Input string `json:"input"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}

	// Pre-invoke balance gate (fail-closed, per-org). Refuse BEFORE any sandbox
	// compute runs: an unfunded org — or, in the default fail-closed posture, an
	// unreachable commerce — gets 402/503 and nothing executes (no free compute).
	// Scoped to THIS caller's org (the same slug that owns the function), so the
	// charge can never target another org. fee is computed once and reused by
	// the post-success debit; fee==0 or unconfigured billing makes this a no-op.
	fee := cloud.ResourceFeeCents(invokeFeeEnvPrefix, "invoke")
	project, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, "invoke", fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	start := time.Now()
	var res execResult
	var runErr error
	if f.Target == "fleet" {
		res, runErr = fleetRun(c.Context(), org, f, body.Input)
	} else {
		res, runErr = s.State.exec.run(c.Context(), f, body.Input, f.TimeoutSec)
	}
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
	if err := store.InsertInvocation(c.Context(), iv); err != nil {
		s.Log.Warn("record invocation failed", "org", org, "fn", name, "err", err)
	}
	// Debit the caller's org ledger when the sandbox ACTUALLY executed — real
	// compute was consumed even if the org's own code exited non-zero (that
	// is a successful invocation of a failing program, not a billing failure).
	// A transport failure (runErr != nil: sandbox unreachable/timeout) ran no
	// billable compute, so it is NOT charged. Per-org, env-attributed, async
	// best-effort so the debit never blocks or corrupts this response; a debit
	// failure is logged for reconciliation.
	//
	// Two-part serverless billing, both on the ONE shared meter:
	//   (1) the flat per-invocation REQUEST fee (gated pre-run above), and
	//   (2) a usage-native COMPUTE debit = GB-seconds (DurationMs × configured
	//       memory), priced by CLOUD_FUNCTION_GBSEC_CENTS.
	// Either is independently free (fee 0 → no-op), so an operator can bill by
	// request alone, compute alone, or both.
	if runErr == nil {
		s.Bill.Meter(principal.Ledger(c), project, "invoke", fee, c.RequestID(), cloud.ClientIP(c))
		gbSecCents := gbSecondsCents(dur, memLimitMB(f.MemoryLimit), cloud.ResourceFeeCents(gbSecFeeEnvPrefix, "gbsec"))
		s.Bill.MeterUsage(principal.Ledger(c), "gbsec", metering.Usage{
			Model:       "gbsec", // the billed unit: GB-seconds of compute.
			AmountCents: gbSecCents,
			Project:     project,
			RequestID:   c.RequestID(),
			ClientIP:    cloud.ClientIP(c),
		})
	}
	code := http.StatusOK
	switch {
	case errors.Is(runErr, errExecUnconfigured):
		// "This deployment cannot run code" is not "the run failed" — it is a
		// deployment fact, and 503 is the status a caller retries against a healthy
		// replica on. 502 would say the sandbox answered badly, which it did not: it
		// is not here.
		code = http.StatusServiceUnavailable
	case iv.Status != "ok":
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

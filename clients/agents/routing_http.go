package agents

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// routing_http.go is the machine-facing surface a `hanzo code --serve` daemon
// uses to CLAIM and complete routed runs. Every route is BOTH org-scoped (the
// gateway-minted X-Org-Id, exactly like the rest of the targets plane) AND
// machine-authenticated (the target claim key in X-Target-Key): a caller must
// prove it is acting in the target's org and that it holds that specific
// machine's capability. A run offered to target X is never reachable from a claim
// for target Y, and a claim for another org's target 404s at the org boundary.
//
//	POST /v1/agents/targets/:id/claim-key            mint/rotate this target's claim key -> {claimKey}
//	POST /v1/agents/targets/:id/claim                long-poll for the next routed run (X-Target-Key)
//	POST /v1/agents/targets/:id/runs/:runId/report   report a routed run's terminal result (X-Target-Key)

// claimLongPoll bounds one Claim wait; on expiry the daemon gets 204 and re-polls
// immediately, which also refreshes its serving liveness. A var (not a const) so a
// test can shrink the empty-poll window without waiting the full window.
var claimLongPoll = 25 * time.Second

const (
	// claimKeyHeader carries the machine capability. Distinct from Authorization
	// (which carries the org bearer): org identity and machine identity are two
	// independent proofs, both required.
	claimKeyHeader = "X-Target-Key"
	maxReportField = 64 << 10
)

// mountRouting registers the route-work machine surface. Called from mountTargets
// AFTER the target CRUD routes so the extra-segment paths are unambiguous.
func mountRouting(s *cloud.Service[state], app *zip.App) {
	app.Post("/v1/agents/targets/:id/claim-key", cloud.Handle(s, mintClaimKey))
	app.Post("/v1/agents/targets/:id/claim", cloud.Handle(s, claimRoutedRun))
	app.Post("/v1/agents/targets/:id/runs/:runId/report", cloud.Handle(s, reportRoutedRun))
}

// mintClaimKey (re)mints the target's claim key and returns it ONCE. Only the
// SHA-256 hash is stored. Org-scoped: only a caller in the target's org can mint,
// and the key is bound to (org, target). Rotating supersedes any prior daemon.
func mintClaimKey(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	// The target must exist in this org before it can carry a capability.
	if _, err := s.State.store.GetTarget(c.Context(), org, id); err == errTargetNotFound {
		return zip.ErrNotFound("target not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "target: %v", err)
	}
	key, err := newClaimKey()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.UpsertClaimKeyHash(c.Context(), org, id, hashClaimKey(key), time.Now().Unix()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"targetId": id, "claimKey": key})
}

// claimRoutedRun authenticates the machine, refreshes its serving liveness, and
// long-polls the rendezvous for the next run addressed to THIS (org, target).
// 200 + the run on a claim; 204 when the poll window elapses with no work.
func claimRoutedRun(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	if err := s.State.store.verifyClaimKey(c.Context(), org, id, c.Header(claimKeyHeader)); err != nil {
		return claimAuthError(err)
	}
	// The poll itself is the runner's liveness proof — stamp it so the dispatch
	// gate (TargetDispatchable) sees a live runner. Best-effort.
	_ = s.State.store.StampServing(c.Context(), org, id, time.Now().Unix())

	ctx, cancel := context.WithTimeout(c.Context(), claimLongPoll)
	defer cancel()
	run, got := routedMailbox.Claim(ctx, org, id)
	if !got {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, routedRunView(run))
}

type reportReq struct {
	OK        bool   `json:"ok"`
	Changed   bool   `json:"changed"`
	Branch    string `json:"branch"`
	CommitSha string `json:"commitSha"`
	Diffstat  string `json:"diffstat"`
	Error     string `json:"error"`
}

// reportRoutedRun completes a claimed run: it delivers the terminal result to the
// run's durable owner (the RoutedRunWorkflow activity), which lets the workflow
// finish. Scoped to (org, target, runId) AND claim-key-authenticated, so a
// machine can only ever report a run it legitimately holds. Idempotent: a report
// for an unknown/already-finished run is a clean no-op (the session terminal was
// already set by the machine's own stream).
func reportRoutedRun(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	runID := strings.TrimSpace(c.Param("runId"))
	if err := s.State.store.verifyClaimKey(c.Context(), org, id, c.Header(claimKeyHeader)); err != nil {
		return claimAuthError(err)
	}
	var body reportReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	res := RoutedResult{
		OK: body.OK, Changed: body.Changed,
		Branch:    clampStr(body.Branch, maxRepo),
		CommitSha: clampStr(body.CommitSha, 128),
		Diffstat:  clampStr(body.Diffstat, maxReportField),
		Error:     clampStr(body.Error, maxReportField),
	}
	delivered := routedMailbox.Report(org, id, runID, res)
	return c.JSON(http.StatusOK, map[string]any{"delivered": delivered})
}

// claimAuthError maps the claim-key verdict onto a fail-closed HTTP status. A
// missing target row, a missing/mismatched key, and an unknown org all collapse
// to 403 so the surface never distinguishes "wrong key" from "no such target" —
// an unauthorized caller learns nothing about what exists.
func claimAuthError(err error) error {
	switch err {
	case errNoClaimKey, errClaimKeyBad, errTargetNotFound:
		return zip.ErrForbidden("target claim rejected")
	default:
		return zip.Errorf(http.StatusInternalServerError, "claim auth: %v", err)
	}
}

// routedRunView is the non-secret run spec handed to the machine. It carries no
// credential by design — the machine authenticates git + model routing with its
// own already-held credentials.
func routedRunView(run RoutedRun) map[string]any {
	return map[string]any{
		"sessionId":      run.SessionID,
		"repo":           run.Repo,
		"project":        run.Project,
		"base":           run.Base,
		"branch":         run.Branch,
		"prompt":         run.Prompt,
		"cloneUrl":       run.CloneURL,
		"timeoutSeconds": run.TimeoutSeconds,
	}
}

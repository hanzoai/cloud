package agents

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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
//	POST /v1/agents/targets/:id/key                  mint/rotate this target's claim key -> {claimKey}
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
func mountRouting(s *cloud.Service[state], app cloud.Router) {
	assertSingleReplica(s.Log)
	o := routingOps{s: s}
	g := app.Group("/v1/agents")
	zip.Post(g, "/targets/:id/key", o.mintClaimKey)
	zip.Post(g, "/targets/:id/claim", o.claim)
	zip.Post(g, "/targets/:id/runs/:runId/report", o.report)
}

// routingOps binds the service to the typed route-work ops. A TypedHandler takes
// no service parameter, so it arrives as a RECEIVER and every op is a method
// value — also the only bound form cmd/zipdoc can lift prose from.
type routingOps struct{ s *cloud.Service[state] }

// claimKeyOf reads the machine capability off the request. A typed op receives
// only a context, and the claim key is a HEADER — the second of this plane's two
// independent proofs, alongside the validated org — so it crosses the same
// cloud.Bridge seam the request does. Empty off the HTTP path, which fails closed:
// verifyClaimKey refuses an empty key.
func claimKeyOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.Header(claimKeyHeader)
	}
	return ""
}

// caller is the VALIDATED principal id (X-User-Id) — the machine-owner identity for
// route-work. tenant() already required a validated principal, so on any handler that
// resolved an org this is non-empty.
func caller(c *zip.Ctx) string { return strings.TrimSpace(c.User()) }

// ownsTarget reports whether the caller may MANAGE this target's route-work plane —
// mint/rotate the claim key, claim, report, patch, delete. A machine belongs to the
// principal that registered it (least privilege, AC-6): its owner may manage it, and
// an org admin (self-service org management, the admin-org model's isAdmin) may manage
// any of the org's targets. An UNOWNED (pre-migration) row is admin-only until its
// owner re-registers — register binds the owner. Fail-closed: an empty caller or an
// empty owner never satisfies the ownership arm, so a non-validated request or a
// pre-migration row is never owner-managed.
func ownsTarget(c *zip.Ctx, t Target) bool {
	if principal.IsOrgAdmin(c) || principal.IsSuperAdmin(c) {
		return true
	}
	u := caller(c)
	return t.Owner != "" && u != "" && u == t.Owner
}

// authorizeTargetManage resolves the (org,id) target and gates it on ownsTarget,
// collapsing every failure — cross-org, unknown, or not-owned — to the SAME
// errTargetNotFound so the machine surface never distinguishes them (no oracle). The
// resolved target is returned for the caller to use (avoids a second read).
func authorizeTargetManage(ctx context.Context, s *cloud.Service[state], org, id string) (Target, error) {
	t, err := s.State.store.GetTarget(ctx, org, id)
	if err != nil {
		return Target{}, errTargetNotFound // unknown / cross-org -> no oracle
	}
	if !targetOwns(ctx, t) {
		return Target{}, errTargetNotFound
	}
	return t, nil
}

// claimKeyOut is the minted capability, returned exactly once.
type claimKeyOut struct {
	// TargetID is the machine the key authenticates.
	TargetID string `json:"targetId"`
	// ClaimKey is the capability itself. It is returned ONCE and never again — only
	// its SHA-256 hash is stored — so a daemon that loses it mints a new one.
	ClaimKey string `json:"claimKey"`
}

// MintTargetClaimKey mints (or rotates) the claim key a `hanzo code --serve`
// daemon presents to claim work for this machine, and returns it ONCE: only its
// SHA-256 hash is stored. Rotating supersedes any prior daemon, so only the
// machine's owner — or an org admin — may call it; every other caller gets the
// same not-found an unknown id gets, and learns nothing about what exists.
//
// Example: {"id": "tgt_1"}
func (o routingOps) mintClaimKey(ctx context.Context, in *targetRef) (*claimKeyOut, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	// The target must exist in this org AND the caller must OWN it (or be an org
	// admin) before it can (re)mint a capability — minting rotates the key, so an
	// un-scoped mint would let any org member strand a victim's daemon and steal its
	// runs. Every failure collapses to the same not-found (no oracle).
	if _, err := authorizeTargetManage(ctx, s, org, id); err != nil {
		return nil, zip.ErrNotFound("target not found")
	}
	key, err := newClaimKey()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.UpsertClaimKeyHash(ctx, org, id, hashClaimKey(key), time.Now().Unix()); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return &claimKeyOut{TargetID: id, ClaimKey: key}, nil
}

// routedRunOut is the non-secret run spec handed to the machine. It carries no
// credential by design — the machine authenticates git + model routing with its
// own already-held credentials — and no cloud-side attribution (actor, agent
// ref), which the machine does not need. Every field is always present, which is
// what this route has always sent.
type routedRunOut struct {
	// SessionID is the live session opened at dispatch; the machine streams its
	// turns into it.
	SessionID string `json:"sessionId"`
	// Repo is the repository to work in and CloneURL is how to fetch it.
	Repo     string `json:"repo"`
	Project  string `json:"project"`
	Base     string `json:"base"`
	Branch   string `json:"branch"`
	Prompt   string `json:"prompt"`
	CloneURL string `json:"cloneUrl"`
	// TimeoutSeconds bounds the run on the machine; 0 means the machine's own default.
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// ClaimRoutedRun is the machine's long poll for work: it authenticates the
// daemon, stamps the liveness the dispatch gate reads (the poll IS the proof a
// runner is listening), and waits up to 25 seconds for the next run addressed to
// THIS machine. It answers the run when one arrives and 204 with no body when the
// window elapses, on which the daemon re-polls immediately.
//
// TWO independent proofs are required and both fail closed to the same 403: the
// caller must own this machine (or be an org admin) AND present its claim key in
// X-Target-Key. A run offered to one machine is unreachable from another's claim.
//
// Example: {"id": "tgt_1"}
func (o routingOps) claim(ctx context.Context, in *targetRef) (*routedRunOut, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	// TWO proofs, both required, both fail-closed to the SAME 403 (no oracle): the
	// caller must OWN this machine (or be an org admin) AND hold its claim key. The
	// ownership gate is defense in depth — with mint owner-scoped an attacker cannot
	// obtain a valid key for a victim's machine, but a claim still refuses a
	// non-owner outright rather than resting solely on the capability.
	if _, err := authorizeTargetManage(ctx, s, org, id); err != nil {
		return nil, claimAuthError(errTargetNotFound)
	}
	if err := s.State.store.verifyClaimKey(ctx, org, id, claimKeyOf(ctx)); err != nil {
		return nil, claimAuthError(err)
	}
	// The poll itself is the runner's liveness proof — stamp it so the dispatch
	// gate (TargetDispatchable) sees a live runner. Best-effort.
	_ = s.State.store.StampServing(ctx, org, id, time.Now().Unix())

	poll, cancel := context.WithTimeout(ctx, claimLongPoll)
	defer cancel()
	run, got := routedMailbox.Claim(poll, org, id)
	if !got {
		// A nil Out IS the 204 this route has always answered on an empty window.
		return nil, nil
	}
	return routedRunView(run), nil
}

// reportRunIn is a claimed run's terminal result. The (target, run) pair comes
// from the path — the URL is the addressing authority — and the outcome from the
// body. The result fields are spelled out HERE rather than in an embedded body
// struct with one user, because zip's schema walk skips an embedded field of an
// unexported type and the published request schema would then hold only the ids.
type reportRunIn struct {
	// ID is the machine reporting, from the path.
	ID string `json:"id"`
	// RunID is the routed run being completed, from the path.
	RunID string `json:"runId"`
	// OK is whether the run succeeded; Changed whether it produced any commit.
	OK      bool `json:"ok"`
	Changed bool `json:"changed"`
	// Branch, CommitSha and Diffstat describe what the run produced; Error is the
	// failure when OK is false. Each is clamped, never rejected.
	Branch    string `json:"branch"`
	CommitSha string `json:"commitSha"`
	Diffstat  string `json:"diffstat"`
	Error     string `json:"error"`
}

// reportOut acknowledges a reported result.
type reportOut struct {
	// Delivered is true when a waiting durable owner received this result. False
	// means there was none to deliver to — an unknown or already-finished run — which
	// is a clean no-op, not an error.
	Delivered bool `json:"delivered"`
}

// ReportRoutedRun completes a claimed run: it delivers the terminal result to the
// run's durable owner, which is what lets that workflow finish. Scoped to (org,
// target, run) and claim-key authenticated, so a machine can only ever report a
// run it legitimately holds. Idempotent — a report for an unknown or
// already-finished run answers delivered:false rather than failing, because the
// session's terminal state was already set by the machine's own stream.
//
// Example: {"id": "tgt_1", "runId": "run_1", "ok": true, "changed": true, "branch": "hanzo/fix"}
func (o routingOps) report(ctx context.Context, in *reportRunIn) (*reportOut, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	runID := strings.TrimSpace(in.RunID)
	// Same two proofs as claim: own the machine (or org admin) AND hold its key, so a
	// non-owner can neither fabricate a report nor complete a victim's run. No oracle.
	if _, err := authorizeTargetManage(ctx, s, org, id); err != nil {
		return nil, claimAuthError(errTargetNotFound)
	}
	if err := s.State.store.verifyClaimKey(ctx, org, id, claimKeyOf(ctx)); err != nil {
		return nil, claimAuthError(err)
	}
	res := RoutedResult{
		OK: in.OK, Changed: in.Changed,
		Branch:    clampStr(in.Branch, maxRepo),
		CommitSha: clampStr(in.CommitSha, 128),
		Diffstat:  clampStr(in.Diffstat, maxReportField),
		Error:     clampStr(in.Error, maxReportField),
	}
	return &reportOut{Delivered: routedMailbox.Report(org, id, runID, res)}, nil
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

// routedRunView projects a queued run onto the machine-facing wire — the same
// eight fields, in the same shape, that this route has always sent.
func routedRunView(run RoutedRun) *routedRunOut {
	return &routedRunOut{
		SessionID:      run.SessionID,
		Repo:           run.Repo,
		Project:        run.Project,
		Base:           run.Base,
		Branch:         run.Branch,
		Prompt:         run.Prompt,
		CloneURL:       run.CloneURL,
		TimeoutSeconds: run.TimeoutSeconds,
	}
}

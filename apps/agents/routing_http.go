package agents

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/authz"
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
//	POST /v1/agent/targets/:id/key                  mint/rotate this target's claim key -> {claimKey}
//	POST /v1/agent/targets/:id/claim                long-poll for the next routed run (X-Target-Key)
//	POST /v1/agent/targets/:id/runs/:runId/report   report a routed run's terminal result (X-Target-Key)

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
	routedMailbox.attach(s.Coord)
	o := routingOps{s: s}
	g := app.Group("/v1/agent")
	g.Post("/targets/:id/key", o.mintClaimKey)
	g.Post("/targets/:id/claim", o.claim)
	g.Post("/targets/:id/runs/:runId/report", o.report)
}

// routingOps binds the service to the typed route-work ops. A TypedHandler takes
// no service parameter, so it arrives as a RECEIVER and every op is a method
// value — also the only bound form cmd/zipdoc can lift prose from.
type routingOps struct{ s *cloud.Service[state] }

// claimKeyOf reads the machine capability off the request. A typed op receives
// only a context, and the claim key is a HEADER — the second of this plane's two
// independent proofs, alongside the validated org — so it crosses the same
// cloud.Bridge client the request does. Empty off the HTTP path, which fails closed:
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

// owner reports whether the person p registered t: under their id, or under the
// key a machine of theirs linked with an API key was written under before
// (principal.Self.Is). Fail-closed: nobody and an unowned row match nothing.
func owner(t Target, p iam.Self) bool {
	return t.Owner != "" && p.Is(t.Owner)
}

// personal reports whether the machines in org are each their owner's alone.
//
// A held org (authz.IsHeldOrg) is where every self-serve signup lands, beside the
// platform's own staff, so its members are strangers to one another. A machine
// one of them links there is that person's: no other member, an admin of the org
// included, may list it, change it, or route a run onto it — a run executes with
// the machine's own credentials, on someone's own computer. In a customer's org
// the admin's reach over the org's machines stands.
func personal(org string) bool { return authz.IsHeldOrg(org) }

// self is who the caller is to the machines of org: the key a machine there is
// registered under and matched against.
//
// In a personal org it is the person (principal.SelfFrom): the id of their IAM
// user row, which their sign-in and every API key of theirs resolve to alike, so
// a machine linked with either credential is theirs under both, and no
// username or display name, which two strangers can present alike, owns
// anything. A machine linked with an API key before machines were keyed on the
// id is still matched by the person's row, `<owner>/<name>` (owner), and is
// written under the id the next time its owner touches it (carry).
//
// Elsewhere it is the validated user id, as it always was. Empty when the call
// names nobody, and empty owns nothing.
func self(ctx context.Context, org string) (iam.Self, error) {
	if !personal(org) {
		return iam.Self{ID: callerOf(ctx)}, nil
	}
	return iam.SelfFrom(ctx)
}

// targetStore is tenantStore for the machine ops. In a personal org it asks who
// the caller is first, so a caller that cannot be told right now is told so
// (503) — a machine's daemon answered "not yours" would stop polling — and one
// whose credential names nobody is refused (403), rather than either being
// answered as a stranger.
func targetStore(ctx context.Context, st *state) (*Store, string, error) {
	sto, org, err := tenantStore(ctx, st)
	if err == nil && personal(org) {
		_, err = iam.SelfFrom(ctx)
	}
	if err != nil {
		return nil, "", err
	}
	return sto, org, nil
}

// carry writes a personal org's machine under its owner's id when the caller is
// its owner and it is still written under their row, the key an API key of
// theirs registered it under before machines were keyed on the id. It is how
// such a machine moves across: matched by the row until then (owner), and keyed
// on the id from its owner's next touch — a claim poll of its daemon, a patch, a
// re-link. Best-effort: a write that fails leaves the row matched as before.
func (s *Store) carry(ctx context.Context, t Target) Target {
	if !personal(t.Org) {
		return t
	}
	p, err := iam.SelfFrom(ctx)
	if err != nil || p.ID == "" || t.Owner == p.ID || !p.Is(t.Owner) {
		return t
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_targets SET owner=$1 WHERE org=$2 AND id=$3 AND owner=$4`,
		p.ID, t.Org, t.ID, t.Owner); err == nil {
		t.Owner = p.ID
	}
	return t
}

// authorizeTargetManage resolves the (org,id) target and gates it on targetOwns,
// collapsing every failure — cross-org, unknown, or not-owned — to the SAME
// errTargetNotFound so the machine surface never distinguishes them (no oracle). The
// resolved target is returned for the caller to use (avoids a second read).
func authorizeTargetManage(ctx context.Context, sto *Store, org, id string) (Target, error) {
	t, err := sto.GetTarget(ctx, org, id)
	if err != nil {
		return Target{}, errTargetNotFound // unknown / cross-org -> no oracle
	}
	if !targetOwns(ctx, t) {
		return Target{}, errTargetNotFound
	}
	return sto.carry(ctx, t), nil
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
	sto, org, err := targetStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	// The target must exist in this org AND the caller must OWN it (or be an org
	// admin) before it can (re)mint a capability — minting rotates the key, so an
	// un-scoped mint would let any org member strand a victim's daemon and steal its
	// runs. Every failure collapses to the same not-found (no oracle).
	if _, err := authorizeTargetManage(ctx, sto, org, id); err != nil {
		return nil, zip.ErrNotFound("target not found")
	}
	key, err := newClaimKey()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := sto.UpsertClaimKeyHash(ctx, org, id, hashClaimKey(key), time.Now().Unix()); err != nil {
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
	Repo string `json:"repo"`
	// Project is the product slug the run is filed under, so the machine can tag
	// what it produces. Empty when the dispatch named none.
	Project string `json:"project"`
	// Base is the branch to start FROM. Empty means the repository's default —
	// resolve it on the machine, since the machine is the one holding the clone.
	Base string `json:"base"`
	// Branch is the ref the run must push its work to, and the ONLY one it is
	// permitted to write: the forge's ref policy refuses anything else from this
	// run's credential. It is decided at dispatch and exists before the work does.
	Branch string `json:"branch"`
	// Prompt is the task, in full, as the person wrote it. There is no second field
	// for context.
	Prompt string `json:"prompt"`
	// CloneURL is how to fetch the repository. It carries no credential: the
	// machine authenticates with the git identity it already holds, or with
	// DeployKey when the run has one.
	CloneURL string `json:"cloneUrl"`
	// DeployKey is the run's own key to its repository, when the repository is one
	// the machine cannot reach otherwise. It opens that one repository, is withdrawn
	// when the run ends, and is for the machine to use for the run and not keep.
	DeployKey string `json:"deployKey,omitempty"`
	// KnownHosts is the forge's host key, to check the connection DeployKey opens.
	KnownHosts string `json:"knownHosts,omitempty"`
	// TimeoutSeconds bounds the run on the machine; 0 means the machine's own default.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// Model is the model the person asked the run's agent to think with, by the
	// id GET /v1/models lists. Empty is the machine's own default.
	Model string `json:"model,omitempty"`
	// Effort is how hard that model reasons: low, medium or high. Empty is the
	// model's own default.
	Effort string `json:"effort,omitempty"`
	// Instructions are the person's own instructions for their runs, as they set
	// them in their settings: the machine's harness reads them as its user's, the
	// way AGENTS.md or CLAUDE.md in its home is read. Empty when they set none.
	Instructions string `json:"instructions,omitempty"`
	// Dir is the folder to work in, an absolute path inside one of the folders its
	// owner granted (the target's `folders`). The cloud checked the grant; the
	// machine checks the real path — symlinks and all — lies inside a folder it
	// still holds before it runs anything there. Empty is the machine's own choice.
	Dir string `json:"dir,omitempty"`
	// ReadOnly tells the machine this run may not write to the granted folder or
	// checkout, commit, push or open a pull request: its answer is the run's terminal
	// status, reported with changed:false. The harness refuses the run if it cannot
	// honour it. This contains one run on the machine; it is not a security boundary
	// against the machine's owner. Not to be confused with DeployKey being read-only.
	ReadOnly bool `json:"readOnly,omitempty"`
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
	sto, org, err := targetStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	// TWO proofs, both required, both fail-closed to the SAME 403 (no oracle): the
	// caller must OWN this machine (or be an org admin) AND hold its claim key. The
	// ownership gate is defense in depth — with mint owner-scoped an attacker cannot
	// obtain a valid key for a victim's machine, but a claim still refuses a
	// non-owner outright rather than resting solely on the capability.
	if _, err := authorizeTargetManage(ctx, sto, org, id); err != nil {
		return nil, claimAuthError(errTargetNotFound)
	}
	if err := sto.verifyClaimKey(ctx, org, id, claimKeyOf(ctx)); err != nil {
		return nil, claimAuthError(err)
	}
	// The poll itself is the runner's liveness proof — stamp it so the dispatch
	// gate (TargetDispatchable) sees a live runner. Best-effort.
	_ = sto.StampServing(ctx, org, id, time.Now().Unix())

	poll, cancel := context.WithTimeout(ctx, claimLongPoll)
	defer cancel()
	run, got := routedMailbox.Claim(poll, org, id)
	if !got {
		// A nil Out IS the 204 this route has always answered on an empty window.
		return nil, nil
	}
	// A folder revoked while the run was queued is not handed out: the run ends
	// refused, and the machine sees the empty window it would have. The read-only
	// capability is re-checked the same way: a stale harness is not handed a run it
	// would have to execute writable.
	if run.ReadOnly {
		t, err := sto.GetTarget(ctx, org, id)
		if err != nil || !t.Spec.ReadOnlyRuns {
			msg := ReadOnlyUnsupported + ": the machine " + id + " no longer advertises readOnlyRuns; the queued run was refused"
			s.Log.Warn("agents: a queued read-only run could not be claimed", "org", org, "target", id, "session", run.SessionID, "err", err)
			if _, rerr := routedMailbox.Report(ctx, org, id, run.SessionID, RoutedResult{Error: msg}); rerr != nil {
				s.Log.Warn("agents: a refused run's result was not delivered", "org", org, "target", id, "session", run.SessionID, "err", rerr)
			}
			return nil, nil
		}
	}
	if run.Dir == "" {
		return routedRunView(run), nil
	}
	if err := sto.inside(ctx, org, id, run.Dir); err != nil {
		folderAudit(ctx, folderDeny, org, id, run.Dir, run.SessionID, err)
		if _, rerr := routedMailbox.Report(ctx, org, id, run.SessionID, RoutedResult{Error: err.Error()}); rerr != nil {
			s.Log.Warn("agents: a refused run's result was not delivered", "org", org, "target", id, "session", run.SessionID, "err", rerr)
		}
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
	OK bool `json:"ok"`
	// Changed says whether the run produced any commit. It is INDEPENDENT of OK: a
	// run can succeed and change nothing (there was nothing to do), and a run can
	// fail after committing some of its work. Two questions, two booleans.
	Changed bool `json:"changed"`
	// Branch, CommitSha and Diffstat describe what the run produced; Error is the
	// failure when OK is false. Each is clamped, never rejected.
	Branch string `json:"branch"`
	// CommitSha is the tip the run pushed, clamped to 128 characters. Empty when it
	// pushed nothing, which is the same case Changed reports false for.
	CommitSha string `json:"commitSha"`
	// Diffstat is the run's own summary of what it changed, as text, clamped to
	// 64 KiB. Free-form: it is shown, never parsed.
	Diffstat string `json:"diffstat"`
	// Error is why the run failed, clamped to 64 KiB. It is CLAMPED rather than
	// refused — a truncated reason is worth more than a rejected report, because a
	// rejected report leaves the durable workflow waiting forever.
	Error string `json:"error"`
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
	sto, org, err := targetStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	runID := strings.TrimSpace(in.RunID)
	// Same two proofs as claim: own the machine (or org admin) AND hold its key, so a
	// non-owner can neither fabricate a report nor complete a victim's run. No oracle.
	if _, err := authorizeTargetManage(ctx, sto, org, id); err != nil {
		return nil, claimAuthError(errTargetNotFound)
	}
	if err := sto.verifyClaimKey(ctx, org, id, claimKeyOf(ctx)); err != nil {
		return nil, claimAuthError(err)
	}
	res := RoutedResult{
		OK: in.OK, Changed: in.Changed,
		Branch:    clampStr(in.Branch, maxRepo),
		CommitSha: clampStr(in.CommitSha, 128),
		Diffstat:  clampStr(in.Diffstat, maxReportField),
		Error:     clampStr(in.Error, maxReportField),
	}
	delivered, err := routedMailbox.Report(ctx, org, id, runID, res)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "agents: the pods that may await this run could not all be asked; report again")
	}
	return &reportOut{Delivered: delivered}, nil
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

// routedRunView projects a queued run onto the machine-facing wire: where to
// work, what to do, and what to think with — never who asked or on whose
// authority.
func routedRunView(run RoutedRun) *routedRunOut {
	out := &routedRunOut{
		SessionID:      run.SessionID,
		Repo:           run.Repo,
		Project:        run.Project,
		Base:           run.Base,
		Branch:         run.Branch,
		Prompt:         run.Prompt,
		CloneURL:       run.CloneURL,
		TimeoutSeconds: run.TimeoutSeconds,
		Model:          run.Model,
		Effort:         run.Effort,
		Instructions:   run.Instructions,
		Dir:            run.Dir,
		ReadOnly:       run.ReadOnly,
	}
	if g := run.Grant; g != nil {
		out.CloneURL, out.DeployKey, out.KnownHosts = g.Remote, g.Key, g.Known
	}
	return out
}

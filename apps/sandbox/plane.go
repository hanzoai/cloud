package sandbox

// plane.go — the sandbox's SECOND adapter: the same five verbs, reachable from a
// peer app instead of from a browser.
//
// It is a peer call and not a Go import, and that is a correctness decision rather
// than a stylistic one. Apps in this fleet are separate PLUGIN BINARIES — each
// plugin/<app>/main.go "links only its own subsystem and the cloud request tier,
// never the whole fleet" — and the host spawns them as children. So importing this
// package from apps/exec would not have joined exec to the running sandbox
// service; it would have given exec a SECOND one, with its own cloud.OrgStore
// opening the same per-org SQLite files the real service has open, and its own
// `go reap()` sweeping leases the real service is still serving. Two writers on
// one store is the exact failure the writer lease exists to prevent.
//
// And the hop is not a cost where the two are fused: plane.Ask checks
// zip.Serving(app) first and dispatches IN-PROCESS when the peer is this process,
// so the same call is a function call in a fused binary and a socket round trip in
// a split one. Which it is, is not the caller's business — that is the whole reason
// there is one way to make it.
//
// The org rides the CALLER, never the argument. cloud.Who(ctx) is what the edge
// minted; an org in the body would be an org the caller chose, and a caller that
// can name the org can read another tenant's sandbox.

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
// Without it these ops reached the fleet MCP server NAMED AND UNDESCRIBED: an
// agent was told `class` is a string and not that the strings are exec, dev and
// desktop, told `project` exists and not that a dev sandbox has no disk without
// one, told `id` exists and not that it is how you get back the computer you
// already hold. An op you have to guess the arguments of is one you use wrong on
// the first try, which for a coding run is a lease spent on a mistake.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// mounted is the service the plane ops answer from. A plane handler is registered
// once per process and has no receiver, so the only way to reach the mounted
// service is a package value — the same shape apps/git and apps/agents use.
var mounted atomic.Pointer[Service]

// Shutdown drains this subsystem's per-org stores: each one's final state ships
// fenced at its lease round and its ownership is released, so the replica that
// picks the org up next hydrates everything this one acknowledged.
//
// It is the ORDERLY half of durability and the reaper is the other. A lease ended
// a second before SIGTERM is durable because of this; a lease ended a second
// before a kill -9 is re-ended by the successor because of the reaper. Neither
// covers the other, and shipping without this one meant every graceful restart
// resurrected whatever had not happened to be swept.
func Shutdown() error { return shutdownStores() }

// shutdownStores is bound at Mount. Unmounted, closing is a no-op rather than a
// nil call — a host that shuts a subsystem it never started is asking a reasonable
// question and the answer is "nothing to drain".
var shutdownStores = func() error { return nil }

// expose publishes the sandbox verbs on the internal plane. Called from Mount.
func expose() {
	p := cloud.Plane()
	zip.Post[plane.LeaseIn, plane.Leased](p, "/sandbox/lease", planeLease,
		zip.WithOperationID(plane.SandboxLease),
		zip.WithSummary("Lease a sandbox, or resume one"))
	zip.Post[plane.RunIn, plane.Ran](p, "/sandbox/run", planeRun,
		zip.WithOperationID(plane.SandboxRun),
		zip.WithSummary("Run a command in a sandbox"))
	zip.Post[plane.PathIn, plane.Blob](p, "/sandbox/read", planeRead,
		zip.WithOperationID(plane.SandboxRead),
		zip.WithSummary("Read a file, or list a directory"))
	zip.Post[plane.WriteIn, plane.Wrote](p, "/sandbox/write", planeWrite,
		zip.WithOperationID(plane.SandboxWrite),
		zip.WithSummary("Write a file in a sandbox"))
	zip.Post[plane.StopIn, plane.Stopped](p, "/sandbox/stop", planeStop,
		zip.WithOperationID(plane.SandboxStop),
		zip.WithSummary("Interrupt what a sandbox is running"))
	zip.Post[plane.EndIn, struct{}](p, "/sandbox/end", planeEnd,
		zip.WithOperationID(plane.SandboxEnd),
		zip.WithSummary("End a sandbox's lease"))
	zip.Post[plane.AttachIn, plane.Attached](p, "/sandbox/attach", planeAttach,
		zip.WithOperationID(plane.SandboxAttach),
		zip.WithSummary("Report that somebody is watching a project"))
}

// planeAttach stamps ATTENTION on the sandbox a project currently holds.
//
// It is the half of the idle clock this process cannot observe. A sandbox knows
// when it was last CALLED, and that is a poor proxy for whether anyone is there:
// reading a diff for twenty minutes touches nothing, and a tab closed twenty
// minutes ago touches nothing either. Only the stream can tell those apart, and
// the stream is in another process (see plane.SandboxAttach).
//
// KEYED ON THE PROJECT, not a sandbox id, because that is what the watcher holds
// — an id changes when a lease is reaped and re-taken while the tab stays open,
// so presence keyed on it would go stale at exactly the moment it decides a reap.
//
// A project with no live sandbox is a SUCCESSFUL empty answer, not an error: a
// user who opened the page before anything was leased is the ordinary case, and
// answering with an error would make an idle stream log a failure every beat.
func planeAttach(ctx context.Context, in *plane.AttachIn) (*plane.Attached, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	project := strings.TrimSpace(in.Project)
	if project == "" {
		return nil, zip.ErrBadRequest("attach: project required")
	}
	st, err := storeFor(s, org)
	if err != nil {
		return nil, err
	}
	m, err := st.Live(ctx, org, project)
	if err != nil {
		return nil, zip.Errorf(500, "attach: %v", err)
	}
	if m.ID == "" {
		return &plane.Attached{}, nil
	}
	if err := st.Watched(ctx, org, m.ID, time.Now().Unix()); err != nil {
		return nil, zip.Errorf(500, "attach: %v", err)
	}
	return &plane.Attached{ID: m.ID}, nil
}

// live resolves the caller's org and the mounted service together, because every
// op below needs both and neither is worth a second spelling.
//
// THESE OPS ARE SERVED ON TWO ENDPOINTS and the tenant rule has to hold on both.
// Mount registers each one a second time on the public app (sandbox.go), which
// is what lets an agent name them at all — and a handler written for the plane
// alone can carry an argument that only holds there. cloud.Who is exactly that:
// off a request it reads the caller the plane stated in-process, on one it reads
// the headers, and a non-empty check cannot tell those apart.
//
// cloud.Tenant is the rule that can: it is principal.Acting — from outside, the
// validated principal and nothing else — plus the one case Acting cannot admit,
// a call with no request behind it, where the org is what the plane stated
// in-process and nothing outside can write. What this op does with the answer
// — open the org's store, lease a pod in its namespace — is the same either way,
// so the tenant it acts on must be, too.
//
// THE TENANT IS ASKED FIRST. A caller with no tenant is told that and nothing
// else — whether this process happens to mount sandbox is a fact about our
// deployment, and answering it before deciding who is asking hands it to anyone.
// It is also what makes the rule observable: refused says 403, resolved gets far
// enough to meet the mount.
func live(ctx context.Context) (*Service, string, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, "", principal.RefusedFrom(ctx)
	}
	s := mounted.Load()
	if s == nil {
		return nil, "", zip.Errorf(503, "sandbox not mounted")
	}
	return s, org, nil
}

// planeLease leases the caller's sandbox, or returns the one it named if that
// lease is still running.
//
// What comes back is a real computer: a pod under a runtime boundary with a
// toolchain already in it, its own filesystem, and a lease that ends it. Every
// other op here acts on the one this returns.
func planeLease(ctx context.Context, in *plane.LeaseIn) (*plane.Leased, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	// The same fact the HTTP endpoint reads off the principal, read here off the
	// caller the plane already carries — cloud.Who is how this endpoint spells
	// identity, and zip.Caller.Admin is the same attestation X-User-IsAdmin carries.
	// One rule, stated once in imageFor; each endpoint names the caller in its own
	// vocabulary. NO BEARER on this endpoint. A plane call arrives under a capability
	// envelope and carries an ATTESTED caller, not the caller's own token — there is
	// nothing here to exchange, and substituting a credential of ours would put an
	// identity in the pod that nobody presented. So a plane lease starts without a
	// session, and the empty string says so rather than a flag saying it twice.
	m, err := Lease(s, ctx, org, principal.LedgerFrom(ctx), cloud.Who(ctx).Admin, "", Spec{ID: in.ID, Class: in.Class,
		Project: in.Project, Runtime: in.Runtime, TTLSec: in.TTLSec, Cluster: in.Cluster})
	if err != nil {
		return nil, err
	}
	return &plane.Leased{ID: m.ID, Class: m.Class, Runtime: m.Runtime, Status: m.Status,
		Workdir: workdirFor(m.Class), Cluster: m.Cluster}, nil
}

// planeRun runs one command inside the caller's sandbox and answers its exit code,
// stdout and stderr. A non-zero exit is a successful call carrying a failed
// program, so it comes back as data and not as an error.
//
// Name a `session` and the command NARRATES INTO IT: its output is appended to
// that session's live log as the program produces it, so anything watching the
// session — GET /v1/agent/sessions/stream, scoped to one run with ?root= —
// watches the work happen rather than waiting for the verdict. Without it the
// call is what it always was: silent until it returns, which for an agentic run
// is twenty-five minutes of blank screen.
//
// The session is named; the TENANT is not. It is the org the caller already
// proved, so a session belonging to somebody else is absent from the org this
// call acts for and the append is refused there.
func planeRun(ctx context.Context, in *plane.RunIn) (*plane.Ran, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	r, err := Run(s, ctx, org, in.ID, Cmd{Argv: in.Argv, Command: in.Command,
		Stdin: in.Stdin, Dir: in.Dir, TimeoutSec: in.TimeoutSec, Session: in.Session,
		Blind: in.Blind})
	if err != nil {
		return nil, err
	}
	return &plane.Ran{ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr}, nil
}

// planeStop interrupts whatever the caller's sandbox is running and answers how
// many commands it ended. The sandbox stays leased — stop ends the WORK, end ends
// the RESOURCE — so whoever stopped a run can still read what it left behind.
func planeStop(ctx context.Context, in *plane.StopIn) (*plane.Stopped, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	n, err := Stop(s, ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	return &plane.Stopped{Stopped: n}, nil
}

// planeRead reads one path in the caller's sandbox: a file's bytes, or a
// directory's entries when the path names one.
func planeRead(ctx context.Context, in *plane.PathIn) (*plane.Blob, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	e, err := Read(s, ctx, org, in.ID, in.Path)
	if err != nil {
		return nil, err
	}
	return &plane.Blob{Path: e.Path, Dir: e.Dir, Data: e.Data, Entries: e.Entries}, nil
}

// planeWrite writes bytes to one path in the caller's sandbox, creating parents,
// and answers the resolved path.
func planeWrite(ctx context.Context, in *plane.WriteIn) (*plane.Wrote, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	path, n, err := Write(s, ctx, org, in.ID, in.Path, in.Data)
	if err != nil {
		return nil, err
	}
	return &plane.Wrote{Path: path, Bytes: n}, nil
}

// planeEnd ends the caller's sandbox lease: the pod goes, and the volume goes only
// when the caller asked for that too.
func planeEnd(ctx context.Context, in *plane.EndIn) (*cloud.Unit, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	if err := End(s, ctx, org, in.ID, in.Purge); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

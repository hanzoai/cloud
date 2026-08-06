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
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// mounted is the service the plane ops answer from. A plane handler is registered
// once per process and has no receiver, so the only way to reach the mounted
// service is a package value — the same shape apps/git and apps/agents use.
var mounted atomic.Pointer[Service]

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
	zip.Post[plane.EndIn, struct{}](p, "/sandbox/end", planeEnd,
		zip.WithOperationID(plane.SandboxEnd),
		zip.WithSummary("End a sandbox's lease"))
}

// live resolves the caller's org and the mounted service together, because every
// op below needs both and neither is worth a second spelling.
func live(ctx context.Context) (*Service, string, error) {
	s := mounted.Load()
	if s == nil {
		return nil, "", zip.Errorf(503, "sandbox not mounted")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, "", zip.ErrForbidden("sandbox: org required")
	}
	return s, org, nil
}

// planeLease leases the caller's sandbox, or returns the one it named if that lease
// is still running. Named handlers, not closures, so zipdoc can lift this prose.
func planeLease(ctx context.Context, in *plane.LeaseIn) (*plane.Leased, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	m, err := Lease(s, ctx, org, Spec{ID: in.ID, Class: in.Class, Project: in.Project, TTLSec: in.TTLSec})
	if err != nil {
		return nil, err
	}
	return &plane.Leased{ID: m.ID, Class: m.Class, Status: m.Status, Workdir: workdirFor(m.Class)}, nil
}

// planeRun runs one command inside the caller's sandbox and answers its exit code,
// stdout and stderr. A non-zero exit is a successful call carrying a failed
// program, so it comes back as data and not as an error.
func planeRun(ctx context.Context, in *plane.RunIn) (*plane.Ran, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	r, err := Run(s, ctx, org, in.ID, Cmd{Argv: in.Argv, Command: in.Command,
		Stdin: in.Stdin, Dir: in.Dir, TimeoutSec: in.TimeoutSec})
	if err != nil {
		return nil, err
	}
	return &plane.Ran{ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr}, nil
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
func planeEnd(ctx context.Context, in *plane.EndIn) (*struct{}, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	if err := End(s, ctx, org, in.ID, in.Purge); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

package coding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// plane_test.go runs a whole coding job through REAL SOCKETS.
//
// The seam fakes elsewhere in this package call the Dispatcher's fields
// directly, which is the right shape for testing the orchestration and the
// wrong shape for testing this: the bug being fixed here was never in the
// orchestration. It was that each seam landed on a package global belonging to
// another PROCESS, and no in-process test can fail on that — the calls all
// resolve, against the nil that ships.
//
// So the peers here are actual zip apps on actual unix sockets, serving the
// actual ops the production plugins serve, and the Dispatcher's seams are the
// production plane clients. What is proven is what could not be proven before:
// every argument ENCODES (a map field would die inside zip.Call, before the
// socket — see plane_encodable_test.go), every reply decodes, and a run whose
// collaborators are all elsewhere still opens its session, points the sandbox at
// its own org, verifies the pushed ref and files its PR.

// peers stands up the three apps a coding run reaches, each backed by the same
// recording fakes the in-process tests use, so an assertion can be made about
// what ARRIVED on the far side rather than what was sent.
type peers struct {
	sessions *fakeSessions
	tracker  *fakeTracker
	clone    string
	tip      string
	found    bool
	gated    []string
}

func servePeers(t *testing.T, p *peers) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())

	agentsApp := zip.New(zip.Config{AppName: "agents", DisableStartupMessage: true})
	zip.Post[plane.SessionOpenIn, plane.SessionOpened](agentsApp, "/agents/session/open",
		func(ctx context.Context, in *plane.SessionOpenIn) (*plane.SessionOpened, error) {
			id, err := p.sessions.OpenOn(ctx, in.Org, in.Actor, in.Agent, in.Title, in.Target)
			if err != nil {
				return nil, err
			}
			return &plane.SessionOpened{SessionID: id}, nil
		}, zip.WithOperationID(plane.AgentsSessionOpen))
	zip.Post[plane.SessionEventIn, plane.CodingAck](agentsApp, "/agents/session/event",
		func(ctx context.Context, in *plane.SessionEventIn) (*plane.CodingAck, error) {
			if err := p.sessions.Log(ctx, in.Org, in.SessionID, in.Kind, in.Actor, in.Payload); err != nil {
				return nil, err
			}
			return &plane.CodingAck{OK: true}, nil
		}, zip.WithOperationID(plane.AgentsSessionEvent))
	zip.Post[plane.SessionCloseIn, plane.CodingAck](agentsApp, "/agents/session/close",
		func(ctx context.Context, in *plane.SessionCloseIn) (*plane.CodingAck, error) {
			if err := p.sessions.Close(ctx, in.Org, in.SessionID, in.Status); err != nil {
				return nil, err
			}
			return &plane.CodingAck{OK: true}, nil
		}, zip.WithOperationID(plane.AgentsSessionClose))
	zip.Post[plane.TargetGateIn, plane.CodingAck](agentsApp, "/agents/target-gate",
		func(_ context.Context, in *plane.TargetGateIn) (*plane.CodingAck, error) {
			p.gated = append(p.gated, in.Org+"/"+in.TargetID)
			return &plane.CodingAck{OK: true}, nil
		}, zip.WithOperationID(plane.AgentsTargetGate))

	gitApp := zip.New(zip.Config{AppName: "git", DisableStartupMessage: true})
	zip.Post[plane.RepoRefIn, plane.RepoCloneURL](gitApp, "/git/clone-url",
		func(_ context.Context, in *plane.RepoRefIn) (*plane.RepoCloneURL, error) {
			p.clone = in.Org + "/" + in.Repo
			return &plane.RepoCloneURL{URL: "https://git.test/v1/git/" + in.Org + "/" + in.Repo + ".git"}, nil
		}, zip.WithOperationID(plane.GitCloneURL))
	zip.Post[plane.RefIn, plane.RefTip](gitApp, "/git/verify-ref",
		func(_ context.Context, _ *plane.RefIn) (*plane.RefTip, error) {
			return &plane.RefTip{SHA: p.tip, Found: p.found}, nil
		}, zip.WithOperationID(plane.GitVerifyRef))

	trackerApp := zip.New(zip.Config{AppName: "tracker", DisableStartupMessage: true})
	zip.Post[plane.AgentPRIn, plane.AgentPROut](trackerApp, "/tracker/agent-pr",
		func(ctx context.Context, in *plane.AgentPRIn) (*plane.AgentPROut, error) {
			ref, err := p.tracker.CreatePR(ctx, PRInput{
				Org: in.Org, Project: in.Project, Repo: in.Repo, Base: in.Base,
				Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
			})
			if err != nil {
				return nil, err
			}
			return &plane.AgentPROut{Identifier: ref.Identifier, ProjectKey: ref.ProjectKey, Number: ref.Number}, nil
		}, zip.WithOperationID(plane.TrackerAgentPR))

	for name, app := range map[string]*zip.App{"agents": agentsApp, "git": gitApp, "tracker": trackerApp} {
		app := app
		plane.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
}

func waitListening(t *testing.T, app string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if up, err := plane.Listening(zip.SocketPath(app)); err == nil && up {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("peer %s never came up", app)
}

// planeDispatcher is the production seam set (adapters.go) with only the sandbox
// runner faked — the runner was always an HTTP client and never had a boundary.
func planeDispatcher(run *fakeRunner) Dispatcher {
	return Dispatcher{
		Sessions: planeSessions{}, Tracker: planeTracker{}, Runner: run,
		CloneURL: planeCloneURL, VerifyRef: planeVerifyRef, TargetGate: planeTargetGate,
	}
}

// A changed run completes with every seam on the far side of a socket.
func TestRun_OverThePlane_CompletesAcrossProcesses(t *testing.T) {
	p := &peers{
		sessions: &fakeSessions{id: "sess_abc123def456"},
		tracker:  &fakeTracker{ref: PRRef{Identifier: "API-7", ProjectKey: "API", Number: 7}},
		tip:      "verifiedsha", found: true,
	}
	servePeers(t, p)

	run := &fakeRunner{
		steps:  []Step{{Type: "step", Step: "clone", Message: "cloning", Status: "ok"}},
		result: RunResult{Changed: true, OK: true, CommitSha: "deadbeef", Diffstat: " 1 file changed"},
	}
	res := planeDispatcher(run).Run(context.Background(), Req{
		Org: "acme", UserID: "u_1", AgentRef: "hanzo", Repo: "api",
		Prompt: "fix the flake", CredUser: "x-access-token", CredToken: "sk-secret",
	})

	if !res.OK || !res.Verified {
		t.Fatalf("run must complete over the plane: %+v", res)
	}
	if res.PR.Identifier != "API-7" {
		t.Fatalf("the PR must be filed through tracker's door, got %q", res.PR.Identifier)
	}
	if res.CommitSha != "verifiedsha" {
		t.Fatalf("the tip must come from git's own storage, got %q", res.CommitSha)
	}
	// ISOLATION: the sandbox is pointed only at THIS org's namespace, and that
	// survives the crossing rather than being re-derived on the far side.
	if p.clone != "acme/api" {
		t.Fatalf("git was asked for %q, want acme/api", p.clone)
	}
	if !strings.Contains(run.gotReq.CloneURL, "/acme/api.git") {
		t.Fatalf("clone url did not survive the crossing: %q", run.gotReq.CloneURL)
	}
	if run.gotReq.CredToken != "sk-secret" {
		t.Fatal("the credential must reach the sandbox unchanged")
	}
	// The session opened, streamed and closed — all three ops, all across.
	if len(p.sessions.opened) != 1 || p.sessions.opened[0].org != "acme" {
		t.Fatalf("session open did not arrive: %+v", p.sessions.opened)
	}
	if len(p.sessions.closes) != 1 || p.sessions.closes[0].status != statusDone {
		t.Fatalf("session close did not arrive: %+v", p.sessions.closes)
	}
	// The event payload is bytes precisely so its shape can cross; a dropped
	// payload would leave mission-control with empty events.
	var sawStarted bool
	for _, e := range p.sessions.events {
		if e.kind == kindStatus && strings.Contains(e.payload, `"status":"started"`) {
			sawStarted = true
		}
	}
	if !sawStarted {
		t.Fatalf("event payloads did not survive the crossing: %+v", p.sessions.events)
	}
}

// A branch git cannot see fails the run CLOSED across the boundary too: the
// integrity gate is the reason the PR exists, and an unreachable git must not
// read as a verified push.
func TestRun_OverThePlane_UnverifiedRefFilesNoPR(t *testing.T) {
	p := &peers{
		sessions: &fakeSessions{id: "sess_abc123def456"},
		tracker:  &fakeTracker{ref: PRRef{Identifier: "API-8"}},
		found:    false,
	}
	servePeers(t, p)

	run := &fakeRunner{result: RunResult{Changed: true, OK: true, CommitSha: "deadbeef"}}
	res := planeDispatcher(run).Run(context.Background(), Req{
		Org: "acme", UserID: "u_1", Repo: "api", Prompt: "fix", CredToken: "sk-secret",
	})

	if res.OK || !strings.Contains(res.Error, "not found in native git") {
		t.Fatalf("an unverified ref must fail closed, got %+v", res)
	}
	if len(p.tracker.inputs) != 0 {
		t.Fatal("no PR may be filed for a branch git cannot see")
	}
	if len(p.sessions.closes) != 1 || p.sessions.closes[0].status != statusError {
		t.Fatalf("the session must close error: %+v", p.sessions.closes)
	}
}

// A peer that is not part of the deployment is an honest error, not a run that
// proceeds without it. This is the shape the whole path had in production.
func TestRun_OverThePlane_MissingPeerFailsHonestly(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir()) // nothing listening at all
	res := planeDispatcher(&fakeRunner{}).Run(context.Background(), Req{
		Org: "acme", UserID: "u_1", Repo: "api", Prompt: "fix", CredToken: "sk-secret",
	})
	if res.OK || res.Error != "git is not available" {
		t.Fatalf("an absent git must stop the run before it starts, got %+v", res)
	}
}

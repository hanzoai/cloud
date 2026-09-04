package coding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sock"
	"github.com/zap-proto/zip"
)

// plane_test.go runs a whole coding job through REAL SOCKETS.
//
// The client fakes elsewhere in this package call the Dispatcher's fields
// directly, which is the right shape for testing the orchestration and the
// wrong shape for testing this: the bug being fixed here was never in the
// orchestration. It was that each client landed on a package global belonging to
// another PROCESS, and no in-process test can fail on that — the calls all
// resolve, against the nil that ships.
//
// So the peers here are actual zip apps on actual unix sockets, serving the
// actual ops the production plugins serve, and the Dispatcher's clients are the
// production plane clients. What is proven is what could not be proven before:
// every argument ENCODES (a map field would die inside zip.Call, before the
// socket — see plane_encodable_test.go), every reply decodes, and a run whose
// collaborators are all elsewhere still opens its session, verifies the pushed
// ref and files its PR.
//
// GIT IS NO LONGER ONE OF THOSE PEERS. The forge is an external host, so the
// facts a run needs from it are read over HTTPS from whichever process the run
// is in, and the stub below is a forge rather than a plane app. The credential
// still crosses a socket — it is read from KMS, which IS a peer — so the whole
// path from "this deployment's secret" to "this run's answer" is exercised.

// peers stands up the three apps a coding run reaches, each backed by the same
// recording fakes the in-process tests use, so an assertion can be made about
// what ARRIVED on the far side rather than what was sent.
type peers struct {
	sessions *fakeSessions
	todo     *fakePR
	tip      string
	found    bool
	gated    []string
	proposed string
	// asked records every path the forge stub was called on, so a test can assert
	// which questions actually reached it and as whom.
	asked []string
	sudo  []string
}

// serveForge stands up a fake forge and points this process's client at it.
//
// It answers on loopback, which forge.New exempts from its https rule, so the
// production client — machine token, Sudo header, /v1 prefix and all — is the
// thing under test rather than a stand-in for it.
func serveForge(t *testing.T, p *peers) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.asked = append(p.asked, r.Method+" "+r.URL.Path)
		p.sudo = append(p.sudo, r.Header.Get("Sudo"))
		if r.Header.Get("Authorization") != "token forge-machine-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/branches/"):
			if !p.found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "agent/abc123def456", "commit": map[string]any{"id": p.tip},
			})
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == http.MethodPost:
			var in struct{ Head string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			p.proposed = in.Head
			_ = json.NewEncoder(w).Encode(map[string]any{
				"html_url": "https://git.test/hanzoai/api/pulls/1",
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "api", "full_name": "hanzoai/api", "default_branch": "main",
				"ssh_url": "git@git.test:acme/api.git",
			})
		}
	}))
	t.Cleanup(srv.Close)
	// A fresh Source per test: the production one holds its credential for five
	// minutes, and a test that inherited another test's forge would pass for the
	// wrong reason.
	// CLOUD_FORGE_HOST is the documented override for a deployment whose forge is
	// not the sibling of its own domain — a developer box, a staging forge, and
	// this. The held credential is dropped either side so no test is answered by
	// the forge another one stood up.
	t.Setenv("CLOUD_FORGE_HOST", srv.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)
}

func servePeers(t *testing.T, p *peers) {
	t.Helper()
	serveForge(t, p)
	t.Setenv("ZIP_RUNTIME_DIR", sock.Dir(t))

	agentsApp := zip.New(zip.Config{AppName: "agents", DisableStartupMessage: true})
	zip.Post[client.SessionOpenIn, client.SessionOpened](agentsApp, "/agents/session/open",
		func(ctx context.Context, in *client.SessionOpenIn) (*client.SessionOpened, error) {
			id, err := p.sessions.OpenOn(ctx, in.Org, in.Actor, in.Agent, in.Title, in.Target)
			if err != nil {
				return nil, err
			}
			return &client.SessionOpened{SessionID: id}, nil
		}, zip.WithOperationID(client.AgentsSessionOpen))
	zip.Post[client.SessionEventIn, client.CodingAck](agentsApp, "/agents/session/event",
		func(ctx context.Context, in *client.SessionEventIn) (*client.CodingAck, error) {
			if err := p.sessions.Log(ctx, in.Org, in.SessionID, in.Kind, in.Actor, in.Payload); err != nil {
				return nil, err
			}
			return &client.CodingAck{OK: true}, nil
		}, zip.WithOperationID(client.AgentsSessionEvent))
	zip.Post[client.SessionCloseIn, client.CodingAck](agentsApp, "/agents/session/close",
		func(ctx context.Context, in *client.SessionCloseIn) (*client.CodingAck, error) {
			if err := p.sessions.Close(ctx, in.Org, in.SessionID, in.Status); err != nil {
				return nil, err
			}
			return &client.CodingAck{OK: true}, nil
		}, zip.WithOperationID(client.AgentsSessionClose))
	zip.Post[client.TargetGateIn, client.CodingAck](agentsApp, "/agents/target-gate",
		func(_ context.Context, in *client.TargetGateIn) (*client.CodingAck, error) {
			p.gated = append(p.gated, in.Org+"/"+in.TargetID)
			return &client.CodingAck{OK: true}, nil
		}, zip.WithOperationID(client.AgentsTargetGate))

	// KMS is where the forge credential comes from, so it is a real peer on a
	// real socket — the one hop between "the deployment's secret" and a run's
	// ability to read the forge at all.
	kmsApp := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	zip.Post[client.SecretIn, client.Secret](kmsApp, "/kms/get",
		func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
			if in.Ref != forge.TokenRef {
				return nil, zip.ErrNotFound("no such secret")
			}
			return &client.Secret{Value: []byte("forge-machine-token")}, nil
		}, zip.WithOperationID(client.KMSGet))

	todoApp := zip.New(zip.Config{AppName: "todo", DisableStartupMessage: true})
	zip.Post[client.AgentPRIn, client.AgentPROut](todoApp, "/todo/agent-pr",
		func(ctx context.Context, in *client.AgentPRIn) (*client.AgentPROut, error) {
			// No in.Org: the org is the caller's plane identity, mirroring the real
			// handler (plugin/todo/clients.go) after the cross-tenant write was closed.
			ref, err := p.todo.Open(ctx, PRInput{
				Org: zip.CallerOf(ctx).Org, Project: in.Project, Repo: in.Repo, Base: in.Base,
				Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
			})
			if err != nil {
				return nil, err
			}
			return &client.AgentPROut{Identifier: ref.Identifier, ProjectKey: ref.ProjectKey, Number: ref.Number}, nil
		}, zip.WithOperationID(client.TodoAgentPR))

	for name, app := range map[string]*zip.App{"agents": agentsApp, "kms": kmsApp, "todo": todoApp} {
		client.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
}

func waitListening(t *testing.T, app string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if up, err := client.Listening(zip.SocketPath(app)); err == nil && up {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("peer %s never came up", app)
}

// planeDispatcher is the production client set (adapters.go) with only the sandbox
// runner faked — the runner was always an HTTP client and never had a boundary.
func planeDispatcher(run *fakeRunner) Dispatcher {
	return Dispatcher{
		Sessions: planeSessions{}, PR: planePR{}, Runner: run,
		CloneURL: forgeCloneURL, VerifyRef: forgeVerifyRef, TargetGate: planeTargetGate,
	}
}

// A changed run completes with every client on the far side of a socket.
func TestRun_OverThePlane_CompletesAcrossProcesses(t *testing.T) {
	p := &peers{
		sessions: &fakeSessions{id: "sess_abc123def456"},
		todo:     &fakePR{ref: PRRef{Identifier: "API-7", ProjectKey: "API", Number: 7}},
		tip:      "verifiedsha", found: true,
	}
	servePeers(t, p)

	run := &fakeRunner{
		steps:  []Step{{Type: "step", Step: "clone", Message: "cloning", Status: "ok"}},
		result: RunResult{Changed: true, OK: true, CommitSha: "deadbeef", Diffstat: " 1 file changed"},
	}
	res := planeDispatcher(run).Run(context.Background(), Req{
		Org: "hanzo", UserID: "u_1", Actor: "zoe", AgentRef: "hanzo", Repo: "api",
		Prompt: "fix the flake", Remote: "git@git.test:hanzoai/api.git",
		Key:   "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----",
		Known: "git.test ssh-ed25519 AAAAPIN",
	})

	if !res.OK || !res.Verified {
		t.Fatalf("run must complete over the plane: %+v", res)
	}
	if res.PR.Identifier != "API-7" {
		t.Fatalf("the PR must be filed through todo's endpoint, got %q", res.PR.Identifier)
	}
	// ONE client, both backends: the row landed on the board AND git answered where
	// the proposal is read. A run whose result carries no address gives a person
	// in a thread nothing to click.
	if p.proposed != "agent/abc123def456" {
		t.Fatalf("git was never asked to propose the branch, got %q", p.proposed)
	}
	if res.PR.URL != "https://git.test/hanzoai/api/pulls/1" {
		t.Fatalf("the address did not come back with the run: %q", res.PR.URL)
	}
	if res.CommitSha != "verifiedsha" {
		t.Fatalf("the tip must come from the forge, got %q", res.CommitSha)
	}
	// THE INTEGRITY GATE ASKED THE FORGE, and asked about THIS org's repository.
	var verified bool
	for _, a := range p.asked {
		if strings.HasPrefix(a, "GET /v1/repos/hanzoai/api/branches/agent/") {
			verified = true
		}
	}
	if !verified {
		t.Fatalf("the forge was never asked whether the branch landed: %v", p.asked)
	}
	// ISOLATION: the sandbox is pointed only at THIS org's namespace.
	if !strings.Contains(run.gotReq.Remote, "hanzoai/api.git") {
		t.Fatalf("the sandbox remote did not survive the crossing: %q", run.gotReq.Remote)
	}
	if !strings.Contains(run.gotReq.Key, "OPENSSH PRIVATE KEY") {
		t.Fatal("the credential must reach the sandbox unchanged")
	}
	// The session opened, streamed and closed — all three ops, all across.
	if len(p.sessions.opened) != 1 || p.sessions.opened[0].org != "hanzo" {
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
		todo:     &fakePR{ref: PRRef{Identifier: "API-8"}},
		found:    false,
	}
	servePeers(t, p)

	run := &fakeRunner{result: RunResult{Changed: true, OK: true, CommitSha: "deadbeef"}}
	res := planeDispatcher(run).Run(context.Background(), Req{
		Org: "hanzo", UserID: "u_1", Actor: "zoe", Repo: "api", Prompt: "fix",
		Remote: "git@git.test:hanzoai/api.git", Key: "k", Known: "git.test ssh-ed25519 AAAAPIN",
	})

	if res.OK || !strings.Contains(res.Error, "not found in native git") {
		t.Fatalf("an unverified ref must fail closed, got %+v", res)
	}
	if len(p.todo.inputs) != 0 {
		t.Fatal("no PR may be filed for a branch git cannot see")
	}
	if len(p.sessions.closes) != 1 || p.sessions.closes[0].status != statusError {
		t.Fatalf("the session must close error: %+v", p.sessions.closes)
	}
}

// A peer that is not part of the deployment is an honest error, not a run that
// proceeds without it. This is the shape the whole path had in production.
func TestRun_OverThePlane_MissingPeerFailsHonestly(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", sock.Dir(t)) // nothing listening at all
	res := planeDispatcher(&fakeRunner{}).Run(context.Background(), Req{
		Org: "hanzo", UserID: "u_1", Actor: "zoe", Repo: "api", Prompt: "fix",
		Remote: "git@git.test:hanzoai/api.git", Key: "k", Known: "git.test ssh-ed25519 AAAAPIN",
	})
	if res.OK {
		t.Fatalf("a run with no peers at all must not succeed: %+v", res)
	}
	// It names the peer it could not reach. "Something went wrong" sends an
	// operator to read code; naming the socket sends them to the right process.
	if !strings.Contains(res.Error, "agents") {
		t.Fatalf("the failure must name the absent peer, got %q", res.Error)
	}
	if res.Verified {
		t.Fatal("nothing may read as verified when nothing could be asked")
	}
}

// socketDir is a runtime dir SHORT enough to hold a unix socket path.
//
// t.TempDir() embeds the test's NAME, and a unix socket path is capped at 104
// bytes on darwin and 108 on linux (sun_path). The descriptive test names in
// this package push a t.TempDir() socket past that, and the failure is not a
// bind error a reader would recognise — the listener simply never comes up and
// every peer reads as absent, which is indistinguishable from the production
// bug these tests exist to catch.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "z")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

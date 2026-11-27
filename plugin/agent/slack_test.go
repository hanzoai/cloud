package main

// slack_test.go — a Slack coding request, end to end, with nothing between the
// question and the pull request but the code that runs in production.
//
// Someone in Slack asks "can you update 'Ship It' on hanzo.ai to 'Get Started' in
// hero?". Afterwards five things are true, and each is asserted on the thing that
// holds it rather than on a return value:
//
//	the room     saw ONE message — posted once, edited in place — ending in the PR link
//	the sandbox  was leased for the org that asked, and kept for its person
//	the forge    holds agent/<id>, with "Get Started" where "Ship It" was
//	the review   a pull request was opened for that branch
//	a refusal    a push the forge refuses ends the message in the forge's own words,
//	             and the sandbox is still kept
//
// Only the models are scripted: the assistant that decides to call the coding
// tool, and the harness inside the sandbox that makes the edit. Channels, agents,
// the coding engine, the fleet's MCP server and the forge client are the
// production code. The sandbox runs real git in a directory of its own, the forge
// is an HTTP server over a real bare repository, and the peers this path only
// asks a question of — the Slack transport, the prepaid gate, KMS, IAM, the board
// — answer on their own sockets, the way their apps do.
//
// The event enters where the Slack adapter hands it over: the channels ingest op,
// with the envelope the adapter builds from a signed Slack message. The adapter's
// own routing is apps/provider's to test.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/transport"
	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	environmentpeer "github.com/hanzoai/cloud/client/environment"
	iampeer "github.com/hanzoai/cloud/client/iam"
	prefpeer "github.com/hanzoai/cloud/client/pref"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	taskpeer "github.com/hanzoai/cloud/client/task"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	transportpeer "github.com/hanzoai/cloud/client/transport"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// Who is asking, and where. The org is one the forge maps (forge.Owner), so the
// run works in its real namespace; the asker's forge login is what the forge
// derives from their address (forge.Login).
const (
	askOrg     = "hanzo"
	askTeam    = "T0HANZO"
	askRoom    = "C0SITE"
	askUser    = "U0ASKER"
	askSubject = "u-asker"
	askEmail   = "asker@hanzo.ai"
	askLogin   = "asker"
	askText    = "can you update 'Ship It' on hanzo.ai to 'Get Started' in hero?"

	forgeToken = "forge-machine-token"
	forgeKey   = "git.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeForgeHostKeyForTheTest"
	refusal    = "push refused: agent branches are closed on this repository"
)

// TestASlackCodingRequest drives both endings through one deployment: the change
// that lands as a pull request, and the push the forge refuses.
func TestASlackCodingRequest(t *testing.T) {
	kit.Tool(t, "git")
	d := bringUp(t)

	t.Run("the change lands as a pull request, in one message", func(t *testing.T) {
		from, leases := d.slack.mark(), d.sandbox.count()
		d.ask(t, "1700000001.000100")
		sends := d.slack.settle(t, from)
		end := sends[len(sends)-1].Text

		// The run's own buttons are on the one message while it works, and a
		// finished run shows none.
		var buttons bool
		for _, s := range sends {
			buttons = buttons || (s.Controls.Kind == client.ControlsCoding && s.Controls.Session != "")
		}
		if !buttons {
			t.Fatalf("the run's Stop and Pause never reached the message:\n%s", transcript(sends))
		}
		if c := sends[len(sends)-1].Controls; c.Session != "" {
			t.Fatalf("the finished run still shows buttons: %+v", c)
		}

		lease := d.sandbox.since(t, leases)
		if lease.org != askOrg || lease.class != "dev" {
			t.Fatalf("the sandbox was leased as %+v, want a dev sandbox for %q", lease, askOrg)
		}
		if d.sandbox.gone(lease.id) {
			t.Fatalf("sandbox %s was ended; a run's sandbox is kept for its person", lease.id)
		}

		pr := d.forge.onlyPull(t)
		if !strings.HasPrefix(pr.head, "agent/") || pr.base != "main" {
			t.Fatalf("the pull request is %+v, want agent/<id> into main", pr)
		}
		for _, file := range []string{"components/home/Fold.tsx", "components/home/FooterHeroComposer.tsx"} {
			body := d.forge.show(t, pr.head, file)
			if !strings.Contains(body, "Get Started") || strings.Contains(body, "Ship It") {
				t.Fatalf("%s on %s reads %q; the edit did not land", file, pr.head, body)
			}
			if main := d.forge.show(t, "main", file); !strings.Contains(main, "Ship It") {
				t.Fatalf("main was written: %s reads %q", file, main)
			}
		}
		if !strings.Contains(end, pr.url) {
			t.Fatalf("the message ends %q, which never says where the pull request is (%s)", end, pr.url)
		}
		if !strings.Contains(end, "started a coding run") {
			t.Fatalf("the assistant's own reply was lost from the message: %q", end)
		}
		if n := d.forge.liveKeys(); n != 0 {
			t.Fatalf("%d push keys are still registered after the run", n)
		}
	})

	t.Run("a refused push is said in the forge's words, and the sandbox is kept", func(t *testing.T) {
		d.forge.refuse(t)
		pulls := d.forge.pullCount()
		from, leases := d.slack.mark(), d.sandbox.count()
		d.ask(t, "1700000002.000100")
		sends := d.slack.settle(t, from)
		end := sends[len(sends)-1].Text

		if !strings.Contains(end, "The coding run failed") || !strings.Contains(end, refusal) {
			t.Fatalf("the message ends %q; it must say the run failed, in the forge's words", end)
		}
		lease := d.sandbox.since(t, leases)
		if d.sandbox.gone(lease.id) {
			t.Fatalf("sandbox %s was ended after the push was refused", lease.id)
		}
		if d.forge.pullCount() != pulls {
			t.Fatal("a pull request was opened for a branch the forge refused")
		}
		if n := d.forge.liveKeys(); n != 0 {
			t.Fatalf("%d push keys are still registered after the run", n)
		}
	})
}

// ── the deployment ──────────────────────────────────────────────────────────

type deployment struct {
	slack   *room
	sandbox *pods
	forge   *forgeHost
}

// bringUp stands the path up on real sockets: channels and agents (with the
// coding endpoint, composed exactly as this program composes it) on the plane,
// the fleet's MCP server on the host socket, and every peer on its own.
func bringUp(t *testing.T) *deployment {
	t.Helper()
	dir := kit.Sockets(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	// The plane is one registry per process and it appends, so the mounts below
	// declare every op on a plane nothing else in this binary has written to.
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	d := &deployment{slack: &room{}, sandbox: newPods(t), forge: newForge(t)}
	t.Setenv("CLOUD_FORGE_HOST", d.forge.srv.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)

	peer(t, manifest.Provider, func(a *zip.App) {
		a.Post("/provider/chat-identity", func(_ context.Context, in *client.ChatIdentityIn) (*client.ChatIdentityOut, error) {
			if in.Org != askOrg || in.User != askUser {
				return &client.ChatIdentityOut{Say: "unlinked"}, nil
			}
			return &client.ChatIdentityOut{Subject: askSubject}, nil
		}, zip.WithOperationID(client.ChatIdentity))
		a.Post("/provider/chat-send", d.slack.send, zip.WithOperationID(client.ChatSend))
	})
	peer(t, sandboxpeer.App, d.sandbox.register)
	// The codebase has no environment: the run asks, and starts bare.
	peer(t, environmentpeer.App, func(a *zip.App) {
		a.Post("/environment/prepare", func(context.Context, *client.Codebase) (*client.Prepared, error) {
			return &client.Prepared{}, nil
		}, zip.WithOperationID(client.EnvironmentPrepare))
	})
	// The person set nothing and the org has no skills or MCP servers: the run
	// asks both, and starts bare.
	peer(t, prefpeer.App, func(a *zip.App) {
		a.Post("/pref/own", func(context.Context, *client.PrefsIn) (*client.Prefs, error) {
			return &client.Prefs{Doc: "{}"}, nil
		}, zip.WithOperationID(client.PrefOwn))
	})
	peer(t, toolpeer.App, func(a *zip.App) {
		a.Post("/tools/kit", func(context.Context, *client.KitIn) (*client.Kit, error) {
			return &client.Kit{}, nil
		}, zip.WithOperationID(client.ToolsKit))
	})
	peer(t, "commerce", func(a *zip.App) {
		a.Post("/commerce/authorize", func(context.Context, *client.AuthorizeIn) (*client.Verdict, error) {
			return &client.Verdict{OK: true}, nil
		}, zip.WithOperationID(client.FinanceAuthorize))
	})
	peer(t, "kms", func(a *zip.App) {
		a.Post("/kms/get", func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
			switch in.Ref {
			case forge.TokenRef:
				return &client.Secret{Value: []byte(forgeToken)}, nil
			case forge.HostKeyRef:
				return &client.Secret{Value: []byte(forgeKey)}, nil
			}
			return nil, zip.ErrNotFound("secret not found")
		}, zip.WithOperationID(client.KMSGet))
	})
	peer(t, iampeer.App, func(a *zip.App) {
		a.Post("/iam/email", func(ctx context.Context, _ *struct{}) (*client.Email, error) {
			if cloud.Who(ctx).User != askSubject {
				return nil, zip.ErrNotFound("no such person")
			}
			return &client.Email{Address: askEmail, Verified: true}, nil
		}, zip.WithOperationID(client.IAMEmail))
	})
	peer(t, taskpeer.App, func(a *zip.App) {
		a.Post("/task/agent-pr", func(context.Context, *client.AgentPRIn) (*client.AgentPROut, error) {
			return &client.AgentPROut{Identifier: "AGT-1", ProjectKey: "AGT", Number: 1}, nil
		}, zip.WithOperationID(client.TodoAgentPR))
		a.Post("/task/upsert", func(context.Context, *client.IssueIn) (*client.IssueUpserted, error) {
			return &client.IssueUpserted{Created: true, Number: 1, Identifier: "AGT-1"}, nil
		}, zip.WithOperationID(client.TaskUpsert))
	})

	// agents and the coding endpoint, through this program's own Use, with its
	// agent MCP server on the plane the way cloud.Listen publishes a plugin's.
	agent := zip.New(zip.Config{AppName: agentpeer.App, DisableStartupMessage: true})
	agent.Use(cloud.Bridge())
	if err := mountAgents(agent, cloud.Deps{AI: assistant{}}); err != nil {
		t.Fatalf("mount agents: %v", err)
	}
	t.Cleanup(func() { _ = agents.Shutdown(context.Background()) })
	t.Cleanup(func() { stopKeeping() })
	cloud.UseMCP(agent)
	edge := filepath.Join(dir, "agentapp.sock")
	listen(t, agent, edge)

	// channels, through its own Use.
	ch := zip.New(zip.Config{AppName: transportpeer.App, DisableStartupMessage: true})
	ch.Use(cloud.Bridge())
	if err := transport.Use(ch, cloud.Deps{}); err != nil {
		t.Fatalf("mount channels: %v", err)
	}
	t.Cleanup(func() { _ = transport.Shutdown(context.Background()) })

	// The plane both of them registered their ops on, at both of their names.
	listen(t, cloud.Plane(), zip.SocketPath(agentpeer.App), zip.SocketPath(transportpeer.App))

	// The fleet's MCP server, composed as cmd/cloud composes it: one server, at
	// the edge and — the address a run's tools are asked of — on the host socket,
	// forwarding inside to the subsystem's plane.
	mcp := client.Use(zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}}),
		manifest.MCPPath, []string{agentpeer.App}, func(app string) (string, string, error) {
			if app != agentpeer.App {
				return "", "", &net.AddrError{Err: "no instance running", Addr: app}
			}
			return edge, manifest.FrameworkMCPPath, nil
		})
	host := zip.New(zip.Config{AppName: "plane", DisableStartupMessage: true})
	mcp.Serve(host, manifest.MCPPath, func(app string) (string, string, error) {
		if app != agentpeer.App {
			return "", "", &net.AddrError{Err: "no instance running", Addr: app}
		}
		return zip.SocketPath(agentpeer.App), manifest.MCPPath, nil
	})
	listen(t, host, zip.SocketPath(plane.HostApp))
	return d
}

// ask delivers one Slack message, as the adapter hands a signed event over.
func (d *deployment) ask(t *testing.T, ts string) {
	t.Helper()
	out, err := transportpeer.TransportIngest(context.Background(), &client.TransportIngestIn{
		Org: askOrg, Provider: "slack", ExternalID: askTeam, User: askUser,
		Channel: askRoom, MessageID: ts, Text: askText, DedupeKey: "Ev" + ts, Public: true,
	})
	if err != nil || out == nil || !out.Taken {
		t.Fatalf("channels did not take the message: %+v %v", out, err)
	}
}

// peer serves one app on its canonical socket.
func peer(t *testing.T, name string, register func(*zip.App)) {
	t.Helper()
	a := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	register(a)
	listen(t, a, zip.SocketPath(name))
}

func listen(t *testing.T, a *zip.App, at ...string) {
	t.Helper()
	go func() { _ = a.Listen(at...) }()
	t.Cleanup(func() { _ = a.Shutdown() })
	for _, path := range at {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if c, err := net.Dial("unix", path); err == nil {
				_ = c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never accepted", path)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// ── the model ───────────────────────────────────────────────────────────────

// assistant is the one scripted mind in the chat path: offered the cloud, it asks
// for a coding run on the repository the person named; handed the run's answer,
// it replies.
type assistant struct{}

func (assistant) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role == infer.RoleTool {
			return &infer.ChatResponse{Content: "On it — I started a coding run on hanzo.ai.", FinishReason: "stop"}, nil
		}
	}
	for _, tool := range req.Tools {
		if tool.Name == agentpeer.App {
			in, _ := json.Marshal(map[string]any{"op": "create_agent_coding", "input": map[string]any{
				"repo":   "hanzo.ai",
				"prompt": "In the hero on hanzo.ai, change the call to action 'Ship It' to 'Get Started'.",
				"pull":   true,
			}})
			return &infer.ChatResponse{FinishReason: "tool_calls",
				ToolCalls: []infer.ToolCall{{ID: "call_1", Name: agentpeer.App, Arguments: string(in)}}}, nil
		}
	}
	return nil, errors.New("assistant: the coding tool was not offered")
}

func (assistant) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }

func (assistant) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

// ── Slack ───────────────────────────────────────────────────────────────────

// room is the Slack transport: it records every send and the ts it answered.
type room struct {
	mu    sync.Mutex
	sends []sent
	n     int
}

type sent struct {
	client.ChatSendIn
	ts string
}

func (r *room) send(_ context.Context, in *client.ChatSendIn) (*client.ChatSendOut, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts := in.Edit
	if ts == "" {
		r.n++
		ts = fmt.Sprintf("1800000000.%06d", r.n)
	}
	r.sends = append(r.sends, sent{*in, ts})
	return &client.ChatSendOut{MessageID: ts}, nil
}

// mark is where the next request's sends begin.
func (r *room) mark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sends)
}

// settle waits for the turn that began at from to conclude, asserts it was ONE
// message — "Working…" posted once, every later send an edit of it, all where
// the question was asked — and returns every send it took.
func (r *room) settle(t *testing.T, from int) []sent {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		r.mu.Lock()
		sends := append([]sent(nil), r.sends[from:]...)
		r.mu.Unlock()
		if n := len(sends); n > 0 && concluded(sends[n-1].Text) {
			for _, s := range sends {
				if s.Org != askOrg || s.Channel != askRoom || s.ReplyTo != "" || s.Private {
					t.Fatalf("a send went somewhere the question was not asked: %+v", s.ChatSendIn)
				}
			}
			if sends[0].Edit != "" || sends[0].Text != "Working…" {
				t.Fatalf("the turn did not open with \"Working…\":\n%s", transcript(sends))
			}
			if n < 2 {
				t.Fatalf("the message was never edited:\n%s", transcript(sends))
			}
			for _, s := range sends[1:] {
				if s.Edit != sends[0].ts {
					t.Fatalf("the turn posted a second message instead of editing %s:\n%s", sends[0].ts, transcript(sends))
				}
			}
			t.Logf("the room saw:\n%s", transcript(sends))
			return sends
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn never concluded; the room saw:\n%s", transcript(sends))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// concluded reports whether text is a turn's last word: the assistant's answer,
// which the turn posts only once the run it started has ended, or a failure.
func concluded(text string) bool {
	return strings.Contains(text, "started a coding run") || strings.Contains(text, "couldn't finish") ||
		strings.Contains(text, "couldn't start") || strings.Contains(text, "lost track")
}

func transcript(sends []sent) string {
	var b strings.Builder
	for _, s := range sends {
		verb := "post " + s.ts
		if s.Edit != "" {
			verb = "edit " + s.Edit
		}
		fmt.Fprintf(&b, "  %s: %q\n", verb, s.Text)
	}
	return b.String()
}

// ── the sandbox ─────────────────────────────────────────────────────────────

// pods is the sandbox: each lease is a directory, each command runs there as a
// real process, and the one command it does not run is the coding harness — the
// model inside the box — which makes the edit the request asked for.
//
// A pod's PATH holds only the programs a run's commands call (podTools), so a
// harness this does not recognise fails the run instead of starting whatever
// agent this machine has installed.
type pods struct {
	t      *testing.T
	root   string
	bin    string
	mu     sync.Mutex
	leases []lease
	boxes  map[string]string // id -> its root
	ended  map[string]bool
}

type lease struct{ id, org, class string }

// podTools is every program a run's commands call: git, the shell and file
// tools that install its credential and hand it its key, and those that list
// what it changed.
var podTools = []string{"git", "sh", "bash", "mkdir", "dirname", "cat", "rm", "cp", "stat", "sha256sum", "cut"}

func newPods(t *testing.T) *pods {
	p := &pods{t: t, root: t.TempDir(), boxes: map[string]string{}, ended: map[string]bool{}}
	p.bin = filepath.Join(p.root, "bin")
	if err := os.Mkdir(p.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Each tool is a script that execs the real one, not a link to it: git finds
	// its helpers relative to where its binary really is.
	for _, name := range podTools {
		path := kit.Tool(t, name)[0]
		script := "#!/bin/sh\nexec '" + path + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(p.bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (p *pods) register(a *zip.App) {
	a.Post("/sandbox/lease", func(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		id := "sbx_" + strconv.Itoa(len(p.leases)+1)
		root := filepath.Join(p.root, id)
		for _, d := range []string{"work", "home"} {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				return nil, err
			}
		}
		p.leases = append(p.leases, lease{id: id, org: cloud.Who(ctx).Org, class: in.Class})
		p.boxes[id] = root
		return &client.Leased{ID: id, Class: in.Class, Status: "ready", Workdir: filepath.Join(root, "work")}, nil
	}, zip.WithOperationID(client.SandboxLease))
	a.Post("/sandbox/run", func(ctx context.Context, in *client.RunIn) (*client.Ran, error) {
		p.mu.Lock()
		root, ok := p.boxes[in.ID]
		p.mu.Unlock()
		if !ok {
			return nil, zip.ErrNotFound("no such sandbox")
		}
		ran := p.exec(ctx, root, in)
		return &ran, nil
	}, zip.WithOperationID(client.SandboxRun))
	a.Post("/sandbox/end", func(_ context.Context, in *client.EndIn) (*struct{}, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.ended[in.ID] = true
		return &struct{}{}, nil
	}, zip.WithOperationID(client.SandboxEnd))
	a.Post("/sandbox/park", func(_ context.Context, in *client.ParkIn) (*struct{}, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.ended[in.ID] = true
		return &struct{}{}, nil
	}, zip.WithOperationID(client.SandboxPark))
}

// exec runs one command in a sandbox. The pod's /tmp is the sandbox's own, so the
// run's credential directory under it is rooted there — the one liberty a
// directory takes in standing in for a pod.
func (p *pods) exec(ctx context.Context, root string, in *client.RunIn) client.Ran {
	argv := make([]string, len(in.Argv))
	for i, a := range in.Argv {
		argv[i] = strings.ReplaceAll(a, "/tmp/hanzo-run", filepath.Join(root, "tmp", "hanzo-run"))
	}
	work := filepath.Join(root, "work")
	// The harness is the command a run's credential shell hands on to (keyed,
	// apps/coding): `dev exec …` somewhere after the shell that reads the key.
	if i := slices.Index(argv, "dev"); i >= 0 && i+1 < len(argv) && argv[i+1] == "exec" {
		return harness(work)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(p.bin, argv[0]), argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Dir = work
	cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "PATH=" + p.bin,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	cmd.Stdin = strings.NewReader(in.Stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return client.Ran{ExitCode: 127, Stderr: err.Error()}
		}
		code = exit.ExitCode()
	}
	return client.Ran{ExitCode: code, Stdout: out.String(), Stderr: errb.String()}
}

// harness is the model inside the box: it makes the change the prompt asked for.
func harness(work string) client.Ran {
	changed := 0
	err := filepath.WalkDir(work, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if e.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(b, []byte("Ship It")) {
			return err
		}
		changed++
		return os.WriteFile(path, bytes.ReplaceAll(b, []byte("Ship It"), []byte("Get Started")), 0o644)
	})
	if err != nil {
		return client.Ran{ExitCode: 1, Stderr: err.Error()}
	}
	return client.Ran{Stdout: fmt.Sprintf("changed %d files", changed)}
}

func (p *pods) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.leases)
}

// since is the ONE lease made after the first n: a run takes exactly one sandbox.
func (p *pods) since(t *testing.T, n int) lease {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.leases) != n+1 {
		t.Fatalf("the run took %d sandboxes, want exactly one: %+v", len(p.leases)-n, p.leases[n:])
	}
	return p.leases[n]
}

func (p *pods) gone(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ended[id]
}

// ── the forge ───────────────────────────────────────────────────────────────

// forgeHost is the forge's REST API over bare repositories on disk, answering
// the calls the forge client makes and nothing else. The namespace is the org's
// own (forge.Owner), and the one person it knows is the asker.
type forgeHost struct {
	srv   *httptest.Server
	owner string
	repos map[string]*forgeRepo // by lowercased name

	mu    sync.Mutex
	keys  map[int64]string
	next  int64
	pulls []pull
}

type forgeRepo struct{ name, website, bare string }

type pull struct{ head, base, title, url string }

func newForge(t *testing.T) *forgeHost {
	t.Helper()
	owner, err := forge.Owner(askOrg)
	if err != nil {
		t.Fatal(err)
	}
	f := &forgeHost{owner: owner, repos: map[string]*forgeRepo{}, keys: map[int64]string{}}
	root := t.TempDir()
	f.repos["hanzo.ai"] = &forgeRepo{name: "hanzo.ai", website: "https://hanzo.ai", bare: site(t, root)}
	for _, name := range []string{"cloud", "hanzo.ai-docs"} {
		f.repos[name] = &forgeRepo{name: name, bare: filepath.Join(root, name+".git")}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// site is the hanzo.ai repository: the two files the hero's text lives in, on
// main, in a bare repository a clone and a push can reach.
func site(t *testing.T, root string) string {
	t.Helper()
	src := filepath.Join(root, "src")
	files := map[string]string{
		"components/home/Fold.tsx": "export function Fold() {\n  return <section className=\"hero\">\n" +
			"    <h1>The Open AI Cloud</h1>\n    <Button href=\"/signup\">Ship It</Button>\n  </section>\n}\n",
		"components/home/FooterHeroComposer.tsx": "export function Composer() {\n  return <Button type=\"submit\">Ship It</Button>\n}\n",
		"README.md":                              "# hanzo.ai\n",
	}
	for name, body := range files {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bare := filepath.Join(root, "hanzo.ai.git")
	run(t, src, "git", "init", "-q", "-b", "main")
	run(t, src, "git", "add", "-A")
	run(t, src, "git", "-c", "user.name=site", "-c", "user.email=site@hanzo.ai", "commit", "-q", "-m", "the site")
	run(t, root, "git", "clone", "-q", "--bare", src, bare)
	return bare
}

func run(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return string(out)
}

// refuse makes the repository refuse every push, the way a forge's own hook does.
func (f *forgeHost) refuse(t *testing.T) {
	t.Helper()
	hook := filepath.Join(f.repos["hanzo.ai"].bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho '"+refusal+"' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// show is a file as the forge holds it on a branch.
func (f *forgeHost) show(t *testing.T, branch, file string) string {
	t.Helper()
	return run(t, f.repos["hanzo.ai"].bare, "git", "show", branch+":"+file)
}

func (f *forgeHost) onlyPull(t *testing.T) pull {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pulls) != 1 {
		t.Fatalf("want one pull request, the forge holds %d: %+v", len(f.pulls), f.pulls)
	}
	return f.pulls[0]
}

func (f *forgeHost) pullCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pulls)
}

func (f *forgeHost) liveKeys() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

func (f *forgeHost) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+forgeToken {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	// Sudo drops to the one person this forge knows; anyone else is nobody.
	if sudo := r.Header.Get("Sudo"); sudo != "" && sudo != askLogin {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, forge.API)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "users" && r.Method == http.MethodGet:
		if parts[1] != askLogin {
			http.NotFound(w, r)
			return
		}
		reply(w, http.StatusOK, map[string]string{"login": askLogin, "email": askEmail})
	case len(parts) == 3 && parts[0] == "orgs" && parts[2] == "repos" && r.Method == http.MethodGet:
		f.list(w, r, parts[1])
	case len(parts) >= 3 && parts[0] == "repos":
		f.repo(w, r, parts[1], parts[2], parts[3:])
	default:
		http.NotFound(w, r)
	}
}

func (f *forgeHost) list(w http.ResponseWriter, r *http.Request, owner string) {
	if owner != f.owner {
		http.NotFound(w, r)
		return
	}
	names := make([]string, 0, len(f.repos))
	for name := range f.repos {
		names = append(names, name)
	}
	sort.Strings(names)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if limit <= 0 {
		limit = 50
	}
	if page <= 0 {
		page = 1
	}
	out := []map[string]any{}
	for i := (page - 1) * limit; i < len(names) && i < page*limit; i++ {
		out = append(out, f.view(f.repos[names[i]]))
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(names)))
	reply(w, http.StatusOK, out)
}

func (f *forgeHost) view(repo *forgeRepo) map[string]any {
	return map[string]any{
		"name": repo.name, "full_name": f.owner + "/" + repo.name, "private": true,
		"default_branch": "main", "ssh_url": "file://" + repo.bare, "website": repo.website,
		"permissions": map[string]bool{"push": true, "pull": true},
	}
}

func (f *forgeHost) repo(w http.ResponseWriter, r *http.Request, owner, name string, rest []string) {
	repo, ok := f.repos[strings.ToLower(name)]
	if owner != f.owner || !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		reply(w, http.StatusOK, f.view(repo))
	case len(rest) == 1 && rest[0] == "branch_protections" && r.Method == http.MethodGet:
		reply(w, http.StatusOK, []any{protected})
	case len(rest) == 2 && rest[0] == "branch_protections" && r.Method == http.MethodGet:
		if rest[1] != "main" {
			http.NotFound(w, r)
			return
		}
		reply(w, http.StatusOK, protected)
	case len(rest) == 1 && rest[0] == "keys":
		f.keyring(w, r)
	case len(rest) == 2 && rest[0] == "keys" && r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(rest[1], 10, 64)
		f.mu.Lock()
		delete(f.keys, id)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case len(rest) >= 2 && rest[0] == "branches" && r.Method == http.MethodGet:
		branch := strings.Join(rest[1:], "/")
		out, err := exec.Command("git", "--git-dir", repo.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		reply(w, http.StatusOK, map[string]any{"name": branch, "commit": map[string]string{"id": strings.TrimSpace(string(out))}})
	case len(rest) == 1 && rest[0] == "pulls" && r.Method == http.MethodPost:
		var in struct{ Head, Base, Title string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		f.mu.Lock()
		p := pull{head: in.Head, base: in.Base, title: in.Title,
			url: fmt.Sprintf("%s/%s/%s/pulls/%d", f.srv.URL, f.owner, repo.name, len(f.pulls)+1)}
		f.pulls = append(f.pulls, p)
		f.mu.Unlock()
		reply(w, http.StatusCreated, map[string]any{"html_url": p.url, "number": len(f.pulls)})
	case len(rest) == 1 && rest[0] == "pulls" && r.Method == http.MethodGet:
		reply(w, http.StatusOK, []any{})
	default:
		http.NotFound(w, r)
	}
}

// protected is the default branch's rule: pushes are whitelisted and a deploy key
// is not on the list, so a run's key can write its own branch and never main.
var protected = map[string]any{
	"rule_name": "main", "enable_push": true, "enable_push_whitelist": true,
	"push_whitelist_deploy_keys": false, "enable_force_push": false,
}

func (f *forgeHost) keyring(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		out := []map[string]any{}
		for id, title := range f.keys {
			out = append(out, map[string]any{"id": id, "title": title, "created_at": time.Now().UTC()})
		}
		reply(w, http.StatusOK, out)
	case http.MethodPost:
		var in struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		f.next++
		f.keys[f.next] = in.Title
		reply(w, http.StatusCreated, map[string]any{"id": f.next})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

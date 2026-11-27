package coding

// github_test.go pins a run whose repository lives on GitHub: where its home is
// read from, what the sandbox is handed, where the branch and the proposal go,
// and that the token ends with the run and never reaches a line anybody reads.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// ghToken is the run's token. Spelled once so every assertion about where it
// may and may not appear asks about the same string.
const ghToken = "ghs_RUNTOKENMATERIALTHATMUSTNEVERLEAK0001"

// ghRead is the token GitHub mints beside it for the agent's own git, a read-only one: asked for
// after a run's write token, as every run's is.
const ghRead = "ghs_READONLYTOKENMATERIALTHATMUSTNEVERLEAK2"

// rig is every peer a run reaches, on one runtime dir, recording what arrived.
type rig struct {
	mu      sync.Mutex
	mirror  bool
	origin  string
	writers map[string]bool
	keys    int      // forge deploy keys minted
	synced  int      // forge mirror syncs asked for
	minted  []string // "<org> <owner>/<repo>" the App was asked to mint for
	revoked []string // Authorization of every token revocation GitHub saw
	pulls   []map[string]string
	// people is every assignment and review request GitHub saw, as
	// "<path> <logins joined by comma>".
	people []string
	// refusePeople answers every assignment and review request 422, as GitHub
	// does for a login that is not a collaborator.
	refusePeople bool
	tip          string // what GitHub says the run's branch points at; "" is absent
	// onePull makes GitHub hold one open pull request per head, as it does: a second open
	// of the same head is refused 422, and the open one is read back by its head.
	onePull bool
	// refs, when set, is what GitHub says each branch points at, by name, instead
	// of tip for all of them; a name it does not hold is absent.
	refs map[string]string
	// admit is the provider's admission of a run on a repository GitHub alone
	// holds: nil refuses everything, as a deployment that admits nobody would.
	admit func(c zip.Caller, owner, repo string) (*client.GitHubRun, error)
	// admitted is every caller the admission was asked for, as the plane saw it.
	admitted []zip.Caller
	// reads is whether each mint the App was asked for (grant or run) could only
	// read, in the order they came.
	reads     []bool
	forgeHits int // every request the forge saw

	sessions *fakeSessions
	todo     *fakePR
	pod      *pod
	// secrets are values the fake KMS holds, by ref, beside the forge's own.
	secrets map[string]string
}

func newRig() *rig {
	return &rig{
		// The live shape: a .git suffix, and an owner the repository has since
		// been transferred away from. The App answers where it lives now.
		mirror: true, origin: "https://github.com/hanzoai/site.git",
		writers:  map[string]bool{"zoe": true},
		sessions: &fakeSessions{id: "sess_abc123def456"},
		todo:     &fakePR{ref: PRRef{Identifier: "SITE-1"}},
		pod:      &pod{},
	}
}

// serve stands the rig up: the forge and GitHub over HTTP, and every plane peer
// on real sockets in one runtime dir.
func (r *rig) serve(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", socketDir(t))

	fg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.forgeHits++
		actor := q.Header.Get("Sudo")
		switch {
		case strings.HasPrefix(q.URL.Path, "/v1/users/"):
			login := strings.TrimPrefix(q.URL.Path, "/v1/users/")
			_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "email": login + "@hanzo.ai"})
		case strings.HasSuffix(q.URL.Path, "/keys") && q.Method == http.MethodPost:
			r.keys++
			var key struct {
				ReadOnly bool `json:"read_only"`
			}
			_ = json.NewDecoder(q.Body).Decode(&key)
			r.reads = append(r.reads, key.ReadOnly)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case strings.HasSuffix(q.URL.Path, "/keys"):
			_ = json.NewEncoder(w).Encode([]any{})
		case strings.HasSuffix(q.URL.Path, "/branch_protections"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"rule_name": "main", "enable_push": false}})
		case strings.HasSuffix(q.URL.Path, "/mirror-sync"):
			r.synced++
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "site", "full_name": "hanzoai/site", "default_branch": "main",
				"ssh_url": "git@git.test:hanzoai/site.git",
				"mirror":  r.mirror, "original_url": r.origin,
				"permissions": map[string]any{"pull": true, "push": actor == "" || r.writers[actor]},
			})
		}
	}))
	t.Cleanup(fg.Close)
	t.Setenv("CLOUD_FORGE_HOST", fg.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if q.Method == http.MethodDelete && q.URL.Path == "/installation/token" {
			r.revoked = append(r.revoked, q.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if q.Header.Get("Authorization") != "Bearer "+ghToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasPrefix(q.URL.Path, "/repos/hanzo-apps/site/git/ref/heads/"):
			tip := r.tip
			if r.refs != nil {
				tip = r.refs[strings.TrimPrefix(q.URL.Path, "/repos/hanzo-apps/site/git/ref/heads/")]
			}
			if tip == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": tip}})
		case q.URL.Path == "/repos/hanzo-apps/site/pulls" && q.Method == http.MethodGet && r.onePull:
			// The open pull request whose head is asked for: GitHub's `head=owner:branch`.
			open := []map[string]any{}
			for _, p := range r.pulls {
				if "hanzo-apps:"+p["head"] == q.URL.Query().Get("head") {
					open = append(open, map[string]any{"number": 9, "html_url": "https://github.com/hanzo-apps/site/pull/9"})
					break
				}
			}
			_ = json.NewEncoder(w).Encode(open)
		case q.URL.Path == "/repos/hanzo-apps/site/pulls" && q.Method == http.MethodPost:
			var asked map[string]any
			_ = json.NewDecoder(q.Body).Decode(&asked)
			in := map[string]string{}
			for k, v := range asked {
				in[k] = fmt.Sprint(v)
			}
			if r.onePull && slices.ContainsFunc(r.pulls, func(p map[string]string) bool { return p["head"] == in["head"] }) {
				w.WriteHeader(http.StatusUnprocessableEntity) // GitHub: a pull request for this head already exists
				return
			}
			r.pulls = append(r.pulls, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 9, "html_url": "https://github.com/hanzo-apps/site/pull/9"})
		case q.Method == http.MethodPost && (q.URL.Path == "/repos/hanzo-apps/site/issues/9/assignees" ||
			q.URL.Path == "/repos/hanzo-apps/site/pulls/9/requested_reviewers"):
			var asked struct {
				Assignees []string `json:"assignees"`
				Reviewers []string `json:"reviewers"`
			}
			_ = json.NewDecoder(q.Body).Decode(&asked)
			r.people = append(r.people, q.URL.Path+" "+strings.Join(append(asked.Assignees, asked.Reviewers...), ","))
			if r.refusePeople {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 9})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(gh.Close)
	was := githubAPI
	githubAPI = gh.URL
	t.Cleanup(func() { githubAPI = was })

	kms := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	kms.Post("/kms/get", func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
		switch in.Ref {
		case forge.TokenRef:
			return &client.Secret{Value: []byte("machine")}, nil
		case forge.HostKeyRef:
			return &client.Secret{Value: []byte("git.test ssh-ed25519 AAAAPINNED")}, nil
		}
		if v, ok := r.secrets[in.Ref]; ok {
			return &client.Secret{Value: []byte(v)}, nil
		}
		return nil, zip.ErrNotFound("no such secret")
	}, zip.WithOperationID(client.KMSGet))

	iam := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	iam.Post("/iam/email", func(context.Context, *struct{}) (*client.Email, error) {
		e := verified("zoe@hanzo.ai")
		return &e, nil
	}, zip.WithOperationID(client.IAMEmail))

	prov := zip.New(zip.Config{AppName: providerpeer.App, DisableStartupMessage: true})
	prov.Post("/integrations/github/grant", func(ctx context.Context, in *client.GitHubGrantIn) (*client.GitHubGrant, error) {
		r.mu.Lock()
		r.minted = append(r.minted, zip.CallerOf(ctx).Org+" "+in.Owner+"/"+in.Repo)
		tok := ghToken
		if in.Read && slices.Contains(r.reads, false) {
			tok = ghRead
		}
		r.reads = append(r.reads, in.Read)
		r.mu.Unlock()
		return &client.GitHubGrant{Token: tok, Owner: "hanzo-apps", Repo: "site"}, nil
	}, zip.WithOperationID(client.IntegrationsGitHubGrant))
	prov.Post("/integrations/github/run", func(ctx context.Context, in *client.GitHubRunIn) (*client.GitHubRun, error) {
		r.mu.Lock()
		c := zip.CallerOf(ctx)
		r.admitted = append(r.admitted, c)
		after := in.Read && slices.Contains(r.reads, false)
		r.reads = append(r.reads, in.Read)
		admit := r.admit
		r.mu.Unlock()
		if admit == nil {
			return nil, zip.ErrForbidden("github run: connect your GitHub account, or ask an org admin")
		}
		run, err := admit(c, in.Owner, in.Repo)
		if err != nil || !after {
			return run, err
		}
		reader := *run
		reader.Token = ghRead
		return &reader, nil
	}, zip.WithOperationID(client.IntegrationsGitHubRun))

	agent := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	agent.Post("/agents/session/open", func(ctx context.Context, in *client.SessionOpenIn) (*client.SessionOpened, error) {
		id, _ := r.sessions.OpenOn(ctx, in.Org, in.Actor, in.Agent, in.Title, in.Target, in.Conversation)
		return &client.SessionOpened{SessionID: id}, nil
	}, zip.WithOperationID(client.AgentsSessionOpen))
	agent.Post("/agents/session/event", func(ctx context.Context, in *client.SessionEventIn) (*client.CodingAck, error) {
		if err := r.sessions.Log(ctx, in.Org, in.SessionID, in.Kind, in.Actor, in.Payload); err != nil {
			return nil, err
		}
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsSessionEvent))
	agent.Post("/agents/session/close", func(ctx context.Context, in *client.SessionCloseIn) (*client.CodingAck, error) {
		_ = r.sessions.Close(ctx, in.Org, in.SessionID, in.Status)
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsSessionClose))
	agent.Post("/agents/session/tag", func(ctx context.Context, in *client.SessionTagIn) (*client.CodingAck, error) {
		if err := r.sessions.Tag(ctx, in.Org, in.SessionID, Tag{Kind: in.Kind, Repo: in.Repo, Base: in.Base,
			Branch: in.Branch, Environment: in.Environment, PR: in.PR, Mode: in.Mode, Sandbox: in.Sandbox, Project: in.Project}); err != nil {
			return nil, err
		}
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsSessionTag))
	// A run on a machine asks the gate on its own request and is queued by the
	// agents process; this rig's machines are always there and listening.
	agent.Post("/agents/target-gate", func(context.Context, *client.TargetGateIn) (*client.CodingAck, error) {
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsTargetGate))
	agent.Post("/agents/route-run", func(context.Context, *client.RouteRunIn) (*client.CodingAck, error) {
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsRouteRun))

	task := zip.New(zip.Config{AppName: "task", DisableStartupMessage: true})
	task.Post("/todo/agent-pr", func(ctx context.Context, in *client.AgentPRIn) (*client.AgentPROut, error) {
		ref, _ := r.todo.Open(ctx, PRInput{Org: zip.CallerOf(ctx).Org, Repo: in.Repo, Base: in.Base,
			Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee, Request: in.Request})
		return &client.AgentPROut{Identifier: ref.Identifier}, nil
	}, zip.WithOperationID(client.TodoAgentPR))
	task.Post("/task/upsert", func(context.Context, *client.IssueIn) (*client.IssueUpserted, error) {
		return &client.IssueUpserted{}, nil
	}, zip.WithOperationID(client.TaskUpsert))

	box := zip.New(zip.Config{AppName: sandboxpeer.App, DisableStartupMessage: true})
	box.Post("/sandbox/lease", func(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
		r.pod.mu.Lock()
		if r.pod.leaseErr != nil {
			if err := r.pod.leaseErr(); err != nil {
				r.pod.mu.Unlock()
				return nil, err
			}
		}
		r.pod.leased = append(r.pod.leased, *in)
		r.pod.lessees = append(r.pod.lessees, zip.CallerOf(ctx))
		r.pod.mu.Unlock()
		return &client.Leased{ID: "sbx_1", Class: in.Class, Status: "ready", Workdir: "/work"}, nil
	}, zip.WithOperationID(client.SandboxLease))
	box.Post("/sandbox/run", func(_ context.Context, in *client.RunIn) (*client.Ran, error) {
		r.pod.mu.Lock()
		r.pod.timeouts = append(r.pod.timeouts, in.TimeoutSec)
		r.pod.mu.Unlock()
		ran := r.pod.exec(in.Argv, in.Stdin, in.Blind...)
		return &ran, nil
	}, zip.WithOperationID(client.SandboxRun))
	box.Post("/sandbox/end", func(context.Context, *client.EndIn) (*struct{}, error) {
		r.pod.mu.Lock()
		r.pod.ends++
		r.pod.mu.Unlock()
		return &struct{}{}, nil
	}, zip.WithOperationID(client.SandboxEnd))
	box.Post("/sandbox/stop", func(context.Context, *client.StopIn) (*client.Stopped, error) {
		return &client.Stopped{}, nil
	}, zip.WithOperationID(client.SandboxStop))

	commerce := zip.New(zip.Config{AppName: "commerce", DisableStartupMessage: true})
	commerce.Post("/commerce/authorize", func(context.Context, *client.AuthorizeIn) (*client.Verdict, error) {
		return &client.Verdict{OK: true}, nil
	}, zip.WithOperationID(client.FinanceAuthorize))

	for name, app := range map[string]*zip.App{
		"kms": kms, "iam": iam, providerpeer.App: prov, "agent": agent, "task": task,
		sandboxpeer.App: box, "commerce": commerce,
	} {
		plane.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
}

// A MIRROR OF GITHUB IS HOMED ON GITHUB. The run is handed that repository's
// HTTPS address — where it lives now, not the name the mirror kept from before
// a transfer — and a token minted for this org, and the forge's copy is given no
// key at all.
func TestDelegate_AGitHubMirrorIsHomedOnGitHub(t *testing.T) {
	r := newRig()
	r.serve(t)

	g, err := delegate(asCaller("sub-zoe"), estate("hanzo"), "zoe", "site", "sess_1", false)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	t.Cleanup(func() { _ = withdraw(context.Background(), "hanzo", g) })
	if g.hub == nil || g.Remote != "https://github.com/hanzo-apps/site.git" {
		t.Fatalf("the run was not homed on GitHub: remote %q", g.Remote)
	}
	if g.Key != ghToken || g.Known != "" {
		t.Fatalf("the run holds the wrong credential for an HTTPS home")
	}
	if !slices.Equal(r.minted, []string{"hanzo hanzoai/site"}) {
		t.Fatalf("the App was asked %v, want one token for the mirror's origin as org hanzo", r.minted)
	}
	if r.keys != 0 {
		t.Fatalf("a key was minted on the forge's copy, which nothing pushed to can leave")
	}
}

// THE FORGE STILL DECIDES WHO MAY RUN. A member who cannot push the forge's copy
// is refused before GitHub is asked for anything.
func TestDelegate_AReaderOfTheMirrorGetsNoToken(t *testing.T) {
	r := newRig()
	r.serve(t)

	if _, err := delegate(asCaller("sub-zoe"), estate("hanzo"), "reader", "site", "sess_1", false); err == nil {
		t.Fatal("a member who cannot push the mirror was handed a token to its origin")
	}
	if len(r.minted) != 0 {
		t.Fatalf("a refused run still minted: %v", r.minted)
	}
}

// ANYTHING ELSE IS ITS OWN HOME, and takes today's path: a forge key, and GitHub
// never asked.
func TestDelegate_AForgeHomeKeepsTheForgePath(t *testing.T) {
	for name, set := range map[string]func(*rig){
		"forge-native":          func(r *rig) { r.mirror = false },
		"a mirror of no origin": func(r *rig) { r.origin = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			set(r)
			r.serve(t)

			g, err := delegate(asCaller("sub-zoe"), estate("hanzo"), "zoe", "site", "sess_1", false)
			if err != nil {
				t.Fatalf("delegate: %v", err)
			}
			if g.hub != nil || !strings.HasPrefix(g.Remote, "git@git.test:") {
				t.Fatalf("a forge home was sent elsewhere: %q", g.Remote)
			}
			if r.keys != 1 || len(r.minted) != 0 {
				t.Fatalf("keys=%d minted=%v, want one forge key and no GitHub token", r.keys, r.minted)
			}
		})
	}
}

// THE TOKEN ENDS WITH THE RUN, and a shutdown ends the ones still running.
func TestAGitHubTokenIsRevokedOnWithdrawAndOnDrain(t *testing.T) {
	r := newRig()
	r.serve(t)
	live.Lock()
	live.m = nil
	live.Unlock()

	g, err := delegate(asCaller("sub-zoe"), estate("hanzo"), "zoe", "site", "sess_1", false)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if err := withdraw(context.Background(), "hanzo", g); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if !slices.Equal(r.revoked, []string{"Bearer " + ghToken}) {
		t.Fatalf("GitHub saw revocations %d, want the run's token once", len(r.revoked))
	}
	if r.synced != 1 {
		t.Fatalf("the forge's copy was not told to follow its home")
	}

	if _, err := delegate(asCaller("sub-zoe"), estate("hanzo"), "zoe", "site", "sess_2", false); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if n := Drain(context.Background()); n != 1 || len(r.revoked) != 2 {
		t.Fatalf("drain withdrew %d and GitHub saw %d revocations, want 1 and 2", n, len(r.revoked))
	}
}

// start runs Start against the rig to its end and returns every log line.
func (r *rig) start(t *testing.T) []string {
	t.Helper()
	engineOnce = sync.Once{}
	t.Cleanup(func() { engineOnce = sync.Once{} })

	var mu sync.Mutex
	var said []string
	logf := func(msg string, kv ...any) {
		b, _ := json.Marshal(kv)
		mu.Lock()
		said = append(said, fmt.Sprint(append([]any{msg}, kv...)...), string(b))
		mu.Unlock()
	}
	acc, err := Start(asCaller("sub-zoe"), "hanzo", "sub-zoe",
		client.CodingStartIn{Repo: "site", Prompt: "fix the header", Pull: true}, logf)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	flights.Lock()
	f := flights.m[acc.SessionID]
	flights.Unlock()
	if f != nil {
		select {
		case <-f.done:
		case <-time.After(20 * time.Second):
			t.Fatal("the run never ended")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), said...)
}

// A RUN HOMED ON GITHUB works there end to end: it clones the origin, pushes its
// branch there, is verified there, proposes there — and its token is fed only
// to the two commands that reach GitHub, revoked when it ends, and never
// written anywhere a person reads.
func TestStart_AGitHubHomeClonesPushesAndProposesThere(t *testing.T) {
	r := newRig()
	tip := theSHA
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: "token is " + ghToken + "\n"} // a harness that echoes it
		}
		return client.Ran{}
	}
	r.tip = newSHA
	r.serve(t)

	said := r.start(t)

	r.pod.mu.Lock()
	ran, stdin := r.pod.ran, r.pod.fed
	r.pod.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()

	fed := 0
	for i, argv := range ran {
		line := strings.Join(argv, " ")
		if strings.Contains(line, ghToken) {
			t.Fatalf("the token is on argv: %q", line)
		}
		if !strings.Contains(stdin[i], ghToken) {
			continue
		}
		fed++
		for _, want := range []string{"https://github.com/hanzo-apps/site.git", "credential.helper= ",
			"credential.https://github.com.helper=", "core.hooksPath=/dev/null", "read -r " + tokenVar} {
			if !strings.Contains(line, want) {
				t.Fatalf("a command fed the token lacks %q: %q", want, line)
			}
		}
	}
	if fed != 2 {
		t.Fatalf("the token was fed to %d commands, want clone and push:\n%s", fed, strings.Join(r.pod.lines(), "\n"))
	}
	if _, ok := r.pod.did("HEAD:refs/heads/agent/abc123def456"); !ok {
		t.Fatalf("the run's branch was not pushed to its home")
	}
	if len(r.pulls) != 1 || r.pulls[0]["head"] != "agent/abc123def456" {
		t.Fatalf("GitHub was not asked to propose the run's branch: %v", r.pulls)
	}
	if body := r.pulls[0]["body"]; !strings.Contains(body, "for `zoe`") || strings.Contains(body, "@") {
		t.Fatalf("the proposal must name its person as text: %q", body)
	}
	var done bool
	for _, e := range r.sessions.events {
		if strings.Contains(e.payload, ghToken) {
			t.Fatalf("the token is in a session event: %s", e.payload)
		}
		if strings.Contains(e.payload, `"url":"https://github.com/hanzo-apps/site/pull/9"`) {
			done = true
		}
	}
	if !done {
		t.Fatalf("the session does not point at the GitHub pull request: %+v", r.sessions.events)
	}
	for _, in := range r.todo.inputs {
		if strings.Contains(in.Body+in.Title, ghToken) {
			t.Fatal("the token is on the board")
		}
	}
	for _, l := range said {
		if strings.Contains(l, ghToken) {
			t.Fatalf("the token is in a log line: %s", l)
		}
	}
	if !slices.Equal(r.revoked, []string{"Bearer " + ghRead, "Bearer " + ghToken}) {
		t.Fatalf("the run's tokens were revoked %d times, want the read-only one and then the run's own", len(r.revoked))
	}
	if r.keys != 0 {
		t.Fatal("a forge key was minted for a GitHub home")
	}
}

// A RUN THAT FAILS still gives its token back, and proposes nothing.
func TestStart_AFailedGitHubRunStillRevokes(t *testing.T) {
	r := newRig()
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{ExitCode: 1, Stderr: "boom"}
		}
		return client.Ran{}
	}
	r.serve(t)

	said := r.start(t)

	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.revoked, []string{"Bearer " + ghRead, "Bearer " + ghToken}) {
		t.Fatalf("a failed run's tokens were revoked %d times, want the read-only one and then the run's own", len(r.revoked))
	}
	if len(r.pulls) != 0 || r.pod.gitVerb("push") {
		t.Fatal("a failed run pushed or proposed")
	}
	for _, l := range said {
		if strings.Contains(l, ghToken) {
			t.Fatalf("the token is in a log line: %s", l)
		}
	}
}

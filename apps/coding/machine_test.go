package coding

// machine_test.go pins a run sent to a person's own machine through Start — the
// whole path, with the real machine registry behind the plane. The machine is
// checked on the caller's own request before anything is opened, and checked
// again when the run is dispatched, from a context its request is gone from, as
// the same caller: in a held org a machine is its owner's alone, and a check
// asked as nobody finds no machine there.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"

	// devmaster keys this test binary: the machine registry is an org database,
	// which opens nothing without a master, and a test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/zap-proto/zip"
)

// machines is the agents app — its HTTP surface, where a person links a machine
// and its runner polls, and its plane ops, where a run opens its session, asks
// the gate and is queued — with a GitHub that admits everyone.
type machines struct {
	http   *zip.App
	mu     sync.Mutex
	opened int
	gated  []zip.Caller // every caller the gate was asked as
	queued []zip.Caller // every caller the queue was asked as
	routed chan client.RouteRunIn
}

// callerOrg is the plane ops' rule (plugin/agent): the org is the caller's, and
// an argument may repeat it and never contradict it.
func callerOrg(ctx context.Context, named string) (string, error) {
	org := strings.TrimSpace(zip.CallerOf(ctx).Org)
	if org == "" || named != "" && named != org {
		return "", zip.ErrForbidden("agents: the org named is not the caller's")
	}
	return org, nil
}

func serveMachines(t *testing.T) *machines {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", socketDir(t))
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	m := &machines{routed: make(chan client.RouteRunIn, 4)}

	m.http = zip.New(zip.Config{DisableStartupMessage: true})
	m.http.Use(cloud.Bridge())
	if err := agents.Use(m.http, cloud.Deps{}); err != nil {
		t.Fatalf("agents: %v", err)
	}
	t.Cleanup(func() { _ = agents.Shutdown(context.Background()) })

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method == http.MethodDelete && q.URL.Path == "/installation/token" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(gh.Close)
	was := githubAPI
	githubAPI = gh.URL
	t.Cleanup(func() { githubAPI = was })

	agent := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
	agent.Post("/agents/session/open", func(ctx context.Context, in *client.SessionOpenIn) (*client.SessionOpened, error) {
		org, err := callerOrg(ctx, in.Org)
		if err != nil {
			return nil, err
		}
		id, err := agents.OpenSessionOn(ctx, org, in.Actor, in.Agent, in.Title, in.Target, in.Conversation)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.opened++
		m.mu.Unlock()
		return &client.SessionOpened{SessionID: id}, nil
	}, zip.WithOperationID(client.AgentsSessionOpen))
	agent.Post("/agents/session/event", func(ctx context.Context, in *client.SessionEventIn) (*client.CodingAck, error) {
		org, err := callerOrg(ctx, in.Org)
		if err != nil {
			return nil, err
		}
		return &client.CodingAck{OK: true}, agents.LogSessionEvent(ctx, org, in.SessionID, in.Kind, in.Actor, in.Payload)
	}, zip.WithOperationID(client.AgentsSessionEvent))
	agent.Post("/agents/session/close", func(ctx context.Context, in *client.SessionCloseIn) (*client.CodingAck, error) {
		org, err := callerOrg(ctx, in.Org)
		if err != nil {
			return nil, err
		}
		return &client.CodingAck{OK: true}, agents.CloseSession(ctx, org, in.SessionID, in.Status)
	}, zip.WithOperationID(client.AgentsSessionClose))
	agent.Post("/agents/session/tag", func(ctx context.Context, in *client.SessionTagIn) (*client.CodingAck, error) {
		org, err := callerOrg(ctx, in.Org)
		if err != nil {
			return nil, err
		}
		return &client.CodingAck{OK: true}, agents.TagSession(ctx, org, in.SessionID, agents.Session{
			Kind: in.Kind, Repo: in.Repo, Base: in.Base, Branch: in.Branch, Environment: in.Environment, Mode: in.Mode,
		})
	}, zip.WithOperationID(client.AgentsSessionTag))
	agent.Post("/agents/target-gate", func(ctx context.Context, in *client.TargetGateIn) (*client.CodingAck, error) {
		m.mu.Lock()
		m.gated = append(m.gated, zip.CallerOf(ctx))
		m.mu.Unlock()
		org, err := callerOrg(ctx, in.Org)
		if err != nil {
			return nil, err
		}
		if err := agents.TargetDispatchable(ctx, org, in.TargetID, in.Dir); err != nil {
			return nil, err
		}
		// The production plane gate asks the capability too (plugin/agent).
		if in.ReadOnly {
			if err := agents.TargetReadOnly(ctx, org, in.TargetID); err != nil {
				return nil, err
			}
		}
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsTargetGate))
	agent.Post("/agents/route-run", func(ctx context.Context, in *client.RouteRunIn) (*client.CodingAck, error) {
		m.mu.Lock()
		m.queued = append(m.queued, zip.CallerOf(ctx))
		m.mu.Unlock()
		org, err := callerOrg(ctx, in.Org)
		if err != nil {
			return nil, err
		}
		// The queue's own check (plugin/agent planeRouteRun).
		if err := agents.Reaches(ctx, org, in.TargetID); err != nil {
			return nil, err
		}
		if err := agents.Admit(ctx, org, in.TargetID, in.Dir, in.SessionID); err != nil {
			return nil, err
		}
		m.routed <- *in
		return &client.CodingAck{OK: true}, nil
	}, zip.WithOperationID(client.AgentsRouteRun))

	prov := zip.New(zip.Config{AppName: providerpeer.App, DisableStartupMessage: true})
	prov.Post("/integrations/github/run", func(ctx context.Context, in *client.GitHubRunIn) (*client.GitHubRun, error) {
		return &client.GitHubRun{Token: ghToken, Owner: "hanzo-apps", Repo: "site", Base: "main", Login: zip.CallerOf(ctx).User}, nil
	}, zip.WithOperationID(client.IntegrationsGitHubRun))

	// A customer's org has no namespace on the estate's forge; its workspace is
	// asked of the project app, which answers that it has none yet.
	project := zip.New(zip.Config{AppName: projectpeer.App, DisableStartupMessage: true})
	project.Post("/projects/workspace", func(context.Context, *client.WorkspaceIn) (*client.Workspace, error) {
		return &client.Workspace{}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))

	for name, app := range map[string]*zip.App{"agent": agent, providerpeer.App: prov, projectpeer.App: project} {
		plane.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
	return m
}

// as is one request to the agents HTTP surface, as a person of org.
func (m *machines) as(t *testing.T, method, path, org, user, key, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user)
	if key != "" {
		req.Header.Set("X-Target-Key", key)
	}
	resp, err := m.http.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// link registers a machine as its owner and starts its runner: a key minted, and
// one claim polled — which is what the gate reads as a runner listening.
func (m *machines) link(t *testing.T, org, owner, host string) string {
	t.Helper()
	code, body := m.as(t, http.MethodPost, "/v1/agent/targets", org, owner, "", `{"label":"`+host+`","host":"`+host+`"}`)
	var tv struct {
		ID       string `json:"id"`
		ClaimKey string `json:"claimKey"`
	}
	if code != http.StatusCreated || json.Unmarshal(body, &tv) != nil {
		t.Fatalf("link: %d %s", code, body)
	}
	if code, body = m.as(t, http.MethodPost, "/v1/agent/targets/"+tv.ID+"/key", org, owner, "", ""); code != http.StatusOK || json.Unmarshal(body, &tv) != nil {
		t.Fatalf("key: %d %s", code, body)
	}
	// A run already waiting answers the poll at once, so the runner's first claim
	// is a short one.
	o := agents.OfferRoutedRun(agents.RoutedRun{Org: org, TargetID: tv.ID, SessionID: "sess_warm"})
	defer o.Close()
	if code, body = m.as(t, http.MethodPost, "/v1/agent/targets/"+tv.ID+"/claim", org, owner, tv.ClaimKey, ""); code != http.StatusOK {
		t.Fatalf("claim: %d %s", code, body)
	}
	return tv.ID
}

// A RUN ON A HELD ORG'S MACHINE IS ITS OWNER'S, ALL THE WAY TO DISPATCH. Through
// Start, as each caller would arrive: the owner's run and a SuperAdmin's are
// dispatched to the machine, and the gate at dispatch and the queue, which
// checks again, were asked as them; a
// stranger and the held org's own admin are refused before a session is opened,
// as an unknown machine is. In a customer's org a member's run reaches a
// colleague's machine, as it always has.
func TestARunReachesAHeldOrgsMachineOnlyAsItsOwner(t *testing.T) {
	m := serveMachines(t)
	spark := m.link(t, "hanzo", "sub-ann", "spark")
	box := m.link(t, "acme", "sub-ann", "box")

	for _, c := range []struct {
		name     string
		org      string
		target   string
		caller   zip.Caller
		dispatch bool
	}{
		{"the owner", "hanzo", spark, zip.Caller{Org: "hanzo", User: "sub-ann"}, true},
		{"a stranger", "hanzo", spark, zip.Caller{Org: "hanzo", User: "sub-mal"}, false},
		{"an admin of the held org", "hanzo", spark, zip.Caller{Org: "hanzo", User: "sub-staff", OrgAdmin: true}, false},
		{"a SuperAdmin", "hanzo", spark, zip.Caller{Org: "hanzo", User: "sub-root", Admin: true}, true},
		{"a machine nobody linked", "hanzo", "tgt_0123456789abcdef0123456789abcdef", zip.Caller{Org: "hanzo", User: "sub-ann"}, false},
		{"a member of a customer's org", "acme", box, zip.Caller{Org: "acme", User: "sub-bob"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			engineOnce = sync.Once{}
			t.Cleanup(func() { engineOnce = sync.Once{} })
			m.mu.Lock()
			m.opened, m.gated, m.queued = 0, nil, nil
			m.mu.Unlock()

			acc, err := Start(zip.WithCaller(context.Background(), c.caller), c.org, c.caller.User,
				client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", TargetID: c.target},
				func(string, ...any) {})
			if !c.dispatch {
				if err == nil || !strings.Contains(err.Error(), "not available") {
					t.Fatalf("start = %+v, %v; want the machine refused as not available", acc, err)
				}
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.opened != 0 {
					t.Fatalf("a refused run opened %d sessions", m.opened)
				}
				return
			}
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			select {
			case run := <-m.routed:
				if run.TargetID != c.target || run.SessionID != acc.SessionID || run.Org != c.org {
					t.Fatalf("dispatched %+v, want %s on %s", run, acc.SessionID, c.target)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("the run was never dispatched to its machine")
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(m.gated) != 2 {
				t.Fatalf("the gate was asked %d times, want at start and at dispatch", len(m.gated))
			}
			for _, g := range append(m.gated, m.queued...) {
				if g.User != c.caller.User || g.Admin != c.caller.Admin {
					t.Fatalf("the gate or the queue was asked as %+v, want the caller %+v", g, c.caller)
				}
			}
			if len(m.queued) != 1 {
				t.Fatalf("the queue was asked %d times, want once", len(m.queued))
			}
		})
	}
}

// A RUN ON YOUR OWN MACHINE NEEDS NO REPOSITORY (#779). An automation that
// targets a machine starts its Dev run with a prompt, the machine, its project
// and nothing about a repository (apps/auto agentTask), and every such run was
// refused "needs the repo it works in" before it reached the machine. It is
// dispatched with no repository, no address and no branch, and the machine's
// runner works in an empty directory. A run that asks for what only a
// repository gives is still refused, saying what it asked for, before a session
// is opened or anything is queued.
//
// MUTATION: refuse every machine run with no repo again, or ask the dispatcher
// for an actor and an address it cannot have, and the automation's run fails.
func TestARunOnYourOwnMachineNeedsNoRepo(t *testing.T) {
	m := serveMachines(t)
	box := m.link(t, "acme", "sub-ann", "box")
	ann := zip.Caller{Org: "acme", User: "sub-ann"}
	t.Cleanup(func() { engineOnce = sync.Once{} })
	start := func(in client.CodingStartIn) (Accepted, error) {
		engineOnce = sync.Once{}
		m.mu.Lock()
		m.opened, m.gated, m.queued = 0, nil, nil
		m.mu.Unlock()
		in.TargetID = box
		return Start(zip.WithCaller(context.Background(), ann), "acme", ann.User, in, func(string, ...any) {})
	}

	t.Run("an automation's run", func(t *testing.T) {
		acc, err := start(client.CodingStartIn{
			Prompt:  "Automation: cd-watchdog\n\nCheck that the site answers and say what you found.",
			Project: "carbon-district", Desktop: true,
		})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if !acc.Routed || acc.TargetID != box || acc.Repo != "" || acc.Branch != "" {
			t.Fatalf("accepted %+v, want a run routed to %s on no repository and no branch", acc, box)
		}
		select {
		case run := <-m.routed:
			if run.TargetID != box || run.SessionID != acc.SessionID || run.Org != "acme" {
				t.Fatalf("dispatched %+v, want %s on %s", run, acc.SessionID, box)
			}
			if run.Repo != "" || run.CloneURL != "" || run.Branch != "" {
				t.Fatalf("a run on no repository was handed repo %q, address %q, branch %q", run.Repo, run.CloneURL, run.Branch)
			}
			if run.Project != "carbon-district" || !strings.Contains(run.Prompt, "cd-watchdog") {
				t.Fatalf("the machine was not handed the automation's task: %+v", run)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the run was never dispatched to its machine")
		}
	})

	for _, c := range []struct {
		name string
		in   client.CodingStartIn
		what string
	}{
		{"a pull request", client.CodingStartIn{Pull: true}, "`pull`"},
		{"a branch", client.CodingStartIn{Branch: "fix-header"}, "`branch`"},
		{"a base", client.CodingStartIn{Base: "main"}, "`base`"},
		{"an issue", client.CodingStartIn{Issue: "7"}, "`issue`"},
	} {
		t.Run("asking for "+c.name, func(t *testing.T) {
			c.in.Prompt = "fix the header"
			acc, err := start(c.in)
			if err == nil || !strings.Contains(err.Error(), "needs the repo it works in") || !strings.Contains(err.Error(), c.what) {
				t.Fatalf("start = %+v, %v; want it refused as needing a repo for %s", acc, err, c.what)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.opened != 0 || len(m.queued) != 0 {
				t.Fatalf("a refused run opened %d sessions and was queued %d times", m.opened, len(m.queued))
			}
		})
	}
}

// A RUN IN A FOLDER OF A MACHINE RUNS ONLY INSIDE A FOLDER ITS OWNER GRANTED
// (hanzo-inc/app#310). Inside one, it is dispatched carrying the folder; outside
// every grant it is refused 403 folder_not_granted before a session is opened; a
// folder needs a machine and is not a repository too.
func TestARunInAFolderNeedsAGrant(t *testing.T) {
	m := serveMachines(t)
	box := m.link(t, "acme", "sub-ann", "box")
	if code, body := m.as(t, http.MethodPost, "/v1/agent/targets/"+box+"/folders", "acme", "sub-ann", "", `{"folder":"/home/ann/site"}`); code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, body)
	}
	ann := zip.Caller{Org: "acme", User: "sub-ann"}
	t.Cleanup(func() { engineOnce = sync.Once{} })
	start := func(in client.CodingStartIn) (Accepted, error) {
		engineOnce = sync.Once{}
		m.mu.Lock()
		m.opened, m.gated, m.queued = 0, nil, nil
		m.mu.Unlock()
		in.Prompt = "fix the header"
		return Start(zip.WithCaller(context.Background(), ann), "acme", ann.User, in, func(string, ...any) {})
	}

	acc, err := start(client.CodingStartIn{TargetID: box, Dir: "/home/ann/site/web"})
	if err != nil {
		t.Fatalf("start in a granted folder: %v", err)
	}
	select {
	case run := <-m.routed:
		if run.Dir != "/home/ann/site/web" || run.SessionID != acc.SessionID || run.TargetID != box {
			t.Fatalf("dispatched %+v, want %s in /home/ann/site/web", run, acc.SessionID)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run was never dispatched to its machine")
	}

	for _, dir := range []string{"/home/ann/other", "/home/ann/site/../other", "/home/ann/sitex"} {
		_, err := start(client.CodingStartIn{TargetID: box, Dir: dir})
		var refused *zip.HTTPError
		if !errors.As(err, &refused) || refused.Status != http.StatusForbidden || refused.Code != agents.FolderNotGranted {
			t.Fatalf("start in %s: %v; want 403 %s", dir, err, agents.FolderNotGranted)
		}
		m.mu.Lock()
		opened := m.opened
		m.mu.Unlock()
		if opened != 0 {
			t.Fatalf("a refused run in %s opened a session", dir)
		}
	}

	for name, in := range map[string]client.CodingStartIn{
		"no machine":   {Dir: "/home/ann/site"},
		"a repository": {TargetID: box, Dir: "/home/ann/site", Repo: "github.com/hanzo-apps/site"},
	} {
		if _, err := start(in); err == nil || !strings.Contains(err.Error(), "folder of a machine") {
			t.Fatalf("a folder with %s: %v", name, err)
		}
	}
}

// A READ-ONLY RUN IS ADMITTED ONLY TO A MACHINE THAT ADVERTISES THE CAPABILITY.
// The refusal is 422 read_only_unsupported at Start (before a session opens or a
// run is queued), and an admitted run carries readOnly all the way to the machine.
// Absent capability is the existing writable behaviour.
func TestReadOnlyRun_CapabilityAtStartAndOnTheWire(t *testing.T) {
	m := serveMachines(t)
	box := m.link(t, "acme", "sub-ann", "box-ro")
	ann := zip.Caller{Org: "acme", User: "sub-ann"}
	start := func(in client.CodingStartIn) (Accepted, error) {
		engineOnce = sync.Once{}
		t.Cleanup(func() { engineOnce = sync.Once{} })
		m.mu.Lock()
		m.opened, m.gated, m.queued = 0, nil, nil
		m.mu.Unlock()
		in.Prompt = "review the checkout"
		return Start(zip.WithCaller(context.Background(), ann), "acme", ann.User, in, func(string, ...any) {})
	}
	// The linked machine has no readOnlyRuns in its spec yet.
	_, err := start(client.CodingStartIn{TargetID: box, ReadOnly: true})
	var refused *zip.HTTPError
	if !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity || refused.Code != agents.ReadOnlyUnsupported {
		t.Fatalf("start on a machine without the capability: %v; want 422 %s", err, agents.ReadOnlyUnsupported)
	}
	m.mu.Lock()
	opened, queued := m.opened, len(m.queued)
	m.mu.Unlock()
	if opened != 0 || queued != 0 {
		t.Fatalf("a refused read-only run opened %d sessions and queued %d runs", opened, queued)
	}

	// The harness on the machine advertises the capability; the same run is now
	// admitted and readOnly crosses the plane with it.
	code, body := m.as(t, http.MethodPatch, "/v1/agent/targets/"+box, "acme", "sub-ann", "",
		`{"spec":{"os":"darwin","arch":"arm64","readOnlyRuns":true}}`)
	if code != http.StatusOK {
		t.Fatalf("advertise readOnlyRuns: %d %s", code, body)
	}
	acc, err := start(client.CodingStartIn{TargetID: box, ReadOnly: true})
	if err != nil {
		t.Fatalf("start on a capable machine: %v", err)
	}
	select {
	case run := <-m.routed:
		if !run.ReadOnly || run.SessionID != acc.SessionID {
			t.Fatalf("the routed run did not carry readOnly alone: %+v", run)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the read-only run was never dispatched")
	}

	// A sandbox has a read-only credential already; readOnly there points at the
	// real answer, mode: plan. A pull request needs a push, which readOnly forbids.
	for name, in := range map[string]client.CodingStartIn{
		"no target":      {ReadOnly: true},
		"a pull request": {TargetID: box, ReadOnly: true, Pull: true},
		"plan mode":      {TargetID: box, ReadOnly: true, Mode: "plan"},
	} {
		_, err := start(in)
		if err == nil {
			t.Fatalf("%s: a read-only run was admitted", name)
		}
		m.mu.Lock()
		opened := m.opened
		m.mu.Unlock()
		if opened != 0 {
			t.Fatalf("%s opened a session", name)
		}
	}
}

// crashing is a session registry that panics opening a session — what any step
// between a run's admission and its start could do.
type crashing struct{ Sessions }

func (crashing) OpenOn(context.Context, string, string, string, string, string, string) (string, error) {
	panic("the session registry crashed")
}

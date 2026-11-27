package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deploy_github_test.go is POST /v1/platform/apps taking a GitHub repository:
// every member may deploy, an org admin lands on universe main and anyone else on
// a review branch, and a GitHub repository is one the caller may see or it is
// 404. The universe is a real bare repository behind git http-backend, the build
// lands as a Job in a fake cluster, and GitHub's answer comes from a provider
// peer on a real unix socket — so the call the handler makes is the production
// client over the production transport, and what the peer RECEIVED is asserted.

// githubPeer is the provider app as a build reaches it: the repositories each
// caller may see, which refs a repository publishes from, and a record of who
// asked what.
type githubPeer struct {
	mu sync.Mutex
	// visible is what each (user, admin) may see, keyed "owner/name" lower-case.
	visible func(who zip.Caller, owner, repo string) (*client.GitHubRepo, bool)
	asked   []zip.Caller
	// trusted names why a ref is one owner/repo publishes from, "" when it is not.
	trusted func(owner, repo, ref string) string
	refs    []client.GitHubRefIn
	// run answers integrations_github_run, the mint a private repository's build
	// asks for (checkout.go); nil refuses every mint. mints records each ask.
	run   func(who zip.Caller, in client.GitHubRunIn) (*client.GitHubRun, error)
	mints []mint
}

// mint is one integrations_github_run the peer was asked: by whom, for what.
type mint struct {
	who zip.Caller
	in  client.GitHubRunIn
}

// minted is every mint the peer was asked for.
func (p *githubPeer) minted() []mint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]mint(nil), p.mints...)
}

// refsAsked is every ref the peer was asked to judge.
func (p *githubPeer) refsAsked() []client.GitHubRefIn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]client.GitHubRefIn(nil), p.refs...)
}

func (p *githubPeer) callers() []zip.Caller {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]zip.Caller(nil), p.asked...)
}

// serveGitHubPeer stands the provider's integrations_github_repo and
// integrations_github_ref ops up on its socket, the way the production plugin
// serves them.
func serveGitHubPeer(t *testing.T, p *githubPeer) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Bind()
	peer := zip.New(zip.Config{AppName: "provider", DisableStartupMessage: true})
	peer.Post("/integrations/github/repo",
		func(ctx context.Context, in *client.GitHubRepoIn) (*client.GitHubRepo, error) {
			who := zip.CallerOf(ctx)
			p.mu.Lock()
			p.asked = append(p.asked, who)
			p.mu.Unlock()
			if p.visible != nil {
				if r, ok := p.visible(who, in.Owner, in.Repo); ok {
					return r, nil
				}
			}
			return nil, zip.ErrNotFound("github repo: not found")
		}, zip.WithOperationID(client.IntegrationsGitHubRepo))
	peer.Post("/integrations/github/ref",
		func(ctx context.Context, in *client.GitHubRefIn) (*client.GitHubRef, error) {
			if zip.CallerOf(ctx).Org == "" {
				return nil, zip.ErrForbidden("github ref: no org on the call")
			}
			p.mu.Lock()
			p.refs = append(p.refs, *in)
			p.mu.Unlock()
			kind := ""
			if p.trusted != nil {
				kind = p.trusted(in.Owner, in.Repo, in.Ref)
			}
			return &client.GitHubRef{Trusted: kind != "", Kind: kind}, nil
		}, zip.WithOperationID(client.IntegrationsGitHubRef))
	peer.Post("/integrations/github/run",
		func(ctx context.Context, in *client.GitHubRunIn) (*client.GitHubRun, error) {
			who := zip.CallerOf(ctx)
			p.mu.Lock()
			p.mints = append(p.mints, mint{who: who, in: *in})
			run := p.run
			p.mu.Unlock()
			if run == nil {
				return nil, zip.ErrForbidden("github run: not admitted")
			}
			return run(who, *in)
		}, zip.WithOperationID(client.IntegrationsGitHubRun))
	go func() { _ = peer.Listen(zip.SocketPath("provider")) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if up, err := plane.Listening(zip.SocketPath("provider")); err == nil && up {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the provider peer never came up")
}

type deployHarness struct {
	app  *zip.App
	k    *k8sClient
	s    *cloud.Service[state]
	bare string
	peer *githubPeer
}

// newDeployHarness is the delivery surface over a real universe, a cluster that
// takes Jobs, and a provider peer where alice (a member) sees acme-gh/web and an
// org admin sees acme-gh/web and acme-gh/ops.
func newDeployHarness(t *testing.T) *deployHarness {
	t.Helper()
	inventory.invalidate()
	t.Cleanup(inventory.invalidate)
	remote, bare := universeRemoteWith(t, map[string]string{
		"charts/app/values/hanzo/cloud.yaml":           "partOf: cloud\nimage:\n  repository: ghcr.io/hanzoai/cloud\n  tag: v1.0.0\n",
		"infra/k8s/hanzo-cd/applicationset-fleet.yaml": fleetSetFixture,
	})
	t.Cleanup(swapUniverseRemote(remote))
	reg := fakeRegistry(t, map[string]bool{"v1.0.0": true})
	t.Cleanup(swapRegistryBase(reg.URL))

	peer := &githubPeer{visible: func(who zip.Caller, owner, repo string) (*client.GitHubRepo, bool) {
		full := strings.ToLower(owner + "/" + repo)
		switch {
		case full == "acme-gh/web" && who.Org == "acme":
			return &client.GitHubRepo{Owner: "acme-gh", Repo: "web", Base: "trunk", Installation: 7, Personal: !who.OrgAdmin}, true
		case full == "acme-gh/ops" && who.Org == "acme" && who.OrgAdmin:
			return &client.GitHubRepo{Owner: "acme-gh", Repo: "ops", Base: "main", Installation: 7}, true
		case full == "acme-gh/secret-site" && who.Org == "acme":
			return &client.GitHubRepo{Owner: "acme-gh", Repo: "secret-site", Base: "main", Private: true, Installation: 7, Personal: !who.OrgAdmin}, true
		case full == "old-owner/site" && who.Org == "acme":
			// Transferred and renamed: GitHub answers where it lives now.
			return &client.GitHubRepo{Owner: "Acme-GH", Repo: "Site2", Base: "main", Installation: 7, Personal: true}, true
		}
		return nil, false
	}}
	serveGitHubPeer(t, peer)

	store, err := openStore(t.TempDir(), testMaster, testKMS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	k := fakeK8s()
	s := &cloud.Service[state]{
		KMS: newFakeKMS(), Log: luxlog.New("test"), Brand: "hanzo",
		State: state{store: store, sitesHost: "hanzo.app", k8s: k},
	}
	fs := &cloud.Service[fleetState]{Log: luxlog.New("test"), Brand: "hanzo", State: fleetState{initErr: "no cluster (test)"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	appsRoutes(app, s, fs)
	return &deployHarness{app: app, k: k, s: s, bare: bare, peer: peer}
}

// jobs is how many build Jobs the cluster holds.
func (h *deployHarness) jobs(t *testing.T) int {
	t.Helper()
	list, err := h.k.dyn.Resource(jobsGVR).Namespace(h.k.buildNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

// post deploys as acme's org admin or as a member of acme.
func (h *deployHarness) post(t *testing.T, admin bool, body map[string]any) (int, []byte) {
	t.Helper()
	if admin {
		return doAdmin(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", body)
	}
	return do(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", body)
}

// deploy is post that must be accepted, decoded.
func (h *deployHarness) deploy(t *testing.T, admin bool, body map[string]any) declareResp {
	t.Helper()
	code, raw := h.post(t, admin, body)
	return deployed(t, code, raw)
}

func deployed(t *testing.T, code int, body []byte) declareResp {
	t.Helper()
	if code != http.StatusAccepted {
		t.Fatalf("deploy want 202, got %d %s", code, body)
	}
	var out declareResp
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return out
}

// TestAnAdminsGitHubDeployLandsOnMain is the one-call deploy: an org admin names
// a GitHub repository, the build launches through the shared build entry, and the
// declaration naming THAT build's tag is committed to universe main.
func TestAnAdminsGitHubDeployLandsOnMain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		out := h.deploy(t, true, map[string]any{"repo": "acme-gh/web"})

		d := out.Declaration
		if d.Mode != modeCommit || d.Ref != universeBranch || !d.Live || d.Review != "" || d.Commit == "" {
			t.Fatalf("an admin's deploy must commit to main: %+v", d)
		}
		if out.Build == nil || out.Build.Repo != "https://github.com/acme-gh/web.git" || out.Build.Ref != "trunk" {
			t.Fatalf("the build must be the GitHub repository at its default branch: %+v", out.Build)
		}
		file := showFile(t, h.bare, declarePath("acme", "web"))
		if !strings.Contains(file, "tag: "+out.Build.ID) && !strings.Contains(file, "tag: '"+out.Build.ID+"'") {
			t.Fatalf("main must name the tag this call's build pushes (%s):\n%s", out.Build.ID, file)
		}
		if !strings.Contains(file, "https://github.com/acme-gh/web") {
			t.Fatalf("the declaration must record where it came from:\n%s", file)
		}
		if n := h.jobs(t); n != 1 {
			t.Fatalf("one build Job, got %d", n)
		}
		if b, err := h.s.State.store.GetBuild(context.Background(), "acme", out.Build.ID); err != nil || b.JobName != out.Build.Job {
			t.Fatalf("the build must be recorded like every /v1/build build: %+v %v", b, err)
		}
		// The peer was asked AS the caller: their org, their person, their admin bit.
		if got := h.peer.callers(); len(got) != 1 || got[0].Org != "acme" || got[0].User != "u-acme" || !got[0].OrgAdmin {
			t.Fatalf("the lookup must carry the caller: %+v", got)
		}
	})
}

// TestAMembersDeployOpensAReview is everyone else: the same deploy lands on a
// review branch, the answer carries the link, and main is untouched.
func TestAMembersDeployOpensAReview(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		out := h.deploy(t, false, map[string]any{
			"repo": "https://github.com/acme-gh/web.git", "ref": "feat/login",
		})
		d := out.Declaration
		if d.Mode != modeBranch || d.Live || !strings.HasPrefix(d.Ref, "deploy/acme/web/") || d.Review == "" {
			t.Fatalf("a member's deploy must open a review: %+v", d)
		}
		if !strings.Contains(d.Review, "/compare/"+universeBranch+"..."+d.Ref) {
			t.Fatalf("the review link must open the pull request for the branch: %q", d.Review)
		}
		if out.Build == nil || out.Build.Ref != "feat/login" {
			t.Fatalf("a named ref is the one built: %+v", out.Build)
		}
		if strings.Contains(mustGit(t, h.bare, "ls-tree", "-r", "--name-only", universeBranch), declarePath("acme", "web")) {
			t.Fatal("a member's deploy reached main")
		}
		if got := h.peer.callers(); len(got) != 1 || got[0].OrgAdmin || got[0].User != "u-acme" {
			t.Fatalf("the lookup must carry the member as a member: %+v", got)
		}
	})
}

// TestAMemberCannotCommitToMain: asking is refused, not downgraded, and nothing
// is built or written for it.
func TestAMemberCannotCommitToMain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		code, body := do(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
			"repo": "acme-gh/web", "mode": "commit",
		})
		if code != http.StatusForbidden || !strings.Contains(string(body), "org admin") {
			t.Fatalf("a member naming commit want 403, got %d %s", code, body)
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a refused deploy launched %d build(s)", n)
		}
		// An admin may still ask for a review.
		out := h.deploy(t, true, map[string]any{
			"repo": "acme-gh/web", "mode": "branch",
		})
		if out.Declaration.Mode != modeBranch || out.Declaration.Live {
			t.Fatalf("an admin asking for a branch gets one: %+v", out.Declaration)
		}
	})
}

// TestAGitHubRepoOutsideTheCallersViewIsNotFound: a repository the caller cannot
// see answers exactly like one that does not exist, and nothing is built.
func TestAGitHubRepoOutsideTheCallersViewIsNotFound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		for _, tc := range []struct {
			name  string
			admin bool
			repo  string
		}{
			{"a member naming the admin-only repository", false, "acme-gh/ops"},
			{"a member naming nobody's repository", false, "globex/secret"},
			{"an admin naming another org's repository", true, "globex/secret"},
		} {
			send := do
			if tc.admin {
				send = doAdmin
			}
			code, body := send(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{"repo": tc.repo})
			if code != http.StatusNotFound {
				t.Errorf("%s: want 404, got %d %s", tc.name, code, body)
			}
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a repository nobody may see launched %d build(s)", n)
		}
		// The admin does see the org's installation.
		h.deploy(t, true, map[string]any{"repo": "acme-gh/ops", "mode": "branch"})
	})
}

// TestAGitHubRepoIsBuiltWhereItLivesNow: a transferred repository is built at
// the name GitHub gives it, and the app takes that name.
func TestAGitHubRepoIsBuiltWhereItLivesNow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		out := h.deploy(t, false, map[string]any{"repo": "old-owner/site"})
		if out.Build.Repo != "https://github.com/Acme-GH/Site2.git" || out.App.Name != "site2" {
			t.Fatalf("want the current home and its name: build=%+v app=%s", out.Build, out.App.Name)
		}
		// A malformed GitHub address is refused, never cloned as some other URL.
		if code, _ := do(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{"repo": "https://github.com/acme-gh"}); code != http.StatusBadRequest {
			t.Fatalf("a GitHub address naming no repository want 400, got %d", code)
		}
	})
}

// TestAnyOtherSourceIsAnAdmins: a member building an arbitrary URL would read it
// through the build's own credentials, so only an admin may.
func TestAnyOtherSourceIsAnAdmins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		code, body := do(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{"repo": "https://gitlab.com/acme/web"})
		if code != http.StatusForbidden {
			t.Fatalf("a member building a non-GitHub source want 403, got %d %s", code, body)
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a refused source launched %d build(s)", n)
		}
		out := h.deploy(t, true, map[string]any{
			"repo": "https://gitlab.com/acme/web", "mode": "branch",
		})
		if out.Build == nil || out.Build.Repo != "https://gitlab.com/acme/web" || out.Build.Ref != "" && out.Build.Ref != "main" {
			t.Fatalf("an admin's other source keeps today's behavior: %+v", out.Build)
		}
		if got := h.peer.callers(); len(got) != 0 {
			t.Fatalf("a non-GitHub source must not ask GitHub: %+v", got)
		}
	})
}

// TestAReleaseToMainIsStillProvenPullable: skipping the pull proof is for the
// build this call launched and nothing else — an admin committing an existing tag
// the registry cannot serve is refused and main is untouched.
func TestAReleaseToMainIsStillProvenPullable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		code, body := doAdmin(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", map[string]any{
			"repo": "acme-gh/web", "tag": "v9.9.9",
		})
		if code < 400 || !strings.Contains(string(body), "pullable") {
			t.Fatalf("committing an image the registry cannot serve must be refused, got %d %s", code, body)
		}
		if strings.Contains(mustGit(t, h.bare, "ls-tree", "-r", "--name-only", universeBranch), declarePath("acme", "web")) {
			t.Fatal("a refused release reached main")
		}
	})
}

// TestAnAnonymousDeployIsRefusedBeforeTheBody: the gate is Member, and it still
// answers before a malformed body is read.
func TestAnAnonymousDeployIsRefusedBeforeTheBody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		code, _ := doAs(t, h.app, http.MethodPost, "/v1/platform/apps", "acme", "", map[string]any{"repo": 7})
		if code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Fatalf("an unvalidated caller must be refused at the gate, got %d", code)
		}
		if got := h.peer.callers(); len(got) != 0 {
			t.Fatal("an unvalidated caller reached the GitHub lookup")
		}
	})
}

// TestAMembersDeployIsABuildOfWhatTheyCanSee pins the two ways a member's deploy
// could reach wider than the repository they were checked against. Releasing an
// existing image names no source at all and would open a review branch with no
// build — and so no build ceiling — behind it. And the repositories that build
// with the platform's standing GitHub read credential belong to orgs whose
// directories are the platform's own, which no member may declare into. Neither
// leaves a build behind.
func TestAMembersDeployIsABuildOfWhatTheyCanSee(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		if code, body := h.post(t, false, map[string]any{"name": "web", "tag": "v1.0.0", "build": false}); code != http.StatusForbidden {
			t.Fatalf("a member releasing an existing image want 403, got %d %s", code, body)
		}
		// A review must not take effect before it is merged, and a sealed value
		// would: it writes the running app's own secret now.
		if code, body := h.post(t, false, map[string]any{"repo": "acme-gh/web",
			"env": []map[string]any{{"name": "DATABASE_URL", "value": "postgres://attacker"}}}); code != http.StatusForbidden {
			t.Fatalf("a member sealing a secret want 403, got %d %s", code, body)
		}
		if got, err := h.s.KMS.GetSecret(context.Background(), kmsSecretRef("acme", "web", "DATABASE_URL")); err == nil && len(got) > 0 {
			t.Fatal("a refused member deploy must seal nothing")
		}
		h.peer.visible = func(who zip.Caller, owner, repo string) (*client.GitHubRepo, bool) {
			if strings.EqualFold(owner+"/"+repo, "luxfi/node") && who.Org == "lux" {
				return &client.GitHubRepo{Owner: "luxfi", Repo: "node", Base: "main", Installation: 9, Personal: true}, true
			}
			return nil, false
		}
		for org := range githubReadRepos {
			for _, admin := range []bool{false, true} {
				call := do
				if admin {
					call = doAdmin
				}
				if code, body := call(t, h.app, http.MethodPost, "/v1/platform/apps", org, map[string]any{"repo": githubReadRepos[org][0]}); code != http.StatusForbidden {
					t.Fatalf("%s (admin=%v) building a repository that reads with the standing credential want 403, got %d %s", org, admin, code, body)
				}
			}
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a refused deploy must launch nothing, got %d Jobs", n)
		}
	})
}

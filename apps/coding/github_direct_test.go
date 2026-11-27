package coding

// github_direct_test.go pins a run on a repository GitHub alone holds: it is
// admitted by GitHub as the caller — never by the forge, which holds no copy to
// hold an ACL — runs, pushes and proposes there, tells its session what it
// runs, and gives its token back; and it is refused, with nothing minted, when
// the caller's GitHub does not reach it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// pushes is an admission that admits exactly the logins it names, as their own
// GitHub, and the people IAM calls org admins at the moment of the call, on the
// org's installation — the provider's rule. It never reads the admin bit the
// caller carries, because the provider does not.
func pushes(logins map[string]string, admins *roster) func(c zip.Caller, owner, repo string) (*client.GitHubRun, error) {
	return func(c zip.Caller, owner, repo string) (*client.GitHubRun, error) {
		if owner+"/"+repo != "hanzo-apps/site" {
			return nil, zip.ErrForbidden("github run: " + owner + "/" + repo + " is in no installation this org holds")
		}
		if login, ok := logins[c.User]; ok {
			return &client.GitHubRun{Token: ghToken, Owner: "hanzo-apps", Repo: "site", Base: "trunk", Login: login}, nil
		}
		if admins.is(c.Org, c.User) {
			return &client.GitHubRun{Token: ghToken, Owner: "hanzo-apps", Repo: "site", Base: "trunk"}, nil
		}
		return nil, zip.ErrForbidden("github run: connect your GitHub account to run on hanzo-apps/site, or ask an org admin")
	}
}

// roster is who administers which org right now, as IAM would answer. nil is nobody.
type roster struct {
	mu sync.Mutex
	of map[string]bool
}

func admins(keys ...string) *roster {
	a := &roster{of: map[string]bool{}}
	for _, k := range keys {
		a.of[k] = true
	}
	return a
}

func (a *roster) is(org, user string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.of[org+"/"+user]
}

func (a *roster) demote(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.of, key)
}

// startAs runs Start as a caller to its end and answers what it answered.
func (r *rig) startAs(t *testing.T, ctx context.Context, subject string, in client.CodingStartIn) (Accepted, error) {
	t.Helper()
	engineOnce = sync.Once{}
	t.Cleanup(func() { engineOnce = sync.Once{} })
	acc, err := Start(ctx, "hanzo", subject, in, func(string, ...any) {})
	if err != nil {
		return acc, err
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
	return acc, nil
}

// A REPOSITORY ON GITHUB ALONE is admitted on the caller's own GitHub, runs
// there end to end, never asks the forge anything, and says what it runs.
func TestStart_AGitHubRepoIsAdmittedOnTheCallersGitHub(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	tip := theSHA
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed\n"}
		}
		return client.Ran{}
	}
	r.tip = newSHA
	r.serve(t)

	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Pull: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if acc.Repo != "hanzo-apps/site" {
		t.Fatalf("the run answered repo %q, want where GitHub says it lives", acc.Repo)
	}
	if !asked(r.admitted, "hanzo", "sub-zoe") {
		t.Fatalf("GitHub admission was asked as %+v, want twice as hanzo/sub-zoe: to push, and to read", r.admitted)
	}
	if r.forgeHits != 0 || r.keys != 0 || len(r.minted) != 0 {
		t.Fatalf("the forge was asked (%d requests, %d keys, %d grants) for a repository it does not hold",
			r.forgeHits, r.keys, len(r.minted))
	}
	if cmd, ok := r.pod.did("https://github.com/hanzo-apps/site.git"); !ok || !strings.Contains(cmd, "trunk") {
		t.Fatalf("the run did not clone the origin at its default branch: %q", cmd)
	}
	if len(r.pulls) != 1 || r.pulls[0]["base"] != "trunk" || r.pulls[0]["head"] != "agent/abc123def456" {
		t.Fatalf("GitHub was not asked to propose the run's branch onto its default: %v", r.pulls)
	}
	if body := r.pulls[0]["body"]; !strings.Contains(body, "for `zoe-gh`") {
		t.Fatalf("the proposal must name the GitHub login that admitted it: %q", body)
	}
	if !slices.Equal(r.revoked, []string{"Bearer " + ghRead, "Bearer " + ghToken}) {
		t.Fatalf("the run's tokens were revoked %d times, want the read-only one and then the run's own", len(r.revoked))
	}
	want := Tag{Kind: "coding", Repo: "hanzo-apps/site", Base: "trunk", Branch: "agent/abc123def456",
		Environment: "sandbox", PR: "https://github.com/hanzo-apps/site/pull/9", Mode: "build", Sandbox: "sbx_1"}
	if got := r.sessions.tagged(); got != want {
		t.Fatalf("the session says it runs %+v, want %+v", got, want)
	}
	for _, e := range r.sessions.events {
		if strings.Contains(e.payload, ghToken) {
			t.Fatalf("the token is in a session event: %s", e.payload)
		}
	}
}

// A CALLER GITHUB DOES NOT ADMIT is refused before anything runs: no sandbox,
// no token, a session closed with the reason, and ErrNotAdmitted — which the
// endpoint answers 403. A member who connected no GitHub is refused the same way,
// and an org admin is admitted on the org's installation without one.
func TestStart_AGitHubRepoTheCallerCannotReachIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		caller  zip.Caller
		admit   func(zip.Caller, string, string) (*client.GitHubRun, error)
		ref     string
		refused bool
	}{
		{"a repository outside every installation", zip.Caller{Org: "hanzo", User: "sub-zoe"},
			pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil), "github.com/octocat/private", true},
		{"a member who connected no GitHub", zip.Caller{Org: "hanzo", User: "sub-bob"},
			pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil), "github.com/hanzo-apps/site", true},
		{"a deployment that admits nobody", zip.Caller{Org: "hanzo", User: "sub-zoe"},
			nil, "hanzo-apps/site", true},
		{"a bit the caller carries is not IAM's word", zip.Caller{Org: "hanzo", User: "sub-bob", OrgAdmin: true},
			pushes(nil, nil), "github.com/hanzo-apps/site", true},
		{"an admin on the org's installation", zip.Caller{Org: "hanzo", User: "sub-bob", OrgAdmin: true},
			pushes(nil, admins("hanzo/sub-bob")), "github.com/hanzo-apps/site", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig()
			r.admit = c.admit
			r.pod.answer = func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}
			r.serve(t)

			acc, err := r.startAs(t, zip.WithCaller(context.Background(), c.caller), c.caller.User,
				client.CodingStartIn{Repo: c.ref, Prompt: "fix the header"})
			r.mu.Lock()
			defer r.mu.Unlock()
			if !c.refused {
				if err != nil {
					t.Fatalf("an admin on the org's installation was refused: %v", err)
				}
				if !asked(r.admitted, "hanzo", "sub-bob") {
					t.Fatalf("the admission was not asked as the caller: %+v", r.admitted)
				}
				if got := r.sessions.tagged(); got.Repo != "hanzo-apps/site" || got.Environment != "sandbox" {
					t.Fatalf("the session was not told what it runs: %+v", got)
				}
				_ = acc
				return
			}
			if !errors.Is(err, ErrNotAdmitted) {
				t.Fatalf("want ErrNotAdmitted, got %v", err)
			}
			if r.pod.gitVerb("clone") || len(r.pulls) != 0 || len(r.revoked) != 0 {
				t.Fatal("a refused run cloned, proposed or held a token")
			}
			if r.forgeHits != 0 {
				t.Fatalf("a refused GitHub run asked the forge %d times", r.forgeHits)
			}
			if n := len(r.sessions.closes); n != 1 || r.sessions.closes[0].status != statusError {
				t.Fatalf("the refused run's session was not closed with its reason: %+v", r.sessions.closes)
			}
		})
	}
}

// A ROUTED RUN ON GITHUB is handed GitHub's address and carries its person — and
// nothing about what they may do — to the completion, which is admitted again as
// them on what IAM and GitHub answer THEN, verifies and proposes there, and gives
// its token back.
func TestRouted_AGitHubRunClonesTheOriginAndCompletesThere(t *testing.T) {
	r := newRig()
	r.admit = pushes(nil, admins("hanzo/sub-bob"))
	r.tip = newSHA
	r.serve(t)

	var routed RoutedRun
	d := Dispatcher{
		Sessions: r.sessions, PR: planePR{},
		CloneURL: func(context.Context, string, string, string) string {
			t.Fatal("the forge was asked where a GitHub repository is")
			return ""
		},
		Route:      func(_ context.Context, run RoutedRun) error { routed = run; return nil },
		TargetGate: func(context.Context, string, string, string, bool) error { return nil },
	}
	res := d.Run(context.Background(), Req{
		Org: "hanzo", UserID: "sub-bob", Repo: "hanzo-apps/site", Prompt: "fix it", TargetID: "tgt_1",
		Actor: "sub-bob", Home: "hanzo-apps/site", SessionID: "sess_abc123def456", Pull: true,
	})
	if !res.OK || routed.CloneURL != "https://github.com/hanzo-apps/site.git" {
		t.Fatalf("the machine was not handed GitHub's address: ok=%v %q (%s)", res.OK, routed.CloneURL, res.Error)
	}
	if routed.Home != "hanzo-apps/site" || routed.Actor != "sub-bob" {
		t.Fatalf("the completion was not handed who it acts for: %+v", routed)
	}

	d.finalizeRouted(context.Background(), routed, RoutedResult{OK: true, Changed: true, CommitSha: newSHA})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.admitted) != 1 || r.admitted[0].User != "sub-bob" || r.admitted[0].OrgAdmin || r.admitted[0].Org != "hanzo" {
		t.Fatalf("the completion must be asked as the run's person, carrying no admin bit: %+v", r.admitted)
	}
	if len(r.pulls) != 1 || r.pulls[0]["head"] != routed.Branch {
		t.Fatalf("the completion did not propose on GitHub: %v", r.pulls)
	}
	if !slices.Equal(r.revoked, []string{"Bearer " + ghToken}) {
		t.Fatalf("the completion's token was revoked %d times, want once", len(r.revoked))
	}
	if n := len(r.sessions.closes); n != 1 || r.sessions.closes[0].status != statusDone {
		t.Fatalf("the routed session did not close done: %+v", r.sessions.closes)
	}
}

// AN ADMIN DEMOTED WHILE THEIR RUN WAS AWAY gets nothing when it reports back:
// the completion is admitted on IAM's word at completion, not on the start's, so
// no token is minted, nothing is proposed, and the session closes with the reason.
func TestRouted_ADemotedAdminsCompletionIsMintedNothing(t *testing.T) {
	r := newRig()
	who := admins("hanzo/sub-bob")
	r.admit = pushes(nil, who)
	r.tip = newSHA
	r.serve(t)

	var routed RoutedRun
	d := Dispatcher{
		Sessions: r.sessions, PR: planePR{},
		Route:      func(_ context.Context, run RoutedRun) error { routed = run; return nil },
		TargetGate: func(context.Context, string, string, string, bool) error { return nil },
	}
	if res := d.Run(context.Background(), Req{
		Org: "hanzo", UserID: "sub-bob", Repo: "hanzo-apps/site", Prompt: "fix it", TargetID: "tgt_1",
		Actor: "sub-bob", Home: "hanzo-apps/site", SessionID: "sess_abc123def456",
	}); !res.OK {
		t.Fatalf("the routed run did not start: %s", res.Error)
	}
	who.demote("hanzo/sub-bob")

	d.finalizeRouted(context.Background(), routed, RoutedResult{OK: true, Changed: true, CommitSha: newSHA})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.admitted) != 1 || r.admitted[0].OrgAdmin {
		t.Fatalf("the completion must be asked once, carrying no admin bit: %+v", r.admitted)
	}
	if len(r.pulls) != 0 || len(r.revoked) != 0 {
		t.Fatalf("a demoted admin's completion proposed %v or held a token (%d revokes)", r.pulls, len(r.revoked))
	}
	if n := len(r.sessions.closes); n != 1 || r.sessions.closes[0].status != statusError {
		t.Fatalf("the completion's session must close with the refusal: %+v", r.sessions.closes)
	}
}

// The endpoint's refusal for a run GitHub would not admit is a 403 naming why —
// the question was well formed, and the answer is about who asked.
func TestNotAdmittedNamesTheRefusal(t *testing.T) {
	err := notAdmitted("octocat/hello", zip.ErrForbidden("github run: connect your GitHub account to run on octocat/hello, or ask an org admin"))
	if !errors.Is(err, ErrNotAdmitted) || !strings.Contains(err.Error(), "connect your GitHub account") {
		t.Fatalf("a refusal must be ErrNotAdmitted and say why: %v", err)
	}
	if err := notAdmitted("octocat/hello", zip.Errorf(http.StatusServiceUnavailable, "github run: not configured")); errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("an unreachable admission is not a refusal of the caller: %v", err)
	}
}

// A FOLLOW-UP is admitted on what IAM says when it starts, not on what was true
// when its run did: it names the person and carries no admin bit, so an org admin
// still an admin is admitted on the org's installation, though the follow-up is
// started from a context of its own with no request behind it.
func TestFollowUp_IsAdmittedOnWhatIAMSaysNow(t *testing.T) {
	r := newRig()
	r.admit = pushes(nil, admins("hanzo/sub-bob")) // admits an org admin, and nobody on their own GitHub
	r.pod.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}
	r.serve(t)
	engineOnce = sync.Once{}
	t.Cleanup(func() { engineOnce = sync.Once{} })

	f := &flight{org: "hanzo", subject: "sub-bob", session: "sess_prev", done: make(chan struct{}), ctx: context.Background(),
		in: client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header"}}
	close(f.done)
	f.follow("sub-bob", client.CodingSteerIn{Text: "and the footer"}, false, func(string, ...any) {})

	r.mu.Lock()
	admitted := append([]zip.Caller(nil), r.admitted...)
	r.mu.Unlock()
	if !asked(admitted, "hanzo", "sub-bob") || admitted[0].OrgAdmin || admitted[1].OrgAdmin {
		t.Fatalf("the follow-up must be admitted as its person, carrying no admin bit: %+v", admitted)
	}
	flights.Lock()
	next := flights.m["sess_abc123def456"]
	flights.Unlock()
	if next != nil {
		<-next.done
	}
}

// `cloud` IN HANZO IS THE PRIVATE BACKEND when it is said bare or under the
// org's own name — including the Hanzo Git address a caller copied from /v1/git
// — and runs on GitHub's hanzo-inc/cloud without asking the forge. Under the
// forge namespace's name, `hanzoai`, it is the open-source edition and is never
// mapped; that spelling is the forge's to answer (TestHomeIsThePrivateEdition).
func TestStart_CloudIsThePrivateBackend(t *testing.T) {
	for ref, want := range map[string]string{
		"cloud":       "hanzo-inc/cloud",
		"cloud.GIT":   "hanzo-inc/cloud",
		"hanzo/cloud": "hanzo-inc/cloud",
		"https://api.hanzo.ai/v1/git/hanzo/cloud": "hanzo-inc/cloud",
		"universe":                   "hanzo-inc/universe",
		"github.com/"+"hanzo-inc/cloud": "hanzo-inc/cloud",
		"github.com/hanzoai/cloud":   "hanzoai/cloud",
	} {
		t.Run(ref, func(t *testing.T) {
			r := newRig()
			var asked string
			r.admit = func(_ zip.Caller, owner, repo string) (*client.GitHubRun, error) {
				asked = owner + "/" + repo
				return &client.GitHubRun{Token: ghToken, Owner: owner, Repo: repo, Base: "main", Login: "zoe-gh"}, nil
			}
			r.pod.answer = func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}
			r.serve(t)
			acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: ref, Prompt: "fix the header"})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.forgeHits != 0 {
				t.Fatalf("the forge was asked %d times about a repository whose code is on GitHub", r.forgeHits)
			}
			if len(r.admitted) != 2 || asked != want {
				t.Fatalf("GitHub admission was asked %d times, last for %q; want twice for %s, to push and to read", len(r.admitted), asked, want)
			}
			if _, ok := r.pod.did("https://github.com/" + want + ".git"); !ok || acc.Repo != want {
				t.Fatalf("%q ran on %q, want %s:\n%s", ref, acc.Repo, want, strings.Join(r.pod.lines(), "\n"))
			}
		})
	}
}

// A FOLLOW-UP RESUMES ITS EARLIER RUN'S SANDBOX ONLY FOR THE SAME REPOSITORY.
// Named on a run of the private backend, a follow-up on the open-source edition
// gets a sandbox of its own and a fresh clone — never the earlier run's tree,
// which it would push to the repository it named.
func TestStart_AFollowUpOnAnotherRepositoryGetsItsOwnSandbox(t *testing.T) {
	for _, c := range []struct {
		repo   string
		reused bool
	}{
		{"github.com/hanzoai/cloud", false},
		{"github.com/"+"hanzo-inc/cloud", true},
		{"cloud", true},
	} {
		t.Run(c.repo, func(t *testing.T) {
			was := visibleRun
			visibleRun = func(_ context.Context, id string) (agents.Session, error) {
				return agents.Session{ID: id, Kind: kindCoding, Repo: "hanzo-inc/cloud", Box: "sbx_private"}, nil
			}
			t.Cleanup(func() { visibleRun = was })
			r := newRig()
			r.admit = func(_ zip.Caller, owner, repo string) (*client.GitHubRun, error) {
				return &client.GitHubRun{Token: ghToken, Owner: owner, Repo: repo, Base: "main", Login: "zoe-gh"}, nil
			}
			r.pod.answer = func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}
			r.serve(t)
			if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
				client.CodingStartIn{Repo: c.repo, After: "sess_earlier", Prompt: "now add tests"}); err != nil {
				t.Fatalf("start: %v", err)
			}
			r.pod.mu.Lock()
			defer r.pod.mu.Unlock()
			if len(r.pod.leased) != 1 {
				t.Fatalf("leased %d sandboxes", len(r.pod.leased))
			}
			if got := r.pod.leased[0].ID == "sbx_private"; got != c.reused {
				t.Fatalf("a follow-up on %s resumed the earlier sandbox = %v, want %v", c.repo, got, c.reused)
			}
		})
	}
}

// A FOLLOW-UP ON A RUN THAT NEVER PUSHED STARTS WHERE THAT RUN DID. The earlier
// branch lived in its sandbox alone; with that sandbox gone, a clone of it found
// nothing ("Remote branch agent/… not found in upstream origin") and the
// follow-up failed seven seconds in. A branch that is on GitHub is still gone on
// from, and with neither the branch nor its base there, the repository's default.
func TestStart_AFollowUpOnARunThatNeverPushedStartsFromItsBase(t *testing.T) {
	earlier := BranchFor("sess_earlier")
	for _, c := range []struct {
		name string
		refs map[string]string
		want string
	}{
		{"its branch is on GitHub", map[string]string{earlier: theSHA, "dev": theSHA}, earlier},
		{"only its base is", map[string]string{"dev": theSHA}, "dev"},
		{"neither is", map[string]string{}, "main"},
	} {
		t.Run(c.name, func(t *testing.T) {
			was := visibleRun
			visibleRun = func(_ context.Context, id string) (agents.Session, error) {
				return agents.Session{ID: id, Kind: kindCoding, Repo: "hanzo-apps/site", Base: "dev"}, nil
			}
			t.Cleanup(func() { visibleRun = was })
			r := newRig()
			r.refs = c.refs
			r.admit = func(_ zip.Caller, owner, repo string) (*client.GitHubRun, error) {
				return &client.GitHubRun{Token: ghToken, Owner: "hanzo-apps", Repo: "site", Base: "main", Login: "zoe-gh"}, nil
			}
			r.pod.answer = func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}
			r.serve(t)
			if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
				client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Prompt: "check this project"}); err != nil {
				t.Fatalf("start: %v", err)
			}
			clone, ok := r.pod.did(" clone ")
			if !ok {
				t.Fatalf("no clone:\n%s", strings.Join(r.pod.lines(), "\n"))
			}
			if !strings.Contains(clone, " -b "+c.want+" ") {
				t.Fatalf("the follow-up cloned %q, want it on %s", clone, c.want)
			}
		})
	}
}

// A FOLLOW-UP OR RETRY ON A CUSTOMER WHOSE GITHUB ACCOUNT MATCHES THEIR MADE
// WORKSPACE NAME resolves on GitHub when the repository is not in the forge
// workspace: the earlier run tagged `Repo: "<owner>/<name>"` without the
// `github.com/` prefix, so `forge.GitHub` treats `<owner>/<name>` as the made
// workspace first, and `Start` falls through to `onHub(<owner>, <name>)`.
func TestStart_AMadeWorkspaceFallsBackToGitHubWhenOwnerMatches(t *testing.T) {
	r := newRig()
	var asked string
	r.admit = func(_ zip.Caller, owner, repo string) (*client.GitHubRun, error) {
		asked = owner + "/" + repo
		return &client.GitHubRun{Token: ghToken, Owner: owner, Repo: repo, Base: "master", Login: "valerypetrov"}, nil
	}
	r.pod.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}
	r.serve(t)

	proj := zip.New(zip.Config{AppName: "project", DisableStartupMessage: true})
	proj.Post("/projects/workspace", func(context.Context, *client.WorkspaceIn) (*client.Workspace, error) {
		return &client.Workspace{Owner: "valerypetrov", Made: true}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))
	plane.Bind()
	go func() { _ = proj.Listen(zip.SocketPath("project")) }()
	t.Cleanup(func() { _ = proj.Shutdown() })
	waitListening(t, "project")

	emptyForge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, "/repos") {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(emptyForge.Close)
	t.Setenv("CLOUD_FORGE_HOST", emptyForge.URL)
	forge.Invalidate()

	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "valerypetrov", User: "sub-valery"})
	engineOnce = sync.Once{}
	t.Cleanup(func() { engineOnce = sync.Once{} })
	acc, err := Start(ctx, "valerypetrov", "sub-valery",
		client.CodingStartIn{Repo: "valerypetrov/ClickHouse", Prompt: "check this pr"}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("start on <owner>/<name> in made workspace was refused: %v", err)
	}
	flights.Lock()
	f := flights.m[acc.SessionID]
	flights.Unlock()
	if f != nil {
		<-f.done
	}
	if asked != "valerypetrov/ClickHouse" || acc.Repo != "valerypetrov/ClickHouse" {
		t.Fatalf("asked=%q acc.Repo=%q, want valerypetrov/ClickHouse", asked, acc.Repo)
	}
}

// asked reports that GitHub was asked twice, to push and to read, and both times as org/user.
func asked(admitted []zip.Caller, org, user string) bool {
	return len(admitted) == 2 && admitted[0] == admitted[1] && admitted[0].Org == org && admitted[0].User == user
}

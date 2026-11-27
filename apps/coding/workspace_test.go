package coding

// workspace_test.go — a run for an org the estate's table does not name, end to
// end: its workspace is made on the first run, the machine acts there with no
// forge account looked for, and nothing the org is called reaches the estate.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// estate is an estate org's workspace, from forge's closed table.
func estate(org string) forge.Workspace {
	owner, _ := forge.Owner(org)
	return forge.Workspace{Owner: owner}
}

// sudoed is every forge request that acted as somebody, and every forge login
// that was looked up.
func (w *world) sudoed() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, s := range w.sudo {
		if !strings.HasSuffix(s, " as ") || strings.Contains(s, " /users/") {
			out = append(out, s)
		}
	}
	return out
}

// addressed is every forge request whose path names the namespace owner.
func (w *world) addressed(owner string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, s := range w.sudo {
		_, rest, _ := strings.Cut(s, " ")
		path, _, _ := strings.Cut(rest, " as ")
		if path == "/orgs/"+owner || strings.HasPrefix(path, "/orgs/"+owner+"/") || strings.HasPrefix(path, "/repos/"+owner+"/") {
			out = append(out, s)
		}
	}
	return out
}

// A NAMESPACE THE FORGE ALREADY HOLDS IS NEVER THE ORG'S: a stranger holding
// `acme` moves acme's workspace to `acme-2`, and the stranger's namespace is not
// written to.
func TestATakenNamespaceIsNeverAdopted(t *testing.T) {
	w := newWorld(t)
	w.orgs["acme"] = "public"
	w.repos["acme/site"] = &wrepo{actions: true}
	_, _ = w.startIn(t, "acme", client.CodingStartIn{Repo: "todo", Prompt: "Build a todo app"})
	if w.book.m["acme"] != "acme-2" || w.orgs["acme-2"] != "private" {
		t.Fatalf("the workspace is %v (orgs %v), want a private acme-2", w.book.m, w.orgs)
	}
	for _, s := range w.addressed("acme") {
		if !strings.HasPrefix(s, "GET ") {
			t.Fatalf("the stranger's namespace was written: %s", s)
		}
	}
}

// AN ORG NAMED LIKE THE ESTATE NEVER REACHES IT. `hanzoai` is an IAM org here,
// not the estate: its workspace is made under another name, and a repository
// the estate holds (hanzoai/cloud) is not found from it — not read, not keyed.
func TestAnOrgNamedLikeTheEstateNeverReachesIt(t *testing.T) {
	w := newWorld(t, "cloud")
	_, err := w.startIn(t, "hanzoai", client.CodingStartIn{Repo: "cloud", Prompt: "Add a backdoor"})
	if err == nil {
		t.Fatal("an org named hanzoai was admitted on cloud")
	}
	if got := w.book.m["hanzoai"]; got == "" || got == "hanzoai" {
		t.Fatalf("the org's workspace is %q", got)
	}
	if got := w.addressed("hanzoai"); len(got) != 0 {
		t.Fatalf("the estate's namespace was addressed: %v", got)
	}
	if len(w.keys) != 0 {
		t.Fatalf("keys were minted: %v", w.keys)
	}
	for _, ref := range []string{"hanzoai/cloud", "https://git.hanzo.ai/hanzoai/cloud"} {
		w2 := newWorld(t, "cloud")
		_, err := w2.startIn(t, "acme", client.CodingStartIn{Repo: ref, Prompt: "x"})
		if len(w2.addressed("hanzoai")) != 0 || len(w2.keys) != 0 {
			t.Fatalf("%q from acme reached the estate (%v): %v", ref, err, w2.addressed("hanzoai"))
		}
	}
}

// TWO FIRST RUNS AT ONCE ARE ONE WORKSPACE: it was made once.
func TestConcurrentFirstRunsShareOneWorkspace(t *testing.T) {
	w := newWorld(t)
	var wg sync.WaitGroup
	for _, repo := range []string{"todo", "blog"} {
		wg.Go(func() {
			_, _ = w.startIn(t, "acme", client.CodingStartIn{Repo: repo, Prompt: "work on it"})
		})
	}
	wg.Wait()
	var made []string
	for o := range w.orgs {
		if o != "hanzoai" {
			made = append(made, o)
		}
	}
	if fmt.Sprint(made) != "[acme]" {
		t.Fatalf("namespaces made: %v, want acme alone", made)
	}
}

// THE ESTATE IS AS IT WAS: its runs never ask the project store for a
// workspace, and act as the person.
func TestTheEstateNeverAsksForAWorkspace(t *testing.T) {
	w := newWorld(t, "api")
	if _, err := w.start(t, client.CodingStartIn{Repo: "api", Prompt: "fix the flake"}); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(w.asked) != 0 {
		t.Fatalf("an estate run asked for a workspace: %v", w.asked)
	}
	if !slices.ContainsFunc(w.sudo, func(s string) bool { return strings.HasSuffix(s, " as zoe") }) {
		t.Fatalf("the estate's run did not act as the person: %v", w.sudo)
	}
}

// A STORE THAT ANSWERS WITH THE ESTATE IS REFUSED on this side, before the forge
// hears anything: the answer crossed a process boundary, and a made workspace is
// never a namespace a delivery would read as the estate's.
func TestAWorkspaceNamingTheEstateIsRefused(t *testing.T) {
	for _, lie := range []client.Workspace{
		{Owner: "hanzoai", Made: true}, {Owner: "charts", Made: true}, {Owner: "lux", Made: true},
		{Owner: "zenlm", Made: true}, {Owner: "hanzo-templates", Made: true},
		{Owner: "acme", Made: false}, {Owner: "../hanzoai", Made: true},
	} {
		w := newWorld(t, "cloud")
		w.lie = &lie
		_, err := w.startIn(t, "acme", client.CodingStartIn{Repo: "cloud", Prompt: "x"})
		if err == nil {
			t.Fatalf("a workspace of %+v was accepted", lie)
		}
		if len(w.sudo) != 0 {
			t.Fatalf("%+v: the forge was asked %v", lie, w.sudo)
		}
	}
}

// A RUN ON THE ORG'S OWN MACHINE IS NOT REFUSED FOR ITS WORKSPACE: it is given a key
// of its own when it is offered, so it goes on to the machine, which this world does
// not answer.
func TestARoutedRunInAMadeWorkspaceGoesOnToItsMachine(t *testing.T) {
	w := newWorld(t)
	_, err := w.startIn(t, "acme", client.CodingStartIn{Repo: "site", Prompt: "x", TargetID: "tgt_0123456789abcdef0123456789abcdef"})
	if err == nil || !strings.Contains(err.Error(), "the machine tgt_0123456789abcdef0123456789abcdef is not available") {
		t.Fatalf("stopped with %v, want it to reach the machine's gate", err)
	}
}

// A RUN ON GITHUB MAKES NO WORKSPACE: it names a repository the forge holds no
// copy of.
func TestARunOnGitHubMakesNoWorkspace(t *testing.T) {
	w := newWorld(t)
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "sub-zoe"})
	_, _ = Start(ctx, "acme", "sub-zoe", client.CodingStartIn{Repo: "github.com/acme/api", Prompt: "x"}, func(string, ...any) {})
	for _, a := range w.asked {
		if strings.HasSuffix(a, "make=true") {
			t.Fatalf("a run on GitHub made a workspace: %v", w.asked)
		}
	}
	if len(w.orgs) != 1 {
		t.Fatalf("namespaces: %v", w.orgs)
	}
}

// THE PERSON AUTHORS THE COMMIT, the agent commits it — and only a proved
// address names anyone.
func TestAuthorLine(t *testing.T) {
	for _, c := range []struct {
		a    Author
		want string
	}{
		{Author{Name: "Zoe Q", Email: "zoe@acme.test"}, "Zoe Q <zoe@acme.test>"},
		{Author{Email: "zoe@acme.test"}, "zoe <zoe@acme.test>"},
		{Author{Name: "Eve <eve@x>\nSigned-off-by: x", Email: "zoe@acme.test"}, "Eve eve@x Signed-off-by: x <zoe@acme.test>"},
		{Author{Name: "Zoe", Email: "not an address"}, ""},
		{Author{Name: "Zoe", Email: "a@b>c"}, ""},
		{Author{Name: "Zo\u202ee\u200b Q", Email: "zoe@acme.test"}, "Zo e Q <zoe@acme.test>"},
		{Author{Name: "...", Email: "zoe@acme.test"}, "zoe <zoe@acme.test>"},
		{Author{Name: "'", Email: "'@acme.test"}, ""},
		{Author{Name: strings.Repeat("é", 120), Email: "zoe@acme.test"}, strings.Repeat("é", 100) + " <zoe@acme.test>"},
		{Author{}, ""},
	} {
		if got := c.a.line(); got != c.want {
			t.Errorf("%+v.line() = %q, want %q", c.a, got, c.want)
		}
	}
}

func TestAuthorIsOnlyAProvedAddress(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", socketDir(t))
	answer := client.Email{Address: "zoe@acme.test", Verified: true, Name: "Zoe"}
	iamApp := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	iamApp.Post("/iam/email", func(context.Context, *struct{}) (*client.Email, error) {
		e := answer
		return &e, nil
	}, zip.WithOperationID(client.IAMEmail))
	plane.Bind()
	go func() { _ = iamApp.Listen(zip.SocketPath("iam")) }()
	t.Cleanup(func() { _ = iamApp.Shutdown() })
	waitListening(t, "iam")

	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "sub-zoe"})
	if got := author(ctx); got != (Author{Name: "Zoe", Email: "zoe@acme.test"}) {
		t.Fatalf("a proved address authored as %+v", got)
	}
	answer.Verified = false
	if got := author(ctx); got != (Author{}) {
		t.Fatalf("an unproved address authored as %+v", got)
	}
	answer.Verified = true

	// Only a run on the forge in a workspace the platform made names its person:
	// the estate's runs and GitHub's keep the agent as author, as they always had.
	made := forge.Workspace{Owner: "acme", Made: true}
	if got := authorIn(ctx, made, true); got != (Author{Name: "Zoe", Email: "zoe@acme.test"}) {
		t.Fatalf("a run in a made workspace authored as %+v", got)
	}
	for name, c := range map[string]struct {
		ws      forge.Workspace
		onForge bool
	}{
		"the estate's run":             {estate("hanzo"), true},
		"a run on GitHub":              {made, false},
		"the estate's run on GitHub":   {estate("hanzo"), false},
		"an org with no workspace yet": {forge.Workspace{}, true},
	} {
		if got := authorIn(ctx, c.ws, c.onForge); got != (Author{}) {
			t.Fatalf("%s authored as %+v, want the agent", name, got)
		}
	}
}

// A RUN IN A MADE WORKSPACE IS READ AS THE MACHINE, in that workspace only:
// whoever the sessions app lets see the run reads its code with no forge
// account looked up, and a session naming any other namespace — the estate's,
// or one its org does not hold — has nothing to read.
func TestAMadeWorkspacesRunIsReadAsTheMachine(t *testing.T) {
	w := newWorld(t)
	w.book.m["acme"] = "acme"
	w.orgs["acme"] = "private"
	w.repos["acme/site"] = &wrepo{private: true}
	w.repos["hanzoai/site"] = &wrepo{private: true}
	sessions := map[string]agents.Session{
		"sess_mine":   {Kind: kindCoding, Org: "acme", Repo: "acme/site", Base: "main", Branch: runBranch},
		"sess_estate": {Kind: kindCoding, Org: "acme", Repo: "hanzoai/site", Base: "main", Branch: runBranch},
		"sess_other":  {Kind: kindCoding, Org: "globex", Repo: "acme/site", Base: "main", Branch: runBranch},
	}
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		if x, ok := sessions[id]; ok {
			return x, nil
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() { visibleRun = was })
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "sub-zoe"})

	r, err := read(ctx, "sess_mine")
	if err != nil {
		t.Fatalf("the org's own run was not readable: %v", err)
	}
	if r.owner != "acme" || r.name != "site" || r.as.Actor() != "" {
		t.Fatalf("read as %+v", r)
	}
	if got := w.sudoed(); len(got) != 0 {
		t.Fatalf("a made workspace's run was read as a person: %v", got)
	}
	for _, id := range []string{"sess_estate", "sess_other"} {
		if _, err := read(ctx, id); status(err) != http.StatusNotFound {
			t.Fatalf("%s: read answered %v, want not found", id, err)
		}
	}
	if got := w.addressed("hanzoai"); len(got) != 0 {
		t.Fatalf("the estate's namespace was read: %v", got)
	}
}

// A NEW ORG'S RUN ON A REPOSITORY ITS WORKSPACE DOES NOT HOLD is told exactly
// that, and where a repository on GitHub is named — never that runs are
// unavailable to it.
func TestANewOrgsRunOnAMissingRepositoryIsToldSo(t *testing.T) {
	w := newWorld(t)
	_, err := w.startIn(t, "acme", client.CodingStartIn{Repo: "api", Prompt: "Fix the tests"})
	if err == nil {
		t.Fatal("a run on a repository the workspace does not hold was admitted")
	}
	if !strings.Contains(err.Error(), `has no repository named or serving "api"`) ||
		!strings.Contains(err.Error(), "github.com/<owner>/<name>") {
		t.Fatalf("refused with %v, want it to name the missing repository and how GitHub's are named", err)
	}
	if w.orgs["acme"] != "private" {
		t.Fatalf("the org's workspace was not made on its first run: %v", w.orgs)
	}
	if len(w.keys) != 0 || len(w.made) != 0 {
		t.Fatalf("a refused run made %v and keyed %v", w.made, w.keys)
	}
}

package coding

// environment_test.go pins a run's half of its codebase's environment: what it
// asks the environment app, what it reads from KMS, how it applies the lot before
// its agent, and what a setup run hands back.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// dbURL is a secret shaped to break a careless shell: quotes, a newline, a
// command substitution and a backtick.
const dbURL = "postgres://u:pa'ss\"w$(rm -rf /)`id`@db/app\nsecond line"

// fakeEnvironment is the environment app's two plane ops as a rig sees them.
type fakeEnvironment struct {
	mu   sync.Mutex
	env  client.Prepared
	orgs []string
	kept []client.Suggestion
}

// servesEnvironment stands the environment app up beside a rig that is already
// serving, in the same runtime dir.
func servesEnvironment(t *testing.T, env client.Prepared) *fakeEnvironment {
	t.Helper()
	s := &fakeEnvironment{env: env}
	app := zip.New(zip.Config{AppName: "environment", DisableStartupMessage: true})
	app.Post("/environment/prepare", func(ctx context.Context, in *client.Codebase) (*client.Prepared, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.orgs = append(s.orgs, cloud.Who(ctx).Org)
		out := s.env
		if in.Setup {
			out.Install, out.Start = "", ""
		}
		return &out, nil
	}, zip.WithOperationID(client.EnvironmentPrepare))
	app.Post("/environment/suggest", func(ctx context.Context, in *client.Suggestion) (*client.Suggestion, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.orgs = append(s.orgs, cloud.Who(ctx).Org)
		s.kept = append(s.kept, *in)
		return in, nil
	}, zip.WithOperationID(client.EnvironmentSuggest))
	plane.Bind()
	go func() { _ = app.Listen(zip.SocketPath("environment")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitListening(t, "environment")
	return s
}

// at is the index of the first command containing want, or -1.
func at(p *pod, want string) int {
	for i, l := range p.lines() {
		if strings.Contains(l, want) {
			return i
		}
	}
	return -1
}

// A SETUP RUN is a plan put to one question. It is handed a key that can only
// read, carries the codebase's secrets and none of its scripts, is told to find
// the environment, delivers nothing — and its answer's environment block goes to
// the environment app as the codebase's proposal, acting for the run's org.
func TestSetup_AnswersAndSuggestsItsEnvironment(t *testing.T) {
	answer := "Installed with pnpm and ran the tests: 42 pass.\n\n" +
		"```environment\n{\"install\": \"pnpm install --frozen-lockfile\", \"start\": \"pnpm dev\", \"secrets\": [\"DATABASE_URL\"]}\n```"
	r := newRig()
	r.mirror = false
	r.secrets = map[string]string{"orgs/hanzo/environment/site/DATABASE_URL": dbURL}
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: answer + "\n"}
		}
		return client.Ran{}
	}
	r.serve(t)
	env := servesEnvironment(t, client.Prepared{
		Install: "make deps", Start: "make serve",
		Secrets: []client.Carried{{Name: "DATABASE_URL", Ref: "orgs/hanzo/environment/site/DATABASE_URL"}},
	})

	acc := r.startMode(t, client.CodingStartIn{Repo: "site", Prompt: "Set up this codebase's environment.", Mode: "setup"})

	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.reads, []bool{true}) {
		t.Fatalf("a setup run must be handed one read-only key, asked %v", r.reads)
	}
	if delivered(r) || len(r.pulls) != 0 || acc.Branch != "" {
		t.Fatalf("a setup run delivered or named a branch (%q):\n%s", acc.Branch, strings.Join(r.pod.lines(), "\n"))
	}
	if at(r.pod, "make deps") >= 0 || at(r.pod, "make serve") >= 0 {
		t.Fatalf("a setup run ran the scripts it is there to find:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	harness := at(r.pod, "--skip-git-repo-check")
	if harness < 0 || !strings.Contains(r.pod.fed[harness], "Set up this repository's development environment.") {
		t.Fatalf("the agent was not told to find the environment")
	}
	if !strings.Contains(r.pod.lines()[harness], envPath) || !slices.Contains(r.pod.blind[harness], dbURL) {
		t.Fatal("a setup run's agent did not carry the codebase's secrets, blinded")
	}
	env.mu.Lock()
	defer env.mu.Unlock()
	if len(env.kept) != 1 {
		t.Fatalf("the answer was suggested %d times", len(env.kept))
	}
	got := env.kept[0]
	if got.Repo != "site" || got.Session != acc.SessionID || got.Install != "pnpm install --frozen-lockfile" ||
		got.Start != "pnpm dev" || !slices.Equal(got.Secrets, []string{"DATABASE_URL"}) ||
		got.Note != "Installed with pnpm and ran the tests: 42 pass." {
		t.Fatalf("the suggestion was %+v", got)
	}
	for _, org := range env.orgs {
		if org != "hanzo" {
			t.Fatalf("the environment app was asked as %q, not the run's org", org)
		}
	}
	end := r.terminal(t)
	if end["status"] != "done" || end["mode"] != "setup" || end["environment"] == nil || end["plan"] == "" {
		t.Fatalf("a setup run must end done with its answer and its environment, got %v", end)
	}
}

// A BUILD applies its codebase's environment after the clone and before its agent:
// the secrets on stdin into a file, the install in the checkout, what the install
// left excluded from the commit, the start detached — and the agent sourced, told,
// and blinded against every value.
func TestBuild_AppliesTheEnvironmentBeforeItsAgent(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	req := aRun()
	req.Prepared = Prepared{
		Install: "pnpm install", Start: "pnpm dev",
		Env: map[string]string{"DATABASE_URL": dbURL}, Missing: []string{"STRIPE_KEY"},
	}
	var said []string
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, steps(&said)); err != nil {
		t.Fatalf("run: %v", err)
	}

	clone, carry, install := at(p, " clone "), at(p, "sh "+envPath), at(p, "pnpm install")
	exclude, start, harness := at(p, "--exclude-standard --directory"), at(p, "nohup"), at(p, "--skip-git-repo-check")
	if clone < 0 || !(clone < carry && carry < install && install < exclude && exclude < start && start < harness) {
		t.Fatalf("clone %d, secrets %d, install %d, exclude %d, start %d, agent %d — want that order:\n%s",
			clone, carry, install, exclude, start, harness, strings.Join(p.lines(), "\n"))
	}
	if !strings.Contains(p.lines()[carry], envPath) || !strings.Contains(p.fed[carry], "export DATABASE_URL=") {
		t.Fatalf("the secrets were not written to %s from stdin: %q", envPath, p.lines()[carry])
	}
	for i, l := range p.lines() {
		if strings.Contains(l, "hunter") || strings.Contains(l, "pa'ss") {
			t.Fatalf("a value reached argv, which is public in the pod: %q", l)
		}
		if i >= carry && !slices.Contains(p.blind[i], dbURL) {
			t.Fatalf("command %d ran with the secrets in reach and not blinded against them: %q", i, l)
		}
	}
	for _, i := range []int{install, start, harness} {
		if !strings.Contains(p.lines()[i], envPath) {
			t.Fatalf("command %d did not source the secrets: %q", i, p.lines()[i])
		}
	}
	task := p.fed[harness]
	if !strings.Contains(task, "install script has already run") || !strings.Contains(task, "STRIPE_KEY") || !strings.Contains(task, startLog) {
		t.Fatalf("the agent was not told what was done to its checkout: %q", task)
	}
	for _, want := range []string{"secrets not set: STRIPE_KEY error", "install installed ok", "secrets exported ok"} {
		if !slices.Contains(said, want) {
			t.Fatalf("the run did not say %q; it said %v", want, said)
		}
	}
}

// A FAILED INSTALL does not fail the run: it says so, and the agent is told —
// fixing what broke the install is often its task.
func TestBuild_AFailedInstallIsSaidAndTheAgentStillRuns(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "npm ci"):
			return client.Ran{ExitCode: 1, Stderr: "npm ERR! missing package-lock.json\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	req := aRun()
	req.Prepared = Prepared{Install: "npm ci"}
	var said []string
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, steps(&said)); err != nil {
		t.Fatalf("a failed install failed the run: %v", err)
	}
	if !slices.Contains(said, "install exit 1: npm ERR! missing package-lock.json error") {
		t.Fatalf("the failed install was not said: %v", said)
	}
	harness := at(p, "--skip-git-repo-check")
	if harness < 0 || !strings.Contains(p.fed[harness], "install script failed with exit 1") {
		t.Fatal("the agent did not run, or was not told the install failed")
	}
	if strings.Contains(p.lines()[harness], envPath) {
		t.Fatal("a run with no secrets sourced a file it never wrote")
	}
}

// A BARE CODEBASE is the run it always was: nothing written, nothing sourced.
func TestBuild_ABareCodebaseRunsAsBefore(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{envPath, "--exclude-standard --directory", "nohup"} {
		if i := at(p, want); i >= 0 {
			t.Fatalf("a bare codebase ran %q", p.lines()[i])
		}
	}
}

// The secrets file is read by a real shell, and each value comes back exactly —
// quotes, newlines and substitutions are bytes, never shell.
func TestExports_EachValueIsExactlyWhatTheShellReads(t *testing.T) {
	sh := kit.Tool(t, "sh")[0]
	env := map[string]string{"DATABASE_URL": dbURL, "PLAIN": "value", "QUOTE": "'", "EMPTY_ISH": " "}
	file := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(file, []byte(exports(env)), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, want := range env {
		out, err := exec.Command(sh, "-c", `. "$0" && printf '%s' "$`+name+`"`, file).Output()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(out) != want {
			t.Fatalf("%s came back %q, want %q", name, out, want)
		}
	}
}

// The answer's LAST environment block is the suggestion, and the prose before it
// the note; an answer with none, or a block that does not parse, suggests nothing.
func TestSuggestionOf(t *testing.T) {
	s, ok := suggestionOf("Tried yarn first.\n```environment\n{\"install\":\"yarn\"}\n```\nThen pnpm.\n" +
		"```environment\n{\"install\":\"pnpm install\",\"start\":\"pnpm dev\",\"secrets\":[\"A\",\"B\"]}\n```")
	if !ok || s.Install != "pnpm install" || s.Start != "pnpm dev" || !slices.Equal(s.Secrets, []string{"A", "B"}) ||
		!strings.HasPrefix(s.Note, "Tried yarn first.") || !strings.HasSuffix(s.Note, "Then pnpm.") {
		t.Fatalf("read %+v %v", s, ok)
	}
	for _, answer := range []string{"No block.", "```environment\nnot json\n```", "```environment\n{\"install\":\"x\"}"} {
		if _, ok := suggestionOf(answer); ok {
			t.Fatalf("%q suggested something", answer)
		}
	}
}

// A RUN ON A REPOSITORY GITHUB ALONE HOLDS has no forge name to key an
// environment by, so it asks for none and runs as it always did.
func TestStart_AGitHubHomeAsksForNoEnvironment(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	env := servesEnvironment(t, client.Prepared{Install: "make"})

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Mode: "plan"})

	env.mu.Lock()
	defer env.mu.Unlock()
	if len(env.orgs) != 0 {
		t.Fatalf("a GitHub-homed run asked the environment app %d times", len(env.orgs))
	}
	if end := r.terminal(t); end["status"] != "done" {
		t.Fatalf("the run did not finish: %v", end)
	}
}

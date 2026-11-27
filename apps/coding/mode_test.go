package coding

// mode_test.go pins what a plan run may do — read, and answer — and what a run's
// agent thinks with.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
)

// planned is the plan the harness prints, as dev exec prints its final message.
const planned = "1. Move the header into components/Header.tsx\n2. Test it in header.test.tsx"

// planPod answers like a checkout whose harness printed a plan and then, as a
// harness may, committed something anyway.
func planPod(r *rig) {
	tip := theSHA
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: planned + "\n", Stderr: "thinking…\n"}
		}
		return client.Ran{}
	}
}

// delivered is whether the run staged, committed or pushed anything.
func delivered(r *rig) bool {
	_, pushed := r.pod.did("HEAD:refs/heads/")
	return pushed || r.pod.gitVerb("commit") || r.pod.gitVerb("add")
}

// startMode runs Start for one input to its end, as the endpoint would.
func (r *rig) startMode(t *testing.T, in client.CodingStartIn) Accepted {
	t.Helper()
	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return acc
}

// terminal is the last status event the session carried, decoded.
func (r *rig) terminal(t *testing.T) map[string]any {
	t.Helper()
	var last map[string]any
	for _, e := range r.sessions.events {
		if e.kind != kindStatus {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(e.payload), &m) == nil {
			last = m
		}
	}
	return last
}

// A PLAN ON A REPOSITORY THE FORGE HOLDS is handed a key that can only read,
// clones, runs its agent told it is planning, and ends with the plan — no
// commit, no push, no proposal, no branch named anywhere.
func TestPlan_ReadsAndAnswersAndDeliversNothing(t *testing.T) {
	r := newRig()
	r.mirror = false // the forge is home: a deploy key
	planPod(r)
	r.serve(t)

	acc := r.startMode(t, client.CodingStartIn{Repo: "site", Prompt: "fix the header", Mode: "plan"})

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys != 1 || !slices.Equal(r.reads, []bool{true}) {
		t.Fatalf("a plan must be handed one read-only key: keys=%d reads=%v", r.keys, r.reads)
	}
	if delivered(r) {
		t.Fatalf("a plan staged, committed or pushed:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	if fed := harnessFed(r.pod); len(fed) == 0 || !strings.Contains(fed[0], "Plan only.") {
		t.Fatalf("the agent was not told it is planning: %q", fed)
	}
	if len(r.pulls) != 0 || len(r.todo.inputs) != 0 {
		t.Fatalf("a plan proposed something: pulls=%v board=%v", r.pulls, r.todo.inputs)
	}
	if acc.Branch != "" {
		t.Fatalf("a plan answered branch %q; it writes none", acc.Branch)
	}
	end := r.terminal(t)
	if end["status"] != "done" || end["mode"] != "plan" || end["plan"] != planned || end["changed"] != false {
		t.Fatalf("a plan must end done carrying its plan, got %v", end)
	}
	if n := len(r.sessions.closes); n != 1 || r.sessions.closes[0].status != statusDone {
		t.Fatalf("the plan's session did not close done: %+v", r.sessions.closes)
	}
	if got := r.sessions.tagged(); got.Mode != "plan" || got.Branch != "" {
		t.Fatalf("the session says %+v, want mode plan and no branch", got)
	}
}

// A PLAN ON A REPOSITORY GITHUB HOLDS asks GitHub for a token that can only read,
// and the same plan comes back.
func TestPlan_OnGitHubAsksForAReadToken(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Mode: "plan"})

	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.reads, []bool{true}) {
		t.Fatalf("a plan's admission must ask for a read token, asked %v", r.reads)
	}
	if delivered(r) || len(r.pulls) != 0 {
		t.Fatal("a plan on GitHub delivered or proposed")
	}
	if end := r.terminal(t); end["plan"] != planned {
		t.Fatalf("the plan did not come back: %v", end)
	}
}

// A BUILD is what a run was before there were modes: a write credential, a push,
// a proposal, and a session that says it built.
func TestBuild_IsTheDefaultAndWrites(t *testing.T) {
	for _, mode := range []string{"", "build"} {
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
		acc := r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Mode: mode, Pull: true})
		r.mu.Lock()
		if !slices.Equal(r.reads, []bool{false, true}) || !delivered(r) || len(r.pulls) != 1 || acc.Branch == "" {
			r.mu.Unlock()
			t.Fatalf("mode %q: reads=%v delivered=%v pulls=%d branch=%q", mode, r.reads, delivered(r), len(r.pulls), acc.Branch)
		}
		if got := r.sessions.tagged(); got.Mode != "build" {
			r.mu.Unlock()
			t.Fatalf("mode %q: the session says mode %q, want build", mode, got.Mode)
		}
		r.mu.Unlock()
	}
}

// WHAT A RUN CANNOT HONOUR IS REFUSED before anything is spent: a mode that is
// not one, a plan routed to a machine (it would push with the machine's own
// credential) or run by an interpreter, an effort outside the set, a model id
// shaped like a flag, and a model or effort named for a harness that does not
// take ours.
func TestCheckAsk(t *testing.T) {
	for _, c := range []struct {
		tool, target, mode, model, effort string
		ok                                bool
	}{
		{"", "", "", "", "", true},
		{"", "", "plan", "zen6-coder", "high", true},
		{"gen", "", "build", "enso-pro", "high", true},
		{"gen", "", "plan", "enso-ultra", "medium", true},
		{"gen", "tgt_1", "build", "enso-pro", "high", true},
		{"codex", "", "build", "zen6", "low", true},
		{"claude", "", "plan", "", "", true},
		{"claude", "tgt_1", "build", "zen6", "high", true}, // a machine runs its own harness with them
		{"", "", "draft", "", "", false},
		{"", "tgt_1", "plan", "", "", false},
		{"python", "", "plan", "", "", false},
		{"", "", "", "", "extreme", false},
		{"", "", "", "--config=x", "", false},
		{"claude", "", "", "zen6", "", false},
		{"node", "", "", "", "low", false},
	} {
		mode, _, _, err := checkAsk(c.tool, c.target, c.mode, c.model, c.effort)
		if (err == nil) != c.ok {
			t.Errorf("%+v: err=%v, want ok=%v", c, err, c.ok)
		}
		if err == nil && mode != modeOf(c.mode) {
			t.Errorf("%+v: mode %q, want %q", c, mode, modeOf(c.mode))
		}
	}
}

// A refusal reaches the caller before a session is opened or a credential minted.
func TestPlan_ToAMachineIsRefusedBeforeAnythingIsSpent(t *testing.T) {
	r := newRig()
	r.serve(t)
	_, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "site", Prompt: "fix it", Mode: "plan", TargetID: "tgt_1"})
	if err == nil || !strings.Contains(err.Error(), "plan") {
		t.Fatalf("a plan routed to a machine must be refused: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions.opened) != 0 || r.keys != 0 || len(r.reads) != 0 {
		t.Fatalf("a refused plan opened %d sessions and minted %d keys", len(r.sessions.opened), r.keys)
	}
}

// THE MODEL AND THE EFFORT REACH THE HARNESS, in the spelling each takes, and a
// run that names neither thinks with the platform's coding model.
func TestArgv_ModelAndEffort(t *testing.T) {
	dev := strings.Join(argvFor("", "", "zen6-coder", "high"), " ")
	if !strings.Contains(dev, "-m zen6-coder") || !strings.Contains(dev, `-c model_reasoning_effort="high"`) {
		t.Fatalf("dev: %s", dev)
	}
	if def := strings.Join(argvFor("dev", "", "", ""), " "); !strings.Contains(def, "-m "+codingModel) || strings.Contains(def, "reasoning") {
		t.Fatalf("dev with neither: %s", def)
	}
	codex := strings.Join(argvFor("codex", "", "enso", "low"), " ")
	if !strings.Contains(codex, "-m enso") || !strings.Contains(codex, `-c model_reasoning_effort="low"`) {
		t.Fatalf("codex: %s", codex)
	}
	if bare := strings.Join(argvFor("codex", "", "", ""), " "); strings.Contains(bare, " -m ") {
		t.Fatalf("codex with no model asked for keeps its own: %s", bare)
	}
	for _, argv := range [][]string{argvFor("", "", "m", "low"), argvFor("codex", "", "m", "low")} {
		if i := slices.Index(argv, "--"); i < 0 || argv[i+1] != "-" || i != len(argv)-2 {
			t.Fatalf("the task must be read from stdin, behind --: %v", argv)
		}
	}
}

// A RUN THAT NAMES A MODEL gets it; one that names none gets the platform's.
func TestRun_TheAskedModelReachesTheHarness(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan",
		Model: "zen6-coder-max", Effort: "high"})
	run, ok := r.pod.did("--skip-git-repo-check")
	if !ok || !strings.Contains(run, "-m zen6-coder-max") || !strings.Contains(run, `model_reasoning_effort="high"`) {
		t.Fatalf("the harness did not think with what was asked: %q", run)
	}
}

// A ROUTED RUN carries the model and the effort to the machine, which runs its
// own harness with them.
func TestRouted_CarriesModelAndEffort(t *testing.T) {
	sess := &fakeSessions{id: "sess_route1abc"}
	router := &fakeRouter{}
	d, _ := routedDispatcher(sess, &fakeRunner{}, router, &fakeGate{})
	req := routedReq()
	req.Model, req.Effort = "zen6-coder", "medium"
	if res := d.Run(context.Background(), req); !res.OK {
		t.Fatalf("routed: %s", res.Error)
	}
	if rr := router.runs[0]; rr.Model != "zen6-coder" || rr.Effort != "medium" {
		t.Fatalf("the routed run carries %q/%q", rr.Model, rr.Effort)
	}
}

// A PLAN STOPPED MID-TASK keeps nothing: it has no branch to keep its work on
// and a credential that could not push one, so nothing is committed or pushed,
// and the stop names no branch.
func TestPlan_StoppedKeepsNothingAndNamesNoBranch(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			cancel(fmt.Errorf("%w: by its owner", agents.ErrStopped))
			return client.Ran{ExitCode: 130}
		}
		return client.Ran{Stdout: theSHA + "\n"}
	}
	servePod(t, p)
	run := aRun()
	run.Plan = true
	res, err := sandboxRunner{}.Run(ctx, "acme", "u_1", run, nil)
	if err != nil || res.Changed || res.CommitSha != "" {
		t.Fatalf("a stopped plan keeps nothing: %+v %v", res, err)
	}
	if _, pushed := p.did("HEAD:refs/heads/"); pushed || p.gitVerb("commit") || p.gitVerb("add") {
		t.Fatalf("a stopped plan wrote something:\n%s", strings.Join(p.lines(), "\n"))
	}

	sess := &fakeSessions{id: "sess_abc123def456"}
	dctx, dcancel := context.WithCancelCause(context.Background())
	d, _ := dispatcherFor(sess, &fakePR{}, nil, true)
	d.Runner = interrupting{dcancel, fmt.Errorf("%w: by its owner", agents.ErrStopped)}
	req := baseReq()
	req.Mode = modePlan
	d.Run(dctx, req)
	var last map[string]any
	for _, e := range sess.events {
		var m map[string]any
		if e.kind == kindStatus && json.Unmarshal([]byte(e.payload), &m) == nil {
			last = m
		}
	}
	if last["status"] != statusStopped || last["branch"] != "" {
		t.Fatalf("a stopped plan must name no branch: %v", last)
	}
}

// A PLAN FITS THE EVENT THAT CARRIES IT, measured as that event encodes it: a
// plan of bytes JSON escapes six-fold still marshals under the cap, and keeps
// the end, where an agent says its answer. A short plan is itself.
func TestPlan_FitsItsEventEncoded(t *testing.T) {
	long := strings.Repeat("<\x01\xff&\"", 40<<10) + "the answer"
	p := planOf(long)
	b, err := json.Marshal(map[string]any{"plan": p})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > planCap+len(`{"plan":}`) {
		t.Fatalf("a %d-byte plan encodes to %d bytes, over the %d cap", len(long), len(b), planCap)
	}
	if !strings.HasPrefix(p, "…") || !strings.HasSuffix(p, "the answer") {
		t.Fatalf("a long plan must keep its end: …%q", p[len(p)-20:])
	}
	if got := planOf("  1. do it\n2. check it \n"); got != "1. do it\n2. check it" {
		t.Fatalf("a short plan must be itself: %q", got)
	}
}

// A PLAN IS SCRUBBED WHOLE, THEN CUT: a credential the cut would split leaves no
// half behind.
func TestPlan_IsScrubbedBeforeItIsCut(t *testing.T) {
	line := strings.Split(theKey, "\n")[2] // a secret line of the run's key
	stdout := line + strings.Repeat("a", planCap-len(`"…"`)-len(line)/2)
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			return client.Ran{Stdout: stdout}
		}
		return client.Ran{Stdout: theSHA + "\n"}
	}
	servePod(t, p)
	run := aRun()
	run.Plan = true
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if half := line[len(line)/2:]; strings.Contains(res.Plan, half[:16]) {
		t.Fatalf("a plan cut through the key kept half of it: …%q", res.Plan[:40])
	}
}

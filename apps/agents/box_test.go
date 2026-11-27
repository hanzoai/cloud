package agents

// box_test.go — a run's sandbox, against a fake apps/sandbox on the plane socket
// the real one answers on, so the lease and the end cross the wire they do in
// production.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// sandboxes is a fake apps/sandbox. A lease answers the way the real one does —
// the sandbox it names while that one runs, the same one given a pod again when it
// is parked, a fresh one otherwise — and every lease, park and end is recorded
// with the org it was made as. Its files live on the sandbox's disk: a park keeps
// them and an end takes them.
type sandboxes struct {
	mu      sync.Mutex
	leases  []client.LeaseIn
	orgs    []string
	users   []string     // who each lease, park and end was made as, in order
	callers []zip.Caller // each lease's caller, whole: who it is paid by
	ends    []string
	parks   []string
	resumed []string
	live    map[string]bool
	parked  map[string]bool
	files   map[string]map[string]string
	minted  int
	// gate, when set, holds every lease until it is closed.
	gate chan struct{}
}

func serveSandboxes(t *testing.T) *sandboxes {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", shortDir(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	p := &sandboxes{live: map[string]bool{}, parked: map[string]bool{}, files: map[string]map[string]string{}}
	app := zip.New(zip.Config{AppName: sandboxpeer.App, DisableStartupMessage: true})
	app.Post("/sandbox/lease", func(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
		p.mu.Lock()
		gate := p.gate
		p.mu.Unlock()
		if gate != nil {
			<-gate
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.leases = append(p.leases, *in)
		p.orgs = append(p.orgs, zip.CallerOf(ctx).Org)
		p.users = append(p.users, "lease:"+zip.CallerOf(ctx).User)
		p.callers = append(p.callers, zip.CallerOf(ctx))
		id := in.ID
		switch {
		case p.parked[id]:
			delete(p.parked, id)
			p.live[id] = true
			p.resumed = append(p.resumed, id)
		case !p.live[id]:
			p.minted++
			id = fmt.Sprintf("m_%d", p.minted)
			p.live[id] = true
			p.files[id] = map[string]string{}
		}
		return &client.Leased{ID: id, Class: in.Class, Status: "running", Workdir: "/work"}, nil
	}, zip.WithOperationID(client.SandboxLease))
	app.Post("/sandbox/park", func(ctx context.Context, in *client.ParkIn) (*struct{}, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.users = append(p.users, "park:"+zip.CallerOf(ctx).User)
		if !p.live[in.ID] && !p.parked[in.ID] {
			return nil, zip.ErrNotFound("sandbox not found")
		}
		delete(p.live, in.ID)
		p.parked[in.ID] = true
		p.parks = append(p.parks, in.ID)
		return &struct{}{}, nil
	}, zip.WithOperationID(client.SandboxPark))
	app.Post("/sandbox/end", func(ctx context.Context, in *client.EndIn) (*struct{}, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.users = append(p.users, "end:"+zip.CallerOf(ctx).User)
		if !p.live[in.ID] && !p.parked[in.ID] {
			return nil, zip.ErrNotFound("sandbox not found")
		}
		delete(p.live, in.ID)
		delete(p.parked, in.ID)
		delete(p.files, in.ID)
		p.ends = append(p.ends, in.ID)
		return &struct{}{}, nil
	}, zip.WithOperationID(client.SandboxEnd))

	path := zip.SocketPath(sandboxpeer.App)
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	accepts(t, path)
	return p
}

func (p *sandboxes) counts() (leases, minted, ends int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.leases), p.minted, len(p.ends)
}

func (p *sandboxes) ended(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.ends, id)
}

func (p *sandboxes) isParked(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.parked[id]
}

// disk is the tool plane over these sandboxes' files: a write keeps a file in the
// sandbox it names and a read answers it, while that sandbox has a pod. A run
// command waits until its run is over, which is a run somebody can stop.
func (p *sandboxes) disk(working chan<- struct{}) *sandboxTools {
	return &sandboxTools{do: func(ctx context.Context, name, args string) (string, error) {
		var in struct{ ID, Path, Data string }
		_ = json.Unmarshal([]byte(args), &in)
		if name == opRun {
			working <- struct{}{}
			<-ctx.Done()
			return "", context.Cause(ctx)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.live[in.ID] {
			return "", fmt.Errorf("sandbox %s has no pod", in.ID)
		}
		switch name {
		case opWrite:
			p.files[in.ID][in.Path] = in.Data
			return "wrote " + in.Path, nil
		case opRead:
			if body, ok := p.files[in.ID][in.Path]; ok {
				return body, nil
			}
			return "", fmt.Errorf("%s: no such file", in.Path)
		}
		return "ok", nil
	}}
}

// sandboxTools offers every tool a run declares and hands each call to do,
// recording the arguments it was given — which is where the box's id shows up.
type sandboxTools struct {
	mu    sync.Mutex
	calls []planeCall
	do    func(ctx context.Context, name, args string) (string, error)
}

func (p *sandboxTools) catalog(_ context.Context, _, _ string, want []string) []infer.ToolDef {
	out := make([]infer.ToolDef, 0, len(want))
	for _, n := range want {
		d := toolDef(n)
		d.Reads = strings.HasPrefix(n, "list_") // as a server marks a tool read-only
		out = append(out, d)
	}
	return out
}

func (p *sandboxTools) call(ctx context.Context, org, actor, name, args string) (string, error) {
	p.mu.Lock()
	p.calls = append(p.calls, planeCall{name: name, args: args, org: org, actor: actor})
	p.mu.Unlock()
	if p.do != nil {
		return p.do(ctx, name, args)
	}
	return "ok", nil
}

func (p *sandboxTools) made() []planeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]planeCall(nil), p.calls...)
}

// idIn reads the sandbox id out of a call's arguments, flat or enveloped.
func idIn(t *testing.T, args string) string {
	t.Helper()
	var v struct {
		ID    string `json:"id"`
		Input struct {
			ID string `json:"id"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		t.Fatalf("arguments %q: %v", args, err)
	}
	if v.ID != "" {
		return v.ID
	}
	return v.Input.ID
}

func toolCall(id, name, args string) infer.ToolCall {
	return infer.ToolCall{ID: id, Name: name, Arguments: args}
}

// boxer is an agent that declares the sandbox ops by name.
func boxer() Agent {
	a := mk("acme", "boxer")
	a.Tools = []string{opLease, opRun, opRead, opWrite, opStop, opEnd, "sandbox"}
	return a
}

func runService(t *testing.T, ai infer.Client) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{Log: luxlog.New("test"), State: state{stores: testStores(t), ai: ai}}
}

// The first sandbox call leases, and every later one — in either spelling — gets
// the same sandbox back. The lease is a dev scratch box on the run's own org, and
// it is given back when the run ends.
func TestARunLeasesOneSandboxAndReusesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		tools := &sandboxTools{}
		withPlane(t, tools)
		ai := &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{toolCall("c1", opRun, `{"command":"curl -s example.com"}`)}},
			{ToolCalls: []infer.ToolCall{toolCall("c2", "sandbox", `{"op":"read_sandbox_file","input":{"path":"out.txt"}}`)}},
			{Content: "done"},
		}}

		r, err := runAgent(runService(t, ai), context.Background(), boxer(), "look it up", nil, account.Org("acme"), "acme/u1", "", "")
		if err != nil || r.Status != "ok" {
			t.Fatalf("run: %+v %v", r, err)
		}
		calls := tools.made()
		if len(calls) != 2 {
			t.Fatalf("want two dispatches, got %+v", calls)
		}
		first, second := idIn(t, calls[0].args), idIn(t, calls[1].args)
		if first == "" || first != second {
			t.Fatalf("both calls must carry the run's one sandbox, got %q then %q", first, second)
		}
		if !strings.Contains(calls[0].args, "curl -s example.com") {
			t.Fatalf("the model's own arguments must survive, got %s", calls[0].args)
		}
		if _, minted, _ := sb.counts(); minted != 1 {
			t.Fatalf("the run minted %d sandboxes, want 1", minted)
		}
		sb.mu.Lock()
		lease, org := sb.leases[0], sb.orgs[0]
		sb.mu.Unlock()
		if lease.Class != boxClass || lease.Project != "" || lease.TTLSec != int(toolRunBudget/time.Second) {
			t.Fatalf("the lease must be a dev scratch box bounded by the tool loop, got %+v", lease)
		}
		if lease.Disk {
			t.Fatal("a run with no session is never resumed, so its box must not pay for a disk")
		}
		if org != "acme" {
			t.Fatalf("the lease was made as %q; it must be the run's org, which pays for it", org)
		}
		if !waitFor(func() bool { return sb.ended(first) }) {
			t.Fatalf("the run ended and its sandbox %s was not given back", first)
		}

	})
}

// A run that never reaches for a sandbox never waits for one and never pays for
// one: an ordinary tool leases nothing.
func TestARunThatNeverUsesASandboxLeasesNone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		withPlane(t, &sandboxTools{})
		a := boxer()
		a.Tools = append(a.Tools, "weather")
		ai := &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{toolCall("c1", "weather", `{"city":"Kyoto"}`)}},
			{Content: "Sunny."},
		}}

		if r, err := runAgent(runService(t, ai), context.Background(), a, "weather?", nil, account.Org("acme"), "acme/u1", "", ""); err != nil || r.Status != "ok" {
			t.Fatalf("run: %+v %v", r, err)
		}
		time.Sleep(50 * time.Millisecond) // the end is asynchronous; give a wrong one time to land
		if leases, _, ends := sb.counts(); leases != 0 || ends != 0 {
			t.Fatalf("a run with no sandbox call made %d leases and %d ends", leases, ends)
		}

	})
}

// A turn's ceiling is a deadline on its context, and the box ends at it even
// while the run is stuck in a call that ignores its context.
func TestADeadlineGivesTheSandboxBackFromAStuckRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		stuck, free := make(chan struct{}), make(chan struct{})
		withPlane(t, &sandboxTools{do: func(context.Context, string, string) (string, error) {
			close(stuck)
			<-free
			return "ok", nil
		}})
		ai := &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{toolCall("c1", opRun, `{"command":"sleep 600"}`)}},
			{Content: "done"},
		}}
		// The deadline passes once the run is stuck in its call, with the cause a timer
		// would give it. A timer started before the run raced the run's own way to the
		// call, and on a busy runner it expired first.
		ctx, expire := context.WithCancelCause(context.Background())
		defer expire(nil)
		svc := runService(t, ai)
		ran := make(chan struct{})
		go func() {
			defer close(ran)
			_, _ = runAgent(svc, ctx, boxer(), "go", nil, account.Org("acme"), "acme/u1", "", "")
		}()

		select {
		case <-stuck:
		case <-time.After(30 * time.Second):
			t.Fatal("the run never reached its sandbox call")
		}
		if sb.ended("m_1") {
			t.Fatal("the sandbox was given back before the deadline")
		}
		expire(context.DeadlineExceeded)
		if !waitFor(func() bool { return sb.ended("m_1") }) {
			t.Fatal("the deadline passed and the stuck run's sandbox was not given back")
		}
		close(free)
		<-ran

	})
}

// A run's box is its PERSON'S: apps/sandbox records the caller of a lease as the
// lessee and lets nobody else in the org act on it but an admin, and the model's
// tool calls carry the run's subject. So the lease, and the end that gives the box
// back, are made as that subject in the run's org — or the model could not reach
// its own box, and the run could not end it.
func TestABoxIsLeasedAndEndedAsTheRunsPerson(t *testing.T) {
	sb := serveSandboxes(t)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withBox(ctx, "acme", "alice", nil, nil)
	if _, err := boxCall(ctx, opRun, `{"command":"ls"}`); err != nil {
		t.Fatalf("run: %v", err)
	}
	cancel()
	if !waitFor(func() bool { return sb.ended("m_1") }) {
		t.Fatal("the box was not given back when its run ended")
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if want := []string{"lease:alice", "end:alice"}; !slices.Equal(sb.users, want) {
		t.Fatalf("the box's calls were made as %v, want %v", sb.users, want)
	}
	if !slices.Equal(sb.orgs, []string{"acme"}) {
		t.Fatalf("the box was leased in %v, want the run's org", sb.orgs)
	}
}

// A lease is never cancelled, so one that lands after its run has ended is given
// back the moment it lands.
func TestALeaseThatLandsLateIsGivenBack(t *testing.T) {
	sb := serveSandboxes(t)
	gate := make(chan struct{})
	sb.mu.Lock()
	sb.gate = gate
	sb.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	ctx = withBox(ctx, "acme", "", nil, nil)
	failed := make(chan error, 1)
	go func() {
		_, err := boxCall(ctx, opRun, `{"command":"ls"}`)
		failed <- err
	}()
	time.Sleep(20 * time.Millisecond) // the lease is under way, held at the gate
	cancel()
	if err := <-failed; err == nil {
		t.Fatal("a call whose run ended while it waited must fail")
	}
	close(gate)
	if !waitFor(func() bool { return sb.ended("m_1") }) {
		t.Fatal("a lease that landed after its run ended was left running")
	}
	if _, err := boxCall(ctx, opRun, `{}`); err == nil {
		t.Fatal("a run that has ended leased again")
	}
}

// A sandbox the call names is the caller's to keep; a lease inside a run is
// always the run's box; stop and end are handed the box only when there is one.
func TestWhatABoxWritesIntoACall(t *testing.T) {
	sb := serveSandboxes(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = withBox(ctx, "acme", "", nil, nil)
	hand := func(name, args string) string {
		t.Helper()
		out, err := boxCall(ctx, name, args)
		if err != nil {
			t.Fatalf("%s %s: %v", name, args, err)
		}
		return out
	}

	if got := hand(opStop, `{}`); got != `{}` {
		t.Fatalf("stop with nothing leased must lease nothing, got %s", got)
	}
	for _, args := range []string{`{"id":"m_theirs","command":"ls"}`, `{"id":"m_theirs"}`} {
		if got := hand(opRun, args); idIn(t, got) != "m_theirs" {
			t.Fatalf("a named sandbox must be kept, got %s", got)
		}
	}
	if got := hand(opLease, `{"id":"m_theirs"}`); idIn(t, got) != "m_theirs" {
		t.Fatalf("a lease that resumes a named sandbox must be left alone, got %s", got)
	}
	if leases, _, _ := sb.counts(); leases != 0 {
		t.Fatalf("%d leases before any call needed the box", leases)
	}
	box := idIn(t, hand(opLease, `{"class":"desktop"}`))
	if box != "m_1" {
		t.Fatalf("a lease that names no sandbox must be the run's box, got %q", box)
	}
	for _, c := range []struct{ name, args string }{
		{opStop, `{}`},
		{opEnd, `{}`},
		{"sandbox", `{"op":"write_sandbox_file","input":{"path":"a.txt","data":"aGk="}}`},
		{"sandbox", `{"op":"run_in_sandbox"}`},
	} {
		if got := idIn(t, hand(c.name, c.args)); got != box {
			t.Fatalf("%s %s: got sandbox %q, want the run's %q", c.name, c.args, got, box)
		}
	}
	if got := hand("weather", `{"op":"run_in_sandbox_please"}`); got != `{"op":"run_in_sandbox_please"}` {
		t.Fatalf("a call that is not a sandbox op must be untouched, got %s", got)
	}
	if _, minted, _ := sb.counts(); minted != 1 {
		t.Fatalf("one run minted %d sandboxes", minted)
	}
	cancel()
	if !waitFor(func() bool { return sb.ended(box) }) {
		t.Fatal("the run ended and kept its sandbox")
	}
}

// A model that reads up on a sandbox op is about to call it, so the pod starts
// while it writes the call; the call then finds that same sandbox.
func TestADescribeStartsTheLease(t *testing.T) {
	sb := serveSandboxes(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = withBox(ctx, "acme", "", nil, nil)

	if got, err := boxCall(ctx, client.Describe, `{"op":"stop_run"}`); err != nil || got != `{"op":"stop_run"}` {
		t.Fatalf("describe must pass through untouched: %s %v", got, err)
	}
	time.Sleep(20 * time.Millisecond)
	if leases, _, _ := sb.counts(); leases != 0 {
		t.Fatal("reading up on stop_run started a lease")
	}
	if _, err := boxCall(ctx, client.Describe, `{"op":"run_in_sandbox"}`); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool { _, minted, _ := sb.counts(); return minted == 1 }) {
		t.Fatal("reading up on run_in_sandbox did not start the lease")
	}
	out, err := boxCall(ctx, opRun, `{"command":"ls"}`)
	if err != nil || idIn(t, out) != "m_1" {
		t.Fatalf("the call must find the sandbox the describe started: %s %v", out, err)
	}
	if _, minted, _ := sb.counts(); minted != 1 {
		t.Fatalf("minted %d sandboxes, want the one", minted)
	}
	cancel()
	if !waitFor(func() bool { return sb.ended("m_1") }) {
		t.Fatal("the run ended and kept its sandbox")
	}
}

// A sandbox that is gone — the model ended it, or it was reaped idle — is
// replaced on the next call rather than handed back dead.
func TestAGoneSandboxIsReplaced(t *testing.T) {
	sb := serveSandboxes(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = withBox(ctx, "acme", "", nil, nil)

	first, err := boxCall(ctx, opRun, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	sb.mu.Lock()
	delete(sb.live, idIn(t, first))
	sb.mu.Unlock()
	second, err := boxCall(ctx, opRun, `{}`)
	if err != nil || idIn(t, second) == idIn(t, first) {
		t.Fatalf("a gone sandbox was handed back: %s then %s (%v)", first, second, err)
	}
	cancel()
	if !waitFor(func() bool { return sb.ended(idIn(t, second)) }) {
		t.Fatal("the run ended and kept its replacement sandbox")
	}
}

// An agent called as a tool runs inside its caller's run, so it works in the
// caller's sandbox and leaves giving it back to the caller.
func TestANestedRunSharesItsCallersSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		tools := &sandboxTools{}
		withPlane(t, tools)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx = withBox(ctx, "acme", "", nil, nil)
		held, err := boxCall(ctx, opRun, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		ai := &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{toolCall("c1", opRead, `{"path":"notes.md"}`)}},
			{Content: "read it"},
		}}

		if r, err := runAgent(runService(t, ai), ctx, boxer(), "read the notes", nil, account.Org("acme"), "acme/u1", "", ""); err != nil || r.Status != "ok" {
			t.Fatalf("nested run: %+v %v", r, err)
		}
		if got := idIn(t, tools.made()[0].args); got != idIn(t, held) {
			t.Fatalf("the nested run used sandbox %q, want its caller's %q", got, idIn(t, held))
		}
		time.Sleep(50 * time.Millisecond) // a wrong end is asynchronous; give it time to land
		if _, minted, ends := sb.counts(); minted != 1 || ends != 0 {
			t.Fatalf("minted %d and ended %d: the nested run must neither lease nor give back", minted, ends)
		}
		cancel()
		if !waitFor(func() bool { return sb.ended(idIn(t, held)) }) {
			t.Fatal("the caller ended and kept its sandbox")
		}

	})
}

// mountTurns mounts the agents service a turn runs in, with ai as its model and
// boxer seeded as the agent a turn asks for.
func mountTurns(t *testing.T, ai infer.Client) *Store {
	t.Helper()
	prev := mounted
	mounted = runService(t, ai)
	t.Cleanup(func() { mounted = prev })
	sto := storeOf(t, &mounted.State, "acme")
	if err := sto.Create(context.Background(), boxer()); err != nil {
		t.Fatal(err)
	}
	return sto
}

// A turn that completes gives its sandbox back.
func TestATurnThatCompletesGivesItsSandboxBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		withPlane(t, &sandboxTools{})
		sto := mountTurns(t, &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{toolCall("c1", opRun, `{"command":"python3 -c 'print(1)'"}`)}},
			{Content: "1"},
		}})

		st, err := startTurn(mounted, context.Background(), "acme", &client.RunOnBehalfIn{Subject: "u1", Ref: "boxer", Input: "run it"})
		if err != nil {
			t.Fatal(err)
		}
		if !waitFor(func() bool { return statusOf(t, sto, "acme", st.SessionID) == StatusDone }) {
			t.Fatalf("the turn did not complete: %s", statusOf(t, sto, "acme", st.SessionID))
		}
		if !waitFor(func() bool { return sb.ended("m_1") }) {
			t.Fatal("a completed turn kept its sandbox")
		}
		if sb.isParked("m_1") || len(sb.parks) != 0 {
			t.Fatal("a completed turn parked its sandbox; only a stop or a pause keeps one")
		}
		sb.mu.Lock()
		lease := sb.leases[0]
		sb.mu.Unlock()
		if !lease.Disk {
			t.Fatal("a turn's box can be parked, so it must have a disk that outlives its pod")
		}

	})
}

// Stopping a turn — `/hanzo stop`, the steering API, a logout — parks its sandbox
// at once, through the same cancel that stops the run: the pod goes and the disk
// stays, and the session records which sandbox it was, for a resume.
func TestStoppingATurnParksItsSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sb := serveSandboxes(t)
		working := make(chan struct{})
		withPlane(t, &sandboxTools{do: func(ctx context.Context, _, _ string) (string, error) {
			close(working)
			<-ctx.Done()
			return "", context.Cause(ctx)
		}})
		sto := mountTurns(t, &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{toolCall("c1", opRun, `{"command":"sleep 600"}`)}},
		}})

		st, err := startTurn(mounted, context.Background(), "acme", &client.RunOnBehalfIn{Subject: "u1", Ref: "boxer", Input: "wait"})
		if err != nil {
			t.Fatal(err)
		}
		<-working
		if n, err := StopSessions(context.Background(), "acme", SessionMatch{Actor: billingActor("acme", "u1"), Session: st.SessionID}); err != nil || n != 1 {
			t.Fatalf("stop: n=%d err=%v", n, err)
		}
		if !waitFor(func() bool { return sb.isParked("m_1") }) {
			t.Fatal("a stopped turn did not park its sandbox")
		}
		if sb.ended("m_1") {
			t.Fatal("a stopped turn ended its sandbox; a stop keeps it for a resume")
		}
		if s := statusOf(t, sto, "acme", st.SessionID); s != StatusStopped {
			t.Fatalf("the turn is %q, want stopped", s)
		}
		if x := reload(t, sto, st.SessionID); x.Box != "m_1" {
			t.Fatalf("the session records sandbox %q, want the parked m_1", x.Box)
		}

	})
}

// The ops the box answers for are ops apps/sandbox publishes, by the names it
// publishes them under. A rename there would leave every call here untouched and
// every model carrying ids again.
func TestTheBoxOpsAreTheSandboxOps(t *testing.T) {
	raw, err := os.ReadFile("../../plugin/sandbox/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	published := map[string]bool{}
	for _, ops := range doc.Paths {
		for _, raw := range ops {
			var op struct {
				OperationID string `json:"operationId"`
			}
			if json.Unmarshal(raw, &op) == nil && op.OperationID != "" {
				published[op.OperationID] = true
			}
		}
	}
	for op := range boxOps {
		if !published[op] {
			t.Errorf("%s is not an operation apps/sandbox publishes", op)
		}
	}
}

// A TURN NEVER INHERITS A CODING RUN'S SANDBOX. Session.Box names a turn's parked
// box and a coding run's sandbox alike, and the coding run's holds a write key and
// the codebase's secrets: a session of kind coding hands its box to no turn, and
// the turn leases one of its own. The control is a turn's own session, whose
// parked box is what its resume holds.
func TestATurnNeverStartsHoldingACodingRunsSandbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coding := withBox(ctx, "acme", "alice", nil, &live{x: Session{ID: "sess_c", Kind: kindCoding, Box: "m_coding"}})
	if got := boxFrom(coding).held(); got != "" {
		t.Fatalf("a turn on a coding session starts holding %q, the coding run's sandbox", got)
	}
	turn := withBox(ctx, "acme", "alice", nil, &live{x: Session{ID: "sess_t", Box: "m_parked"}})
	if got := boxFrom(turn).held(); got != "m_parked" {
		t.Fatalf("a resumed turn holds %q, want the box its session parked", got)
	}
}

package coding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/zap-proto/zip"
)

// THREE PODS, ONE RUN. A person's word to a run arrives at whichever pod took the
// request; the run is on the one that started it. Each pod has a fleet of its own;
// they share only the KV.

func eachKV(t *testing.T, f func(t *testing.T, pod func(id string) *coord.Coord)) {
	t.Helper()
	t.Run("in process", func(t *testing.T) { f(t, coordtest.Memory(t)) })
	t.Run("kv", func(t *testing.T) {
		pod := coordtest.Memory(t)
		f(t, pod)
	})
}

// podFleet is a pod's flights, answering for the others.
func podFleet(t *testing.T, c *coord.Coord) *fleet {
	t.Helper()
	fl := newFleet()
	fl.serve(c, nil)
	t.Cleanup(func() { fl.serve(nil, nil) })
	return fl
}

// flyOn registers a working run on fl, as Start does.
func flyOn(t *testing.T, fl *fleet, session, subject string, to client.Reply) (*flight, context.Context) {
	t.Helper()
	ctx, stop := context.WithCancelCause(context.Background())
	f := &flight{org: "acme", subject: subject, session: session, cancel: stop, done: make(chan struct{}), ctx: ctx,
		in: client.CodingStartIn{Repo: "api", Prompt: "add a health check", Reply: to}}
	fl.fly(f)
	t.Cleanup(func() { f.forget() })
	return f, ctx
}

// A word said on one pod redirects the run flying on another: the run is stopped
// with its work kept, for a new instruction, exactly as it is at home — and once.
func TestARedirectOnAnotherPodReachesTheRun(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		// The run never lands here, so its follow-up waits out followWait in the
		// background; the test ends first.
		flying, asked, third := podFleet(t, pod("cloud-0")), podFleet(t, pod("cloud-1")), podFleet(t, pod("cloud-2"))
		f, ctx := flyOn(t, flying, "sess_r", "u-acme", client.Reply{})

		in := client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_r", Text: "use the other endpoint"}
		if _, err := asked.follow(context.Background(), "acme", "u-acme", in, nil); err != nil {
			t.Fatalf("a redirect asked on another pod: %v", err)
		}
		cause := context.Cause(ctx)
		if !errors.Is(cause, agents.ErrStopped) || !strings.Contains(cause.Error(), "new instruction") {
			t.Fatalf("the run's cause is %v, want stopped for a new instruction", cause)
		}
		f.mu.Lock()
		claimed := f.claimed
		f.mu.Unlock()
		if !claimed {
			t.Fatal("the run was stopped and no follow-up took it")
		}

		// A second word, from a third pod, finds it taken.
		_, err := third.follow(context.Background(), "acme", "u-acme", in, nil)
		if code := codeOf(t, err); code != http.StatusConflict {
			t.Fatalf("a second redirect: %v, want 409 already followed up", err)
		}
	})
}

// The refusals a pod gives at home are the refusals it gives for another's run: a
// stranger's word, another org's, a run nobody flies.
func TestRefusalsCrossPodsIntact(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		flying, asked := podFleet(t, pod("cloud-0")), podFleet(t, pod("cloud-1"))
		to := client.Reply{Provider: "slack", Channel: "C1", Thread: "T1"}
		_, ctx := flyOn(t, flying, "sess_r", "u-acme", to)

		if _, err := asked.follow(context.Background(), "acme", "u-other", client.CodingSteerIn{Subject: "u-other", SessionID: "sess_r", Text: "no"}, nil); codeOf(t, err) != http.StatusForbidden {
			t.Fatalf("another person redirected the run: %v", err)
		}
		if _, err := asked.follow(context.Background(), "other-org", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_r", Text: "no"}, nil); codeOf(t, err) != http.StatusNotFound {
			t.Fatalf("another org redirected the run: %v", err)
		}
		start := time.Now()
		if _, err := asked.follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_nope", Text: "x"}, nil); codeOf(t, err) != http.StatusNotFound {
			t.Fatalf("a run nobody flies: %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("a run nobody flies took %v to be not found: it waited for a timer", took)
		}
		if ctx.Err() != nil {
			t.Fatalf("a refused word stopped the run: %v", context.Cause(ctx))
		}

		// The thread the run narrates into names it too, with no session id.
		if _, err := asked.follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", Reply: to, Text: "yes"}, nil); err != nil {
			t.Fatalf("a word in the run's thread, from another pod: %v", err)
		}
		if ctx.Err() == nil {
			t.Fatal("the word in the thread never reached the run")
		}
	})
}

// More time asked on one pod is given to the run flying on another, and the answer
// is the run's own: the time it now has.
func TestMoreTimeCrossesPods(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		flying, asked := podFleet(t, pod("cloud-0")), podFleet(t, pod("cloud-1"))
		f, _ := flyOn(t, flying, "sess_t", "u-acme", client.Reply{})
		f.clock = newClock(time.Now(), time.Minute, time.Hour, f.cancel)
		t.Cleanup(f.clock.end)

		got, err := asked.extend(context.Background(), "acme", "u-acme", &client.CodingExtendIn{Session: "sess_t", Seconds: 600})
		if err != nil {
			t.Fatalf("more time asked on another pod: %v", err)
		}
		if got.Seconds < 600 || got.Seconds > 700 {
			t.Fatalf("the run has %d seconds, want its minute and the 600 given", got.Seconds)
		}
		if _, err := asked.extend(context.Background(), "acme", "u-other", &client.CodingExtendIn{Session: "sess_t", Seconds: 60}); codeOf(t, err) != http.StatusForbidden {
			t.Fatalf("another person extended the run: %v", err)
		}
		if _, err := asked.extend(context.Background(), "acme", "u-acme", &client.CodingExtendIn{Session: "sess_nope", Seconds: 60}); codeOf(t, err) != http.StatusNotFound {
			t.Fatalf("a run nobody flies: %v", err)
		}
		if _, err := asked.extend(context.Background(), "acme", "u-acme", &client.CodingExtendIn{Session: "sess_t", Seconds: 0}); codeOf(t, err) != http.StatusBadRequest {
			t.Fatalf("no time asked for: %v", err)
		}
	})
}

// A deployment of one pod asks no one, and a run it does not fly is not found at once.
func TestAloneAPodAsksNoOne(t *testing.T) {
	fl := newFleet()
	start := time.Now()
	_, err := fl.follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_nope"}, nil)
	if codeOf(t, err) != http.StatusNotFound || time.Since(start) > time.Second {
		t.Fatalf("a run nobody flies: %v after %v", err, time.Since(start))
	}
}

// flyTicketed is a working run on fl whose harness holds ticket and whose kit carries
// server.
func flyTicketed(t *testing.T, fl *fleet, session, subject, tick, server string) *flight {
	t.Helper()
	f, ctx := flyOn(t, fl, session, subject, client.Reply{Provider: "slack", Channel: "C1"})
	f.ticket = ticketKey(tick)
	fl.Lock()
	fl.tickets[f.ticket] = f
	fl.Unlock()
	_ = ctx
	f.servers = map[string]bool{server: true}
	return f
}

// The harness in a run's sandbox calls cloud with the run's ticket and lands on any
// pod. The ticket opens its run wherever it flies, and nothing else.
func TestATicketOpensItsRunOnAnotherPod(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		flying, asked := podFleet(t, pod("cloud-0")), podFleet(t, pod("cloud-1"))
		mine := ticket()
		f := flyTicketed(t, flying, "sess_k", "u-acme", mine, "stripe-com")

		if ok, err := asked.holds(mine); err != nil || !ok {
			t.Fatalf("the ticket of a run on another pod: held=%v err=%v", ok, err)
		}
		for _, other := range []string{ticket(), "run_forged", ""} {
			if ok, err := asked.holds(other); err != nil || ok {
				t.Fatalf("%q names a run: held=%v err=%v", other, ok, err)
			}
		}
		start := time.Now()
		for _, c := range []struct {
			name, tick, server string
			want               error
		}{
			{"a forged ticket", "run_forged", "stripe-com", ErrNoTicket},
			{"a server the kit does not carry", mine, "evil", ErrNotCarried},
		} {
			if err := asked.carried(c.tick, c.server); !errors.Is(err, c.want) {
				t.Fatalf("%s: %v, want %v", c.name, err, c.want)
			}
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("refusing took %v: it waited for a timer", took)
		}
		if err := asked.carried(mine, "stripe-com"); err != nil {
			t.Fatalf("a server the kit carries: %v", err)
		}
		f.mu.Lock()
		f.paused = true
		f.mu.Unlock()
		if err := asked.carried(mine, "stripe-com"); err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("a paused run's ticket: %v, want it refused as paused", err)
		}
	})
}

// A relayed request goes to the pod flying the run and acts as the run's person in
// the run's org, whatever the request on the other pod carried.
func TestARelayCrossesPodsAsTheRunsPerson(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		flying, asked := podFleet(t, pod("cloud-0")), podFleet(t, pod("cloud-1"))
		mine := ticket()
		flyTicketed(t, flying, "sess_r", "sub-zoe", mine, "stripe-com")
		got := make(chan zip.Caller, 1)
		prev := relayTools
		relayTools = func(ctx context.Context, in *client.RelayIn) (*client.Relayed, error) {
			got <- zip.CallerOf(ctx)
			return &client.Relayed{Result: json.RawMessage(`{"tools":[]}`)}, nil
		}
		t.Cleanup(func() { relayTools = prev })

		req := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", User: "mallory", OrgAdmin: true})
		out, err := asked.relay(req, mine, "stripe-com", "tools/list", nil)
		if err != nil || string(out) != `{"tools":[]}` {
			t.Fatalf("relay across pods: %s %v", out, err)
		}
		if who := <-got; who.Org != "acme" || who.User != "sub-zoe" {
			t.Fatalf("the relay acted as %+v, want the run's person in the run's org", who)
		}
		if _, err := asked.relay(req, mine, "evil", "tools/list", nil); !errors.Is(err, ErrNotCarried) {
			t.Fatalf("a server the kit does not carry: %v", err)
		}
		if _, err := asked.relay(req, ticket(), "stripe-com", "tools/list", nil); !errors.Is(err, ErrNoTicket) {
			t.Fatalf("a forged ticket: %v", err)
		}
	})
}

// A question put on one pod to the person of a run on another is answered, and when
// the harness goes away before the answer the pod flying the run stops waiting.
func TestAnAskCrossesPodsAndIsCancelledWithTheHarness(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		flying, asked := podFleet(t, pod("cloud-0")), podFleet(t, pod("cloud-1"))
		mine := ticket()
		flyTicketed(t, flying, "sess_a", "u-acme", mine, "stripe-com")

		waiting := make(chan struct{})
		ended := make(chan error, 1)
		prev := askAgents
		askAgents = func(ctx context.Context, org, session string, to client.Reply, in client.AskIn, wait time.Duration) (string, error) {
			if in.Question == "now?" {
				return "answered", nil
			}
			close(waiting)
			<-ctx.Done()
			ended <- context.Cause(ctx)
			return "", ctx.Err()
		}
		t.Cleanup(func() { askAgents = prev })

		text, err := asked.ask(context.Background(), mine, client.AskIn{Question: "now?"})
		if err != nil || text != "answered" {
			t.Fatalf("an ask on another pod: %q %v", text, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := asked.ask(ctx, mine, client.AskIn{Question: "later?"})
			done <- err
		}()
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Fatal("the question never reached the person's pod")
		}
		cancel()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("the pod flying the run kept waiting on a harness that went away")
		}
		if err := <-done; err == nil {
			t.Fatal("an ask the harness abandoned returned an answer")
		}
		if _, err := asked.ask(context.Background(), ticket(), client.AskIn{Question: "x"}); !errors.Is(err, ErrNoTicket) {
			t.Fatalf("a forged ticket: %v", err)
		}
	})
}

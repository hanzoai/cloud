package coding

// relay_test.go — a run's ticket opens the org's MCP servers its kit carries, and
// nothing else: no ticket, a spent or forged one, another run's server and a
// paused run are all refused before the tool app is asked, and what is relayed
// acts as the run's person in the run's org whatever the request carried.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

func TestARunsTicketOpensTheServersItsKitCarries(t *testing.T) {
	mine, theirs := ticket(), ticket()
	f, ctx := fakeRun(t, "sess_r1", "sub-zoe", client.Reply{Provider: "slack", Channel: "C1"})
	f.ctx, f.ticket = ctx, ticketKey(mine)
	fly(f)
	g, gctx := fakeRun(t, "sess_r2", "sub-max", client.Reply{Provider: "slack", Channel: "C2"})
	g.ctx, g.ticket = gctx, ticketKey(theirs)
	fly(g)
	stow(mine, Kit{Servers: []Server{{Name: "stripe-com", ID: "stripe-com"}}})
	stow(theirs, Kit{Servers: []Server{{Name: "docs", ID: "docs"}}})

	var asked []zip.Caller
	var sent []client.RelayIn
	prev := relayTools
	relayTools = func(ctx context.Context, in *client.RelayIn) (*client.Relayed, error) {
		asked, sent = append(asked, zip.CallerOf(ctx)), append(sent, *in)
		return &client.Relayed{Result: json.RawMessage(`{"tools":[]}`)}, nil
	}
	t.Cleanup(func() { relayTools = prev })

	// The request that reaches the relay is the harness's: whatever it says about
	// who is calling is not what the run acts as.
	req := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", User: "mallory", OrgAdmin: true})
	for _, c := range []struct {
		name, ticket, server string
		want                 error
	}{
		{"no ticket", "", "stripe-com", ErrNoTicket},
		{"a forged ticket", "run_forged", "stripe-com", ErrNoTicket},
		{"another run's server", mine, "docs", ErrNotCarried},
		{"a server no kit carries", mine, "evil", ErrNotCarried},
	} {
		if _, err := Relay(req, c.ticket, c.server, "tools/list", nil); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, want %v", c.name, err, c.want)
		}
		if err := Carried(c.ticket, c.server); !errors.Is(err, c.want) {
			t.Fatalf("%s: Carried = %v, want %v", c.name, err, c.want)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("a refused request reached the tool app: %+v", sent)
	}

	out, err := Relay(req, mine, "stripe-com", "tools/call", json.RawMessage(`{"name":"charge","arguments":{}}`))
	if err != nil || string(out) != `{"tools":[]}` {
		t.Fatalf("the run's own server = %s %v", out, err)
	}
	if len(sent) != 1 || sent[0].Server != "stripe-com" || sent[0].Method != "tools/call" || string(sent[0].Params) != `{"name":"charge","arguments":{}}` {
		t.Fatalf("the tool app was asked %+v", sent)
	}
	if who := asked[0]; who.Org != "acme" || who.User != "sub-zoe" || who.OrgAdmin || who.Admin {
		t.Fatalf("the relay acted as %+v, want the run's person in the run's org and nothing more", who)
	}
	if _, err := Relay(req, theirs, "docs", "tools/list", nil); err != nil || asked[1].User != "sub-max" {
		t.Fatalf("the other run's own server: %v as %+v", err, asked[1])
	}

	// A paused run opens nothing, and one that ended is forgotten with its ticket.
	f.interrupt(agents.ErrPaused)
	f.land(Result{}, agents.ErrPaused)
	if err := Carried(mine, "stripe-com"); err == nil {
		t.Fatal("a paused run's ticket still opens its servers")
	}
	f.forget()
	if _, err := Relay(req, mine, "stripe-com", "tools/list", nil); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("an ended run's ticket: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("a refused request reached the tool app: %+v", sent[2:])
	}
}

// A run's relay is bounded twice: four requests in flight at once, and a burst
// of twenty then one a second. A request past either is refused before the tool
// app is asked — not queued behind the others — and a slot comes back when its
// request answers.
func TestARunsRelayIsBounded(t *testing.T) {
	tick := ticket()
	f, ctx := fakeRun(t, "sess_bound", "sub-zoe", client.Reply{})
	f.ctx, f.ticket = ctx, ticketKey(tick)
	fly(f)
	stow(tick, Kit{Servers: []Server{{Name: "docs", ID: "docs"}}})

	hold := make(chan struct{})
	entered := make(chan struct{}, relayCalls)
	var asked atomic.Int64
	prev := relayTools
	relayTools = func(ctx context.Context, in *client.RelayIn) (*client.Relayed, error) {
		asked.Add(1)
		entered <- struct{}{}
		<-hold
		return &client.Relayed{Result: json.RawMessage(`{}`)}, nil
	}
	t.Cleanup(func() { relayTools = prev })

	var wg sync.WaitGroup
	for range relayCalls {
		wg.Go(func() {
			if _, err := Relay(context.Background(), tick, "docs", "tools/list", nil); err != nil {
				t.Errorf("a request within the bound: %v", err)
			}
		})
	}
	for range relayCalls {
		<-entered
	}
	if _, err := Relay(context.Background(), tick, "docs", "tools/list", nil); !errors.Is(err, ErrRelayLimit) {
		t.Fatalf("a fifth request in flight: %v, want ErrRelayLimit", err)
	}
	close(hold)
	wg.Wait()
	if n := asked.Load(); n != relayCalls {
		t.Fatalf("the tool app was asked %d times, want %d: the refused request reached it", n, relayCalls)
	}

	// The burst: twenty in all, four of them already spent above.
	relayTools = func(ctx context.Context, in *client.RelayIn) (*client.Relayed, error) {
		asked.Add(1)
		return &client.Relayed{Result: json.RawMessage(`{}`)}, nil
	}
	for i := relayCalls; i < relayBurst; i++ {
		if _, err := Relay(context.Background(), tick, "docs", "tools/call", nil); err != nil {
			t.Fatalf("request %d of the burst: %v", i+1, err)
		}
	}
	if _, err := Relay(context.Background(), tick, "docs", "tools/call", nil); !errors.Is(err, ErrRelayLimit) {
		t.Fatalf("a request past the burst: %v, want ErrRelayLimit", err)
	}
	if n := asked.Load(); n != relayBurst {
		t.Fatalf("the tool app was asked %d times, want %d", n, relayBurst)
	}
	// Another run's bucket is its own.
	other := ticket()
	g, gctx := fakeRun(t, "sess_bound2", "sub-max", client.Reply{})
	g.ctx, g.ticket = gctx, ticketKey(other)
	fly(g)
	stow(other, Kit{Servers: []Server{{Name: "docs", ID: "docs"}}})
	if _, err := Relay(context.Background(), other, "docs", "tools/call", nil); err != nil {
		t.Fatalf("another run is not bounded by this one: %v", err)
	}
}

// TestARunsTicketEnforcesToolLevelPermissionsAndForwardsSessionID proves P1 #4:
// when a run's kit admits specific tools on an MCP server, a valid run ticket
// cannot invoke an unapproved or muted tool on that server, and permitted calls
// forward the run's session ID for per-run audit attribution.
func TestARunsTicketEnforcesToolLevelPermissionsAndForwardsSessionID(t *testing.T) {
	tick := ticket()
	f, ctx := fakeRun(t, "sess_tool_perm", "sub-zoe", client.Reply{})
	f.ctx, f.ticket = ctx, ticketKey(tick)
	fly(f)
	stow(tick, Kit{Servers: []Server{{
		Name:  "docs",
		ID:    "docs",
		Tools: []string{"echo", "search"},
	}}})

	var sent []client.RelayIn
	prev := relayTools
	relayTools = func(ctx context.Context, in *client.RelayIn) (*client.Relayed, error) {
		sent = append(sent, *in)
		return &client.Relayed{Result: json.RawMessage(`{"ok":true}`)}, nil
	}
	t.Cleanup(func() { relayTools = prev })

	// Calling an unapproved tool ("wipe") with a valid run ticket for "docs" is refused
	// before contacting the tool plane.
	for _, raw := range []string{
		`{"name":"wipe","arguments":{}}`,
		`{"name":"docs_wipe","arguments":{}}`,
		`{"name":"","arguments":{}}`,
		`not-json`,
	} {
		if _, err := Relay(context.Background(), tick, "docs", "tools/call", json.RawMessage(raw)); !errors.Is(err, ErrToolNotCarried) {
			t.Fatalf("unapproved tool payload %s = %v, want ErrToolNotCarried", raw, err)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("unapproved tool call reached the tool plane: %+v", sent)
	}

	// Calling an admitted tool ("echo") succeeds and carries the run session ID.
	out, err := Relay(context.Background(), tick, "docs", "tools/call", json.RawMessage(`{"name":"echo","arguments":{"q":"hi"}}`))
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("admitted tool call = %s, %v", out, err)
	}
	if len(sent) != 1 || sent[0].Session != "sess_tool_perm" {
		t.Fatalf("expected Session=sess_tool_perm on RelayIn, got %+v", sent)
	}
}
